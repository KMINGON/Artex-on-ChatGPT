package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestLLMUsageMetadataMigrationAndLegacyArchive(t *testing.T) {
	d, _ := templateFixtureDB(t)
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	if err := d.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`ALTER TABLE llm_usage DROP COLUMN metadata; INSERT INTO llm_usage(profile_name,status,input_tokens) VALUES('pre-migration','ok',7)`); err != nil {
		t.Fatal(err)
	}
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	var oldMeta []byte
	var input int
	var received *bool
	if err := d.QueryRow(`SELECT metadata,input_tokens,usage_received FROM llm_usage WHERE profile_name='pre-migration'`).Scan(&oldMeta, &input, &received); err != nil {
		t.Fatal(err)
	}
	if string(oldMeta) != "{}" || input != 7 || received != nil {
		t.Fatalf("migration invented old details: %s %d %v", oldMeta, input, received)
	}
	task, _ := graphMutationTask(t, d)
	metadata := json.RawMessage(`{"requested_model":"requested","requested_program":"daybreak_blue","response":{"model":"actual","access_programs_present":false}}`)
	for _, usage := range []LLMUsage{{TaskID: fmt.Sprint(task.ID), ExplorationID: task.ExplorationID, ProfileName: "legacy", InputTokens: 2, Status: "ok"}, {TaskID: fmt.Sprint(task.ID), ExplorationID: task.ExplorationID, ProfileName: "new", InputTokens: 5, Status: "ok", Metadata: metadata}} {
		if err := d.InsertLLMUsage(&usage); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
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
	// Old packages have no metadata member. json_populate_recordset must accept
	// this shape without changing unknown runtime details into Standard.
	rows, err := decodeArchiveRows(snapshot.Tables["llm_usage"])
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row["profile_name"] == "legacy" {
			delete(row, "metadata")
		}
	}
	snapshot.Tables["llm_usage"], err = json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CompleteTaskArchive(archive.ID, snapshot, "/tmp/usage-metadata-fixture.tar.zst", "fixture", 1, 1); err != nil {
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
	var legacy []byte
	if err := d.QueryRow(`SELECT metadata,usage_received FROM llm_usage WHERE profile_name='legacy'`).Scan(&legacy, &received); err != nil {
		t.Fatal(err)
	}
	if len(legacy) != 0 || received != nil {
		t.Fatalf("legacy archive invented data: %s %v", legacy, received)
	}
	var restored []byte
	if err := d.QueryRow(`SELECT metadata FROM llm_usage WHERE profile_name='new'`).Scan(&restored); err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(restored, &value); err != nil {
		t.Fatal(err)
	}
	response := value["response"].(map[string]any)
	if value["requested_program"] != "daybreak_blue" || response["access_programs_present"] != false || response["model"] != "actual" || strings.Contains(string(restored), "standard") {
		t.Fatalf("unknown actual program was replaced: %s", restored)
	}
	if _, exists := response["access_program_cyber"]; exists {
		t.Fatal("invented actual cyber program")
	}
	if total := graphCount(t, d, `SELECT sum(input_tokens)::int FROM llm_usage WHERE task_id=$1`, fmt.Sprint(task.ID)); total != 7 {
		t.Fatalf("usage changed across schema/archive: %d", total)
	}
}
