package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// A refuting fact recorded by a Worker is handed to the next Planner round, and
// stays reachable once newer facts have pushed it out of the recent window.
func TestRefutingFactReachesPlannerAndStaysReachable(t *testing.T) {
	_, task, tools, assigned, other := graphToolFixture(t)
	finding, err := tools.ts.RecordFinding(t.Context(), db.RecordFindingInput{TaskID: task.ID, IntentID: assigned,
		VulnClass: "idor", Name: "order access", Severity: "high", Summary: "다른 사용자의 주문 1002를 조회했다", Worker: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	const refutation = "반증: 주문 1002는 요청한 사용자의 주문이었다"
	refuting, err := tools.ts.AddFactWithIntent(t.Context(), map[string]any{"summary": refutation, "detail": "소유자 ID 대조 결과", "confidence": "observed"}, nil, "worker", assigned)
	if err != nil {
		t.Fatal(err)
	}

	// The round fired by the finished Worker names the new fact and shows its summary.
	trigger := renderTriggers(tools.ts, []TriggerEvent{{Kind: "done", IntentID: assigned}})
	if !strings.Contains(trigger, fmt.Sprintf("#%d", refuting)) {
		t.Fatalf("trigger does not name the refuting fact: %s", trigger)
	}
	overview := renderGraphOverview(tools.graphOverviewData())
	if !strings.Contains(overview, refutation) || !strings.Contains(overview, fmt.Sprintf(`"id":%d`, finding.NodeID)) {
		t.Fatalf("overview lacks the refutation or the refuted finding: %s", overview)
	}

	// Twenty newer facts fill the recent window.
	for i := range 20 {
		if _, err := tools.ts.AddFactWithIntent(t.Context(), map[string]any{"summary": fmt.Sprintf("later observation %d", i)}, nil, "worker", other); err != nil {
			t.Fatal(err)
		}
	}
	data := tools.graphOverviewData()
	recent := data["recent_facts"].([]map[string]any)
	inWindow := false
	for _, fact := range recent {
		inWindow = inWindow || fact["id"] == refuting
	}
	t.Logf("refuting fact in the default Planner input after 20 newer facts: %v (window %d of %v facts)", inWindow, len(recent), data["facts"])
	// The totals tell the Planner that the window is partial, and the finding is still listed.
	if data["facts"] != 21 || data["findings_total"] != 1 {
		t.Fatalf("totals: facts=%v findings_total=%v", data["facts"], data["findings_total"])
	}
	listed := callReadJSON(t, tools.listFacts(), `{"q":"반증"}`).(map[string]any)
	facts := listed["facts"].([]any)
	if listed["total"] != float64(1) || len(facts) != 1 || facts[0].(map[string]any)["id"] != float64(refuting) {
		t.Fatalf("list_facts does not reach the refuting fact: %v", listed)
	}
	detail, _ := json.Marshal(callReadJSON(t, tools.nodeDetail(), fmt.Sprintf(`{"id":%d}`, refuting)))
	if !strings.Contains(string(detail), "소유자 ID 대조 결과") {
		t.Fatalf("node_detail lost the refutation detail: %s", detail)
	}
}

// The Planner's finding count is the stored count, not the length of a capped list.
func TestGraphOverviewCountsEveryFinding(t *testing.T) {
	_, _, tools, _, _ := graphToolFixture(t)
	const total = 1001
	for i := range total {
		if _, err := tools.ts.AddNode(db.KindFinding, map[string]any{"summary": fmt.Sprintf("finding %d", i)}, 5, "confirmed", "worker", nil); err != nil {
			t.Fatal(err)
		}
	}
	data := tools.graphOverviewData()
	if data["findings_total"] != total || len(data["finding_list"].([]map[string]any)) != 10 {
		t.Fatalf("findings_total=%v finding_list=%d", data["findings_total"], len(data["finding_list"].([]map[string]any)))
	}
}
