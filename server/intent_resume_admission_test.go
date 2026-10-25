package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestIntentResumeRevivesTerminalTaskAndPreservesPauseQueue(t *testing.T) {
	m := stateGraphManager(t)
	defer restoreConcurrencySetting(t, m)()
	if err := m.SetConcurrency(false, defaultConcurrencyLimit); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, status                string
		paused, queued, unavailable bool
	}{
		{name: "completed task", status: "done"},
		{name: "timed out task", status: "timeout"},
		{name: "terminal without provider joins queue", status: "done", unavailable: true},
		{name: "user pause is preserved", status: "running", paused: true},
		{name: "queue is preserved", status: "running", queued: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task, err := m.CreateTask(tc.name, "resume fixture", nil, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer m.DeleteTask(task.ID, DeleteTaskOptions{})
			intent, err := task.Store.AddIntent(map[string]any{"summary": "paused fixture"}, 1, nil, "planner")
			if err != nil {
				t.Fatal(err)
			}
			if err := task.Store.SetIntentState(intent, "paused"); err != nil {
				t.Fatal(err)
			}
			if err := m.SetTaskStatus(task.ID, tc.status); err != nil {
				t.Fatal(err)
			}
			s := newAdmissionTestServer(m, func(*Task) bool { return !tc.unavailable })
			if tc.paused {
				if err := m.SetTaskPaused(task.ID, true); err != nil {
					t.Fatal(err)
				}
				s.engine.Pause(task.ID, nil)
			}
			if tc.queued {
				if err := m.ApplyTaskAdmission(task.ID, tc.status, tc.status, true, "resume", false); err != nil {
					t.Fatal(err)
				}
				s.engine.Pause(task.ID, nil)
			}
			before := task.lifecycleSnapshot()
			result, err := s.applyIntentControl(t.Context(), task, intent, "resume", "", "")
			if err != nil || result.State != "open" {
				t.Fatalf("resume=%+v %v", result, err)
			}
			state := task.lifecycleSnapshot()
			if state.Status != "running" || state.Paused != tc.paused || state.Queued != (tc.queued || tc.unavailable) {
				t.Fatalf("resume changed lifecycle incorrectly: %+v", state)
			}
			if tc.queued && state.QueuedAt != before.QueuedAt {
				t.Fatal("intent resume changed FIFO position")
			}
			if tc.paused && !s.engine.IsPaused(task.ID) {
				t.Fatal("intent resume cleared user's task pause")
			}
			stored, err := task.Store.GetNode(intent)
			if err != nil || stored.State != "open" {
				t.Fatalf("intent=%+v %v", stored, err)
			}
		})
	}
}

func TestIntentResumeRollsBackWhenAdmissionConflicts(t *testing.T) {
	m := stateGraphManager(t)
	defer restoreConcurrencySetting(t, m)()
	if err := m.SetConcurrency(false, defaultConcurrencyLimit); err != nil {
		t.Fatal(err)
	}
	task, err := m.CreateTask("conflicting resume", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	intent, err := task.Store.AddIntent(map[string]any{"summary": "paused fixture"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Store.SetIntentState(intent, "paused"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetTaskStatus(task.ID, "timeout"); err != nil {
		t.Fatal(err)
	}
	id, _ := strconv.ParseInt(task.ID, 10, 64)
	// A deterministic stale lifecycle snapshot forces the admission CAS to fail.
	if err := m.pg.SetStatus(id, "done"); err != nil {
		t.Fatal(err)
	}
	s := newAdmissionTestServer(m, nil)
	if _, err := s.applyIntentControl(t.Context(), task, intent, "resume", "", ""); err == nil {
		t.Fatal("conflicting admission succeeded")
	}
	stored, err := task.Store.GetNode(intent)
	if err != nil || stored.State != "paused" {
		t.Fatalf("failed resume left intent runnable: %+v %v", stored, err)
	}
	row, err := m.pg.GetTask(id)
	if err != nil || row.Status != "done" {
		t.Fatalf("failed resume changed terminal task: %+v %v", row, err)
	}
}

func TestIntentResumeWaitsForCompletionMemoryRefresh(t *testing.T) {
	m := stateGraphManager(t)
	defer restoreConcurrencySetting(t, m)()
	if err := m.SetConcurrency(false, defaultConcurrencyLimit); err != nil {
		t.Fatal(err)
	}
	task, err := m.CreateTask("completion memory gap", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	intent, err := task.Store.AddIntent(map[string]any{"summary": "paused fixture"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Store.SetIntentState(intent, "paused"); err != nil {
		t.Fatal(err)
	}
	s := newAdmissionTestServer(m, nil)
	id, _ := strconv.ParseInt(task.ID, 10, 64)
	// Reproduce the exact gap between a terminal DB commit and its live-handle
	// refresh. The Manager owns taskStateMu across both operations.
	m.taskStateMu.Lock()
	locked := true
	defer func() {
		if locked {
			m.taskStateMu.Unlock()
		}
	}()
	if err := m.pg.SetStatus(id, "done"); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		_, err := s.applyIntentControl(t.Context(), task, intent, "resume", "", "")
		finished <- err
	}()
	select {
	case err := <-finished:
		t.Fatalf("resume crossed incomplete lifecycle refresh: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	node, err := task.Store.GetNode(intent)
	if err != nil || node.State != "paused" {
		t.Fatalf("resume changed intent before lifecycle refresh: %+v %v", node, err)
	}
	task.updateLifecycle(func(state *taskLifecycleState) { state.Status = "done" })
	m.taskStateMu.Unlock()
	locked = false
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("resume did not proceed after lifecycle refresh")
	}
	if task.lifecycleSnapshot().Status != "running" {
		t.Fatal("resumed intent was stranded in terminal task")
	}
	row, err := m.pg.GetTask(id)
	if err != nil || row.Status != "running" {
		t.Fatalf("resumed task status=%+v %v", row, err)
	}
}

func TestGoalChangeWaitsForCompletionMemoryRefresh(t *testing.T) {
	m := stateGraphManager(t)
	defer restoreConcurrencySetting(t, m)()
	if err := m.SetConcurrency(false, defaultConcurrencyLimit); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"add", "edit"} {
		t.Run(action, func(t *testing.T) {
			task, err := m.CreateTask("goal completion gap", "fixture", nil, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer m.DeleteTask(task.ID, DeleteTaskOptions{})
			goal, err := task.Store.AddGoalWithOrigin(t.Context(), map[string]any{"text": "old goal"}, "human")
			if err != nil {
				t.Fatal(err)
			}
			s := newAdmissionTestServer(m, nil)
			id, _ := strconv.ParseInt(task.ID, 10, 64)
			m.taskStateMu.Lock()
			locked := true
			defer func() {
				if locked {
					m.taskStateMu.Unlock()
				}
			}()
			if err := m.pg.SetStatus(id, "done"); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/goals", strings.NewReader(`{"text":"new work"}`))
			req.SetPathValue("id", task.ID)
			req.SetPathValue("gid", fmt.Sprint(goal))
			recorder := httptest.NewRecorder()
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				if action == "add" {
					s.addGoal(recorder, req)
				} else {
					s.editGoal(recorder, req)
				}
			}()
			// The new goal commits after the terminal DB write but before the live
			// handle refresh. Admission must wait for that refresh, not return 200
			// while the task remains done.
			deadline := time.Now().Add(3 * time.Second)
			for {
				var saved bool
				if err := m.pg.QueryRow(`SELECT EXISTS(SELECT 1 FROM exploration_nodes WHERE exploration_id=$1 AND kind='goal' AND payload->>'text'='new work')`, task.ExpID).Scan(&saved); err != nil {
					t.Fatal(err)
				}
				if saved {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("new goal did not commit before admission")
				}
				time.Sleep(time.Millisecond)
			}
			select {
			case <-finished:
				t.Fatal("goal handler returned during incomplete lifecycle refresh")
			case <-time.After(20 * time.Millisecond):
			}
			task.updateLifecycle(func(state *taskLifecycleState) { state.Status = "done" })
			m.taskStateMu.Unlock()
			locked = false
			select {
			case <-finished:
			case <-time.After(3 * time.Second):
				t.Fatal("goal admission did not resume")
			}
			if recorder.Code != http.StatusOK || task.lifecycleSnapshot().Status != "running" {
				t.Fatalf("goal lost after completion: HTTP=%d state=%s", recorder.Code, task.lifecycleSnapshot().Status)
			}
		})
	}
}
