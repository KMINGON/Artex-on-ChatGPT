package db

import (
	"context"
	"errors"
	"fmt"
)

// AssetObservationContext binds an explicitly registered asset to its execution.
// The callback and all provenance/scope writes commit or roll back as one item.
type AssetObservationContext struct {
	TaskID, ExplorationID, OwnerNodeID int64
	Type, Domain, URL, IP              string
}

func (s *AssetStore) RegisterObservedAsset(ctx context.Context, in AssetObservationContext, upsert func(*AssetStore) (int64, error)) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := lockCompanyScopeMutation(tx); err != nil {
		return 0, err
	}
	scoped := &AssetStore{db: s.db, company: s.company, tx: tx}
	if in.TaskID > 0 {
		if err := LockTaskEvidenceTx(tx, in.TaskID); err != nil {
			return 0, err
		}
		var explorationID int64
		if err := tx.QueryRowContext(ctx, `SELECT exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL FOR KEY SHARE`, in.TaskID).Scan(&explorationID); err != nil {
			return 0, fmt.Errorf("자산을 등록할 작업을 확인하지 못했습니다: %w", err)
		}
		if in.ExplorationID > 0 && in.ExplorationID != explorationID {
			return 0, errors.New("자산의 작업과 탐색 기록이 일치하지 않습니다")
		}
		in.ExplorationID = explorationID
	}
	var ownerKind string
	if in.OwnerNodeID > 0 {
		if in.ExplorationID <= 0 {
			return 0, errors.New("자산 출처 노드를 연결하려면 탐색 컨텍스트가 필요합니다")
		}
		if err := tx.QueryRowContext(ctx, `SELECT kind FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 FOR KEY SHARE`, in.OwnerNodeID, in.ExplorationID).Scan(&ownerKind); err != nil {
			return 0, fmt.Errorf("자산 출처는 현재 작업의 탐색 노드여야 합니다: %w", err)
		}
	}
	id, err := upsert(scoped)
	if err != nil {
		return 0, err
	}
	if in.OwnerNodeID > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO exploration_anchors(node_id,asset_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, in.OwnerNodeID, id); err != nil {
			return 0, fmt.Errorf("자산의 탐색 연결을 저장하지 못했습니다: %w", err)
		}
	}
	if in.TaskID > 0 {
		var sourceNodeID *int64
		summary := "에이전트가 insert_assets로 등록"
		if in.OwnerNodeID > 0 {
			sourceNodeID = &in.OwnerNodeID
			summary = fmt.Sprintf("탐색 노드 #%d에서 insert_assets로 등록", in.OwnerNodeID)
			if ownerKind == KindIntent {
				summary = fmt.Sprintf("Worker 의도 #%d에서 insert_assets로 등록", in.OwnerNodeID)
			}
		}
		if err := scoped.SetTaskAssetSource(in.TaskID, id, "agent", summary, sourceNodeID); err != nil {
			return 0, fmt.Errorf("자산 출처를 저장하지 못했습니다: %w", err)
		}
		if err := scoped.AddAutoScope(in.TaskID, in.Type, in.Domain, in.URL, in.IP); err != nil {
			return 0, fmt.Errorf("자산 조회 범위를 저장하지 못했습니다: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}
