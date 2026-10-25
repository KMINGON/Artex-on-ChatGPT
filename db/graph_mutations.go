package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func addGraphNodeTx(ctx context.Context, tx *sql.Tx, explorationID int64, kind string, payload map[string]any, priority int, state, origin string, anchors []int64) (int64, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO exploration_nodes(exploration_id,kind,payload,priority,state,origin)
VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, explorationID, kind, string(raw), priority, state, origin).Scan(&id); err != nil {
		return 0, err
	}
	for _, assetID := range anchors {
		if _, err := tx.ExecContext(ctx, `INSERT INTO exploration_anchors(node_id,asset_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, assetID); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// graphSourceNodeTx permits local nodes and explicitly related source tasks only.
// Lock the selected node so it cannot disappear between validation and linking.
func graphSourceNodeTx(ctx context.Context, tx *sql.Tx, explorationID, nodeID int64) (kind string, err error) {
	err = tx.QueryRowContext(ctx, `SELECT n.kind FROM exploration_nodes n WHERE n.id=$2 AND n.state<>'dismissed' AND (
n.exploration_id=$1 OR EXISTS (
 SELECT 1 FROM tasks owner
 JOIN task_relations relation ON relation.task_id=owner.id
 JOIN tasks source ON source.id=relation.source_task_id
 WHERE owner.exploration_id=$1 AND source.exploration_id=n.exploration_id
 AND owner.deleted_at IS NULL AND source.deleted_at IS NULL
)) FOR KEY SHARE OF n`, explorationID, nodeID).Scan(&kind)
	return
}

// OutcomeIDs identifies the real fact/finding nodes produced by one local intent.
func (s *ExplorationStore) OutcomeIDs(intentID int64) ([]int64, error) {
	rows, err := s.db.Query(`SELECT n.id FROM exploration_edges e
JOIN exploration_nodes n ON n.id=e.dst_id AND n.exploration_id=e.exploration_id
WHERE e.exploration_id=$1 AND e.src_id=$2 AND e.rel='yields' AND n.kind IN ('fact','finding') ORDER BY n.id`, s.expID, intentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// lockGraphTaskTx applies the same archive boundary as finding evidence writes.
// A standalone exploration has no task row and retains its existing behavior.
func lockGraphTaskTx(ctx context.Context, tx *sql.Tx, explorationID int64) error {
	var taskID int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM tasks WHERE exploration_id=$1`, explorationID).Scan(&taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return LockTaskEvidenceTx(tx, taskID)
}

// State changes share the completion lock. Automatic recovery/claim must not
// reopen terminal tasks; explicit user changes leave task admission to the server.
func (s *ExplorationStore) execGraphStateMutation(liveTaskOnly bool, query string, args ...any) (int64, error) {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := lockGraphTaskTx(ctx, tx, s.expID); err != nil {
		return 0, err
	}
	if liveTaskOnly {
		var status string
		err := tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE exploration_id=$1`, s.expID).Scan(&status)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		if IsTerminal(status) {
			return 0, nil
		}
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// AddIntentWithParents commits a new intent and all its provenance together.
// The source nodes remain read-only, including nodes in a directly related task.
func (s *ExplorationStore) AddIntentWithParents(ctx context.Context, payload map[string]any, priority int, anchors, parents []int64, origin string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := lockGraphTaskTx(ctx, tx, s.expID); err != nil {
		return 0, err
	}
	if len(parents) == 0 {
		var root int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM exploration_nodes WHERE exploration_id=$1 AND kind='fact' AND state='origin' ORDER BY id LIMIT 1 FOR KEY SHARE`, s.expID).Scan(&root)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		if root > 0 {
			parents = []int64{root}
		}
	}
	for _, parent := range parents {
		kind, err := graphSourceNodeTx(ctx, tx, s.expID, parent)
		if err != nil {
			return 0, fmt.Errorf("parent_id %d의 근거를 조회하지 못했습니다: %w", parent, err)
		}
		if kind != KindFact && kind != KindFinding {
			return 0, fmt.Errorf("parent_id %d는 사실 또는 취약점 노드여야 합니다", parent)
		}
	}
	if origin == "" {
		origin = "planner"
	}
	id, err := addGraphNodeTx(ctx, tx, s.expID, KindIntent, payload, priority, "open", origin, anchors)
	if err != nil {
		return 0, err
	}
	for _, parent := range parents {
		if _, err := tx.ExecContext(ctx, `INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, s.expID, parent, RelDerivedFrom, id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// AddFactWithIntent never leaves a successful fact without its requested yields edge.
// intentID=0 is retained for explicit main-agent/maintenance observations.
func (s *ExplorationStore) AddFactWithIntent(ctx context.Context, payload map[string]any, anchors []int64, origin string, intentID int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := lockGraphTaskTx(ctx, tx, s.expID); err != nil {
		return 0, err
	}
	if intentID > 0 {
		var kind string
		if err := tx.QueryRowContext(ctx, `SELECT kind FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 FOR KEY SHARE`, intentID, s.expID).Scan(&kind); err != nil {
			return 0, fmt.Errorf("intent_id는 이 작업의 의도여야 합니다: %w", err)
		}
		if kind != KindIntent {
			return 0, errors.New("intent_id는 이 작업의 의도여야 합니다")
		}
	}
	id, err := addGraphNodeTx(ctx, tx, s.expID, KindFact, payload, 5, "confirmed", origin, anchors)
	if err != nil {
		return 0, err
	}
	if intentID > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id) VALUES($1,$2,$3,$4)`, s.expID, intentID, RelYields, id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// GoalProof is kept in the goal payload so detail views and archives preserve it.
type GoalProof struct {
	EvidenceID int64     `json:"evidence_id"`
	Reason     string    `json:"reason"`
	ProvedAt   time.Time `json:"proved_at"`
}

// AddGoalWithOrigin keeps the task root and goal lineage in one transaction.
func (s *ExplorationStore) AddGoalWithOrigin(ctx context.Context, payload map[string]any, origin string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := lockGraphTaskTx(ctx, tx, s.expID); err != nil {
		return 0, err
	}
	var root int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM exploration_nodes WHERE exploration_id=$1 AND kind='fact' AND state='origin' ORDER BY id LIMIT 1 FOR KEY SHARE`, s.expID).Scan(&root)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	id, err := addGraphNodeTx(ctx, tx, s.expID, KindGoal, payload, 0, "open", origin, nil)
	if err != nil {
		return 0, err
	}
	if root > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id) VALUES($1,$2,'spawns',$3)`, s.expID, root, id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// GoalCompletionReview preserves why a final goal can close despite pending work.
type GoalCompletionReview struct {
	Reason                   string           `json:"reason"`
	ActiveIntentIDs          []int64          `json:"active_intent_ids"`
	ActiveIntentVersions     map[int64]int    `json:"active_intent_versions"`
	PendingFollowUps         []FollowUpRef    `json:"pending_follow_ups"`
	PendingFollowUpRevisions map[string]int64 `json:"pending_follow_up_revisions"`
	PendingTodoIndexes       []int            `json:"pending_todo_indexes"` // zero-based indexes in the saved Planner Todo list
	LatestResultNodeID       *int64           `json:"latest_result_node_id,omitempty"`
	PlannerTodoRevision      int64            `json:"planner_todo_revision"`
	PlannerTodosSHA256       string           `json:"planner_todos_sha256"`
	ReviewedAt               time.Time        `json:"reviewed_at"`
}

func pendingGoalWorkTx(ctx context.Context, tx *sql.Tx, explorationID int64) (GoalCompletionReview, error) {
	review := GoalCompletionReview{ActiveIntentIDs: []int64{}, ActiveIntentVersions: map[int64]int{}, PendingFollowUps: []FollowUpRef{}, PendingFollowUpRevisions: map[string]int64{}, PendingTodoIndexes: []int{}}
	rows, err := tx.QueryContext(ctx, `SELECT id,content_version FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent' AND state IN ('open','running','paused') ORDER BY id`, explorationID)
	if err != nil {
		return review, err
	}
	for rows.Next() {
		var id int64
		var version int
		if err := rows.Scan(&id, &version); err != nil {
			rows.Close()
			return review, err
		}
		review.ActiveIntentIDs = append(review.ActiveIntentIDs, id)
		review.ActiveIntentVersions[id] = version
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return review, err
	}
	var latestResult int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM exploration_nodes
		WHERE exploration_id=$1 AND kind IN ('fact','finding') AND state<>'origin'
		AND payload->>'observation_source' IS DISTINCT FROM 'http_response'`, explorationID).Scan(&latestResult); err != nil {
		return review, err
	}
	review.LatestResultNodeID = &latestResult
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT payload FROM exploration_nodes WHERE exploration_id=$1 AND ((kind='fact' AND state='origin') OR kind='begin') ORDER BY CASE WHEN kind='fact' THEN 0 ELSE 1 END,id LIMIT 1`, explorationID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return review, nil
	}
	if err != nil {
		return review, err
	}
	todos, err := decodePlannerTodoState(raw)
	if err != nil {
		return review, err
	}
	review.PlannerTodoRevision = todos.Revision
	encoded, _ := json.Marshal(todos.Items)
	review.PlannerTodosSHA256 = fmt.Sprintf("%x", sha256.Sum256(encoded))
	for index, todo := range todos.Items {
		if todo.Status == "pending" || todo.Status == "in_progress" {
			review.PendingTodoIndexes = append(review.PendingTodoIndexes, index)
		}
	}
	return review, nil
}

// ProveGoal commits the proof, its explanation, and the goal state atomically.
func (s *ExplorationStore) ProveGoal(ctx context.Context, goalID, evidenceID int64, reason string, completionReview ...string) (allMet bool, count int, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, err
	}
	defer tx.Rollback()
	if err := lockGraphTaskTx(ctx, tx, s.expID); err != nil {
		return false, 0, err
	}
	// Concurrent proofs must see each other's committed states before allMet is read.
	var locked int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM explorations WHERE id=$1 FOR UPDATE`, s.expID).Scan(&locked); err != nil {
		return false, 0, err
	}
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT payload FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 AND kind='goal' FOR UPDATE`, goalID, s.expID).Scan(&raw); err != nil {
		return false, 0, fmt.Errorf("goal_id는 이 작업의 목표 노드여야 합니다: %w", err)
	}
	kind, err := graphSourceNodeTx(ctx, tx, s.expID, evidenceID)
	if err != nil || (kind != KindFact && kind != KindFinding) {
		if err == nil {
			err = errors.New("사실 또는 취약점 노드가 아닙니다")
		}
		return false, 0, fmt.Errorf("evidence_id는 이 작업 또는 직접 연결된 작업의 사실/취약점 노드여야 합니다: %w", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false, 0, err
	}
	if payload == nil {
		payload = map[string]json.RawMessage{}
	}
	// Check the last outstanding goal before any proof/state writes. The task row
	// lock serializes this decision with intent/fact/lead registration.
	var remainingGoals int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind='goal' AND id<>$2 AND state<>'met'`, s.expID, goalID).Scan(&remainingGoals); err != nil {
		return false, 0, err
	}
	if remainingGoals == 0 {
		review, err := pendingGoalWorkTx(ctx, tx, s.expID)
		if err != nil {
			return false, 0, err
		}
		if len(completionReview) > 0 {
			review.Reason = strings.TrimSpace(completionReview[0])
		}
		if (len(review.ActiveIntentIDs) > 0 || len(review.PendingTodoIndexes) > 0) && review.Reason == "" {
			return false, 0, fmt.Errorf("진행 중 의도 %d개, 미완료 계획 항목 %d개가 남아 있습니다. 결과를 확인하거나 completion_review에 남은 작업이 목표 달성에 영향을 주지 않는 근거를 기록하세요", len(review.ActiveIntentIDs), len(review.PendingTodoIndexes))
		}
		if review.Reason == "" {
			review.Reason = strings.TrimSpace(reason)
		}
		review.ReviewedAt = time.Now().UTC()
		payload["completion_review"], err = json.Marshal(review)
		if err != nil {
			return false, 0, err
		}
	}
	var proofs []GoalProof
	if existing := payload["proofs"]; len(existing) > 0 {
		if err := json.Unmarshal(existing, &proofs); err != nil {
			return false, 0, fmt.Errorf("기존 목표 증명 기록을 읽지 못했습니다: %w", err)
		}
	}
	proof := GoalProof{EvidenceID: evidenceID, Reason: reason, ProvedAt: time.Now().UTC()}
	replaced := false
	for i := range proofs {
		if proofs[i].EvidenceID == evidenceID {
			proofs[i], replaced = proof, true
			break
		}
	}
	if !replaced {
		proofs = append(proofs, proof)
	}
	payload["proofs"], err = json.Marshal(proofs)
	if err != nil {
		return false, 0, err
	}
	raw, err = json.Marshal(payload)
	if err != nil {
		return false, 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, s.expID, evidenceID, RelProves, goalID); err != nil {
		return false, 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE exploration_nodes SET state='met',payload=$1,blocked_reason=NULL,content_version=content_version+1 WHERE id=$2 AND exploration_id=$3`, string(raw), goalID, s.expID); err != nil {
		return false, 0, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(bool_and(state='met'),false) FROM exploration_nodes WHERE exploration_id=$1 AND kind='goal'`, s.expID).Scan(&count, &allMet); err != nil {
		return false, 0, err
	}
	if err := tx.Commit(); err != nil {
		return false, 0, err
	}
	return allMet && count > 0, count, nil
}
