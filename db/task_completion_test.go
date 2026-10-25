package db

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func reviewedCompletionFixture(t *testing.T, d *DB) (*Task, *ExplorationStore, int64) {
	t.Helper()
	task, s := graphMutationTask(t, d)
	proof, _ := s.OriginFactID()
	goal, err := s.AddGoal(map[string]any{"text": "bounded goal"}, "human")
	if err != nil {
		t.Fatal(err)
	}
	if all, _, err := s.ProveGoal(context.Background(), goal, proof, "goal verified"); err != nil || !all {
		t.Fatalf("proof %v %v", all, err)
	}
	node, _ := s.GetNode(goal)
	if !strings.Contains(string(node.Payload), "completion_review") {
		t.Fatal("empty-work completion review was not stored")
	}
	return task, s, goal
}

func TestReviewedTaskCompletionRejectsWorkAddedAfterProof(t *testing.T) {
	d, _ := templateFixtureDB(t)
	ctx := context.Background()
	for _, kind := range []string{"goal", "intent", "todo"} {
		t.Run(kind, func(t *testing.T) {
			task, s, goal := reviewedCompletionFixture(t, d)
			switch kind {
			case "goal":
				if _, err := s.AddGoalWithOrigin(ctx, map[string]any{"text": "new goal"}, "human"); err != nil {
					t.Fatal(err)
				}
			case "intent":
				if _, err := s.AddIntentWithParents(ctx, map[string]any{"summary": "new direction"}, 5, nil, nil, "planner"); err != nil {
					t.Fatal(err)
				}
			case "todo":
				if _, err := s.SavePlannerTodos(ctx, []PlannerTodo{{Content: "new plan", Status: "pending"}}, 0); err != nil {
					t.Fatal(err)
				}
			}
			if won, err := d.CompleteReviewedTask(ctx, task.ID); err != nil || won {
				t.Fatalf("stale review completed %v %v", won, err)
			}
			row, err := d.GetTask(task.ID)
			if err != nil || row.Status == "done" || row.CompletedAt != nil {
				t.Fatalf("changed task lifecycle: %+v %v", row, err)
			}
			node, err := s.GetNode(goal)
			if err != nil {
				t.Fatal(err)
			}
			if kind != "goal" && node.State != "open" {
				t.Fatalf("new work did not reopen reviewed goal: %+v", node)
			}
		})
	}
}

func TestReviewedTaskCompletionAcceptsCoveredWorkAndDetectsTodoReuse(t *testing.T) {
	d, _ := templateFixtureDB(t)
	ctx := context.Background()
	for _, change := range []string{"none", "todo_revision", "todo_content_without_revision", "todo_completed"} {
		t.Run(change, func(t *testing.T) {
			task, s := graphMutationTask(t, d)
			_, err := s.AddIntentWithParents(ctx, map[string]any{"summary": "approved independent worker"}, 5, nil, nil, "planner")
			if err != nil {
				t.Fatal(err)
			}
			followUpFact(t, s, "approved independent lead")
			revision, err := s.SavePlannerTodos(ctx, []PlannerTodo{{Content: "approved independent todo", Status: "pending"}}, 0)
			if err != nil {
				t.Fatal(err)
			}
			goal, err := s.AddGoal(map[string]any{"text": "explicit bounded goal"}, "human")
			if err != nil {
				t.Fatal(err)
			}
			proof, _ := s.OriginFactID()
			if _, _, err := s.ProveGoal(ctx, goal, proof, "goal done", "known remaining directions are outside this bounded goal"); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "todo_revision":
				if _, err := s.SavePlannerTodos(ctx, []PlannerTodo{{Content: "new pending work at reused index", Status: "pending"}}, revision); err != nil {
					t.Fatal(err)
				}
			case "todo_content_without_revision":
				if _, err := d.Exec(`UPDATE exploration_nodes SET payload=jsonb_set(payload,'{planner_todos,0,content}','"changed without revision"') WHERE id=$1`, proof); err != nil {
					t.Fatal(err)
				}
			case "todo_completed":
				if _, err := s.SavePlannerTodos(ctx, []PlannerTodo{{Content: "approved independent todo", Status: "completed"}}, revision); err != nil {
					t.Fatal(err)
				}
			}
			want := change == "none" || change == "todo_completed"
			won, err := d.CompleteReviewedTask(ctx, task.ID)
			if err != nil || won != want {
				t.Fatalf("completion won=%v want=%v err=%v", won, want, err)
			}
			row, err := d.GetTask(task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if want && (row.Status != "done" || row.CompletedAt == nil) {
				t.Fatalf("missing terminal transition: %+v", row)
			}
			if !want {
				node, _ := s.GetNode(goal)
				if node.State != "open" || row.Status == "done" {
					t.Fatalf("stale todo review was retained: %+v %+v", row, node)
				}
			}
		})
	}
}

func TestReviewedTaskCompletionLifecycleLegacyAndCancel(t *testing.T) {
	d, _ := templateFixtureDB(t)
	ctx := context.Background()
	for _, lifecycle := range []string{"done", "failed", "timeout", "paused", "queued", "archiving"} {
		t.Run(lifecycle, func(t *testing.T) {
			task, _, _ := reviewedCompletionFixture(t, d)
			switch lifecycle {
			case "paused":
				if err := d.SetPaused(task.ID, true); err != nil {
					t.Fatal(err)
				}
			case "queued":
				if _, err := d.Exec(`UPDATE tasks SET queued=true WHERE id=$1`, task.ID); err != nil {
					t.Fatal(err)
				}
			case "archiving":
				if err := d.SetPaused(task.ID, true); err != nil {
					t.Fatal(err)
				}
				if _, err := d.QueueTaskArchive(task.ID); err != nil {
					t.Fatal(err)
				}
				if err := d.SetPaused(task.ID, false); err != nil {
					t.Fatal(err)
				}
			default:
				if _, err := d.Exec(`UPDATE tasks SET status=$2 WHERE id=$1`, task.ID, lifecycle); err != nil {
					t.Fatal(err)
				}
			}
			if won, err := d.CompleteReviewedTask(ctx, task.ID); err != nil || won {
				t.Fatalf("changed lifecycle %s: %v %v", lifecycle, won, err)
			}
		})
	}
	task, _ := graphMutationTask(t, d)
	if won, err := d.CompleteReviewedTask(ctx, task.ID); err != nil || !won {
		t.Fatalf("goalless empty frontier compatibility %v %v", won, err)
	}
	legacy, legacyStore := graphMutationTask(t, d)
	goal, err := legacyStore.AddGoal(map[string]any{"text": "legacy met"}, "human")
	if err != nil {
		t.Fatal(err)
	}
	if err := legacyStore.SetNodeState(goal, "met"); err != nil {
		t.Fatal(err)
	}
	if won, err := d.CompleteReviewedTask(ctx, legacy.ID); err != nil || !won {
		t.Fatalf("legacy all-met compatibility %v %v", won, err)
	}
	cancelled, _, _ := reviewedCompletionFixture(t, d)
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if won, err := d.CompleteReviewedTask(cancelCtx, cancelled.ID); err == nil || won {
		t.Fatalf("cancelled completion %v %v", won, err)
	}
	row, _ := d.GetTask(cancelled.ID)
	if row.Status == "done" {
		t.Fatal("cancelled request completed task")
	}
}

func TestReviewedTaskCompletionUsesLatestReview(t *testing.T) {
	d, _ := templateFixtureDB(t)
	ctx := context.Background()
	task, s, first := reviewedCompletionFixture(t, d)
	followUpFact(t, s, "new late lead")
	proof, _ := s.OriginFactID()
	second, err := s.AddGoal(map[string]any{"text": "second goal"}, "human")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ProveGoal(ctx, second, proof, "second verified", "late lead is independent"); err != nil {
		t.Fatal(err)
	}
	if won, err := d.CompleteReviewedTask(ctx, task.ID); err != nil || !won {
		t.Fatalf("used older completion snapshot: %v %v", won, err)
	}
	node, _ := s.GetNode(first)
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(node.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload["completion_review"]) == 0 {
		t.Fatal("older review discarded")
	}
}

func TestReviewedTaskCompletionHasSingleConcurrentWinner(t *testing.T) {
	d, _ := templateFixtureDB(t)
	task, _, _ := reviewedCompletionFixture(t, d)
	type outcome struct {
		won bool
		err error
	}
	outcomes := make(chan outcome, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			won, err := d.CompleteReviewedTask(context.Background(), task.ID)
			outcomes <- outcome{won, err}
		}()
	}
	wg.Wait()
	close(outcomes)
	winners := 0
	for result := range outcomes {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.won {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("completion had %d winners", winners)
	}
}

func TestLegacyFollowUpDoesNotBlockReviewedCompletion(t *testing.T) {
	d, _ := templateFixtureDB(t)
	task, s, goal := reviewedCompletionFixture(t, d)
	fact := followUpFact(t, s, "legacy pending note")
	if _, _, err := s.ProveGoal(t.Context(), goal, fact, "legacy note reviewed"); err != nil {
		t.Fatal(err)
	}
	if won, err := d.CompleteReviewedTask(t.Context(), task.ID); err != nil || !won {
		t.Fatalf("legacy note blocked reviewed completion: won=%v err=%v", won, err)
	}
}

func TestReviewedCompletionReopensForLateAnalysisResultButNotHTTPObservation(t *testing.T) {
	d, _ := templateFixtureDB(t)
	for _, tc := range []struct {
		name, source string
		reopen       bool
	}{
		{"analysis_fact", "", true},
		{"http_observation", "http_response", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task, s, goal := reviewedCompletionFixture(t, d)
			payload := map[string]any{"summary": "late result"}
			if tc.source != "" {
				payload["observation_source"] = tc.source
			}
			if _, err := s.AddFactWithIntent(t.Context(), payload, nil, "worker", 0); err != nil {
				t.Fatal(err)
			}
			won, err := d.CompleteReviewedTask(t.Context(), task.ID)
			if err != nil || won == tc.reopen {
				t.Fatalf("completion=%v reopen=%v err=%v", won, tc.reopen, err)
			}
			node, err := s.GetNode(goal)
			if err != nil {
				t.Fatal(err)
			}
			if tc.reopen && node.State != "open" {
				t.Fatal("late analytical result did not reopen goal")
			}
		})
	}
}
