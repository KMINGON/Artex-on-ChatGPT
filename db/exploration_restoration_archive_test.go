package db

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
)

func TestExplorationRestorationArchivePreservesDigestAndRunBoundaries(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires an explicitly configured isolated ARTEX_PG_DSN")
	}
	d, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	task, err := d.CreateTaskWithOptions("archive exploration state", "keep unfinished verification", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = d.Exec(`DELETE FROM task_archives WHERE task_id=$1`, task.ID)
		_ = d.DeleteTask(task.ID)
	}()
	store := d.Exploration(task.ExplorationID)
	origin, err := store.OriginFactID()
	if err != nil {
		t.Fatal(err)
	}
	asset, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: fmt.Sprintf("archive-exploration-%d.example", task.ID), TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	fact, err := store.AddNode(KindFact, map[string]any{"summary": "retained evidence", "detail": "unfinished comparison source", "asset_ids": []int64{asset}}, 1, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Anchor(fact, asset); err != nil {
		t.Fatal(err)
	}
	digest, err := store.AddDigest(map[string]any{
		"body": "summary with source provenance", "input_complete": true,
		"source_signature": "original-source-signature", "signature": "original-version-signature", "generation": 1,
		"member_ids": map[string]any{"facts": []int64{fact}, "intents": []int64{}}, "anchor_ids": []int64{origin},
	}, []int64{fact})
	if err != nil {
		t.Fatal(err)
	}
	beforeDigest, err := store.GetNode(digest)
	if err != nil {
		t.Fatal(err)
	}
	makeIntent := func(label string) int64 {
		t.Helper()
		id, err := store.AddIntent(map[string]any{"summary": label}, 1, nil, "planner")
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	working, preparing := makeIntent("current run has partial evidence"), makeIntent("next attempt is preparing")
	appendActivity := func(intent int64, kind, detail, metadata string) int64 {
		t.Helper()
		id, err := store.AppendActivity(Activity{NodeID: &intent, Worker: "worker", Kind: kind, Detail: detail, Metadata: json.RawMessage(metadata)})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	appendActivity(working, "result", "old completed result", `{"run_id":"old"}`)
	appendActivity(working, "round", "prepare new run", `{"worker_preparation_start":true}`)
	runBoundary := appendActivity(working, "round", "new run", `{"run_start":true,"run_id":"current","session_id":"worker-session"}`)
	partial := appendActivity(working, "text", "current unfinished comparison", `{"run_id":"current","session_id":"worker-session"}`)
	appendActivity(working, "result", "late old completion", `{"run_id":"old"}`)
	appendActivity(preparing, "result", "previous result", `{"run_id":"previous"}`)
	appendActivity(preparing, "round", "preparation boundary", `{"worker_preparation_start":true}`)
	appendActivity(preparing, "result", "late previous result", `{"run_id":"previous"}`)
	if err := d.SetPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}
	archive, err := d.QueueTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := d.ClaimTaskArchiveJob(t.Context()); err != nil || claimed == nil || claimed.ID != archive.ID {
		t.Fatalf("archive claim: %+v %v", claimed, err)
	}
	snapshot, err := d.SnapshotTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CompleteTaskArchive(archive.ID, snapshot, "fixture.tar.zst", "fixture", 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := d.QueueTaskArchiveRestore(archive.ID); err != nil {
		t.Fatal(err)
	}
	if claimed, err := d.ClaimTaskArchiveJob(t.Context()); err != nil || claimed == nil || claimed.ID != archive.ID {
		t.Fatalf("restore claim: %+v %v", claimed, err)
	}
	if _, err := d.RestoreTaskArchive(archive.ID, snapshot, 0); err != nil {
		t.Fatal(err)
	}
	got, err := store.LatestWorkerOutput(working)
	if err != nil || got == nil || got.ID != partial || got.Detail != "current unfinished comparison" {
		t.Fatalf("restored current result: %+v %v", got, err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(got.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["run_id"] != "current" || metadata["session_id"] != "worker-session" || metadata["run_start_activity_id"] != float64(runBoundary) {
		t.Fatalf("restored boundary metadata: %s", got.Metadata)
	}
	if got, err := store.LatestWorkerOutput(preparing); err != nil || got != nil {
		t.Fatalf("restored preparation reused the previous result: %+v %v", got, err)
	}
	afterDigest, err := store.GetNode(digest)
	if err != nil || afterDigest == nil {
		t.Fatalf("restored digest: %+v %v", afterDigest, err)
	}
	var beforePayload, afterPayload map[string]any
	if err := json.Unmarshal(beforeDigest.Payload, &beforePayload); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(afterDigest.Payload, &afterPayload); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforePayload, afterPayload) {
		t.Fatalf("restore changed digest completeness, signatures, members or graph anchors: %s", afterDigest.Payload)
	}
	if members, err := store.DigestMembers(digest); err != nil || !reflect.DeepEqual(members, []int64{fact}) {
		t.Fatalf("restored covers edges: %v %v", members, err)
	}
	var restoredAsset int64
	if err := d.QueryRow(`SELECT asset_id FROM exploration_anchors WHERE node_id=$1`, fact).Scan(&restoredAsset); err != nil || restoredAsset != asset {
		t.Fatalf("restored asset anchor: %d %v", restoredAsset, err)
	}
	if err := d.CompleteTaskArchiveRestore(archive.ID); err != nil {
		t.Fatal(err)
	}
}
