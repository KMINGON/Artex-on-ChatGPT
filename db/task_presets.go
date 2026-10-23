package db

import (
	"encoding/json"
	"fmt"
)

// ExecutionProfile is immutable per task. BrowserLimit describes the creation-time
// global pool; it does not reserve a private pool or override a later global limit.
type ExecutionProfile struct {
	ID           string `json:"id"`
	Version      int    `json:"version"`
	Workers      int    `json:"workers"`
	BrowserLimit int    `json:"browser_limit"`
}

func executionProfileSnapshot(profile *ExecutionProfile) ([]byte, error) {
	if profile != nil && (profile.Workers < 1 || profile.BrowserLimit < 1) {
		return nil, fmt.Errorf("실행 자원 스냅샷이 올바르지 않습니다")
	}
	return json.Marshal(profile)
}
