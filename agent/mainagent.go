package agent

import (
	"context"
	"fmt"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/guard"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// MainAgent is the thin human-interface orchestrator (docs §4.2 / §7). The human
// chats with it; it observes (read tools), and steers by injecting hints
// (→planner) or direct high-priority intents (→frontier). It does NOT run the
// autonomous intent-generation loop (that is the planner's job).
type MainAgent struct {
	guard              *guard.Guard
	findingRecorder    FindingRecorder
	connectionIdentity func() string
	prov               llm.Provider
	model              string
	tx                 *transcript.Store // raw LLM conversation persistence (nil = off)
	window             int               // context window in tokens (for compaction)
	windowFn           func() int        // optional dynamic task-chain minimum
	maxTurns           int               // max agent turns per run (0 = unlimited)
	proxyAddr          string            // recording proxy for WebFetch (empty = direct)
	proxyFallback      string
	proxyCACert        string                                 // recording proxy's CA cert path (HTTPS verify)
	webSearch          WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir            string                                 // shared work dir (surfaced in prompt as artifact-output target)
	steerWork          func(intentID int64, msg string) error // engine callback: steer a running work (nil = off)
	nonStreamingFn     func() bool                            // resolver: use non-streaming (Complete) path? (nil = streaming)
	maxTokensFn        func() int                             // resolver: per-reply output cap (nil/0 = send no cap)
}

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (m *MainAgent) SetNonStreaming(fn func() bool) { m.nonStreamingFn = fn }

func (m *MainAgent) nonStreaming() bool { return m.nonStreamingFn != nil && m.nonStreamingFn() }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (m *MainAgent) SetMaxTokens(fn func() int) { m.maxTokensFn = fn }

func (m *MainAgent) maxTokens() int {
	if m.maxTokensFn == nil {
		return 0
	}
	return m.maxTokensFn()
}

func NewMainAgent(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *MainAgent {
	return &MainAgent{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns}
}

func (m *MainAgent) SetCompactionWindowResolver(fn func() int) { m.windowFn = fn }

// SetGuard applies the task's configured rules to orchestration tool calls too.
func (m *MainAgent) SetGuard(g *guard.Guard) { m.guard = g }

func (m *MainAgent) compactionWindow() int {
	if m.windowFn != nil {
		return m.windowFn()
	}
	return m.window
}

// SetProxy points the main agent's WebFetch at the recording proxy plus the CA
// cert it trusts to verify HTTPS through it (empty addr = direct).
func (m *MainAgent) SetProxy(addr, caCert string, fallback ...string) {
	m.proxyAddr, m.proxyCACert, m.proxyFallback = addr, caCert, ""
	if len(fallback) > 0 {
		m.proxyFallback = fallback[0]
	}
}

// SetWebSearch selects the web_search backend for the main agent (off by default).
func (m *MainAgent) SetWebSearch(o WebSearchOpts) { m.webSearch = o }

// SetSteerWork wires the engine callback that lets the main agent's steer_work
// tool inject a mid-run course-correction into a running work (nil = tool off).
func (m *MainAgent) SetSteerWork(fn func(intentID int64, msg string) error) { m.steerWork = fn }

// mainAgentDefaultTmpl is the built-in EDITABLE body (段 [A]) of the main agent
// prompt, seeded into agent_prompts. Goal is a {{.Goal}} template var; the 中间
// 产物输出规约 tail is code-owned (artifactSpec), appended after rendering.
const mainAgentDefaultTmpl = `당신은 허가된 모의 침투 테스트 시스템의 "메인 에이전트"로서 운영자와 시스템을 연결합니다. 직접 탐색하거나 스스로 의도를 연속 생성하지 않습니다(이는 계획자의 역할입니다). 역할은 다음과 같습니다:

1. 관찰: graph_overview / list_findings / list_facts / list_assets / get_worker_output으로 현재 진행 상황에 관한 사용자 질문에 답하세요.
2. 방향 조정(사용자의 의도를 시스템에 반영):
   - "방향 변경/특정 취약점 강조/특정 영역 집중"을 원하면 add_hint로 힌트를 작성하세요(계획자가 다음에 읽습니다).
   - "구체적인 대상을 즉시 테스트"하기를 원하면 add_intent로 우선순위가 높은 의도(priority 8-10)를 직접 추가하세요. 시스템은 완료된 작업을 자동으로 실행 상태로 되돌리고 Worker가 해당 의도를 실행하게 하며, 실행 후 다시 완료 상태가 됩니다.
     **작업 목표가 모두 달성된 경우**(graph_overview의 goals가 모두 met): 배정 전에 이 의도에 "새로 달성해야 할 결과"가 내포되어 있는지 판단하세요. 있다면 추정한 목표를 한 문장으로 설명하고 **정식 목표로 등록할지 물으세요**. 사용자가 원하면 set_goals로 등록하세요(이후 일반 계획 절차에 들어가 계획자가 자율적으로 진행합니다). 원하지 않거나 잠깐 확인하려는 경우에는 add_intent로 해당 의도만 배정하고, Worker 실행 후 작업은 완료 상태로 돌아갑니다(자율적으로 계속하지 않음). 명백한 일회성 확인이며 새 목표가 없다면 매번 묻지 말고 바로 add_intent를 사용하세요.
   - "실행 중인 의도(work)를 실시간 조정(X 대신 Y에 집중)"하려면 steer_work를 사용하세요(중단하거나 기존 진행을 잃지 않으며 Worker의 다음 동작 전에 적용). 먼저 get_worker_output으로 진행 내용을 확인하세요. 방향 전체가 잘못되었다면 add_intent로 새 의도를 별도 배정하세요.
   - "새로 달성할 최종 목표 추가"를 원하면 set_goals로 목표를 보완하세요. 시스템은 목표를 작업 그래프에 저장하고 **완료/일시 중지된 작업을 자동으로 실행 상태로 되돌립니다**(계획자가 달성 여부를 다시 판단). 사용자가 재개 버튼을 누를 필요는 없습니다.
   - "테스트 제약 추가/변경(예: 현재 포트만 테스트, 무차별 대입 금지, 수동적 정찰만 허용)"을 원하면 set_constraints로 등록하세요(type=allow 허용 / type=deny 금지). 다음 계획 회차부터 planner/worker 프롬프트에 제약이 주입되어 탐색 경계를 정합니다. 개요의 「제약 관리」에서도 추가/수정/삭제할 수 있습니다.
3. 무엇을 했는지 알기 쉽게 간결하게 답하세요.

현재 작업 목표: {{.Goal}}

발견을 지어내지 말고 도구가 반환한 실제 데이터만 근거로 답하세요.`

func mainAgentSystem(goal, dataDir, workDir string) string {
	body := renderSystem("mainagent", mainAgentDefaultTmpl, MainVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir) + langDirective(resolveOutputLang())
}

// Chat handles one human message and returns the assistant reply. emit, if
// non-nil, receives each execution step (thinking / tool_use / tool_result /
// text / result) so the main-agent session shows its work — exactly like the
// worker/planner sessions — not just the final answer.
func (m *MainAgent) Chat(ctx context.Context, taskID int64, mainSeg int, as *db.AssetStore, ts *db.ExplorationStore, goal, message string, emit func(db.Activity), notify, resume func(), notifyGoal, notifyHint func([]string)) (string, error) {
	tsx := NewToolSet(ts, "human")
	tsx.SetFindingRecorder(m.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.SetNotify(notify)         // 通用唤醒（无专用回调的写操作走它，debounced）
	tsx.SetResumeTask(resume)     // set_goals 新增目标 → 把已完成/暂停的任务拉回 running
	tsx.SetNotifyGoal(notifyGoal) // set_goals 新增目标 → 给 planner 记一条「人新增了目标：…」触发
	tsx.SetNotifyHint(notifyHint) // add_hint 新增提示 → 给 planner 记一条「人新增了 N 条战略提示：…」触发
	tsx.steerWork = m.steerWork   // enable steer_work tool (nil = unavailable)
	// 领域工具 + 基础默认工具集（Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash）
	// 资产覆盖度功能关闭时剔除 add_task_scope/list_untested_assets（不入 prompt）。
	base := append(tsx.DropCoverageTools(tsx.MainAgentTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup := AugmentTools(ctx, "mainagent", base)
	defer cleanup()
	// 本任务的工作目录 <workDir>/tasks/<taskID>，先建好。
	mainDir := ensureRunDir(m.workDir, taskID, 0)
	ctx = intercept.WithReviewWorkingDirectory(ctx, mainDir)
	system, boundary := deferredSystem(mainAgentSystem(goal, m.workDir, mainDir)+constraintBlock(ts), def)
	opts := agentcore.Options{
		Provider:               m.prov,
		SystemPrompt:           system,
		DynamicBoundary:        boundary,
		Tools:                  tools,
		DeferredTools:          def.Deferred,
		UnlockSet:              def.Unlock,
		PermissionMode:         permission.ModeBypass,
		EnableWebFetch:         true, // 走记录代理留痕；载入代理 CA 验证 MITM 重签的 HTTPS 证书
		WebFetchProxy:          m.proxyAddr,
		WebFetchCACert:         m.proxyCACert,
		WebFetchRecordingProxy: m.proxyCACert != "",
		WebFetchFallbackProxy:  m.proxyFallback,
		// 联网搜索(可选)。ddgs 无需 key；brave-free 需 BraveKey；tavily 需 TavilyKey。
		// WebSearchProxy 是独立出口代理(http/https/socks5)，与记录流量的 MITM 代理无关；空则直连。
		EnableWebSearch:    m.webSearch.Enabled,
		WebSearchBackend:   m.webSearch.Backend,
		BraveSearchAPIKey:  m.webSearch.BraveKey,
		TavilySearchAPIKey: m.webSearch.TavilyKey,
		WebSearchProxy:     m.webSearch.Proxy,
		BashEnv:            proxyEnv(m.proxyAddr, m.proxyCACert), // Bash 子命令默认走代理+信任 CA
		WorkingDir:         mainDir,                              // 本任务工作目录 <workDir>/tasks/<taskID>
		ToolOutputDir:      cmdOutDir(mainDir),
		MaxTurns:           m.maxTurns,                             // 0 = unlimited (configurable in agent management)
		Compaction:         compactionConfig(m.compactionWindow()), // long chats stay within the window
		Todos:              actool.NewTodoStore(),                  // 会话级临时待办（TodoWrite），纯规划用，退出即丢
		// 命中预算(步数)→ SDK 跑收尾:向用户输出一句进展总结。Prompt 与收尾轮数可后台编辑(默认 10 轮)。
		Settlement:   wrapupSettlement("mainagent", nil),
		NonStreaming: m.nonStreaming(), // 该 profile 选非流式时走 Provider.Complete
		MaxTokens:    m.maxTokens(),    // 0 = 不发上限,由服务端默认值决定
	}
	if m.guard != nil {
		opts.Hooks = m.guard.Hooks()
	}
	if m.tx != nil { // persist raw human↔AI conversation; one accumulating file per segment
		opts.Transcript = m.tx
		// Segment 0 keeps the legacy "exp%d-main" name so existing transcripts still
		// load; each new session (seg>=1) gets its own file for a clean context.
		opts.SessionID = fmt.Sprintf("exp%d-main", ts.ID())
		if mainSeg > 0 {
			opts.SessionID = fmt.Sprintf("exp%d-main-s%d", ts.ID(), mainSeg)
		}
	}
	baseSession := fmt.Sprintf("exp%d-main", ts.ID())
	if mainSeg > 0 {
		baseSession = fmt.Sprintf("exp%d-main-s%d", ts.ID(), mainSeg)
	}
	boundSession, changed, err := connectedSession(m.workDir, baseSession, m.connectionIdentity)
	if err != nil {
		return "", err
	}
	if m.tx != nil {
		opts.SessionID = boundSession
	}
	if changed && emit != nil {
		emit(db.Activity{Worker: "mainagent", Kind: "text", Summary: "모델 연결이 변경되어 새 컨텍스트를 시작했습니다. 이전 대화 기록과 작업 데이터는 보존됩니다."})
	}
	ctx = attachSideCapture(ctx, &opts)
	s := agentcore.NewSession(opts)
	defer s.Close()
	// reload the prior conversation from the transcript so the agent has context
	// across turns (each Chat is a fresh session; without this it can't see earlier
	// messages). First turn: no file yet → Resume loads nothing and proceeds.
	if m.tx != nil {
		if err := s.Resume(opts.SessionID); err != nil {
			return "", err
		}
	}
	// C2: this session is fresh each turn; re-unlock skill-gated MCPs from prior
	// Skill() calls in the reloaded history so revealed tools stay callable.
	seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
	text, _, err := captureRunSession(ctx, s, message, func(r db.Activity) {
		if emit != nil {
			r.Worker = "mainagent"
			emit(r)
		}
	})
	if closeErr := s.Close(); closeErr != nil {
		return text, closeErr
	}
	return text, err
}
