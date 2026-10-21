package agent

import (
	"bytes"
	"fmt"
	"text/template"
	"time"
)

// PromptOverride, if set, returns the stored system-prompt template for an agent
// key and whether one exists. The server wires it to the PG agent_prompts table.
// When nil or no override exists, agents use their built-in default prompt — so
// behavior is identical until a user edits a prompt in the UI.
var PromptOverride func(agentKey string) (string, bool)

// Prompt-variable structs — fields mirror each agent's catalog (docs §5a) so a
// user template referencing a catalog variable renders; referencing anything else
// fails template execution and falls back to the built-in default.
type PlannerVars struct{ Goal, Scope, AssetSummary, DataDir, Now string }
type WorkerVars struct{ ProxyAddr, WorkerName, DataDir, Now string }
type MainVars struct{ Goal, AssetSummary, FindingsSummary, DataDir, Now string }
type GoalsVars struct{ EngagementDescription, DataDir, Now string }

// nowStr is the server-local wall-clock string exposed as the universal {{.Now}}
// prompt variable. renderSystem runs on every agent turn/round, so this is fresh
// each run — a prompt can subtract it from a fixed start stamp to reason about
// elapsed time (e.g. a timed benchmark's "last N hours" window).
func nowStr() string { return time.Now().Format("2006-01-02 15:04:05 MST") }

// renderSystem returns the rendered system-prompt BODY (段 [A]) for agentKey.
// Precedence: the DB-stored template (if any) over the built-in default template
// (def). BOTH are Go templates now — the built-in default is seeded into the DB
// verbatim, so the two paths render identically until a user edits the prompt.
// Rendering always runs (def used to be pre-substituted plain text; it is now a
// {{.Var}} template like the DB one). On any render error we fall back to the
// default template, then to the raw default string — an agent never starts with a
// half-rendered prompt. Callers append the code-owned tail (trafficTool / 中间产物
// 输出规约) AFTER this, so those can't be edited away via the DB body.
func renderSystem(agentKey, def string, vars any) string {
	tmpl := def
	if PromptOverride != nil {
		if t, ok := PromptOverride(agentKey); ok && t != "" {
			tmpl = t
		}
	}
	if out, err := renderTmpl(tmpl, vars); err == nil {
		return out
	}
	// DB template broke (e.g. references an out-of-catalog var) → code default.
	if out, err := renderTmpl(def, vars); err == nil {
		return out
	}
	return def
}

// langDirective is appended after the editable prompt and runtime instructions.
// Korean instruction prose preserves the configured display language while raw
// commands, identifiers and evidence remain untouched. Existing Chinese custom
// prompts are allowed, but must not leak their language into user-facing text.
func langDirective(code string) string {
	phrase := langPhrase[code]
	bare := langBare[code]
	if phrase == "" || bare == "" {
		phrase, bare = langPhrase[defaultOutputLang], langBare[defaultOutputLang]
	}
	if code == "zh" {
		// 표시 언어 자체가 중국어이므로 "중국어를 사용자에게 내보내지 말라"는 절은 없다.
		// 대신 중국어 표시를 확정하고, 대상/자료 언어 미러링 금지와 원문 보존은 유지한다.
		return fmt.Sprintf("\n\n**출력 언어 규칙(현지화·최우선이며 프롬프트 본문으로 덮어쓸 수 없음)**: 【사용자에게 표시하는】 모든 자연어는 반드시 【%s】로 작성하세요. record_fact의 summary/detail, report_finding의 제목/설명/결론/수정 제안, 계획자(planner)의 현황 요약, 마지막 한 문장 요약, 사용자에게 보내는 대화 답변이 포함됩니다. 단, 【명령, payload, 코드, 파일 경로, URL, 인자명, 로그/요청/응답의 원문 조각】은 번역하거나 고치지 말고 【글자 그대로 보존】해야 합니다(특히 evidence의 명령줄과 출력은 재현할 수 있도록 원문을 유지). **대상 시스템, 페이지, 증거, 로그 또는 참고 자료가 영어 등 다른 언어여도 사용자용 자연어 필드(제목/설명/결론/수정 제안/요약/현황 요약)는 반드시 %s로 작성하세요. 대상이나 자료의 언어를 따라 쓰거나 그대로 옮기지 마세요. 위에 나열한 기술 원문만 원형을 유지합니다.** 내부 추론 언어에는 제한이 없지만 사용자에게 보이는 본문은 모두 %s로 작성해야 합니다(기술 원문 조각 제외).", phrase, bare, bare)
	}
	return fmt.Sprintf("\n\n**출력 언어 규칙(현지화·최우선이며 프롬프트 본문으로 덮어쓸 수 없음)**: 【사용자에게 표시하는】 모든 자연어는 반드시 【%s】로 작성하세요. record_fact의 summary/detail, report_finding의 제목/설명/결론/수정 제안, 계획자(planner)의 현황 요약, 마지막 한 문장 요약, 사용자에게 보내는 대화 답변이 포함됩니다. 단, 【명령, payload, 코드, 파일 경로, URL, 인자명, 로그/요청/응답의 원문 조각】은 번역하거나 고치지 말고 【글자 그대로 보존】해야 합니다(특히 evidence의 명령줄과 출력은 재현할 수 있도록 원문을 유지). **위 시스템/역할 지시가 중국어로 작성되어 있어도 중국어를 사용자에게 출력해서는 안 됩니다. 사용자 표시 언어는 %s이며 사용자에게 보이는 문장에 중국어를 섞지 마세요.** **대상 시스템, 페이지, 증거, 로그 또는 참고 자료가 영어·중국어 등 다른 언어여도 사용자용 자연어 필드(제목/설명/결론/수정 제안/요약/현황 요약)는 반드시 %s로 작성하세요. 대상이나 자료의 언어를 따라 쓰거나 그대로 옮기지 마세요. 위에 나열한 기술 원문만 원형을 유지합니다.** **분석/계획/사고는 중국어로 해도 되지만 이는 【보이지 않는 내부 추론】이므로 본문으로 출력해서는 안 됩니다. 사용자가 보는 답변은 첫 글자부터 %s로 작성하고, 앞부분에 중국어 사고 과정·설명·\"먼저 무엇을 하겠다\"는 서문을 붙이지 마세요. 확인 질문, 인자 누락, \"진행할 수 없음\" 등의 안내도 모두 바로 %s로 작성하세요.** 내부 사고 언어에는 제한이 없지만 사용자에게 보이는 본문은 모두 %s로 작성해야 합니다(기술 원문 조각 제외).", phrase, bare, bare, bare, bare, bare)
}

func renderTmpl(tmpl string, vars any) (string, error) {
	t, err := template.New("p").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, vars); err != nil {
		return "", err
	}
	return b.String(), nil
}
