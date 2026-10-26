package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/evidence"
	actool "github.com/Autumn-27/norma/tool"
)

type failingFindingRecorder struct{}

func (failingFindingRecorder) Record(context.Context, db.RecordFindingInput, []db.TrafficRef) (*db.RecordedFinding, error) {
	return nil, errors.New("fixture persistence failed")
}

func TestReportFindingOptionalTrafficWithoutCapture(t *testing.T) {
	old := FindingTrafficBindingEnabled
	FindingTrafficBindingEnabled = func() bool { return true }
	t.Cleanup(func() { FindingTrafficBindingEnabled = old })
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("TCP evidence", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	ts := NewToolSet(nil, "")
	ts.ts, ts.taskID = d.Exploration(task.ExplorationID), task.ID
	ts.SetFindingRecorder(evidence.New(d, nil, t.TempDir()))
	notices := 0
	ts.notifyFinding = func(int64, string) { notices++ }
	for _, refs := range []string{"", `,"traffic_refs":[]`, `,"traffic_refs":null`, `,"evidence_hint_id":null`, `,"evidence_hint_id":0`} {
		body := json.RawMessage(`{"vulnclass":"TCP","severity":"low","summary":"verified TCP fixture","evidence":"command output proves the finding"` + refs + `}`)
		tool := ts.addFinding()
		if err := actool.ValidateInput(tool.InputSchema(), body); err != nil {
			t.Fatalf("optional hint rejected before tool invocation: %v", err)
		}
		executor := actool.NewExecuteExtraTool(actool.NewRegistry(tool), actool.NewUnlockSet(tool.Name()))
		wrapped, _ := json.Marshal(map[string]any{"tool_name": tool.Name(), "params": body})
		res, err := executor.Call(t.Context(), wrapped, nil)
		if err != nil || !strings.HasPrefix(res.Flatten(), "finding recorded: ") {
			t.Fatalf("optional traffic rejected: %v %s", err, res.Flatten())
		}
		var out db.RecordedFinding
		if err := json.Unmarshal([]byte(strings.SplitN(res.Flatten(), "\n", 2)[1]), &out); err != nil {
			t.Fatal(err)
		}
		f, err := d.GetFinding(out.FindingID)
		if err != nil || f == nil || len(out.Traffic.Bindings) != 0 || f.Evidence != "command output proves the finding" {
			t.Fatalf("lost non-HTTP evidence: %+v %v", f, err)
		}
	}
	if notices != 5 {
		t.Fatalf("successful reports notified %d times", notices)
	}
}

func TestReportFindingOptionalHintRejectsInvalidReferences(t *testing.T) {
	old := FindingTrafficBindingEnabled
	FindingTrafficBindingEnabled = func() bool { return true }
	t.Cleanup(func() { FindingTrafficBindingEnabled = old })
	_, _, tools, intentID, _ := graphToolFixture(t)
	tool := tools.addFinding()
	for _, value := range []string{`-1`, `true`, `[]`, `{}`, `1.5`, `"wrong"`} {
		body := json.RawMessage(`{"vulnclass":"TEST","severity":"low","summary":"fixture","evidence_hint_id":` + value + `}`)
		res, err := tool.Call(t.Context(), body, nil)
		if err != nil || !res.IsError || tools.writes.Findings != 0 {
			t.Fatalf("invalid hint %s did not fail atomically: %v %s", value, err, res.Flatten())
		}
	}
	// A real node with the wrong kind must not become a hint merely because
	// null and zero are now accepted as absent optional evidence.
	body := json.RawMessage(fmt.Sprintf(`{"vulnclass":"TEST","severity":"low","summary":"fixture","evidence_hint_id":%d}`, intentID))
	res, err := tool.Call(t.Context(), body, nil)
	if err != nil || !res.IsError || !strings.Contains(res.Flatten(), "힌트 노드") || tools.writes.Findings != 0 {
		t.Fatalf("non-hint reference accepted: %v %s", err, res.Flatten())
	}
}

func TestReportFindingAtomicContract(t *testing.T) {
	old := FindingTrafficBindingEnabled
	FindingTrafficBindingEnabled = func() bool { return true }
	t.Cleanup(func() { FindingTrafficBindingEnabled = old })
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("report contract", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	ts := NewToolSet(nil, "")
	ts.ts = d.Exploration(task.ExplorationID)
	ts.taskID = task.ID
	ts.worker = "fixture"
	notices := 0
	ts.notifyFinding = func(int64, string) { notices++ }
	call := func(body string) string {
		t.Helper()
		res, err := ts.addFinding().Call(t.Context(), json.RawMessage(body), nil)
		if err != nil {
			t.Fatal(err)
		}
		return res.Flatten()
	}
	ts.SetFindingRecorder(failingFindingRecorder{})
	if got := call(`{"vulnclass":"TEST","summary":"fail","severity":"low","traffic_refs":[{"traffic_id":"x"}]}`); !strings.Contains(got, "persistence failed") {
		t.Fatal(got)
	}
	if notices != 0 || ts.writes.Findings != 0 {
		t.Fatal("notified before commit")
	}
	ts.SetFindingRecorder(nil)
	got := call(`{"vulnclass":"TEST","summary":"legacy report","severity":"low"}`)
	lines := strings.SplitN(got, "\n", 2)
	if len(lines) != 2 {
		t.Fatal(got)
	}
	var out db.RecordedFinding
	if err = json.Unmarshal([]byte(lines[1]), &out); err != nil {
		t.Fatal(err)
	}
	if lines[0] != fmt.Sprintf("finding recorded: %d", out.NodeID) || out.FindingID <= 0 || notices != 1 {
		t.Fatal(got)
	}
	f, err := d.GetFinding(out.FindingID)
	if err != nil || f == nil || f.NodeID == nil || *f.NodeID != out.NodeID {
		t.Fatalf("wrong finding/node mapping: %+v %v", f, err)
	}
	if got = call(`{"vulnclass":"TEST","summary":"no storage","severity":"low","traffic_refs":[{"traffic_id":"x"}]}`); !strings.Contains(got, "등록하지 않았습니다") {
		t.Fatal(got)
	}
	if notices != 1 {
		t.Fatal("failed tool triggered reporter")
	}
}
