package server

import (
	"context"
	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/policy"
	"strings"
	"testing"
)

func TestChildTaskCannotReplaceExecutingParentPolicy(t *testing.T) {
	p, _ := policy.Resolve("observe")
	parent := &Task{ID: "7", Description: "fixture.local only", TestPreset: "observe", TestPolicy: p, ExecutionProfile: &db.ExecutionProfile{ID: "low", Version: 1, Workers: 2, BrowserLimit: 2}}
	otherPolicy, _ := policy.Resolve("lab")
	s := &Server{m: &Manager{tasks: map[string]*Task{"7": parent, "8": {ID: "8", TestPreset: "lab", TestPolicy: otherPolicy}}}}
	ctx := agent.WithRunInfo(context.Background(), agent.RunInfo{TaskID: 7})
	if _, _, err := s.childTaskOptions(ctx, "8", "lab"); err == nil {
		t.Fatal("child broadened its parent's preset")
	}
	child, parentID, err := s.childTaskOptions(ctx, "8", "")
	if err != nil {
		t.Fatal(err)
	}
	if parentID != "7" || child.TestPreset != "observe" || child.ExecutionProfile.Workers != 2 {
		t.Fatalf("wrong inheritance: %+v parent=%s", child, parentID)
	}
	child.TestPolicy.Constraints[0].Text = "changed child"
	child.ExecutionProfile.Workers = 99
	if parent.TestPolicy.Constraints[0].Text == "changed child" || parent.ExecutionProfile.Workers != 2 {
		t.Fatal("child aliases its parent")
	}
	if _, _, err = s.childTaskOptions(context.Background(), "missing", ""); err == nil {
		t.Fatal("missing parent silently ignored")
	}
}

func TestChildTaskInheritsLegacyScopeWithoutChoosingPreset(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	parent, err := m.CreateTask("legacy fixture", "Only https://scope.invalid:8443, no adjacent hosts", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(parent.ID, DeleteTaskOptions{}) }()
	if _, err := parent.Store.AddConstraint("deny", "Keep existing customer records unchanged", "human"); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: m}
	opts, _, err := s.childTaskOptions(context.Background(), parent.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if opts.TestPreset != "" || parent.TestPolicy != nil || opts.TestPolicy == nil {
		t.Fatal("inheritance selected a catalog preset or lost legacy scope")
	}
	child, err := m.CreateTaskWithOptions("child fixture", "inspect authentication", opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(child.ID, DeleteTaskOptions{}) }()
	saved, err := child.Store.TestPolicy()
	if err != nil {
		t.Fatal(err)
	}
	var scope, constraint bool
	for _, rule := range saved.Constraints {
		scope = scope || strings.Contains(rule.Text, parent.Goal)
		constraint = constraint || rule.Kind == "deny" && rule.Text == "Keep existing customer records unchanged"
	}
	if !scope || !constraint {
		t.Fatalf("inherited policy lost target or constraint: %+v", saved)
	}
}
