package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestCompressionInputPreservesStructuredPayloadAndAssetAnchors(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"summary":            "HTTP 응답 관찰 1개: 범위 내 endpoint 1개 등록",
		"observations":       []map[string]any{{"url": "https://fixture.invalid/unresolved", "method": "PATCH", "state": "registered", "asset_id": 904}},
		"new_evidence_field": map[string]any{"next_check": "FOLLOW-UP-UNTESTED"},
	})
	nodes := map[int64]*db.Node{1: {ID: 1, Kind: db.KindFact, State: "confirmed", Payload: payload, Anchors: []int64{904}}}
	input := buildCompressionInput(newColdGraph([]cgNode{fact(1)}, nil), block{Members: []int64{1}}, nodes)
	for _, required := range []string{"https://fixture.invalid/unresolved", "PATCH", "FOLLOW-UP-UNTESTED", "asset_ids: [904]"} {
		if !strings.Contains(input, required) {
			t.Errorf("complete compression input omitted %q: %s", required, input)
		}
	}
}

func TestCompressionRechecksAssetAndLineageChangesWithoutVersionBumps(t *testing.T) {
	for _, change := range []string{"asset_anchor", "lineage"} {
		t.Run(change, func(t *testing.T) {
			d := testDB(t)
			defer d.Close()
			task, err := d.CreateTask("compression reference freshness "+change, "fixture", nil, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
			store := d.Exploration(task.ExplorationID)
			var members []int64
			for range 2 {
				id, err := store.AddNode(db.KindFact, map[string]any{"summary": "observation"}, 0, "confirmed", "fixture", nil)
				if err != nil {
					t.Fatal(err)
				}
				members = append(members, id)
			}
			if err := store.Link(members[0], db.RelYields, members[1]); err != nil {
				t.Fatal(err)
			}
			assetID, err := d.Assets().UpsertEndpoint(db.UpsertEndpointReq{URL: fmt.Sprintf("https://fixture.invalid/compression/%d", task.ID), Method: "GET", TaskID: task.ID})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Anchor(members[0], assetID); err != nil {
				t.Fatal(err)
			}
			graph, nodes, versions, err := loadCompressionSnapshot(store)
			if err != nil {
				t.Fatal(err)
			}
			if len(nodes[members[0]].Anchors) != 1 || nodes[members[0]].Anchors[0] != assetID {
				t.Fatal("persisted asset anchor did not reach the compression source snapshot")
			}
			provider := &graphSummaryProvider{stop: "end_turn"}
			compactor := NewCompactor(provider, "fixture")
			compactor.foldBlock(t.Context(), store, graph, block{Members: members}, nodes, versions, 1)
			if !hiddenMembersFor(store)(members[0]) {
				t.Fatal("stable complete source did not fold")
			}
			active, err := store.ActiveDigests()
			if err != nil || len(active) != 1 {
				t.Fatalf("initial digest: %+v %v", active, err)
			}
			oldDigest := active[0].ID
			if change == "asset_anchor" {
				if err := store.Anchor(members[1], assetID); err != nil {
					t.Fatal(err)
				}
			} else {
				root, err := store.OriginFactID()
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Link(root, db.RelYields, members[0]); err != nil {
					t.Fatal(err)
				}
			}
			currentVersions, err := store.ContentVersions()
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range members {
				if currentVersions[id] != versions[id] {
					t.Fatal("fixture unexpectedly bumped node content versions")
				}
				if hiddenMembersFor(store)(id) {
					t.Fatal("changed asset/lineage information was hidden by a stale digest")
				}
			}
			if _, err := d.Exec("UPDATE explorations SET round_no=100 WHERE id=$1", task.ExplorationID); err != nil {
				t.Fatal(err)
			}
			if err := store.ApplyStampOps([]db.StampOp{{ID: members[0], Set: true, Round: 1}, {ID: members[1], Set: true, Round: 1}}); err != nil {
				t.Fatal(err)
			}
			compactor.major(t.Context(), store)
			active, err = store.ActiveDigests()
			if err != nil || len(active) != 1 || active[0].ID == oldDigest || provider.calls != 2 {
				t.Fatalf("same-version source change did not replace the digest: calls=%d active=%v err=%v", provider.calls, active, err)
			}
			if !hiddenMembersFor(store)(members[0]) {
				t.Fatal("fresh replacement did not fold unchanged sources")
			}
		})
	}
}

func TestCompressionRejectsSourceChangedBeforeVersionRead(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("concurrent compression source", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	store := d.Exploration(task.ExplorationID)
	var members []int64
	for range 2 {
		id, err := store.AddNode(db.KindFact, map[string]any{"summary": "old observation"}, 0, "confirmed", "fixture", nil)
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, id)
	}
	graph, oldNodes, err := loadColdGraph(store)
	if err != nil {
		t.Fatal(err)
	}
	// This is the original minor/major interleaving: source read, a concurrent
	// fact change, then the independent content-version read.
	if _, err := d.Exec(`UPDATE exploration_nodes SET payload=jsonb_set(payload,'{summary}','"NEW-UNFINISHED-LEAD"'),content_version=content_version+1 WHERE id=$1`, members[0]); err != nil {
		t.Fatal(err)
	}
	versions, err := store.ContentVersions()
	if err != nil {
		t.Fatal(err)
	}
	NewCompactor(&graphSummaryProvider{stop: "end_turn"}, "fixture").foldBlock(t.Context(), store, graph, block{Members: members}, oldNodes, versions, 1)
	digests, err := store.ActiveDigests()
	if err != nil {
		t.Fatal(err)
	}
	if len(digests) != 0 || hiddenMembersFor(store)(members[0]) {
		t.Fatal("old source was saved with a new content version and hid the updated observation")
	}
}

func TestDigestContextDistinguishesCompleteFromCurrentSource(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("digest freshness context", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	store := d.Exploration(task.ExplorationID)
	var members []int64
	for range 2 {
		id, err := store.AddNode(db.KindFact, map[string]any{"summary": "original fact"}, 0, "confirmed", "fixture", nil)
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, id)
	}
	legacyID, err := store.AddDigest(map[string]any{"body": "legacy summary"}, members)
	if err != nil {
		t.Fatal(err)
	}
	graph, nodes, versions, err := loadCompressionSnapshot(store)
	if err != nil {
		t.Fatal(err)
	}
	NewCompactor(&graphSummaryProvider{stop: "end_turn"}, "fixture").foldBlock(t.Context(), store, graph, block{Members: members}, nodes, versions, 1)
	digests, err := store.ActiveDigests()
	if err != nil || len(digests) != 2 {
		t.Fatalf("digests=%v err=%v", digests, err)
	}
	completeID := digests[1].ID
	tools := NewToolSet(store, "planner")
	assertContext := func(id int64, complete, current bool) {
		t.Helper()
		shown, _ := coldDigestsRecent(store, 8)
		var overview map[string]any
		for _, item := range shown {
			if item["id"] == id {
				overview = item
			}
		}
		expanded := callReadJSON(t, tools.expandDigest(), fmt.Sprintf(`{"id":%d}`, id)).(map[string]any)
		for _, entry := range []map[string]any{overview, expanded} {
			if entry == nil || entry["input_complete"] != complete || entry["source_current"] != current {
				t.Fatalf("digest %d complete/current flags: %+v", id, entry)
			}
			if !current && entry["source_note"] != staleDigestNote {
				t.Fatal("historical digest lost its original-first guidance")
			}
		}
	}
	assertContext(legacyID, false, false)
	assertContext(completeID, true, true)
	if err := store.SetNodeState(members[0], "dismissed"); err != nil {
		t.Fatal(err)
	}
	assertContext(completeID, true, false)
}
