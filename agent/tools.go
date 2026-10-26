package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
	acperm "github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// compactIntents retains scheduling metadata, direction, and lineage so the
// planner can compare queued work without fetching every node's full payload.
// parentsOf/yieldsOf come from the exploration edges in graph_overview and link
// upstream evidence to the facts/findings each intent produced.
func compactIntents(ns []*db.Node, parentsOf, yieldsOf map[int64][]int64) []map[string]any {
	out := make([]map[string]any, 0, len(ns))
	for _, n := range ns {
		var p map[string]any
		_ = json.Unmarshal(n.Payload, &p)
		m := map[string]any{"id": n.ID, "summary": p["summary"], "state": n.State,
			"priority": n.Priority, "created_at": n.CreatedAt}
		if n.Inherited {
			m["source_task_id"] = n.SourceTaskID
			m["inherited"] = true
		}
		// asset_ids is the structured "which assets this direction covers" signal for
		// dedup; fall back to legacy payload keys (target_ids plural, then target_id
		// single) so intents stored before the rename still surface their anchors.
		if tg, ok := p["asset_ids"]; ok && tg != nil {
			m["asset_ids"] = tg
		} else if tg, ok := p["target_ids"]; ok && tg != nil {
			m["asset_ids"] = tg
		} else if tg, ok := p["target_id"]; ok && tg != nil && tg != "" {
			m["asset_ids"] = []any{tg}
		}
		if ps := parentsOf[n.ID]; len(ps) > 0 {
			m["parents"] = ps // 上游：本意图派生自哪些节点（多个事实可共同产生一个意图）
		}
		if ys := yieldsOf[n.ID]; len(ys) > 0 {
			m["yields"] = ys // 下游：本意图产生了哪些事实/发现
		}
		out = append(out, m)
	}
	return out
}

// ToolSet exposes the PG-backed dual graph (asset + exploration) to an LLM agent.
// One ToolSet is created per planner/worker run; per-run signals live here.
type ToolSet struct {
	findingRecorder FindingRecorder
	as              *db.AssetStore   // asset store (optional; nil = asset tools not available)
	cs              *db.CompanyStore // company store (optional)
	ts              *db.ExplorationStore
	worker          string
	taskID          int64 // PG tasks.id; 0 when unknown (tests / orchestrator cross-task reads)
	// coverageDisabled mirrors tasks.coverage_enabled=false. Stored inverted so the
	// zero value (all existing ToolSet constructions) means ENABLED — matching the
	// DB default (true). When true: graphOverviewData drops the coverage block, the
	// auto-scope hook (insertAssets) is skipped, and add_task_scope/list_untested_assets
	// are filtered out of the agent's tool list. The scope field stays regardless.
	coverageDisabled bool
	// ownerNode is the exploration node that writes attach to: assets this run
	// touches get anchored to it as lineage/provenance (NOT visibility — the asset
	// graph is global and shared). Worker = its claimed intent; planner = begin root.
	ownerNode int64
	GoalMet   bool
	Reason    string
	writes    WriteCounts
	// killWork, if set, terminates a running work by intent id (engine callback,
	// wired by the planner). nil = the kill_work tool reports unavailable.
	killWork func(intentID int64) error
	// steerWork, if set, queues a mid-run course-correction for the work running an
	// intent id (engine callback, wired by the planner): the worker injects it before
	// its next tool call and re-plans, without being killed. nil = tool unavailable.
	steerWork func(intentID int64, msg string) error
	// enrich, if set, receives async auto-completion triggers (DNS resolve for a
	// domain, HTTP probe for a site). nil = no engine enrichment.
	enrich EnrichTrigger
	// notify, if set, wakes the task's planner after a graph change that should be
	// re-planned promptly (currently: a new hint). nil = no wake (the hint is still
	// stored and read on the next round triggered by other events). debounced.
	notify func()
	// notifyFinding, if set, wakes the task's planner when this run reports a finding,
	// carrying (intentID, summary) so the round can spell out which intent found what.
	// Wired for workers; nil elsewhere → falls back to notify (bare wake).
	notifyFinding func(intentID int64, summary string)
	// resumeTask, if set, revives the task after a graph change that should make a
	// stopped task run again (currently: set_goals adds a goal). It flips a terminal/
	// paused task back to running and (re)starts the engine loops — a plain notify()
	// can't, because the planner's terminal gate swallows wakes. Wired ONLY for the
	// main agent (human steering); nil for the goals decomposer and workers.
	resumeTask func()
	// notifyGoal, if set, wakes the planner AND records ONE "人新增了 N 个目标：…" trigger
	// for a whole set_goals call (batch-aware — one call, one trigger, not one per goal)
	// so the next round spells out the added goals (instead of the planner having to
	// spot new open goals in the overview). Wired ONLY for the main agent; nil for the
	// goals decomposer (round-0 has no running planner to inform) and workers → those
	// fall back to the bare notify.
	notifyGoal func(texts []string)
	// notifyHint, if set, wakes the planner AND records ONE "人新增了 N 条战略提示：…"
	// trigger for a whole add_hint call (batch-aware — one call, one trigger) so the next
	// round is told the round was fired by a new hint and spells the hint out, instead of
	// the planner having to spot it folded into the graph overview. Wired for the main
	// agent + cross-task orchestration; nil elsewhere → falls back to the bare notify.
	notifyHint func(texts []string)
}

// SetNotifyGoal wires the goal-add trigger callback (see ToolSet.notifyGoal). Set only
// by the main-agent chat, so runtime-added goals are announced to the planner by name.
func (t *ToolSet) SetNotifyGoal(fn func([]string)) { t.notifyGoal = fn }

// SetNotifyHint wires the hint-add trigger callback (see ToolSet.notifyHint). Set by
// the main-agent chat and cross-task orchestration, so a runtime-added hint fires a
// planner round announced by name instead of a bare wake.
func (t *ToolSet) SetNotifyHint(fn func([]string)) { t.notifyHint = fn }

// SetResumeTask wires the task-revive callback (see ToolSet.resumeTask). Set only by
// the main-agent chat, so runtime-added goals can pull a finished task back to running.
func (t *ToolSet) SetResumeTask(fn func()) { t.resumeTask = fn }

// SetNotify wires the planner-wake callback (see ToolSet.notify). Set by callers
// that hold the task handle (main-agent chat, cross-task orchestration).
func (t *ToolSet) SetNotify(fn func()) { t.notify = fn }

// SetNotifyFinding wires the finding-wake callback (see ToolSet.notifyFinding).
func (t *ToolSet) SetNotifyFinding(fn func(int64, string)) { t.notifyFinding = fn }

// EnrichTrigger is the enrichment engine seen from the tool layer (see package
// enrich). Kept as an interface here to avoid coupling agent → enrich.
type EnrichTrigger interface {
	ResolveDomain(id int64, host string)
	ProbeSite(id int64, url string)
}

// WriteCounts breaks down what a worker persisted this run, by node kind, so the
// engine can log an accurate "wrote back" summary instead of lumping assets and
// findings under "facts" (record_fact → Facts, insert_assets → Assets,
// report_finding → Findings; each element of a batch counts once).
type WriteCounts struct {
	Facts            int
	Assets           int
	Findings         int
	ObservedAssets   int
	ObservationFacts int
}

// Total counts explicit result writes. Automatically recorded HTTP observations
// do not replace the worker's responsibility to record a conclusion/evidence.
func (w WriteCounts) Total() int { return w.Facts + w.Assets + w.Findings }

// String renders the per-kind breakdown for logs, e.g. "事实1 资产25 漏洞0".
func (w WriteCounts) String() string {
	text := fmt.Sprintf("사실 %d 자산 %d 취약점 %d", w.Facts, w.Assets, w.Findings)
	if w.ObservedAssets > 0 || w.ObservationFacts > 0 {
		text += fmt.Sprintf(" / 자동 HTTP 관찰: 자산 %d, 관찰 사실 %d", w.ObservedAssets, w.ObservationFacts)
	}
	return text
}

// Writes reports what this run wrote back, split by node kind (so the engine can
// tell "explored but persisted nothing" apart from a completed intent, and log an
// honest breakdown instead of calling assets/findings "facts").
func (t *ToolSet) Writes() WriteCounts { return t.writes }

func NewToolSet(ts *db.ExplorationStore, worker string) *ToolSet {
	return &ToolSet{ts: ts, worker: worker}
}

// SetTaskID sets the PG task id on this ToolSet so that report_finding can
// dual-write to the standalone findings table (which survives task deletion).
func (t *ToolSet) SetTaskID(id int64) { t.taskID = id }

// SetCoverageEnabled records whether this task has the asset-coverage feature on
// (default enabled). Passing false makes graphOverviewData omit the coverage block
// and DropCoverageTools filter the two coverage-only tools out of the agent's tool
// list. It does NOT stop scope accumulation: insertAssets' auto-scope hook runs
// either way, because task_scope is the task's range boundary (the filter basis for
// asset queries), not merely a coverage denominator.
func (t *ToolSet) SetCoverageEnabled(enabled bool) { t.coverageDisabled = !enabled }

// CoverageDisabled reports whether the coverage feature is off for this task.
func (t *ToolSet) CoverageDisabled() bool { return t.coverageDisabled }

// coverageOnlyTools are the LLM tools that only make sense when asset coverage is
// on. When the feature is off they are filtered out of the agent's tool list so
// they neither pollute the prompt nor let the model build a disabled denominator.
// add_task_scope is deliberately NOT here: task_scope is the task's range boundary
// (the filter basis for asset queries), not merely a coverage denominator, so the
// agents that own范围定义 keep it either way — in lockstep with insertAssets'
// auto-scope hook, which also runs regardless of the switch.
var coverageOnlyTools = map[string]bool{"list_untested_assets": true}

// DropCoverageTools returns tools with the coverage-only ones removed when this
// task has the feature disabled; otherwise it returns tools unchanged.
func (t *ToolSet) DropCoverageTools(tools []actool.CoreTool) []actool.CoreTool {
	if !t.coverageDisabled {
		return tools
	}
	out := tools[:0:0]
	for _, tool := range tools {
		if coverageOnlyTools[tool.Name()] {
			continue
		}
		out = append(out, tool)
	}
	return out
}

// Cross-task reuse: exported accessors returning the per-task tool logic bound to
// THIS ToolSet's store. Host-side orchestration tools build a ToolSet for an
// arbitrary task, then Call these — so cross-task reads/hint reuse the exact
// same logic as the in-task tools. (readTool ignores ToolContext, so Call(…,nil)
// is safe; add_hint is a writeTool but also doesn't deref the context here.)
func (t *ToolSet) GraphOverviewTool() actool.CoreTool      { return t.graphOverview() }
func (t *ToolSet) ListFindingsTool() actool.CoreTool       { return t.listFindings() }
func (t *ToolSet) GetWorkerTraceTool() actool.CoreTool     { return t.getWorkerTrace() }
func (t *ToolSet) ListWorkerTracesTool() actool.CoreTool   { return t.listWorkerTraces() }
func (t *ToolSet) SearchWorkerTracesTool() actool.CoreTool { return t.searchAllWorkerTraces() }
func (t *ToolSet) NodeDetailTool() actool.CoreTool         { return t.nodeDetail() }
func (t *ToolSet) AddHintTool() actool.CoreTool            { return t.addHint() }

// SetEnrich wires the async enrichment engine (DNS/HTTP auto-completion).
func (t *ToolSet) SetEnrich(e EnrichTrigger) { t.enrich = e }

// SetOwnerNode sets the exploration node that writes anchor to (worker: its
// intent node; planner/main: the begin root). Assets created/referenced while
// ownerNode is set are anchored to it as lineage (not visibility).
func (t *ToolSet) SetOwnerNode(id int64) { t.ownerNode = id }

// pid parses an id that may arrive as a JSON number or string ("" / 0 → 0).
func pid(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		return v
	}
	return 0
}

// pidList parses a list of ids (number|string), dropping zeros/invalids.
func pidList(raw []json.RawMessage) []int64 {
	var out []int64
	for _, r := range raw {
		if v := pid(r); v > 0 {
			out = append(out, v)
		}
	}
	return out
}

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}
func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func intp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func idp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func readTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		ReadOnly:    func(json.RawMessage) bool { return true },
		Concurrent:  func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, acperm.Context) acperm.Decision { return acperm.Allowed() },
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

func writeTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		Permissions: func(context.Context, json.RawMessage, acperm.Context) acperm.Decision { return acperm.Allowed() },
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

// readExpTool / writeExpTool build a domain tool whose handler dereferences the
// task-bound ExplorationStore. Two ToolSets carry a nil store: the catalog's
// seed-only shell (never called) and the server-level one behind buildDomainReg,
// which the tools table can bind to ANY agent — including ones that never run
// inside a task (auto/pentest/reporter/自定义 agent/旁路提问). Refusing there
// keeps a mis-bound tool a bad tool call; without the guard it was a nil deref,
// and tool handlers run on the harness's own goroutine, so the panic is out of
// reach of every recover() in the server and kills the whole process.
func (t *ToolSet) readExpTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return readTool(name, desc, schema, t.needExploration(name, run))
}

func (t *ToolSet) writeExpTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return writeTool(name, desc, schema, t.needExploration(name, run))
}

// needExploration wraps a handler so it only runs with an exploration store.
// Tools that degrade more usefully than "unavailable" (report_finding points at
// add_task_hint, set_goals/set_constraints at the task itself) keep their own
// bespoke guard instead.
func (t *ToolSet) needExploration(name string, run func(context.Context, json.RawMessage) (actool.Result, error)) func(context.Context, json.RawMessage) (actool.Result, error) {
	return func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
		if t.ts == nil {
			return actool.Errorf(name + " 작업 컨텍스트(탐색 그래프)가 필요합니다. 현재 에이전트는 작업 안에서 실행 중이 아니므로 탐색 그래프를 가져올 수 없어 이 도구를 사용할 수 없습니다. 작업 안에서 사용하거나 task_id를 받는 작업 간 조회 도구(get_task_node_detail / list_task_findings / get_task_graph 등)를 사용하세요."), nil
		}
		return run(ctx, in)
	}
}

func jsonResult(v any) (actool.Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	return actool.Text(string(b)), nil
}

// --- read tools (planner + worker) ---

func (t *ToolSet) graphOverview() actool.CoreTool {
	return t.readExpTool("graph_overview",
		"(탐색 연결 그래프) 탐색 현황 요약: 자산 수, 인터페이스 없는 사이트, frontier, 발견, hints(사용자/메인 에이전트의 전략 힌트이며 의도 생성 시 반영 필수). 계획 시 먼저 호출하세요.",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			return jsonResult(t.graphOverviewData())
		})
}

// graphOverviewData computes the distilled situational snapshot shared by the
// graph_overview tool and the planner's wake-up prompt (which pre-injects it so
// the model needn't spend a turn calling the tool — every plan round starts with
// an empty context and always needs this first).
func isHTTPObservationFact(n *db.Node) bool {
	if n == nil || n.Kind != db.KindFact {
		return false
	}
	var payload struct {
		Source string `json:"observation_source"`
	}
	return json.Unmarshal(n.Payload, &payload) == nil && payload.Source == "http_response"
}

func (t *ToolSet) graphOverviewData() map[string]any {
	out := map[string]any{}
	// goals summary folded in so the planner needn't call list_goals each round.
	goals, _ := t.ts.ListByKind(db.KindGoal, 100)
	gsum := make([]map[string]any, 0, len(goals))
	for _, g := range goals {
		var p map[string]any
		_ = json.Unmarshal(g.Payload, &p)
		gsum = append(gsum, map[string]any{"id": g.ID, "state": g.State, "text": p["text"]})
	}
	out["goals"] = gsum
	// hints: 人类/主 agent 通过 add_hint 挂上图的战略提示；folded in so the
	// planner reads them every round when generating intents (否则只写不读).
	hints, _ := t.ts.ListByKind(db.KindHint, 50)
	hsum := make([]map[string]any, 0, len(hints))
	for _, h := range hints {
		var p map[string]any
		_ = json.Unmarshal(h.Payload, &p)
		hint := map[string]any{"id": h.ID, "state": h.State, "text": p["text"]}
		if findingTrafficBindingEnabled() && p["traffic_refs"] != nil {
			hint["traffic_refs"] = p["traffic_refs"]
		}
		hsum = append(hsum, hint)
	}
	out["hints"] = hsum
	// lineage from the exploration edges: an intent's parents (what it
	// derived_from — possibly several facts combined) and its yields (the
	// facts/findings it produced). factFrom maps a fact → the intent that
	// produced it. This is the relationship layer the flat lists lacked.
	edges, _ := t.ts.Edges(5000)
	parentsOf := map[int64][]int64{}
	yieldsOf := map[int64][]int64{}
	factFrom := map[int64]int64{}
	for _, e := range edges {
		switch e.Rel {
		case db.RelDerivedFrom, db.RelSpawns: // upstream: derived_from (fact/finding/intent→intent) or spawns (origin fact→goal, legacy begin→intent)
			parentsOf[e.To] = append(parentsOf[e.To], e.From)
		case db.RelYields: // intent --yields--> fact/finding
			yieldsOf[e.From] = append(yieldsOf[e.From], e.To)
			factFrom[e.To] = e.From
		}
	}
	// A single source snapshot validates both complete digest coverage and hot
	// revival; changed or still-active originals remain visible in the overview.
	hidden := hiddenMembersFor(t.ts)
	const openIntentsCap = 30
	fr, _ := t.ts.Frontier(openIntentsCap) // priority DESC, id ASC —— 优先级最高的前 N 条；真实总数见 frontier_open
	out["open_intents"] = compactIntents(fr, parentsOf, yieldsOf)
	all, _ := t.ts.ListByKind(db.KindIntent, 300)
	var running, recentDone []*db.Node
	for _, n := range all {
		switch n.State {
		case "running":
			running = append(running, n)
		case "done", "blocked", "exhausted":
			if hidden(n.ID) {
				continue // in a cold_digest and still cold — shown via cold_digests (§6.2)
			}
			recentDone = append(recentDone, n) // 最新在前（all 按 id 降序）；折叠的已剔除，输出时截最新 N
		}
	}
	out["running_intents"] = compactIntents(running, parentsOf, yieldsOf)
	// done_intents_total：已结束意图（done/blocked/exhausted）总数，与 recent_done_intents
	// 平行命名——后者只是它的最新窗口截断视图。两键并排即自描述："看到的是 N/总数"，
	// 让 planner 去重时别把"没显示"当成"没派过"，无需在提示词里另行解释。
	if dt, err := t.ts.CountFinishedIntents(); err == nil {
		out["done_intents_total"] = dt
	}
	// frontier_open：开放意图真实总数（open_intents 只是其中优先级最高的前 N 条截断视图）。
	if fo, err := t.ts.CountOpenIntents(); err == nil {
		out["frontier_open"] = fo
	} else {
		out["frontier_open"] = len(fr)
	}
	// findings (confirmed vulns) and facts (worker exploration results) are
	// now distinct node kinds. recent_facts surfaces fact summaries (esp.
	// negative results) so the planner sees them in one call; full content
	// via node_detail(id).
	vulnNodes, _ := t.ts.ListByKind(db.KindFinding, 1000)
	factNodes, analysisFacts, observationFacts, _ := t.ts.AnalysisFactOverview(1000) // newest analysis facts first
	out["findings_total"] = len(vulnNodes)                                           // 确认漏洞总数（目标判定看它）；明细见 finding_list（最新一窗）
	out["facts"] = analysisFacts
	out["http_observation_facts"] = observationFacts
	// findings 是任务里最高价值的产物 → 概览带最新一窗（≤10 条，vulnNodes 已按 id 降序即最新在前），
	// 让 planner 每轮判目标时一眼看到最近确认的漏洞；全量/更早的用 list_findings 取。
	// 每条只留 {id, summary, from_intent?}：from_intent 是产生本漏洞的意图。
	// evidence/assets/vulnclass/severity/state 等仍可用 list_findings / node_detail(id) 取。
	const findingListCap = 10
	findingList := make([]map[string]any, 0, findingListCap)
	for _, n := range vulnNodes {
		if len(findingList) >= findingListCap {
			break
		}
		var fp map[string]any
		_ = json.Unmarshal(n.Payload, &fp)
		m := map[string]any{"id": n.ID, "summary": fp["summary"]}
		if from := factFrom[n.ID]; from > 0 {
			m["from_intent"] = from // 本漏洞由哪个意图产生
		}
		findingList = append(findingList, m)
	}
	out["finding_list"] = findingList
	// recent_facts：非折叠事实里最新的一窗（≤N，factNodes 按 id 降序即最新在前）。已折进
	// digest 且仍冷的（hidden）走 cold_digests，不在此重复。每条 {id, summary, from_intent?,
	// confidence?}；evidence 等详情用 node_detail(id)。更早的用 list_facts 翻。
	const recentFactsCap = 20
	recentFacts := make([]map[string]any, 0, recentFactsCap)
	for _, n := range factNodes {
		if len(recentFacts) >= recentFactsCap {
			break
		}
		if hidden(n.ID) {
			continue // 已折进 digest 且仍冷 —— 见 cold_digests
		}
		m := compactNode(n)
		if from := factFrom[n.ID]; from > 0 {
			m["from_intent"] = from // 本事实由哪个意图产生
		}
		// confidence 带进概览：让规划者一眼看出哪条结论只是 inferred（尤其否定结论
		// 别当铁案）；evidence 较长，留给 node_detail(id)。
		var fp map[string]any
		if json.Unmarshal(n.Payload, &fp) == nil {
			if c, ok := fp["confidence"].(string); ok && c != "" {
				m["confidence"] = c
			}
		}
		recentFacts = append(recentFacts, m)
	}
	out["recent_facts"] = recentFacts
	// recent_done_intents：非折叠的已结束意图里最新的一窗（≤N，recentDone 已按 id 降序）。
	// 更早的看 done_intents_total 计数 + node_detail(id)。
	const recentDoneCap = 12
	if len(recentDone) > recentDoneCap {
		recentDone = recentDone[:recentDoneCap]
	}
	out["recent_done_intents"] = compactIntents(recentDone, parentsOf, yieldsOf)
	// cold-digest §6.1: 折叠冷区的 digest body，按最新成员时间降序取前 N；被截的更旧 digest
	// 只给裸 id（仍可 expand_digest 展开），避免冷区唯一出口被无限拉长。
	const coldDigestsCap = 15
	if cds, more := coldDigestsRecent(t.ts, coldDigestsCap); len(cds) > 0 {
		out["cold_digests"] = cds // [{id, body, member_count}] —— 直接读 body (§6.1)
		if len(more) > 0 {
			out["cold_digests_more"] = more // 被截断的更旧 digest 的 id；用 expand_digest(id) 展开
		}
	}
	// the original task (root) so the planner always has it, not just the
	// decomposed goals.
	if description, goal, err := t.ts.Root(); err == nil {
		root := map[string]any{"description": description, "goal": goal}
		if id, _ := t.ts.OriginFactID(); id > 0 {
			root["id"] = id
		}
		out["task"] = root
	}
	// Direct source tasks are a live, read-only blackboard view. Keep their
	// summaries in a separate field so their intents never enter this task's
	// frontier or get mistaken for locally claimable work.
	out["related_tasks"] = t.relatedTaskOverviews()
	// coverage：粗略的资产测试覆盖度参考——范围(task_scope)内的资产里，被 fact 碰过的
	// 占比 + by_type(按类型的 总数/已测)。要看未测的具体资产由 agent 按需调 list_untested_assets 自行判断。仅任务上下文有。
	// 资产覆盖度功能关闭时(coverageDisabled)：只保留 host_count(目标主机数的感知信息)，
	// 丢弃 denominator/tested/pct/by_type/note 等覆盖度度量，避免污染上下文、也不诱导
	// 已隐藏的 add_task_scope/list_untested_assets。
	if t.as != nil && t.ts != nil && t.taskID > 0 {
		{
			m := map[string]any{}
			if !t.coverageDisabled {
				if cov, err := t.as.TaskCoverageWithSources(t.taskID); err == nil {
					m["denominator"] = cov.Denominator
					m["tested"] = cov.Tested
					m["by_type"] = cov.ByType
					m["note"] = "coverage는 fact/finding이 연결된 자산 관찰 비율이며 취약점 유형별 검증률이 아닙니다. 정찰 사실도 포함하므로 이 수치만으로 테스트 완료를 판단하지 마세요. 현재 작업과 직접 연결된 작업의 scope·관찰 기준점을 포함하며 연결된 scope는 읽기 전용입니다. list_untested_assets는 아직 fact/finding이 연결되지 않은 자산을 조회합니다."
					if cov.Denominator == 0 {
						m["pct"] = nil
						m["status"] = "범위가 지정되지 않음"
					} else {
						m["pct"] = cov.Pct
					}
				}
			}
			if hosts, err := t.as.HostsByTaskWithSources(t.taskID); err == nil {
				// 只给主机总数，不再把 host 列表平铺进 graph_overview（大范围任务里那是每轮
				// 都重复携带的大量字符串，对规划决策价值有限）；具体主机按需 list_assets 查。
				m["host_count"] = len(hosts)
			}
			if len(m) > 0 {
				out["coverage"] = m
			}
		}
	}
	return out
}

func inheritedMap(m map[string]any, sourceTaskID int64) map[string]any {
	m["source_task_id"] = sourceTaskID
	m["inherited"] = true
	return m
}

const (
	relatedOverviewTotalTextRunes      = 48_000
	relatedOverviewMaxTextPerSource    = 8_000
	relatedOverviewMaxGoalsPerSource   = 8
	relatedOverviewMaxHintsPerSource   = 6
	relatedOverviewMaxFactsPerSource   = 12
	relatedOverviewMaxFindingsPerTask  = 6
	relatedOverviewMaxIntentsPerTask   = 8
	relatedOverviewMaxScopePerSource   = 12
	relatedOverviewMaxDigestsPerSource = 6
)

// Text budgets exclude structural IDs, states and counters. Only rendered
// summaries change; node_detail still returns the persisted originals.
type overviewTextBudget struct {
	remaining int
	truncated bool
}

func relatedOverviewBudgetForSources(sourceCount int) int {
	if sourceCount <= 0 {
		return 0
	}
	if sourceCount > db.MaxTaskSourceCount {
		sourceCount = db.MaxTaskSourceCount
	}
	perSource := relatedOverviewTotalTextRunes / sourceCount
	if perSource > relatedOverviewMaxTextPerSource {
		perSource = relatedOverviewMaxTextPerSource
	}
	return perSource
}

func (b *overviewTextBudget) take(value any, fieldLimit int) string {
	var text string
	switch value := value.(type) {
	case string:
		text = strings.TrimSpace(value)
	case nil:
		return ""
	default:
		text = strings.TrimSpace(fmt.Sprint(value))
	}
	if text == "" {
		return ""
	}
	if b.remaining <= 0 || fieldLimit <= 0 {
		b.truncated = true
		return ""
	}
	runes := []rune(text)
	limit := fieldLimit
	if limit > b.remaining {
		limit = b.remaining
	}
	if len(runes) > limit {
		b.truncated = true
		if limit == 1 {
			text = "…"
		} else {
			text = string(runes[:limit-1]) + "…"
		}
		runes = []rune(text)
	}
	b.remaining -= len(runes)
	return text
}

func (b *overviewTextBudget) takeField(entry map[string]any, field string, limit int) {
	if entry[field] == nil {
		return
	}
	part := overviewTextBudget{remaining: min(b.remaining, limit)}
	before := part.remaining
	entry[field] = part.take(entry[field], limit)
	b.remaining -= before - part.remaining
	if part.truncated {
		entry[field+"_truncated"] = true
		b.truncated = true
	}
}

const overviewTruncationNote = "summary_truncated=true 또는 *_truncated=true가 표시된 개요에는 생략된 내용이 있습니다. 목표·힌트가 생략되면 계획·완료 판단 전에 해당 id를 node_detail로 조회하세요. task.id는 원래 작업 설명·목표를 담은 시작 노드이며, digest 전문도 node_detail(id) 또는 expand_digest(id)로 조회할 수 있습니다. 원문은 저장소에 그대로 보존됩니다."

func recentTerminalIntents(store *db.ExplorationStore, limit int) []*db.Node {
	if limit <= 0 {
		return []*db.Node{}
	}
	const batch = 300
	cursor := int64(0)
	out := make([]*db.Node, 0, limit)
	for len(out) < limit {
		page, more, err := store.ListByKindPage(db.KindIntent, cursor, batch)
		if err != nil || len(page) == 0 {
			break
		}
		for _, intent := range page {
			switch intent.State {
			case "done", "blocked", "exhausted", "stopped":
				out = append(out, intent)
			}
			if len(out) >= limit {
				break
			}
		}
		if !more {
			break
		}
		cursor = page[len(page)-1].ID
	}
	return out
}

// relatedTaskOverviews distills persistent blackboard state from direct source
// tasks. It intentionally reads each source's local store methods, never its own
// related sources, so inheritance is one level only.
func (t *ToolSet) relatedTaskOverviews() []map[string]any {
	sources, err := t.ts.DirectSourceStores()
	if err != nil {
		return []map[string]any{}
	}
	if len(sources) > db.MaxTaskSourceCount {
		sources = sources[:db.MaxTaskSourceCount]
	}
	perSourceTextBudget := relatedOverviewBudgetForSources(len(sources))
	out := make([]map[string]any, 0, len(sources))
	for _, source := range sources {
		ts := source.Store
		// §2 cross-task: render the source task's OWN folded view — fold out the
		// members it has already folded, and surface its cold_digests read-only.
		hidden := hiddenMembersFor(ts)
		budget := overviewTextBudget{remaining: perSourceTextBudget}
		item := map[string]any{
			"source_task_id": source.Task.TaskID,
			"inherited":      true,
			"task": map[string]any{
				"description": budget.take(source.Task.Description, 800),
				"goal":        budget.take(source.Task.Goal, 800),
				"status":      source.Task.Status,
			},
		}
		if id, _ := ts.OriginFactID(); id > 0 {
			item["task"].(map[string]any)["id"] = id
		}
		stats, statsErr := ts.Stats()

		edges, _ := ts.Edges(5000)
		parentsOf := map[int64][]int64{}
		yieldsOf := map[int64][]int64{}
		factFrom := map[int64]int64{}
		for _, edge := range edges {
			switch edge.Rel {
			case db.RelDerivedFrom, db.RelSpawns:
				parentsOf[edge.To] = append(parentsOf[edge.To], edge.From)
			case db.RelYields:
				yieldsOf[edge.From] = append(yieldsOf[edge.From], edge.To)
				factFrom[edge.To] = edge.From
			}
		}

		goals, _ := ts.ListByKind(db.KindGoal, relatedOverviewMaxGoalsPerSource)
		goalSummary := make([]map[string]any, 0, len(goals))
		for _, goal := range goals {
			var payload map[string]any
			_ = json.Unmarshal(goal.Payload, &payload)
			goalSummary = append(goalSummary, inheritedMap(map[string]any{
				"id": goal.ID, "state": goal.State, "text": budget.take(payload["text"], 400),
			}, source.Task.TaskID))
		}
		item["goals"] = goalSummary

		hints, _ := ts.ListByKind(db.KindHint, relatedOverviewMaxHintsPerSource)
		hintSummary := make([]map[string]any, 0, len(hints))
		for _, hint := range hints {
			var payload map[string]any
			_ = json.Unmarshal(hint.Payload, &payload)
			hintSummary = append(hintSummary, inheritedMap(map[string]any{
				"id": hint.ID, "state": hint.State, "text": budget.take(payload["text"], 400),
			}, source.Task.TaskID))
		}
		item["hints"] = hintSummary

		facts, factTotal, observationTotal, _ := ts.AnalysisFactOverview(relatedOverviewMaxFactsPerSource)
		findings, _ := ts.ListByKind(db.KindFinding, relatedOverviewMaxFindingsPerTask)
		intentNodes, _ := ts.ListByKind(db.KindIntent, 300)
		terminalIntent := make(map[int64]bool, len(intentNodes))
		for _, intent := range intentNodes {
			terminalIntent[intent.ID] = inheritedIntentSummaryState(intent.State)
		}
		item["facts"] = factTotal
		item["http_observation_facts"] = observationTotal
		item["findings"] = len(findings)
		if statsErr == nil {
			item["findings"] = stats[db.KindFinding]
			if stats[db.KindGoal] > len(goals) || stats[db.KindHint] > len(hints) ||
				factTotal > len(facts) || stats[db.KindFinding] > len(findings) {
				budget.truncated = true
			}
		}
		recentFindings := make([]map[string]any, 0, len(findings))
		for _, finding := range findings {
			entry := inheritedMap(compactFinding(finding), source.Task.TaskID)
			entry["summary"] = budget.take(entry["summary"], 400)
			recentFindings = append(recentFindings, entry)
		}
		item["recent_findings"] = recentFindings
		recentFacts := make([]map[string]any, 0, len(facts))
		for _, fact := range facts {
			if hidden(fact.ID) {
				continue // folded into this source's cold_digests — shown there (§2/§6.2)
			}
			m := inheritedMap(compactNode(fact), source.Task.TaskID)
			m["summary"] = budget.take(m["summary"], 400)
			if from := factFrom[fact.ID]; from > 0 && terminalIntent[from] {
				m["from_intent"] = from
			}
			var payload map[string]any
			if json.Unmarshal(fact.Payload, &payload) == nil {
				if confidence, ok := payload["confidence"].(string); ok && confidence != "" {
					m["confidence"] = confidence
				}
			}
			recentFacts = append(recentFacts, m)
		}
		item["recent_facts"] = recentFacts

		recentDoneRaw := recentTerminalIntents(ts, relatedOverviewMaxIntentsPerTask)
		recentDone := recentDoneRaw[:0] // in-place filter: drop this source's folded intents (§2)
		for _, intent := range recentDoneRaw {
			if hidden(intent.ID) {
				continue
			}
			recentDone = append(recentDone, intent)
		}
		for _, intent := range recentDone {
			intent.Inherited = true
			intent.SourceTaskID = source.Task.TaskID
		}
		intentResults := compactIntents(recentDone, parentsOf, yieldsOf)
		for i, intent := range recentDone {
			intentResults[i]["summary"] = budget.take(intentResults[i]["summary"], 400)
			acts, _, err := ts.ActivityPageForTerminalIntent(intent.ID, 0, 20)
			if err != nil {
				continue
			}
			var resultSummary, textFallback string
			for _, activity := range acts {
				switch activity.Kind {
				case "result":
					resultSummary = activity.Summary
				case "text":
					textFallback = activity.Summary
				}
			}
			if resultSummary == "" {
				resultSummary = textFallback
			}
			if resultSummary != "" {
				intentResults[i]["result_summary"] = budget.take(resultSummary, 800)
			}
		}
		item["recent_intent_results"] = intentResults
		// §2 cross-task: the source task's folded cold region, read-only, newest-member
		// first & capped like the current task's. Members (and overflow digests) are
		// resolvable via expand_digest(id)/node_detail(id), which search source tasks.
		if cds, more := coldDigestsRecent(ts, relatedOverviewMaxDigestsPerSource); len(cds) > 0 {
			for _, cd := range cds {
				cd["inherited"] = true
				cd["source_task_id"] = source.Task.TaskID
			}
			item["cold_digests"] = cds
			if len(more) > 0 {
				item["cold_digests_more"] = more // 被截断的更旧 digest 的 id；expand_digest(id) 展开
			}
		}
		if statsErr == nil {
			item["node_stats"] = stats
		}

		if t.as != nil {
			if scopeRows, err := t.as.ListTaskScope(source.Task.TaskID); err == nil && len(scopeRows) > 0 {
				scopeCount := len(scopeRows)
				if len(scopeRows) > relatedOverviewMaxScopePerSource {
					scopeRows = scopeRows[:relatedOverviewMaxScopePerSource]
					budget.truncated = true
				}
				scope := make([]map[string]any, 0, len(scopeRows))
				for _, row := range scopeRows {
					entry := map[string]any{"kind": row.Kind, "source": budget.take(row.Source, 300)}
					switch {
					case row.Domain != "":
						entry["value"] = budget.take(row.Domain, 400)
					case row.Net != "":
						entry["value"] = budget.take(row.Net, 400)
					case row.Value != "":
						entry["value"] = budget.take(row.Value, 400)
					case row.CompanyID != nil:
						entry["company_id"] = *row.CompanyID
					}
					scope = append(scope, entry)
				}
				item["asset_scope"] = scope
				item["asset_scope_count"] = scopeCount
			}
			if coverage, err := t.as.TaskCoverage(source.Task.TaskID, source.Task.ExplorationID); err == nil {
				item["asset_coverage"] = map[string]any{
					"denominator": coverage.Denominator,
					"tested":      coverage.Tested,
					"pct":         coverage.Pct,
					"by_type":     coverage.ByType,
				}
			}
		}
		if budget.truncated {
			item["summary_truncated"] = true
			item["summary_note"] = overviewTruncationNote
		}
		out = append(out, item)
	}
	return out
}

func inheritedIntentSummaryState(state string) bool {
	switch state {
	case "done", "blocked", "exhausted", "stopped":
		return true
	default:
		return false
	}
}

// compactNode distills any exploration node to id + summary + state, dropping the
// big detail/evidence (fetch that on demand via node_detail).
func compactNode(n *db.Node) map[string]any {
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	m := map[string]any{"id": n.ID, "state": n.State, "summary": p["summary"]}
	if n.Inherited {
		inheritedMap(m, n.SourceTaskID)
	}
	return m
}

// compactFinding is compactNode plus the vuln-specific vulnclass/severity.
func compactFinding(n *db.Node) map[string]any {
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	m := map[string]any{"id": n.ID, "state": n.State, "summary": p["summary"]}
	if n.Inherited {
		inheritedMap(m, n.SourceTaskID)
	}
	if vc, ok := p["vulnclass"]; ok && vc != nil && vc != "" {
		m["vulnclass"] = vc
	}
	if sv, ok := p["severity"]; ok && sv != nil && sv != "" {
		m["severity"] = sv
	}
	return m
}

func (t *ToolSet) listFindings() actool.CoreTool {
	return t.readExpTool("list_findings", "이 작업과 직접 연결된 작업의 【확인된 취약점】을 나열합니다(간결한 id+task_id+intent_id+vulnclass+severity+요약+상태). 연결된 작업 항목은 source_task_id/inherited=true가 포함되며 읽기 전용입니다. 취약점만 포함하고 일반 탐색 사실은 list_facts, 상세는 node_detail(id)로 조회하세요.",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			f, _ := t.ts.ListByKindWithSources(db.KindFinding, 500)
			if err := t.ts.PopulateFindingTrafficIDs(f); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			intentOf, _ := t.ts.FindingIntentsWithSources() // finding id -> 产生它的 intent id
			taskID := t.taskID
			if taskID <= 0 {
				taskID, _ = t.ts.TaskID()
			}
			out := make([]map[string]any, 0, len(f))
			for _, n := range f {
				m := compactFinding(n)
				if n.FindingID > 0 {
					m["finding_id"], m["finding_node_id"], m["traffic_count"] = n.FindingID, n.ID, n.TrafficCount
				}
				if n.Inherited {
					m["task_id"] = n.SourceTaskID
				} else {
					m["task_id"] = taskID
				}
				if iid, ok := intentOf[n.ID]; ok {
					m["intent_id"] = iid
				}
				out = append(out, m)
			}
			return jsonResult(out)
		})
}

// factsPageSize is the default page size for list_facts. Facts pile up on long
// tasks; returning all of them at once (the old behaviour) could blow up the
// context, so default to the newest page and let the agent page/filter for more.
const factsPageSize = 20

func (t *ToolSet) listFacts() actool.CoreTool {
	return t.readExpTool("list_facts", "이 작업과 직접 연결된 작업의 【탐색 사실/결론】을 최신순으로 페이지 조회합니다(간결한 id+요약+상태. 긴 요약은 생략되므로 전문은 node_detail(id) 사용). 모든 인자는 선택 사항입니다: limit(기본 20, 최대 100), before(커서. 이전 페이지의 next_before로 더 오래된 페이지 조회, 생략/0=최신 페이지), q(요약 키워드 필터). 반환값 {facts, total, has_more, next_before}에서 total은 필터 후 전체 개수이고 has_more=true이면 next_before로 다음 페이지를 조회하세요. 연결된 작업 항목은 source_task_id/inherited=true가 포함되며 읽기 전용입니다. 취약점은 list_findings를 사용하세요.",
		obj(map[string]any{
			"limit":  intp("반환 개수, 기본 20, 최대 100"),
			"before": intp("페이지 커서: 이 값보다 id가 작은 오래된 사실만 반환합니다. 생략 또는 0이면 최신 페이지"),
			"q":      str("사실 요약의 키워드로 필터링(대소문자 무시). 생략하면 필터 없음"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Limit  int    `json:"limit"`
				Before int64  `json:"before"`
				Q      string `json:"q"`
			}
			_ = json.Unmarshal(in, &a)
			limit := a.Limit
			if limit <= 0 {
				limit = factsPageSize
			}
			if limit > 100 {
				limit = 100
			}
			f, hasMore, total, err := t.ts.ListByKindPageWithSources(db.KindFact, a.Before, limit, strings.TrimSpace(a.Q))
			if err != nil {
				return actool.Result{}, err
			}
			out := make([]map[string]any, 0, len(f))
			for _, n := range f {
				out = append(out, compactFact(n))
			}
			res := map[string]any{"facts": out, "total": total, "has_more": hasMore}
			if hasMore && len(f) > 0 {
				res["next_before"] = f[len(f)-1].ID // 传回它取下一页(更旧的)
			}
			return jsonResult(res)
		})
}

// factSummaryMax caps a fact summary in list_facts output. Facts carry one-line
// conclusions, but nothing enforces brevity; a runaway summary must not bloat a
// whole page. Full text stays available via node_detail(id).
const factSummaryMax = 160

// compactFact is compactNode with the summary rune-capped for list_facts, so a
// page of facts stays bounded regardless of how long any single summary grew.
func compactFact(n *db.Node) map[string]any {
	m := compactNode(n)
	if s, ok := m["summary"].(string); ok && len([]rune(s)) > factSummaryMax {
		m["summary"] = string([]rune(s)[:factSummaryMax]) + "…"
		m["summary_truncated"] = true
	}
	return m
}

func (t *ToolSet) nodeDetail() actool.CoreTool {
	return t.readExpTool("node_detail", "이 작업 또는 직접 연결된 작업의 【탐색 그래프 노드】 전체 내용을 id로 조회합니다. 상속 노드는 source_task_id/inherited=true가 포함되며 읽기 전용입니다. list_facts/list_findings/graph_overview의 탐색 노드 id만 사용하세요. 자산은 list_assets/asset_neighbors를 사용하세요.",
		obj(map[string]any{"id": idp("탐색 그래프 노드 id(자산 id가 아님)")}, "id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				ID json.RawMessage `json:"id"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.ID)
			if id <= 0 {
				return actool.Errorf("id는 필수입니다"), nil
			}
			n, err := t.ts.GetNodeWithSources(id)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if n == nil {
				return actool.Errorf(fmt.Sprintf("탐색 노드 %d를 찾지 못했습니다. 자산을 조회하려면 list_assets / asset_neighbors를 사용하세요(자산과 탐색 노드는 서로 다른 id 공간이므로 자산 id를 node_detail에 전달할 수 없습니다).", id)), nil
			}
			if err := t.ts.PopulateFindingTrafficIDs([]*db.Node{n}); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(n) // full payload incl. detail / evidence, plus explicit finding IDs
		})
}

// --- planner write tools ---

// intentItem 是 add_intent 批量/单条的一条探索方向。
type intentItem struct {
	FollowUpRefs []db.FollowUpRef  `json:"follow_up_refs"`
	Summary      string            `json:"summary"`
	AssetIDs     []json.RawMessage `json:"asset_ids"`
	ParentIDs    []json.RawMessage `json:"parent_ids"`
	Priority     int               `json:"priority"`
}

// addOneIntent 创建一条意图节点并连上游血缘，返回 id。
// 约束：意图只能锚在已确认知识上——每个 parent_id 必须是已存在的 fact/finding
// 节点（不能挂在别的意图/目标/提示上）。顶层全新方向留空 parent_ids，兜底连 origin fact。
// 这样"每个意图都连到 fact 节点、且是发现驱动而非凭空规划"从创建路径上被强制。
func (t *ToolSet) addOneIntent(it intentItem) (int64, error) {
	return t.addOneIntentContext(context.Background(), it)
}

func (t *ToolSet) addOneIntentContext(ctx context.Context, it intentItem) (int64, error) {
	return t.registerOneIntentContext(ctx, it)
}

func (t *ToolSet) registerOneIntentContext(ctx context.Context, it intentItem) (int64, error) {
	if len(it.FollowUpRefs) > 0 {
		return 0, fmt.Errorf("follow_up_refs는 더 이상 사용하지 않습니다. 근거 fact/finding을 parent_ids로 연결하세요")
	}
	if strings.TrimSpace(it.Summary) == "" {
		return 0, fmt.Errorf("summary는 비워 둘 수 없습니다")
	}
	// 先校验锚点（建节点前，避免坏锚点留下孤儿意图）。
	parents := pidList(it.ParentIDs)
	for _, pidv := range parents {
		n, err := t.ts.GetNodeWithSources(pidv)
		if err != nil || n == nil {
			return 0, fmt.Errorf("parent_id %d가 이 작업 또는 직접 연결된 작업에 없습니다. parent_ids는 기존 【사실(fact)/발견(finding)】 노드 id여야 하며 최상위 새 방향이면 parent_ids를 비워 두세요", pidv)
		}
		if n.Kind != db.KindFact && n.Kind != db.KindFinding {
			help := ""
			if n.Kind == db.KindIntent && !n.Inherited {
				if ids, err := t.ts.OutcomeIDs(pidv); err == nil && len(ids) > 0 {
					help = fmt.Sprintf(" 이 의도의 결과 노드 %v를 node_detail로 확인한 뒤 실제 근거를 선택하세요.", ids)
				}
			}
			return 0, fmt.Errorf("parent_id %d는 %q 노드이므로 의도의 근거로 사용할 수 없습니다. 의도는 확인된 【사실(fact)/발견(finding)】에만 연결할 수 있고 의도/목표/힌트에는 연결할 수 없습니다. 최상위 새 방향이면 parent_ids를 비워 두세요.%s", pidv, n.Kind, help)
		}
	}
	priority := it.Priority
	if priority == 0 {
		priority = 5
	}
	anchors := pidList(it.AssetIDs)
	// 资产拦截：意图绑定的资产若命中系统资产拦截规则，则禁止下发该意图。
	if t.as != nil && len(anchors) > 0 {
		hits, err := t.as.CheckAssetsIntercept(t.taskID, anchors)
		if err != nil {
			return 0, fmt.Errorf("자산 차단 검증 실패: %w", err)
		}
		if len(hits) > 0 {
			var b strings.Builder
			fmt.Fprintf(&b, "의도 「%s」에 연결된 자산이 테스트 범위 검증을 통과하지 못했습니다. 관련 자산의 테스트를 중지하세요: ", it.Summary)
			for _, h := range hits {
				fmt.Fprintf(&b, "\n - %s", h.Describe())
			}
			return 0, fmt.Errorf("%s", b.String())
		}
	}
	payload := map[string]any{"summary": it.Summary}
	if len(anchors) > 0 {
		payload["asset_ids"] = anchors
	}
	return t.ts.AddIntentWithParents(ctx, payload, priority, anchors, parents, "planner")
}

func (t *ToolSet) addIntent() actool.CoreTool {
	return t.writeExpTool("add_intent", "【탐색 방향】을 생성하여 frontier에 저장하고 탐색 연결 그래프에 연결합니다. 각 의도에는 검증 질문 또는 사용자가 요구한 독립 산출물 하나를 맡기세요. summary에 대상·질문·확인할 근거를 쓰고, 서로 독립적인 질문을 한 항목에 합치지 마세요. 같은 질문의 대조·변형·원인·영향 확인은 한 Worker가 이어서 깊게 검증합니다. 일부 실패나 미확보 전제가 있으면 다른 접근과 전제 확보도 배정하며, 사용자가 요구하지 않은 사전 조건으로 허용된 검증을 줄이지 마세요.\n"+
		"★일괄 제출 우선: 한 회차에서 선별한 여러 새 방향을 intents 배열로 한 번에 제출하세요(개별 호출보다 왕복 횟수 절약). 반환 ids 배열은 intents와 길이·순서가 같습니다(실패 항목 id=0, 상세는 errors). 단일 항목은 intents를 생략하고 최상위 summary를 전달하세요.",
		withBatchSchema(obj(map[string]any{
			"intents":    map[string]any{"type": "array", "description": "【우선 사용】추가할 독립 의도의 배열이며 각 요소를 별도 의도로 순서대로 처리합니다. 필드는 아래 최상위 필드(summary/asset_ids/parent_ids/priority)와 같으며 반환 ids는 길이·순서가 같습니다.", "items": map[string]any{"type": "object"}},
			"summary":    str("[단일] 대상 또는 관련 자산 묶음 + 검증 질문 하나(또는 사용자가 요구한 독립 산출물) + 확인할 근거. 같은 질문의 정상 대조·입력 변형·영향 검증을 함께 다루고, 전제가 부족하면 확보 방법도 확인하세요. 독립적인 주제를 나열하지 마세요."),
			"asset_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "이 방향에서 테스트/공격할 【대상 자산 id】(**가능하면 전달**, 0/1/여러 개. list_assets의 자산 id이며 탐색 노드 id가 아님). 대상 자산(사이트/인터페이스/인자/호스트 등)을 지정합니다. 구체적인 자산을 다루면 반드시 전달하세요. 탐색 대상을 구조화하여 중복 확인과 의도·자산 연결에 사용합니다. 구체적인 대상 자산이 없는 순수한 전역 정찰에만 비워 두세요."},
			"parent_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "상위 근거 id(선택 사항, 0/1/여러 개): 이 방향을 종합하여 도출한 【확인된 사실(fact)/발견(finding)】. **기존 fact/finding 노드 id만 지정하며 의도/목표/힌트는 금지**합니다. 의도는 확인된 지식과 발견에 근거해야 하며 근거 없이 계획해서는 안 됩니다. 여러 사실이 함께 새 의도를 만들면 모두 전달하고 최상위 새 정찰 방향이면 비워 두세요(작업 시작점 origin fact에 자동 연결)."},
			"priority":   intp("우선순위 0-10, 기본 5"),
		}), "intents", "summary"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Intents    []intentItem `json:"intents"`
				intentItem              // 单条模式：顶层 summary/asset_ids/parent_ids/priority
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("의도 입력을 해석할 수 없습니다: " + err.Error()), nil
			}
			batch := len(a.Intents) > 0
			items := a.Intents
			if !batch {
				items = []intentItem{a.intentItem}
			}

			ids := make([]int64, len(items))
			errs := map[string]string{}
			createdAny := false
			for i, it := range items {
				id, err := t.registerOneIntentContext(ctx, it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
				createdAny = true
			}

			// 人经主 agent 直投意图 → 若任务已 done（无 open 目标的 goalless 分支），把它
			// 拉回 running，worker 才能领这条意图执行。resumeTask 仅由主 agent 的 Chat 接入
			// (SetResumeTask)；planner 的 ToolSet 为 nil，故 planner 自己调 add_intent 时此段
			// no-op，不影响其正常产意图。意图节点已在上面建好(open)，复活时不会被误判抽干。
			if createdAny && t.resumeTask != nil {
				t.resumeTask()
			}

			if !batch { // 单条：保持原返回
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("intent created: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

func (t *ToolSet) listGoals() actool.CoreTool {
	return t.readExpTool("list_goals", "이 작업의 목표 노드와 상태(open/met)를 나열하여 달성 여부를 판단합니다.",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			g, _ := t.ts.ListByKind(db.KindGoal, 100)
			return jsonResult(g)
		})
}

func (t *ToolSet) proveGoal() actool.CoreTool {
	return t.writeExpTool("prove_goal", "발견/사실이 해당 목표 달성을 증명할 때 증거와 이유를 연결하여 met로 표시합니다. 마지막 목표는 대기·실행·일시정지 의도의 결과를 먼저 반영한 뒤 증명하세요. 일부 발견이나 보고서 작성만으로 남은 검증을 끝내지 마세요.",
		obj(map[string]any{
			"goal_id":           idp("목표 노드 id"),
			"evidence_id":       idp("목표를 증명하는 발견/사실 노드 id"),
			"reason":            str("이 증거가 목표를 충족하는 이유"),
			"completion_review": str("마지막 목표 증명 시 미완료 Todo가 남으면 그 항목과 목표의 관계 및 종료해도 되는 근거를 적으세요. 이 설명으로 대기·실행·일시정지 의도를 취소하거나 검증 완료로 대신할 수 없습니다. 해당 결과를 먼저 반영하고, 목표에 필요한 미검증 질문이나 확보할 전제가 남으면 검증을 이어가세요."),
		}, "goal_id", "evidence_id", "reason"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				GoalID           json.RawMessage `json:"goal_id"`
				EvidenceID       json.RawMessage `json:"evidence_id"`
				Reason           string          `json:"reason"`
				CompletionReview string          `json:"completion_review"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("목표 증명 입력이 올바르지 않습니다: " + err.Error()), nil
			}
			goal, ev := pid(a.GoalID), pid(a.EvidenceID)
			if goal == 0 || ev == 0 {
				return actool.Errorf("goal_id와 evidence_id는 필수입니다"), nil
			}
			if strings.TrimSpace(a.Reason) == "" {
				return actool.Errorf("reason에 증거가 목표를 충족하는 이유를 적으세요"), nil
			}
			allMet, goalCount, err := t.ts.ProveGoal(ctx, goal, ev, a.Reason, a.CompletionReview)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if allMet {
				t.GoalMet = true
				t.Reason = fmt.Sprintf("목표 %d개가 모두 met입니다(마지막으로 goal %d에서 완료됨)", goalCount, goal)
				return actool.Text(fmt.Sprintf("goal %d marked met; 모든 목표의 증명을 저장했습니다. 실행 종료 직전 새 목표·의도·단서·Todo를 다시 확인합니다", goal)), nil
			}
			return actool.Text(fmt.Sprintf("goal %d marked met", goal)), nil
		})
}

func (t *ToolSet) goalMet() actool.CoreTool {
	return t.writeExpTool("goal_met", "전체 작업의 최종 완료를 요청합니다. open 목표는 먼저 prove_goal로 근거를 연결해 증명해야 하며, 이 도구로 개별 증명을 생략할 수 없습니다. 마지막 완료 검토 뒤 새 작업이 생기면 실행을 계속합니다. 이번 계획 회차 종료나 Worker 결과 대기에는 호출하지 마세요.",
		obj(map[string]any{"reason": str("달성 근거(목표를 실제 달성한 증거여야 하며 \"이번 회차에 새 방향 없음\" 같은 회차 종료 이유는 안 됨)")}, "reason"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct{ Reason string }
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("완료 요청 입력이 올바르지 않습니다: " + err.Error()), nil
			}
			if strings.TrimSpace(a.Reason) == "" {
				return actool.Errorf("reason에 전체 작업의 완료 근거를 적으세요"), nil
			}
			if open, err := t.ts.HasOpenGoal(); err != nil {
				return actool.Errorf(err.Error()), nil
			} else if open {
				return actool.Errorf("아직 open 목표가 있습니다. prove_goal로 각 목표의 실제 근거와 완료 검토를 먼저 기록하세요"), nil
			}
			t.GoalMet = true
			t.Reason = a.Reason
			return actool.Text("최종 완료 검토를 요청했습니다. 실행 종료 직전 남은 작업을 다시 확인합니다"), nil
		})
}

// --- worker write tools ---

func (t *ToolSet) addFinding() actool.CoreTool {
	return writeTool("report_finding", "확인한 취약점을 기록하며 evidence에 명령 출력·로그 등 검증 가능한 증거를 제공합니다. 작업 컨텍스트에서는 현재 intent_id를 전달하세요. 반환 finding_id는 독립 취약점 레코드 ID이고 finding_node_id는 탐색 노드 ID입니다(첫 줄은 이 노드 번호 유지).", obj(map[string]any{
		"vulnclass": str("취약점 유형"), "name": str("취약점 이름"), "severity": str("critical|high|medium|low"), "summary": str("발견 요약"),
		"intent_id": idp("현재 작업의 의도 id"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "영향받는 자산 id"},
		"evidence":         str("증거/PoC 텍스트"),
		"evidence_hint_id": map[string]any{"type": []string{"integer", "null"}, "description": "선택 사항: 이 작업에서 이 취약점에 대응하는 힌트 노드 ID. 힌트가 없으면 생략, null 또는 0을 사용하세요. 양수 ID는 해당 힌트의 구조화된 traffic_refs를 가져옵니다. 상속한 힌트나 다른 취약점의 힌트를 참조할 수 없습니다"},
		"traffic_refs": map[string]any{"type": []string{"array", "null"}, "description": "선택 사항. HTTP/HTTPS 취약점은 검색 후 요청/응답이 실제로 결론을 뒷받침하는지 각각 확인하고 재현 순서대로 실제 ID를 지정하세요. TCP 등 HTTP가 아니거나 미수집·정확한 기록 없음의 경우 생략 또는 []로 지정해도 보고할 수 있습니다. evidence에 이유와 검증 가능한 다른 증거를 제공할 수 있습니다. ID를 추측하거나 도메인/시각만으로 연관성을 단정하지 마세요. 확보한 증거를 먼저 재사용하고 검증·재현에 필요한 추가 요청은 허용 범위에서 수행하세요. 단지 보고 형식을 채우기 위한 추가 탐지는 필요하지 않습니다. 용도: baseline 정상 대조 / proof 취약점 입증 / verification 추가 검증 / supporting 보조 증거.",
			"items": obj(map[string]any{"traffic_id": str("traffic_search가 반환한 실제 트래픽 ID"), "role": map[string]any{"type": "string", "enum": []string{"baseline", "proof", "verification", "supporting"}}, "note": str("이 트래픽이 취약점 결론을 뒷받침하는 방식")}, "traffic_id")},
	}, "vulnclass", "severity", "summary"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
		var a struct {
			VulnClass, Name, Severity, Summary, Evidence string
			IntentID                                     json.RawMessage   `json:"intent_id"`
			AssetIDs                                     []json.RawMessage `json:"asset_ids"`
			TrafficRefs                                  []db.TrafficRef   `json:"traffic_refs"`
			EvidenceHintID                               json.RawMessage   `json:"evidence_hint_id"`
		}
		if err := json.Unmarshal(in, &a); err != nil {
			return actool.Errorf(err.Error()), nil
		}
		if t.ts == nil {
			return actool.Errorf("report_finding에는 작업 컨텍스트가 필요합니다. 플랫폼 대화에서는 add_task_hint로 해당 작업에 취약점을 인계하고 기존 traffic_refs를 힌트에 포함하여 작업 에이전트가 등록하게 하세요. 이미 등록한 취약점은 bind_finding_traffic으로 추가 연결할 수 있습니다."), nil
		}
		// Auto-binding off: ignore the evidence params instead of rejecting the call.
		// stripTrafficParameters already removes them from the advertised schema, but
		// models routinely emit fields anyway — failing here would discard a confirmed
		// finding over a stray parameter. The success path below reports evidence_status
		// "not_bound" with the "已关闭，可在页面人工关联" note, which is what the caller needs.
		if !findingTrafficBindingEnabled() {
			a.TrafficRefs, a.EvidenceHintID = nil, nil
		}
		hintID := pid(a.EvidenceHintID)
		rawHintID := strings.TrimSpace(string(a.EvidenceHintID))
		if hintID < 0 || (hintID == 0 && rawHintID != "" && rawHintID != "null" && rawHintID != "0") {
			return actool.Errorf("evidence_hint_id는 양수인 힌트 노드 ID여야 합니다. 인계 힌트가 없으면 생략, null 또는 0을 사용하세요"), nil
		}
		refs, err := t.findingRefsFromHint(hintID, a.TrafficRefs)
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		intentID, err := t.resultIntent(pid(a.IntentID))
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		input := db.RecordFindingInput{TaskID: t.taskID, ExplorationID: t.ts.ID(), IntentID: intentID, VulnClass: a.VulnClass, Name: a.Name, Severity: a.Severity, Summary: a.Summary, Evidence: a.Evidence, Worker: t.worker, AssetIDs: pidList(a.AssetIDs)}
		var recorded *db.RecordedFinding
		if t.findingRecorder != nil {
			recorded, err = t.findingRecorder.Record(ctx, input, refs)
		} else if len(refs) > 0 {
			return actool.Errorf("트래픽 증거 저장소를 사용할 수 없어 취약점을 등록하지 않았습니다"), nil
		} else {
			recorded, err = t.ts.RecordFinding(ctx, input)
		}
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		if t.notifyFinding != nil {
			iid := input.IntentID
			if iid <= 0 {
				iid = t.ownerNode
			}
			t.notifyFinding(iid, a.Summary)
		} else if t.notify != nil {
			t.notify()
		}
		t.writes.Findings++
		// Keep the first line's node-ID contract for existing reporter triggers.
		for i := range recorded.Traffic.Bindings {
			recorded.Traffic.Bindings[i].Snapshot.ReqHead = ""
			recorded.Traffic.Bindings[i].Snapshot.RespHead = ""
		}
		result := struct {
			*db.RecordedFinding
			EvidenceStatus string `json:"evidence_status"`
			EvidenceNote   string `json:"evidence_note,omitempty"`
		}{RecordedFinding: recorded, EvidenceStatus: "bound"}
		if len(recorded.Traffic.Bindings) == 0 {
			result.EvidenceStatus = "not_bound"
			result.EvidenceNote = "취약점은 저장했지만 트래픽은 연결하지 않았습니다. TCP/패킷 없음이면 계속 진행할 수 있습니다. 검증한 HTTP 트래픽이 있으면 사용 가능한 bind_finding_traffic 또는 취약점 화면에서 연결한 뒤 증거 인계를 완료하세요. 취약점을 중복 생성하지 마세요."
			if !findingTrafficBindingEnabled() {
				result.EvidenceNote = "취약점을 저장했습니다. 에이전트 트래픽 자동 연결이 꺼져 있으므로 화면에서 수동으로 연결할 수 있습니다."
			}
		}
		raw, _ := json.Marshal(result)
		return actool.Text(fmt.Sprintf("finding recorded: %d\n%s", recorded.NodeID, raw)), nil
	})
}

// recordFact writes a general exploration RESULT/conclusion (not a vuln, not a
// new asset) into the EXPLORATION graph, chained to the intent that produced it.
// This is the home for observations and — importantly — negative results
// ("port closed", "param not injectable", "no login found"). Such conclusions
// must NOT be stuffed into the asset graph via upsert_asset.
// factItem 是 record_fact 批量/单条的一条事实。
type factItem struct {
	FollowUps  []string          `json:"follow_ups"`
	Summary    string            `json:"summary"`
	Detail     string            `json:"detail"`
	Evidence   string            `json:"evidence"`   // 一行关键证据（命令+关键输出行），支撑结论、便于事后核对
	Confidence string            `json:"confidence"` // observed（直接看到）| inferred（据现象推断）
	IntentID   json.RawMessage   `json:"intent_id"`
	AssetIDs   []json.RawMessage `json:"asset_ids"`
}

// recordOneFact 写一条 fact 节点并连到意图（intent→yields→fact）。defaultIntent 为
// 批量时的默认意图（本条未给 intent_id 时用）。
func (t *ToolSet) recordOneFact(it factItem, defaultIntent int64) (int64, error) {
	return t.recordOneFactContext(context.Background(), it, defaultIntent)
}

// resultIntent uses the assigned intent as authority, while root-owned tools may
// still write explicit observations without pretending that a root fact is an intent.
func (t *ToolSet) resultIntent(intentID int64) (int64, error) {
	if t.ownerNode > 0 {
		owner, err := t.ts.GetNode(t.ownerNode)
		if err != nil || owner == nil {
			return 0, fmt.Errorf("현재 실행의 출처 노드를 확인하지 못했습니다")
		}
		if owner.Kind == db.KindIntent {
			if intentID > 0 && intentID != owner.ID {
				return 0, fmt.Errorf("현재 Worker의 결과는 할당된 intent_id %d에만 기록할 수 있습니다", owner.ID)
			}
			intentID = owner.ID
		}
	}
	if intentID > 0 {
		n, err := t.ts.GetNode(intentID)
		if err != nil || n == nil || n.Kind != db.KindIntent {
			return 0, fmt.Errorf("intent_id는 이 작업의 의도여야 합니다(연결된 작업 의도는 읽기 전용)")
		}
	}
	return intentID, nil
}

func (t *ToolSet) recordOneFactContext(ctx context.Context, it factItem, defaultIntent int64) (int64, error) {
	if len(it.FollowUps) > 0 {
		return 0, fmt.Errorf("follow_ups는 더 이상 사용하지 않습니다. 새 검증 방향과 근거를 detail에 기록하세요")
	}
	if strings.TrimSpace(it.Summary) == "" {
		return 0, fmt.Errorf("summary는 비워 둘 수 없습니다")
	}
	payload := map[string]any{"summary": it.Summary}
	if it.Detail != "" {
		payload["detail"] = it.Detail
	}
	if e := strings.TrimSpace(it.Evidence); e != "" {
		payload["evidence"] = e
	}
	if c := strings.TrimSpace(it.Confidence); c != "" {
		payload["confidence"] = c
	}
	intent := pid(it.IntentID)
	if intent <= 0 {
		intent = defaultIntent
	}
	intent, err := t.resultIntent(intent)
	if err != nil {
		return 0, err
	}
	id, err := t.ts.AddFactWithIntent(ctx, payload, pidList(it.AssetIDs), t.worker, intent)
	if err != nil {
		return 0, err
	}
	t.writes.Facts++
	return id, nil
}

func (t *ToolSet) recordFact() actool.CoreTool {
	return t.writeExpTool("record_fact", "탐색 【사실/결론】을 탐색 그래프에 기록하고 이를 생성한 의도(intent_id)에 연결합니다. 특징 식별/열거 등 【긍정적 결론】과 \"포트 닫힘\"/\"인자 주입 불가\"/\"로그인 진입점 미발견\" 등 【부정적 결론】을 포함하는 탐색 결과를 기록합니다.\n"+
		"⚠️한 번의 탐색에서 얻은 여러 관찰은 【하나의 사실로 종합】하고 나누지 마세요. 합칠 수 있으면 하나로 표현하세요: summary=이번 결론을 요약한 한 문장, detail=관련 세부 내용(여러 항목 포함 가능). 예: 특징 식별 의도 → 사실 하나 {summary:\"X 사이트의 기술 스택과 응답 특징 식별\", detail:\"nginx 1.25 / Vue3 / 200 / title=.. / body_len=..\"}. 상태 코드·특징·제목을 각각 기록하지 마세요. 보통 한 의도에는 사실 하나이며 지나치게 나누면 그래프가 계속 커집니다.\n"+
		"★facts 배열은 【서로 다른】 결론 여러 개를 한 번에 기록할 때 사용합니다(각 항목의 intent_id를 생략하면 최상위 intent_id 사용). 반환 ids는 facts와 길이·순서가 같습니다.\n"+
		"⚠️도구 출력에서 【실제로 관찰한】 결론만 쓰고 추측하지 마세요. evidence와 confidence는 부정확한 결론이 그래프를 오염시키지 않도록 합니다:\n"+
		"  · evidence=이 결론을 뒷받침하는 핵심 증거 【한 줄】(명령+가장 잘 입증하는 출력 한두 줄). **반드시 간결하게** 작성하세요. 세부 내용은 detail에 있으므로 긴 출력을 다시 붙이지 마세요.\n"+
		"  · confidence=observed(출력에서 직접 관찰) | inferred(현상에서 추론).\n"+
		"  · **부정적 결론**(주입 불가/포트 닫힘/진입점 미발견 등)은 \"관찰 + 잠정적 해석\"만 쓰세요. 실제 본 것을 설명하고 방향 포기 여부는 계획자가 전체 현황을 종합하여 결정하게 하세요. evidence는 반드시 제공하고, 수단을 충분히 시도하지 않았거나 증거가 약하면(한 번만 확인/그렇게 보임 포함) inferred, 충분히 시도하고 직접 관찰한 경우에만 observed로 표시하세요.",
		withBatchSchema(obj(map[string]any{
			"facts":      map[string]any{"type": "array", "description": "【서로 다른 결론이 여러 개일 때 사용】사실 배열. 각 요소는 아래 최상위 필드(summary/detail/evidence/confidence/intent_id/asset_ids)와 같고 intent_id 생략 시 최상위 값을 사용합니다. 반환 ids는 길이·순서가 같습니다.", "items": map[string]any{"type": "object"}},
			"summary":    str("이번 탐색 결론을 【요약한 한 문장】(detail의 요약)"),
			"intent_id":  idp("이 사실을 생성한 의도 id(할당받은 의도. 일괄 제출 시 각 항목의 기본값)"),
			"detail":     str("이 사실의 세부 내용: 이번 탐색에서 얻은 여러 관찰 사실을 모두 기록하세요"),
			"evidence":   str("핵심 증거 【한 줄】: 명령 + 결론을 가장 잘 입증하는 출력 한두 줄. 간결하게 쓰고 긴 출력을 붙이지 마세요(상세는 detail)."),
			"confidence": str("observed(출력에서 직접 관찰) | inferred(현상에서 추론). 부정적 결론은 반드시 정확하게 표시하세요."),
			"asset_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "관련 자산 id(선택 사항, 0/1/여러 개): 이 사실이 다루는 자산"},
		}), "facts", "summary"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Facts    []factItem `json:"facts"`
				factItem            // 单条模式 + 批量默认 intent_id
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("사실 입력을 해석할 수 없습니다: " + err.Error()), nil
			}
			batch := len(a.Facts) > 0
			items := a.Facts
			if !batch {
				items = []factItem{a.factItem}
			}
			defaultIntent := pid(a.factItem.IntentID) // 顶层 intent_id = 批量默认

			ids := make([]int64, len(items))
			errs := map[string]string{}
			for i, it := range items {
				id, err := t.recordOneFactContext(ctx, it, defaultIntent)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
			}

			if !batch { // 单条：保持原返回
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("fact recorded: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

type hintItem struct {
	Text        string            `json:"text"`
	AssetIDs    []json.RawMessage `json:"asset_ids"`
	TrafficRefs []db.TrafficRef   `json:"traffic_refs"`
}

// addOneHint 挂一条 hint 节点(active/human)到探索图,可锚定资产,返回 id。
func (t *ToolSet) addOneHint(it hintItem) (int64, error) {
	if len(it.TrafficRefs) > 0 && !findingTrafficBindingEnabled() {
		return 0, fmt.Errorf("에이전트 트래픽 자동 연결이 꺼져 있어 traffic_refs를 포함한 힌트를 저장하지 않았습니다. 시스템 설정에서 켜거나 텍스트만 인계할 수 있습니다")
	}
	if strings.TrimSpace(it.Text) == "" {
		return 0, fmt.Errorf("text는 비워 둘 수 없습니다")
	}
	var anchors []int64
	for _, raw := range it.AssetIDs {
		if tid := pid(raw); tid > 0 {
			anchors = append(anchors, tid)
		}
	}
	refs, err := db.NormalizeTrafficRefs(it.TrafficRefs)
	if err != nil {
		return 0, err
	}
	payload := map[string]any{"text": it.Text}
	if len(refs) > 0 {
		payload["traffic_refs"] = refs
	}
	// 唤醒 planner 不在此处逐条做——由 addHint 在整批写完后统一触发一次（带上提示文本），
	// 避免一次 add_hint 多条提示逐条刷屏 planner 的触发行。
	return t.ts.AddNode(db.KindHint, payload, 0, "active", "human", anchors)
}

type goalItem struct {
	Text      string `json:"text"`
	VulnClass string `json:"vulnclass"`
}

// addOneGoal 挂一条 goal 节点(open)到探索图:连到任务根(origin fact,rel spawns)。
// origin 取 t.worker(缺省 system):goals 拆解器写入的记 "goals"、主 agent 运行时记
// "human"。唤醒 planner 由 setGoals 在整批写完后统一做(见下),这里只负责落库。
func (t *ToolSet) addOneGoal(it goalItem) (int64, error) {
	return t.addOneGoalContext(context.Background(), it)
}

func (t *ToolSet) addOneGoalContext(ctx context.Context, it goalItem) (int64, error) {
	text := strings.TrimSpace(it.Text)
	if text == "" {
		return 0, fmt.Errorf("text는 비워 둘 수 없습니다")
	}
	payload := map[string]any{"text": text}
	if vc := strings.TrimSpace(it.VulnClass); vc != "" {
		payload["vulnclass"] = vc
	}
	origin := t.worker
	if origin == "" {
		origin = "system"
	}
	return t.ts.AddGoalWithOrigin(ctx, payload, origin)
}

// setGoals 给【本任务】新增探索目标(goal 节点)。既是目标拆解器的提交工具,也是主
// agent 运行时补目标的工具——同一个受管工具,可在 web 端改描述/schema、按 agent 绑定。
func (t *ToolSet) setGoals() actool.CoreTool {
	return writeTool("set_goals",
		"【이 작업】에 탐색 목표(goal)를 추가합니다. 목표는 최종적으로 제공하거나 검증할 수 있는 결과이며 공격 단계나 정찰 동작이 아닙니다.\n"+
			"★일괄 제출 우선: 여러 목표를 goals 배열로 한 번에 제출하세요. 반환 ids는 길이·순서가 같습니다(실패 항목 id=0, 상세는 errors). 단일 항목은 goals를 생략하고 최상위 text를 전달하세요.\n"+
			"vulnclass는 선택 사항이며 대응하는 취약점 유형(예: SQLi/IDOR)입니다. 비즈니스 로직 목표는 비워 두세요. 목표 달성 여부는 시스템이 met로 판정하며 이 도구는 추가만 담당합니다.",
		obj(map[string]any{
			"goals":     map[string]any{"type": "array", "description": "【우선 사용】추가할 목표 배열이며 순서대로 처리합니다. 각 요소: text(필수, 독립적으로 검증 가능한 최종 목표)+vulnclass(선택). 반환 ids는 길이·순서가 같습니다.", "items": map[string]any{"type": "object"}},
			"text":      str("[단일] 독립적으로 검증 가능한 최종 목표"),
			"vulnclass": str("[단일] 명확한 경우 대응 취약점 유형(예: SQLi/IDOR). 비즈니스 로직 목표는 비워 둘 수 있습니다"),
		}),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("set_goals 비활성: ExplorationStore가 초기화되지 않았습니다"), nil
			}
			var a struct {
				Goals    []goalItem `json:"goals"`
				goalItem            // 单条模式:顶层 text/vulnclass
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("목표 입력을 해석할 수 없습니다: " + err.Error()), nil
			}
			batch := len(a.Goals) > 0
			items := a.Goals
			if !batch {
				items = []goalItem{a.goalItem}
			}

			ids := make([]int64, len(items))
			errs := map[string]string{}
			var addedTexts []string
			for i, it := range items {
				id, err := t.addOneGoalContext(ctx, it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
				addedTexts = append(addedTexts, strings.TrimSpace(it.Text))
			}
			if len(addedTexts) > 0 {
				// 唤醒 planner(整批一次)。优先 notifyGoal:一次 set_goals 记一条「人新增了
				// N 个目标:…」触发,不逐条刷屏;拆解器/worker 无此回调 → 退回纯 notify(拆解器
				// round-0 连 notify 也没接,即无操作,因为此时 planner 尚未启动)。
				switch {
				case t.notifyGoal != nil:
					t.notifyGoal(addedTexts)
				case t.notify != nil:
					t.notify()
				}
				// 主 agent 运行时新增目标 → 把已完成/暂停的任务拉回 running 继续跑(终态门会
				// 吞掉普通 notify,必须显式复活)。仅 mainagent 接了此回调;拆解器/worker 为 nil。
				if t.resumeTask != nil {
					t.resumeTask()
				}
			}

			if !batch { // 单条:保持原返回
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("goal added: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

type constraintItem struct {
	Text string `json:"text"`
	Type string `json:"type"` // allow | deny
}

// addOneConstraint 落一条操作约束到 task_constraints。origin 取 t.worker(缺省 system):
// 拆解器写 "goals"、主 agent 写 "human"。
func (t *ToolSet) addOneConstraint(it constraintItem) (int64, error) {
	text := strings.TrimSpace(it.Text)
	if text == "" {
		return 0, fmt.Errorf("text는 비워 둘 수 없습니다")
	}
	kind := strings.TrimSpace(strings.ToLower(it.Type))
	if kind == "" {
		kind = "deny" // 默认按禁止处理:未标注类型时更保守
	}
	if kind != "allow" && kind != "deny" {
		return 0, fmt.Errorf("type은 allow 또는 deny여야 합니다")
	}
	return t.ts.AddConstraint(kind, text, t.worker)
}

// setConstraints 给【本任务】新增操作约束(allow=允许做什么 / deny=禁止做什么)。既是目标
// 拆解器 round-0 抽约束的提交工具,也是主 agent 运行时补约束的工具——同一受管工具,可在 web
// 端改描述/schema、按 agent 绑定。约束会被注入 planner/worker 的系统提示以约束探索边界。
func (t *ToolSet) setConstraints() actool.CoreTool {
	return writeTool("set_constraints",
		"【이 작업】에 탐색 경계를 정하는 작업 제약을 추가합니다: type=allow(허용 작업) 또는 deny(금지 작업).\n"+
			"제약은 \"할 수 있거나 할 수 없는 작업\"의 규정(예: \"현재 포트만 테스트하며 다른 포트는 스캔 금지\", \"운영 DB 쓰기 금지\", \"수동적 정찰만 허용\")이며 목표나 공격 단계가 아닙니다.\n"+
			"★일괄 제출 우선: 여러 제약을 constraints 배열로 한 번에 제출하세요. 반환 ids는 길이·순서가 같습니다(실패 항목 id=0, 상세는 errors). 단일 항목은 constraints를 생략하고 최상위 text/type을 전달하세요.\n"+
			"작업 목표/설명에 【명시된】 제약만 등록하고 지어내지 마세요. 유형이 불확실하면 더 보수적인 deny를 사용하세요.",
		obj(map[string]any{
			"constraints": map[string]any{"type": "array", "description": "【우선 사용】추가할 제약 배열이며 순서대로 처리합니다. 각 요소: text(필수, 제약 하나)+type(allow|deny). 반환 ids는 길이·순서가 같습니다.", "items": map[string]any{"type": "object"}},
			"text":        str("[단일] 작업 제약 하나의 내용"),
			"type":        str("[단일] allow(허용) 또는 deny(금지). 생략하면 deny로 처리"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("set_constraints 비활성: ExplorationStore가 초기화되지 않았습니다"), nil
			}
			var a struct {
				Constraints    []constraintItem `json:"constraints"`
				constraintItem                  // 单条模式:顶层 text/type
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Constraints) > 0
			items := a.Constraints
			if !batch {
				items = []constraintItem{a.constraintItem}
			}
			ids := make([]int64, len(items))
			errs := map[string]string{}
			for i, it := range items {
				id, err := t.addOneConstraint(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
			}
			if !batch { // 单条:保持简单返回
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("constraint added: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

func (t *ToolSet) addHint() actool.CoreTool {
	return t.writeExpTool("add_hint", "사용자/메인 에이전트의 전략 힌트를 탐색 그래프에 추가합니다. 계획자가 다음에 의도를 생성할 때 읽습니다.\n"+
		"★일괄 제출 우선: 여러 힌트를 hints 배열로 한 번에 제출하세요(개별 호출보다 왕복 횟수 절약). 반환 ids는 hints와 길이·순서가 같습니다(실패 항목 id=0, 상세는 errors). 단일 항목은 hints를 생략하고 최상위 text를 전달하세요.",
		obj(map[string]any{
			"hints":        map[string]any{"type": "array", "description": "【우선 사용】추가할 힌트 배열이며 순서대로 처리합니다. 각 요소는 아래 최상위 필드(text/asset_ids/traffic_refs)와 같으며 반환 ids는 길이·순서가 같습니다.", "items": obj(map[string]any{"text": str("힌트 내용"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "traffic_refs": HintTrafficSchema()})},
			"text":         str("[단일] 힌트 내용(예: '인증 후 인터페이스를 집중 조사')"),
			"traffic_refs": HintTrafficSchema(),
			"asset_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "연결할 자산 id(선택 사항, 0/1/여러 개)"},
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Hints    []hintItem `json:"hints"`
				hintItem            // 单条模式：顶层 text/asset_ids
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Hints) > 0
			items := a.Hints
			if !batch {
				items = []hintItem{a.hintItem}
			}

			ids := make([]int64, len(items))
			errs := map[string]string{}
			var addedTexts []string
			for i, it := range items {
				id, err := t.addOneHint(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
				addedTexts = append(addedTexts, strings.TrimSpace(it.Text))
			}
			if len(addedTexts) > 0 {
				// 唤醒 planner（整批一次）。优先 notifyHint：一次 add_hint 记一条「人新增了
				// N 条战略提示：…」触发，让 planner 明确"本轮由新增 hint 触发"并看到提示内容；
				// 未接该回调时退回纯 notify（bare wake，hint 仍折在图里供其自行读取）。
				switch {
				case t.notifyHint != nil:
					t.notifyHint(addedTexts)
				case t.notify != nil:
					t.notify()
				}
			}

			if !batch { // 单条：保持原返回
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("hint added: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

// killWorkTool lets the planner terminate a single running work (by intent id).
func (t *ToolSet) killWorkTool() actool.CoreTool {
	return t.writeExpTool("kill_work", "실행 중인 의도(work)를 종료합니다. 방향이 어긋났거나 무의미한 탐색을 중지할 때 사용합니다. 의도는 stopped로 표시되며 자동 재할당되지 않습니다. 먼저 get_worker_output으로 무엇을 하는지 확인한 뒤 결정하세요.",
		obj(map[string]any{"intent_id": idp("종료할 의도 id(= work 식별자)")}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.killWork == nil {
				return actool.Errorf("현재 kill_work를 사용할 수 없습니다"), nil
			}
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id는 필수입니다"), nil
			}
			node, err := t.ts.GetNode(id)
			if err != nil || node == nil || node.Kind != db.KindIntent {
				return actool.Errorf("intent_id는 이 작업의 의도여야 합니다(연결된 작업 의도는 읽기 전용)"), nil
			}
			if err := t.killWork(id); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(fmt.Sprintf("의도 %d의 work에 종료 신호를 보냈습니다", id)), nil
		})
}

// steerWorkTool lets the planner inject a mid-run course-correction into a running
// work WITHOUT killing it: the message reaches the worker before its next tool call,
// which re-plans its next step (already-gathered context is kept). For in-intent
// nudges ("停做 X、聚焦 Y"); if the whole direction is wrong use kill_work + a new intent.
func (t *ToolSet) steerWorkTool() actool.CoreTool {
	return t.writeExpTool("steer_work", "실행 중인 의도(work)에 실시간으로 방향 조정 지시를 주입합니다. 중단하거나 기존 진행을 잃지 않고 Worker가 다음 동작 전에 지시를 받아 조정합니다. \"X 대신 Y에 집중\" 같은 【의도 안에서의】 조정에 사용하세요. 방향 전체가 잘못되었으면 kill_work 후 새 의도를 배정해야 합니다. 먼저 get_worker_output으로 진행 내용을 확인하는 것을 권장합니다.",
		obj(map[string]any{
			"intent_id": idp("방향을 조정할 의도 id(= work 식별자)"),
			"message":   str("Worker에 보낼 방향 조정 지시. 무엇을 멈추고 어디로 전환할지 명확히 설명하세요"),
		}, "intent_id", "message"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.steerWork == nil {
				return actool.Errorf("현재 steer_work를 사용할 수 없습니다"), nil
			}
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
				Message  string          `json:"message"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id는 필수입니다"), nil
			}
			node, err := t.ts.GetNode(id)
			if err != nil || node == nil || node.Kind != db.KindIntent {
				return actool.Errorf("intent_id는 이 작업의 의도여야 합니다(연결된 작업 의도는 읽기 전용)"), nil
			}
			if strings.TrimSpace(a.Message) == "" {
				return actool.Errorf("message는 필수입니다"), nil
			}
			if err := t.steerWork(id, a.Message); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(fmt.Sprintf("의도 %d의 work에 방향 조정 지시를 주입했습니다(다음 단계부터 적용)", id)), nil
		})
}

// getWorkerOutput returns a work's final (or截至中止时的) conclusion text by intent id.
func (t *ToolSet) getWorkerOutput() actool.CoreTool {
	return t.readExpTool("get_worker_output", "이 작업 또는 직접 연결된 작업의 의도(work)에 대한 최신 실행 결과를 조회합니다. 연결된 작업 결과는 source_task_id/inherited=true가 포함되며 읽기 전용입니다. 최신 실행의 result가 없으면 해당 실행의 마지막 text를 반환하며, 재개 전의 결과를 새 결과로 대신하지 않습니다. intent_state와 output_kind를 함께 확인하세요. final_text는 기존 필드명이며 작업 전체의 검증 완료를 뜻하지 않습니다.",
		obj(map[string]any{"intent_id": idp("의도 id(= work 식별자)")}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id는 필수입니다"), nil
			}
			intentNode, err := t.ts.GetNodeWithSources(id)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if intentNode == nil || intentNode.Kind != db.KindIntent {
				return actool.Errorf("intent_id가 이 작업 또는 직접 연결된 작업에 속하지 않습니다"), nil
			}
			pick, err := t.ts.LatestWorkerOutputWithSources(id)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if pick == nil {
				result := map[string]any{"intent_id": id, "intent_state": intentNode.State, "output_kind": "none", "final_text": "(현재 실행에는 아직 출력이 없습니다)", "has_output": false}
				if intentNode.BlockedReason != "" {
					result["blocked_reason"] = intentNode.BlockedReason
					if intentNode.BlockedReason == db.IntentBlockedActivityStorage {
						result["final_text"] = "현재 실행의 활동 기록을 저장하지 못했습니다. 과거 실행 결과를 현재 결과로 사용하지 마세요. 기존 근거와 과거 출력은 node_detail/get_worker_trace로 확인할 수 있습니다."
					}
				}
				if intentNode.Inherited {
					inheritedMap(result, intentNode.SourceTaskID)
				}
				return jsonResult(result)
			}
			detail := pick.Detail
			if detail == "" {
				detail = pick.Summary
			}
			result := map[string]any{
				"intent_id": id, "final_text": detail,
				"summary": pick.Summary, "is_error": pick.IsError,
				"intent_state": intentNode.State, "output_kind": pick.Kind,
				"activity_id": pick.ID, "created_at": pick.CreatedAt, "has_output": true,
			}
			if intentNode.BlockedReason != "" {
				result["blocked_reason"] = intentNode.BlockedReason
			}
			var metadata map[string]any
			_ = json.Unmarshal(pick.Metadata, &metadata)
			result["run_boundary_known"] = metadata["run_start_activity_id"] != nil
			if runID, ok := metadata["run_id"].(string); ok && runID != "" {
				result["run_id"] = runID
			}
			if intentNode.Inherited {
				inheritedMap(result, intentNode.SourceTaskID)
			}
			return jsonResult(result)
		})
}

// traceSteps renders summary-only trace rows, re-truncating each summary to 100
// chars — the stored summary is capped at 200 for the UI transcript; the trace
// tools want it tighter since a whole work's step list is many rows.
func traceSteps(acts []db.Activity) []map[string]any {
	steps := make([]map[string]any, 0, len(acts))
	for i := range acts {
		step := map[string]any{
			"step_id": acts[i].ID, "kind": acts[i].Kind, "tool": acts[i].Tool,
			"is_error": acts[i].IsError, "summary": firstLine(acts[i].Summary, 100),
		}
		if acts[i].Inherited {
			inheritedMap(step, acts[i].SourceTaskID)
		}
		steps = append(steps, step)
	}
	return steps
}

// getWorkerTrace exposes a work's execution PROCESS (not just its final output):
// list step summaries, keyword-search within one work, or pull full detail of a
// few specific steps. Thinking steps are excluded everywhere.
func (t *ToolSet) getWorkerTrace() actool.CoreTool {
	return t.readExpTool("get_worker_trace",
		"의도(work)의 【실행 과정】을 조회합니다(최종 결론만 주는 get_worker_output과 구분). 세 가지 사용법:\n"+
			"① intent_id만 전달 → 각 단계의 요약 흐름 반환(summary≤100자, step_id 포함. 동작 개요만 있고 전체 출력은 없음).\n"+
			"② intent_id + q → 키워드가 일치한 단계 요약만 반환(요약과 전체 출력 모두 검색. summary만 제공하므로 내용은 ③ 사용).\n"+
			"③ intent_id + step_ids → 해당 단계의 전체 내용(detail) 반환. 한 번에 최대 5개이며 초과 시 앞 5개만 반환하고 notice/omitted_step_ids에 조회하지 않은 항목을 표시합니다.\n"+
			"일반적인 흐름: ①/②로 의심스러운 단계의 step_id를 찾고 ③으로 전체 출력을 조회하세요. thinking 단계는 제외합니다. 직접 연결된 작업의 과거 trace도 지원하며 결과는 source_task_id/inherited=true가 포함되고 읽기 전용입니다.",
		obj(map[string]any{
			"intent_id": idp("의도 id(= work 식별자)"),
			"q":         str("키워드: 요약/전체 출력에 일치하는 단계만 반환(선택 사항, step_ids와 함께 사용할 수 없음)"),
			"step_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "전체 내용을 조회할 step_id(①/②에서 반환된 값. 한 번에 최대 5개, 초과 시 앞 5개만 반환하고 나머지는 omitted_step_ids에 나열)"},
			"limit":     intp("요약 흐름/검색의 반환 상한(선택 사항)"),
		}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IntentID json.RawMessage   `json:"intent_id"`
				Q        string            `json:"q"`
				StepIDs  []json.RawMessage `json:"step_ids"`
				Limit    int               `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id는 필수입니다"), nil
			}
			intentNode, nodeErr := t.ts.GetNodeWithSources(id)
			if nodeErr != nil {
				return actool.Errorf(nodeErr.Error()), nil
			}
			if intentNode == nil || intentNode.Kind != db.KindIntent {
				return actool.Errorf("intent_id가 이 작업 또는 직접 연결된 작업에 속하지 않습니다"), nil
			}
			// ③ detail drill-down by step ids, thinking excluded by the store.
			if len(a.StepIDs) > 0 {
				// Dedup + drop invalid ids first so garbage/duplicates don't eat into
				// the per-call cap. detail is returned in full (untruncated), so the
				// cap bounds one tool result; over the cap we serve the first N and
				// tell the model exactly which ids were deferred, instead of erroring
				// and forcing it to re-plan the call.
				const maxStepIDs = 5
				var ids []int64
				seen := make(map[int64]bool)
				for _, raw := range a.StepIDs {
					if v := pid(raw); v > 0 && !seen[v] {
						seen[v] = true
						ids = append(ids, v)
					}
				}
				var omitted []int64
				if len(ids) > maxStepIDs {
					omitted = append(omitted, ids[maxStepIDs:]...)
					ids = ids[:maxStepIDs]
				}
				acts, err := t.ts.ActivityByIDsWithSources(ids)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				steps := make([]map[string]any, 0, len(acts))
				for i := range acts {
					if acts[i].NodeID == nil || *acts[i].NodeID != id || acts[i].Inherited != intentNode.Inherited ||
						(acts[i].Inherited && acts[i].SourceTaskID != intentNode.SourceTaskID) {
						continue
					}
					step := map[string]any{
						"step_id": acts[i].ID, "kind": acts[i].Kind, "tool": acts[i].Tool,
						"is_error": acts[i].IsError, "detail": acts[i].Detail,
					}
					if acts[i].Inherited {
						inheritedMap(step, acts[i].SourceTaskID)
					}
					steps = append(steps, step)
				}
				result := map[string]any{"intent_id": id, "steps": steps, "returned_step_ids": ids}
				if len(omitted) > 0 {
					// returned_step_ids/omitted_step_ids let the model decide programmatically
					// whether another call is worth it; the notice states the same in prose.
					result["omitted_step_ids"] = omitted
					result["notice"] = fmt.Sprintf(
						"한 번에 최대 %d개 단계의 전체 내용을 조회할 수 있습니다. 이번에는 앞 %d개(%v)를 반환했으며 나머지 %d개는 %v입니다."+
							"이 내용으로 충분히 파악할 수 있으면 나머지 단계를 조회할 필요가 없습니다. 더 필요하면 해당 step_id로 다시 호출하세요.",
						maxStepIDs, len(ids), ids, len(omitted), omitted)
				}
				if intentNode.Inherited {
					inheritedMap(result, intentNode.SourceTaskID)
				}
				return jsonResult(result)
			}
			// ①/② summary stream, optionally keyword-filtered; 100-char summaries.
			var acts []db.Activity
			var err error
			if strings.TrimSpace(a.Q) != "" {
				acts, err = t.ts.ActivityTraceSearchWithSources(id, a.Q, a.Limit)
			} else {
				acts, err = t.ts.ActivityTraceWithSources(id, a.Limit)
			}
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			result := map[string]any{"intent_id": id, "steps": traceSteps(acts)}
			if intentNode.Inherited {
				inheritedMap(result, intentNode.SourceTaskID)
			}
			return jsonResult(result)
		})
}

// searchAllWorkerTraces keyword-searches EVERY work's process in this task — for
// finding what a worker saw but never wrote back as a fact. Returns only matching
// summaries (≤100 chars), each tagged with its intent_id for follow-up drill-down.
func (t *ToolSet) searchAllWorkerTraces() actool.CoreTool {
	return t.readExpTool("search_all_worker_traces",
		"【시스템이 대부분의 정보를 이미 제공하므로 보통 사용을 권장하지 않습니다】 【이 작업의 다른 work 실행 과정】에서 키워드(q)를 검색하여 Worker가 봤지만 fact에 기록하지 않은 내용(경로/token/오류 등)을 찾습니다."+
			"자신의 의도 단계는 이미 컨텍스트에 있으므로 자동으로 제외합니다."+
			"일치하는 단계 요약(summary≤100자)만 반환하며 각 항목에 intent_id가 있습니다. get_worker_trace(intent_id, step_ids=[...])로 전체 내용을 조회하세요.",
		obj(map[string]any{
			"q":     str("키워드(모든 work 단계의 요약+전체 출력 검색)"),
			"limit": intp("반환 상한, 기본 100(선택 사항)"),
		}, "q"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Q     string `json:"q"`
				Limit int    `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Q) == "" {
				return actool.Errorf("q는 필수입니다"), nil
			}
			// 排除调用者自身这条意图的步骤（worker 的自有 trace 已在其上下文里）。
			acts, err := t.ts.ActivityTraceSearchAllWithSources(t.ownerNode, a.Q, a.Limit)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			hits := make([]map[string]any, 0, len(acts))
			for i := range acts {
				var intent int64
				if acts[i].NodeID != nil {
					intent = *acts[i].NodeID
				}
				hit := map[string]any{
					"intent_id": intent, "step_id": acts[i].ID, "worker": acts[i].Worker,
					"kind": acts[i].Kind, "tool": acts[i].Tool, "is_error": acts[i].IsError,
					"summary": firstLine(acts[i].Summary, 100),
				}
				if acts[i].Inherited {
					inheritedMap(hit, acts[i].SourceTaskID)
				}
				hits = append(hits, hit)
			}
			return jsonResult(map[string]any{"query": a.Q, "hits": hits})
		})
}

// listWorkerTraces gives a worker (which has no graph_overview and can't see the
// intent graph) a lightweight index of the works in this task — intent_id +
// one-line summary + state — so it can DISCOVER which works to inspect via
// get_worker_trace. Without this a worker only knows intent_ids that come back
// from search_all_worker_traces hits. Excludes still-open intents (not yet run →
// no process to inspect).
func (t *ToolSet) listWorkerTraces() actool.CoreTool {
	return t.readExpTool("list_worker_traces",
		"【시스템이 대부분의 정보를 이미 제공하므로 보통 사용을 권장하지 않습니다】 이 작업에서 【실행한 work(의도)】의 목록을 반환합니다: intent_id + 방향 한 문장(summary) + 상태."+
			"Worker는 탐색 그래프를 볼 수 없으므로 살펴볼 work를 찾는 데 사용하세요. get_worker_trace(intent_id)로 단계를 보고 get_worker_trace(intent_id, step_ids=[...])로 상세를 조회하세요."+
			"실행된 항목(running/done/exhausted/blocked/stopped)만 포함하며 아직 실행하지 않은 open은 제외합니다. 작업 경계는 여전히 할당받은 의도 하나입니다. 다른 work는 관찰 재사용과 중복 작업 방지 목적으로만 보세요.",
		obj(map[string]any{
			"q":     str("summary 키워드 필터(선택 사항)"),
			"limit": intp("반환 상한, 기본 50(선택 사항)"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Q     string `json:"q"`
				Limit int    `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			limit := a.Limit
			if limit <= 0 {
				limit = 50
			}
			all, err := t.ts.ListByKindWithSources(db.KindIntent, 500)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			q := strings.ToLower(strings.TrimSpace(a.Q))
			out := make([]map[string]any, 0, limit)
			for _, n := range all {
				if n.Inherited && n.State == "running" {
					continue
				}
				switch n.State {
				case "running", "done", "exhausted", "blocked", "stopped": // has run → has a process
				default:
					continue
				}
				var p map[string]any
				_ = json.Unmarshal(n.Payload, &p)
				summary, _ := p["summary"].(string)
				if q != "" && !strings.Contains(strings.ToLower(summary), q) {
					continue
				}
				item := map[string]any{"intent_id": n.ID, "summary": summary, "state": n.State}
				if n.Inherited {
					inheritedMap(item, n.SourceTaskID)
				}
				out = append(out, item)
				if len(out) >= limit {
					break
				}
			}
			return jsonResult(map[string]any{"works": out})
		})
}

// PlannerTools is the read + intent-generation + goal-judgement tool set.
func (t *ToolSet) PlannerTools() []actool.CoreTool {
	return []actool.CoreTool{
		t.graphOverview(), t.listFindings(), t.listFacts(), t.nodeDetail(),
		// cold-digest §6.1: restore folded cold nodes (digest body → members → detail).
		t.expandDigest(),
		t.getWorkerOutput(), t.getWorkerTrace(), t.searchAllWorkerTraces(), t.listGoals(), t.addIntent(), t.proveGoal(), t.goalMet(),
		t.killWorkTool(), t.steerWorkTool(),
		// report_finding：规划态势研判时若自身已确证漏洞，可直接登记（与 worker 同工具）。
		t.addFinding(),
		// list_companies：查看企业列表 + scope + 资产数（拿 company_id / 理解归属范围）。
		t.listCompanies(),
		// list_assets：规划时按 DSL 检索全资产库（配合 list_untested_assets 的"范围内未测"视角，
		// 补上"按域名/指纹/端口/状态码等条件在整库里查"的能力）。
		t.listAssets(),
		// add_company_scope：规划时可把域名/IP/CIDR/ICP/关键词纳入某公司的资产范围（自动认领命中资产）。
		t.addCompanyScope(),
		// add_task_scope：主动把整根域/整公司/某子域/IP 纳入本任务测试范围(覆盖度分母)。
		t.addTaskScope(),
		// list_untested_assets：按需查本任务范围内未测资产(类型+分页)，自行决定补测。
		t.listUntestedAssets(),
	}
}
