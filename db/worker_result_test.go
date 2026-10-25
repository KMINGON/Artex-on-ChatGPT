package db

import "testing"

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
