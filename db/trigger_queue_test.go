package db

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func triggerQueueDB(t *testing.T) (*DB, *AgentTrigger) {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	key := fmt.Sprintf("trigger_regression_%d", time.Now().UnixNano())
	if _, err := d.Exec(`INSERT INTO agents(key,name,role,builtin,enabled) VALUES($1,$1,'worker',false,true)`, key); err != nil {
		t.Fatal(err)
	}
	tr, err := d.CreateTrigger(&AgentTrigger{AgentKey: key, Enabled: true, OnTaskTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		d.Exec(`DELETE FROM agent_triggers WHERE id=$1`, tr.ID)
		d.Exec(`DELETE FROM conversations WHERE agent_key=$1`, key)
		d.Exec(`DELETE FROM agents WHERE key=$1`, key)
	})
	return d, tr
}

func TestTriggerDeliveryAtomicCursorRestartAndClaim(t *testing.T) {
	d, tr := triggerQueueDB(t)
	key := tr.AgentKey + "-cursor"
	t.Cleanup(func() { d.Exec(`DELETE FROM scheduler_state WHERE key=$1`, key) })
	v := TriggerDelivery{TriggerID: tr.ID, EventKey: "finding:1", AgentKey: tr.AgentKey, Title: "fixture", Message: "saved request"}
	bad := v
	bad.TriggerID = -1
	bad.EventKey = "bad"
	if err := d.CommitTriggerBatch(TriggerBatch{Deliveries: []TriggerDelivery{v, bad}, StateKey: key, StateValue: "1"}); err == nil {
		t.Fatal("invalid delivery committed")
	}
	if value, _ := d.GetSchedState(key); value != "" {
		t.Fatal("cursor advanced without pending work")
	}
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM trigger_deliveries WHERE trigger_id=$1`, tr.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial batch saved: %d %v", n, err)
	}
	batch := TriggerBatch{Deliveries: []TriggerDelivery{v}, StateKey: key, StateValue: "1", IntervalID: tr.ID}
	for i := 0; i < 2; i++ {
		if err := d.CommitTriggerBatch(batch); err != nil {
			t.Fatal(err)
		}
	}
	// Re-opening the DB represents losing every in-memory queue entry.
	reopened, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	pending, err := reopened.PendingTriggerDeliveries()
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	for _, p := range pending {
		if p.TriggerID == tr.ID {
			if id != 0 {
				t.Fatal("duplicate delivery")
			}
			id = p.ID
		}
	}
	if id == 0 {
		t.Fatal("pending work lost on reopen")
	}
	if value, _ := d.GetSchedState(key); value != "1" {
		t.Fatalf("cursor=%q", value)
	}
	var claimed atomic.Int64
	var conversationID atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := reopened.ClaimTriggerConversation([]int64{id}, tr.AgentKey, "fixture", v.Message, "saved")
			if err != nil {
				t.Error(err)
				return
			}
			if c != nil {
				claimed.Add(1)
				conversationID.Store(c.ID)
			}
		}()
	}
	wg.Wait()
	if claimed.Load() != 1 {
		t.Fatalf("concurrent claims=%d", claimed.Load())
	}
	if err := d.QueryRow(`SELECT count(*) FROM conversation_activities WHERE conversation_id=$1 AND kind='user' AND detail=$2`, conversationID.Load(), v.Message).Scan(&n); err != nil || n != 1 {
		t.Fatalf("claim missing its user message: %d %v", n, err)
	}
	// Startup marks claimed work interrupted instead of replaying possible tools.
	for i := 0; i < 2; i++ {
		if err := reopened.RecoverTriggerDeliveries(); err != nil {
			t.Fatal(err)
		}
	}
	var state string
	if err := d.QueryRow(`SELECT state FROM trigger_deliveries WHERE id=$1`, id).Scan(&state); err != nil || state != "interrupted" {
		t.Fatalf("state=%s err=%v", state, err)
	}
	if c, err := d.ClaimTriggerConversation([]int64{id}, tr.AgentKey, "duplicate", "duplicate", ""); err != nil || c != nil {
		t.Fatalf("interrupted run replayed: %v %v", c, err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM conversation_activities WHERE conversation_id=$1 AND kind='error'`, conversationID.Load()).Scan(&n); err != nil || n != 1 {
		t.Fatalf("recovery notification count=%d err=%v", n, err)
	}
}

func TestTimeoutEventsFollowCompletionOrderAndRevival(t *testing.T) {
	d, tr := triggerQueueDB(t)
	if err := d.SeedTimeoutEvents(); err != nil {
		t.Fatal(err)
	}
	first, err := d.CreateTask("older task", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.CreateTask("newer task", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.DeleteTask(first.ID); d.DeleteTask(second.ID) })
	stamp := time.Now().UTC().Truncate(time.Microsecond)
	for i, id := range []int64{second.ID, first.ID, first.ID} {
		// The last occurrence represents a revived task timing out again.
		if _, err := d.Exec(`UPDATE tasks SET status='timeout',completed_at=$2 WHERE id=$1`, id, stamp.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
		events, err := d.UnseenTimeoutEvents()
		if err != nil {
			t.Fatal(err)
		}
		var found *TaskEvent
		for j := range events {
			if events[j].TaskID == id {
				found = &events[j]
				break
			}
		}
		if found == nil {
			t.Fatalf("missed timeout occurrence %d for task %d", i, id)
		}
		v := TriggerDelivery{TriggerID: tr.ID, EventKey: fmt.Sprintf("timeout:%d:%s", id, found.OccurredAt.Format(time.RFC3339Nano)), AgentKey: tr.AgentKey, TaskID: id, Message: "timeout"}
		if err := d.CommitTriggerBatch(TriggerBatch{Deliveries: []TriggerDelivery{v}, Timeouts: []TaskEvent{*found}}); err != nil {
			t.Fatal(err)
		}
		events, err = d.UnseenTimeoutEvents()
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			if e.TaskID == id {
				t.Fatal("consumed timeout returned twice")
			}
		}
	}
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM trigger_deliveries WHERE trigger_id=$1`, tr.ID).Scan(&n); err != nil || n != 3 {
		t.Fatalf("deliveries=%d %v", n, err)
	}
}

func TestMergedTriggerClaimDisabledAndCompleted(t *testing.T) {
	d, tr := triggerQueueDB(t)
	for _, key := range []string{"one", "two"} {
		if err := d.CommitTriggerBatch(TriggerBatch{Deliveries: []TriggerDelivery{{TriggerID: tr.ID, EventKey: key, AgentKey: tr.AgentKey, Message: key}}}); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := d.PendingTriggerDeliveries()
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, p := range pending {
		if p.TriggerID == tr.ID {
			ids = append(ids, p.ID)
		}
	}
	tr.Enabled = false
	if err := d.UpdateTrigger(tr); err != nil {
		t.Fatal(err)
	}
	if c, err := d.ClaimTriggerConversation(ids, tr.AgentKey, "merge", "one + two", ""); err != nil || c != nil {
		t.Fatalf("disabled trigger ran: %v %v", c, err)
	}
	tr.Enabled = true
	if err := d.UpdateTrigger(tr); err != nil {
		t.Fatal(err)
	}
	c, err := d.ClaimTriggerConversation(ids, tr.AgentKey, "merge", "one + two", "")
	if err != nil || c == nil {
		t.Fatalf("claim: %v %v", c, err)
	}
	if err := d.FinishTriggerDeliveries(ids, false); err != nil {
		t.Fatal(err)
	}
	if err := d.RecoverTriggerDeliveries(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM trigger_deliveries WHERE id=ANY($1) AND state='finished' AND conversation_id=$2`, ids, c.ID).Scan(&n); err != nil || n != 2 {
		t.Fatalf("merged status lost: %d %v", n, err)
	}
}

func TestPendingTriggerCanceledWithSourceTaskAndNeverRestored(t *testing.T) {
	d, tr := triggerQueueDB(t)
	for _, mode := range []string{"delete", "archive", "hard_delete"} {
		t.Run(mode, func(t *testing.T) {
			task, err := d.CreateTask("pending source", "fixture", nil, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer d.DeleteTask(task.ID)
			v := TriggerDelivery{TriggerID: tr.ID, EventKey: mode, AgentKey: tr.AgentKey, TaskID: task.ID, Message: "do not run after source is gone"}
			if err := d.CommitTriggerBatch(TriggerBatch{Deliveries: []TriggerDelivery{v}}); err != nil {
				t.Fatal(err)
			}
			var id int64
			if err := d.QueryRow(`SELECT id FROM trigger_deliveries WHERE trigger_id=$1 AND event_key=$2`, tr.ID, mode).Scan(&id); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "delete":
				err = d.DeleteTask(task.ID)
			case "archive":
				_, err = d.Exec(`UPDATE tasks SET archived_at=now(),deleted_at=now() WHERE id=$1`, task.ID)
			case "hard_delete":
				_, err = d.DeleteTaskCascade(task.ID, false, false)
			}
			if err != nil {
				t.Fatal(err)
			}
			var state string
			if err := d.QueryRow(`SELECT state FROM trigger_deliveries WHERE id=$1`, id).Scan(&state); err != nil || state != "canceled" {
				t.Fatalf("state=%s err=%v", state, err)
			}
			if _, err := d.Exec(`UPDATE tasks SET deleted_at=NULL,archived_at=NULL WHERE id=$1`, task.ID); err != nil {
				t.Fatal(err)
			}
			if c, err := d.ClaimTriggerConversation([]int64{id}, tr.AgentKey, "stale", "stale", ""); err != nil || c != nil {
				t.Fatalf("restored task replayed canceled work: %v %v", c, err)
			}
		})
	}
	// Polling can observe a task just before deletion; delayed enqueue must also
	// become canceled instead of being revived later with the task.
	v := TriggerDelivery{TriggerID: tr.ID, EventKey: "late", AgentKey: tr.AgentKey, TaskID: -1, Message: "late poll"}
	if err := d.CommitTriggerBatch(TriggerBatch{Deliveries: []TriggerDelivery{v}}); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := d.QueryRow(`SELECT state FROM trigger_deliveries WHERE trigger_id=$1 AND event_key='late'`, tr.ID).Scan(&state); err != nil || state != "canceled" {
		t.Fatalf("late enqueue=%s %v", state, err)
	}
}
