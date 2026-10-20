package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// runOutputError prevents an outer worker retry from replaying a run whose
// earlier output or tools may already have had effects.
type runOutputError struct{ cause error }

func (e *runOutputError) Error() string  { return e.cause.Error() }
func (e *runOutputError) Unwrap() error  { return e.cause }
func (e *runOutputError) NoReplay() bool { return true }

func protectRunOutput(err error, trace *runTrace) error {
	if err != nil && (trace.outputEmitted || trace.toolCalls > 0) {
		return &runOutputError{cause: err}
	}
	return err
}

// ErrorDiagnostics accepts only the provider's bounded, body-free diagnostic
// contract. Error() text is intentionally not copied into metadata.
func ErrorDiagnostics(err error) map[string]any {
	var source interface{ Diagnostics() map[string]any }
	if errors.As(err, &source) {
		return source.Diagnostics()
	}
	return nil
}

func failureActivity(ctx context.Context, activity db.Activity, err error, runID, sessionID string, trace *runTrace, turns int) db.Activity {
	if err == nil {
		return activity
	}
	info := RunInfoFrom(ctx)
	metadata := map[string]any{
		"run_id": runID, "session_id": sessionID, "role": trace.role,
		"model_turns": turns, "tool_calls": trace.toolCalls, "run_output_emitted": trace.outputEmitted,
	}
	if info.TaskID != 0 {
		metadata["task_id"] = info.TaskID
	}
	if info.IntentID != 0 {
		metadata["intent_id"] = info.IntentID
	}
	if diagnostic := ErrorDiagnostics(err); diagnostic != nil {
		metadata["llm_error"] = diagnostic
	}
	activity.Metadata, _ = json.Marshal(metadata)
	log.Printf("[llm-failure] %s", activity.Metadata)
	return activity
}

func sessionRole(sessionID string) string {
	switch {
	case strings.Contains(sessionID, "-main"):
		return "mainagent"
	case strings.Contains(sessionID, "-worker"):
		return "worker"
	case strings.Contains(sessionID, "-planner"):
		return "planner"
	default:
		return "chat"
	}
}
