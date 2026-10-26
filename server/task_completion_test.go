package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
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

func TestPlannerCompletionWaitsForActiveWorkerAndPublishesFinalState(t *testing.T) {
	m := stateGraphManager(t)
	task, err := m.CreateTask("completion keeps validation", "finish remaining validation", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	goal, err := task.Store.AddGoal(map[string]any{"text": "validated objective"}, "human")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := task.Store.AddIntent(map[string]any{"summary": "negative control remains"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Store.SetIntentState(intent, "running"); err != nil {
		t.Fatal(err)
	}
	root, _ := task.Store.OriginFactID()
	var evidence, calls atomic.Int64
	evidence.Store(root)
	waiting := make(chan struct{}, 1)
	provider := &browserObservationProvider{step: func(req llm.CompletionRequest) []llm.StreamEvent {
		call := calls.Add(1)
		if call == 1 || call == 3 {
			input := fmt.Sprintf(`{"goal_id":%d,"evidence_id":%d,"reason":"verified evidence","completion_review":"remaining worker is outside this bounded goal"}`, goal, evidence.Load())
			return []llm.StreamEvent{{Type: llm.SEToolUseStart, ToolID: fmt.Sprint(call), ToolName: "prove_goal"}, {Type: llm.SEToolInputJSON, Text: input}, {Type: llm.SEMessageDelta, StopReason: "tool_use"}}
		}
		if call == 2 {
			select {
			case waiting <- struct{}{}:
			default:
			}
		}
		return []llm.StreamEvent{{Type: llm.SETextDelta, Text: "현재 검증 결과를 반영합니다."}, {Type: llm.SEMessageDelta, StopReason: "end_turn"}}
	}}
	oldAugment, oldResolve := agent.ToolAugment, agent.ToolResolve
	agent.ToolAugment, agent.ToolResolve = nil, nil
	t.Cleanup(func() { agent.ToolAugment, agent.ToolResolve = oldAugment, oldResolve })
	planner := agent.NewPlanner(provider, "fixture", m.dir, nil, 0, 4)
	e := NewEngine(m)
	e.debounce = time.Millisecond
	e.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) { return planner, nil })
	ctx, cancel := context.WithCancel(t.Context())
	run := e.execContextFor(ctx, task.ID)
	done := make(chan struct{})
	go func() { defer close(done); e.plannerLoop(ctx, task) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("planner did not stop")
		}
	}()
	task.Notify()
	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("planner did not attempt the early proof")
	}
	s := &Server{m: m, engine: e}
	assertAPI := func(finished bool, workState string) {
		t.Helper()
		list := httptest.NewRecorder()
		s.listTasks(list, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
		var body struct {
			Tasks []TaskDTO `json:"tasks"`
		}
		if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &body) != nil {
			t.Fatalf("task list failed: status=%d body=%s", list.Code, list.Body.String())
		}
		found := false
		for _, dto := range body.Tasks {
			if dto.ID != task.ID {
				continue
			}
			found = true
			wantMet, wantInFlight := 0, 1
			if finished {
				wantMet, wantInFlight = 1, 0
			}
			if (dto.Status == "done") != finished || dto.GoalsTotal != 1 || dto.GoalsMet != wantMet || dto.InFlight != wantInFlight {
				t.Fatalf("published task state incorrect: %+v", dto)
			}
		}
		if !found {
			t.Fatal("task missing from list")
		}
		graph := httptest.NewRecorder()
		s.explorationGraph(graph, httptest.NewRequest(http.MethodGet, "/api/exploration/graph?task="+task.ID, nil))
		var graphBody struct {
			Nodes []TaskNodeDTO `json:"nodes"`
		}
		if graph.Code != http.StatusOK || json.Unmarshal(graph.Body.Bytes(), &graphBody) != nil {
			t.Fatalf("graph response failed: %s", graph.Body.String())
		}
		states := map[string]string{}
		for _, node := range graphBody.Nodes {
			states[node.ID] = node.State
		}
		wantGoal := "open"
		if finished {
			wantGoal = "met"
		}
		if states[strconv.FormatInt(goal, 10)] != wantGoal || states[strconv.FormatInt(intent, 10)] != workState {
			t.Fatalf("published graph state incorrect: %+v", states)
		}
	}
	if run.Err() != nil {
		t.Fatal("early goal proof cancelled Worker execution")
	}
	assertAPI(false, "running")
	fact, err := task.Store.AddFactWithIntent(t.Context(), map[string]any{"summary": "negative control completed"}, nil, "worker", intent)
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Store.SetIntentState(intent, "done"); err != nil {
		t.Fatal(err)
	}
	evidence.Store(fact)
	task.NotifyDone(intent)
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for task.lifecycleSnapshot().Status != "done" || run.Err() == nil {
		select {
		case <-deadline:
			t.Fatalf("Worker result did not drive final completion; calls=%d", calls.Load())
		case <-tick.C:
		}
	}
	assertAPI(true, "done")
}

func TestGoallessPausedIntentDoesNotTriggerAutomaticPlanner(t *testing.T) {
	m := stateGraphManager(t)
	task, err := m.CreateTask("paused direct intent", "", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	intent, err := task.Store.AddIntent(map[string]any{"summary": "wait for human resume"}, 5, nil, "human")
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Store.SetIntentState(intent, "paused"); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	provider := &browserObservationProvider{step: func(llm.CompletionRequest) []llm.StreamEvent {
		calls.Add(1)
		return []llm.StreamEvent{{Type: llm.SETextDelta, Text: "must not run"}, {Type: llm.SEMessageDelta, StopReason: "end_turn"}}
	}}
	planner := agent.NewPlanner(provider, "fixture", m.dir, nil, 0, 1)
	e := NewEngine(m)
	e.debounce = time.Millisecond
	checked := make(chan struct{}, 1)
	e.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) {
		select {
		case checked <- struct{}{}:
		default:
		}
		return planner, nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); e.plannerLoop(ctx, task) }()
	task.Notify()
	select {
	case <-checked:
	case <-time.After(5 * time.Second):
		t.Error("planner wake was not inspected")
	}
	// The next wake can only reach the resolver after this one has returned.
	// This avoids cancelling early enough to hide an unwanted model call.
	task.Notify()
	select {
	case <-checked:
	case <-time.After(5 * time.Second):
		t.Error("planner did not finish inspecting the paused work")
	}
	if won, err := m.CompleteReviewedTask(t.Context(), task.ID, nil); err != nil || won {
		t.Errorf("paused intent completed: won=%v err=%v", won, err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("planner loop did not stop")
	}
	node, err := task.Store.GetNode(intent)
	if err != nil || node.State != "paused" || task.lifecycleSnapshot().Status == "done" || calls.Load() != 0 {
		t.Fatalf("explicit pause was not preserved: node=%+v calls=%d err=%v", node, calls.Load(), err)
	}
}

func TestLegacyMetGoalReopensBeforePlannerWithActiveWork(t *testing.T) {
	m := stateGraphManager(t)
	for _, state := range []string{"running", "paused"} {
		t.Run(state, func(t *testing.T) {
			task, err := m.CreateTask("legacy completion", "review unfinished validation", nil, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer m.DeleteTask(task.ID, DeleteTaskOptions{})
			goal, err := task.Store.AddGoal(map[string]any{"text": "previously declared complete"}, "human")
			if err != nil {
				t.Fatal(err)
			}
			if err := task.Store.SetNodeState(goal, "met"); err != nil {
				t.Fatal(err)
			}
			intent, err := task.Store.AddIntent(map[string]any{"summary": "unfinished branch"}, 5, nil, "planner")
			if err != nil {
				t.Fatal(err)
			}
			if err := task.Store.SetIntentState(intent, state); err != nil {
				t.Fatal(err)
			}
			called := make(chan struct{}, 1)
			provider := &browserObservationProvider{step: func(llm.CompletionRequest) []llm.StreamEvent {
				select {
				case called <- struct{}{}:
				default:
				}
				return []llm.StreamEvent{{Type: llm.SETextDelta, Text: "남은 결과를 기다립니다."}, {Type: llm.SEMessageDelta, StopReason: "end_turn"}}
			}}
			planner := agent.NewPlanner(provider, "fixture", m.dir, nil, 0, 1)
			e := NewEngine(m)
			e.debounce = time.Millisecond
			e.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) { return planner, nil })
			ctx, cancel := context.WithCancel(t.Context())
			run := e.execContextFor(ctx, task.ID)
			done := make(chan struct{})
			go func() { defer close(done); e.plannerLoop(ctx, task) }()
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("planner loop did not stop")
				}
			}()
			task.Notify()
			select {
			case <-called:
			case <-time.After(5 * time.Second):
				t.Fatal("legacy goal was left waiting for heartbeat instead of reviewed on this wake")
			}
			goalNode, err := task.Store.GetNode(goal)
			if err != nil || goalNode.State != "open" {
				t.Fatalf("Planner received a stale met goal: %+v err=%v", goalNode, err)
			}
			intentNode, err := task.Store.GetNode(intent)
			if err != nil || intentNode.State != state || run.Err() != nil || task.lifecycleSnapshot().Status == "done" {
				t.Fatalf("legacy review stopped/resumed work: %+v err=%v cancellation=%v", intentNode, err, run.Err())
			}
		})
	}
}

func TestDeadlineWithActiveIntentRemainsTimeout(t *testing.T) {
	m := stateGraphManager(t)
	task, err := m.CreateTask("deadline with work", "unfinished verification", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	goal, err := task.Store.AddGoal(map[string]any{"text": "legacy met goal"}, "human")
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Store.SetNodeState(goal, "met"); err != nil {
		t.Fatal(err)
	}
	if _, err := task.Store.AddIntent(map[string]any{"summary": "still needs verification"}, 5, nil, "planner"); err != nil {
		t.Fatal(err)
	}
	status, won, err := NewEngine(m).finishTimedTask(t.Context(), task, true)
	if err != nil || !won || status != "timeout" || task.lifecycleSnapshot().Status != "timeout" {
		t.Fatalf("deadline incorrectly extended or called done: status=%s won=%v err=%v", status, won, err)
	}
}
