package compaction

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/Autumn-27/norma/llm"
)

// Summary generation limits. Overflow recovery partitions complete rounds; it
// never drops an earlier round merely to make a summary request fit.
const (
	compactMaxOutputTokens = 20000 // COMPACT_MAX_OUTPUT_TOKENS
	maxStreamingRetries    = 2     // MAX_COMPACT_STREAMING_RETRIES
	summaryPartMarker      = "다음은 긴 대화의 연속된 한 구간입니다. 다른 구간은 별도로 요약합니다. 이 구간의 사용자 조건·근거·미완료 작업·참조 ID를 보존하고, 제시되지 않은 앞뒤 맥락이나 완료 여부를 추측하지 마세요."
)

// summarySystemPrompt is the summarizer's system prompt (== claude-code
// streamCompactSummary fallback path). Thinking is disabled and no tools are
// offered; the no-tools guardrails live in the user prompt (§7).
const summarySystemPrompt = "You are a helpful AI assistant tasked with summarizing conversations."

// ProviderSummarizer builds a Summarizer backed by an llm.Provider. It replays
// the real conversation messages (not a rendered text transcript) followed by the
// summary-request user message, with thinking disabled and output capped at
// min(20000, maxOutputTokens). It retries transient stream failures
// (MAX_COMPACT_STREAMING_RETRIES). If the summary request itself overflows, it
// summarizes smaller, complete round groups and combines every part in order.
// A failed part invalidates the whole summary, leaving the caller's history intact.
func ProviderSummarizer(p llm.Provider, maxOutputTokens int, nonStreaming bool) Summarizer {
	maxOut := compactMaxOutputTokens
	if maxOutputTokens > 0 && maxOutputTokens < maxOut {
		maxOut = maxOutputTokens
	}
	return func(ctx context.Context, msgs []llm.Message) (string, error) {
		var summarize func([]llm.Message, bool) ([]string, error)
		summarize = func(head []llm.Message, part bool) ([]string, error) {
			requestHead := head
			if part {
				requestHead = append([]llm.Message{llm.UserText(summaryPartMarker)}, head...)
			}
			raw, err := streamSummary(ctx, p, maxOut, requestHead, nonStreaming)
			if err == nil {
				body := formatCompactSummary(raw)
				if body == "" {
					return nil, errors.New("대화 요약에 보존할 본문이 없습니다")
				}
				return []string{body}, nil
			}
			if !IsOverflow(err) || summaryRetryForbidden(err) {
				return nil, err
			}
			left, right, ok := splitSummaryReplay(head)
			if !ok {
				return nil, err // an indivisible round cannot be shortened without loss
			}
			before, err := summarize(left, true)
			if err != nil {
				return nil, err
			}
			after, err := summarize(right, true)
			if err != nil {
				return nil, err
			}
			return append(before, after...), nil
		}
		parts, err := summarize(pairForReplay(msgs), false)
		if err != nil {
			return "", err
		}
		if len(parts) == 1 {
			return parts[0], nil
		}
		var joined strings.Builder
		for index, part := range parts {
			fmt.Fprintf(&joined, "\n\n【대화 구간 %d/%d — 시간순】\n%s", index+1, len(parts), part)
		}
		return strings.TrimSpace(joined.String()), nil
	}
}

// streamSummary issues one summary request (head + summary-request message),
// retrying transient (non-overflow) stream failures. Overflow is returned
// immediately so the caller can split the input without losing any rounds.
func streamSummary(ctx context.Context, p llm.Provider, maxOut int, head []llm.Message, nonStreaming bool) (string, error) {
	reqMsgs := make([]llm.Message, 0, len(head)+1)
	reqMsgs = append(reqMsgs, head...)
	reqMsgs = append(reqMsgs, llm.UserText(compactPrompt))
	req := llm.CompletionRequest{
		System:    []string{summarySystemPrompt},
		Messages:  reqMsgs,
		MaxTokens: maxOut,
		Thinking:  "disabled",
	}
	var lastErr error
	for range maxStreamingRetries {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var text string
		var stopReason string
		var tools bool
		var callErr error
		if nonStreaming {
			msg, stop, _, err := p.Complete(ctx, req)
			text, callErr = msg.Text(), err
			stopReason = stop
			for _, block := range msg.Content {
				tools = tools || block.Type == llm.BlockToolUse
			}
		} else {
			acc := llm.NewAccumulator()
			for ev, err := range p.Stream(ctx, req) {
				if err != nil {
					callErr = err
					break
				}
				acc.Add(ev)
				tools = tools || ev.Type == llm.SEToolUseStart || ev.Type == llm.SEToolInputJSON
			}
			if callErr == nil {
				text = acc.Message().Text()
				stopReason = acc.StopReason
			}
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if callErr == nil {
			if stopReason != "end_turn" || tools || strings.TrimSpace(text) == "" {
				return "", fmt.Errorf("summary did not complete normally (stop_reason=%q)", stopReason)
			}
			return text, nil
		}
		if summaryRetryForbidden(callErr) {
			return "", callErr
		}
		if IsOverflow(callErr) {
			return "", callErr
		}
		lastErr = callErr // transient — retry
	}
	return "", lastErr
}

func summaryRetryForbidden(err error) bool {
	var noFallback interface{ NoFallback() bool }
	var noReplay interface{ NoReplay() bool }
	return errors.As(err, &noReplay) && noReplay.NoReplay() || errors.As(err, &noFallback) && noFallback.NoFallback()
}

// pairForReplay preserves complete tool exchanges. Unpaired calls/results are
// represented as explicitly uncertain summary material, not replayable tool
// blocks: missing metadata must not erase their inputs, evidence, or images.
func pairForReplay(msgs []llm.Message) []llm.Message {
	useIDs := map[string]bool{}
	resIDs := map[string]bool{}
	for _, m := range msgs {
		for _, b := range m.Content {
			switch b.Type {
			case llm.BlockToolUse:
				useIDs[b.ID] = true
			case llm.BlockToolResult:
				resIDs[b.ToolUseID] = true
			}
		}
	}
	out := make([]llm.Message, 0, len(msgs))
	for _, m := range msgs {
		nc := make([]llm.ContentBlock, 0, len(m.Content))
		changed := false
		for _, b := range m.Content {
			switch b.Type {
			case llm.BlockToolUse:
				if !resIDs[b.ID] {
					nc = append(nc, llm.TextBlock(fmt.Sprintf("[연결된 결과가 없는 도구 호출 기록 — 실행 여부·성공 여부 미확인]\nID: %q\n도구: %q\n입력 원문:\n%s", b.ID, b.Name, string(b.Input))))
					changed = true
					continue
				}
			case llm.BlockToolResult:
				if !useIDs[b.ToolUseID] {
					nc = append(nc, llm.TextBlock(fmt.Sprintf("[연결된 호출이 없는 도구 결과 기록 — 실행 맥락·성공 여부를 추론하지 마세요]\n호출 ID: %q\n오류 표시: %t\n결과 원문:", b.ToolUseID, b.IsError)))
					nc = append(nc, b.Content...)
					changed = true
					continue
				}
			}
			nc = append(nc, b)
		}
		if changed {
			m.ProviderData = nil
		}
		if len(nc) > 0 || len(m.ProviderData) > 0 {
			if len(nc) > 0 {
				m.Content = nc
			}
			out = append(out, m)
		}
	}
	return out
}

// splitSummaryReplay chooses a balanced boundary between API rounds, keeping
// even interleaved tool exchanges together. Each side is strictly smaller, so
// repeated overflow splitting is finite without discarding a single group.
func splitSummaryReplay(msgs []llm.Message) ([]llm.Message, []llm.Message, bool) {
	groups := groupByAPIRound(msgs)
	if len(groups) < 2 {
		return nil, nil, false
	}
	lastReference := map[string]int{}
	weights := make([]int, len(groups))
	var total int
	for index, group := range groups {
		weights[index] = max(1, EstimateTokens(group))
		total += weights[index]
		for _, message := range group {
			for _, block := range message.Content {
				if block.Type == llm.BlockToolUse {
					lastReference[block.ID] = index
				} else if block.Type == llm.BlockToolResult {
					lastReference[block.ToolUseID] = index
				}
			}
		}
	}
	cut, position, weight, end, best := 0, 0, 0, 0, total+1
	for index, group := range groups[:len(groups)-1] {
		position += len(group)
		weight += weights[index]
		for _, message := range group {
			for _, block := range message.Content {
				if block.Type == llm.BlockToolUse {
					end = max(end, lastReference[block.ID])
				} else if block.Type == llm.BlockToolResult {
					end = max(end, lastReference[block.ToolUseID])
				}
			}
		}
		if end > index {
			continue
		}
		distance := total - 2*weight
		if distance < 0 {
			distance = -distance
		}
		if distance < best {
			cut, best = position, distance
		}
	}
	if cut == 0 {
		return nil, nil, false
	}
	return msgs[:cut], msgs[cut:], true
}

// groupByAPIRound partitions messages into one group per API round-trip: a new
// group begins at each assistant message. Mirrors claude-code
// groupMessagesByApiRound (Norma has one assistant message per round, so role
// boundaries == message-id boundaries). Group 0 holds any leading preamble.
func groupByAPIRound(msgs []llm.Message) [][]llm.Message {
	var groups [][]llm.Message
	var cur []llm.Message
	for _, m := range msgs {
		if m.Role == llm.RoleAssistant && len(cur) > 0 {
			groups = append(groups, cur)
			cur = nil
		}
		cur = append(cur, m)
	}
	if len(cur) > 0 {
		groups = append(groups, cur)
	}
	return groups
}

var (
	analysisRe = regexp.MustCompile(`(?s)<analysis>.*?</analysis>`)
	summaryRe  = regexp.MustCompile(`(?s)<summary>(.*?)</summary>`)
	blankRe    = regexp.MustCompile(`\n{2,}`)
)

// formatCompactSummary post-processes the model's raw output (== claude-code
// formatCompactSummary): strip the <analysis> scratchpad, unwrap <summary> into a
// "Summary:" header, and collapse blank-line runs.
func formatCompactSummary(raw string) string {
	s := analysisRe.ReplaceAllString(raw, "")
	if m := summaryRe.FindStringSubmatch(s); m != nil {
		body := strings.TrimSpace(m[1])
		if body == "" {
			return ""
		}
		s = "Summary:\n" + body
	}
	s = blankRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// compactPrompt is the full summary-request user message:
// NO_TOOLS_PREAMBLE + BASE_COMPACT_PROMPT + NO_TOOLS_TRAILER (§7).
var compactPrompt = noToolsPreamble + baseCompactPrompt + noToolsTrailer

// noToolsPreamble == claude-code NO_TOOLS_PREAMBLE (prompt.ts:19-26).
const noToolsPreamble = `CRITICAL: Respond with TEXT ONLY. Do NOT call any tools.

- Do NOT use Read, Bash, Grep, Glob, Edit, Write, or ANY other tool.
- You already have all the context you need in the conversation above.
- Tool calls will be REJECTED and will waste your only turn — you will fail the task.
- Your entire response must be plain text: an <analysis> block followed by a <summary> block.

`

// noToolsTrailer == claude-code NO_TOOLS_TRAILER (prompt.ts:269-272).
const noToolsTrailer = `

REMINDER: Do NOT call any tools. Respond with plain text only — an <analysis> block followed by a <summary> block. Tool calls will be rejected and you will fail the task.`

// baseCompactPrompt == claude-code BASE_COMPACT_PROMPT (prompt.ts:61-143), verbatim
// (including the DETAILED_ANALYSIS_INSTRUCTION_BASE it interpolates and the source
// trailing spaces on the "All user messages:" and "...in your response. " lines).
const baseCompactPrompt = `Your task is to create a detailed summary of the conversation so far, paying close attention to the user's explicit requests and your previous actions.
This summary should be thorough in capturing technical details, code patterns, and architectural decisions that would be essential for continuing development work without losing context.

Before providing your final summary, wrap your analysis in <analysis> tags to organize your thoughts and ensure you've covered all necessary points. In your analysis process:

1. Chronologically analyze each message and section of the conversation. For each section thoroughly identify:
   - The user's explicit requests and intents
   - Your approach to addressing the user's requests
   - Key decisions, technical concepts and code patterns
   - Specific details like:
     - file names
     - full code snippets
     - function signatures
     - file edits
   - Errors that you ran into and how you fixed them
   - Pay special attention to specific user feedback that you received, especially if the user told you to do something differently.
2. Double-check for technical accuracy and completeness, addressing each required element thoroughly.

Your summary should include the following sections:

1. Primary Request and Intent: Capture all of the user's explicit requests and intents in detail
2. Key Technical Concepts: List all important technical concepts, technologies, and frameworks discussed.
3. Files and Code Sections: Enumerate specific files and code sections examined, modified, or created. Pay special attention to the most recent messages and include full code snippets where applicable and include a summary of why this file read or edit is important.
4. Errors and fixes: List all errors that you ran into, and how you fixed them. Pay special attention to specific user feedback that you received, especially if the user told you to do something differently.
5. Problem Solving: Document problems solved and any ongoing troubleshooting efforts.
6. All user messages: List ALL user messages that are not tool results. These are critical for understanding the users' feedback and changing intent.
7. Pending Tasks: Outline any pending tasks that you have explicitly been asked to work on.
8. Current Work: Describe in detail precisely what was being worked on immediately before this summary request, paying special attention to the most recent messages from both user and assistant. Include file names and code snippets where applicable.
9. Optional Next Step: List the next step that you will take that is related to the most recent work you were doing. IMPORTANT: ensure that this step is DIRECTLY in line with the user's most recent explicit requests, and the task you were working on immediately before this summary request. If your last task was concluded, then only list next steps if they are explicitly in line with the users request. Do not start on tangential requests or really old requests that were already completed without confirming with the user first.
                       If there is a next step, include direct quotes from the most recent conversation showing exactly what task you were working on and where you left off. This should be verbatim to ensure there's no drift in task interpretation.

Here's an example of how your output should be structured:

<example>
<analysis>
[Your thought process, ensuring all points are covered thoroughly and accurately]
</analysis>

<summary>
1. Primary Request and Intent:
   [Detailed description]

2. Key Technical Concepts:
   - [Concept 1]
   - [Concept 2]
   - [...]

3. Files and Code Sections:
   - [File Name 1]
      - [Summary of why this file is important]
      - [Summary of the changes made to this file, if any]
      - [Important Code Snippet]
   - [File Name 2]
      - [Important Code Snippet]
   - [...]

4. Errors and fixes:
    - [Detailed description of error 1]:
      - [How you fixed the error]
      - [User feedback on the error if any]
    - [...]

5. Problem Solving:
   [Description of solved problems and ongoing troubleshooting]

6. All user messages:
    - [Detailed non tool use user message]
    - [...]

7. Pending Tasks:
   - [Task 1]
   - [Task 2]
   - [...]

8. Current Work:
   [Precise description of current work]

9. Optional Next Step:
   [Optional Next step to take]

</summary>
</example>

Please provide your summary based on the conversation so far, following this structure and ensuring precision and thoroughness in your response.

There may be additional summarization instructions provided in the included context. If so, remember to follow these instructions when creating the above summary. Examples of instructions include:
<example>
## Compact Instructions
When summarizing the conversation focus on typescript code changes and also remember the mistakes you made and how you fixed them.
</example>

<example>
# Summary instructions
When you are using compact - please focus on test output and code changes. Include file reads verbatim.
</example>`
