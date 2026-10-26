package guard

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/hook"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
)

func TestToolResultsKeepTargetResponsesSeparateFromPlatformBlocks(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		toolError      bool
		block          bool
	}{
		{"target authentication error", "HTTP/1.1 500\n<title>Blocked illegal activity by fixture</title>", false, false},
		{"target forbidden response", "HTTP/1.1 403 Forbidden", false, false},
		{"target unavailable", "HTTP/1.1 502 Bad Gateway", false, false},
		{"target retired interface", "HTTP/1.1 410 deprecated for security reasons", false, false},
		{"tool execution error", "command failed: forbidden", true, false},
		{"target copies hook wording", "Blocked by hook: target supplied text", false, false},
		{"platform intercept", "unused target response", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := New()
			if tc.block {
				g.Hooks().On(hook.PreToolUse, func(_ context.Context, ev hook.Event) hook.Result {
					return g.block(ev.ToolName, systemBlockMessage("검사에서 지정한 규칙"), "")
				})
			}
			toolCalls := 0
			probe := tool.Build(tool.Spec{
				Name: "Bash", Schema: map[string]any{"type": "object"},
				Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
					return permission.Decision{Behavior: permission.Allow}
				},
				Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
					toolCalls++
					result := tool.Text(tc.response)
					result.IsError = tc.toolError
					return result, nil
				},
			})
			var received *llm.ContentBlock
			modelCalls := 0
			deps := harness.QueryDeps{CallModel: func(_ context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
				modelCalls++
				for _, msg := range req.Messages {
					for _, block := range msg.Content {
						if block.Type == llm.BlockToolResult && block.ToolUseID == "probe" {
							copy := block
							received = &copy
						}
					}
				}
				return func(yield func(llm.StreamEvent, error) bool) {
					events := []llm.StreamEvent{{Type: llm.SETextDelta, Text: "done"}, {Type: llm.SEMessageDelta, StopReason: "end_turn"}}
					if modelCalls == 1 {
						events = []llm.StreamEvent{{Type: llm.SEToolUseStart, ToolID: "probe", ToolName: "Bash"}, {Type: llm.SEToolInputJSON, Text: `{}`}, {Type: llm.SEMessageDelta, StopReason: "tool_use"}}
					}
					for _, event := range events {
						if !yield(event, nil) {
							return
						}
					}
				}
			}}
			in := harness.QueryInput{Messages: []llm.Message{llm.UserText("inspect fixture")}, Tools: tool.NewRegistry(probe), Hooks: g.Hooks(), PermissionMode: permission.ModeBypass, MaxTurns: 3}
			for _, err := range harness.Query(t.Context(), in, deps) {
				if err != nil {
					t.Fatal(err)
				}
			}
			if received == nil || modelCalls != 2 {
				t.Fatalf("tool result was not delivered to next model turn: received=%v calls=%d", received != nil, modelCalls)
			}
			if received.IsError != (tc.block || tc.toolError) {
				t.Fatalf("tool outcome changed: %+v", received)
			}
			if tc.block {
				if toolCalls != 0 || !strings.HasPrefix(received.Content[0].Text, "Blocked by hook: [ARTEX 플랫폼 정책") || g.Attributions()["blocked"] != 1 {
					t.Fatalf("platform block not preserved: executed=%d result=%+v attribution=%v", toolCalls, received, g.Attributions())
				}
			} else {
				if toolCalls != 1 || received.Content[0].Text != tc.response || g.Attributions()["blocked"] != 0 {
					t.Fatalf("target result reclassified as a platform block: executed=%d result=%+v attribution=%v", toolCalls, received, g.Attributions())
				}
				class := "ok"
				if tc.toolError {
					class = "error"
				}
				if g.Attributions()[class] != 1 {
					t.Fatalf("actual execution outcome lost: %v", g.Attributions())
				}
			}
		})
	}
}
