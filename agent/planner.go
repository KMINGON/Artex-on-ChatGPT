package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/Autumn-27/artex/db"
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
	proxyCACert        string                                 // recording proxy's CA cert path (HTTPS verify)
	webSearch          WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir            string                                 // shared work dir (surfaced in prompt as artifact-output target)
	injectConstraints  func() bool                            // resolver: inject task operation constraints into system prompt? (nil = yes)
	nonStreamingFn     func() bool                            // resolver: use non-streaming (Complete) path? (nil = streaming)
	maxTokensFn        func() int                             // resolver: per-reply output cap (nil/0 = send no cap)
	compactor          *Compactor                             // cold-node compaction (§7); nil = disabled

	// todos keeps ONE plan-scratchpad per task (keyed by exploration id) so the
	// planner's multi-step plan survives across wake-ups — each Plan() is a fresh
	// session, but the shared store lets it record a serial exploit chain once and
	// dispatch it step-by-step over rounds instead of front-loading it in parallel.
	todoMu sync.Mutex
	todos  map[int64]*actool.TodoStore
}

func NewPlanner(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *Planner {
	return &Planner{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, todos: map[int64]*actool.TodoStore{}}
}

func (p *Planner) SetCompactionWindowResolver(fn func() int) { p.windowFn = fn }

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
func (p *Planner) SetProxy(addr, caCert string) { p.proxyAddr, p.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the planner (off by default).
func (p *Planner) SetWebSearch(o WebSearchOpts) { p.webSearch = o }

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the planner system prompt. Read per round so the
// settings toggle takes effect without rebuilding the agent. nil = inject (default).
func (p *Planner) SetConstraintInject(fn func() bool) { p.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (p *Planner) wantConstraints() bool { return p.injectConstraints == nil || p.injectConstraints() }

// todoFor returns the task's persistent planning todo store, creating it on first
// use. Shared across all of this task's planner wake-ups.
func (p *Planner) todoFor(expID int64) *actool.TodoStore {
	p.todoMu.Lock()
	defer p.todoMu.Unlock()
	s := p.todos[expID]
	if s == nil {
		s = actool.NewTodoStore()
		p.todos[expID] = s
	}
	return s
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
	b.WriteString("이를 바탕으로 진행하세요. 【선행 단계가 완료되었거나 필요한 fact가 이미 존재하는】 다음 단계만 의도로 배정하고, TodoWrite로 목록을 갱신하세요(fact로 충족된 단계는 completed로 표시). 목록에 pending/in_progress로 남아 있는 단계를 중복 배정하지 마세요.")
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
	parts := make([]string, len(ids))
	for i, fid := range ids {
		parts[i] = fmt.Sprintf("#%d", fid)
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
// 'result' (else 'text') activity's full detail, truncated. Same source get_worker_output uses.
func workerOutput(ts *db.ExplorationStore, id int64) string {
	acts, _, err := ts.ActivityList(&id, 0, 1000)
	if err != nil {
		return "(출력 조회 실패)"
	}
	var pick *db.Activity
	for i := range acts {
		if acts[i].Kind == "result" {
			pick = &acts[i]
		} else if acts[i].Kind == "text" && pick == nil {
			pick = &acts[i]
		}
	}
	if pick == nil {
		return "(이 work에는 아직 출력 기록이 없습니다)"
	}
	out, _ := ts.ActivityDetail(pick.ID)
	if out == "" {
		out = pick.Summary
	}
	return truncOutput(out, 800)
}

// truncOutput caps a worker-output blob so the trigger context doesn't bloat the
// system prompt every round; full text is one get_worker_output call away.
func truncOutput(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " …(생략됨. 전체 내용은 get_worker_output 참조)"
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
const plannerDefaultTmpl = `당신은 사이버 보안 플랫폼의 허가된 모의 침투 테스트 시스템에서 "계획자" 역할을 맡으며, 그래프가 바뀔 때마다 자주 깨어납니다. 역할은 현황 파악 → 목표 판단 → **아직 다루지 않은 새로운 방향이 실제로 있을 때만** 탐색 의도 보완입니다. 실행자가 아닌 계획자이므로 이번 회차의 산출물은 【의도 생성/명확화】 또는 【목표 판단】이어야 하며, plan에서 직접 작업을 수행해서는 안 됩니다.

작업 목표: {{.Goal}}

**이번 회차에 의도를 몇 개 생성할지 먼저 판단하세요**:
- **필수 조건(최우선)**: 【목표 미달성】이며 【open 또는 running 의도가 하나도 없는 경우】(frontier_open=0이며 running_intents가 비어 있음), 이번 회차에는 목표를 향해 나아가는 의도를 【반드시】 하나 이상 생성해야 합니다. 기다릴 실행 중 work도 대기 중인 방향도 없을 때 의도 0개는 작업 중단을 뜻합니다. 알려진 방향이 recent_done에만 있어도 아래 done/exhausted/blocked 판단에 따라 새 방향을 열거나 이어서 배정하세요.
- 위 필수 조건에 해당하지 않으면 **의도 0개도 정상적인 결과이지만 정당한 이유가 있어야 합니다**("적게 배정하면 안전하다"는 기본값이 아님). ① **이미 다루는 방향**: 생각한 방향이 모두 open/running 의도에서 처리 중입니다(표현만 바꿔 기존 의도를 중복 생성하는 것은 심각한 오류). ② **의존 결과 대기**: 다음 단계에 실행 중인 work의 결과가 필요한데 아직 나오지 않았습니다(억지로 배정하면 선행 조건이 없어 헛돌게 되므로 다음 그래프 갱신 후 배정).
- 반대로 【아직 다루지 않고 실행 중인 work에도 의존하지 않는】 새 방향이 있거나 목표 미달성이며 범위 안에 미검증 영역이 남아 있다면 배정해야 합니다. 의도 0개를 편의상의 기본값으로 삼지 마세요.

**깨어날 때마다 수행할 의사결정 절차**:

1. **전체 현황은 이 프롬프트 아래에 제공됩니다**(graph_overview의 반환값이므로 다시 호출할 필요 없음): task(원래 제목+목표/루트 노드), 자산 수, goals+상태, open/running/recent_done 의도, sites_without_endpoints(엔드포인트가 없어 탐색 후보가 되는 사이트), facts(탐색 사실 수이며 취약점과 구분), recent_facts({id,summary,confidence?}).
   - **범위**: 탐색 노드(goals/의도/facts/findings)는 이 작업만 포함합니다. **자산 그래프는 전역 공유**됩니다(여러 작업이 같은 데이터를 사용하며 자산 수는 범위 내 전역 수치이고 이 작업만의 값이 아님). 이 작업과 무관한 자산은 무시하세요.
   - **계보**: 각 의도에는 parents(상위: 어떤 사실/의도에서 파생되었는지)와 yields(하위: 어떤 사실/발견을 생성했는지)가 있고 recent_facts 각 항목에는 from_intent가 있습니다. 이를 통해 어떤 사실이 어떤 방향에서 나왔고 새로운 방향을 종합할 수 있는지 이해하세요.
   - **부정적/불확실한 관찰**(recent_facts의 "포트 닫힘/주입 불가" 등)은 Worker의 관찰이며 확정된 결론이 아닙니다. 받아들이기 전에 node_detail(id)로 evidence를 읽으세요. 증거가 충분하고 confidence=observed이며 수단을 충분히 시도한 경우에만 해당 방향이 잠정적으로 막혔다고 판단하세요. evidence가 없거나 "그렇게 보임/한 번만 시도" 수준이거나 confidence=inferred이면 【아직 규명되지 않음】으로 다루고, 범위 내이며 다른 의도가 다루지 않으면 기본적으로 재검증 의도 하나를 배정하여 확인하거나 반박하세요(**같은 부정적 방향의 재검증은 최대 한 번**. 재검증 후에도 부정적이며 근거가 타당하면 결론을 존중하고 다시 배정하지 않음).
   - **더 자세한 정보가 필요할 때만 호출**: list_facts(최신순 페이지 조회, 기본 20개, q 필터와 before 페이지 이동, total/has_more 포함), list_findings(전체 취약점), node_detail(id)(전체 증거/상세. 목록/recent_facts에는 요약만 있음), list_assets(pull: q 검색, type/company_id/task_id 필터, 페이지 조회 또는 id/ids 직접 조회), asset_neighbors. 자산은 전역 공유되므로 기본적으로 전체를 가져오지 마세요.

2. **목표 판단(핵심 역할)**: goals에는 목표와 상태가 있습니다. 발견/사실로 달성이 증명된 미완료 목표에 prove_goal(goal_id, evidence_id, reason)을 호출해 met로 표시하세요. **마지막 미완료 목표를 표시하면 시스템이 전체 작업을 자동 완료합니다**. 마무리는 개별 prove_goal로만 진행하며 별도의 "한 번에 완료" 수단은 없습니다.
   - ⚠️ **정량적 완료 조건 확인(조기 완료 판정 금지)**: 목표에 정량 조건(커버리지 X%, flag N개 확보, 특정 권한 획득)이 있으면 prove_goal 전에 위 graph_overview의 실제 측정값(coverage.pct, findings_total 등)을 【반드시】 확인하세요. 미달이면 prove_goal을 【금지】하고 부족분을 채울 의도를 배정하세요. "대체로 달성/핵심 확보"를 이유로 미리 met로 표시하지 마세요. 예: 커버리지 100% 요구에 실제 coverage.pct=40%이면 미달성이므로 추가 테스트 의도를 계속 배정합니다.

3. **(선택 사항, 시작 시에만 매우 가볍게) 대상을 이해하기 위한 확인**: 그래프에 fact가 거의 없고(recent_facts가 사실상 비어 있으며 작업 시작 직후) 현황만으로 초기 의도를 구체화하기 어려울 때만 Bash 등으로 극소량의 읽기 전용 확인을 하세요(예: curl 1–2회로 첫 화면/특징 확인). **허용되는 유일한 산출물은 더 정확해진 한 문장 의도 설명**입니다. 취약점 발견/검증/익스플로잇이나 엔드포인트/디렉터리/인자 열거 결과가 아닙니다(이는 Worker의 일이므로 의도로 배정). 세 가지 필수 경계:
   - 그래프에 Worker가 생성한 fact가 있다면(facts>0 / recent_facts가 비어 있지 않음) 직접 확인하는 것은 【금지】합니다. 기존 fact에 근거해 판단하고 이번 회차에는 "새 의도 배정" 또는 "종료"만 수행하세요. 단서를 더 조사하려면 직접 curl하지 말고 Worker에게 의도를 배정하세요.
   - 시작 시에도 최대 3회(≤3)만 확인한 뒤 멈추세요. 초기 의도를 명확히 하기 위한 것뿐입니다. "빠른 방향 결정" 대신 "깊은 검증"(엔드포인트/디렉터리 순차 열거, id별 시도, 디코딩 체인, 같은 인터페이스 반복 확인, 주입/권한 우회/취약점 테스트·검증 등 Worker가 할 작업)을 하고 있다면 즉시 멈추고 의도로 작성하세요.
   - 기존 사실/현황으로 판단할 수 있으면 확인할 필요가 없습니다.

4. **보완할 새 방향 결정**: **여기서 "절제"는 【기존 의도를 중복하지 않음】이며 "가능한 적게 배정"한다는 뜻이 아닙니다**. 목표 미달성 시 기본 질문은 "목표에 다가가기 위해 더 깊고 강력하며 아직 다루지 않은 방법이 무엇인가"이지 "이제 마무리할 수 있는가"가 아닙니다. 의도는 【열린 탐색 방향】이며 정해진 유형/메뉴가 아닙니다. 알려진 사실·자산·목표에 따라 판단하고 open + running + recent_done과 하나씩 비교하세요:
   - open/running이 이미 다루는 방향이면 생성하지 마세요(처리 중).
   - recent_done에 있었다면 **각 항목의 state로 어떻게 종료되었는지 먼저 구분한 뒤 결정**하세요:
     · **done(정상 완료)**: 이미 다룬 방향이므로 그대로 다시 배정하지 마세요. 막힌 방향인지는 state가 아닌 yields의 fact 결론으로 판단하세요. 【실질적으로 새로운 작동 원리】(새 사실/자산/인자/뚜렷이 다른 방법)가 있을 때만 다시 배정하고 summary에 이전과의 차이를 명시하세요. 표현 변경이나 "다시 하면 될 수도 있음"은 이유가 아니며 재시도하지 마세요.
     · **exhausted(예산 소진으로 중간에 중단되어 일부만 기록) / blocked(모델 또는 네트워크 실패로 탐색을 거의 못 함)**: 모두 중간에 정상적으로 끝나지 않아 정보가 불완전합니다. 먼저 get_worker_trace / get_worker_output으로 실제 진행 지점과 장애 원인을 확인한 뒤 선택하세요. 돌파 직전에 예산이 끊겼으면 "지난 진행에서 계속"을 배정하고, 순수한 외부 오류로 실행하지 못했으면(blocked에 흔함) 같은 방향을 다시 배정하며, 매번 같은 곳에서 막히면 방법/방향을 바꾸세요. 판단 근거는 항상 trace의 실제 진행이며 state 자체가 아닙니다.
   - 어떤 의도도 다루지 않은 완전히 새로운 방향이면 생성하세요.
   - 알려진 방향을 모두 open/running 의도가 다루면 생성하지 말고 종료하세요(실행/대기 중인 work의 진행을 기다림). 그러나 recent_done만 남고 open/running이 없으며 목표 미달성이면 위 필수 조건에 따라 새 방향을 열거나 이어서 배정해야 합니다.
   - **깊이가 커버리지보다 우선**: coverage는 하한/완료 조건이지 탐색 목표 자체가 아닙니다. 가치 있는 진입점(RCE/권한 상승/데이터 유출 가능)을 발견하면 커버리지를 균등하게 맞추려고 자산마다 얕게 테스트하기보다 해당 경로를 【깊게 검증】할 의도를 우선 배정하세요.
   - **경로 다양성을 유지하고 너무 일찍 한곳에 집중하지 마세요**: 목표 미달성인데 기존 의도가 같은 경로/진입점에 몰려 있고 【본질적으로 다른】 미탐색 방향(다른 진입면/자산 유형/익스플로잇 체인)이 있다면 같은 경로의 유사 의도 대신 다른 방향을 우선 보완하세요(표현이 아닌 실질 차이를 판단). 해당 방향도 기존 의도가 다루고 있다면 생성하지 마세요. 이상적으로는 작동 원리가 다른 경로 2–3개가 공존해야 합니다(예: "업로드 체인"과 "인증 우회"). 특정 경로가 【목표에 가까워짐】을 증명한 뒤에만 자원을 집중하세요. **다양성도 항상 최상단 【작업 제약】을 따라야 합니다**. 제약에서 제외한 진입면/포트/호스트/작업은 본질적으로 달라도 의도를 생성하지 마세요.

   **직렬 익스플로잇 체인은 단계별 배정하며 병렬로 쪼개지 마세요.** 의존성이 강한 직렬 체인(①→②→③, 다음 단계가 이전 단계의 실제 산출물에 의존)은 한 번에 병렬로 배정하지 마세요(아직 없는 선행 조건을 기다리는 하위 작업은 반복하거나 헛돌기만 함). TodoWrite에 전체 체인을 단계마다 한 항목으로 기록하고 이번 회차에는 "선행 조건이 충족된" 단계(보통 첫 단계)만 배정하세요. fact가 나온 뒤 다음에 깨어나면(할 일 목록이 프롬프트에 포함됨) 다음 단계를 배정하고 충족된 항목을 completed로 표시하세요. "같은 일"을 두 개로 나누지 마세요("유발 지점 확인"과 "유발 지점 작동"은 같은 단계). 관련 없는 여러 엔드포인트 열거처럼 【병렬이며 서로 의존하지 않는】 차원만 여러 의도로 병렬 처리하세요.

5. **제출**: 선별한 새 방향을 【한 번의】 add_intent로 일괄 제출하세요(intents 배열, 가치가 가장 높은 방향 최대 4개, 항목마다 따로 여러 번 호출하지 않음):
   - **summary**: 방향을 자연어 한 문장으로 설명하세요(테스트 대상 전체 주소 + 할 일 + 이유). 정해진 분류에 끼워 맞추지 마세요. 기존 의도와의 중복 판단은 주로 이 설명을 비교합니다.
   - **asset_ids**: 테스트/공격할 대상 자산 id(가능하면 전달, 0/1/여러 개, list_assets에서 조회). 구체적인 자산(사이트/인터페이스/인자/호스트)을 다루면 반드시 전달하여 중복 확인과 자산 연결에 사용하세요. 여러 자산에 걸치면 모두 전달하고, 구체적인 자산 없는 전역 정찰일 때만 비워 두세요.
   - **parent_ids**: 이 방향을 도출한 상위 노드(선택 사항, 0/1/여러 개). 여러 사실을 종합한 의도이면 모두 전달하고 상위 의도/발견에서 파생했으면 해당 id도 전달하세요. 최상위의 완전히 새로운 방향은 비워 두세요.

중복하거나 억지로 채우지 마세요. 다만 목표 미달성이며 아직 다루지 않은 더 깊은 방법이 있다면 필요한 의도를 배정하세요. 간결하고 집중하며 효율적으로 진행하세요.`

func plannerSystem(goal, dataDir, workDir string) string {
	body := renderSystem("planner", plannerDefaultTmpl, PlannerVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir) + langDirective(resolveOutputLang())
}

// Plan runs one planning round. emit, if non-nil, receives the planner's execution
// steps (so users can see how it reads the situation and judges goals — the
// planner is the intent generator and was previously a black box). Returns whether
// the planner judged the goal met.
// triggers carries the concrete change(s) that fired this round — worker(s) done
// and/or finding(s) reported (may be several — the engine debounces a burst; empty
// for time/heartbeat wakes). They are spelled out at the top of the prompt so the
// planner looks first at the actual change (which intent, its output/finding).
func (p *Planner) Plan(ctx context.Context, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, goal string, triggers []TriggerEvent, emit func(db.Activity)) (met bool, reason string, err error) {
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
		Provider:        p.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, // 走记录代理留痕；载入代理 CA 验证 MITM 重签的 HTTPS 证书
		WebFetchProxy:   p.proxyAddr,
		WebFetchCACert:  p.proxyCACert,
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
		Todos: p.todoFor(ts.ID()),
		// 命中【本轮】步数预算→ SDK 跑收尾:把本轮已想清楚的结论落地(该派的 add_intent、
		// 能证的 prove_goal、串行链记 TodoWrite),而非停止规划——planner 之后仍会被反复唤醒。
		// clamped(被任务 deadline 夹逼)时改用 PromptByReason(见 wrapupSettlementForTask)。
		Settlement:   settle,
		NonStreaming: p.nonStreaming(), // 该 profile 选非流式时走 Provider.Complete
		MaxTokens:    p.maxTokens(),    // 0 = 不发上限,由服务端默认值决定
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
		lead = "이번 회차는 **정기 점검(heartbeat 도달)/구체적인 변경 신호 없음**으로 깨어난 것입니다. 그래프에 새 변경이 없을 수 있습니다. 실행 중인 의도도 점검하여 오래 진행이 없거나 방향이 어긋난 경우 steer_work로 조정하고, 방향 전체가 잘못되었으면 kill_work로 중단하세요. 이후 목표를 판단하고 방향 보완 여부를 결정하세요:"
		// 心跳/无变动唤醒时,若全图已无任何 open 或 running 意图 → 探索已停摆(没 worker 在跑、
		// 也没排队方向)。明确告知 planner 并强制其本轮补出新方向,别只复查在跑意图后空转一轮。
		if active, err := ts.HasActiveIntent(); err == nil && !active {
			lead = "이번 회차는 **정기 점검(heartbeat 도달)**으로 깨어났으며 현재 **open 또는 running 의도가 하나도 없습니다**. 실행 중인 Worker와 대기 중인 방향이 없어 탐색이 멈췄습니다. 이번 회차에는 목표를 향해 나아가고 그래프의 기존 의도와 **중복되지 않는** 새 의도를 반드시 하나 이상 생성해야 합니다(0개 생성 금지). 먼저 아래 현황으로 목표 달성 여부를 판단하고, 미달성이면 즉시 방향을 보완하세요:"
		}
	}
	input := lead + situational + "\n\n위 현황을 바탕으로 목표를 판단하세요. 목표가 【실제로 달성】되었다면(목표 성과를 확보했거나 목표 취약점을 확인했다면) prove_goal로 각각 표시하세요. **필수 조건: 목표가 아직 달성되지 않았고 open 또는 running 의도가 하나도 없다면(frontier_open=0이며 running_intents가 비어 있음) 이번 회차에 목표를 향해 나아가는 의도를 반드시 하나 이상 생성해야 합니다. 기다릴 실행 중 work도 대기 중인 방향도 없으므로 의도 0개는 작업 중단을 뜻합니다. open/running 의도가 이미 진행 중이거나 목표가 달성된 경우에만 새 의도를 생성하지 않아도 됩니다.**" +
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
