package db

import (
	"database/sql"
	"encoding/json"
)

// LatestWorkerOutput reads the latest run's complete result (or latest text when
// that run has no result), independently of trace pagination and tool volume.
// Legacy activities without run boundaries retain their result-first behavior.
func (s *ExplorationStore) LatestWorkerOutput(intentID int64) (*Activity, error) {
	return s.latestWorkerOutput(intentID, false)
}

func (s *ExplorationStore) latestWorkerOutput(intentID int64, terminalOnly bool) (*Activity, error) {
	var a Activity
	var runStartID int64
	err := s.db.QueryRow(`WITH latest_boundary AS (
		SELECT id,metadata->>'run_id' AS run_id FROM activity
		WHERE exploration_id=$1 AND node_id=$2
		  AND (metadata->>'run_start'='true' OR metadata->>'worker_preparation_start'='true')
		ORDER BY id DESC LIMIT 1
	), boundary AS (
		SELECT COALESCE((SELECT id FROM latest_boundary),0) AS id,
		       (SELECT run_id FROM latest_boundary) AS run_id
	) SELECT a.id,a.node_id,COALESCE(a.worker,''),a.kind,
		a.is_error,COALESCE(a.summary,''),COALESCE(a.detail,''),a.metadata,a.created_at
		,boundary.id
		FROM activity a JOIN exploration_nodes n ON n.id=a.node_id AND n.exploration_id=a.exploration_id
		CROSS JOIN boundary
		WHERE a.exploration_id=$1 AND a.node_id=$2 AND n.kind='intent'
		  AND a.kind IN ('result','text')
		  AND a.id>boundary.id
		  AND (boundary.id=0 OR a.metadata->>'run_id'=boundary.run_id)
		  AND NOT (n.state='blocked' AND COALESCE(n.blocked_reason,'')=$4)
		  AND (NOT $3 OR n.state IN ('done','blocked','exhausted','stopped'))
		ORDER BY CASE WHEN a.kind='result' THEN 0 ELSE 1 END,a.id DESC LIMIT 1`,
		s.expID, intentID, terminalOnly, IntentBlockedActivityStorage).Scan(&a.ID, &a.NodeID, &a.Worker, &a.Kind,
		&a.IsError, &a.Summary, &a.Detail, &a.Metadata, &a.CreatedAt, &runStartID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if runStartID > 0 {
		metadata := map[string]any{}
		_ = json.Unmarshal(a.Metadata, &metadata)
		if metadata == nil {
			metadata = map[string]any{}
		}
		metadata["run_start_activity_id"] = runStartID
		a.Metadata, _ = json.Marshal(metadata)
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
