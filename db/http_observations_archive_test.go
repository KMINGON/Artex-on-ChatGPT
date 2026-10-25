package db

import "testing"

func TestHTTPObservationArchiveRemapsRecreatedAsset(t *testing.T) {
	d, task, store, intent := httpObservationFixture(t)
	if err := d.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	observation := HTTPObservation{URL: "https://app.observation.invalid/recreated", Method: "GET", Status: 200}
	result, err := d.Assets().RecordHTTPObservations(ctx, task.ID, store.ID(), intent, []HTTPObservation{observation})
	if err != nil || len(result.AssetIDs) != 1 {
		t.Fatalf("initial observation=%+v err=%v", result, err)
	}
	original := result.AssetIDs[0]
	if _, err := d.Exec(`UPDATE tasks SET status='done',paused=true WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	archive, err := d.QueueTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClaimTaskArchiveJob(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := d.SnapshotTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CompleteTaskArchive(archive.ID, snapshot, "/tmp/http-remap-fixture.tar.zst", "fixture", 1, 1); err != nil {
		t.Fatal(err)
	}
	if got := graphCount(t, d, `SELECT count(*) FROM assets WHERE id=$1`, original); got != 0 {
		t.Fatal("exclusive asset was not archived")
	}
	other, _ := graphMutationTask(t, d)
	replacement, err := d.Assets().UpsertEndpoint(UpsertEndpointReq{URL: observation.URL, Method: observation.Method, TaskID: other.ID})
	if err != nil || replacement == original {
		t.Fatalf("asset was not recreated with new ID: %d %v", replacement, err)
	}
	if _, err := d.QueueTaskArchiveRestore(archive.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClaimTaskArchiveJob(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RestoreTaskArchive(archive.ID, snapshot, 0); err != nil {
		t.Fatal(err)
	}
	if err := d.CompleteTaskArchiveRestore(archive.ID); err != nil {
		t.Fatal(err)
	}
	observed := observedPayload(t, store, result.FactID)
	if len(observed) != 1 || observed[0].AssetID != replacement || observed[0].State != "registered" {
		t.Fatalf("restored observation retains obsolete asset ID: %+v; want %d", observed, replacement)
	}
	if n := graphCount(t, d, `SELECT count(*) FROM exploration_anchors WHERE node_id=$1 AND asset_id=$2`, result.FactID, replacement); n != 1 {
		t.Fatal("restored fact anchor differs from observation payload")
	}
	if n := graphCount(t, d, `SELECT count(*) FROM task_asset_links WHERE task_id=$1 AND source_node_id=$2 AND asset_id=$3`, task.ID, result.FactID, replacement); n != 1 {
		t.Fatal("restored provenance differs from observation payload")
	}
	retry, err := d.Assets().RecordHTTPObservations(ctx, task.ID, store.ID(), intent, []HTTPObservation{observation})
	if err != nil || retry.Registered != 0 || retry.Skipped != 1 || retry.FactID != result.FactID {
		t.Fatalf("restored observation cannot resume idempotently: %+v %v", retry, err)
	}
}
