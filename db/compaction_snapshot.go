package db

import (
	"context"
	"database/sql"
	"encoding/json"
)

// CompactionSnapshot reads source content, versions, asset references and graph
// relations from the same database snapshot. A later version must never certify
// older content as complete when a Worker writes during background compression.
func (s *ExplorationStore) CompactionSnapshot() ([]*Node, []Edge, map[int64]int, error) {
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, nil, nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT `+nodeCols+`,content_version,
		COALESCE((SELECT jsonb_agg(asset_id ORDER BY asset_id) FROM exploration_anchors WHERE node_id=exploration_nodes.id),'[]'::jsonb)
		FROM exploration_nodes WHERE exploration_id=$1 ORDER BY id`, s.expID)
	if err != nil {
		return nil, nil, nil, err
	}
	var nodes []*Node
	versions := map[int64]int{}
	for rows.Next() {
		var n Node
		var version int
		var anchors []byte
		if err := rows.Scan(&n.ID, &n.Kind, &n.Payload, &n.Priority, &n.State, &n.Origin, &n.Owner, &n.BlockedReason, &n.DeleteReason, &n.CreatedAt, &version, &anchors); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		if err := json.Unmarshal(anchors, &n.Anchors); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		nodes = append(nodes, &n)
		versions[n.ID] = version
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, nil, err
	}
	rows, err = tx.Query(`SELECT src_id,rel,dst_id FROM exploration_edges WHERE exploration_id=$1`, s.expID)
	if err != nil {
		return nil, nil, nil, err
	}
	var edges []Edge
	for rows.Next() {
		var edge Edge
		if err := rows.Scan(&edge.From, &edge.Rel, &edge.To); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		edges = append(edges, edge)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, nil, err
	}
	return nodes, edges, versions, nil
}
