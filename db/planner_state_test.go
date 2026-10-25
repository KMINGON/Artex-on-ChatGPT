package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestPlannerTodosPersistWithoutLosingRootOrAcceptingStaleWrite(t *testing.T) {
	d, dsn := templateFixtureDB(t)
	task, err := d.CreateTaskWithOptions("original description", "original goal", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	store := d.Exploration(task.ExplorationID)
	state, err := store.LoadPlannerTodos(t.Context())
	if err != nil || state.Revision != 0 || len(state.Items) != 0 {
		t.Fatalf("initial state = %+v, %v", state, err)
	}
	items := []PlannerTodo{{Content: "Verify evidence before next stage", Status: "pending"}}
	revision, err := store.SavePlannerTodos(t.Context(), items, state.Revision)
	if err != nil || revision != 1 {
		t.Fatalf("first save: %d, %v", revision, err)
	}
	if _, err := store.SavePlannerTodos(t.Context(), nil, 0); !errors.Is(err, ErrPlannerStateChanged) {
		t.Fatalf("stale write accepted: %v", err)
	}
	if _, err := store.SavePlannerTodos(t.Context(), []PlannerTodo{{Content: "invalid", Status: "done"}}, revision); err == nil {
		t.Fatal("invalid state accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.SavePlannerTodos(ctx, nil, revision); err == nil {
		t.Fatal("canceled write accepted")
	}
	reopened, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	state, err = reopened.Exploration(task.ExplorationID).LoadPlannerTodos(t.Context())
	if err != nil || state.Revision != revision || len(state.Items) != 1 || state.Items[0] != items[0] {
		t.Fatalf("reopened plan = %+v, %v", state, err)
	}
	id, err := store.OriginFactID()
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.GetNode(id)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(node.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["description"] != "original description" || payload["goal"] != "original goal" {
		t.Fatalf("Todo write replaced task origin: %s", node.Payload)
	}
	if _, err := store.SavePlannerTodos(t.Context(), nil, state.Revision); err != nil {
		t.Fatal(err)
	}
	state, err = store.LoadPlannerTodos(t.Context())
	if err != nil || len(state.Items) != 0 || state.Revision != 2 {
		t.Fatalf("explicit plan clear = %+v, %v", state, err)
	}
}

func TestPlannerTodosRejectMalformedStoredState(t *testing.T) {
	for _, raw := range []string{
		`null`,
		`{"planner_todos":"not an array"}`,
		`{"planner_todos":[{"content":"keep","status":"unknown"}]}`,
		`{"planner_todos_revision":-1}`,
	} {
		if _, err := decodePlannerTodoState([]byte(raw)); err == nil {
			t.Fatalf("malformed state treated as empty plan: %s", raw)
		}
	}
}

func TestPlannerTodosSurviveArchiveRestore(t *testing.T) {
	d, _ := templateFixtureDB(t)
	if err := d.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	task, err := d.CreateTaskWithOptions("archived plan", "preserve pending work", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	items := []PlannerTodo{{Content: "Continue evidence verification", Status: "in_progress"}}
	if _, err := d.Exploration(task.ExplorationID).SavePlannerTodos(t.Context(), items, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE tasks SET paused=true WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	archive, err := d.QueueTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exploration(task.ExplorationID).SavePlannerTodos(t.Context(), nil, 1); err == nil {
		t.Fatal("queued archive accepted a late plan overwrite")
	}
	if _, err := d.ClaimTaskArchiveJob(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := d.SnapshotTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CompleteTaskArchive(archive.ID, snapshot, "fixture.tar.zst", "fixture", 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := d.QueueTaskArchiveRestore(archive.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClaimTaskArchiveJob(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RestoreTaskArchive(archive.ID, snapshot, 0); err != nil {
		t.Fatal(err)
	}
	restored, err := d.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := d.Exploration(restored.ExplorationID).LoadPlannerTodos(t.Context())
	if err != nil || state.Revision != 1 || len(state.Items) != 1 || state.Items[0] != items[0] {
		t.Fatalf("restored Todo: %+v, %v", state, err)
	}
}
