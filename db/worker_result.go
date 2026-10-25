package db

import "database/sql"

// LatestWorkerOutput reads the complete latest result (or latest text when no
// result exists), independently of trace pagination and tool output volume.
func (s *ExplorationStore) LatestWorkerOutput(intentID int64) (*Activity, error) {
	return s.latestWorkerOutput(intentID, false)
}

func (s *ExplorationStore) latestWorkerOutput(intentID int64, terminalOnly bool) (*Activity, error) {
	var a Activity
	err := s.db.QueryRow(`SELECT a.id,a.node_id,COALESCE(a.worker,''),a.kind,
		a.is_error,COALESCE(a.summary,''),COALESCE(a.detail,''),a.metadata,a.created_at
		FROM activity a JOIN exploration_nodes n ON n.id=a.node_id AND n.exploration_id=a.exploration_id
		WHERE a.exploration_id=$1 AND a.node_id=$2 AND n.kind='intent'
		  AND a.kind IN ('result','text')
		  AND (NOT $3 OR n.state IN ('done','blocked','exhausted','stopped'))
		ORDER BY CASE WHEN a.kind='result' THEN 0 ELSE 1 END,a.id DESC LIMIT 1`,
		s.expID, intentID, terminalOnly).Scan(&a.ID, &a.NodeID, &a.Worker, &a.Kind,
		&a.IsError, &a.Summary, &a.Detail, &a.Metadata, &a.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// LatestWorkerOutputWithSources preserves the direct-source boundary and only
// exposes completed source intents. State and output are read in one query.
func (s *ExplorationStore) LatestWorkerOutputWithSources(intentID int64) (*Activity, error) {
	node, err := s.GetNode(intentID)
	if err != nil {
		return nil, err
	}
	if node != nil {
		return s.LatestWorkerOutput(intentID)
	}
	sources, err := s.DirectSourceStores()
	if err != nil {
		return nil, err
	}
	for _, source := range sources {
		output, err := source.Store.latestWorkerOutput(intentID, true)
		if err != nil {
			return nil, err
		}
		if output != nil {
			output.SourceTaskID = source.Task.TaskID
			output.Inherited = true
			return output, nil
		}
	}
	return nil, nil
}
