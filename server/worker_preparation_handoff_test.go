package server

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

func TestWorkerPreparationIdentityPreservesOutputWhenRunMarkerFails(t *testing.T) {
	m := stateGraphManager(t)
	oldAugment, oldResolve := agent.ToolAugment, agent.ToolResolve
	agent.ToolAugment, agent.ToolResolve = nil, nil
	t.Cleanup(func() { agent.ToolAugment, agent.ToolResolve = oldAugment, oldResolve })
	task, err := m.CreateTask("missing model start marker", "retain current output", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	intentID, err := task.Store.AddIntent(map[string]any{"summary": "finish remaining control"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Store.SetIntentState(intentID, "running"); err != nil {
		t.Fatal(err)
	}
	intent, err := task.Store.GetNode(intentID)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("reject_model_marker_%s", task.ID)
	statement := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
IF NEW.node_id=%d AND NEW.metadata->>'run_start'='true' THEN RAISE EXCEPTION 'fixture model marker failure'; END IF;
RETURN NEW; END $$;
CREATE TRIGGER %s BEFORE INSERT ON activity FOR EACH ROW EXECUTE FUNCTION %s()`, name, intentID, name, name)
	if _, err := m.pg.Exec(statement); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := m.pg.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON activity; DROP FUNCTION IF EXISTS %s()", name, name)); err != nil {
			t.Error(err)
		}
	}()
	calls := 0
	provider := &browserObservationProvider{step: func(llm.CompletionRequest) []llm.StreamEvent {
		calls++
		if calls == 1 {
			return []llm.StreamEvent{{Type: llm.SEToolUseStart, ToolID: "control-proof", ToolName: "record_fact"}, {Type: llm.SEToolInputJSON, Text: `{"summary":"control verified","detail":"latest control evidence"}`}, {Type: llm.SEMessageDelta, StopReason: "tool_use"}}
		}
		return []llm.StreamEvent{{Type: llm.SETextDelta, Text: "latest validation result"}, {Type: llm.SEMessageDelta, StopReason: "end_turn"}}
	}}
	worker := agent.NewWorker(provider, "fixture", m.dir, nil, 0, 4)
	if !NewEngine(m).runIntent(t.Context(), task, "fixture", worker, intent, "", "") {
		t.Fatal("worker did not run")
	}
	current, err := task.Store.LatestWorkerOutput(intentID)
	if err != nil || current == nil || current.Detail != "latest validation result" || calls != 2 {
		t.Fatalf("lost current result after model marker failure: output=%+v calls=%d err=%v", current, calls, err)
	}
	var prepID int64
	var prepRunID string
	if err := m.pg.QueryRow(`SELECT id,metadata->>'run_id' FROM activity WHERE node_id=$1 AND metadata->>'worker_preparation_start'='true' ORDER BY id DESC LIMIT 1`, intentID).Scan(&prepID, &prepRunID); err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(current.Metadata, &metadata); err != nil || prepRunID == "" || metadata["run_id"] != prepRunID || metadata["run_start_activity_id"] != float64(prepID) {
		t.Fatalf("preparation and captured result identity differ: prep=%d/%s result=%s err=%v", prepID, prepRunID, current.Metadata, err)
	}
	if _, err := task.Store.AppendActivity(db.Activity{NodeID: &intentID, Kind: "result", Detail: "late previous run", Metadata: json.RawMessage(`{"run_id":"previous-run"}`)}); err != nil {
		t.Fatal(err)
	}
	latest, err := task.Store.LatestWorkerOutput(intentID)
	if err != nil || latest == nil || latest.ID != current.ID {
		t.Fatalf("late previous result replaced the current output: %+v %v", latest, err)
	}
}

func TestWorkerPreparationFailureDoesNotReusePreviousSuccess(t *testing.T) {
	m := stateGraphManager(t)
	oldAugment, oldResolve := agent.ToolAugment, agent.ToolResolve
	agent.ToolAugment, agent.ToolResolve = nil, nil
	t.Cleanup(func() { agent.ToolAugment, agent.ToolResolve = oldAugment, oldResolve })
	for _, failure := range []string{"scope", "activity_boundary"} {
		t.Run(failure, func(t *testing.T) {
			task, err := m.CreateTask("resume failed preparation", "continue unfinished work", nil, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer m.DeleteTask(task.ID, DeleteTaskOptions{})
			taskID, _ := strconv.ParseInt(task.ID, 10, 64)
			assetID, err := m.assets.UpsertEndpoint(db.UpsertEndpointReq{URL: fmt.Sprintf("https://preparation-%s.invalid/probe", task.ID), Method: "GET", TaskID: taskID})
			if err != nil {
				t.Fatal(err)
			}
			intentID, err := task.Store.AddIntent(map[string]any{"summary": "resume validation", "asset_ids": []int64{assetID}}, 5, []int64{assetID}, "planner")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := task.Store.AppendActivity(db.Activity{NodeID: &intentID, Worker: "fixture", Kind: "result", Detail: "previous successful run; new controls remain"}); err != nil {
				t.Fatal(err)
			}
			if err := task.Store.SetIntentState(intentID, "running"); err != nil {
				t.Fatal(err)
			}
			intent, err := task.Store.GetNode(intentID)
			if err != nil {
				t.Fatal(err)
			}
			name := fmt.Sprintf("reject_worker_preparation_%s", task.ID)
			table, condition := "task_scope", fmt.Sprintf("NEW.task_id=%d", taskID)
			if failure == "activity_boundary" {
				table = "activity"
				condition = fmt.Sprintf("NEW.node_id=%d AND NEW.metadata->>'worker_preparation_start'='true'", intentID)
			}
			statement := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF %s THEN RAISE EXCEPTION 'fixture preparation failure'; END IF; RETURN NEW; END $$;
CREATE TRIGGER %s BEFORE INSERT ON %s FOR EACH ROW EXECUTE FUNCTION %s()`, name, condition, name, table, name)
			if _, err := m.pg.Exec(statement); err != nil {
				t.Fatal(err)
			}
			removeFailure := func() {
				t.Helper()
				if _, err := m.pg.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON %s; DROP FUNCTION IF EXISTS %s()", name, table, name)); err != nil {
					t.Fatal(err)
				}
			}
			defer removeFailure()
			calls := 0
			provider := &browserObservationProvider{step: func(llm.CompletionRequest) []llm.StreamEvent {
				calls++
				if calls == 1 {
					return []llm.StreamEvent{{Type: llm.SEToolUseStart, ToolID: "fresh-proof", ToolName: "record_fact"}, {Type: llm.SEToolInputJSON, Text: `{"summary":"new controls verified","detail":"fresh validation after preparation recovery"}`}, {Type: llm.SEMessageDelta, StopReason: "tool_use"}}
				}
				return []llm.StreamEvent{{Type: llm.SETextDelta, Text: "fresh validation after recovery"}, {Type: llm.SEMessageDelta, StopReason: "end_turn"}}
			}}
			worker := agent.NewWorker(provider, "fixture", m.dir, nil, 0, 4)
			e := NewEngine(m)
			if !e.runIntent(t.Context(), task, "fixture", worker, intent, "", "") {
				t.Fatal("preparation attempt did not run")
			}
			stored, err := task.Store.GetNode(intentID)
			if err != nil || stored.State != "blocked" || calls != 0 {
				t.Fatalf("preparation failure not isolated: state=%+v calls=%d err=%v", stored, calls, err)
			}
			result, err := task.Store.LatestWorkerOutput(intentID)
			if err != nil || (result != nil && (!result.IsError || strings.Contains(result.Detail, "previous successful run"))) {
				t.Fatalf("previous success presented as failed attempt: result=%+v err=%v", result, err)
			}
			triggers := task.drainTriggers()
			if len(triggers) != 1 || triggers[0].IntentID != intentID {
				t.Fatalf("Planner did not receive incomplete execution: %+v", triggers)
			}
			var preserved int
			if err := m.pg.QueryRow(`SELECT count(*) FROM activity WHERE node_id=$1 AND detail='previous successful run; new controls remain'`, intentID).Scan(&preserved); err != nil || preserved != 1 {
				t.Fatalf("historical result was deleted: count=%d err=%v", preserved, err)
			}
			removeFailure()
			if _, err := task.Store.ReopenIntent(intentID); err != nil {
				t.Fatal(err)
			}
			if claimed, err := task.Store.ClaimIntent(intentID, "fixture"); err != nil || !claimed {
				t.Fatalf("recovery claim failed: claimed=%v err=%v", claimed, err)
			}
			intent, _ = task.Store.GetNode(intentID)
			if !e.runIntent(t.Context(), task, "fixture", worker, intent, "", "") {
				t.Fatal("recovered worker did not run")
			}
			stored, err = task.Store.GetNode(intentID)
			if err != nil || stored.State != "done" || calls != 2 {
				t.Fatalf("recovered work did not finish: state=%+v calls=%d err=%v", stored, calls, err)
			}
			result, err = task.Store.LatestWorkerOutput(intentID)
			if err != nil || result == nil || result.IsError || !strings.Contains(result.Detail, "fresh validation after recovery") {
				t.Fatalf("fresh result was not handed off: result=%+v err=%v", result, err)
			}
		})
	}
}
