package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestTaskStatusToolsAndHTTPAgree(t *testing.T) {
	for _, tc := range []struct {
		name, status                       string
		queued, paused, started, exhausted bool
		want                               string
	}{
		{name: "created", want: "created"},
		{name: "queued", queued: true, want: "queued"},
		{name: "paused", paused: true, started: true, want: "paused"},
		{name: "terminal wins", status: "done", queued: true, paused: true, want: "done"},
		{name: "chain exhausted remains started", status: "running", started: true, exhausted: true, want: "running"},
		{name: "running", status: "running", started: true, want: "running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newAdmissionTestServer(nil, nil)
			task := &Task{ID: "7", Status: tc.status, Queued: tc.queued, Paused: tc.paused}
			if tc.started {
				s.engine.started.Store(task.ID, true)
			}
			if tc.exhausted {
				task.setLLMState(nil, nil, nil, 0, "chain_exhausted", "")
			}
			if got := s.deriveTaskStatus(task); got != tc.want {
				t.Fatalf("tool status=%s want %s", got, tc.want)
			}
			if got := s.resolvedTaskStatus(task); got != tc.want {
				t.Fatalf("HTTP status=%s want %s", got, tc.want)
			}
		})
	}
}

func stateGraphManager(t *testing.T) *Manager {
	t.Helper()
	if os.Getenv("ARTEX_PG_DSN") == "" {
		t.Skip("requires an explicitly configured isolated ARTEX_PG_DSN")
	}
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func TestUnlimitedTaskStampsFirstActualRun(t *testing.T) {
	m := stateGraphManager(t)
	task, err := m.CreateTask("unlimited timestamp", "", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	e := NewEngine(m)
	e.stampFirstRun(task)
	state := task.lifecycleSnapshot()
	if state.FirstRunAt <= 0 || state.DeadlineAt != 0 {
		t.Fatalf("unlimited clock=%+v", state)
	}
	id, _ := strconv.ParseInt(task.ID, 10, 64)
	stored, err := m.pg.GetTask(id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.FirstRunAt == nil || stored.DeadlineAt != nil {
		t.Fatalf("unlimited DB clock=%+v", stored)
	}
	first := *stored.FirstRunAt
	// A fresh process must preserve the persisted first run, including no-timeout tasks.
	NewEngine(m).stampFirstRun(task)
	stored, err = m.pg.GetTask(id)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.FirstRunAt.Equal(first) {
		t.Fatal("restart reset first_run_at")
	}
	dto := taskDTO(task, "running")
	if dto.FirstRunUnix != first.Unix() || dto.FirstRunAt == "" {
		t.Fatalf("missing HTTP start stamp %+v", dto)
	}
}

func TestGraphHTTPPageMetadataAndConnectedNodes(t *testing.T) {
	m := stateGraphManager(t)
	task, err := m.CreateTask("HTTP graph", "", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	root, err := task.Store.OriginFactID()
	if err != nil {
		t.Fatal(err)
	}
	child, err := task.Store.AddIntent(map[string]any{"summary": "child"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Store.Link(root, db.RelDerivedFrom, child); err != nil {
		t.Fatal(err)
	}
	s := newAdmissionTestServer(m, nil)
	req := httptest.NewRequest("GET", "/api/exploration/graph?task="+task.ID+"&limit=1", nil)
	rec := httptest.NewRecorder()
	s.explorationGraph(rec, req)
	if rec.Code != 200 {
		t.Fatalf("HTTP %d %s", rec.Code, rec.Body.String())
	}
	var page struct {
		Nodes      []TaskNodeDTO `json:"nodes"`
		Edges      []EdgeDTO     `json:"edges"`
		TotalNodes int           `json:"total_nodes"`
		HasMore    bool          `json:"has_more"`
		NextBefore string        `json:"next_before"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Nodes) != 2 || len(page.Edges) != 1 || page.TotalNodes != 2 || !page.HasMore || page.NextBefore != strconv.FormatInt(child, 10) {
		t.Fatalf("page=%+v", page)
	}
}

func TestRuntimeSeparatesTaskWorkerSnapshotFromCurrentGlobalLimits(t *testing.T) {
	m := stateGraphManager(t)
	task, err := m.CreateTask("runtime snapshot", "", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	task.ExecutionProfile = &db.ExecutionProfile{ID: "medium", Version: 1, Workers: 5, BrowserLimit: 4}
	previousWorkers := m.Workers()
	defer m.SetWorkers(previousWorkers)
	if err := m.SetWorkers(9); err != nil {
		t.Fatal(err)
	}
	previousLimit, _, err := m.pg.GetSetting(settingBrowserLimit)
	if err != nil {
		t.Fatal(err)
	}
	defer m.pg.SetSetting(settingBrowserLimit, previousLimit)
	if err := m.pg.SetSetting(settingBrowserLimit, "8"); err != nil {
		t.Fatal(err)
	}
	s := newAdmissionTestServer(m, nil)
	runtime := s.taskRuntime(task)
	if runtime.Workers != 5 || runtime.WorkerSource != "task_snapshot" || runtime.BrowserLimit != 8 {
		t.Fatalf("mixed creation and current settings: %+v", runtime)
	}
	task.ExecutionProfile = nil
	runtime = s.taskRuntime(task)
	if runtime.Workers != 9 || runtime.WorkerSource != "global" {
		t.Fatalf("legacy global workers=%+v", runtime)
	}
}

func TestSeedAndFallbackIntentsPersistRootLineage(t *testing.T) {
	m := stateGraphManager(t)
	task, err := m.CreateTask("atomic entry points", "", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	s := newAdmissionTestServer(m, nil)
	s.seedFirstIntent(task)
	s.fallbackChat(task, "의도 두 번째 작업")
	page, err := task.Store.GraphPageWithSources(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if page.TotalNodes != 3 || page.TotalEdges != 2 {
		t.Fatalf("entry point lineage nodes=%d edges=%d", page.TotalNodes, page.TotalEdges)
	}
	root, err := task.Store.OriginFactID()
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range page.Edges {
		if edge.From != root || edge.Rel != db.RelDerivedFrom {
			t.Fatalf("unrooted edge %+v", edge)
		}
	}
	// A stale task handle cannot claim success after its backing task was deleted.
	if _, err := m.DeleteTask(task.ID, DeleteTaskOptions{}); err != nil {
		t.Fatal(err)
	}
	if response := s.fallbackChat(task, "의도 삭제 이후"); strings.Contains(response, trMsg(fallbackIntentInjected)) {
		t.Fatal("reported success after failed persistence")
	}
	if response := s.fallbackChat(task, "힌트 삭제 이후"); strings.Contains(response, trMsg(fallbackHintRecorded)) {
		t.Fatal("reported hint success after failed persistence")
	}
}
