package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestGraphOverviewCarriesIntentSchedulingContext(t *testing.T) {
	d, _, tools, queued, running := graphToolFixture(t)
	finished, err := tools.ts.AddIntent(map[string]any{"summary": "finished question"}, 7, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	for id, state := range map[int64]string{running: "running", finished: "done"} {
		if err := tools.ts.SetIntentState(id, state); err != nil {
			t.Fatal(err)
		}
	}
	created := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	if _, err := d.Exec(`UPDATE exploration_nodes SET priority=4, created_at=$1 WHERE id=$2`, created, queued); err != nil {
		t.Fatal(err)
	}
	result, err := tools.graphOverview().Call(t.Context(), json.RawMessage(`{}`), nil)
	if err != nil || result.IsError {
		t.Fatalf("graph overview: %v, %s", err, result.Flatten())
	}
	type intent struct {
		ID        int64     `json:"id"`
		Priority  *int      `json:"priority"`
		CreatedAt time.Time `json:"created_at"`
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal([]byte(result.Flatten()), &data); err != nil {
		t.Fatal(err)
	}
	for key, id := range map[string]int64{"open_intents": queued, "running_intents": running, "recent_done_intents": finished} {
		var nodes []intent
		if err := json.Unmarshal(data[key], &nodes); err != nil {
			t.Fatal(err)
		}
		source, err := tools.ts.GetNode(id)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, node := range nodes {
			if node.ID != id {
				continue
			}
			found = true
			if node.Priority == nil || *node.Priority != source.Priority || !node.CreatedAt.Equal(source.CreatedAt) {
				t.Fatalf("%s lost persisted scheduling context: %+v", key, node)
			}
		}
		if !found {
			t.Fatalf("%s missing intent %d", key, id)
		}
	}
	input := renderGraphOverview(tools.graphOverviewData())
	start := strings.Index(input, "{")
	if start < 0 || json.Unmarshal([]byte(input[start:]), &data) != nil {
		t.Fatal("Planner preloaded overview is not JSON")
	}
	var queuedInput []intent
	if err := json.Unmarshal(data["open_intents"], &queuedInput); err != nil {
		t.Fatal(err)
	}
	if len(queuedInput) != 1 || queuedInput[0].ID != queued || queuedInput[0].Priority == nil || *queuedInput[0].Priority != 4 || !queuedInput[0].CreatedAt.Equal(created) {
		t.Fatal("Planner preloaded overview lost the queued intent age or priority")
	}
}
