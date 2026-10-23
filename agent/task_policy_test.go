package agent

import (
	"context"
	"database/sql"
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

type taskPolicyProbeProvider struct {
	requests []llm.CompletionRequest
	input    string
}

func (p *taskPolicyProbeProvider) Stream(_ context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		p.requests = append(p.requests, req)
		events := []llm.StreamEvent{{Type: llm.SETextDelta, Text: "done"}, {Type: llm.SEMessageDelta, StopReason: "end_turn"}}
		if len(p.requests) == 1 {
			events = []llm.StreamEvent{
				{Type: llm.SEToolUseStart, ToolID: "policy-write", ToolName: "Write"},
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
func (p *taskPolicyProbeProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	acc := llm.NewAccumulator()
	for event, err := range p.Stream(ctx, req) {
		if err != nil {
			return llm.Message{}, "", llm.Usage{}, err
		}
		acc.Add(event)
	}
	return acc.Message(), acc.StopReason, acc.Usage, nil
}

func runPolicyProbe(role string, ctx context.Context, provider llm.Provider, dir string, taskID int64, store *db.ExplorationStore, intent *db.Node, g *guard.Guard) error {
	switch role {
	case "planner":
		a := NewPlanner(provider, "fixture", dir, nil, 0, 3)
		a.SetConstraintInject(func() bool { return false })
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
		a.SetConstraintInject(func() bool { return false })
		_, _, err := a.Execute(ctx, "worker", taskID, nil, store, intent, g.Hooks(), nil, nil, nil)
		return err
	}
}

func TestTaskPolicyReadFailureStopsAllAgentsBeforeProvider(t *testing.T) {
	// A closed pool reproduces a storage failure without connecting to any server.
	pool, err := sql.Open("pgx", "postgres://fixture@127.0.0.1:1/fixture")
	if err != nil {
		t.Fatal(err)
	}
	_ = pool.Close()
	store := (&db.DB{DB: pool}).Exploration(1)
	for _, role := range []string{"planner", "mainagent", "worker"} {
		t.Run(role, func(t *testing.T) {
			p := &taskPolicyProbeProvider{}
			err := runPolicyProbe(role, t.Context(), p, t.TempDir(), 1, store, &db.Node{ID: 1}, guard.New())
			if err == nil || !strings.Contains(err.Error(), "작업 정책을 불러오지 못해") || len(p.requests) != 0 {
				t.Fatalf("policy failure proceeded to model: requests=%d error=%v", len(p.requests), err)
			}
		})
	}
}

func TestTaskAgentsPreservePresetAndApplyConfiguredGuard(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires an explicitly configured isolated ARTEX_PG_DSN")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTaskWithOptions("policy fixture", "inspect fixture", db.TaskCreateOptions{TestPreset: "observe"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = d.Exec(`DELETE FROM tasks WHERE id=$1`, task.ID)
		_, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, task.ExplorationID)
	}()
	store := d.Exploration(task.ExplorationID)
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
			p := &taskPolicyProbeProvider{input: string(input)}
			g, blocked := guard.New(), 0
			g.Hooks().On(hook.PreToolUse, func(_ context.Context, event hook.Event) hook.Result {
				if event.ToolName == "Write" {
					blocked++
					return hook.Result{Decision: "block", Message: "configured policy probe denied"}
				}
				return hook.Result{}
			})
			if err := runPolicyProbe(role, t.Context(), p, dir, task.ID, store, intent, g); err != nil {
				t.Fatal(err)
			}
			if blocked != 1 || len(p.requests) < 2 {
				t.Fatalf("guard not invoked: blocks=%d requests=%d", blocked, len(p.requests))
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("blocked tool executed: %v", err)
			}
			if !strings.Contains(strings.Join(p.requests[0].System, "\n"), "업무 데이터 생성/수정/삭제") {
				t.Fatal("custom constraint visibility disabled the required preset")
			}
			messages, _ := json.Marshal(p.requests[1].Messages)
			if !strings.Contains(string(messages), "configured policy probe denied") {
				t.Fatal("model did not receive configured block reason")
			}
		})
	}
}
