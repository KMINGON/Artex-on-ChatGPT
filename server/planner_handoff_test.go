package server

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

func TestFailedPlannerHandoffPreservesArrivalsDuringRound(t *testing.T) {
	task := &Task{notify: make(chan struct{}, 1)}
	task.NotifyDone(12)
	task.NotifyFinding(12, "partially verified; second account remains")
	before := task.drainTriggers()
	if len(before) != 2 {
		t.Fatalf("initial handoff=%+v", before)
	}
	// A different Worker can finish while the model request is failing/paused.
	task.NotifyDone(34)
	task.restoreTriggers(before)
	want := append(append([]agent.TriggerEvent{}, before...), agent.TriggerEvent{Kind: "done", IntentID: 34})
	got := task.drainTriggers()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("handoff lost/reordered: %+v, want %+v", got, want)
	}
	// Repeated failed rounds preserve the same events without duplicating them.
	task.restoreTriggers(got)
	if next := task.drainTriggers(); !reflect.DeepEqual(next, want) {
		t.Fatalf("repeated failure changed handoff: %+v", next)
	}
	if next := task.drainTriggers(); len(next) != 0 {
		t.Fatalf("successful consumption left stale events: %+v", next)
	}
}

type interruptedPlannerProvider struct {
	started chan struct{}
	release chan struct{}
}

func (p *interruptedPlannerProvider) Stream(ctx context.Context, _ llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		select {
		case p.started <- struct{}{}:
		default:
		}
		select {
		case <-p.release:
			yield(llm.StreamEvent{}, errors.New("fixture interrupted model request"))
		case <-ctx.Done():
			yield(llm.StreamEvent{}, ctx.Err())
		}
	}
}

func (p *interruptedPlannerProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return accumulateStreamForTest(ctx, p.Stream, req)
}

func TestPlannerLoopRetainsWorkerHandoffAfterModelFailure(t *testing.T) {
	m := stateGraphManager(t)
	task, err := m.CreateTask("incomplete verification", "finish remaining questions", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	if _, err := task.Store.AddGoal(map[string]any{"text": "remaining validation"}, "human"); err != nil {
		t.Fatal(err)
	}
	finished := func(summary string) int64 {
		t.Helper()
		id, err := task.Store.AddIntent(map[string]any{"summary": summary}, 5, nil, "planner")
		if err != nil {
			t.Fatal(err)
		}
		if err := task.Store.SetIntentState(id, "done"); err != nil {
			t.Fatal(err)
		}
		if _, err := task.Store.AppendActivity(db.Activity{NodeID: &id, Worker: "work#1", Kind: "result", Detail: summary}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := finished("positive control saved; negative control remains")
	second := finished("new identity condition remains to test")
	provider := &interruptedPlannerProvider{started: make(chan struct{}, 1), release: make(chan struct{})}
	planner := agent.NewPlanner(provider, "fixture", m.dir, nil, 0, 1)
	e := NewEngine(m)
	e.debounce = time.Millisecond
	e.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) { return planner, nil })
	ctx, cancel := context.WithCancel(t.Context())
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
	task.NotifyDone(first)
	select {
	case <-provider.started:
	case <-time.After(5 * time.Second):
		t.Fatal("planner did not receive the first handoff")
	}
	// Add an arrival without waking another round, so this test observes the
	// failed round's handoff boundary rather than consuming it immediately.
	task.trigMu.Lock()
	task.pendingTriggers = append(task.pendingTriggers, agent.TriggerEvent{Kind: "done", IntentID: second})
	task.trigMu.Unlock()
	close(provider.release)
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		task.trigMu.Lock()
		events := append([]agent.TriggerEvent{}, task.pendingTriggers...)
		task.trigMu.Unlock()
		if len(events) == 2 && events[0].IntentID == first && events[1].IntentID == second {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("model failure discarded Worker handoff: %+v", events)
		case <-tick.C:
		}
	}
}
