package server

import (
	"context"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

func TestReviewedCompletionDoesNotCancelANewGoal(t *testing.T) {
	m := stateGraphManager(t)
	task, err := m.CreateTask("completion race", "verify completion", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	root, err := task.Store.OriginFactID()
	if err != nil {
		t.Fatal(err)
	}
	goal, err := task.Store.AddGoal(map[string]any{"text": "first"}, "human")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := task.Store.ProveGoal(t.Context(), goal, root, "fixture"); err != nil {
		t.Fatal(err)
	}
	next, err := task.Store.AddGoalWithOrigin(t.Context(), map[string]any{"text": "new goal"}, "human")
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(m)
	run := e.execContextFor(t.Context(), task.ID)
	won, err := m.CompleteReviewedTask(t.Context(), task.ID, func() { e.cancelExec(task.ID, agent.AbortGoalMet) })
	if err != nil || won || run.Err() != nil || task.lifecycleSnapshot().Status == "done" {
		t.Fatalf("new goal lost: won=%v err=%v cancelled=%v", won, err, run.Err())
	}
	if _, _, err := task.Store.ProveGoal(t.Context(), next, root, "fixture finished"); err != nil {
		t.Fatal(err)
	}
	won, err = m.CompleteReviewedTask(t.Context(), task.ID, func() { e.cancelExec(task.ID, agent.AbortGoalMet) })
	if err != nil || !won || run.Err() == nil || task.lifecycleSnapshot().Status != "done" {
		t.Fatalf("completion not applied: won=%v err=%v", won, err)
	}
	if err := m.SetTaskStatus(task.ID, "running"); err != nil {
		t.Fatal(err)
	}
	fresh := e.execContextFor(context.Background(), task.ID)
	if fresh.Err() != nil || fresh == run {
		t.Fatal("resume inherited completed run cancellation")
	}
}

func TestTimedCompletionRechecksNewWork(t *testing.T) {
	m := stateGraphManager(t)
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "reviewed", true: "new goal"}[changed], func(t *testing.T) {
			task, err := m.CreateTask("deadline completion", "verify completion", nil, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer m.DeleteTask(task.ID, DeleteTaskOptions{})
			root, _ := task.Store.OriginFactID()
			goal, err := task.Store.AddGoal(map[string]any{"text": "initial"}, "human")
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := task.Store.ProveGoal(t.Context(), goal, root, "fixture proof"); err != nil {
				t.Fatal(err)
			}
			want := "done"
			if changed {
				if _, err := task.Store.AddGoal(map[string]any{"text": "late goal"}, "human"); err != nil {
					t.Fatal(err)
				}
				want = "timeout"
			}
			status, won, err := NewEngine(m).finishTimedTask(t.Context(), task, true)
			if err != nil || !won || status != want || task.lifecycleSnapshot().Status != want {
				t.Fatalf("deadline completion status=%s won=%v err=%v want=%s", status, won, err, want)
			}
		})
	}
}

func TestGoallessPendingTodoReachesPlanner(t *testing.T) {
	m := stateGraphManager(t)
	task, err := m.CreateTask("pending work", "", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	if _, err := task.Store.SavePlannerTodos(t.Context(), []db.PlannerTodo{{Content: "review remaining evidence", Status: "pending"}}, 0); err != nil {
		t.Fatal(err)
	}
	called := make(chan struct{}, 1)
	provider := &browserObservationProvider{step: func(req llm.CompletionRequest) []llm.StreamEvent {
		select {
		case called <- struct{}{}:
		default:
		}
		return []llm.StreamEvent{{Type: llm.SETextDelta, Text: "fixture review"}, {Type: llm.SEMessageDelta, StopReason: "end_turn"}}
	}}
	planner := agent.NewPlanner(provider, "fixture", m.dir, nil, 0, 1)
	e := NewEngine(m)
	e.debounce = time.Millisecond
	e.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) { return planner, nil })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); e.plannerLoop(ctx, task) }()
	task.Notify()
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Error("pending Todo was silently left behind without a Planner review")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("planner loop did not stop")
	}
	if task.lifecycleSnapshot().Status == "done" {
		t.Fatal("pending work was marked complete")
	}
}
