package compaction

import (
	"context"
	"iter"
	"reflect"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

type stoppedSummaryProvider struct {
	stop  string
	tool  bool
	calls int
}

func (p *stoppedSummaryProvider) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.calls++
	msg := asstText("partial summary")
	if p.tool {
		msg.Content = append(msg.Content, llm.ContentBlock{Type: llm.BlockToolUse, ID: "call", Name: "Read"})
	}
	return msg, p.stop, llm.Usage{}, nil
}

func (p *stoppedSummaryProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		p.calls++
		if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "partial summary"}, nil) {
			return
		}
		if p.tool && !yield(llm.StreamEvent{Type: llm.SEToolUseStart, ToolName: "Read"}, nil) {
			return
		}
		if !yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: p.stop}, nil) {
			return
		}
		yield(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
	}
}

func TestSummarizerRejectsIncompleteCompletionWithoutRetry(t *testing.T) {
	for _, nonStreaming := range []bool{false, true} {
		for _, stop := range []string{"", "max_tokens", "tool_use"} {
			p := &stoppedSummaryProvider{stop: stop}
			text, err := ProviderSummarizer(p, 0, nonStreaming)(t.Context(), []llm.Message{llm.UserText("original history")})
			if err == nil || text != "" || p.calls != 1 {
				t.Fatalf("partial summary accepted or retried: streaming=%v stop=%q calls=%d text=%q err=%v", !nonStreaming, stop, p.calls, text, err)
			}
		}
		p := &stoppedSummaryProvider{stop: "end_turn", tool: true}
		if _, err := ProviderSummarizer(p, 0, nonStreaming)(t.Context(), nil); err == nil {
			t.Fatal("summary tool output was accepted")
		}
	}
}

func TestFailedSummaryPreservesOriginalHistory(t *testing.T) {
	p := &stoppedSummaryProvider{stop: "max_tokens"}
	c := New(smallWindow(), ProviderSummarizer(p, 0, false))
	var messages, original []llm.Message
	for range 8 {
		messages = append(messages, bigMsg(llm.RoleUser, 4000))
		original = append(original, bigMsg(llm.RoleUser, 4000))
	}
	out, compacted := c.Reactive(t.Context(), messages)
	if p.calls != 1 {
		t.Fatalf("summary was not attempted exactly once: calls=%d", p.calls)
	}
	if compacted || !reflect.DeepEqual(out, original) || !reflect.DeepEqual(messages, original) {
		t.Fatal("failed summary changed the source history")
	}
}
