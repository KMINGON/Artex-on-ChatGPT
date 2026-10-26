package db

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

func completionFixture(t *testing.T, d *DB) (*Task, *ExplorationStore, int64) {
	t.Helper()
	task, s := graphMutationTask(t, d)
	root, _ := s.OriginFactID()
	goal, err := s.AddGoal(map[string]any{"text": "bounded goal"}, "human")
	if err != nil {
		t.Fatal(err)
	}
	if all, _, err := s.ProveGoal(t.Context(), goal, root, "verified evidence"); err != nil || !all {
		t.Fatalf("prove goal: %v %v", all, err)
	}
	return task, s, goal
}

func TestGoalProofDoesNotRequireCompletionReview(t *testing.T) {
	d, _ := templateFixtureDB(t)
	task, s := graphMutationTask(t, d)
	root, _ := s.OriginFactID()
	goal, err := s.AddGoal(map[string]any{"text": "bounded goal"}, "human")
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"open", "running", "paused"} {
		intent, err := s.AddIntent(map[string]any{"summary": state}, 5, nil, "planner")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetIntentState(intent, state); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SavePlannerTodos(t.Context(), []PlannerTodo{{Content: "remaining note", Status: "pending"}}, 0); err != nil {
		t.Fatal(err)
	}
	if all, _, err := s.ProveGoal(t.Context(), goal, root, "verified evidence"); err != nil || !all {
		t.Fatalf("new completion gate: all=%v err=%v", all, err)
	}
	node, err := s.GetNode(goal)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(node.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload["proofs"]) == 0 || len(payload["completion_review"]) > 0 {
		t.Fatalf("unexpected proof payload: %s", node.Payload)
	}
	if _, err := s.AddFactWithIntent(t.Context(), map[string]any{"summary": "later observed fact"}, nil, "worker", 0); err != nil {
		t.Fatal(err)
	}
	if won, err := d.CompleteTaskIfGoalsSettled(t.Context(), task.ID, false); err != nil || !won {
		t.Fatalf("goal completion changed: %v %v", won, err)
	}
	saved, _ := s.GetNode(goal)
	if saved.State != "met" {
		t.Fatalf("extra re-review reopened goal: %s", saved.State)
	}
}

func TestTaskCompletionPreservesConcurrentGoalAndLifecycleChanges(t *testing.T) {
	d, _ := templateFixtureDB(t)
	for _, state := range []string{"new goal", "paused", "queued", "deleted", "terminal", "archived"} {
		t.Run(state, func(t *testing.T) {
			task, s, _ := completionFixture(t, d)
			var err error
			switch state {
			case "new goal":
				_, err = s.AddGoal(map[string]any{"text": "new objective"}, "human")
			case "paused":
				_, err = d.Exec(`UPDATE tasks SET paused=true WHERE id=$1`, task.ID)
			case "queued":
				_, err = d.Exec(`UPDATE tasks SET queued=true WHERE id=$1`, task.ID)
			case "deleted":
				_, err = d.Exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, task.ID)
			case "terminal":
				_, err = d.Exec(`UPDATE tasks SET status='failed' WHERE id=$1`, task.ID)
			case "archived":
				_, err = d.Exec(`INSERT INTO task_archives(task_id,state,archive_path) VALUES($1,'archiving','fixture')`, task.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if won, err := d.CompleteTaskIfGoalsSettled(t.Context(), task.ID, false); err != nil || won {
				t.Fatalf("concurrent change overwritten: won=%v err=%v", won, err)
			}
		})
	}
}

func TestGoallessCompletionWaitsForIntentButDoesNotInvokeTodoReview(t *testing.T) {
	d, _ := templateFixtureDB(t)
	for _, state := range []string{"open", "running", "paused"} {
		t.Run(state, func(t *testing.T) {
			task, s := graphMutationTask(t, d)
			intent, err := s.AddIntent(map[string]any{"summary": "manual work"}, 5, nil, "human")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.SetIntentState(intent, state); err != nil {
				t.Fatal(err)
			}
			if won, err := d.CompleteTaskIfGoalsSettled(t.Context(), task.ID, true); err != nil || won {
				t.Fatalf("unfinished manual intent completed: %v %v", won, err)
			}
			if err := s.SetIntentState(intent, "done"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.SavePlannerTodos(t.Context(), []PlannerTodo{{Content: "legacy note", Status: "pending"}}, 0); err != nil {
				t.Fatal(err)
			}
			if won, err := d.CompleteTaskIfGoalsSettled(t.Context(), task.ID, true); err != nil || !won {
				t.Fatalf("drained manual work blocked by extra review: %v %v", won, err)
			}
		})
	}
}

func TestTaskCompletionHasSingleConcurrentWinnerAndHonorsCancellation(t *testing.T) {
	d, _ := templateFixtureDB(t)
	task, _, _ := completionFixture(t, d)
	winners := make(chan bool, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			won, err := d.CompleteTaskIfGoalsSettled(t.Context(), task.ID, false)
			if err != nil {
				t.Error(err)
			}
			winners <- won
		}()
	}
	wg.Wait()
	close(winners)
	count := 0
	for won := range winners {
		if won {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("winners=%d", count)
	}
	cancelled, _, _ := completionFixture(t, d)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if won, err := d.CompleteTaskIfGoalsSettled(ctx, cancelled.ID, false); err == nil || won {
		t.Fatalf("cancelled completion committed: %v %v", won, err)
	}
}
