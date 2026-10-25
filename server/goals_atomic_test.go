package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestGoalPersistenceFailureDoesNotStartOrReportSuccess(t *testing.T) {
	if os.Getenv("ARTEX_PG_DSN") == "" {
		t.Skip("requires isolated ARTEX_PG_DSN")
	}
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatalf("isolated postgres: %v", err)
	}
	defer m.Close()
	task, err := m.CreateTask("goal persistence failure", "bounded local goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	name := fmt.Sprintf("reject_goal_spawn_%d", task.ExpID)
	query := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.exploration_id=%d AND NEW.rel='spawns' THEN RAISE EXCEPTION 'fixture goal edge failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER %s BEFORE INSERT ON exploration_edges FOR EACH ROW EXECUTE FUNCTION %s()`, name, task.ExpID, name, name)
	if _, err := m.pg.Exec(query); err != nil {
		t.Fatal(err)
	}
	defer m.pg.Exec(fmt.Sprintf(`DROP TRIGGER %s ON exploration_edges; DROP FUNCTION %s()`, name, name))
	s := &Server{m: m, engine: NewEngine(m), ctx: context.Background()}
	s.startTaskEngine(task)
	if snapshot := task.lifecycleSnapshot(); snapshot.Status != "failed" {
		t.Fatalf("goal-less task did not fail: %+v", snapshot)
	}
	if s.engine.Started(task.ID) {
		t.Fatal("started task with failed goal storage")
	}
	var goals int
	if err := m.pg.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind='goal'`, task.ExpID).Scan(&goals); err != nil || goals != 0 {
		t.Fatalf("orphan goal: %d %v", goals, err)
	}
	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/goals", strings.NewReader(`{"text":"manual goal"}`))
	req.SetPathValue("id", task.ID)
	recorder := httptest.NewRecorder()
	s.addGoal(recorder, req)
	if recorder.Code != 500 {
		t.Fatalf("HTTP goal success after failed edge: %d %s", recorder.Code, recorder.Body.String())
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	req = httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/goals", strings.NewReader(`{"text":"cancelled goal"}`)).WithContext(cancelled)
	req.SetPathValue("id", task.ID)
	recorder = httptest.NewRecorder()
	s.addGoal(recorder, req)
	if recorder.Code < 400 {
		t.Fatalf("cancelled HTTP goal saved: %d", recorder.Code)
	}
	if err := m.pg.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind='goal'`, task.ExpID).Scan(&goals); err != nil || goals != 0 {
		t.Fatalf("cancelled/failed orphan goal: %d %v", goals, err)
	}
}
