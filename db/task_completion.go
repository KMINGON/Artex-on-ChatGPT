package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// CompleteReviewedTask is the final compare-and-set boundary between a Planner's
// review and stopping execution. Graph/Todo writers take the same task row lock.
func (d *DB) CompleteReviewedTask(ctx context.Context, taskID int64) (bool, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var explorationID int64
	var status string
	var paused, queued bool
	var deleted sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT exploration_id,status,paused,queued,deleted_at FROM tasks WHERE id=$1 FOR UPDATE`, taskID).Scan(&explorationID, &status, &paused, &queued, &deleted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if IsTerminal(status) || paused || queued || deleted.Valid {
		return false, nil
	}
	var archiving bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_archives WHERE task_id=$1 AND state<>$2)`, taskID, ArchiveFailed).Scan(&archiving); err != nil {
		return false, err
	}
	if archiving {
		return false, nil
	}
	var openGoals int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind='goal' AND state<>'met'`, explorationID).Scan(&openGoals); err != nil {
		return false, err
	}
	if openGoals > 0 {
		return false, nil
	}
	current, err := pendingGoalWorkTx(ctx, tx, explorationID)
	if err != nil {
		return false, err
	}
	// Parse timestamps in Go so malformed legacy payloads produce an explicit
	// failure rather than choosing an arbitrary review via a string sort.
	rows, err := tx.QueryContext(ctx, `SELECT id,payload->'completion_review' FROM exploration_nodes WHERE exploration_id=$1 AND kind='goal' AND payload ? 'completion_review'`, explorationID)
	if err != nil {
		return false, err
	}
	var reviewed GoalCompletionReview
	var reviewGoal int64
	for rows.Next() {
		var id int64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return false, err
		}
		var candidate GoalCompletionReview
		if err := json.Unmarshal(raw, &candidate); err != nil {
			rows.Close()
			return false, fmt.Errorf("목표 완료 결산을 읽지 못했습니다: %w", err)
		}
		if candidate.ReviewedAt.IsZero() {
			rows.Close()
			return false, errors.New("목표 완료 결산의 검토 시각이 없습니다")
		}
		if reviewGoal == 0 || candidate.ReviewedAt.After(reviewed.ReviewedAt) || (candidate.ReviewedAt.Equal(reviewed.ReviewedAt) && id > reviewGoal) {
			reviewed, reviewGoal = candidate, id
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	if reviewGoal == 0 {
		// Legacy all-met and explicit goalless tasks used frontier exhaustion. They
		// may retain that behavior only when no new structured work remains.
		if len(current.ActiveIntentIDs) > 0 || len(current.PendingTodoIndexes) > 0 {
			return false, nil
		}
	} else if completionReviewHasNewWork(reviewed, current) {
		if _, err := tx.ExecContext(ctx, `UPDATE exploration_nodes SET state='open',blocked_reason=NULL,content_version=content_version+1 WHERE id=$1 AND exploration_id=$2`, reviewGoal, explorationID); err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status='done',completed_at=COALESCE(completed_at,now()),updated_at=now() WHERE id=$1`, taskID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func completionReviewHasNewWork(reviewed, current GoalCompletionReview) bool {
	if reviewed.LatestResultNodeID != nil && current.LatestResultNodeID != nil && *current.LatestResultNodeID > *reviewed.LatestResultNodeID {
		return true
	}
	intents := make(map[int64]bool, len(reviewed.ActiveIntentIDs))
	for _, id := range reviewed.ActiveIntentIDs {
		intents[id] = true
	}
	for _, id := range current.ActiveIntentIDs {
		version, versionKnown := reviewed.ActiveIntentVersions[id]
		if !intents[id] || !versionKnown || version != current.ActiveIntentVersions[id] {
			return true
		}
	}
	if len(current.PendingTodoIndexes) > 0 {
		if reviewed.PlannerTodoRevision != current.PlannerTodoRevision || reviewed.PlannerTodosSHA256 != current.PlannerTodosSHA256 {
			return true
		}
		pending := make(map[int]bool, len(reviewed.PendingTodoIndexes))
		for _, index := range reviewed.PendingTodoIndexes {
			pending[index] = true
		}
		for _, index := range current.PendingTodoIndexes {
			if !pending[index] {
				return true
			}
		}
	}
	return false
}
