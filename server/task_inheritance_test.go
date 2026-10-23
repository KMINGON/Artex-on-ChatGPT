package server

import (
	"context"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestChildTaskPreservesExecutingParentAndResourceProfile(t *testing.T) {
	parent := &Task{ID: "7", ExecutionProfile: &db.ExecutionProfile{ID: "low", Version: 1, Workers: 2, BrowserLimit: 2}}
	s := &Server{m: &Manager{tasks: map[string]*Task{"7": parent, "8": {ID: "8"}}}}
	ctx := agent.WithRunInfo(context.Background(), agent.RunInfo{TaskID: 7})
	child, parentID, err := s.childTaskOptions(ctx, "8")
	if err != nil || parentID != "7" || child.ExecutionProfile == nil || child.ExecutionProfile.Workers != 2 {
		t.Fatalf("wrong parent/resources: %+v parent=%s err=%v", child, parentID, err)
	}
	child.ExecutionProfile.Workers = 99
	if parent.ExecutionProfile.Workers != 2 {
		t.Fatal("child aliases parent resources")
	}
	if _, _, err = s.childTaskOptions(context.Background(), "missing"); err == nil {
		t.Fatal("missing parent silently ignored")
	}
	plain, parentID, err := s.childTaskOptions(context.Background(), "")
	if err != nil || parentID != "" || plain.ExecutionProfile != nil {
		t.Fatalf("standalone task inherited settings: %+v %s %v", plain, parentID, err)
	}
}
