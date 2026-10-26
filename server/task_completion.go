package server

import (
	"context"
	"strconv"
	"time"
)

// CompleteTaskIfGoalsSettled serializes completion/cancellation with manager resume
// transitions. A new goal can revive the task only after the old run is cancelled.
func (m *Manager) CompleteTaskIfGoalsSettled(ctx context.Context, id string, requireIdle bool, onComplete func()) (bool, error) {
	_, won, err := m.finishTask(ctx, id, requireIdle, false, true, onComplete)
	return won, err
}

func (m *Manager) finishTimedTask(ctx context.Context, id string, met bool, onComplete func()) (string, bool, error) {
	return m.finishTask(ctx, id, false, true, met, onComplete)
}

func (m *Manager) finishTask(ctx context.Context, id string, requireIdle, deadlineReached, met bool, onComplete func()) (string, bool, error) {
	m.taskStateMu.Lock()
	defer m.taskStateMu.Unlock()
	taskID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return "", false, err
	}
	var won bool
	status := "done"
	if deadlineReached {
		status, won, err = m.pg.FinishTimedTask(ctx, taskID, met)
	} else {
		won, err = m.pg.CompleteTaskIfGoalsSettled(ctx, taskID, requireIdle)
	}
	if err != nil || !won {
		return "", won, err
	}
	m.mu.Lock()
	if task := m.tasks[id]; task != nil {
		task.updateLifecycle(func(state *taskLifecycleState) {
			state.Status = status
			if state.CompletedAt == 0 {
				state.CompletedAt = time.Now().Unix()
			}
		})
	}
	m.mu.Unlock()
	if status == "done" && onComplete != nil {
		onComplete()
	}
	return status, true, nil
}
