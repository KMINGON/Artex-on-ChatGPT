package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestPlannerUsesFactGraphInsteadOfFollowUpQueue(t *testing.T) {
	tools := NewToolSet(nil, "fixture")
	for _, tc := range []struct {
		tool, batch, removed string
	}{
		{"record_fact", "facts", "follow_ups"},
		{"add_intent", "intents", "follow_up_refs"},
	} {
		var found bool
		for _, tool := range tools.PlannerTools() {
			if tool.Name() == "list_follow_ups" || tool.Name() == "update_follow_ups" {
				t.Fatalf("legacy queue tool exposed to Planner: %s", tool.Name())
			}
			if tool.Name() != tc.tool {
				continue
			}
			found = true
			props := tool.InputSchema()["properties"].(map[string]any)
			if props[tc.removed] != nil {
				t.Errorf("%s still advertises %s", tc.tool, tc.removed)
			}
			items := props[tc.batch].(map[string]any)["items"].(map[string]any)
			if items["properties"].(map[string]any)[tc.removed] != nil {
				t.Errorf("%s batch still advertises %s", tc.tool, tc.removed)
			}
		}
		if tc.tool == "record_fact" {
			continue // Worker tool, checked directly below.
		}
		if !found {
			t.Fatalf("missing %s", tc.tool)
		}
	}
	if tools.recordFact().InputSchema()["properties"].(map[string]any)["follow_ups"] != nil {
		t.Fatal("record_fact still advertises follow-up queue")
	}
}

func TestLegacyFollowUpFactPayloadRemainsReadable(t *testing.T) {
	_, _, tools, assigned, _ := graphToolFixture(t)
	leads, err := db.NewFollowUps([]string{"separate path"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := tools.ts.AddFactWithIntent(t.Context(), map[string]any{"summary": "old observation", "follow_ups": leads}, nil, "worker", assigned)
	if err != nil {
		t.Fatal(err)
	}
	node, err := tools.ts.GetNode(id)
	if err != nil || node == nil {
		t.Fatalf("fact missing: %v", err)
	}
	leads = db.FollowUpsFromPayload(node.Payload)
	if len(leads) != 1 || leads[0].Summary != "separate path" {
		t.Fatalf("legacy payload lost: %+v", leads)
	}
	got := callReadJSON(t, tools.nodeDetail(), `{"id":`+strconv.FormatInt(id, 10)+`}`)
	if got == nil {
		t.Fatal("node_detail did not return legacy fact")
	}
	intent, err := tools.addOneIntentContext(context.Background(), intentItem{Summary: "verify separate path", ParentIDs: []json.RawMessage{json.RawMessage(strconv.FormatInt(id, 10))}})
	if err != nil || intent == 0 {
		t.Fatalf("fact-derived intent: id=%d err=%v", intent, err)
	}
	edges, err := tools.ts.EdgesTouching([]int64{intent})
	if err != nil {
		t.Fatal(err)
	}
	linked := false
	for _, edge := range edges {
		if edge.From == id && edge.To == intent && edge.Rel == db.RelDerivedFrom {
			linked = true
		}
	}
	if !linked {
		t.Fatalf("fact provenance lost: edges=%v", edges)
	}
}

func TestHTTPObservationDoesNotDisplacePlannerFacts(t *testing.T) {
	_, _, tools, assigned, _ := graphToolFixture(t)
	fact, err := tools.ts.AddFactWithIntent(t.Context(), map[string]any{"summary": "실제 권한 비교 결론"}, nil, "worker", assigned)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 21; i++ {
		if _, err := tools.ts.AddFactWithIntent(t.Context(), map[string]any{"summary": "HTTP response observed", "observation_source": "http_response"}, nil, "browser", assigned); err != nil {
			t.Fatal(err)
		}
	}
	overview := tools.graphOverviewData()
	if overview["facts"] != 1 || overview["http_observation_facts"] != 21 {
		t.Fatalf("wrong fact counts: facts=%v observations=%v", overview["facts"], overview["http_observation_facts"])
	}
	recent, ok := overview["recent_facts"].([]map[string]any)
	if !ok || len(recent) != 1 || recent[0]["id"] != fact {
		t.Fatalf("analysis fact displaced: %+v", overview["recent_facts"])
	}
	if got := factIDsYielded(tools.ts, assigned); got != "#"+strconv.FormatInt(fact, 10) {
		t.Fatalf("worker completion included HTTP observation facts: %s", got)
	}
}

func TestOldFollowUpToolInputsReturnActionableErrors(t *testing.T) {
	_, _, tools, assigned, _ := graphToolFixture(t)
	before, err := tools.ts.OutcomeIDs(assigned)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body, guidance string
	}{
		{"record_fact", `{"summary":"old note","follow_ups":["new path"]}`, "detail"},
		{"add_intent", `{"summary":"old assignment","follow_up_refs":[{"fact_id":1,"lead_id":1}]}`, "parent_ids"},
	} {
		var tool = tools.recordFact()
		if tc.name == "add_intent" {
			tool = tools.addIntent()
		}
		result, err := tool.Call(t.Context(), json.RawMessage(tc.body), nil)
		if err != nil || !result.IsError || !strings.Contains(result.Flatten(), tc.guidance) {
			t.Fatalf("%s old input silently accepted: result=%s err=%v", tc.name, result.Flatten(), err)
		}
	}
	after, err := tools.ts.OutcomeIDs(assigned)
	if err != nil || len(after) != len(before) {
		t.Fatalf("deprecated input wrote a fact: before=%v after=%v err=%v", before, after, err)
	}
}
