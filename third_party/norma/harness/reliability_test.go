package harness

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
)

func TestCancelledQueueCannotRestartOnSettlementContext(t *testing.T) {
	for _, replace := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
		var late atomic.Int32
		fn := tool.Build(tool.Spec{Name: "ReadFixture", Schema: map[string]any{"type": "object"}, Concurrent: func(json.RawMessage) bool { return true },
			Run: func(_ context.Context, _ json.RawMessage, tc *tool.ToolContext) (tool.Result, error) {
				if tc.ToolUseID == "first" {
					close(entered)
					<-release
					close(done)
				} else {
					late.Add(1)
				}
				return tool.Text("ok"), nil
			}})
		l := &loop{ctx: context.Background(), toolCtx: ctx, in: QueryInput{Tools: tool.NewRegistry(fn), PermissionMode: permission.ModeBypass}, sem: make(chan struct{}, 1), yield: func(Event, error) bool { return true }}
		e := newStreamExec(l)
		e.add(llm.ContentBlock{Type: llm.BlockToolUse, ID: "first", Name: "ReadFixture", Input: []byte(`{}`)})
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("first tool did not start")
		}
		e.add(llm.ContentBlock{Type: llm.BlockToolUse, ID: "queued", Name: "ReadFixture", Input: []byte(`{}`)})
		cancel()
		if _, ab := e.finish(); !ab {
			t.Fatal("expected synthetic cancellation")
		}
		if replace {
			l.toolCtx = context.Background()
		}
		close(release)
		<-done
		// Joining the semaphore after the active handler exits permits any old
		// waiter to contend; its cancelled snapshot still prohibits handler entry.
		time.Sleep(10 * time.Millisecond)
		if late.Load() != 0 {
			t.Fatal("queued handler started after terminal")
		}
		results := e.results()
		if len(results) != 2 || !results[0].IsError || !results[1].IsError {
			t.Fatalf("synthetic results changed: %+v", results)
		}
	}
}

func TestTruncatedToolIsNeverExecuted(t *testing.T) {
	var calls atomic.Int32
	fn := tool.Build(tool.Spec{Name: "Effect", Schema: map[string]any{"type": "object"}, Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
		calls.Add(1)
		return tool.Text("bad"), nil
	}})
	m := &scriptedModel{turns: [][]llm.StreamEvent{{
		{Type: llm.SEToolUseStart, ToolID: "x", ToolName: "Effect"},
		{Type: llm.SEToolInputJSON, Text: `{"unfinished":`},
		{Type: llm.SEMessageDelta, StopReason: "max_tokens"},
		{Type: llm.SEMessageStop},
	}}}
	term := drain(t, QueryInput{Tools: tool.NewRegistry(fn), PermissionMode: permission.ModeBypass}, QueryDeps{CallModel: m.call})
	if calls.Load() != 0 || term.Reason != ReasonOutputLimit || m.calls != 1 {
		t.Fatalf("calls=%d reason=%s models=%d", calls.Load(), term.Reason, m.calls)
	}
}

func TestNonStreamingErrorRetainsKnownUsage(t *testing.T) {
	failure := errors.New("fixture interrupted response")
	want := llm.Usage{InputTokens: 19, OutputTokens: 7, CacheReadTokens: 3}
	calls := 0
	term := drain(t, QueryInput{NonStreaming: true, Tools: tool.NewRegistry()}, QueryDeps{CallModelSync: func(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		calls++
		return llm.Message{}, "", want, failure
	}})
	if term.Reason != ReasonModelError || !errors.Is(term.Err, failure) || term.Usage != want || calls != 1 {
		t.Fatalf("known usage lost or failure retried: %+v calls=%d", term, calls)
	}
}
