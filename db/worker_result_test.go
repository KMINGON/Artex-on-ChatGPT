package db

import (
	"encoding/json"
	"testing"
)

func TestLatestWorkerOutputBeyondTraceWindowAndDirectSources(t *testing.T) {
	d, _ := templateFixtureDB(t)
	source, err := d.CreateTaskWithOptions("source", "result fixture", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	store := d.Exploration(source.ExplorationID)
	intentID, err := store.AddIntent(map[string]any{"summary": "long run"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	appendOutput := func(kind, detail string) int64 {
		t.Helper()
		id, err := store.AppendActivity(Activity{NodeID: &intentID, Kind: kind, Detail: detail})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	appendOutput("text", "early text")
	if _, err := d.Exec(`INSERT INTO activity(exploration_id,node_id,kind,detail)
		SELECT $1,$2,'tool','large trace' FROM generate_series(1,1100)`, source.ExplorationID, intentID); err != nil {
		t.Fatal(err)
	}
	lastText := appendOutput("text", "latest text")
	got, err := store.LatestWorkerOutput(intentID)
	if err != nil || got == nil || got.ID != lastText {
		t.Fatalf("latest text: %+v, %v", got, err)
	}
	appendOutput("result", "old result")
	lastResult := appendOutput("result", "complete result with evidence")
	got, err = store.LatestWorkerOutput(intentID)
	if err != nil || got == nil || got.ID != lastResult || got.Detail != "complete result with evidence" {
		t.Fatalf("latest result: %+v, %v", got, err)
	}
	if err := store.SetIntentState(intentID, "done"); err != nil {
		t.Fatal(err)
	}
	child, err := d.CreateTaskWithOptions("child", "direct source", TaskCreateOptions{SourceTaskIDs: []int64{source.ID}})
	if err != nil {
		t.Fatal(err)
	}
	childStore := d.Exploration(child.ExplorationID)
	got, err = childStore.LatestWorkerOutputWithSources(intentID)
	if err != nil || got == nil || got.ID != lastResult || !got.Inherited || got.SourceTaskID != source.ID {
		t.Fatalf("direct source output: %+v, %v", got, err)
	}
	grandchild, err := d.CreateTaskWithOptions("grandchild", "no transitive source", TaskCreateOptions{SourceTaskIDs: []int64{child.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := d.Exploration(grandchild.ExplorationID).LatestWorkerOutputWithSources(intentID); err != nil || got != nil {
		t.Fatalf("transitive output leaked: %+v, %v", got, err)
	}
	if err := store.SetIntentState(intentID, "running"); err != nil {
		t.Fatal(err)
	}
	if got, err := childStore.LatestWorkerOutputWithSources(intentID); err != nil || got != nil {
		t.Fatalf("active source output leaked: %+v, %v", got, err)
	}
}

func TestLatestWorkerOutputDoesNotReusePreviousRunResult(t *testing.T) {
	d, _ := templateFixtureDB(t)
	task, err := d.CreateTaskWithOptions("resume", "result fixture", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	store := d.Exploration(task.ExplorationID)
	intentID, err := store.AddIntent(map[string]any{"summary": "continue unfinished verification"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	appendActivity := func(kind, detail, metadata string) int64 {
		t.Helper()
		id, err := store.AppendActivity(Activity{NodeID: &intentID, Kind: kind, Detail: detail, Metadata: json.RawMessage(metadata)})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	old := appendActivity("result", "previous final result", `{}`)
	appendActivity("text", "legacy status after result", `{}`)
	if got, err := store.LatestWorkerOutput(intentID); err != nil || got == nil || got.ID != old {
		t.Fatalf("legacy result preference changed: %+v %v", got, err)
	}
	// The boundary itself can fail to persist. Its explicit blocked reason must
	// also prevent a previous result from being presented as the new execution.
	if err := store.SetIntentBlockedReason(intentID, IntentBlockedActivityStorage); err != nil {
		t.Fatal(err)
	}
	if got, err := store.LatestWorkerOutput(intentID); err != nil || got != nil {
		t.Fatalf("missing boundary after storage failure reused old result: %+v %v", got, err)
	}
	if err := store.SetIntentState(intentID, "open"); err != nil {
		t.Fatal(err)
	}
	if node, err := store.GetNode(intentID); err != nil || node.BlockedReason != "" {
		t.Fatalf("resume did not clear the storage failure reason: %+v %v", node, err)
	}
	for _, boundary := range []string{`{"worker_preparation_start":true}`, `{"run_start":true,"run_id":"resumed"}`} {
		appendActivity("round", "next run", boundary)
		if got, err := store.LatestWorkerOutput(intentID); err != nil || got != nil {
			t.Fatalf("previous result leaked before new output: %+v %v", got, err)
		}
	}
	progress := appendActivity("text", "new run has an unfinished question", `{"run_id":"resumed"}`)
	appendActivity("result", "late result from the canceled run", `{"run_id":"canceled"}`)
	if got, err := store.LatestWorkerOutput(intentID); err != nil || got == nil || got.ID != progress || got.Kind != "text" {
		t.Fatalf("current partial output was displaced: %+v %v", got, err)
	} else {
		var metadata map[string]any
		_ = json.Unmarshal(got.Metadata, &metadata)
		if metadata["run_start_activity_id"] == nil || metadata["run_id"] != "resumed" {
			t.Fatalf("current output lost its boundary: %s", got.Metadata)
		}
	}
	finished := appendActivity("result", "new run result", `{"run_id":"resumed"}`)
	appendActivity("text", "status after current result", `{"run_id":"resumed"}`)
	if got, err := store.LatestWorkerOutput(intentID); err != nil || got == nil || got.ID != finished {
		t.Fatalf("current result was displaced by status text: %+v %v", got, err)
	}
	appendActivity("round", "failed preparation after another resume", `{"worker_preparation_start":true}`)
	appendActivity("result", "late output during new preparation", `{"run_id":"resumed"}`)
	if err := store.SetIntentState(intentID, "blocked"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.LatestWorkerOutput(intentID); err != nil || got != nil {
		t.Fatalf("preparation failure reused old completed output: %+v %v", got, err)
	}
}
