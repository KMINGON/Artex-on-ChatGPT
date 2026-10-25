package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

func TestWorkerArtifactsCanBeHandedToAnotherWorkerAndPlanner(t *testing.T) {
	base := t.TempDir()
	taskRoot := ensureRunDir(base, 1, 0)
	workerA := ensureRunDir(base, 1, 11)
	workerB := ensureRunDir(base, 1, 12)
	shared := filepath.Join(taskRoot, "shared", "coverage.txt")
	for _, path := range []string{filepath.Join(workerA, "evidence.txt"), shared} {
		input, _ := json.Marshal(map[string]string{"file_path": path, "content": "fixture evidence preserved"})
		result, err := actool.NewWrite().Call(t.Context(), input, &actool.ToolContext{WorkingDir: workerA})
		if err != nil || result.IsError {
			t.Fatalf("write: %v, %s", err, result.Flatten())
		}
		for _, cwd := range []string{workerB, taskRoot} {
			input, _ := json.Marshal(map[string]string{"file_path": path})
			result, err := actool.NewRead().Call(t.Context(), input, &actool.ToolContext{WorkingDir: cwd})
			if err != nil || result.IsError || !strings.Contains(result.Flatten(), "fixture evidence preserved") {
				t.Fatalf("handoff to %s: %v, %s", cwd, err, result.Flatten())
			}
		}
	}
	if _, err := os.Stat(filepath.Join(workerB, "evidence.txt")); !os.IsNotExist(err) {
		t.Fatalf("worker-local evidence leaked into another cwd: %v", err)
	}
	for role, prompt := range map[string]string{"worker": workerSystem("", "", base, taskRoot, workerB), "planner": plannerSystem("verify", base, taskRoot)} {
		if !strings.Contains(prompt, "task_root="+taskRoot) || !strings.Contains(prompt, "shared_dir="+filepath.Join(taskRoot, "shared")) {
			t.Fatalf("%s missing concrete handoff directory", role)
		}
		if strings.Contains(prompt, "다른 절대 경로에 저장하지 마세요") {
			t.Fatalf("%s disallows its own shared absolute path", role)
		}
	}
}

func TestPlannerReloadsTodosAndColdGraphKeepsOrigin(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires an explicitly configured isolated ARTEX_PG_DSN")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTaskWithOptions("persistent planner", "retain frontier", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.DeleteTask(task.ID) }()
	store := d.Exploration(task.ExplorationID)
	first, err := plannerTodoStore(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := plannerTodoStore(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	result, err := first.Tool().Call(t.Context(), []byte(`{"todos":[{"content":"preserve pending verification","status":"pending"}]}`), nil)
	if err != nil || result.IsError {
		t.Fatalf("first save: %v, %s", err, result.Flatten())
	}
	result, err = stale.Tool().Call(t.Context(), []byte(`{"todos":[]}`), nil)
	if err != nil || !result.IsError {
		t.Fatalf("late execution overwrote newer plan: %v, %s", err, result.Flatten())
	}
	reloaded, err := plannerTodoStore(t.Context(), store)
	if err != nil || len(reloaded.List()) != 1 || !strings.Contains(renderPlannerTodos(reloaded.List()), "preserve pending verification") {
		t.Fatalf("reloaded plan: %v, %+v", err, reloaded)
	}
	origin, err := store.OriginFactID()
	if err != nil {
		t.Fatal(err)
	}
	graph, _, err := loadColdGraph(store)
	if err != nil {
		t.Fatal(err)
	}
	if !graph.hotSet()[origin] {
		t.Fatal("task origin became cold")
	}
}

func TestCompressionInputIncludesBoundedDetailEvidenceAndFollowUps(t *testing.T) {
	nodes := map[int64]*db.Node{}
	var light []cgNode
	var members []int64
	for id := int64(1); id <= 40; id++ {
		payload, _ := json.Marshal(map[string]any{"summary": "short observation", "detail": "START-" + strings.Repeat("가", 4000) + "-END", "evidence": "request evidence", "follow_ups": []map[string]any{{"id": 1, "summary": "next clue", "state": "resolved"}}})
		nodes[id] = &db.Node{ID: id, Kind: db.KindFact, State: "confirmed", Payload: payload}
		light = append(light, fact(id))
		members = append(members, id)
	}
	input := buildCompressionInput(newColdGraph(light, nil), block{Members: members}, nodes)
	if len([]rune(input)) > 35_000 {
		t.Fatalf("bounded details overflowed: %d runes", len([]rune(input)))
	}
	if strings.Count(input, "START-") != 40 || strings.Count(input, "-END") != 40 {
		t.Fatal("late nodes or detail suffix lost from bounded compression input")
	}
	for _, want := range []string{"evidence: request evidence", "follow_ups:", "next clue", "resolved", "node_detail(40)"} {
		if !strings.Contains(input, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if !strings.Contains(string(nodes[40].Payload), strings.Repeat("가", 4000)) {
		t.Fatal("compression mutated original payload")
	}
}
