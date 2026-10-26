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

func TestCaptureWorkerRunsHaveSeparateBoundariesAndAttribution(t *testing.T) {
	_, _, tools, assigned, _ := graphToolFixture(t)
	if _, err := tools.ts.AppendActivity(db.Activity{NodeID: &assigned, Kind: "result", Detail: "old completion"}); err != nil {
		t.Fatal(err)
	}
	var runIDs []string
	for run := 1; run <= 2; run++ {
		provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
			// The new run has started, but the model has not produced a result yet.
			if old, err := tools.ts.LatestWorkerOutput(assigned); err != nil || old != nil {
				t.Errorf("previous completion leaked into run %d: %+v %v", run, old, err)
			}
			if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: fmt.Sprintf("run %d verification", run)}, nil) {
				return
			}
			yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "end_turn"}, nil)
		}}
		var boundary string
		text, reason, err := captureRun(WithRunInfo(t.Context(), RunInfo{IntentID: assigned}), agentcore.Options{Provider: provider, SessionID: "fixture-worker", MaxTurns: 1}, "fixture", func(a db.Activity) {
			var metadata map[string]any
			if err := json.Unmarshal(a.Metadata, &metadata); err != nil {
				t.Fatal(err)
			}
			id, _ := metadata["run_id"].(string)
			if id == "" || metadata["session_id"] != "fixture-worker" {
				t.Fatalf("activity lost run identity: %s", a.Metadata)
			}
			if a.Kind == "round" && metadata["run_start"] == true {
				boundary = id
			}
			if boundary == "" || id != boundary {
				t.Fatal("output arrived before its matching run boundary")
			}
			a.NodeID = &assigned
			if _, err := tools.ts.AppendActivity(a); err != nil {
				t.Fatal(err)
			}
		})
		if err != nil || reason != harness.ReasonCompleted || text != fmt.Sprintf("run %d verification", run) {
			t.Fatalf("capture failed: %q %s %v", text, reason, err)
		}
		runIDs = append(runIDs, boundary)
		output := callReadJSON(t, tools.getWorkerOutput(), fmt.Sprintf(`{"intent_id":%d}`, assigned)).(map[string]any)
		if output["final_text"] != text || output["output_kind"] != "result" || output["run_id"] != boundary || output["run_boundary_known"] != true || output["activity_id"] == nil || output["intent_state"] == nil {
			t.Fatalf("output metadata misrepresented the current run: %+v", output)
		}
	}
	if runIDs[0] == runIDs[1] {
		t.Fatal("resumed executions share a run identity")
	}
	if _, err := tools.ts.AppendActivity(db.Activity{NodeID: &assigned, Kind: "round", Metadata: json.RawMessage(`{"worker_preparation_start":true}`)}); err != nil {
		t.Fatal(err)
	}
	empty := callReadJSON(t, tools.getWorkerOutput(), fmt.Sprintf(`{"intent_id":%d}`, assigned)).(map[string]any)
	if empty["has_output"] != false || empty["output_kind"] != "none" || empty["intent_state"] == nil {
		t.Fatalf("new preparation looks like old completion: %+v", empty)
	}
}

func TestCaptureStorageFailureRetainsPartialOutputAndCloseError(t *testing.T) {
	store := transcript.NewStore(t.TempDir())
	path := store.MainPath("fixture-storage")
	provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		if err := os.Rename(path, path+".original"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "partial verification evidence"}, nil) {
			return
		}
		yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "end_turn"}, nil)
	}}
	var result db.Activity
	text, reason, err := captureRun(t.Context(), agentcore.Options{Provider: provider, Transcript: store, SessionID: "fixture-storage", MaxTurns: 1}, "fixture", func(a db.Activity) {
		if a.Kind == "result" {
			result = a
		}
	})
	var storage *harness.StorageError
	if reason != harness.ReasonStorageError || !errors.As(err, &storage) || text != "partial verification evidence" || !result.IsError || !strings.Contains(result.Detail, "partial verification evidence") {
		t.Fatalf("storage failure lost evidence or appeared complete: %q %s %v %+v", text, reason, err, result)
	}
}

func TestGetWorkerOutputExplainsMissingBoundaryStorageFailure(t *testing.T) {
	_, _, tools, assigned, _ := graphToolFixture(t)
	oldID, err := tools.ts.AppendActivity(db.Activity{NodeID: &assigned, Kind: "result", Detail: "previous verification evidence"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tools.ts.SetIntentBlockedReason(assigned, db.IntentBlockedActivityStorage); err != nil {
		t.Fatal(err)
	}
	output := callReadJSON(t, tools.getWorkerOutput(), fmt.Sprintf(`{"intent_id":%d}`, assigned)).(map[string]any)
	if output["has_output"] != false || output["blocked_reason"] != db.IntentBlockedActivityStorage || !strings.Contains(output["final_text"].(string), "저장하지 못했습니다") {
		t.Fatalf("missing activity was presented as old completion: %+v", output)
	}
	node := callReadJSON(t, tools.nodeDetail(), fmt.Sprintf(`{"id":%d}`, assigned)).(map[string]any)
	if node["blocked_reason"] != db.IntentBlockedActivityStorage {
		t.Fatalf("node detail lost the persisted failure cause: %+v", node)
	}
	if detail, err := tools.ts.ActivityDetail(oldID); err != nil || detail != "previous verification evidence" {
		t.Fatalf("previous evidence was removed instead of distinguished: %q %v", detail, err)
	}
}
