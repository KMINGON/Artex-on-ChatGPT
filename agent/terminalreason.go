package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Autumn-27/norma/harness"
)

// runTrace retains the latest tool call so an interrupted run can identify the
// operation that was still in flight.
type runTrace struct {
	startedAt     time.Time
	id            string
	name          string
	input         string
	at            time.Time
	pending       bool
	role          string
	toolCalls     int
	outputEmitted bool
}

func (t *runTrace) start(id, name, input string) {
	t.id, t.name, t.input, t.at, t.pending = id, name, input, time.Now(), true
	t.toolCalls++
}

func (t *runTrace) done(id string) {
	if id == t.id {
		t.pending = false
	}
}

var reasonHint = map[harness.TerminalReason]string{
	harness.ReasonCompleted:         "모델이 이번 회차를 정상 종료했지만 텍스트 요약을 남기지 않았습니다. 사실과 자산은 이번 회차의 도구 호출 기록을 기준으로 확인하세요",
	harness.ReasonMaxTurns:          "단계 상한(MaxTurns)에 도달했습니다. SDK가 마무리를 수행하고 사실과 자산을 기록했으며, 의도는 실패 대신 exhausted로 표시되어 계획자가 방향을 바꾸어 계속할 수 있습니다",
	harness.ReasonTimeout:           "단일 실행의 시간 예산(MaxDuration)에 도달했습니다. 실행 중인 도구를 중단하고 바로 마무리하여 파악한 사실과 자산을 기록하며, 의도는 exhausted로 표시됩니다",
	harness.ReasonModelError:        "이번 모델 호출을 완료하지 못했습니다. 구체 원인과 재시도 여부는 기록된 진단 정보로 확인하세요",
	harness.ReasonOutputLimit:       "출력 상한과 이어쓰기 예산에 도달해 응답이 미완료로 끝났습니다. 생성된 내용은 남아 있지만 작업 완료를 뜻하지 않습니다",
	harness.ReasonStorageError:      "실행 기록을 읽거나 저장하지 못했습니다. 기존 기록을 확인한 뒤 재개해야 하며 앞서 실행한 도구를 자동으로 다시 실행하지 않습니다",
	harness.ReasonBlockingLimit:     "컨텍스트 길이가 절대 상한에 도달하여 요청 전 차단되었습니다. 의도의 범위를 줄이거나 도구 반환값을 압축하세요",
	harness.ReasonPromptTooLong:     "프롬프트가 너무 길고 컨텍스트 압축 재시도도 소진되어 계속 실행할 수 없습니다",
	harness.ReasonImageError:        "현재 모델은 이번 멀티모달 내용을 지원하지 않습니다. 이미지 지원 모델로 바꾸거나 도구에서 이미지를 반환하지 않도록 하세요",
	harness.ReasonStopHookPrevented: "Stop 훅이 이번 회차의 종료를 막았으며 이후 계속하지 못했습니다. 작업 Guard 규칙이 지나치게 엄격한지 확인하세요",
	harness.ReasonHookStopped:       "도구 또는 훅이 실행을 중지했습니다(예: 범위 밖 대상 또는 금지 명령). 마지막 tool_result의 차단 안내를 확인하세요",
	harness.ReasonAbortedStreaming:  "모델 출력 스트리밍 중 실행이 취소되었습니다",
	harness.ReasonAbortedTools:      "도구 실행 단계에서 실행이 취소되었습니다",
}

type outputLimitError struct{}

func (outputLimitError) Error() string {
	return "output_limit: " + reasonHint[harness.ReasonOutputLimit]
}
func (outputLimitError) NoReplay() bool   { return true }
func (outputLimitError) NoFallback() bool { return true }

// terminalText renders a terminal event with no final text into a compact summary
// and a Markdown detail block.
func terminalText(ctx context.Context, term *harness.Terminal, tr *runTrace) (string, string) {
	reason := term.Reason
	if reason == harness.ReasonModelError {
		return modelFailureText(term, tr)
	}
	aborted := reason == harness.ReasonAbortedStreaming || reason == harness.ReasonAbortedTools
	// Prompt may return ctx.Err directly without a terminal event. Preserve the
	// cancellation cause instead of falling back to an empty/unknown terminal reason.
	if reason == "" && ctx.Err() != nil {
		aborted = true
	}

	var sum string
	if aborted {
		_, short, _, ok := AbortReason(ctx)
		if !ok {
			short = "취소 원인을 가져오지 못했습니다"
		}
		stage := "실행 도중"
		switch reason {
		case harness.ReasonAbortedStreaming:
			stage = "모델 출력 단계"
		case harness.ReasonAbortedTools:
			stage = "도구 실행 단계"
		}
		sum = "(실행 중단: " + short + "; 중단 지점: " + stage + progressSuffix(term, tr) + ", 미완료)"
	} else if reason == harness.ReasonOutputLimit || reason == harness.ReasonStorageError {
		sum = "실행 미완료 (" + string(reason) + "): " + terminalReasonHint(reason)
	} else if reason == harness.ReasonMaxTurns || reason == harness.ReasonTimeout {
		sum = "(실행 예산 상한 도달(" + string(reason) + "), 마무리 후 사실 기록 완료" + progressSuffix(term, tr) + "; 이번 실행의 텍스트 요약 없음)"
	} else {
		hint := terminalReasonHint(reason)
		sum = "(텍스트 요약 없음, 종료 상태 " + terminalReasonLabel(reason) + "：" + firstLine(hint, 80) + "）"
	}

	var b strings.Builder
	b.WriteString(sum)
	b.WriteString("\n\n")
	displayReason := terminalReasonLabel(reason)
	fmt.Fprintf(&b, "- **종료 상태**: `%s` - %s\n", displayReason, terminalReasonHint(reason))
	if aborted {
		code, _, why, ok := AbortReason(ctx)
		if ok {
			fmt.Fprintf(&b, "- **중단 원인** (`%s`): %s\n", code, why)
		} else {
			b.WriteString("- **중단 원인**: 확인할 수 없습니다. 취소한 측에서 context.WithCancelCause로 명시적인 원인을 첨부하지 않았을 수 있습니다\n")
		}
	}
	if term.Err != nil {
		fmt.Fprintf(&b, "- **하위 오류**: `%v`\n", term.Err)
	}
	if (aborted || reason == harness.ReasonOutputLimit || reason == harness.ReasonStorageError) && strings.TrimSpace(term.Text) != "" {
		b.WriteString("- **종료 전 생성된 부분 출력**:\n\n")
		b.WriteString(term.Text)
		b.WriteString("\n\n")
	}
	if term.Turns > 0 {
		fmt.Fprintf(&b, "- **실행 횟수**: 모델 회차 %d회\n", term.Turns)
	}
	if !tr.startedAt.IsZero() {
		fmt.Fprintf(&b, "- **이번 실행 시간**: %s\n", roundDur(time.Since(tr.startedAt)))
	}
	if u := term.Usage; u.InputTokens+u.OutputTokens+u.CacheReadTokens+u.CacheWriteTokens > 0 {
		fmt.Fprintf(&b, "- **누적 토큰**: 입력 %d / 출력 %d / 캐시 읽기 %d / 캐시 쓰기 %d\n",
			u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens)
	}
	if tr.name == "" {
		b.WriteString("- **도구 호출**: 이번 실행은 도구 호출 전에 종료되었습니다\n")
	} else if tr.pending {
		fmt.Fprintf(&b, "- **중단 당시 실행 중인 도구**: `%s`(%s 동안 실행, **결과 미반환**)\n\n  ```json\n  %s\n  ```\n",
			tr.name, roundDur(time.Since(tr.at)), firstLine(tr.input, 300))
	} else {
		fmt.Fprintf(&b, "- **중단 전 마지막 도구**: `%s`(정상 반환 완료)\n", tr.name)
	}
	return sum, b.String()
}

func modelFailureText(term *harness.Terminal, tr *runTrace) (string, string) {
	label := "에이전트"
	if tr.role == "mainagent" {
		label = "메인 에이전트"
	} else if tr.role == "worker" {
		label = "Worker"
	}
	summary := "이번 " + label + " 호출을 완료하지 못했습니다 (model_error)"
	var b strings.Builder
	b.WriteString(summary + "\n\n")
	if term.Err != nil {
		fmt.Fprintf(&b, "- **오류**: %v\n", term.Err)
	}
	if tr.toolCalls == 0 {
		b.WriteString("- **도구 실행**: 이번 실행에서 도구 호출 전 실패했습니다.\n")
	} else {
		fmt.Fprintf(&b, "- **도구 실행**: 이번 실행에서 %d회 호출했습니다. 앞선 작업이 반영되었을 수 있으므로 실행 기록을 확인하세요.\n", tr.toolCalls)
	}
	d := ErrorDiagnostics(term.Err)
	if attempts, ok := d["attempts"].(int); ok {
		switch {
		case attempts == 0:
			b.WriteString("- **요청 시도**: HTTP 요청 전 실패했습니다. 재시도 없음.\n")
		case attempts == 1:
			b.WriteString("- **요청 시도**: 실패한 모델 요청의 HTTP 시도 1회, 재시도 없음.\n")
		default:
			fmt.Fprintf(&b, "- **요청 시도**: 실패한 모델 요청의 HTTP 시도 %d회, 재시도 %d회.\n", attempts, attempts-1)
		}
	} else {
		b.WriteString("- **요청 시도**: HTTP 시도·재시도 횟수를 확인할 수 없습니다.\n")
	}
	if term.Turns > 0 {
		fmt.Fprintf(&b, "- **모델 회차**: %d회. HTTP 시도 횟수와 별개입니다.\n", term.Turns)
	}
	return summary, b.String()
}

func terminalReasonLabel(reason harness.TerminalReason) string {
	if reason == "" {
		return "context_canceled"
	}
	return string(reason)
}

func terminalReasonHint(reason harness.TerminalReason) string {
	if hint := reasonHint[reason]; hint != "" {
		return hint
	}
	if reason == "" {
		return "실행 context가 취소되었지만 하위 계층에서 Terminal 이벤트를 생성하지 않았습니다"
	}
	return "알 수 없는 종료 상태입니다. harness에 TerminalReason이 추가되었을 수 있으므로 reasonHint를 보완하세요"
}

func progressSuffix(term *harness.Terminal, tr *runTrace) string {
	var parts []string
	if term.Turns > 0 {
		parts = append(parts, fmt.Sprintf("%d회", term.Turns))
	}
	if !tr.startedAt.IsZero() {
		parts = append(parts, roundDur(time.Since(tr.startedAt)))
	}
	if len(parts) == 0 {
		return ""
	}
	return ", 실행량 " + strings.Join(parts, " / ")
}

func roundDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return d.Round(100 * time.Millisecond).String()
	case d < time.Hour:
		return d.Round(time.Second).String()
	default:
		return d.Round(time.Minute).String()
	}
}
