package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/guard"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// Planner is the event-driven LLM planner (docs §4.3): each time the asset or
// exploration graph changes (debounced), it reads the exploration route, queries
// assets, judges whether the task goal is met, and emits 0..N exploration intents
// into the frontier. It is the sole intent generator.
type Planner struct {
	guard              *guard.Guard
	findingRecorder    FindingRecorder
	connectionIdentity func() string
	prov               llm.Provider
	model              string
	tx                 *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window             int                                    // context window in tokens (for compaction)
	windowFn           func() int                             // optional dynamic task-chain minimum
	maxTurns           int                                    // max agent turns per run (0 = unlimited)
	killWork           func(intentID int64) error             // engine callback to terminate a running work (nil = off)
	steerWork          func(intentID int64, msg string) error // engine callback to steer a running work mid-run (nil = off)
	proxyAddr          string                                 // recording proxy for WebFetch (empty = direct)
	proxyFallback      string
	proxyCACert        string        // recording proxy's CA cert path (HTTPS verify)
	webSearch          WebSearchOpts // web_search tool backend selection (off by default)
	workDir            string        // shared work dir (surfaced in prompt as artifact-output target)
	injectConstraints  func() bool   // resolver: inject task operation constraints into system prompt? (nil = yes)
	nonStreamingFn     func() bool   // resolver: use non-streaming (Complete) path? (nil = streaming)
	maxTokensFn        func() int    // resolver: per-reply output cap (nil/0 = send no cap)
	compactor          *Compactor    // cold-node compaction (§7); nil = disabled
}

func NewPlanner(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *Planner {
	return &Planner{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns}
}

func (p *Planner) SetCompactionWindowResolver(fn func() int) { p.windowFn = fn }

// SetGuard applies the same configured tool rules as the task's workers.
func (p *Planner) SetGuard(g *guard.Guard) { p.guard = g }

// SetCompactor wires the cold-node compactor (cold-digest §7). Called each
// planner wake-up to advance the round counter, maintain cold stamps, and
// (off the hot path) fold cold nodes into digests. nil = feature disabled.
func (p *Planner) SetCompactor(c *Compactor) { p.compactor = c }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (p *Planner) SetNonStreaming(fn func() bool) { p.nonStreamingFn = fn }

func (p *Planner) nonStreaming() bool { return p.nonStreamingFn != nil && p.nonStreamingFn() }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (p *Planner) SetMaxTokens(fn func() int) { p.maxTokensFn = fn }

func (p *Planner) maxTokens() int {
	if p.maxTokensFn == nil {
		return 0
	}
	return p.maxTokensFn()
}

func (p *Planner) compactionWindow() int {
	if p.windowFn != nil {
		return p.windowFn()
	}
	return p.window
}

// SetProxy points the planner's WebFetch at the recording proxy plus the CA cert
// it trusts to verify HTTPS through it (empty addr = direct).
func (p *Planner) SetProxy(addr, caCert string, fallback ...string) {
	p.proxyAddr, p.proxyCACert, p.proxyFallback = addr, caCert, ""
	if len(fallback) > 0 {
		p.proxyFallback = fallback[0]
	}
}

// SetWebSearch selects the web_search backend for the planner (off by default).
func (p *Planner) SetWebSearch(o WebSearchOpts) { p.webSearch = o }

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the planner system prompt. Read per round so the
// settings toggle takes effect without rebuilding the agent. nil = inject (default).
func (p *Planner) SetConstraintInject(fn func() bool) { p.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (p *Planner) wantConstraints() bool { return p.injectConstraints == nil || p.injectConstraints() }

// plannerTodoStore loads each round from the graph, including after a restart or
// provider switch. A failed load must not become a fresh empty plan.
func plannerTodoStore(ctx context.Context, ts *db.ExplorationStore) (*actool.TodoStore, error) {
	state, err := ts.LoadPlannerTodos(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]actool.Todo, len(state.Items))
	for i, item := range state.Items {
		items[i] = actool.Todo{Content: item.Content, Status: actool.TodoStatus(item.Status)}
	}
	revision := state.Revision
	return actool.NewPersistentTodoStore(items, func(ctx context.Context, items []actool.Todo) error {
		stored := make([]db.PlannerTodo, len(items))
		for i, item := range items {
			stored[i] = db.PlannerTodo{Content: item.Content, Status: string(item.Status)}
		}
		next, err := ts.SavePlannerTodos(ctx, stored, revision)
		if err == nil {
			revision = next
		}
		return err
	}), nil
}

// SetKillWork wires the engine's per-work terminate callback so the planner's
// kill_work tool can stop a single running worker.
func (p *Planner) SetKillWork(fn func(intentID int64) error) { p.killWork = fn }

// SetSteerWork wires the engine's per-work steering callback so the planner's
// steer_work tool can inject a mid-run course-correction into a running worker.
func (p *Planner) SetSteerWork(fn func(intentID int64, msg string) error) { p.steerWork = fn }

// renderPlannerTodos formats the persistent planning todo for injection into the
// wake-up prompt (empty when there are no todos yet — first wake-up).
func renderPlannerTodos(items []actool.Todo) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【계획 할 일 목록(이전 회차에 작성했으며 다시 깨어나도 유지됨)】: \n")
	for _, it := range items {
		mark := map[actool.TodoStatus]string{actool.TodoPending: "☐", actool.TodoInProgress: "▶", actool.TodoCompleted: "✔"}[it.Status]
		if mark == "" {
			mark = "☐"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", mark, it.Content))
	}
	b.WriteString("각 항목을 실제 의도와 근거에 대조하세요. pending/in_progress라는 표기만으로 Worker가 수행 중이라고 가정하지 마세요. 실행 중인 의도가 없는 미완료 단계는 배정하고, 필요한 선행 조건이 없다면 이를 확보하거나 확인하는 작업부터 배정하세요. 사실로 충족된 항목만 completed로 표시하고 미검증 단계를 완료로 바꾸지 마세요.")
	return b.String()
}

// TriggerEvent describes what concretely caused this planning round to fire, so
// the planner looks first at the actual change instead of re-scanning the whole
// overview. Kind:
//
//	"done"    — a worker finished intent IntentID (its output conclusion is fetched).
//	"finding" — a worker reported a finding on intent IntentID (Detail = 摘要).
//	"goal"    — the human (via 主 agent 的 set_goals) added one OR MORE goals in a
//	            single call (Goals = 本次新增的目标文本，1+ 条；set_goals 支持批量).
//	"goal_deleted" — the human deleted a goal from 总览的目标管理 (Detail = 被删目标文本).
//	"goal_edited"  — the human edited a goal from 总览的目标管理 (OldGoal→NewGoal 文本).
//	"cancelled" — the human deleted intent IntentID (Detail = 删除原因). The intent is
//	            stopped (not deleted) and the reason is attached to it as a fact.
type TriggerEvent struct {
	Kind     string
	IntentID int64
	Detail   string
	Summary  string   // Kind=="cancelled" 专用：删除前捕获的意图摘要（真删除后节点已不存在，无法再查）
	Goals    []string // Kind=="goal" 专用：本次 set_goals 新增的目标文本（1 条或多条）
	OldGoal  string   // Kind=="goal_edited" 专用：修改前的目标文本
	NewGoal  string   // Kind=="goal_edited" 专用：修改后的目标文本
	Hints    []string // Kind=="hint" 专用：本次 add_hint 新增的提示文本（1 条或多条）
}

// renderTriggers spells out the change(s) that fired this round: for a finished
// worker — which intent + its output conclusion; for a finding — which intent +
// what was found. Empty for time/heartbeat wakes. Reads the store (best-effort;
// a blank field never blocks the round).
func renderTriggers(ts *db.ExplorationStore, evs []TriggerEvent) string {
	if len(evs) == 0 || ts == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【이번 회차를 유발한 실제 변경 사항(먼저 읽고 방향 보완 여부를 결정)】: ")
	for _, ev := range evs {
		switch ev.Kind {
		case "goal":
			if len(ev.Goals) == 1 {
				b.WriteString(fmt.Sprintf("\n- 사용자(메인 에이전트)가 목표를 추가했습니다: %s — 새로 달성해야 할 목표입니다. 아직 대응하는 의도가 없으면 탐색 방향을 보완하세요.", ev.Goals[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- 사용자(메인 에이전트)가 목표 %d개를 추가했습니다: %s — 모두 새로 달성해야 할 목표입니다. 대응하는 의도가 없는 목표마다 탐색 방향을 보완하세요.", len(ev.Goals), strings.Join(ev.Goals, "；")))
			}
		case "hint":
			if len(ev.Hints) == 1 {
				b.WriteString(fmt.Sprintf("\n- 사용자(메인 에이전트)가 전략 힌트를 추가했습니다: %s — 탐색 그래프에 반영되었습니다. 아직 대응하는 의도가 없으면 이를 바탕으로 탐색 방향을 조정/보완하세요.", ev.Hints[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- 사용자(메인 에이전트)가 전략 힌트 %d개를 추가했습니다: %s — 모두 탐색 그래프에 반영되었습니다. 각각을 바탕으로 탐색 방향을 조정/보완하세요.", len(ev.Hints), strings.Join(ev.Hints, "；")))
			}
		case "goal_deleted":
			b.WriteString(fmt.Sprintf("\n- 사용자가 이 목표를 삭제했습니다: %s — 목표가 제거되었으므로 남은 목표/방향을 다시 판단하세요(삭제된 목표에 의도를 더 배정할 필요는 없습니다).", ev.Detail))
		case "goal_edited":
			b.WriteString(fmt.Sprintf("\n- 사용자가 목표를 「%s」에서 「%s」로 변경했습니다. 새 목표에 맞게 탐색 방향을 조정하세요(기존 방향이 더 이상 적합하지 않으면 배정을 중단하세요).", ev.OldGoal, ev.NewGoal))
		case "finding":
			b.WriteString(fmt.Sprintf("\n- 의도 #%d(%s)의 Worker가 finding을 보고했습니다: %s", ev.IntentID, intentSummary(ts, ev.IntentID), ev.Detail))
		case "cancelled":
			// 意图内容优先用删除时捕获的 Summary（真删除后节点已不存在，intentSummary 查不到）。
			sm := ev.Summary
			if sm == "" {
				sm = intentSummary(ts, ev.IntentID)
			}
			b.WriteString(fmt.Sprintf("\n- 사용자가 의도 #%d를 삭제했습니다. 의도 내용: %s, 삭제 이유: %s. 이 의도는 삭제되어 더 이상 실행되지 않습니다. 이를 반영해 다시 계획하세요.", ev.IntentID, sm, ev.Detail))
		default: // "done"
			b.WriteString(fmt.Sprintf("\n- 의도 #%d(%s)의 Worker가 종료되었습니다. 결과: %s", ev.IntentID, intentSummary(ts, ev.IntentID), workerOutput(ts, ev.IntentID)))
			if fids := factIDsYielded(ts, ev.IntentID); fids != "" {
				b.WriteString(fmt.Sprintf("; 이 의도에서 새로 생성한 사실 id: %s ", fids))
			}
		}
	}
	b.WriteString("\n(전체 세부 정보는 node_detail / get_worker_output / list_findings로 다시 조회할 수 있습니다.)")
	return b.String()
}

// factIDsYielded lists the fact ids an intent produced this run as "#12、#15", so the
// planner can jump straight to the round's incremental facts. Empty (best-effort) when
// the intent yielded no facts or the lookup fails.
func factIDsYielded(ts *db.ExplorationStore, id int64) string {
	ids, err := ts.FactsYielded(id)
	if err != nil || len(ids) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ids))
	for _, fid := range ids {
		node, err := ts.GetNode(fid)
		if err != nil || isHTTPObservationFact(node) {
			continue
		}
		parts = append(parts, fmt.Sprintf("#%d", fid))
	}
	return strings.Join(parts, "、")
}

// intentSummary reads an intent node's one-line summary (best-effort, "?" on miss).
func intentSummary(ts *db.ExplorationStore, id int64) string {
	n, err := ts.GetNode(id)
	if err != nil || n == nil {
		return "?"
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok && s != "" {
			return s
		}
	}
	return "?"
}

// workerOutput returns the finished worker's conclusion for an intent — the last
// 'result' (else 'text') activity's full detail. Same source get_worker_output uses.
func workerOutput(ts *db.ExplorationStore, id int64) string {
	pick, err := ts.LatestWorkerOutput(id)
	if err != nil {
		return "(출력 조회 실패)"
	}
	if pick == nil {
		return "(이 work에는 아직 출력 기록이 없습니다)"
	}
	out := pick.Detail
	if out == "" {
		out = pick.Summary
	}
	return out
}

// renderGraphOverview folds the pre-computed graph_overview snapshot into the
// wake-up prompt so the planner starts each round with the full situation in
// hand — saving the round-trip it would otherwise spend calling the tool. It is
// the exact same JSON graph_overview would return; deeper detail is still one
// tool call away (node_detail / list_facts / …).
func renderGraphOverview(data map[string]any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back to the model calling graph_overview itself
	}
	return "\n\n【이번 회차 현황(graph_overview를 미리 조회한 것으로 해당 도구의 반환값과 같습니다. 세부 정보가 필요하면 node_detail/list_facts 등을 호출하세요)】: \n" + string(b)
}

// plannerDefaultTmpl is the built-in EDITABLE body (段 [A]) of the planner prompt,
// seeded into agent_prompts. Goal is a {{.Goal}} template var; the 中间产物输出规约
// tail is code-owned (artifactSpec) and appended by plannerSystem after rendering.
const plannerDefaultTmpl = `당신은 사이버 보안 플랫폼의 허가된 모의 침투 테스트 시스템에서 "계획자" 역할을 맡으며, 그래프가 바뀔 때마다 자주 깨어납니다. 전체 범위를 보면서 현황 파악 → 결과와 미검증 질문 확인 → 필요한 후속 의도 배정 → 목표 판단 순서로 진행하세요. 실행자가 아닌 계획자이므로 이번 회차의 산출물은 【의도 생성/명확화】 또는 【목표 판단】이어야 하며, plan에서 직접 작업을 수행해서는 안 됩니다.

작업 목표: {{.Goal}}

**이번 회차에 의도를 몇 개 생성할지 먼저 판단하세요**:
- **필수 조건(최우선)**: 【목표 미달성】이며 【open 또는 running 의도가 하나도 없는 경우】(frontier_open=0이며 running_intents가 비어 있음), 남은 질문, 미완료 결과, 아직 살피지 않은 표면과 대체 방법을 검토하여 다음 검증을 하나 이상 배정하세요. 필요한 전제가 없으면 허용 범위에서 그 전제를 확보하거나 가능성을 확인하는 작업도 탐색입니다. 전제를 아직 준비하지 않았다는 이유로 종료하지 마세요. 실제 사용자 제약·외부 장애로 남은 모든 검증이 불가능함을 근거로 확인한 경우에만 미달성 상태와 필요한 사용자 조치를 명시하고 대기하세요. 알려진 방향이 recent_done에만 있어도 아래 done/exhausted/blocked 판단에 따라 새 방향을 열지 판단하세요.
- 위 필수 조건에 해당하지 않으면 **의도 0개도 정상적인 결과이지만 정당한 이유가 있어야 합니다**("적게 배정하면 안전하다"는 기본값이 아님). ① **이미 다루는 방향**: 생각한 방향이 모두 open/running 의도에서 처리 중입니다(같은 대상·조건·질문을 동시에 중복 배정하지 않음. 다른 계정·조건의 비교나 필요한 재현 검증은 구분). ② **의존 결과 대기**: 다음 단계에 실행 중인 work의 결과가 필요한데 아직 나오지 않았습니다(억지로 배정하면 선행 조건이 없어 헛돌게 되므로 다음 그래프 갱신 후 배정).
- 반대로 【아직 다루지 않고 실행 중인 work에도 의존하지 않는】 새 방향이 있거나 목표 미달성이며 범위 안에 미검증 영역이 남아 있다면 배정해야 합니다. 의도 0개를 편의상의 기본값으로 삼지 마세요.

**깨어날 때마다 수행할 의사결정 절차**:

1. **전체 현황은 이 프롬프트 아래에 제공됩니다**(graph_overview의 반환값이므로 다시 호출할 필요 없음): task(원래 제목+목표/루트 노드), 자산 수, goals+상태, open/running/recent_done 의도, sites_without_endpoints(엔드포인트가 없어 탐색 후보가 되는 사이트), facts(탐색 사실 수이며 취약점과 구분), recent_facts({id,summary,confidence?}).
   - **범위**: 현재 작업의 목표·진행 중 의도와 함께 직접 연결된 작업의 사실·발견·종료된 의도 등 읽기 전용 근거가 제공됩니다. source_task_id/inherited 표기를 확인하고, 연결된 작업의 노드·상태·범위는 수정하지 마세요. **자산 그래프는 전역 공유**되며 현재 작업과 직접 연결된 작업의 scope·관찰을 구분해 사용하세요. 이 작업과 무관한 자산과 간접 연결된 작업은 포함된 것으로 가정하지 마세요.
   - **계보**: 각 의도에는 parents(상위 근거 fact/finding ID)와 yields(이 의도가 생성한 fact/finding ID)가 있고 recent_facts 각 항목에는 from_intent(생산 의도 ID)가 있습니다. from_intent와 parent_ids는 서로 다른 계약입니다. 실제 근거 fact/finding에서 새로운 방향을 도출하세요.
   - **부정적/불확실한 관찰**(recent_facts의 "포트 닫힘/주입 불가" 등)은 Worker의 관찰이며 확정된 결론이 아닙니다. 받아들이기 전에 node_detail(id)로 evidence를 읽으세요. 증거가 충분하고 confidence=observed이며 수단을 충분히 시도한 경우에만 해당 방향이 잠정적으로 막혔다고 판단하세요. evidence가 없거나 "그렇게 보임/한 번만 시도" 수준이거나 confidence=inferred이면 【아직 규명되지 않음】으로 다루고, 범위 내이며 다른 의도가 다루지 않으면 재검증을 배정하여 확인하거나 반박하세요. 재검증 횟수만으로 방향을 닫지 마세요. 정상 대조, 다른 입력·계정·상태·경로, 재현성 또는 오류 복구로 결론을 바꿀 수 있는 검증이 남았는지 판단하세요. 충분한 근거로 배제한 조건과 아직 시험하지 않은 조건을 구분하세요.
   - **더 자세한 정보가 필요할 때만 호출**: list_facts(최신순 페이지 조회, 기본 20개, q 필터와 before 페이지 이동, total/has_more 포함), list_findings(전체 취약점), node_detail(id)(전체 증거/상세. 목록/recent_facts에는 요약만 있음), list_assets(pull: q 검색, type/company_id/task_id 필터, 페이지 조회 또는 id/ids 직접 조회), asset_neighbors. 자산은 전역 공유되므로 기본적으로 전체를 가져오지 마세요.

2. **(선택 사항, 시작 시에만 매우 가볍게) 대상을 이해하기 위한 확인**: 그래프에 fact가 거의 없고(recent_facts가 사실상 비어 있으며 작업 시작 직후) 현황만으로 초기 의도를 구체화하기 어려울 때만 Bash 등으로 극소량의 읽기 전용 확인을 하세요(예: curl 1–2회로 첫 화면/특징 확인). **허용되는 유일한 산출물은 더 정확해진 한 문장 의도 설명**입니다. 취약점 발견/검증/익스플로잇이나 엔드포인트/디렉터리/인자 열거 결과가 아닙니다(이는 Worker의 일이므로 의도로 배정). 세 가지 필수 경계:
   - 그래프에 Worker가 생성한 fact가 있다면(facts>0 / recent_facts가 비어 있지 않음) 직접 확인하는 것은 【금지】합니다. 기존 fact에 근거해 판단하고 이번 회차에는 "새 의도 배정" 또는 "종료"만 수행하세요. 단서를 더 조사하려면 직접 curl하지 말고 Worker에게 의도를 배정하세요.
   - 시작 시에도 최대 3회(≤3)만 확인한 뒤 멈추세요. 초기 의도를 명확히 하기 위한 것뿐입니다. "빠른 방향 결정" 대신 "깊은 검증"(엔드포인트/디렉터리 순차 열거, id별 시도, 디코딩 체인, 같은 인터페이스 반복 확인, 주입/권한 우회/취약점 테스트·검증 등 Worker가 할 작업)을 하고 있다면 즉시 멈추고 의도로 작성하세요.
   - 기존 사실/현황으로 판단할 수 있으면 확인할 필요가 없습니다.

3. **결과를 확인하고 보완할 방향 결정**: **여기서 "절제"는 【기존 의도를 중복하지 않음】이며 "가능한 적게 배정"한다는 뜻이 아닙니다**. 목표 미달성 시 기본 질문은 "목표에 다가가기 위해 더 깊고 강력하며 아직 다루지 않은 방법이 무엇인가"이지 "이제 마무리할 수 있는가"가 아닙니다. 의도는 【열린 탐색 방향】이며 정해진 유형/메뉴가 아닙니다. 알려진 사실·자산·목표에 따라 판단하고 open + running + recent_done과 하나씩 비교하세요:
   - open/running이 이미 다루는 방향이면 생성하지 마세요(처리 중).
   - recent_done에 있었다면 **각 항목의 state로 어떻게 종료되었는지 먼저 구분한 뒤 결정**하세요:
     · **done(정상 반환)**: 의도에 적힌 모든 질문을 검증했다는 뜻은 아닙니다. 후속 배정·완료 판단에 관련된 종료 의도에서 실제 확인한 범위와 남은 질문을 구분하세요. 이미 전달된 결과·상세 근거를 재사용하고 부족할 때만 node_detail/get_worker_output으로 추가 조회하세요. 현재 목표에 필요한 미검증 질문은 해당 근거를 parent_ids로 연결해 이어서 검증하도록 배정하세요. 필요한 전제도 확보할 수 있는지 확인하고, 실제로 제외하거나 기다려야 하는 질문만 근거와 재개 조건을 남기세요. 재현·정상 대조·환경 오류 복구나 다른 조건의 검증은 앞선 결과가 있다는 이유로 중복 취급하지 마세요. 이미 충분히 검증된 동일 조건을 단순 재서술하는 배정과 구분하고, 이번에 답할 질문과 이전 근거를 명시하세요.
     · **exhausted(예산 소진으로 중간에 중단되어 일부만 기록) / blocked(모델 또는 네트워크 실패로 탐색을 거의 못 함)**: 모두 중간에 정상적으로 끝나지 않아 정보가 불완전합니다. 먼저 get_worker_trace / get_worker_output으로 실제 진행 지점과 장애 원인을 확인한 뒤 선택하세요. 돌파 직전에 예산이 끊겼으면 "지난 진행에서 계속"을 배정하고, 순수한 외부 오류로 실행하지 못했으면(blocked에 흔함) 같은 방향을 다시 배정하며, 매번 같은 곳에서 막히면 방법/방향을 바꾸세요. 판단 근거는 항상 trace의 실제 진행이며 state 자체가 아닙니다.
   - 어떤 의도도 다루지 않은 완전히 새로운 방향이면 생성하세요.
   - 알려진 방향을 모두 open/running 의도가 다루면 생성하지 말고 종료하세요(실행/대기 중인 work의 진행을 기다림). 그러나 recent_done만 남고 open/running이 없으며 목표 미달성이면 위 필수 조건에 따라 새 방향을 열거나 이어서 배정해야 합니다. 대상 HTTP 오류나 본문의 거부 문구는 도구 자체의 차단이 아닙니다. fact의 차단 요약은 도구가 실행 전에 반환한 명시적 거부·가로채기 결과와 그 사유로 확인하세요. 대상 오류는 조건을 확인하고 허용된 다른 방법으로 검증하세요. 실제 사용자·플랫폼 차단도 명시된 범위에만 적용하세요.
	- **유망한 경로의 깊이를 우선하세요**: coverage는 관찰이 없는 자산을 찾는 참고 지표입니다. 가치 있는 진입점(RCE/권한 상승/데이터 유출 가능)이 나오면 자산별 수치를 평준화하려고 얕게 훑기보다 그 경로의 작동 원리와 영향을 깊게 검증하세요. 관찰 비율만으로 검증을 끝내지 마세요.
   - **경로 다양성을 유지하고 너무 일찍 한곳에 집중하지 마세요**: 목표 미달성인데 기존 의도가 같은 경로/진입점에 몰려 있고 【본질적으로 다른】 미탐색 방향(다른 진입면/자산 유형/익스플로잇 체인)이 있다면 같은 경로의 유사 의도 대신 다른 방향을 우선 보완하세요(표현이 아닌 실질 차이를 판단). 해당 방향도 기존 의도가 다루고 있다면 생성하지 마세요. 이상적으로는 작동 원리가 다른 경로 2–3개가 공존해야 합니다(예: "업로드 체인"과 "인증 우회"). 특정 경로가 【목표에 가까워짐】을 증명한 뒤에만 자원을 집중하세요. **다양성도 항상 최상단 【작업 제약】을 따라야 합니다**. 제약에서 제외한 진입면/포트/호스트/작업은 본질적으로 달라도 의도를 생성하지 마세요.

   **한 의도에는 동일한 전제와 판단 기준을 공유하는 검증 질문 하나를 맡기세요.** 서로 다른 증거와 완료 기준이 필요한 주제를 한 의도에 나열하지 마세요. 같은 질문의 정상 대조·입력 변형·반례·영향 확인과 관련 자산 비교는 한 Worker가 이어서 깊게 수행하게 하며, 명령·엔드포인트·취약점 이름마다 기계적으로 쪼개지 마세요. 사용자가 요구한 독립 산출물을 배정할 때도 해당 결과와 완료 기준을 명시하세요. 사용자가 허용한 작업과 명시적으로 금지한 작업을 함께 전달하세요. 명시적 제약은 지키되 정상 UI의 존재·사전 삭제 보장·별도 승인을 새 조건으로 붙이지 마세요. UI 없는 API·권한 오류도 허용된 객체·역할·상태로 검증하세요. 정리 실패는 사용자 입력에 따라 기록하세요.

   **같은 질문의 연속 검증은 한 Worker에서 이어가고, 독립 질문 사이의 의존성만 단계별로 배정하세요.** 의존성이 강한 직렬 체인(①→②→③, 다음 단계가 이전 단계의 실제 산출물에 의존)은 한 번에 병렬로 배정하지 마세요(아직 없는 선행 조건을 기다리는 하위 작업은 반복하거나 헛돌기만 함). 서로 다른 의도로 나누어야 하는 직렬 단계와 미완료 질문을 TodoWrite에 기록하고, 선행 조건이 충족된 단계 또는 그 조건을 확보할 단계를 배정하세요. 같은 질문 안의 준비·재현·영향 확인을 별도 Worker로 기계적으로 나누지 마세요. fact가 나온 뒤 다음에 깨어나면(할 일 목록이 프롬프트에 포함됨) 다음 단계를 배정하고 충족된 항목을 completed로 표시하세요. "같은 일"을 두 개로 나누지 마세요("유발 지점 확인"과 "유발 지점 작동"은 같은 단계). 【병렬이며 서로 의존하지 않는】 검증 질문만 여러 의도로 병렬 처리하세요.

4. **제출**: 선별한 새 방향을 【한 번의】 add_intent로 일괄 제출하세요(intents 배열, 가치가 가장 높은 방향 최대 4개, 항목마다 따로 여러 번 호출하지 않음). 이 수는 이번 회차의 배정 수이며 전체 탐색 범위의 상한이 아닙니다. 네 칸에 모든 분야를 넣으려고 독립 질문을 합치지 마세요. 이번에 배정하지 못한 구체적인 질문은 근거 ID와 함께 TodoWrite에 남겨 후속 회차에서 배정하세요:
   - **summary**: 테스트 대상 전체 주소 또는 관련 자산 묶음 + 검증 질문 + 필요한 근거와 종료 조건을 간결하게 설명하세요. 정해진 분류에 끼워 맞추지 마세요. 기존 의도와의 중복 판단은 주로 이 설명을 비교합니다.
   - **asset_ids**: 테스트/공격할 대상 자산 id(가능하면 전달, 0/1/여러 개, list_assets에서 조회). 구체적인 자산(사이트/인터페이스/인자/호스트)을 다루면 반드시 전달하여 중복 확인과 자산 연결에 사용하세요. 여러 자산에 걸치면 모두 전달하고, 구체적인 자산 없는 전역 정찰일 때만 비워 두세요.
   - **parent_ids**: 이 방향의 근거인 fact/finding 노드 id(선택 사항, 0/1/여러 개). 여러 사실을 종합했다면 모두 전달하세요. 상위 의도 자체의 id는 사용할 수 없으며 그 의도에서 나온 fact/finding의 id를 전달하세요. 최상위의 완전히 새로운 방향은 비워 두세요.

5. **목표 판단(후속 질문 검토 후)**: 새 결과와 남은 질문을 검토하고 필요한 후속 의도를 배정한 뒤, 발견/사실로 달성이 증명된 미완료 목표에 prove_goal(goal_id, evidence_id, reason)을 호출해 met로 표시하세요. 해당 목표에 필요한 미검증 질문과 open/running/paused 의도가 남으면 완료 처리하지 마세요. 실행 중인 검증은 결과를 기다리고, 사용자가 일시정지한 작업은 임의로 다른 의도로 재배정하지 말고 재개 지시를 기다리세요. 전제 부족·예산 소진·도구 오류·사용자 조치 대기는 목표 달성의 근거가 아닙니다. **마지막 미완료 목표를 표시하면 시스템이 전체 작업의 완료를 판단합니다**. 마무리는 개별 prove_goal로만 진행하며 별도의 "한 번에 완료" 수단은 없습니다.
   - ⚠️ **정량적 완료 조건 확인(조기 완료 판정 금지)**: prove_goal 전에 각 목표의 단위에 맞는 실제 근거를 확인하세요. coverage.pct는 fact/finding이 연결된 **자산 관찰 비율**이며 정찰 관찰도 포함합니다. 취약점 유형별 검증률·전체 공격면 검증률 또는 실습 문제 해결률로 해석하지 마세요. findings_total도 ARTEX 발견 수이므로 외부 실습의 해결 문제 수와 구분하세요. 유형별 검증이나 flag N개가 목표라면 해당 결과의 증거·미검증 항목·남은 후속 단서를 직접 확인하세요. coverage가 없으면 미측정으로 취급하며, 100%여도 이 수치만으로 목표 달성을 선언하지 마세요.

목표 미달성이면 기존 결과의 한계를 확인하고 더 깊게 검증할 방법을 찾으세요. 사용자 산출물은 필요한 때 작성하되, 결과가 나올 때마다 같은 문서를 새 Worker로 반복 갱신하는 일을 탐색 대신 배정하지 마세요. 최종 정리는 남은 검증을 검토한 뒤 최신 근거를 모아 수행하세요.`

func plannerSystem(goal, dataDir, workDir string) string {
	body := renderSystem("planner", plannerDefaultTmpl, PlannerVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir) + sharedArtifactSpec(workDir) +
		plannerGraphContract + followUpPlannerInstructions + findingReportContract + langDirective(resolveOutputLang())
}

const plannerGraphContract = `

**현재 그래프 도구 계약**: 과거 프롬프트의 지표·ID 설명과 다르면 아래 현재 규약을 적용하세요. 사용자가 정한 작업 범위와 허용·차단 제약은 유지합니다.
- add_intent의 parent_ids와 parents는 근거 fact/finding ID입니다. from_intent는 생산 의도 ID이며 parent_ids에 넣지 않습니다. 의도의 실제 결과는 사용 가능한 get_worker_output/node_detail로 확인하세요.
- 현재 작업과 직접 연결된 작업의 근거를 함께 읽을 수 있습니다. source_task_id/inherited=true인 항목과 연결된 scope는 읽기 전용입니다. 간접 연결된 작업까지 읽거나 상속된 상태를 수정한다고 가정하지 마세요.
- coverage.pct는 fact/finding 연결 여부에 따른 자산 관찰 비율입니다. 정찰·HTTP 관찰도 포함하며 취약점 유형 검증률·공격면 검증률·실습 문제 해결률이 아닙니다. 100%여도 목표 달성 증거와 미검증 방향을 별도로 확인하고, 값이 없으면 미측정으로 다루세요. findings_total과 외부 실습의 해결 문제 수도 구분하세요.
- 도구 이름이 문구에 등장해도 실제 활성 도구 목록·허용된 지연 도구와 현재 스키마가 호출 가능 여부를 결정합니다. 비활성·미연결 도구를 호출하거나 사용자 설정을 바꾸어 우회하지 마세요. 필요한 조회·갱신 도구가 없으면 사용 가능한 fact/detail 조회로 근거를 인계하고 처리하지 못한 부분을 명시하세요.`

// Plan runs one planning round. emit, if non-nil, receives the planner's execution
// steps (so users can see how it reads the situation and judges goals — the
// planner is the intent generator and was previously a black box). Returns whether
// the planner judged the goal met.
// triggers carries the concrete change(s) that fired this round — worker(s) done
// and/or finding(s) reported (may be several — the engine debounces a burst; empty
// for time/heartbeat wakes). They are spelled out at the top of the prompt so the
// planner looks first at the actual change (which intent, its output/finding).
func (p *Planner) Plan(ctx context.Context, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, goal string, triggers []TriggerEvent, emit func(db.Activity)) (met bool, reason string, err error) {
	todos, err := plannerTodoStore(ctx, ts)
	if err != nil {
		return false, "", fmt.Errorf("계획 복원 실패: %w", err)
	}
	// cold-digest §2.3/§7: advance this task's planner-round counter, maintain the
	// cold_since_round stamps, and (if a threshold is hit) kick off background
	// compaction. Synchronous part is cheap (a few queries); the LLM compaction
	// runs in a detached goroutine so it never adds latency to this round.
	p.compactor.OnPlannerRound(ctx, ts)
	tsx := NewToolSet(ts, "planner")
	tsx.SetFindingRecorder(p.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.killWork = p.killWork   // enable kill_work tool (nil = unavailable)
	tsx.steerWork = p.steerWork // enable steer_work tool (nil = unavailable)
	if origin, _ := ts.OriginFactID(); origin > 0 {
		tsx.SetOwnerNode(origin) // planner-side anchors default to the task root (origin fact)
	}
	// 领域工具 + 基础默认工具集（Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash）
	// 资产覆盖度功能关闭时剔除 add_task_scope/list_untested_assets（不入 prompt）。
	base := append(tsx.DropCoverageTools(tsx.PlannerTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup := AugmentTools(ctx, "planner", base)
	defer cleanup()
	// 关键态势（刚完成的意图 + 预取的完整图）改放【本轮 user 输入】(见下方 input)，system
	// 只留静态规划正文。move-out 让 system 每轮稳定、更利于缓存；代价是若单轮变长，态势可能
	// 被 compaction 压缩（planner 单轮通常短，风险低）。situational 会拼进下方 input。
	situational := renderTriggers(ts, triggers) + renderGraphOverview(tsx.graphOverviewData())
	// 任务级 deadline / 终局模式(经 ctx 注入,见 taskclock.go)。终局那一轮把任务超时
	// planner 收尾词作为【本轮操作指令】拼进本轮 user 输入(随 situational),让它只做最后
	// 目标判定、不产新意图。
	tc := taskClockFrom(ctx)
	if tc.Final {
		situational += "\n\n【작업 최종 마무리(이번 회차의 특별 지시이며 위 일반 계획 절차보다 우선)】: " + resolveTaskTimeoutWrapup("planner")
	}
	// 本任务的工作目录 <workDir>/tasks/<taskID>，先建好。
	taskDir := ensureRunDir(p.workDir, taskID, 0)
	ctx = intercept.WithReviewContext(ctx, taskDir, intercept.ReviewBackground{})
	sysBody := plannerSystem(goal, p.workDir, taskDir)
	if p.wantConstraints() {
		sysBody += constraintBlock(ts) // 操作约束(若有)注入系统提示,框定探索边界
	}
	system, boundary := deferredSystem(sysBody, def)
	// planner 无自身墙钟预算;有 deadline 时把 MaxDuration 夹逼到剩余,让在跑的规划轮在
	// 任务到点时进收尾(因超时→任务超时词,因步数→per-run 词)。
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, 0)
	settle := wrapupSettlement("planner", nil)
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("planner", nil, clamped)
	}
	opts := agentcore.Options{
		Provider:               p.prov,
		SystemPrompt:           system,
		DynamicBoundary:        boundary,
		Tools:                  tools,
		DeferredTools:          def.Deferred,
		UnlockSet:              def.Unlock,
		PermissionMode:         permission.ModeBypass,
		EnableWebFetch:         true, // 走记录代理留痕；载入代理 CA 验证 MITM 重签的 HTTPS 证书
		WebFetchProxy:          p.proxyAddr,
		WebFetchCACert:         p.proxyCACert,
		WebFetchRecordingProxy: p.proxyCACert != "",
		WebFetchFallbackProxy:  p.proxyFallback,
		// 联网搜索(可选)。ddgs 无需 key；brave-free 需 BraveKey；tavily 需 TavilyKey。
		// WebSearchProxy 是独立出口代理(http/https/socks5)，与记录流量的 MITM 代理无关；空则直连。
		EnableWebSearch:    p.webSearch.Enabled,
		WebSearchBackend:   p.webSearch.Backend,
		BraveSearchAPIKey:  p.webSearch.BraveKey,
		TavilySearchAPIKey: p.webSearch.TavilyKey,
		WebSearchProxy:     p.webSearch.Proxy,
		BashEnv:            proxyEnv(p.proxyAddr, p.proxyCACert), // Bash 子命令默认走代理+信任 CA
		WorkingDir:         taskDir,                              // 本任务工作目录 <workDir>/tasks/<taskID>
		ToolOutputDir:      cmdOutDir(taskDir),
		MaxTurns:           p.maxTurns, // 0 = unlimited (configurable in agent management)
		MaxDuration:        maxDur,     // 0=不限;有 deadline 时=距 deadline 剩余
		Compaction:         compactionConfig(p.compactionWindow()),
		// 跨唤醒共享的规划待办：让串行链在多轮之间保留（session 是新的，store 不是）。
		Todos: todos,
		// 命中【本轮】步数预算→ SDK 跑收尾:把本轮已想清楚的结论落地(该派的 add_intent、
		// 能证的 prove_goal、串行链记 TodoWrite),而非停止规划——planner 之后仍会被反复唤醒。
		// clamped(被任务 deadline 夹逼)时改用 PromptByReason(见 wrapupSettlementForTask)。
		Settlement:   settle,
		NonStreaming: p.nonStreaming(), // 该 profile 选非流式时走 Provider.Complete
		MaxTokens:    p.maxTokens(),    // 0 = 不发上限,由服务端默认值决定
	}
	if p.guard != nil {
		opts.Hooks = p.guard.Hooks()
	}
	if p.tx != nil { // persist raw LLM conversation; one accumulating file per task's planner
		opts.Transcript = p.tx
		opts.SessionID = fmt.Sprintf("exp%d-planner", ts.ID())
	}
	baseSession := fmt.Sprintf("exp%d-planner", ts.ID())
	boundSession, changed, connectionErr := connectedSession(p.workDir, baseSession, p.connectionIdentity)
	if connectionErr != nil {
		return false, "", connectionErr
	}
	if p.tx != nil {
		opts.SessionID = boundSession
	}
	if changed {
		if emit != nil {
			emit(db.Activity{Worker: "planner", Kind: "text", Summary: "모델 연결이 변경되어 새 컨텍스트를 시작했습니다. 이전 작업 데이터는 보존됩니다."})
		}
	}
	// 态势（刚完成的意图 + 完整图）现在拼进本轮 user 输入（见下方 input）。user 里还有
	// 指令 + 跨唤醒待办（todo 是模型自己的规划便签，可再生，放 user 即可）。
	// 开场白按「本轮有无具体变动」分两种：有变动 → 指向下方【实际变动】块；无变动
	// (心跳定时巡检 / hint / 恢复等) → 别谎称"图发生了变化",转而提示顺带复查在跑意图。
	lead := "구체적인 변경이 발생했습니다(아래 【이번 회차를 유발한 실제 변경 사항】 참조). 이를 바탕으로 다음 단계를 계획하세요:"
	if len(triggers) == 0 {
		lead = "이번 회차는 **정기 점검(heartbeat 도달)/구체적인 변경 신호 없음**으로 깨어난 것입니다. 그래프에 새 변경이 없을 수 있습니다. 실행 중인 의도도 점검하여 오래 진행이 없거나 방향이 어긋난 경우 steer_work로 조정하고, 방향 전체가 잘못되었으면 kill_work로 중단하세요. 이후 결과의 남은 질문과 필요한 후속 배정을 확인한 뒤 목표를 판단하세요:"
		// 心跳/无变动唤醒时,若全图已无任何 open 或 running 意图 → 探索已停摆(没 worker 在跑、
		// 也没排队方向)。明确告知 planner 并强制其本轮补出新方向,别只复查在跑意图后空转一轮。
		if active, err := ts.HasActiveIntent(); err == nil && !active {
			lead = "이번 회차는 **정기 점검(heartbeat 도달)**으로 깨어났으며 현재 **open 또는 running 의도가 하나도 없습니다**. 실행 중인 Worker와 대기 중인 방향이 없어 탐색이 멈췄습니다. 미달성 목표와 기존 결과의 남은 질문을 확인하고 다음 검증을 배정하세요. 아직 확보하지 않은 전제와 실제 외부 차단을 구분하고, 전제를 확보하거나 다른 조건·방법으로 검증할 의도를 먼저 찾으세요:"
		}
	}
	input := lead + situational + "\n\n위 현황과 Worker 결과의 미검증 질문을 먼저 확인하고 필요한 후속 의도를 배정한 뒤 목표를 판단하세요. 해당 목표에 필요한 검증을 마치고 【실제로 달성】한 근거가 있다면 prove_goal로 각각 표시하세요. **목표 미달성에 활성 의도가 없다면 다음 검증이나 필요한 전제를 확보할 의도를 배정하세요. 부정적 결과·실패·미실행을 검증 완료로 취급하지 말고, 이번에 배정하지 못한 질문도 Todo에 보존하세요.**" +
		renderPlannerTodos(opts.Todos.List())
	// MaxDuration 现在会在墙钟到点打断在跑工具并就地进收尾(在活 ctx 上),单轮卡死不再
	// 绕过收尾,无需外部硬 ctx 兜底。ctx 只承载 pause / kill / shutdown。
	_, _, err = captureRun(ctx, opts, input,
		func(r db.Activity) {
			if emit != nil {
				r.Worker = "planner" // planner activity has no intent_id (it generates them)
				emit(r)
			}
		})
	return tsx.GoalMet, tsx.Reason, err
}
