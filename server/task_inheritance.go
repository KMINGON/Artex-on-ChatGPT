package server

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

// Keep model selection and resource settings tied to the executing parent.
func (s *Server) childTaskOptions(ctx context.Context, parentRef string) (db.TaskCreateOptions, string, error) {
	opts := db.TaskCreateOptions{}
	if ri := agent.RunInfoFrom(ctx); ri.TaskID > 0 {
		parentRef = strconv.FormatInt(ri.TaskID, 10)
	}
	if parentRef == "" {
		return opts, "", nil
	}
	parent, ok := s.m.Task(parentRef)
	if !ok {
		return opts, "", fmt.Errorf("상위 작업을 찾을 수 없습니다")
	}
	if parent.ExecutionProfile != nil {
		copy := *parent.ExecutionProfile
		opts.ExecutionProfile = &copy
	}
	return opts, parentRef, nil
}
