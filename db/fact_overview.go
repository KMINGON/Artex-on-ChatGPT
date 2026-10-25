package db

// AnalysisFactOverview keeps automatic HTTP response records out of the short
// planning window while leaving their facts, anchors, and full read APIs intact.
func (s *ExplorationStore) AnalysisFactOverview(limit int) ([]*Node, int, int, error) {
	if limit <= 0 {
		limit = 20
	}
	var analysis, observations int
	if err := s.db.QueryRow(`SELECT count(*) FILTER (WHERE state<>'origin' AND payload->>'observation_source' IS DISTINCT FROM 'http_response'),
		count(*) FILTER (WHERE payload->>'observation_source'='http_response')
		FROM exploration_nodes WHERE exploration_id=$1 AND kind='fact'`, s.expID).Scan(&analysis, &observations); err != nil {
		return nil, 0, 0, err
	}
	rows, err := s.db.Query(`SELECT `+nodeCols+` FROM exploration_nodes
		WHERE exploration_id=$1 AND kind='fact' AND state<>'origin'
		AND payload->>'observation_source' IS DISTINCT FROM 'http_response'
		ORDER BY id DESC LIMIT $2`, s.expID, limit)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	nodes, err := scanNodes(rows)
	return nodes, analysis, observations, err
}
