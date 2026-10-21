package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

func TestCaptureOutputLimitKeepsPartialTextAndReportsIncomplete(t *testing.T) {
	calls := 0
	provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		calls++
		if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "partial answer"}, nil) {
			return
		}
		yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "max_tokens"}, nil)
	}}
	var result db.Activity
	text, reason, err := captureRun(t.Context(), agentcore.Options{Provider: provider, MaxTurns: 10}, "fixture", func(a db.Activity) {
		if a.Kind == "result" {
			result = a
		}
	})
	if reason != harness.ReasonOutputLimit || err == nil || calls != 4 {
		t.Fatalf("reason=%s err=%v calls=%d", reason, err, calls)
	}
	if text != "partial answer" || !result.IsError || !strings.Contains(result.Detail, "partial answer") || !strings.Contains(result.Summary, "output_limit") {
		t.Fatalf("lost incomplete result: text=%q activity=%+v", text, result)
	}
	var protected interface {
		NoReplay() bool
		NoFallback() bool
	}
	if !errors.As(err, &protected) || !protected.NoReplay() || !protected.NoFallback() {
		t.Fatal("partial output may be automatically replayed")
	}
}
