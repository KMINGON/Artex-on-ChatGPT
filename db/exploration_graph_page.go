package db

import (
	"context"
	"database/sql"
)

// GraphPage includes a newest-first node page and its visible immediate neighbours.
// The cursor covers only the page, so an old neighbour cannot skip intervening history.
type GraphPage struct {
	Nodes      []*Node
	Edges      []Edge
	TotalNodes int
	TotalEdges int
	PageNodes  int
	NextBefore int64
	HasMore    bool
}

const visibleGraphCTE = `WITH graph_context AS (
  SELECT $1::bigint AS exploration_id, 0::bigint AS source_task_id
  UNION ALL
  SELECT source.exploration_id, source.id
  FROM tasks owner
  JOIN task_relations relation ON relation.task_id=owner.id
  JOIN tasks source ON source.id=relation.source_task_id AND source.deleted_at IS NULL
  WHERE owner.exploration_id=$1 AND owner.deleted_at IS NULL
), visible_nodes AS (
  SELECT n.*, c.source_task_id
  FROM exploration_nodes n JOIN graph_context c USING (exploration_id)
  WHERE c.source_task_id=0 OR n.kind<>'intent' OR n.state IN ('done','blocked','exhausted','stopped')
), visible_edges AS (
  SELECT DISTINCT e.src_id, e.rel, e.dst_id
  FROM exploration_edges e
  JOIN graph_context c ON c.exploration_id=e.exploration_id
  JOIN visible_nodes src ON src.id=e.src_id
  JOIN visible_nodes dst ON dst.id=e.dst_id
)`

func scanGraphNodes(rows *sql.Rows) ([]*Node, error) {
	defer rows.Close()
	out := []*Node{}
	for rows.Next() {
		n := new(Node)
		if err := rows.Scan(&n.ID, &n.Kind, &n.Payload, &n.Priority, &n.State, &n.Origin,
			&n.Owner, &n.BlockedReason, &n.DeleteReason, &n.CreatedAt, &n.SourceTaskID); err != nil {
			return nil, err
		}
		n.Inherited = n.SourceTaskID > 0
		out = append(out, n)
	}
	return out, rows.Err()
}

// GraphPageWithSources never returns dangling edges or a directly related task's
// live frontier. Counts, node page and edge neighbours share one read snapshot.
func (s *ExplorationStore) GraphPageWithSources(ctx context.Context, before int64, limit int) (GraphPage, error) {
	if limit <= 0 {
		limit = 2000
	}
	limit = min(limit, 2000)
	page := GraphPage{Nodes: []*Node{}, Edges: []Edge{}}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, visibleGraphCTE+`
SELECT (SELECT count(*) FROM visible_nodes), (SELECT count(*) FROM visible_edges)`, s.expID).
		Scan(&page.TotalNodes, &page.TotalEdges); err != nil {
		return page, err
	}
	rows, err := tx.QueryContext(ctx, visibleGraphCTE+`
SELECT `+nodeCols+`, source_task_id FROM visible_nodes
WHERE ($2::bigint=0 OR id<$2) ORDER BY id DESC LIMIT $3`, s.expID, before, limit+1)
	if err != nil {
		return page, err
	}
	nodes, err := scanGraphNodes(rows)
	if err != nil {
		return page, err
	}
	page.HasMore = len(nodes) > limit
	if page.HasMore {
		nodes = nodes[:limit]
	}
	page.PageNodes = len(nodes)
	if len(nodes) == 0 {
		return page, tx.Commit()
	}
	page.NextBefore = nodes[len(nodes)-1].ID
	ids := make([]int64, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	rows, err = tx.QueryContext(ctx, visibleGraphCTE+`
SELECT src_id,rel,dst_id FROM visible_edges
WHERE src_id=ANY($2::bigint[]) OR dst_id=ANY($2::bigint[])
ORDER BY src_id,rel,dst_id`, s.expID, ids)
	if err != nil {
		return page, err
	}
	neighbours := map[int64]bool{}
	for rows.Next() {
		var edge Edge
		if err := rows.Scan(&edge.From, &edge.Rel, &edge.To); err != nil {
			rows.Close()
			return page, err
		}
		page.Edges = append(page.Edges, edge)
		neighbours[edge.From], neighbours[edge.To] = true, true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	for _, n := range nodes {
		delete(neighbours, n.ID)
	}
	page.Nodes = nodes
	if len(neighbours) > 0 {
		ids = ids[:0]
		for id := range neighbours {
			ids = append(ids, id)
		}
		rows, err = tx.QueryContext(ctx, visibleGraphCTE+`
SELECT `+nodeCols+`,source_task_id FROM visible_nodes WHERE id=ANY($2::bigint[]) ORDER BY id DESC`, s.expID, ids)
		if err != nil {
			return page, err
		}
		refs, err := scanGraphNodes(rows)
		if err != nil {
			return page, err
		}
		page.Nodes = append(page.Nodes, refs...)
	}
	return page, tx.Commit()
}
