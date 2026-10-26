package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
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
	won, err := m.CompleteTaskIfGoalsSettled(t.Context(), task.ID, false, func() { e.cancelExec(task.ID, agent.AbortGoalMet) })
	if err != nil || won || run.Err() != nil || task.lifecycleSnapshot().Status == "done" {
		t.Fatalf("new goal lost: won=%v err=%v cancelled=%v", won, err, run.Err())
	}
	if _, _, err := task.Store.ProveGoal(t.Context(), next, root, "fixture finished"); err != nil {
		t.Fatal(err)
	}
	won, err = m.CompleteTaskIfGoalsSettled(t.Context(), task.ID, false, func() { e.cancelExec(task.ID, agent.AbortGoalMet) })
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

func TestOriginalPlannerCompletionPublishesGraphWithoutExtraReview(t *testing.T) {
	m := stateGraphManager(t)
	task, err := m.CreateTask("test input preserved", "verify observed result", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	goal, err := task.Store.AddGoal(map[string]any{"text": "observed result"}, "goals")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := task.Store.AddIntentWithParents(t.Context(), map[string]any{"summary": "independent validation"}, 5, nil, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	fact, err := task.Store.AddFactWithIntent(t.Context(), map[string]any{"summary": "observed response", "detail": "fixture evidence"}, nil, "worker", intent)
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Store.SetIntentState(intent, "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := task.Store.SavePlannerTodos(t.Context(), []db.PlannerTodo{{Content: "unrelated saved note", Status: "pending"}}, 0); err != nil {
		t.Fatal(err)
	}
	oldAugment, oldResolve := agent.ToolAugment, agent.ToolResolve
	agent.ToolAugment, agent.ToolResolve = nil, nil
	t.Cleanup(func() { agent.ToolAugment, agent.ToolResolve = oldAugment, oldResolve })
	calls := 0
	provider := &browserObservationProvider{step: func(llm.CompletionRequest) []llm.StreamEvent {
		calls++
		if calls == 1 {
			return []llm.StreamEvent{{Type: llm.SEToolUseStart, ToolID: "proof", ToolName: "prove_goal"}, {Type: llm.SEToolInputJSON, Text: fmt.Sprintf(`{"goal_id":%d,"evidence_id":%d,"reason":"verified evidence"}`, goal, fact)}, {Type: llm.SEMessageDelta, StopReason: "tool_use"}}
		}
		return []llm.StreamEvent{{Type: llm.SETextDelta, Text: "목표를 확인했습니다."}, {Type: llm.SEMessageDelta, StopReason: "end_turn"}}
	}}
	planner := agent.NewPlanner(provider, "fixture", m.dir, nil, 0, 4)
	e := NewEngine(m)
	e.debounce = time.Millisecond
	e.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) { return planner, nil })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	run := e.execContextFor(ctx, task.ID)
	loopDone := make(chan struct{})
	go func() { defer close(loopDone); e.plannerLoop(ctx, task) }()
	defer func() {
		cancel()
		select {
		case <-loopDone:
		case <-time.After(5 * time.Second):
			t.Error("planner did not stop")
		}
	}()
	task.Notify()
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for task.lifecycleSnapshot().Status != "done" || run.Err() == nil {
		select {
		case <-deadline:
			t.Fatalf("completion blocked: status=%s", task.lifecycleSnapshot().Status)
		case <-tick.C:
		}
	}
	// A result already in flight may arrive after completion; it remains readable.
	late, err := task.Store.AddFactWithIntent(context.Background(), map[string]any{"summary": "late saved evidence"}, nil, "worker", intent)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{m: m, engine: e}
	recorder := httptest.NewRecorder()
	s.listTasks(recorder, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
	var listing struct {
		Tasks []TaskDTO `json:"tasks"`
	}
	if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &listing) != nil {
		t.Fatalf("task API failed: %s", recorder.Body.String())
	}
	found := false
	for _, row := range listing.Tasks {
		if row.ID == task.ID {
			found = true
			if row.Status != "done" || row.GoalsMet != 1 {
				t.Fatalf("wrong completion DTO: %+v", row)
			}
		}
	}
	if !found {
		t.Fatal("completed task absent from API")
	}
	graph := httptest.NewRecorder()
	s.explorationGraph(graph, httptest.NewRequest(http.MethodGet, "/api/exploration/graph?task="+task.ID, nil))
	var body struct {
		Nodes []TaskNodeDTO `json:"nodes"`
	}
	if graph.Code != http.StatusOK || json.Unmarshal(graph.Body.Bytes(), &body) != nil {
		t.Fatal("graph API failed")
	}
	states := map[string]string{}
	for _, node := range body.Nodes {
		states[node.ID] = node.State
	}
	if states[strconv.FormatInt(goal, 10)] != "met" || states[strconv.FormatInt(late, 10)] != "confirmed" {
		t.Fatalf("graph lost goal/late result: %+v", states)
	}
	description, goalText, err := task.Store.Root()
	if err != nil || description != "test input preserved" || goalText != "verify observed result" {
		t.Fatal("task input rewritten")
	}
}

func TestTimedCompletionPreservesAbsoluteDeadlineWhilePausedOrQueued(t *testing.T) {
	m := stateGraphManager(t)
	for _, state := range []string{"paused", "queued"} {
		for _, newGoal := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/new_goal=%v", state, newGoal), func(t *testing.T) {
				task, err := m.CreateTask("absolute deadline", "verify completion", nil, 1, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer m.DeleteTask(task.ID, DeleteTaskOptions{})
				root, _ := task.Store.OriginFactID()
				goal, err := task.Store.AddGoal(map[string]any{"text": "observed result"}, "human")
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := task.Store.ProveGoal(t.Context(), goal, root, "verified evidence"); err != nil {
					t.Fatal(err)
				}
				if state == "paused" {
					if _, err := m.pg.Exec(`UPDATE tasks SET paused=true WHERE id=$1`, task.ID); err != nil {
						t.Fatal(err)
					}
					task.updateLifecycle(func(s *taskLifecycleState) { s.Paused = true })
				} else {
					if _, err := m.pg.Exec(`UPDATE tasks SET queued=true WHERE id=$1`, task.ID); err != nil {
						t.Fatal(err)
					}
					task.updateLifecycle(func(s *taskLifecycleState) { s.Queued = true })
				}
				if won, err := m.CompleteTaskIfGoalsSettled(t.Context(), task.ID, false, nil); err != nil || won {
					t.Fatalf("normal completion overwrote %s: %v %v", state, won, err)
				}
				expected := "done"
				if newGoal {
					if _, err := task.Store.AddGoal(map[string]any{"text": "new objective"}, "human"); err != nil {
						t.Fatal(err)
					}
					expected = "timeout"
				}
				got, won, err := NewEngine(m).finishTimedTask(t.Context(), task, true)
				if err != nil || !won || got != expected || task.lifecycleSnapshot().Status != expected {
					t.Fatalf("absolute deadline: state=%s met=true got=%s want=%s won=%v err=%v", state, got, expected, won, err)
				}
			})
		}
	}
}

func TestTimedFinalizationDoesNotOverwriteDeletionArchiveOrTerminalState(t *testing.T) {
	m := stateGraphManager(t)
	for _, state := range []string{"deleted", "archiving", "terminal"} {
		for _, met := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/met=%v", state, met), func(t *testing.T) {
				task, err := m.CreateTask("deadline race", "verify result", nil, 1, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					_, _ = m.pg.Exec(`DELETE FROM task_archives WHERE task_id=$1`, task.ID)
					_, _ = m.pg.Exec(`UPDATE tasks SET deleted_at=NULL WHERE id=$1`, task.ID)
					_, _ = m.DeleteTask(task.ID, DeleteTaskOptions{})
				}()
				root, _ := task.Store.OriginFactID()
				goal, err := task.Store.AddGoal(map[string]any{"text": "observed result"}, "human")
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := task.Store.ProveGoal(t.Context(), goal, root, "verified evidence"); err != nil {
					t.Fatal(err)
				}
				switch state {
				case "deleted":
					_, err = m.pg.Exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, task.ID)
				case "archiving":
					_, err = m.pg.Exec(`INSERT INTO task_archives(task_id,state) VALUES($1,'archiving')`, task.ID)
				case "terminal":
					err = m.SetTaskStatus(task.ID, "failed")
				}
				if err != nil {
					t.Fatal(err)
				}
				e := NewEngine(m)
				run := e.execContextFor(t.Context(), task.ID)
				before := m.TaskStatus(task.ID)
				_, won, err := e.finishTimedTask(t.Context(), task, met)
				if err != nil || won || m.TaskStatus(task.ID) != before || run.Err() != nil {
					t.Fatalf("deadline overwrote %s: won=%v err=%v status=%s", state, won, err, m.TaskStatus(task.ID))
				}
			})
		}
	}
}
