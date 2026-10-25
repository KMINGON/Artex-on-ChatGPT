package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// PlannerTodo is stored on the original task node so task archives preserve the
// plan with its graph, without a second independently restored state store.
type PlannerTodo struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

type PlannerTodoState struct {
	Items    []PlannerTodo
	Revision int64
}

var ErrPlannerStateChanged = errors.New("계획 상태가 다른 실행에서 변경되었습니다. 최신 계획을 다시 읽어 주세요")

func (s *ExplorationStore) LoadPlannerTodos(ctx context.Context) (PlannerTodoState, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM exploration_nodes
		WHERE exploration_id=$1 AND ((kind='fact' AND state='origin') OR kind='begin')
		ORDER BY CASE WHEN kind='fact' THEN 0 ELSE 1 END, id LIMIT 1`, s.expID).Scan(&raw)
	if err != nil {
		return PlannerTodoState{}, fmt.Errorf("계획 저장 노드 조회: %w", err)
	}
	return decodePlannerTodoState(raw)
}

func decodePlannerTodoState(raw []byte) (PlannerTodoState, error) {
	if strings.TrimSpace(string(raw)) == "null" {
		return PlannerTodoState{}, errors.New("계획 저장 노드의 내용이 비어 있습니다")
	}
	var payload struct {
		Items    []PlannerTodo `json:"planner_todos"`
		Revision int64         `json:"planner_todos_revision"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return PlannerTodoState{}, fmt.Errorf("저장된 계획 해석: %w", err)
	}
	if payload.Revision < 0 {
		return PlannerTodoState{}, errors.New("저장된 계획 버전이 올바르지 않습니다")
	}
	if err := validatePlannerTodos(payload.Items); err != nil {
		return PlannerTodoState{}, err
	}
	return PlannerTodoState{Items: payload.Items, Revision: payload.Revision}, nil
}

func validatePlannerTodos(items []PlannerTodo) error {
	if len(items) > 200 {
		return errors.New("계획 항목은 최대 200개까지 저장할 수 있습니다")
	}
	total := 0
	for _, item := range items {
		size := len([]rune(item.Content))
		total += size
		if strings.TrimSpace(item.Content) == "" || size > 4000 {
			return errors.New("계획 내용은 1~4000자로 작성해 주세요")
		}
		switch item.Status {
		case "pending", "in_progress", "completed":
		default:
			return errors.New("계획 상태는 pending, in_progress, completed 중 하나여야 합니다")
		}
	}
	if total > 24_000 {
		return errors.New("계획 전체는 24000자 이내로 요약하고 상세 근거는 fact로 연결해 주세요")
	}
	return nil
}

// SavePlannerTodos rejects a late write from an older Planner instance rather
// than replacing a newer plan after a connection change or resumed execution.
func (s *ExplorationStore) SavePlannerTodos(ctx context.Context, items []PlannerTodo, expectedRevision int64) (int64, error) {
	if err := validatePlannerTodos(items); err != nil {
		return expectedRevision, err
	}
	if expectedRevision < 0 {
		return expectedRevision, ErrPlannerStateChanged
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return expectedRevision, err
	}
	defer tx.Rollback()
	if err := lockGraphTaskTx(ctx, tx, s.expID); err != nil {
		return expectedRevision, err
	}
	var id int64
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT id,payload FROM exploration_nodes
		WHERE exploration_id=$1 AND ((kind='fact' AND state='origin') OR kind='begin')
		ORDER BY CASE WHEN kind='fact' THEN 0 ELSE 1 END, id LIMIT 1 FOR UPDATE`, s.expID).Scan(&id, &raw); err != nil {
		if err == sql.ErrNoRows {
			return expectedRevision, errors.New("계획을 저장할 작업 시작 노드가 없습니다")
		}
		return expectedRevision, err
	}
	current, err := decodePlannerTodoState(raw)
	if err != nil {
		return expectedRevision, err
	}
	if current.Revision != expectedRevision {
		return expectedRevision, ErrPlannerStateChanged
	}
	if items == nil {
		items = []PlannerTodo{}
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return expectedRevision, err
	}
	nextRevision := expectedRevision + 1
	if _, err := tx.ExecContext(ctx, `UPDATE exploration_nodes SET payload=payload ||
		jsonb_build_object('planner_todos',$2::jsonb,'planner_todos_revision',$3::bigint),
		content_version=content_version+1 WHERE id=$1 AND exploration_id=$4`, id, string(encoded), nextRevision, s.expID); err != nil {
		return expectedRevision, err
	}
	if err := tx.Commit(); err != nil {
		return expectedRevision, err
	}
	return nextRevision, nil
}
