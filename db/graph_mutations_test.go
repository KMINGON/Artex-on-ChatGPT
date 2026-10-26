package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func graphMutationTask(t *testing.T, d *DB, sources ...int64) (*Task, *ExplorationStore) {
	t.Helper()
	task, err := d.CreateTaskWithOptions("graph mutation fixture", "verify provenance", TaskCreateOptions{SourceTaskIDs: sources})
	if err != nil {
		t.Fatal(err)
	}
	return task, d.Exploration(task.ExplorationID)
}

func graphCount(t *testing.T, d *DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := d.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Fail after node creation: the caller must never report a persisted orphan.
func TestGraphMutationsRollbackMandatoryEdges(t *testing.T) {
	d, _ := templateFixtureDB(t)
	_, s := graphMutationTask(t, d)
	ctx := context.Background()
	intent, err := s.AddIntentWithParents(ctx, map[string]any{"summary": "source"}, 5, nil, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	fact, err := s.AddFactWithIntent(ctx, map[string]any{"summary": "proof"}, nil, "worker", intent)
	if err != nil {
		t.Fatal(err)
	}
	goal, err := s.AddGoal(map[string]any{"text": "goal", "custom": "preserve"}, "human")
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Exec(`CREATE FUNCTION reject_graph_edge_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture edge failure'; END $$;
 CREATE TRIGGER reject_graph_edge_fixture BEFORE INSERT ON exploration_edges FOR EACH ROW EXECUTE FUNCTION reject_graph_edge_fixture()`)
	if err != nil {
		t.Fatal(err)
	}
	before := graphCount(t, d, `SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1`, s.ID())
	if id, err := s.AddIntentWithParents(ctx, map[string]any{"summary": "failed"}, 5, nil, []int64{fact}, "planner"); err == nil || id != 0 {
		t.Fatalf("intent id=%d err=%v", id, err)
	}
	if id, err := s.AddFactWithIntent(ctx, map[string]any{"summary": "failed"}, nil, "worker", intent); err == nil || id != 0 {
		t.Fatalf("fact id=%d err=%v", id, err)
	}
	if err := s.SetIntentState(intent, "done"); err != nil {
		t.Fatal(err)
	}
	if all, _, err := s.ProveGoal(ctx, goal, fact, "failure must not set met"); err == nil || all {
		t.Fatalf("proof all=%v err=%v", all, err)
	}
	if got := graphCount(t, d, `SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1`, s.ID()); got != before {
		t.Fatalf("orphan rows: before %d after %d", before, got)
	}
	node, err := s.GetNode(goal)
	if err != nil || node.State == "met" || strings.Contains(string(node.Payload), "proofs") {
		t.Fatalf("proof partially committed: %+v, %v", node, err)
	}
	if _, err := d.Exec(`DROP TRIGGER reject_graph_edge_fixture ON exploration_edges; DROP FUNCTION reject_graph_edge_fixture()`); err != nil {
		t.Fatal(err)
	}
	all, total, err := s.ProveGoal(ctx, goal, fact, "observed response")
	if err != nil || !all || total != 1 {
		t.Fatalf("proof: all=%v total=%d err=%v", all, total, err)
	}
	if _, _, err := s.ProveGoal(ctx, goal, fact, "refined reason"); err != nil {
		t.Fatal(err)
	}
	node, _ = s.GetNode(goal)
	var payload struct {
		Custom string      `json:"custom"`
		Proofs []GoalProof `json:"proofs"`
	}
	if err := json.Unmarshal(node.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Custom != "preserve" || len(payload.Proofs) != 1 || payload.Proofs[0].Reason != "refined reason" || payload.Proofs[0].EvidenceID != fact || payload.Proofs[0].ProvedAt.IsZero() {
		t.Fatalf("lost/doubled proof payload: %+v", payload)
	}
	if n := graphCount(t, d, `SELECT count(*) FROM exploration_edges WHERE exploration_id=$1 AND rel='proves'`, s.ID()); n != 1 {
		t.Fatalf("duplicate proof edges: %d", n)
	}
}

func TestGraphMutationsDirectSourcesAndOwnership(t *testing.T) {
	d, _ := templateFixtureDB(t)
	deep, deepStore := graphMutationTask(t, d)
	source, sourceStore := graphMutationTask(t, d, deep.ID)
	_, s := graphMutationTask(t, d, source.ID)
	ctx := context.Background()
	sourceFact, _ := sourceStore.OriginFactID()
	deepFact, _ := deepStore.OriginFactID()
	intent, err := s.AddIntentWithParents(ctx, map[string]any{"summary": "source-backed"}, 5, nil, []int64{sourceFact}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if id, err := s.AddIntentWithParents(ctx, map[string]any{"summary": "indirect"}, 5, nil, []int64{deepFact}, "planner"); err == nil || id != 0 {
		t.Fatalf("indirect source accepted: %d %v", id, err)
	}
	sourceIntent, err := sourceStore.AddIntent(map[string]any{"summary": "foreign worker"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if id, err := s.AddFactWithIntent(ctx, map[string]any{"summary": "wrong worker"}, nil, "worker", sourceIntent); err == nil || id != 0 {
		t.Fatalf("source write accepted: %d %v", id, err)
	}
	fact, err := s.AddFactWithIntent(ctx, map[string]any{"summary": "own worker"}, nil, "worker", intent)
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := s.OutcomeIDs(intent)
	if err != nil || len(outputs) != 1 || outputs[0] != fact {
		t.Fatalf("outcomes: %v %v", outputs, err)
	}
	goal, err := s.AddGoal(map[string]any{"text": "source-backed goal"}, "human")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ProveGoal(ctx, goal, deepFact, "indirect"); err == nil {
		t.Fatal("accepted indirect proof")
	}
	if err := s.SetIntentState(intent, "done"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ProveGoal(ctx, goal, sourceFact, "direct source evidence"); err != nil {
		t.Fatal(err)
	}
	n, _ := sourceStore.GetNode(sourceFact)
	if n.State != "origin" {
		t.Fatal("source node changed")
	}
}

func TestGraphProofsConcurrentAndCancelled(t *testing.T) {
	d, _ := templateFixtureDB(t)
	_, s := graphMutationTask(t, d)
	proof, _ := s.OriginFactID()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		goal, err := s.AddGoal(map[string]any{"text": fmt.Sprint(i)}, "human")
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := s.ProveGoal(context.Background(), goal, proof, "concurrent proof")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := graphCount(t, d, `SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind='goal' AND state='met'`, s.ID()); n != 2 {
		t.Fatalf("met=%d", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if id, err := s.AddIntentWithParents(ctx, map[string]any{"summary": "cancelled"}, 5, nil, nil, "planner"); err == nil || id != 0 {
		t.Fatalf("cancelled write: %d %v", id, err)
	}
}

func TestGraphMutationsRejectArchivedTaskWrites(t *testing.T) {
	d, _ := templateFixtureDB(t)
	task, s := graphMutationTask(t, d)
	intent, err := s.AddIntent(map[string]any{"summary": "worker"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	goal, err := s.AddGoal(map[string]any{"text": "goal"}, "human")
	if err != nil {
		t.Fatal(err)
	}
	root, _ := s.OriginFactID()
	if _, err := d.Exec(`UPDATE tasks SET status='done',paused=true WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.QueueTaskArchive(task.ID); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.AddFactWithIntent(ctx, map[string]any{"summary": "late result"}, nil, "worker", intent); err == nil {
		t.Fatal("late fact changed archive snapshot")
	}
	if _, err := s.AddIntentWithParents(ctx, map[string]any{"summary": "late plan"}, 5, nil, nil, "planner"); err == nil {
		t.Fatal("late intent changed archive snapshot")
	}
	if _, _, err := s.ProveGoal(ctx, goal, root, "late proof"); err == nil {
		t.Fatal("late proof changed archive snapshot")
	}
}

func TestGoalCreationAtomicOriginAndCancellation(t *testing.T) {
	d, _ := templateFixtureDB(t)
	_, s := graphMutationTask(t, d)
	ctx := context.Background()
	root, err := s.OriginFactID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`CREATE FUNCTION reject_goal_spawn_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.rel='spawns' THEN RAISE EXCEPTION 'fixture goal edge failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_goal_spawn_fixture BEFORE INSERT ON exploration_edges FOR EACH ROW EXECUTE FUNCTION reject_goal_spawn_fixture()`); err != nil {
		t.Fatal(err)
	}
	if id, err := s.AddGoalWithOrigin(ctx, map[string]any{"text": "must roll back"}, "human"); err == nil || id != 0 {
		t.Fatalf("goal partial success: %d %v", id, err)
	}
	if n := graphCount(t, d, `SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind='goal'`, s.ID()); n != 0 {
		t.Fatal("goal orphan survived failed spawns")
	}
	if _, err := d.Exec(`DROP TRIGGER reject_goal_spawn_fixture ON exploration_edges; DROP FUNCTION reject_goal_spawn_fixture()`); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if id, err := s.AddGoalWithOrigin(cancelled, map[string]any{"text": "cancelled"}, "human"); err == nil || id != 0 {
		t.Fatalf("cancelled goal saved %d %v", id, err)
	}
	goal, err := s.AddGoal(map[string]any{"text": "wrapper also atomic", "vulnclass": "fixture"}, "goals")
	if err != nil {
		t.Fatal(err)
	}
	if n := graphCount(t, d, `SELECT count(*) FROM exploration_edges WHERE exploration_id=$1 AND src_id=$2 AND rel='spawns' AND dst_id=$3`, s.ID(), root, goal); n != 1 {
		t.Fatalf("missing origin link %d", n)
	}
	standalone, err := d.CreateExploration("legacy standalone", "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exploration(standalone).AddGoalWithOrigin(ctx, map[string]any{"text": "no task root"}, "human"); err != nil {
		t.Fatal("standalone exploration compatibility", err)
	}
}
