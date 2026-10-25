package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

type graphStateFixture struct {
	task                                                      *Task
	store                                                     *ExplorationStore
	open, paused, blocked, running, goal, finding, findingRow int64
}

func newGraphStateFixture(t *testing.T, d *DB) graphStateFixture {
	t.Helper()
	task, store := graphMutationTask(t, d)
	f := graphStateFixture{task: task, store: store}
	for _, item := range []struct {
		id    *int64
		state string
	}{
		{&f.open, "open"}, {&f.paused, "paused"}, {&f.blocked, "blocked"}, {&f.running, "running"},
	} {
		id, err := store.AddNode(KindIntent, map[string]any{"summary": item.state}, 5, item.state, "planner", nil)
		if err != nil {
			t.Fatal(err)
		}
		*item.id = id
	}
	if err := store.SetIntentBlockedReason(f.blocked, IntentBlockedLLMQuota); err != nil {
		t.Fatal(err)
	}
	var err error
	f.goal, err = store.AddGoal(map[string]any{"text": "original goal"}, "human")
	if err != nil {
		t.Fatal(err)
	}
	f.finding, err = store.AddNode(KindFinding, map[string]any{"summary": "source finding"}, 5, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	f.findingRow, err = d.AddFinding(task.ID, f.finding, "test", "source finding", SeverityLow, "fixture", "", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

var graphStateOperations = []struct {
	name string
	run  func(graphStateFixture) error
}{
	{"set_node", func(f graphStateFixture) error { return f.store.SetNodeState(f.goal, "met") }},
	{"edit_goal", func(f graphStateFixture) error { return f.store.UpdateGoalPayload(f.goal, "edited goal", "test") }},
	{"delete_goal", func(f graphStateFixture) error { return f.store.DeleteGoal(f.goal) }},
	{"reset_running", func(f graphStateFixture) error { _, err := f.store.ResetRunningIntents(); return err }},
	{"reopen", func(f graphStateFixture) error { _, err := f.store.ReopenIntent(f.blocked); return err }},
	{"reopen_blocked", func(f graphStateFixture) error { _, err := f.store.ReopenBlockedIntents(); return err }},
	{"set_intent", func(f graphStateFixture) error { return f.store.SetIntentState(f.open, "stopped") }},
	{"compare_set", func(f graphStateFixture) error {
		_, err := f.store.CompareAndSetIntentState(f.paused, "paused", "open")
		return err
	}},
	{"soft_delete", func(f graphStateFixture) error {
		_, err := f.store.SoftDeleteIntent(f.open, "user removed")
		return err
	}},
	{"cancel", func(f graphStateFixture) error { _, err := f.store.CancelIntent(f.open); return err }},
	{"claim", func(f graphStateFixture) error { _, err := f.store.ClaimIntent(f.open, "worker"); return err }},
	{"block_reason", func(f graphStateFixture) error {
		return f.store.SetIntentBlockedReason(f.running, IntentBlockedLLMQuota)
	}},
	{"quota_recovery", func(f graphStateFixture) error {
		_, err := f.store.ReopenIntentsByBlockedReason(IntentBlockedLLMQuota)
		return err
	}},
	{"admission_rollback", func(f graphStateFixture) error { return f.store.DiscardOpenIntent(f.open) }},
	{"finding_follow_up", func(f graphStateFixture) error {
		_, _, err := f.store.AddFindingFollowUpIntent(f.findingRow, f.finding, "inspect finding", Activity{Kind: "text", Worker: "human"})
		return err
	}},
}

func TestGraphStateMutationsSerializeWithTaskCompletionAndArchive(t *testing.T) {
	d, _ := templateFixtureDB(t)
	for _, op := range graphStateOperations {
		t.Run(op.name, func(t *testing.T) {
			f := newGraphStateFixture(t, d)
			tx, err := d.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.Exec("SELECT id FROM tasks WHERE id=$1 FOR UPDATE", f.task.ID); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- op.run(f) }()
			select {
			case err := <-done:
				t.Fatalf("mutation bypassed completion task lock: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("mutation did not resume after task lock release")
			}
		})
	}
	f := newGraphStateFixture(t, d)
	if _, err := d.Exec("UPDATE tasks SET status='done' WHERE id=$1", f.task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.QueueTaskArchive(f.task.ID); err != nil {
		t.Fatal(err)
	}
	for _, op := range graphStateOperations {
		if err := op.run(f); !errors.Is(err, ErrTaskArchiveState) {
			t.Errorf("%s changed queued archive: %v", op.name, err)
		}
	}
}

func TestGraphAutomaticRecoveryDoesNotReviveTerminalTask(t *testing.T) {
	d, _ := templateFixtureDB(t)
	for _, status := range []string{"done", "failed", "timeout"} {
		t.Run(status, func(t *testing.T) {
			f := newGraphStateFixture(t, d)
			if _, err := d.Exec("UPDATE tasks SET status=$1,completed_at=now() WHERE id=$2", status, f.task.ID); err != nil {
				t.Fatal(err)
			}
			if count, err := f.store.ResetRunningIntents(); err != nil || count != 0 {
				t.Fatalf("startup reset: %d %v", count, err)
			}
			if count, err := f.store.ReopenIntentsByBlockedReason(IntentBlockedLLMQuota); err != nil || count != 0 {
				t.Fatalf("quota recovery: %d %v", count, err)
			}
			if claimed, err := f.store.ClaimIntent(f.open, "late worker"); err != nil || claimed {
				t.Fatalf("late claim: %v %v", claimed, err)
			}
			for id, state := range map[int64]string{f.open: "open", f.blocked: "blocked", f.running: "running"} {
				n, err := f.store.GetNode(id)
				if err != nil || n.State != state {
					t.Fatalf("automatic recovery changed %d: %+v %v", id, n, err)
				}
			}
			// Explicit rerun/resume remains permitted. The caller owns admission.
			if changed, err := f.store.ReopenIntent(f.blocked); err != nil || !changed {
				t.Fatalf("user rerun: %v %v", changed, err)
			}
			if changed, err := f.store.CompareAndSetIntentState(f.paused, "paused", "open"); err != nil || !changed {
				t.Fatalf("user resume: %v %v", changed, err)
			}
			if err := f.store.SetIntentState(f.running, "done"); err != nil {
				t.Fatal(err)
			}
			row, err := d.GetTask(f.task.ID)
			if err != nil || row.Status != status || row.CompletedAt == nil {
				t.Fatalf("DB mutation silently revived task: %+v %v", row, err)
			}
		})
	}
}

func TestGoalEditInvalidatesOldProofAtomicallyAndPreservesUserPayload(t *testing.T) {
	d, _ := templateFixtureDB(t)
	task, s := graphMutationTask(t, d)
	root, _ := s.OriginFactID()
	goal, err := s.AddGoal(map[string]any{"text": "original objective", "vulnclass": "old", "custom": json.RawMessage("9007199254740993")}, "human")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ProveGoal(t.Context(), goal, root, "observed proof"); err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetNode(goal)
	if err := s.UpdateGoalPayload(goal, "original objective", "old"); err != nil {
		t.Fatal(err)
	}
	same, _ := s.GetNode(goal)
	if same.State != "met" || string(same.Payload) != string(before.Payload) {
		t.Fatal("unchanged form invalidated existing proof")
	}
	trigger := fmt.Sprintf(`CREATE FUNCTION reject_goal_edit_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id=%d AND NEW.payload->>'text'='edited objective' THEN RAISE EXCEPTION 'fixture edit failure'; END IF; RETURN NEW; END $$;
CREATE TRIGGER reject_goal_edit_fixture BEFORE UPDATE ON exploration_nodes FOR EACH ROW EXECUTE FUNCTION reject_goal_edit_fixture()`, goal)
	if _, err := d.Exec(trigger); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateGoalPayload(goal, "edited objective", ""); err == nil {
		t.Fatal("failed edit reported success")
	}
	after, _ := s.GetNode(goal)
	if after.State != "met" || string(after.Payload) != string(before.Payload) || graphCount(t, d, "SELECT count(*) FROM exploration_edges WHERE dst_id=$1 AND rel='proves'", goal) != 1 {
		t.Fatal("failed edit partially removed proof")
	}
	if _, err := d.Exec("DROP TRIGGER reject_goal_edit_fixture ON exploration_nodes; DROP FUNCTION reject_goal_edit_fixture()"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateGoalPayload(goal, "edited objective", ""); err != nil {
		t.Fatal(err)
	}
	after, _ = s.GetNode(goal)
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(after.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if after.State != "open" || string(payload["custom"]) != "9007199254740993" || len(payload["proofs"]) > 0 || len(payload["completion_review"]) > 0 || len(payload["vulnclass"]) > 0 {
		t.Fatalf("edited goal payload: %s state=%s", after.Payload, after.State)
	}
	if graphCount(t, d, "SELECT count(*) FROM exploration_edges WHERE dst_id=$1 AND rel='proves'", goal) != 0 {
		t.Fatal("old proof edge remains")
	}
	if won, err := d.CompleteReviewedTask(t.Context(), task.ID); err != nil || won {
		t.Fatalf("edited objective completed: %v %v", won, err)
	}
	if err := s.UpdateGoalPayload(root, "wrong kind", ""); err == nil {
		t.Fatal("fact accepted as goal")
	}
	if err := s.UpdateGoalPayload(goal+999999, "missing", ""); err == nil {
		t.Fatal("missing goal accepted")
	}
}

func TestCompletionReviewDetectsSameIntentRerun(t *testing.T) {
	d, _ := templateFixtureDB(t)
	for _, change := range []string{"claim_only", "reopen", "resume", "set_state", "quota_recovery", "startup_recovery", "legacy_snapshot"} {
		t.Run(change, func(t *testing.T) {
			task, s := graphMutationTask(t, d)
			intent, err := s.AddIntent(map[string]any{"summary": "approved work"}, 5, nil, "planner")
			if err != nil {
				t.Fatal(err)
			}
			if change == "resume" {
				if err := s.SetIntentState(intent, "paused"); err != nil {
					t.Fatal(err)
				}
			}
			if change == "startup_recovery" {
				if _, err := s.ClaimIntent(intent, "first"); err != nil {
					t.Fatal(err)
				}
			}
			goal, err := s.AddGoal(map[string]any{"text": "reviewed objective"}, "human")
			if err != nil {
				t.Fatal(err)
			}
			root, _ := s.OriginFactID()
			if _, _, err := s.ProveGoal(t.Context(), goal, root, "verified", "remaining execution is independent"); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "claim_only":
				_, err = s.ClaimIntent(intent, "normal worker")
			case "reopen":
				err = s.SetIntentState(intent, "stopped")
				if err == nil {
					_, err = s.ReopenIntent(intent)
				}
			case "resume":
				_, err = s.CompareAndSetIntentState(intent, "paused", "open")
			case "set_state":
				err = s.SetIntentState(intent, "open")
			case "quota_recovery":
				err = s.SetIntentBlockedReason(intent, IntentBlockedLLMQuota)
				if err == nil {
					_, err = s.ReopenIntentsByBlockedReason(IntentBlockedLLMQuota)
				}
			case "startup_recovery":
				_, err = s.ResetRunningIntents()
			case "legacy_snapshot":
				_, err = d.Exec("UPDATE exploration_nodes SET payload=payload #- '{completion_review,active_intent_versions}' WHERE id=$1", goal)
			}
			if err != nil {
				t.Fatal(err)
			}
			want := change == "claim_only"
			won, err := d.CompleteReviewedTask(context.Background(), task.ID)
			if err != nil || won != want {
				t.Fatalf("completion=%v want=%v error=%v", won, want, err)
			}
			if !want {
				node, _ := s.GetNode(goal)
				if node.State != "open" {
					t.Fatal("rerun did not reopen completion review")
				}
			}
		})
	}
}
