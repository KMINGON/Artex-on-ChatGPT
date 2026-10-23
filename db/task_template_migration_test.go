package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// These startup migrations mutate fixed seed names, so each test owns its database.
func templateFixtureDB(t *testing.T) (*DB, string) {
	t.Helper()
	source := os.Getenv("ARTEX_PG_DSN")
	if source == "" {
		t.Skip("requires an explicitly configured isolated ARTEX_PG_DSN")
	}
	parsed, err := url.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("pgx", source)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("artex_template_fixture_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE DATABASE "` + name + `"`); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var d *DB
	t.Cleanup(func() {
		if d != nil {
			_ = d.Close()
		}
		if _, err := admin.Exec(`DROP DATABASE "` + name + `" WITH (FORCE)`); err != nil {
			t.Error(err)
		}
		_ = admin.Close()
	})
	parsed.Path = "/" + name
	d, err = Open(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	return d, parsed.String()
}

func TestDefaultTaskTemplatesRespectNameConflictsEditsAndDeletion(t *testing.T) {
	d, dsn := templateFixtureDB(t)
	if _, err := d.Exec(`DELETE FROM task_templates`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`DELETE FROM settings WHERE key=$1`, defaultTaskTemplatesFlag); err != nil {
		t.Fatal(err)
	}
	seeds := defaultTaskTemplateSeeds()
	user, err := d.CreateTaskTemplate(TaskTemplateInput{Name: seeds[0].Input.Name, Description: "사용자가 먼저 작성한 템플릿", Goal: "기존 목표"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE task_categories SET name='사용자가 지정한 운영 분류명' WHERE nkey='운영'`); err != nil {
		t.Fatal(err)
	}
	if err := d.seedDefaultTaskTemplates(); err != nil {
		t.Fatal(err)
	}
	list, err := d.ListTaskTemplates()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 4 {
		t.Fatalf("seed conflict aborted remaining inserts: %d", len(list))
	}
	for _, item := range list {
		if item.ID == user.ID {
			if item.Description != user.Description || item.TaskName != nil {
				t.Fatal("name conflict overwrote user template")
			}
			continue
		}
		if item.TaskName == nil || item.TimeoutSeconds == nil || *item.TimeoutSeconds != 0 || item.PlanHeartbeatSeconds == nil || *item.PlanHeartbeatSeconds != 600 || item.SeedFirstIntent == nil || *item.SeedFirstIntent || item.CoverageEnabled == nil || !*item.CoverageEnabled || len(item.InterceptRules) != 0 || item.CategoryID == nil {
			t.Fatalf("incomplete ordinary template: %+v", item)
		}
	}
	var category string
	if err := d.QueryRow(`SELECT name FROM task_categories WHERE nkey='운영'`).Scan(&category); err != nil || category != "사용자가 지정한 운영 분류명" {
		t.Fatal("seed overwrote existing category")
	}
	edited := list[0]
	if edited.ID == user.ID {
		edited = list[1]
	}
	description := "사용자가 편집한 기본 템플릿"
	if _, err := d.PatchTaskTemplate(edited.ID, TaskTemplatePatch{Description: &description}); err != nil {
		t.Fatal(err)
	}
	var deletedID int64
	for _, item := range list {
		if item.ID != user.ID && item.ID != edited.ID {
			deletedID = item.ID
			break
		}
	}
	if _, err := d.DeleteTaskTemplate(deletedID); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	kept, err := reopened.GetTaskTemplate(edited.ID)
	if err != nil || kept.Description != description {
		t.Fatal("restart replaced edited seed")
	}
	current, err := reopened.ListTaskTemplates()
	if err != nil || len(current) != 3 {
		t.Fatal("restart restored a deleted seed")
	}
}

func TestTemplateNullableExecutionFieldsRoundTrip(t *testing.T) {
	d, _ := templateFixtureDB(t)
	old, err := d.CreateTaskTemplate(TaskTemplateInput{Name: "old format", Description: "description", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	if old.TaskName != nil || old.TimeoutSeconds != nil || old.PlanHeartbeatSeconds != nil || old.SeedFirstIntent != nil || old.CoverageEnabled != nil {
		t.Fatal("old template acquired task-form defaults")
	}
	empty, zero, heartbeat, off := "", 0, 600, false
	updated, err := d.PatchTaskTemplate(old.ID, TaskTemplatePatch{SetTaskName: true, TaskName: &empty, SetTimeoutSeconds: true, TimeoutSeconds: &zero, SetPlanHeartbeatSeconds: true, PlanHeartbeatSeconds: &heartbeat, SetSeedFirstIntent: true, SeedFirstIntent: &off, SetCoverageEnabled: true, CoverageEnabled: &off})
	if err != nil {
		t.Fatal(err)
	}
	if updated.TaskName == nil || *updated.TaskName != "" || updated.TimeoutSeconds == nil || *updated.TimeoutSeconds != 0 || updated.SeedFirstIntent == nil || *updated.SeedFirstIntent || updated.CoverageEnabled == nil || *updated.CoverageEnabled {
		t.Fatal("explicit empty/zero/false became unset")
	}
	desc := "only description changed"
	updated, err = d.PatchTaskTemplate(old.ID, TaskTemplatePatch{Description: &desc})
	if err != nil || updated.TimeoutSeconds == nil || updated.CoverageEnabled == nil {
		t.Fatal("unrelated patch dropped execution settings")
	}
	updated, err = d.PatchTaskTemplate(old.ID, TaskTemplatePatch{SetTaskName: true, SetTimeoutSeconds: true, SetPlanHeartbeatSeconds: true, SetSeedFirstIntent: true, SetCoverageEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if updated.TaskName != nil || updated.TimeoutSeconds != nil || updated.PlanHeartbeatSeconds != nil || updated.SeedFirstIntent != nil || updated.CoverageEnabled != nil {
		t.Fatal("cannot reset nullable fields")
	}
	negative := -1
	if _, err := d.PatchTaskTemplate(old.ID, TaskTemplatePatch{SetTimeoutSeconds: true, TimeoutSeconds: &negative}); err == nil {
		t.Fatal("negative timeout accepted")
	}
}

func TestLegacyTaskPolicyMigrationPreservesOriginalAndIsAtomic(t *testing.T) {
	d, dsn := templateFixtureDB(t)
	task, err := d.CreateTask("사용자 작업 설명", "사용자 목표", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"id":"observe","version":2,"name":"저장된 이전 설정","instructions":"저장된 실행 절차","constraints":[{"kind":"deny","text":"사용자 자료 변경 금지"}]}`
	if _, err := d.Exec(`UPDATE tasks SET test_preset='observe',test_policy=$2 WHERE id=$1`, task.ID, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exploration(task.ExplorationID).AddConstraint("deny", "사용자 자료 변경 금지", "human"); err != nil {
		t.Fatal(err)
	}
	broken, err := d.CreateTask("broken legacy", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE tasks SET test_policy='{"constraints":[{"kind":"invalid","text":"fixture"}]}' WHERE id=$1`, broken.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.migrateLegacyTaskPolicies(); err == nil {
		t.Fatal("invalid legacy rule did not roll back migration")
	}
	var description string
	var marked bool
	if err := d.QueryRow(`SELECT description,test_policy_migrated_at IS NOT NULL FROM tasks WHERE id=$1`, task.ID).Scan(&description, &marked); err != nil || description != task.Description || marked {
		t.Fatal("failed migration partially committed")
	}
	if _, err := d.Exec(`UPDATE tasks SET test_policy='null' WHERE id=$1`, broken.ID); err != nil {
		t.Fatal(err)
	}
	oldTemplate, err := d.CreateTaskTemplate(TaskTemplateInput{Name: "old policy template", Description: "사용자 템플릿 설명", Goal: "목표"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE task_templates SET test_preset='observe' WHERE id=$1`, oldTemplate.ID); err != nil {
		t.Fatal(err)
	}
	deleted, err := d.CreateTask("deleted legacy", "deleted goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE tasks SET test_policy=$2,deleted_at=now() WHERE id=$1`, deleted.ID, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE explorations SET description=NULL WHERE id=$1`, deleted.ExplorationID); err != nil {
		t.Fatal(err)
	}
	if err := d.migrateLegacyTaskPolicies(); err != nil {
		t.Fatal(err)
	}
	var deletedMigrated bool
	if err := d.QueryRow(`SELECT test_policy_migrated_at IS NOT NULL FROM tasks WHERE id=$1`, deleted.ID).Scan(&deletedMigrated); err != nil || !deletedMigrated {
		t.Fatal("deleted task with a nullable exploration description was not migrated")
	}
	current, err := d.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(current.Description, task.Description) || !strings.Contains(current.Description, "저장된 실행 절차") || current.Goal != task.Goal {
		t.Fatal("stored user text or instructions lost")
	}
	var expDescription, originDescription, originSummary, archivedJSON string
	if err := d.QueryRow(`SELECT e.description,n.payload->>'description',n.payload->>'summary',t.test_policy::text FROM tasks t JOIN explorations e ON e.id=t.exploration_id JOIN exploration_nodes n ON n.exploration_id=e.id AND n.state='origin' WHERE t.id=$1`, task.ID).Scan(&expDescription, &originDescription, &originSummary, &archivedJSON); err != nil {
		t.Fatal(err)
	}
	if expDescription != current.Description || originDescription != current.Description || !strings.Contains(originSummary, "사용자 작업 설명") || !strings.Contains(originSummary, "저장된 실행 절차") || !strings.Contains(archivedJSON, "저장된 실행 절차") {
		t.Fatal("exploration/origin/archive text not preserved")
	}
	rules, err := d.Exploration(task.ExplorationID).ListConstraints()
	if err != nil || len(rules) != 1 || rules[0].Origin != "human" {
		t.Fatal("migration duplicated/replaced user constraint")
	}
	migratedTemplate, err := d.GetTaskTemplate(oldTemplate.ID)
	if err != nil || !strings.HasPrefix(migratedTemplate.Description, oldTemplate.Description) || !strings.Contains(migratedTemplate.Description, legacyV1TaskPolicy("observe").Constraints[1].Text) || migratedTemplate.TaskName != nil {
		t.Fatal("legacy template did not retain its old rule text and optional values")
	}
	reopened, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	repeat, err := reopened.GetTask(task.ID)
	if err != nil || repeat.Description != current.Description {
		t.Fatal("restart appended policy twice")
	}
	rules, err = reopened.Exploration(task.ExplorationID).ListConstraints()
	if err != nil || len(rules) != 1 {
		t.Fatal("restart duplicated constraints")
	}
	encoded, _ := json.Marshal(repeat)
	if strings.Contains(string(encoded), `"test_policy"`) || strings.Contains(string(encoded), `"test_preset"`) {
		t.Fatal("retired policy remains in runtime DTO")
	}
}

func TestArchivedLegacyPolicyMigratesAfterRestore(t *testing.T) {
	d, _ := templateFixtureDB(t)
	if err := d.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	task, err := d.CreateTaskWithOptions("archive original", "unchanged goal", TaskCreateOptions{ExecutionProfile: &ExecutionProfile{ID: "low", Version: 1, Workers: 2, BrowserLimit: 2}})
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"version":1,"name":"archive policy","constraints":[{"kind":"deny","text":"archived constraint"}]}`
	if _, err := d.Exec(`UPDATE tasks SET test_policy=$2,paused=true WHERE id=$1`, task.ID, raw); err != nil {
		t.Fatal(err)
	}
	archive, err := d.QueueTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClaimTaskArchiveJob(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := d.SnapshotTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := decodeArchiveRows(snapshot.Tables["tasks"])
	if err != nil {
		t.Fatal(err)
	}
	delete(rows[0], "test_policy_migrated_at") // Packages created before this migration have no marker column.
	snapshot.Tables["tasks"], _ = json.Marshal(rows)
	if err := d.CompleteTaskArchive(archive.ID, snapshot, "fixture.tar.zst", "fixture", 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := d.migrateLegacyTaskPolicies(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := d.QueryRow(`SELECT count(*) FROM task_constraints WHERE exploration_id=$1`, task.ExplorationID).Scan(&count); err != nil || count != 0 {
		t.Fatal("migration created constraints in empty archive stub")
	}
	// Restoration must use the manifest resource snapshot, not current stub values.
	if _, err := d.Exec(`UPDATE tasks SET execution_profile='null' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.QueueTaskArchiveRestore(archive.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClaimTaskArchiveJob(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RestoreTaskArchive(archive.ID, snapshot, 0); err != nil {
		t.Fatal(err)
	}
	restored, err := d.GetTask(task.ID)
	if err != nil || restored == nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(restored.Description, "archive original") || !strings.Contains(restored.Description, "archived constraint") || restored.ExecutionProfile == nil || restored.ExecutionProfile.Workers != 2 {
		t.Fatalf("legacy archive restoration lost instructions/resources: %+v", restored)
	}
	if err := d.migrateLegacyTaskPolicies(); err != nil {
		t.Fatal(err)
	}
	rules, err := d.Exploration(task.ExplorationID).ListConstraints()
	if err != nil || len(rules) != 1 || rules[0].Text != "archived constraint" {
		t.Fatal("restored policy was lost or duplicated")
	}
}

func TestLegacyTemplateAtOriginalDescriptionLimitRemainsEditable(t *testing.T) {
	d, _ := templateFixtureDB(t)
	for _, id := range []string{"lab", "observe", "standard", "extended"} {
		if utf8.RuneCountInString(legacyPolicyText(legacyV1TaskPolicy(id)))+2 > MaxTaskTemplateDescriptionRunes-16000 {
			t.Fatalf("legacy %s exceeds migration headroom", id)
		}
	}
	original := strings.Repeat("가", 16000)
	item, err := d.CreateTaskTemplate(TaskTemplateInput{Name: "full legacy description", Description: original, Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE task_templates SET test_preset='lab' WHERE id=$1`, item.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.migrateLegacyTaskPolicies(); err != nil {
		t.Fatal(err)
	}
	migrated, err := d.GetTaskTemplate(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(migrated.Description, original) || utf8.RuneCountInString(migrated.Description) <= 16000 {
		t.Fatal("legacy text truncated")
	}
	if _, err := d.PatchTaskTemplate(item.ID, TaskTemplatePatch{Description: &migrated.Description}); err != nil {
		t.Fatalf("unchanged migrated description cannot be saved: %v", err)
	}
	tooLong := strings.Repeat("가", MaxTaskTemplateTextRunes+1)
	if _, err := d.PatchTaskTemplate(item.ID, TaskTemplatePatch{Goal: &tooLong}); err == nil {
		t.Fatal("description compatibility changed goal limit")
	}
}
