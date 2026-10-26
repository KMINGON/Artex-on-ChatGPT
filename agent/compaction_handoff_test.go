package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestCompressionOversizedSourceStaysVisibleWithoutStarvingOtherNodes(t *testing.T) {
	nodes := map[int64]*db.Node{}
	for id := int64(1); id <= 3; id++ {
		detail := "complete short detail"
		if id == 1 {
			detail = strings.Repeat("가", compressionInputRunes) + "unfinished verification"
		}
		payload, _ := json.Marshal(map[string]any{"summary": "observation", "detail": detail})
		nodes[id] = &db.Node{ID: id, Kind: db.KindFact, State: "confirmed", Payload: payload}
	}
	graph := newColdGraph([]cgNode{fact(1), fact(2), fact(3)}, nil)
	parts := completeCompressionBlocks(graph, []block{{Members: []int64{1, 2, 3}}}, nodes, 2)
	if len(parts) != 1 || len(parts[0].Members) != 2 || parts[0].Members[0] != 2 || parts[0].Members[1] != 3 {
		t.Fatalf("oversized node blocked the complete small pair: %+v", parts)
	}
	provider := &graphSummaryProvider{stop: "end_turn"}
	compactor := NewCompactor(provider, "fixture")
	for range 2 {
		body, err := compactor.compress(t.Context(), graph, block{Members: []int64{1, 2}}, nodes)
		if err != nil || body != "" || provider.calls != 0 {
			t.Fatalf("oversized source was repeatedly sent to the model: body=%q calls=%d err=%v", body, provider.calls, err)
		}
	}
	if _, err := compactor.compress(t.Context(), graph, parts[0], nodes); err != nil || provider.calls != 1 {
		t.Fatalf("small complete pair did not proceed: calls=%d err=%v", provider.calls, err)
	}
}

func TestLegacyAndChangedDigestSourcesRemainReadableAndVisible(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("preserve all verification clues", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	store := d.Exploration(task.ExplorationID)
	detail := strings.Repeat("가", 2000) + "UNFINISHED-MIDDLE" + strings.Repeat("나", 2000)
	var members []int64
	for range 2 {
		id, err := store.AddNode(db.KindFact, map[string]any{"summary": "partial result", "detail": detail}, 0, "confirmed", "fixture", nil)
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, id)
	}
	legacyID, err := store.AddDigest(map[string]any{"body": "legacy summary did not see the middle"}, members)
	if err != nil {
		t.Fatal(err)
	}
	tools := NewToolSet(store, "planner")
	tools.SetTaskID(task.ID)
	assertVisible := func() {
		t.Helper()
		visible := map[int64]bool{}
		for _, item := range tools.graphOverviewData()["recent_facts"].([]map[string]any) {
			visible[item["id"].(int64)] = true
		}
		for _, id := range members {
			if !visible[id] || hiddenMembersFor(store)(id) {
				t.Fatalf("source %d is hidden behind an incomplete or stale digest", id)
			}
			read := callReadJSON(t, tools.nodeDetail(), fmt.Sprintf(`{"id":%d}`, id)).(map[string]any)
			if read["payload"].(map[string]any)["detail"] != detail {
				t.Fatalf("source %d lost the original middle clue", id)
			}
		}
	}
	assertVisible()
	graph, nodes, err := loadColdGraph(store)
	if err != nil {
		t.Fatal(err)
	}
	versions, err := store.ContentVersions()
	if err != nil {
		t.Fatal(err)
	}
	compactor := NewCompactor(&graphSummaryProvider{stop: "end_turn"}, "fixture")
	compactor.foldBlock(t.Context(), store, graph, block{Members: members}, nodes, versions, 1)
	covered, err := completeCoveredMembers(store)
	if err != nil || len(covered) != 2 || covered[members[0]] == legacyID {
		t.Fatalf("complete input was not folded correctly: covered=%v err=%v", covered, err)
	}
	completeID := covered[members[0]]
	if !hiddenMembersFor(store)(members[0]) {
		t.Fatal("complete and unchanged source did not fold")
	}
	expanded := callReadJSON(t, tools.expandDigest(), fmt.Sprintf(`{"id":%d}`, completeID)).(map[string]any)
	if len(expanded["members"].([]any)) != 2 {
		t.Fatal("digest expansion no longer matches its covers edges")
	}
	// A source change invalidates the summary as a replacement without deleting
	// either the old summary or the original node.
	if err := store.SetNodeState(members[0], "dismissed"); err != nil {
		t.Fatal(err)
	}
	assertVisible()
	for _, id := range []int64{legacyID, completeID} {
		if node, err := store.GetNode(id); err != nil || node == nil {
			t.Fatalf("historical digest %d disappeared: %v", id, err)
		}
	}
}

func TestGraphMajorRevivalKeepsPreviousDigestUntilReplacementSaved(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("summary revival fixture", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	store := d.Exploration(task.ExplorationID)
	var members []int64
	for range 2 {
		id, err := store.AddNode(db.KindFact, map[string]any{"summary": "cold observation"}, 0, "confirmed", "fixture", nil)
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, id)
	}
	if err := store.Link(members[0], db.RelYields, members[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec("UPDATE explorations SET round_no=100 WHERE id=$1", task.ExplorationID); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyStampOps([]db.StampOp{{ID: members[0], Set: true, Round: 1}, {ID: members[1], Set: true, Round: 1}}); err != nil {
		t.Fatal(err)
	}
	previous, err := store.AddDigest(map[string]any{"body": "previous readable summary", "signature": "legacy", "generation": 1}, members)
	if err != nil {
		t.Fatal(err)
	}
	provider := &graphSummaryProvider{stop: "end_turn", afterComplete: func() {
		if _, err := store.AddIntentWithParents(t.Context(), map[string]any{"summary": "new verification"}, 5, nil, members, "fixture"); err != nil {
			t.Fatal(err)
		}
	}}
	NewCompactor(provider, "fixture").major(t.Context(), store)
	active, err := store.ActiveDigests()
	if err != nil || provider.calls != 1 || len(active) != 1 || active[0].ID != previous {
		t.Fatalf("previous digest retired before replacement could save: calls=%d active=%v err=%v", provider.calls, active, err)
	}
	for _, id := range members {
		if hiddenMembersFor(store)(id) {
			t.Fatalf("revived source %d was hidden", id)
		}
	}
}
