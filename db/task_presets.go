package db

import (
	"encoding/json"
	"fmt"
	"github.com/Autumn-27/artex/policy"
)

// ExecutionProfile is immutable per task. BrowserLimit describes the creation-time
// global pool; it does not reserve a private pool or override a later global limit.
type ExecutionProfile struct {
	ID           string `json:"id"`
	Version      int    `json:"version"`
	Workers      int    `json:"workers"`
	BrowserLimit int    `json:"browser_limit"`
}

func taskPresetSnapshot(opts TaskCreateOptions) (*policy.TaskPreset, []byte, []byte, error) {
	preset := opts.TestPolicy
	if preset == nil {
		var err error
		preset, err = policy.Resolve(opts.TestPreset)
		if err != nil {
			return nil, nil, nil, err
		}
	} else if preset.ID != opts.TestPreset || preset.Version < 1 {
		return nil, nil, nil, fmt.Errorf("작업 프리셋 스냅샷이 올바르지 않습니다")
	}
	if opts.ExecutionProfile != nil && (opts.ExecutionProfile.Workers < 1 || opts.ExecutionProfile.BrowserLimit < 1) {
		return nil, nil, nil, fmt.Errorf("실행 자원 스냅샷이 올바르지 않습니다")
	}
	p, err := json.Marshal(preset)
	if err != nil {
		return nil, nil, nil, err
	}
	e, err := json.Marshal(opts.ExecutionProfile)
	return preset, p, e, err
}

func (s *ExplorationStore) TestPolicy() (*policy.TaskPreset, error) {
	var raw []byte
	err := s.db.QueryRow(`SELECT test_policy FROM tasks WHERE exploration_id=$1 AND deleted_at IS NULL`, s.expID).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var preset *policy.TaskPreset
	if err = json.Unmarshal(raw, &preset); err != nil {
		return nil, err
	}
	return preset, nil
}
