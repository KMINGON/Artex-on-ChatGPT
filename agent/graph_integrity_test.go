package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func graphToolFixture(t *testing.T) (*db.DB, *db.Task, *ToolSet, int64, int64) {
	t.Helper()
	d := testDB(t)
	t.Cleanup(func() { _ = d.Close() })
	task, err := d.CreateTask("graph tool ownership", "verify graph tools", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	s := d.Exploration(task.ExplorationID)
	assigned, err := s.AddIntent(map[string]any{"summary": "assigned"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.AddIntent(map[string]any{"summary": "other worker"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	tools := NewToolSet(s, "fixture-worker")
	tools.SetTaskID(task.ID)
	tools.SetOwnerNode(assigned)
	tools.SetAssetStore(d.Assets(), d.Companies())
	return d, task, tools, assigned, other
}

func TestWorkerFactOwnershipDefaultsAndBatch(t *testing.T) {
	_, _, tools, assigned, other := graphToolFixture(t)
	id, err := tools.recordOneFact(factItem{Summary: "assigned by context"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := tools.ts.OutcomeIDs(assigned)
	if err != nil || len(outputs) != 1 || outputs[0] != id {
		t.Fatalf("missing default yields: %v %v", outputs, err)
	}
	explicit := json.RawMessage(fmt.Sprint(other))
	if id, err := tools.recordOneFact(factItem{Summary: "misattributed", IntentID: explicit}, 0); err == nil || id != 0 {
		t.Fatalf("wrong explicit accepted: %d %v", id, err)
	}
	if id, err := tools.recordOneFact(factItem{Summary: "misattributed batch default"}, other); err == nil || id != 0 {
		t.Fatalf("wrong batch default accepted: %d %v", id, err)
	}
	raw := json.RawMessage(fmt.Sprintf(`{"facts":[{"summary":"second implicit"},{"summary":"wrong explicit","intent_id":%d},{"summary":"third explicit","intent_id":%d}]}`, other, assigned))
	res, err := tools.recordFact().Call(context.Background(), raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	var batch struct {
		IDs    []int64           `json:"ids"`
		Errors map[string]string `json:"errors"`
	}
	if err := json.Unmarshal([]byte(res.Flatten()), &batch); err != nil {
		t.Fatal(err, res.Flatten())
	}
	if len(batch.IDs) != 3 || batch.IDs[0] == 0 || batch.IDs[1] != 0 || batch.IDs[2] == 0 || batch.Errors["1"] == "" {
		t.Fatalf("batch attribution %+v", batch)
	}
	outputs, err = tools.ts.OutcomeIDs(assigned)
	if err != nil || len(outputs) != 3 {
		t.Fatalf("missing batch yields: %v %v", outputs, err)
	}
	if tools.writes.Facts != 3 {
		t.Fatalf("counted failed writes: %+v", tools.writes)
	}
	if outputs, err := tools.ts.OutcomeIDs(other); err != nil || len(outputs) != 0 {
		t.Fatalf("wrote another worker: %v %v", outputs, err)
	}
	// Main/Planner origin facts are not worker intents. Explicit main observations
	// remain valid without inventing an intent for the root node.
	root, _ := tools.ts.OriginFactID()
	tools.SetOwnerNode(root)
	if _, err := tools.recordOneFact(factItem{Summary: "main observation"}, 0); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerFindingOwnershipUsesSameAssignedIntent(t *testing.T) {
	_, _, tools, assigned, other := graphToolFixture(t)
	call := func(raw string) string {
		t.Helper()
		res, err := tools.addFinding().Call(t.Context(), json.RawMessage(raw), nil)
		if err != nil {
			t.Fatal(err)
		}
		return res.Flatten()
	}
	got := call(`{"vulnclass":"fixture","severity":"low","summary":"implicit owner","evidence":"local fixture"}`)
	if !strings.HasPrefix(got, "finding recorded:") {
		t.Fatal(got)
	}
	outputs, err := tools.ts.OutcomeIDs(assigned)
	if err != nil || len(outputs) != 1 {
		t.Fatalf("finding missing default yields: %v %v", outputs, err)
	}
	got = call(fmt.Sprintf(`{"vulnclass":"fixture","severity":"low","summary":"other owner","intent_id":%d}`, other))
	if !strings.Contains(got, "할당된 intent_id") {
		t.Fatalf("wrong finding owner accepted: %s", got)
	}
	if tools.writes.Findings != 1 {
		t.Fatalf("failed finding counted: %+v", tools.writes)
	}
}

func TestIntentParentErrorIncludesActualResultNode(t *testing.T) {
	_, _, tools, assigned, _ := graphToolFixture(t)
	fact, err := tools.recordOneFact(factItem{Summary: "actual result"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, err := tools.addOneIntent(intentItem{Summary: "follow up", ParentIDs: []json.RawMessage{json.RawMessage(fmt.Sprint(assigned))}})
	if err == nil || id != 0 || !strings.Contains(err.Error(), fmt.Sprintf("[%d]", fact)) {
		t.Fatalf("error did not identify actual fact: %d %v", id, err)
	}
	id, err = tools.addOneIntent(intentItem{Summary: "follow up", ParentIDs: []json.RawMessage{json.RawMessage(fmt.Sprint(fact))}})
	if err != nil || id == 0 {
		t.Fatalf("valid result rejected: %d %v", id, err)
	}
}

func TestInsertAssetsCommitsOnlySuccessfulBatchItems(t *testing.T) {
	d, task, tools, assigned, _ := graphToolFixture(t)
	domain := fmt.Sprintf("asset-atomic-%d.invalid", task.ID)
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM assets WHERE domain=$1`, domain) })
	payload := map[string]any{"assets": []any{map[string]any{"type": "root_domain", "domain": domain}, map[string]any{"type": "ip", "ip": "not-an-ip"}}}
	out := callInsertAssets(t, tools, payload)
	results, _ := out["results"].([]any)
	errs, _ := out["errors"].([]any)
	if len(results) != 1 || len(errs) != 1 || tools.writes.Assets != 1 {
		t.Fatalf("batch=%+v counters=%+v", out, tools.writes)
	}
	id := int64(results[0].(map[string]any)["id"].(float64))
	var count int
	if err := d.QueryRow(`SELECT count(*) FROM task_asset_links l JOIN exploration_anchors a ON a.asset_id=l.asset_id AND a.node_id=l.source_node_id WHERE l.task_id=$1 AND l.asset_id=$2 AND l.source='agent' AND l.source_node_id=$3`, task.ID, id, assigned).Scan(&count); err != nil || count != 1 {
		t.Fatalf("missing atomic source/anchor: %d %v", count, err)
	}
	again := callInsertAssets(t, tools, payload)
	retried := again["results"].([]any)
	if int64(retried[0].(map[string]any)["id"].(float64)) != id {
		t.Fatalf("retry duplicated asset: %+v", again)
	}
}

func TestGoalMetCannotBypassIndividualProof(t *testing.T) {
	_, _, tools, _, _ := graphToolFixture(t)
	goal, err := tools.ts.AddGoal(map[string]any{"text": "verify evidence"}, "human")
	if err != nil {
		t.Fatal(err)
	}
	result, err := tools.goalMet().Call(t.Context(), json.RawMessage(`{"reason":"finished"}`), nil)
	if err != nil || tools.GoalMet || !result.IsError || !strings.Contains(result.Flatten(), "prove_goal") {
		t.Fatalf("open goal bypassed: %v %v %s", tools.GoalMet, err, result.Flatten())
	}
	root, _ := tools.ts.OriginFactID()
	if _, _, err := tools.ts.ProveGoal(t.Context(), goal, root, "fixture proof"); err != nil {
		t.Fatal(err)
	}
	result, err = tools.goalMet().Call(t.Context(), json.RawMessage(`{"reason":"reviewed"}`), nil)
	if err != nil || result.IsError || !tools.GoalMet {
		t.Fatalf("reviewed completion request rejected: %v %s", err, result.Flatten())
	}
}
