package agentcore

import (
	"context"
	"iter"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
)

// The host owns the wire adapter; these tests exercise the engine contract.
type fixtureProvider func(llm.CompletionRequest) llm.Message

func (p fixtureProvider) Stream(_ context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		message := p(req)
		stop := "end_turn"
		for _, block := range message.Content {
			if block.Type == llm.BlockToolUse {
				stop = "tool_use"
				if !yield(llm.StreamEvent{Type: llm.SEToolUseStart, ToolID: block.ID, ToolName: block.Name}, nil) {
					return
				}
				if !yield(llm.StreamEvent{Type: llm.SEToolInputJSON, Text: string(block.Input)}, nil) {
					return
				}
			} else if block.Type == llm.BlockText {
				if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: block.Text}, nil) {
					return
				}
			}
		}
		if !yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: stop, Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}}, nil) {
			return
		}
		yield(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
	}
}
func (p fixtureProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	acc := llm.NewAccumulator()
	for ev, err := range p.Stream(ctx, req) {
		if err != nil {
			return llm.Message{}, "", llm.Usage{}, err
		}
		acc.Add(ev)
	}
	return acc.Message(), acc.StopReason, acc.Usage, nil
}
func textProvider(text string) fixtureProvider {
	return func(llm.CompletionRequest) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(text)}}
	}
}

func TestEndToEnd(t *testing.T) {
	calls := 0
	provider := fixtureProvider(func(req llm.CompletionRequest) llm.Message {
		calls++
		if calls == 1 {
			return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "tu1", Name: "Bash", Input: []byte(`{"command":"echo loop-works"}`)}}}
		}
		found := false
		for _, message := range req.Messages {
			for _, block := range message.Content {
				if block.Type == llm.BlockToolResult {
					for _, text := range block.Content {
						found = found || strings.Contains(text.Text, "loop-works")
					}
				}
			}
		}
		if !found {
			t.Error("second request missing tool result")
		}
		return textProvider("The command ran.")(req)
	})
	s := NewSession(Options{Provider: provider, SystemPrompt: []string{"test"}, Tools: []tool.CoreTool{tool.NewBash()}, PermissionMode: permission.ModeBypass, WorkingDir: t.TempDir()})
	var text string
	for ev, err := range s.Prompt(context.Background(), "run echo") {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == harness.KindText {
			text += ev.Text
		}
	}
	if text != "The command ran." || calls != 2 || len(s.Messages()) != 4 {
		t.Fatalf("text=%q calls=%d messages=%d", text, calls, len(s.Messages()))
	}
}
func TestRunConvenience(t *testing.T) {
	out, err := Run(context.Background(), Options{Provider: textProvider("hello"), SystemPrompt: []string{"hi"}, Tools: []tool.CoreTool{}}, "say hello")
	if err != nil || out != "hello" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}
