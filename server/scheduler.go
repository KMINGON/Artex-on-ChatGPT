package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

// Scheduler drives P3 triggers: on each tick it fires due interval triggers and
// scans for new findings / newly-met goals (any task) to fire event triggers.
// Each fire enqueues a NEW conversation on the agent's per-agent trigger queue
// (StartTriggeredRun): fires for the same agent run one at a time in FIFO order,
// distinct agents still run concurrently. State is persisted (per-trigger last_fire
// + finding watermark + fired-goal set) so a restart resumes without double-firing.
// Triggers only attach to CUSTOM agents.
type Scheduler struct {
	s    *Server
	pg   *db.DB
	tick time.Duration
}

const (
	schedKeyLastFinding    = "last_finding_id"    // watermark: max finding node id fired for
	schedKeyFiredGoals     = "fired_goals"        // JSON array of goal node ids already fired
	schedKeyLastToolCall   = "last_toolcall_id"   // watermark: max activity id fired for tool call
	schedKeyLastTaskCreate = "last_taskcreate_id" // watermark: max task id fired for task create
)

func newScheduler(s *Server) *Scheduler {
	return &Scheduler{s: s, pg: s.m.pg, tick: 5 * time.Second}
}

// Run loops until ctx is done, ticking the scheduler. Started once from server New.
func (sc *Scheduler) Run(ctx context.Context) {
	if sc.pg == nil {
		return
	}
	for {
		if err := sc.init(); err == nil {
			break
		} else {
			log.Printf("[scheduler] initialize: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(sc.tick):
		}
	}
	sc.s.loadPendingTriggers()
	t := time.NewTicker(sc.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sc.s.reconcileConcurrency() // 并发上限:有空位就把排队任务补位启动
			sc.step()
		}
	}
}

// init seeds the watermarks on first run so pre-existing findings/goals don't all
// fire at once — only events created AFTER the scheduler first starts count.
func (sc *Scheduler) init() error {
	for _, source := range []struct {
		key   string
		query func(int64) ([]db.TaskEvent, error)
	}{
		{schedKeyLastFinding, sc.pg.NewFindingsSince},
		{schedKeyLastToolCall, sc.pg.NewToolCallsSince},
		{schedKeyLastTaskCreate, sc.pg.NewTasksSince},
	} {
		raw, err := sc.pg.GetSchedState(source.key)
		if err != nil {
			return err
		}
		if raw != "" {
			continue
		}
		events, err := source.query(0)
		if err != nil {
			return err
		}
		var last int64
		for _, e := range events {
			if e.NodeID > last {
				last = e.NodeID
			}
		}
		if err := sc.pg.SetSchedState(source.key, strconv.FormatInt(last, 10)); err != nil {
			return err
		}
	}
	raw, err := sc.pg.GetSchedState(schedKeyFiredGoals)
	if err != nil {
		return err
	}
	if raw == "" {
		events, err := sc.pg.MetGoals()
		if err != nil {
			return err
		}
		ids := []int64{}
		for _, e := range events {
			ids = append(ids, e.NodeID)
		}
		data, _ := json.Marshal(ids)
		if err := sc.pg.SetSchedState(schedKeyFiredGoals, string(data)); err != nil {
			return err
		}
	}
	if err := sc.pg.SeedTimeoutEvents(); err != nil {
		return err
	}
	return sc.pg.RecoverTriggerDeliveries()
}

func (sc *Scheduler) step() {
	triggers, err := sc.pg.ListEnabledTriggers()
	if err != nil {
		return
	}
	sc.fireIntervals(triggers)
	sc.fireFindings(triggers)
	sc.fireGoals(triggers)
	sc.fireTaskTimeouts(triggers)
	sc.fireToolCalls(triggers)
	sc.fireTaskCreates(triggers)
	sc.s.loadPendingTriggers()
}

// fireIntervals fires triggers whose interval has elapsed since last_fire.
func (sc *Scheduler) fireIntervals(triggers []*db.AgentTrigger) {
	now := time.Now()
	for _, tr := range triggers {
		if tr.IntervalSec <= 0 {
			continue
		}
		due := tr.LastFire == nil || now.Sub(*tr.LastFire) >= time.Duration(tr.IntervalSec)*time.Second
		if !due {
			continue
		}
		ctx := "\n\n【이번 실행은 예약 트리거에 의해 시작되었습니다】" + now.Format(" 2006-01-02 15:04:05 MST")
		eventKey := "interval:initial"
		if tr.LastFire != nil {
			eventKey = "interval:" + tr.LastFire.UTC().Format(time.RFC3339Nano)
		}
		sc.commit(db.TriggerBatch{IntervalID: tr.ID, Deliveries: []db.TriggerDelivery{{
			TriggerID: tr.ID, EventKey: eventKey, AgentKey: tr.AgentKey,
			Title: fmt.Sprintf("예약 트리거 · %s", now.Format("15:04")), Message: tr.IntervalMessage + ctx,
		}}})
	}
}

// fireFindings fires on_finding triggers for findings above the persisted
// watermark (monotonic node id → no double-fire across restarts).
func (sc *Scheduler) fireFindings(triggers []*db.AgentTrigger) {
	var want []*db.AgentTrigger
	for _, tr := range triggers {
		if tr.OnFinding {
			want = append(want, tr)
		}
	}
	last, err := sc.lastID(schedKeyLastFinding)
	if err != nil {
		log.Printf("[scheduler] cursor: %v", err)
		return
	}
	events, err := sc.pg.NewFindingsSince(last)
	if err != nil || len(events) == 0 {
		return
	}
	// Advance the watermark whether or not any on_finding trigger is active: a
	// finding fires only for triggers live at the moment it appears. Otherwise a
	// trigger enabled later would replay the entire historical backlog at once.
	batch := db.TriggerBatch{StateKey: schedKeyLastFinding}
	maxID := last
	for _, e := range events {
		if e.NodeID > maxID {
			maxID = e.NodeID
		}
		if len(want) == 0 {
			continue
		}
		msgCtx := fmt.Sprintf("\n\n【이번 실행은 작업의 취약점 발견으로 시작되었습니다】\n발견: [%s/%s] %s",
			e.VulnClass, e.Severity, e.Summary)
		for _, tr := range want {
			batch.Deliveries = append(batch.Deliveries, delivery(tr, "finding:"+strconv.FormatInt(e.NodeID, 10), fmt.Sprintf("취약점 트리거 · task#%d", e.TaskID), tr.FindingMessage+msgCtx, e))
		}
	}
	batch.StateValue = strconv.FormatInt(maxID, 10)
	sc.commit(batch)
}

// fireGoals fires on_goal_met triggers for met goals not yet in the fired set.
func (sc *Scheduler) fireGoals(triggers []*db.AgentTrigger) {
	var want []*db.AgentTrigger
	for _, tr := range triggers {
		if tr.OnGoalMet {
			want = append(want, tr)
		}
	}
	events, err := sc.pg.MetGoals()
	if err != nil || len(events) == 0 {
		return
	}
	// Mark goals as consumed whether or not a trigger is active, so enabling an
	// on_goal_met trigger later doesn't replay every already-met goal.
	fired, err := sc.firedGoalSet()
	if err != nil {
		log.Printf("[scheduler] goals: %v", err)
		return
	}
	batch := db.TriggerBatch{StateKey: schedKeyFiredGoals}
	changed := false
	for _, e := range events {
		if fired[e.NodeID] {
			continue
		}
		fired[e.NodeID] = true
		changed = true
		if len(want) == 0 {
			continue
		}
		msgCtx := fmt.Sprintf("\n\n【이번 실행은 작업의 목표 달성으로 시작되었습니다】\n달성한 목표: %s", e.Summary)
		for _, tr := range want {
			batch.Deliveries = append(batch.Deliveries, delivery(tr, "goal:"+strconv.FormatInt(e.NodeID, 10), fmt.Sprintf("목표 트리거 · task#%d", e.TaskID), tr.GoalMessage+msgCtx, e))
		}
	}
	if changed {
		ids := make([]int64, 0, len(fired))
		for id := range fired {
			ids = append(ids, id)
		}
		data, _ := json.Marshal(ids)
		batch.StateValue = string(data)
		sc.commit(batch)
	}
}

// fireTaskTimeouts observes completion occurrences, independent of task creation order.
func (sc *Scheduler) fireTaskTimeouts(triggers []*db.AgentTrigger) {
	events, err := sc.pg.UnseenTimeoutEvents()
	if err != nil || len(events) == 0 {
		return
	}
	batch := db.TriggerBatch{Timeouts: events}
	for _, e := range events {
		for _, tr := range triggers {
			if !tr.OnTaskTimeout {
				continue
			}
			key := fmt.Sprintf("timeout:%d:%s", e.TaskID, e.OccurredAt.UTC().Format(time.RFC3339Nano))
			batch.Deliveries = append(batch.Deliveries, delivery(tr, key,
				fmt.Sprintf("시간 초과 트리거 · task#%d", e.TaskID), tr.TaskTimeoutMessage+"\n\n【이번 실행은 작업 시간 초과로 시작되었습니다】", e))
		}
	}
	sc.commit(batch)
}

// fireTaskCreates fires on_task_create triggers for tasks newly created above the
// persisted watermark (task id → no double-fire across restarts).
func (sc *Scheduler) fireTaskCreates(triggers []*db.AgentTrigger) {
	var want []*db.AgentTrigger
	for _, tr := range triggers {
		if tr.OnTaskCreate {
			want = append(want, tr)
		}
	}
	last, err := sc.lastID(schedKeyLastTaskCreate)
	if err != nil {
		log.Printf("[scheduler] cursor: %v", err)
		return
	}
	events, err := sc.pg.NewTasksSince(last)
	if err != nil || len(events) == 0 {
		return
	}
	// Advance the watermark even with no active trigger — see fireFindings.
	batch := db.TriggerBatch{StateKey: schedKeyLastTaskCreate}
	maxID := last
	for _, e := range events {
		if e.NodeID > maxID {
			maxID = e.NodeID
		}
		if len(want) == 0 {
			continue
		}
		msgCtx := "\n\n【이번 실행은 작업 생성으로 시작되었습니다】"
		for _, tr := range want {
			batch.Deliveries = append(batch.Deliveries, delivery(tr, "create:"+strconv.FormatInt(e.NodeID, 10), fmt.Sprintf("작업 생성 트리거 · task#%d", e.TaskID), tr.TaskCreateMessage+msgCtx, e))
		}
	}
	batch.StateValue = strconv.FormatInt(maxID, 10)
	sc.commit(batch)
}

// fireToolCalls fires on_tool_call triggers for tool calls (tool_result rows) above
// the persisted watermark whose tool name is in the trigger's selected set. The fire
// message carries the task id/desc/goal + tool name + (truncated) input & output.
func (sc *Scheduler) fireToolCalls(triggers []*db.AgentTrigger) {
	var want []*db.AgentTrigger
	for _, tr := range triggers {
		if tr.OnToolCall && len(tr.ToolNames) > 0 {
			want = append(want, tr)
		}
	}
	last, err := sc.lastID(schedKeyLastToolCall)
	if err != nil {
		log.Printf("[scheduler] cursor: %v", err)
		return
	}
	events, err := sc.pg.NewToolCallsSince(last)
	if err != nil || len(events) == 0 {
		return
	}
	// Advance the watermark even with no active trigger — see fireFindings.
	batch := db.TriggerBatch{StateKey: schedKeyLastToolCall}
	maxID := last
	for _, e := range events {
		if e.NodeID > maxID {
			maxID = e.NodeID
		}
		if len(want) == 0 {
			continue
		}
		// A failed finding write has no committed finding to report. Keep other
		// tool-error triggers available for user-defined automation.
		if e.ToolIsErr && e.Tool == "report_finding" {
			continue
		}
		errTag := ""
		if e.ToolIsErr {
			errTag = "[error] "
		}
		msgCtx := fmt.Sprintf("\n\n【이번 실행은 도구 호출로 시작되었습니다】\n도구: %s\n인자: %s\n결과: %s%s",
			e.Tool, trunc(e.ToolInput, 1500), errTag, trunc(e.ToolOutput, 1500))
		for _, tr := range want {
			if !containsFold(tr.ToolNames, e.Tool) {
				continue
			}
			batch.Deliveries = append(batch.Deliveries, delivery(tr, "tool:"+strconv.FormatInt(e.NodeID, 10), fmt.Sprintf("도구 트리거 · %s · task#%d", e.Tool, e.TaskID), tr.ToolCallMessage+msgCtx, e))
		}
	}
	batch.StateValue = strconv.FormatInt(maxID, 10)
	sc.commit(batch)
}

// containsFold reports whether name is in set (case-insensitive).
func containsFold(set []string, name string) bool {
	for _, s := range set {
		if strings.EqualFold(s, name) {
			return true
		}
	}
	return false
}

// trunc caps s to max runes, appending an ellipsis + original length when cut.
func trunc(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + fmt.Sprintf("…(잘림, 전체 %d자)", len(r))
}

func (sc *Scheduler) lastID(key string) (int64, error) {
	raw, err := sc.pg.GetSchedState(key)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(raw, 10, 64)
}

func (sc *Scheduler) firedGoalSet() (map[int64]bool, error) {
	raw, err := sc.pg.GetSchedState(schedKeyFiredGoals)
	if err != nil {
		return nil, err
	}
	var ids []int64
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, err
	}
	out := map[int64]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

func (sc *Scheduler) commit(batch db.TriggerBatch) {
	if err := sc.pg.CommitTriggerBatch(batch); err != nil {
		log.Printf("[scheduler] persist pending runs: %v", err)
	}
}

func delivery(tr *db.AgentTrigger, key, title, message string, e db.TaskEvent) db.TriggerDelivery {
	return db.TriggerDelivery{TriggerID: tr.ID, EventKey: key, AgentKey: tr.AgentKey, Title: title, Message: message,
		TaskID: e.TaskID, TaskDesc: e.TaskDesc, TaskGoal: e.TaskGoal, Mergeable: true}
}
