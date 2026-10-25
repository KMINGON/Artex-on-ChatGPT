package db

import (
	"context"
	"testing"
)

func TestGraphPagePreservesConnectedNeighboursAndCursor(t *testing.T) {
	d, _ := templateFixtureDB(t)
	task, err := d.CreateTask("graph pagination", "observe", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	store := d.Exploration(task.ExplorationID)
	origin, err := store.OriginFactID()
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{origin}
	for range 4 {
		id, err := store.AddNode(KindFact, map[string]any{"summary": "observation"}, 0, "confirmed", "worker", nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := store.Link(ids[4], RelDerivedFrom, origin); err != nil {
		t.Fatal(err)
	}
	first, err := store.GraphPageWithSources(context.Background(), 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if first.TotalNodes != 5 || first.TotalEdges != 1 || first.PageNodes != 2 || !first.HasMore || first.NextBefore != ids[3] {
		t.Fatalf("wrong page metadata: %+v", first)
	}
	if len(first.Nodes) != 3 || first.Nodes[0].ID != ids[4] || first.Nodes[1].ID != ids[3] {
		t.Fatalf("page lost its connected old neighbour: %+v", first.Nodes)
	}
	seen := map[int64]bool{}
	for _, n := range first.Nodes {
		seen[n.ID] = true
	}
	for _, e := range first.Edges {
		if !seen[e.From] || !seen[e.To] {
			t.Fatal("dangling edge")
		}
	}
	second, err := store.GraphPageWithSources(context.Background(), first.NextBefore, 2)
	if err != nil {
		t.Fatal(err)
	}
	if second.Nodes[0].ID != ids[2] || second.NextBefore != ids[1] || !second.HasMore {
		t.Fatalf("old neighbour skipped history: %+v", second)
	}
	last, err := store.GraphPageWithSources(context.Background(), second.NextBefore, 2)
	if err != nil {
		t.Fatal(err)
	}
	if last.PageNodes != 1 || last.HasMore || last.NextBefore != origin {
		t.Fatalf("last page %+v", last)
	}
}

func TestGraphPageKeepsDirectSourceReadBoundary(t *testing.T) {
	d, _ := templateFixtureDB(t)
	grand, err := d.CreateTask("grandparent", "", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	source, err := d.CreateTaskWithOptions("source", "", TaskCreateOptions{SourceTaskIDs: []int64{grand.ID}})
	if err != nil {
		t.Fatal(err)
	}
	target, err := d.CreateTaskWithOptions("target", "", TaskCreateOptions{SourceTaskIDs: []int64{source.ID}})
	if err != nil {
		t.Fatal(err)
	}
	sourceStore := d.Exploration(source.ExplorationID)
	fact, err := sourceStore.AddNode(KindFact, map[string]any{"summary": "source result"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	live, err := sourceStore.AddIntent(map[string]any{"summary": "live frontier"}, 3, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	done, err := sourceStore.AddIntent(map[string]any{"summary": "finished"}, 3, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := sourceStore.SetNodeState(done, "done"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{live, done} {
		if err := sourceStore.Link(fact, RelDerivedFrom, id); err != nil {
			t.Fatal(err)
		}
	}
	page, err := d.Exploration(target.ExplorationID).GraphPageWithSources(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	// target origin + source origin/fact/terminal intent, no transitive grandparent.
	if page.TotalNodes != 4 || page.TotalEdges != 1 {
		t.Fatalf("source boundary/counts: %+v", page)
	}
	for _, n := range page.Nodes {
		if n.ID == live {
			t.Fatal("live source frontier leaked")
		}
		if n.ID == fact || n.ID == done {
			if !n.Inherited || n.SourceTaskID != source.ID {
				t.Fatalf("missing provenance %+v", n)
			}
		}
	}
	if err := sourceStore.SetNodeState(done, "open"); err != nil {
		t.Fatal(err)
	}
	updated, err := d.Exploration(target.ExplorationID).GraphPageWithSources(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if updated.TotalNodes != 3 || updated.TotalEdges != 0 {
		t.Fatalf("reopened source intent retained: %+v", updated)
	}
}

func TestGraphPageDoesNotSilentlyCutOffEdgesOrLatestNodes(t *testing.T) {
	d, _ := templateFixtureDB(t)
	task, err := d.CreateTask("large graph", "", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO exploration_nodes(exploration_id,kind,payload,state)
 SELECT $1,'fact','{}'::jsonb,'confirmed' FROM generate_series(1,101)`, task.ExplorationID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id)
 SELECT $1,a.id,'derived_from',b.id FROM exploration_nodes a JOIN exploration_nodes b ON a.id<b.id
 WHERE a.exploration_id=$1 AND b.exploration_id=$1`, task.ExplorationID); err != nil {
		t.Fatal(err)
	}
	store := d.Exploration(task.ExplorationID)
	page, err := store.GraphPageWithSources(context.Background(), 0, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if page.TotalEdges <= 5000 || len(page.Edges) != page.TotalEdges {
		t.Fatalf("edges truncated %d/%d", len(page.Edges), page.TotalEdges)
	}
	if _, err := d.Exec(`INSERT INTO exploration_nodes(exploration_id,kind,payload,state)
 SELECT $1,'fact','{}'::jsonb,'confirmed' FROM generate_series(1,2100)`, task.ExplorationID); err != nil {
		t.Fatal(err)
	}
	page, err = store.GraphPageWithSources(context.Background(), 0, 99999)
	if err != nil {
		t.Fatal(err)
	}
	if page.PageNodes != 2000 || !page.HasMore || page.TotalNodes != 2202 {
		t.Fatalf("large graph metadata %+v", page)
	}
	tail, err := store.GraphPageWithSources(context.Background(), page.NextBefore, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if tail.PageNodes != 202 || tail.HasMore || len(tail.Edges) != tail.TotalEdges {
		t.Fatalf("history unreachable %+v", tail)
	}
}
