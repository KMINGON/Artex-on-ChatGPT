package agent

import (
	"testing"

	"github.com/Autumn-27/norma/harness"
)

func TestWorkerUsesOriginalDefaultTools(t *testing.T) {
	_, task, tools, assigned, _ := graphToolFixture(t)
	intent, err := tools.ts.GetNode(assigned)
	if err != nil {
		t.Fatal(err)
	}
	provider := &taskGuardProbeProvider{input: `{"file_path":"fixture.txt","content":"fixture"}`}
	worker := NewWorker(provider, "fixture", t.TempDir(), nil, 0, 3)
	reason, _, err := worker.Execute(t.Context(), "worker", task.ID, nil, tools.ts, intent, nil, nil, nil, nil)
	if err != nil || reason != harness.ReasonCompleted || len(provider.requests) < 1 {
		t.Fatalf("worker launch: reason=%s err=%v", reason, err)
	}
	available := map[string]bool{}
	for _, tool := range provider.requests[0].Tools {
		available[tool.Name] = true
	}
	for _, name := range []string{"Read", "Write", "Edit", "LS", "Bash"} {
		if !available[name] {
			t.Errorf("original worker tool missing: %s", name)
		}
	}
	for _, name := range []string{"Grep", "Glob", "MultiEdit"} {
		if available[name] {
			t.Errorf("additional worker tool exposed by default: %s", name)
		}
	}
}
