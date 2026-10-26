package db

import (
	"context"
	"database/sql"
	"errors"
)

// CompleteTaskIfGoalsSettled preserves the original goal-based completion rule.
// The task lock prevents completion from overwriting concurrent lifecycle changes.
// requireIdle is used by manual/goalless work, which finishes when its queue drains.
func (d *DB) CompleteTaskIfGoalsSettled(ctx context.Context, taskID int64, requireIdle bool) (bool, error) {
	_, won, err := d.finishTask(ctx, taskID, requireIdle, false, true)
	return won, err
}

// FinishTimedTask keeps the original absolute deadline: pause/queue does not
// extend it. The final goal decision and task status commit under one row lock.
func (d *DB) FinishTimedTask(ctx context.Context, taskID int64, met bool) (string, bool, error) {
	return d.finishTask(ctx, taskID, false, true, met)
}

func (d *DB) finishTask(ctx context.Context, taskID int64, requireIdle, deadlineReached, met bool) (string, bool, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	var explorationID int64
	var status string
	var paused, queued bool
	var deleted sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT exploration_id,status,paused,queued,deleted_at FROM tasks WHERE id=$1 FOR UPDATE`, taskID).Scan(&explorationID, &status, &paused, &queued, &deleted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	if IsTerminal(status) || deleted.Valid || (!deadlineReached && (paused || queued)) {
		return "", false, nil
	}
	var archiving bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_archives WHERE task_id=$1 AND state<>$2)`, taskID, ArchiveFailed).Scan(&archiving); err != nil {
		return "", false, err
	}
	if archiving {
		return "", false, nil
	}
	var openGoals int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind='goal' AND state='open'`, explorationID).Scan(&openGoals); err != nil {
		return "", false, err
	}
	targetStatus := "done"
	if deadlineReached {
		if !met || openGoals > 0 {
			targetStatus = "timeout"
		}
	} else if openGoals > 0 {
		return "", false, nil
	}
	if requireIdle {
		var active bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent' AND state IN ('open','running','paused'))`, explorationID).Scan(&active); err != nil {
			return "", false, err
		}
		if active {
			return "", false, nil
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status=$2,completed_at=COALESCE(completed_at,now()),updated_at=now() WHERE id=$1`, taskID, targetStatus); err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return targetStatus, true, nil
}
