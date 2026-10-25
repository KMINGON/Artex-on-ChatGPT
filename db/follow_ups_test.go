package db

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func followUpFact(t *testing.T, s *ExplorationStore, summaries ...string) int64 {
	t.Helper()
	leads, err := NewFollowUps(summaries)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.AddFactWithIntent(t.Context(), map[string]any{"summary": "old observation", "follow_ups": leads}, nil, "worker", 0)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func readFactLeads(t *testing.T, s *ExplorationStore, id int64) []FollowUp {
	t.Helper()
	node, err := s.GetNode(id)
	if err != nil || node == nil {
		t.Fatalf("legacy fact missing: %v", err)
	}
	return FollowUpsFromPayload(node.Payload)
}

func TestLegacyFollowUpPayloadReadableWithoutNewClaims(t *testing.T) {
	d, _ := templateFixtureDB(t)
	_, s := graphMutationTask(t, d)
	fact := followUpFact(t, s, "separate path")
	old := readFactLeads(t, s, fact)
	if len(old) != 1 || old[0].State != "open" || old[0].Summary != "separate path" {
		t.Fatalf("legacy payload lost: %+v", old)
	}
	items, total, err := s.ListFollowUps(false, 20, 0)
	if err != nil || total != 1 || len(items) != 1 || items[0].FactID != fact {
		t.Fatalf("legacy list lost: %+v total=%d err=%v", items, total, err)
	}
	intent, err := s.AddIntentWithParents(t.Context(), map[string]any{"summary": "verify", "follow_up_refs": []FollowUpRef{{FactID: fact, LeadID: 1}}}, 5, nil, []int64{fact}, "planner")
	if err != nil || intent == 0 {
		t.Fatalf("fact-derived intent: id=%d err=%v", intent, err)
	}
	if got := readFactLeads(t, s, fact)[0]; got.State != "open" || got.IntentID != 0 {
		t.Fatalf("legacy note gained active claim: %+v", got)
	}
	if err := s.UpdateFollowUps(t.Context(), []FollowUpUpdate{{FollowUpRef: FollowUpRef{FactID: fact, LeadID: 1}, State: "assigned", Reason: "old call"}}); err == nil || !strings.Contains(err.Error(), "종료되었습니다") {
		t.Fatalf("legacy mutation did not explain removal: %v", err)
	}
}

func TestLegacyFollowUpArchiveRestorePreservesPayload(t *testing.T) {
	d, _ := templateFixtureDB(t)
	if err := d.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	task, s := graphMutationTask(t, d)
	fact := followUpFact(t, s, "old pending lead")
	before, _ := s.GetNode(fact)
	if err := d.SetPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}
	archive, err := d.QueueTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClaimTaskArchiveJob(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := d.SnapshotTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CompleteTaskArchive(archive.ID, snapshot, "/tmp/legacy-follow-up-fixture.tar.zst", "fixture", 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := d.QueueTaskArchiveRestore(archive.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClaimTaskArchiveJob(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RestoreTaskArchive(archive.ID, snapshot, 0); err != nil {
		t.Fatal(err)
	}
	if err := d.CompleteTaskArchiveRestore(archive.ID); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetNode(fact)
	if err != nil || after == nil || string(after.Payload) != string(before.Payload) {
		t.Fatalf("legacy payload changed on restore: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(after.Payload, &payload); err != nil || payload["follow_ups"] == nil {
		t.Fatalf("restored lead unreadable: %v", err)
	}
}
