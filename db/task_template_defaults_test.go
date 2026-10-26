package db

import (
	"reflect"
	"testing"
)

// Legacy migration fixtures need the state before any automatic templates existed.
func resetDefaultTaskTemplateSeed(t *testing.T, d *DB) {
	t.Helper()
	if _, err := d.Exec(`DELETE FROM task_templates; DELETE FROM settings WHERE key='default_task_templates_v1'`); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultTaskTemplatesSeedOnceAndPreserveUserChanges(t *testing.T) {
	d, dsn := templateFixtureDB(t)
	list, err := d.ListTaskTemplates()
	if err != nil || len(list) != 4 {
		t.Fatalf("default templates: count=%d err=%v", len(list), err)
	}
	wantCategories := map[string]string{
		"실습 · 전체 검증": "실습", "운영 · 관찰": "운영",
		"운영 · 일반 검증": "운영", "운영 · 확장 검증": "운영",
	}
	for _, item := range list {
		category, known := wantCategories[item.Name]
		if !known || item.Description == "" || item.Goal == "" || item.CategoryID == nil {
			t.Fatalf("incomplete default template: %+v", item)
		}
		var categoryName string
		if err := d.QueryRow(`SELECT name FROM task_categories WHERE id=$1`, item.CategoryID).Scan(&categoryName); err != nil || categoryName != category {
			t.Fatalf("template category: %q %v", categoryName, err)
		}
		if item.TaskName != nil || item.TimeoutSeconds != nil || item.PlanHeartbeatSeconds != nil || item.SeedFirstIntent != nil || item.CoverageEnabled != nil || len(item.InterceptRules) != 0 {
			t.Fatalf("default template overrides execution settings: %+v", item)
		}
	}
	unchangedTask, err := d.CreateTask(list[1].Description, list[1].Goal, nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	description, goal, taskName, zero, off := "사용자 설명", "사용자 목표", "사용자 작업명", 0, false
	edited, err := d.PatchTaskTemplate(list[0].ID, TaskTemplatePatch{
		Description: &description, Goal: &goal, SetTaskName: true, TaskName: &taskName,
		SetTimeoutSeconds: true, TimeoutSeconds: &zero, SetCoverageEnabled: true, CoverageEnabled: &off,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DeleteTaskTemplate(list[1].ID); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, err := reopened.ListTaskTemplates()
	if err != nil || len(after) != 3 {
		t.Fatalf("deleted template restored on restart: count=%d err=%v", len(after), err)
	}
	got, err := reopened.GetTaskTemplate(edited.ID)
	if err != nil || !reflect.DeepEqual(got, edited) {
		t.Fatalf("edited template changed on restart: got=%+v want=%+v err=%v", got, edited, err)
	}
	if deleted, err := reopened.GetTaskTemplate(list[1].ID); err != nil || deleted != nil {
		t.Fatalf("deleted template remains: %+v %v", deleted, err)
	}
	task, err := reopened.GetTask(unchangedTask.ID)
	if err != nil || task.Description != unchangedTask.Description || task.Goal != unchangedTask.Goal {
		t.Fatal("template deletion changed an existing task")
	}
	rootDescription, rootGoal, err := reopened.Exploration(unchangedTask.ExplorationID).Root()
	if err != nil || rootDescription != unchangedTask.Description || rootGoal != unchangedTask.Goal {
		t.Fatal("template deletion changed an existing exploration")
	}
}

func TestDefaultTaskTemplatesRespectPreviousSeedMarker(t *testing.T) {
	d, dsn := templateFixtureDB(t)
	resetDefaultTaskTemplateSeed(t, d)
	if err := d.SetSetting("default_task_templates_v1", "done"); err != nil {
		t.Fatal(err)
	}
	if err := d.seedDefaultTaskTemplates(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	list, err := reopened.ListTaskTemplates()
	if err != nil || len(list) != 0 {
		t.Fatalf("previously deleted defaults restored: count=%d err=%v", len(list), err)
	}
}

func TestDefaultTaskTemplatesPreserveExistingNames(t *testing.T) {
	d, _ := templateFixtureDB(t)
	resetDefaultTaskTemplateSeed(t, d)
	timeout, coverage := 123, false
	existing, err := d.CreateTaskTemplate(TaskTemplateInput{
		Name: "운영 · 관찰", Description: "사용자가 만든 설명", Goal: "사용자가 만든 목표",
		TimeoutSeconds: &timeout, CoverageEnabled: &coverage,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.seedDefaultTaskTemplates(); err != nil {
		t.Fatal(err)
	}
	list, err := d.ListTaskTemplates()
	if err != nil || len(list) != 4 {
		t.Fatalf("remaining defaults not seeded: count=%d err=%v", len(list), err)
	}
	got, err := d.GetTaskTemplate(existing.ID)
	if err != nil || !reflect.DeepEqual(got, existing) {
		t.Fatalf("existing template overwritten: got=%+v want=%+v err=%v", got, existing, err)
	}
}

func TestDefaultTaskTemplateSeedRollsBackAndRetries(t *testing.T) {
	d, _ := templateFixtureDB(t)
	resetDefaultTaskTemplateSeed(t, d)
	if _, err := d.Exec(`DELETE FROM task_categories; CREATE FUNCTION reject_template_seed() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.name='운영 · 관찰' THEN RAISE EXCEPTION 'fixture seed failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_template_seed BEFORE INSERT ON task_templates FOR EACH ROW EXECUTE FUNCTION reject_template_seed()`); err != nil {
		t.Fatal(err)
	}
	if err := d.seedDefaultTaskTemplates(); err == nil {
		t.Fatal("expected seed failure")
	}
	var templates, categories, marker int
	if err := d.QueryRow(`SELECT (SELECT count(*) FROM task_templates), (SELECT count(*) FROM task_categories), (SELECT count(*) FROM settings WHERE key='default_task_templates_v1')`).Scan(&templates, &categories, &marker); err != nil {
		t.Fatal(err)
	}
	if templates != 0 || categories != 0 || marker != 0 {
		t.Fatalf("failed seed partially committed: templates=%d categories=%d marker=%d", templates, categories, marker)
	}
	if _, err := d.Exec(`DROP TRIGGER reject_template_seed ON task_templates`); err != nil {
		t.Fatal(err)
	}
	if err := d.seedDefaultTaskTemplates(); err != nil {
		t.Fatal(err)
	}
	list, err := d.ListTaskTemplates()
	if err != nil || len(list) != 4 {
		t.Fatalf("retry did not seed defaults: count=%d err=%v", len(list), err)
	}
	var seeded string
	if err := d.QueryRow(`SELECT value FROM settings WHERE key='default_task_templates_v1'`).Scan(&seeded); err != nil || seeded != "done" {
		t.Fatalf("retry did not save completion marker: %q %v", seeded, err)
	}
}
