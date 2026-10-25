package db

import "testing"

func TestCoverageExcludesOriginAcrossLocalAndSourceContext(t *testing.T) {
	d, _ := templateFixtureDB(t)
	source, sourceStore := graphMutationTask(t, d)
	task, store := graphMutationTask(t, d, source.ID)
	as := d.Assets()
	addOriginAsset := func(s *ExplorationStore, taskID int64, url string) int64 {
		t.Helper()
		id, err := as.UpsertEndpoint(UpsertEndpointReq{URL: url, Method: "GET", TaskID: taskID})
		if err != nil {
			t.Fatal(err)
		}
		origin, err := s.OriginFactID()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Anchor(origin, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	sourceID := addOriginAsset(sourceStore, source.ID, "https://source.example.test/input")
	localID := addOriginAsset(store, task.ID, "https://current.example.test/input")
	check := func(wantLocal, wantContext int) {
		t.Helper()
		local, err := as.TaskCoverage(task.ID, store.ID())
		if err != nil || local.Denominator != 1 || local.Tested != wantLocal {
			t.Fatalf("local=%+v err=%v", local, err)
		}
		coverage, err := as.TaskCoverageWithSources(task.ID)
		if err != nil || coverage.Denominator != 2 || coverage.Tested != wantContext {
			t.Fatalf("source context=%+v err=%v", coverage, err)
		}
		_, untested, err := as.ListUntestedAssetsWithSources(task.ID, "endpoint", 100, 0)
		if err != nil || untested != 2-wantContext {
			t.Fatalf("unobserved=%d err=%v", untested, err)
		}
		graph, err := as.BuildCoverageGraph(task.ID, store.ID())
		if err != nil {
			t.Fatal(err)
		}
		observed := 0
		for _, node := range graph.Nodes {
			if node.Tested {
				observed++
			}
		}
		if observed != wantContext {
			t.Fatalf("graph result-linked=%d want=%d", observed, wantContext)
		}
	}
	check(0, 0)
	if _, err := sourceStore.AddNode(KindFinding, map[string]any{"summary": "source finding"}, 0, "confirmed", "worker", []int64{sourceID}); err != nil {
		t.Fatal(err)
	}
	check(0, 1)
	fact, err := store.AddNode(KindFact, map[string]any{"summary": "observed response"}, 0, "confirmed", "worker", []int64{localID})
	if err != nil {
		t.Fatal(err)
	}
	check(1, 2)
	if err := store.SetNodeState(fact, "dismissed"); err != nil {
		t.Fatal(err)
	}
	check(0, 1)
}
