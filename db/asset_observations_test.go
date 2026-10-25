package db

import (
	"context"
	"fmt"
	"testing"
)

func TestObservedAssetAtomicScopeAndProvenance(t *testing.T) {
	d, _ := templateFixtureDB(t)
	task, s := graphMutationTask(t, d)
	intent, err := s.AddIntent(map[string]any{"summary": "register assets"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	as := d.Assets()
	in := AssetObservationContext{TaskID: task.ID, ExplorationID: s.ID(), OwnerNodeID: intent, Type: "endpoint", URL: "http://192.0.2.91:3000/api/products"}
	upsert := func(store *AssetStore) (int64, error) {
		return store.UpsertEndpoint(UpsertEndpointReq{URL: in.URL, Method: "GET", TaskID: task.ID})
	}
	if _, err := d.Exec(`CREATE FUNCTION reject_scope_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture scope failure'; END $$;
 CREATE TRIGGER reject_scope_fixture BEFORE INSERT ON task_scope FOR EACH ROW EXECUTE FUNCTION reject_scope_fixture()`); err != nil {
		t.Fatal(err)
	}
	before := graphCount(t, d, `SELECT count(*) FROM assets`)
	if id, err := as.RegisterObservedAsset(context.Background(), in, upsert); err == nil || id != 0 {
		t.Fatalf("partial asset success %d %v", id, err)
	}
	if n := graphCount(t, d, `SELECT count(*) FROM assets`); n != before {
		t.Fatalf("asset side effects survived rollback %d -> %d", before, n)
	}
	if n := graphCount(t, d, `SELECT count(*) FROM exploration_anchors WHERE node_id=$1`, intent); n != 0 {
		t.Fatalf("anchors survived: %d", n)
	}
	if n := graphCount(t, d, `SELECT count(*) FROM task_asset_links WHERE task_id=$1`, task.ID); n != 0 {
		t.Fatalf("provenance survived: %d", n)
	}
	if _, err := d.Exec(`DROP TRIGGER reject_scope_fixture ON task_scope; DROP FUNCTION reject_scope_fixture()`); err != nil {
		t.Fatal(err)
	}
	id, err := as.RegisterObservedAsset(context.Background(), in, upsert)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := as.RegisterObservedAsset(context.Background(), in, upsert)
	if err != nil || retry != id {
		t.Fatalf("retry duplicate %d %d %v", id, retry, err)
	}
	if n := graphCount(t, d, `SELECT count(*) FROM exploration_anchors WHERE node_id=$1 AND asset_id=$2`, intent, id); n != 1 {
		t.Fatalf("anchors=%d", n)
	}
	if n := graphCount(t, d, `SELECT count(*) FROM task_asset_links WHERE task_id=$1 AND asset_id=$2 AND source='agent' AND source_node_id=$3`, task.ID, id, intent); n != 1 {
		t.Fatalf("provenance=%d", n)
	}
	scopes, err := as.ListTaskScope(task.ID)
	if err != nil || len(scopes) != 1 || scopes[0].Net != "192.0.2.91/32" {
		t.Fatalf("scopes=%+v %v", scopes, err)
	}
	rows, err := as.QueryDSLInScope("", "endpoint", task.ID, 100, 0)
	if err != nil || len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("endpoint not discoverable: %+v %v", rows, err)
	}
	other, _ := graphMutationTask(t, d)
	bad := in
	bad.TaskID = other.ID
	if _, err := as.RegisterObservedAsset(context.Background(), bad, upsert); err == nil {
		t.Fatal("accepted mismatched task/exploration")
	}
}

func TestObservedIPURLCoverageAndFindingSemantics(t *testing.T) {
	d, _ := templateFixtureDB(t)
	task, s := graphMutationTask(t, d)
	as := d.Assets()
	var ids []int64
	for i, host := range []string{"192.0.2.92", "[2001:db8::92]"} {
		url := fmt.Sprintf("http://%s:3000/path%d", host, i)
		in := AssetObservationContext{TaskID: task.ID, ExplorationID: s.ID(), Type: "endpoint", URL: url}
		id, err := as.RegisterObservedAsset(context.Background(), in, func(store *AssetStore) (int64, error) {
			return store.UpsertEndpoint(UpsertEndpointReq{URL: url, Method: "GET", TaskID: task.ID})
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	scopes, err := as.ListTaskScope(task.ID)
	if err != nil || len(scopes) != 2 {
		t.Fatalf("IP literal scopes: %+v %v", scopes, err)
	}
	rows, err := as.QueryDSLInScope("", "endpoint", task.ID, 100, 0)
	if err != nil || len(rows) != 2 {
		t.Fatalf("scope list: %+v %v", rows, err)
	}
	finding, err := s.AddNode(KindFinding, map[string]any{"summary": "finding only"}, 5, "confirmed", "worker", ids[:1])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddNode(KindFact, map[string]any{"summary": "dismissed"}, 5, "dismissed", "worker", ids[1:]); err != nil {
		t.Fatal(err)
	}
	check := func(want int) {
		t.Helper()
		cov, err := as.TaskCoverageWithSources(task.ID)
		if err != nil || cov.Tested != want {
			t.Fatalf("context coverage: %+v %v", cov, err)
		}
		local, err := as.TaskCoverage(task.ID, s.ID())
		if err != nil || local.Tested != want || local.Denominator != cov.Denominator {
			t.Fatalf("local coverage differs: %+v vs %+v %v", local, cov, err)
		}
		_, untested, err := as.ListUntestedAssetsWithSources(task.ID, "endpoint", 100, 0)
		if err != nil || untested != 2-want {
			t.Fatalf("backlog=%d %v", untested, err)
		}
		graph, err := as.BuildCoverageGraph(task.ID, s.ID())
		if err != nil {
			t.Fatal(err)
		}
		observed := 0
		for _, n := range graph.Nodes {
			if n.Tested {
				observed++
			}
		}
		if observed != want {
			t.Fatalf("graph observed=%d want=%d", observed, want)
		}
	}
	check(1)
	if err := s.SetNodeState(finding, "dismissed"); err != nil {
		t.Fatal(err)
	}
	check(0)
	if _, err := d.Exec(`UPDATE tasks SET status='done',paused=true WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.QueueTaskArchive(task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := as.RegisterObservedAsset(context.Background(), AssetObservationContext{TaskID: task.ID}, func(store *AssetStore) (int64, error) {
		return store.UpsertIP(UpsertIPReq{IP: "192.0.2.99", TaskID: task.ID})
	}); err == nil {
		t.Fatal("accepted late asset after archive queued")
	}
}
