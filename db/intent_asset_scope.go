package db

import (
	"context"
	"errors"
	"fmt"
)

// PrepareIntentAssets loads the explicit worker targets and, when enabled,
// saves their conservative scope as one task-owned mutation. It does not
// re-register assets or replace their original provenance.
func (s *AssetStore) PrepareIntentAssets(ctx context.Context, taskID, explorationID, intentID int64, ids []int64, addScope bool) ([]*Asset, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if taskID <= 0 || explorationID <= 0 || intentID <= 0 {
		return nil, errors.New("시작 자산을 연결할 작업·의도가 필요합니다")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := LockTaskEvidenceTx(tx, taskID); err != nil {
		return nil, err
	}
	var valid bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks t JOIN exploration_nodes n ON n.exploration_id=t.exploration_id WHERE t.id=$1 AND t.exploration_id=$2 AND n.id=$3 AND n.kind='intent')`, taskID, explorationID, intentID).Scan(&valid); err != nil {
		return nil, err
	}
	if !valid {
		return nil, errors.New("시작 자산의 작업과 의도가 일치하지 않습니다")
	}
	rows, err := tx.QueryContext(ctx, assetSelectCols+` WHERE id=ANY($1::bigint[]) ORDER BY last_seen DESC FOR KEY SHARE`, ids)
	if err != nil {
		return nil, err
	}
	assets, err := scanAssets(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if addScope {
		scoped := &AssetStore{db: s.db, company: s.company, tx: tx}
		for _, asset := range assets {
			if err := scoped.AddAutoScope(taskID, asset.Type, asset.Domain, asset.URL, asset.IP); err != nil {
				return nil, fmt.Errorf("시작 자산 #%d의 조회 범위를 저장하지 못했습니다: %w", asset.ID, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return assets, nil
}
