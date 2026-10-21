package db

import "database/sql"

type TriggerDelivery struct {
	ID, TriggerID, TaskID                                  int64
	EventKey, AgentKey, Title, Message, TaskDesc, TaskGoal string
	Mergeable                                              bool
}

// TriggerBatch commits the pending work and the scheduler's cursor together.
// A crash can leave pending work, but cannot advance past work never saved.
type TriggerBatch struct {
	Deliveries           []TriggerDelivery
	StateKey, StateValue string
	IntervalID           int64
	Timeouts             []TaskEvent
}

func (d *DB) CommitTriggerBatch(b TriggerBatch) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, v := range b.Deliveries {
		state := "pending"
		if v.TaskID != 0 {
			var alive bool
			// Lock before inserting so a concurrent archive/delete sees this row
			// when its cancellation trigger runs after our transaction commits.
			err := tx.QueryRow(`SELECT true FROM tasks WHERE id=$1 AND deleted_at IS NULL AND archived_at IS NULL FOR SHARE`, v.TaskID).Scan(&alive)
			if err == sql.ErrNoRows {
				state = "canceled"
			} else if err != nil {
				return err
			}
		}
		if _, err = tx.Exec(`INSERT INTO trigger_deliveries
 (trigger_id,event_key,agent_key,title,message,task_id,task_description,task_goal,mergeable,state)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(trigger_id,event_key) DO NOTHING`,
			v.TriggerID, v.EventKey, v.AgentKey, utf8Clean(v.Title), utf8Clean(v.Message), v.TaskID,
			utf8Clean(v.TaskDesc), utf8Clean(v.TaskGoal), v.Mergeable, state); err != nil {
			return err
		}
	}
	if b.StateKey != "" {
		if _, err = tx.Exec(`INSERT INTO scheduler_state(key,value) VALUES($1,$2)
 ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`, b.StateKey, b.StateValue); err != nil {
			return err
		}
	}
	if b.IntervalID != 0 {
		if _, err = tx.Exec(`UPDATE agent_triggers SET last_fire=now() WHERE id=$1`, b.IntervalID); err != nil {
			return err
		}
	}
	for _, e := range b.Timeouts {
		if _, err = tx.Exec(`INSERT INTO trigger_timeout_events(task_id,completed_at) VALUES($1,$2)
 ON CONFLICT(task_id) DO UPDATE SET completed_at=EXCLUDED.completed_at`, e.TaskID, e.OccurredAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SeedTimeoutEvents upgrades the old task-ID watermark without replaying every
// historical timeout. New timeouts are tracked by each task's completion stamp.
func (d *DB) SeedTimeoutEvents() error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO scheduler_state(key,value) VALUES('timeout_events_v2','1') ON CONFLICT DO NOTHING`)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 0 {
		if _, err = tx.Exec(`INSERT INTO trigger_timeout_events(task_id,completed_at)
 SELECT id,COALESCE(completed_at,created_at) FROM tasks WHERE status='timeout' AND deleted_at IS NULL
 ON CONFLICT DO NOTHING`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) UnseenTimeoutEvents() ([]TaskEvent, error) {
	rows, err := d.Query(`SELECT t.id,t.description,t.goal,COALESCE(t.completed_at,t.created_at)
 FROM tasks t LEFT JOIN trigger_timeout_events e ON e.task_id=t.id
 WHERE t.status='timeout' AND t.deleted_at IS NULL
 AND (e.task_id IS NULL OR e.completed_at IS DISTINCT FROM COALESCE(t.completed_at,t.created_at))
 ORDER BY COALESCE(t.completed_at,t.created_at),t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskEvent
	for rows.Next() {
		var e TaskEvent
		if err = rows.Scan(&e.TaskID, &e.TaskDesc, &e.TaskGoal, &e.OccurredAt); err != nil {
			return nil, err
		}
		e.NodeID = e.TaskID
		out = append(out, e)
	}
	return out, rows.Err()
}

func (d *DB) PendingTriggerDeliveries() ([]TriggerDelivery, error) {
	rows, err := d.Query(`SELECT d.id,d.trigger_id,d.event_key,d.agent_key,d.title,d.message,d.task_id,d.task_description,d.task_goal,d.mergeable
 FROM trigger_deliveries d JOIN agent_triggers t ON t.id=d.trigger_id
 JOIN agents a ON a.key=d.agent_key
 WHERE d.state='pending' AND t.enabled AND a.enabled
 AND (d.task_id=0 OR EXISTS(SELECT 1 FROM tasks source WHERE source.id=d.task_id AND source.deleted_at IS NULL AND source.archived_at IS NULL))
 ORDER BY d.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TriggerDelivery
	for rows.Next() {
		var v TriggerDelivery
		if err = rows.Scan(&v.ID, &v.TriggerID, &v.EventKey, &v.AgentKey, &v.Title, &v.Message, &v.TaskID, &v.TaskDesc, &v.TaskGoal, &v.Mergeable); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ClaimTriggerConversation saves the conversation and its first message in the
// same transaction as the claim. Returning nil means another runner claimed it,
// or its trigger was disabled/deleted. No model call may precede this commit.
func (d *DB) ClaimTriggerConversation(ids []int64, agentKey, title, message, summary string) (*Conversation, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT d.state,t.enabled,a.enabled FROM trigger_deliveries d
 JOIN agent_triggers t ON t.id=d.trigger_id JOIN agents a ON a.key=d.agent_key
 WHERE d.id=ANY($1) AND d.agent_key=$2
 AND (d.task_id=0 OR EXISTS(SELECT 1 FROM tasks source WHERE source.id=d.task_id AND source.deleted_at IS NULL AND source.archived_at IS NULL))
 ORDER BY d.id FOR UPDATE OF d`, ids, agentKey)
	if err != nil {
		return nil, err
	}
	count, ready := 0, true
	for rows.Next() {
		var state string
		var triggerEnabled, agentEnabled bool
		if err = rows.Scan(&state, &triggerEnabled, &agentEnabled); err != nil {
			rows.Close()
			return nil, err
		}
		count++
		ready = ready && state == "pending" && triggerEnabled && agentEnabled
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if !ready || count != len(ids) {
		return nil, nil
	}
	c, err := scanConv(tx.QueryRow(`INSERT INTO conversations(agent_key,title) VALUES($1,$2) RETURNING `+convCols, agentKey, utf8Clean(title)))
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`INSERT INTO conversation_activities(conversation_id,worker,kind,summary,detail)
 VALUES($1,$2,'user',$3,$4)`, c.ID, agentKey, utf8Clean(summary), utf8Clean(message)); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`UPDATE trigger_deliveries SET state='running',conversation_id=$2,updated_at=now() WHERE id=ANY($1)`, ids, c.ID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (d *DB) FinishTriggerDeliveries(ids []int64, interrupted bool) error {
	state := "finished"
	if interrupted {
		state = "interrupted"
	}
	_, err := d.Exec(`UPDATE trigger_deliveries SET state=$2,updated_at=now() WHERE id=ANY($1) AND state='running'`, ids, state)
	return err
}

// RecoverTriggerDeliveries is called once on process startup, before any queue
// drains. Pending runs survive; started runs require human review, never replay.
func (d *DB) RecoverTriggerDeliveries() error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`WITH interrupted AS (
 UPDATE trigger_deliveries SET state='interrupted',updated_at=now() WHERE state='running'
 RETURNING conversation_id,agent_key)
 INSERT INTO conversation_activities(conversation_id,worker,kind,is_error,summary,detail)
 SELECT DISTINCT conversation_id,agent_key,'error',true,$1,$1 FROM interrupted WHERE conversation_id IS NOT NULL`,
		"서버가 종료되어 트리거 실행 결과를 확인할 수 없습니다. 도구가 이미 실행됐을 수 있으므로 자동 재실행하지 않았습니다. 실행 기록을 확인해 주세요.")
	if err != nil {
		return err
	}
	return tx.Commit()
}
