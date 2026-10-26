package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

func TestCaptureCloseFailureReachesCurrentWorkerOutput(t *testing.T) {
	_, _, tools, assigned, _ := graphToolFixture(t)
	calls, closes := 0, 0
	provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		calls++
		if !yield(llm.StreamEvent{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 11, CacheReadTokens: 3}}, nil) {
			return
		}
		if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "verified partial evidence"}, nil) {
			return
		}
		yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "end_turn", Usage: llm.Usage{OutputTokens: 7, CacheWriteTokens: 2}}, nil)
	}}
	session := agentcore.NewSession(agentcore.Options{Provider: provider, SessionID: "fixture-worker-close", MaxTurns: 1})
	defer session.Close()
	closeErr := errors.New("fixture transcript close failure")
	var results []db.Activity
	var runID string
	text, reason, err := captureRunSessionFinalized(WithRunInfo(t.Context(), RunInfo{IntentID: assigned}), session, "fixture", func(a db.Activity) {
		var metadata map[string]any
		if err := json.Unmarshal(a.Metadata, &metadata); err != nil {
			t.Fatal(err)
		}
		if a.Kind == "round" {
			runID, _ = metadata["run_id"].(string)
		}
		if runID == "" || metadata["run_id"] != runID {
			t.Fatal("close failure lost its run attribution")
		}
		a.NodeID = &assigned
		id, appendErr := tools.ts.AppendActivity(a)
		if appendErr != nil {
			t.Fatal(appendErr)
		}
		a.ID = id
		if a.Kind == "result" {
			results = append(results, a)
		}
	}, func() error {
		closes++
		return errors.Join(session.Close(), &harness.StorageError{Err: closeErr})
	})
	var storage *harness.StorageError
	if calls != 1 || closes != 1 || text != "verified partial evidence" || reason != harness.ReasonStorageError || !errors.Is(err, closeErr) || !errors.As(err, &storage) {
		t.Fatalf("close contract: calls=%d closes=%d text=%q reason=%s err=%v", calls, closes, text, reason, err)
	}
	if len(results) != 2 || results[0].IsError || !results[1].IsError {
		t.Fatalf("normal output was replaced or close failure was not appended: %+v", results)
	}
	if results[1].InputTokens != nil || results[1].OutputTokens != nil || results[1].CacheReadTokens != nil || results[1].CacheWriteTokens != nil {
		t.Fatal("the close failure counted the original model usage again")
	}
	usage, usageErr := tools.ts.TokenTotal()
	if usageErr != nil || usage.InputTokens != 11 || usage.OutputTokens != 7 || usage.CacheReadTokens != 3 || usage.CacheWriteTokens != 2 {
		t.Fatalf("close failure changed the stored model usage: %+v %v", usage, usageErr)
	}
	if err := tools.ts.SetNodeState(assigned, "blocked"); err != nil {
		t.Fatal(err)
	}
	output := callReadJSON(t, tools.getWorkerOutput(), fmt.Sprintf(`{"intent_id":%d}`, assigned)).(map[string]any)
	if output["is_error"] != true || output["run_id"] != runID || output["intent_state"] != "blocked" || !strings.Contains(output["final_text"].(string), text) || !strings.Contains(output["final_text"].(string), "저장") {
		t.Fatalf("API/tool lookup hid the current close failure: %+v", output)
	}
	if preserved, err := tools.ts.ActivityDetail(results[0].ID); err != nil || preserved != text {
		t.Fatalf("original evidence changed: %q %v", preserved, err)
	}
	if rendered := renderTriggers(tools.ts, []TriggerEvent{{Kind: "done", IntentID: assigned}}); !strings.Contains(rendered, "저장") || !strings.Contains(rendered, text) {
		t.Fatalf("Planner missed the close failure: %s", rendered)
	}
}

func TestWorkerTranscriptFailureUsesFinalizedCapture(t *testing.T) {
	d, task, tools, assigned, _ := graphToolFixture(t)
	store := transcript.NewStore(t.TempDir())
	path := store.MainPath(WorkerSessionID(tools.ts.ID(), assigned))
	calls := 0
	provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		calls++
		if err := os.Rename(path, path+".original"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "worker partial verification"}, nil) {
			return
		}
		yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "end_turn"}, nil)
	}}
	worker := NewWorker(provider, "fixture", t.TempDir(), store, 0, 1)
	intent, err := tools.ts.GetNode(assigned)
	if err != nil {
		t.Fatal(err)
	}
	var results []db.Activity
	reason, _, err := worker.Execute(t.Context(), "fixture-worker", task.ID, d.Assets(), tools.ts, intent, nil, func(a db.Activity) {
		if a.Kind == "result" {
			results = append(results, a)
		}
		if _, err := tools.ts.AppendActivity(a); err != nil {
			t.Fatal(err)
		}
	}, nil, nil)
	if calls != 1 || reason != harness.ReasonStorageError || err == nil || len(results) != 2 || !results[0].IsError || !results[1].IsError {
		t.Fatalf("Worker transcript failure: calls=%d reason=%s err=%v results=%d", calls, reason, err, len(results))
	}
	var first, last map[string]any
	_ = json.Unmarshal(results[0].Metadata, &first)
	_ = json.Unmarshal(results[1].Metadata, &last)
	if first["run_id"] != last["run_id"] || last["failure_stage"] != "session_close" || last["original_terminal_reason"] != "storage_error" {
		t.Fatalf("Worker finalization lost attribution: %v %v", first, last)
	}
	output, err := tools.ts.LatestWorkerOutput(assigned)
	if err != nil || output == nil || !output.IsError || !strings.Contains(output.Detail, "worker partial verification") {
		t.Fatalf("Worker latest output lost storage failure/evidence: %+v %v", output, err)
	}
}

func TestCaptureCloseFailurePreservesOriginalErrorAndReason(t *testing.T) {
	for _, kind := range []string{"model_error", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var original error = diagnosticFixtureError{}
			provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
				if kind == "cancelled" {
					cancel()
					original = context.Canceled
				}
				yield(llm.StreamEvent{}, original)
			}}
			session := agentcore.NewSession(agentcore.Options{Provider: provider, SessionID: "fixture-worker-original", MaxTurns: 1})
			defer session.Close()
			var final db.Activity
			closeErr := errors.New("fixture close failure")
			_, reason, err := captureRunSessionFinalized(ctx, session, "fixture", func(a db.Activity) {
				if a.Kind == "result" {
					final = a
				}
			}, func() error { return errors.Join(session.Close(), &harness.StorageError{Err: closeErr}) })
			var noReplay interface{ NoReplay() bool }
			if reason != harness.ReasonStorageError || !errors.Is(err, original) || !errors.Is(err, closeErr) || !errors.As(err, &noReplay) || !noReplay.NoReplay() {
				t.Fatalf("original failure or replay protection lost: %s %v", reason, err)
			}
			var metadata map[string]any
			if err := json.Unmarshal(final.Metadata, &metadata); err != nil {
				t.Fatal(err)
			}
			if !final.IsError || metadata["terminal_reason"] != "storage_error" || metadata["original_terminal_reason"] == nil {
				t.Fatalf("missing close failure cause: %+v %s", final, final.Metadata)
			}
			if kind == "model_error" && (metadata["original_terminal_reason"] != "model_error" || metadata["llm_error"] == nil) {
				t.Fatalf("original diagnostics lost: %s", final.Metadata)
			}
			if kind == "cancelled" && metadata["original_terminal_reason"] != "aborted_streaming" {
				t.Fatalf("original cancellation reason lost: %s", final.Metadata)
			}
		})
	}
}
