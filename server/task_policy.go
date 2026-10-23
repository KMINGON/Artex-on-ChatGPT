package server

import (
	"context"
	"fmt"
	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/policy"
	"strconv"
)

// The executing task is authoritative. A model cannot drop or replace its parent
// in spawn_task arguments to obtain a broader test preset.
func (s *Server) childTaskOptions(ctx context.Context, parentRef, requested string) (db.TaskCreateOptions, string, error) {
	opts := db.TaskCreateOptions{TestPreset: requested}
	if ri := agent.RunInfoFrom(ctx); ri.TaskID > 0 {
		parentRef = strconv.FormatInt(ri.TaskID, 10)
	}
	if parentRef == "" {
		_, err := policy.Resolve(requested)
		return opts, "", err
	}
	parent, ok := s.m.Task(parentRef)
	if !ok {
		return opts, "", fmt.Errorf("상위 작업을 찾을 수 없습니다")
	}
	if requested != "" && requested != parent.TestPreset {
		return opts, "", fmt.Errorf("하위 작업은 상위 작업의 테스트 프리셋을 상속합니다")
	}
	opts.TestPreset = parent.TestPreset
	if parent.TestPolicy != nil {
		copy := *parent.TestPolicy
		copy.Constraints = append([]policy.Constraint(nil), parent.TestPolicy.Constraints...)
		opts.TestPolicy = &copy
	} else {
		// Legacy tasks can have a manually declared scope without a catalog preset.
		// Preserve that scope without choosing a new preset on the user's behalf.
		opts.TestPolicy = &policy.TaskPreset{ID: parent.TestPreset, Version: 1, Name: "상속된 작업 범위", NameEN: "Inherited task scope"}
	}
	// The title is often abbreviated; the goal carries the actual target and scope.
	// Keep both, independently of read-only graph inheritance and preset selection.
	opts.TestPolicy.Constraints = append(opts.TestPolicy.Constraints, policy.Constraint{Kind: "allow", Text: "상위 작업의 승인 대상·범위를 유지하세요: " + parent.Description + "\n상위 작업 목표: " + parent.Goal})
	if parent.Store != nil {
		rows, err := parent.Store.ListConstraints()
		if err != nil {
			return opts, "", err
		}
		for _, row := range rows {
			opts.TestPolicy.Constraints = append(opts.TestPolicy.Constraints, policy.Constraint{Kind: row.Kind, Text: row.Text})
		}
	}
	if parent.ExecutionProfile != nil {
		copy := *parent.ExecutionProfile
		opts.ExecutionProfile = &copy
	}
	return opts, parentRef, nil
}
