package server

import (
	"errors"
	"testing"

	"github.com/Autumn-27/norma/harness"
)

func TestWorkerTerminalStateKeepsInterruptedValidationUnfinished(t *testing.T) {
	for _, tc := range []struct {
		reason harness.TerminalReason
		want   string
	}{
		{harness.ReasonCompleted, "done"},
		{harness.ReasonMaxTurns, "exhausted"},
		{harness.ReasonTimeout, "exhausted"},
		{harness.ReasonOutputLimit, "exhausted"},
		{harness.ReasonBlockingLimit, "exhausted"},
		{harness.ReasonPromptTooLong, "exhausted"},
		{harness.ReasonStorageError, "blocked"},
		{harness.ReasonModelError, "blocked"},
		{harness.ReasonImageError, "blocked"},
		{harness.ReasonHookStopped, "blocked"},
		{harness.ReasonStopHookPrevented, "blocked"},
		{harness.ReasonAbortedStreaming, "blocked"},
		{harness.ReasonAbortedTools, "blocked"},
		{"", "blocked"},
		{"future_incomplete_reason", "blocked"},
	} {
		t.Run(string(tc.reason), func(t *testing.T) {
			if got := workerTerminalState(tc.reason, nil); got != tc.want {
				t.Fatalf("reason=%q state=%q, want %q", tc.reason, got, tc.want)
			}
		})
	}
	if got := workerTerminalState(harness.ReasonCompleted, errors.New("late persistence failure")); got != "blocked" {
		t.Fatalf("failed completion state=%q", got)
	}
	if got := workerTerminalState(harness.ReasonOutputLimit, errors.New("partial output")); got != "exhausted" {
		t.Fatalf("partial output state=%q", got)
	}
}
