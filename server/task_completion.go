package server

import (
	"context"
	"strconv"
	"time"
)

// CompleteReviewedTask serializes completion/cancellation with manager resume
// transitions. A new goal can revive the task only after the old run is cancelled.
func (m *Manager) CompleteReviewedTask(ctx context.Context, id string, onComplete func()) (bool, error) {
	m.taskStateMu.Lock()
	defer m.taskStateMu.Unlock()
	taskID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return false, err
	}
	won, err := m.pg.CompleteReviewedTask(ctx, taskID)
	if err != nil || !won {
		return won, err
	}
	m.mu.Lock()
	if task := m.tasks[id]; task != nil {
		task.updateLifecycle(func(state *taskLifecycleState) {
			state.Status = "done"
			if state.CompletedAt == 0 {
				state.CompletedAt = time.Now().Unix()
			}
		})
	}
	m.mu.Unlock()
	if onComplete != nil {
		onComplete()
	}
	return true, nil
}
