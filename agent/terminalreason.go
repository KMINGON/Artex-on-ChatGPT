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
	harness.ReasonCompleted:         "模型正常结束了本轮，但没有留下文字总结；事实和资产以本轮工具调用记录为准",
	harness.ReasonMaxTurns:          "达到步数上限(MaxTurns)：SDK 已执行收尾并写回事实和资产，意图会标记为 exhausted，供规划者换方向继续，而不是作为失败处理",
	harness.ReasonTimeout:           "达到单次运行的墙钟预算(MaxDuration)：到点会打断在跑的工具并就地进收尾，把已识别的事实和资产写回，意图会标记为 exhausted",
	harness.ReasonModelError:        "이번 모델 호출을 완료하지 못했습니다. 구체 원인과 재시도 여부는 기록된 진단 정보로 확인하세요",
	harness.ReasonBlockingLimit:     "上下文长度达到硬上限，请求在发出前被拦截；应收窄意图粒度或压缩工具返回",
	harness.ReasonPromptTooLong:     "提示词过长且上下文压缩重试已经用尽，无法继续执行",
	harness.ReasonImageError:        "当前模型不支持本轮多模态内容；请切换支持视觉的模型或避免工具返回图片",
	harness.ReasonStopHookPrevented: "Stop 钩子阻止本轮结束，随后未能继续；请检查任务 Guard 规则是否过严",
	harness.ReasonHookStopped:       "工具或钩子主动停止继续执行，例如越界目标或禁用命令；请检查最后一条 tool_result 的拦截说明",
	harness.ReasonAbortedStreaming:  "运行在模型输出流式生成阶段被取消",
	harness.ReasonAbortedTools:      "运行在工具执行阶段被取消",
}

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
			short = "未能取得取消原因"
		}
		stage := "执行过程中"
		switch reason {
		case harness.ReasonAbortedStreaming:
			stage = "模型输出阶段"
		case harness.ReasonAbortedTools:
			stage = "工具执行阶段"
		}
		sum = "（运行被中断：" + short + "；停在" + stage + progressSuffix(term, tr) + "，未完成）"
	} else if reason == harness.ReasonMaxTurns || reason == harness.ReasonTimeout {
		sum = "（达到运行预算上限(" + string(reason) + ")，已收尾写回事实" + progressSuffix(term, tr) + "；本次无文字总结）"
	} else {
		hint := terminalReasonHint(reason)
		sum = "（无文字总结，终态 " + terminalReasonLabel(reason) + "：" + firstLine(hint, 80) + "）"
	}

	var b strings.Builder
	b.WriteString(sum)
	b.WriteString("\n\n")
	displayReason := terminalReasonLabel(reason)
	fmt.Fprintf(&b, "- **终态**: `%s` - %s\n", displayReason, terminalReasonHint(reason))
	if aborted {
		code, _, why, ok := AbortReason(ctx)
		if ok {
			fmt.Fprintf(&b, "- **中断原因** (`%s`): %s\n", code, why)
		} else {
			b.WriteString("- **中断原因**: 无法取得；取消方可能没有通过 context.WithCancelCause 附加具名原因\n")
		}
	}
	if term.Err != nil {
		fmt.Fprintf(&b, "- **底层错误**: `%v`\n", term.Err)
	}
	if aborted && strings.TrimSpace(term.Text) != "" {
		b.WriteString("- **取消前已生成的部分输出**:\n\n")
		b.WriteString(term.Text)
		b.WriteString("\n\n")
	}
	if term.Turns > 0 {
		fmt.Fprintf(&b, "- **已执行**: %d 轮模型回合\n", term.Turns)
	}
	if !tr.startedAt.IsZero() {
		fmt.Fprintf(&b, "- **本次运行耗时**: %s\n", roundDur(time.Since(tr.startedAt)))
	}
	if u := term.Usage; u.InputTokens+u.OutputTokens+u.CacheReadTokens+u.CacheWriteTokens > 0 {
		fmt.Fprintf(&b, "- **累计 token**: 输入 %d / 输出 %d / 缓存读 %d / 缓存写 %d\n",
			u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens)
	}
	if tr.name == "" {
		b.WriteString("- **工具调用**: 本次运行还没有发出工具调用就结束了\n")
	} else if tr.pending {
		fmt.Fprintf(&b, "- **中断时正在执行的工具**: `%s`（已运行 %s，**未返回结果**）\n\n  ```json\n  %s\n  ```\n",
			tr.name, roundDur(time.Since(tr.at)), firstLine(tr.input, 300))
	} else {
		fmt.Fprintf(&b, "- **中断前最后一个工具**: `%s`（已正常返回）\n", tr.name)
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
		return "运行的 context 已取消，但底层没有产生 Terminal 事件"
	}
	return "未知终态；harness 可能新增了 TerminalReason，请补充 reasonHint"
}

func progressSuffix(term *harness.Terminal, tr *runTrace) string {
	var parts []string
	if term.Turns > 0 {
		parts = append(parts, fmt.Sprintf("%d 轮", term.Turns))
	}
	if !tr.startedAt.IsZero() {
		parts = append(parts, roundDur(time.Since(tr.startedAt)))
	}
	if len(parts) == 0 {
		return ""
	}
	return "，已运行 " + strings.Join(parts, " / ")
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
