package agent

import (
	"context"
	"encoding/json"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/guard"
	"github.com/Autumn-27/norma/hook"
	"github.com/Autumn-27/norma/llm"
)

type taskGuardProbeProvider struct {
	requests []llm.CompletionRequest
	input    string
}

func (p *taskGuardProbeProvider) Stream(_ context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		p.requests = append(p.requests, req)
		events := []llm.StreamEvent{{Type: llm.SETextDelta, Text: "done"}, {Type: llm.SEMessageDelta, StopReason: "end_turn"}}
		if len(p.requests) == 1 {
			events = []llm.StreamEvent{
				{Type: llm.SEToolUseStart, ToolID: "guard-write", ToolName: "Write"},
				{Type: llm.SEToolInputJSON, Text: p.input},
				{Type: llm.SEMessageDelta, StopReason: "tool_use"},
			}
		}
		for _, event := range events {
			if !yield(event, nil) {
				return
			}
		}
	}
}
func (p *taskGuardProbeProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	acc := llm.NewAccumulator()
	for event, err := range p.Stream(ctx, req) {
		if err != nil {
			return llm.Message{}, "", llm.Usage{}, err
		}
		acc.Add(event)
	}
	return acc.Message(), acc.StopReason, acc.Usage, nil
}

func runGuardProbe(role string, ctx context.Context, provider llm.Provider, dir string, taskID int64, store *db.ExplorationStore, intent *db.Node, g *guard.Guard) error {
	switch role {
	case "planner":
		a := NewPlanner(provider, "fixture", dir, nil, 0, 3)
		a.SetConstraintInject(func() bool { return true })
		a.SetGuard(g)
		_, _, err := a.Plan(ctx, taskID, nil, store, "inspect fixture", nil, nil)
		return err
	case "mainagent":
		a := NewMainAgent(provider, "fixture", dir, nil, 0, 3)
		a.SetGuard(g)
		_, err := a.Chat(ctx, taskID, 0, nil, store, "inspect fixture", "continue", nil, nil, nil, nil, nil)
		return err
	default:
		a := NewWorker(provider, "fixture", dir, nil, 0, 3)
		a.SetConstraintInject(func() bool { return true })
		_, _, err := a.Execute(ctx, "worker", taskID, nil, store, intent, g.Hooks(), nil, nil, nil)
		return err
	}
}

func TestTaskAgentsPreserveExistingConstraintsAndConfiguredGuard(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires an explicitly configured isolated ARTEX_PG_DSN")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTaskWithOptions("guard fixture", "inspect fixture", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = d.Exec(`DELETE FROM tasks WHERE id=$1`, task.ID)
		_, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, task.ExplorationID)
	}()
	store := d.Exploration(task.ExplorationID)
	const constraint = "Keep the fixture contents unchanged."
	if _, err := store.AddConstraint("deny", constraint, "human"); err != nil {
		t.Fatal(err)
	}
	id, err := store.AddNode("intent", map[string]any{"summary": "inspect fixture"}, 0, "running", "planner", nil)
	if err != nil {
		t.Fatal(err)
	}
	intent := &db.Node{ID: id, Payload: json.RawMessage(`{"summary":"inspect fixture"}`)}
	for _, role := range []string{"planner", "mainagent", "worker"} {
		t.Run(role, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "must-not-exist.txt")
			input, _ := json.Marshal(map[string]string{"file_path": marker, "content": "unexpected execution"})
			p := &taskGuardProbeProvider{input: string(input)}
			g, blocked := guard.New(), 0
			g.Hooks().On(hook.PreToolUse, func(_ context.Context, event hook.Event) hook.Result {
				if event.ToolName == "Write" {
					blocked++
					return hook.Result{Decision: "block", Message: "configured guard probe denied"}
				}
				return hook.Result{}
			})
			if err := runGuardProbe(role, t.Context(), p, dir, task.ID, store, intent, g); err != nil {
				t.Fatal(err)
			}
			if blocked != 1 || len(p.requests) < 2 {
				t.Fatalf("guard not invoked: blocks=%d requests=%d", blocked, len(p.requests))
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("blocked tool executed: %v", err)
			}
			system := strings.Join(p.requests[0].System, "\n")
			if !strings.Contains(system, constraint) || strings.Contains(system, "【작업 프리셋:") {
				t.Fatal("existing constraints were lost or a separate preset prompt was injected")
			}
			messages, _ := json.Marshal(p.requests[1].Messages)
			if !strings.Contains(string(messages), "configured guard probe denied") {
				t.Fatal("model did not receive configured block reason")
			}
		})
	}
}
