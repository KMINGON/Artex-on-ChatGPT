package db

import (
	"encoding/json"
	"os"
	"testing"
)

type forkTemplateFixture struct{ Revision, Name, Category, Description, Goal string }

func forkTemplateFixtures(t *testing.T) []forkTemplateFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/fork_task_template_defaults.json")
	if err != nil {
		t.Fatal(err)
	}
	var entries []forkTemplateFixture
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 20 {
		t.Fatalf("expected five released versions: %d", len(entries))
	}
	return entries
}
func insertForkTemplateFixture(t *testing.T, d *DB, entry forkTemplateFixture) *TaskTemplate {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO task_categories(name,nkey) VALUES($1,$1) ON CONFLICT(nkey) DO NOTHING`, entry.Category); err != nil {
		t.Fatal(err)
	}
	var category int64
	if err := d.QueryRow(`SELECT id FROM task_categories WHERE nkey=$1`, entry.Category).Scan(&category); err != nil {
		t.Fatal(err)
	}
	zero, hb, seed, coverage := 0, MinPlanHeartbeatSeconds, false, true
	row, err := d.CreateTaskTemplate(TaskTemplateInput{Name: entry.Name, TaskName: &entry.Name, Description: entry.Description, Goal: entry.Goal, CategoryID: &category, TimeoutSeconds: &zero, PlanHeartbeatSeconds: &hb, SeedFirstIntent: &seed, CoverageEnabled: &coverage})
	if err != nil {
		t.Fatal(err)
	}
	return row
}
func TestRemoveUneditedForkTemplatesKeepsExistingTaskAndCategory(t *testing.T) {
	d, _ := templateFixtureDB(t)
	resetDefaultTaskTemplateSeed(t, d)
	for _, entry := range forkTemplateFixtures(t) {
		t.Run(entry.Revision+"/"+entry.Name, func(t *testing.T) {
			if _, err := d.Exec(`DELETE FROM settings WHERE key=$1`, forkTaskTemplatesRemovedFlag); err != nil {
				t.Fatal(err)
			}
			if err := d.SetSetting("default_task_templates_v1", "done"); err != nil {
				t.Fatal(err)
			}
			row := insertForkTemplateFixture(t, d, entry)
			task, err := d.CreateTask(entry.Description, entry.Goal, nil, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			if err := d.removeUneditedForkTaskTemplates(); err != nil {
				t.Fatal(err)
			}
			if got, err := d.GetTaskTemplate(row.ID); err != nil || got != nil {
				t.Fatalf("unchanged seed retained: %+v %v", got, err)
			}
			got, err := d.GetTask(task.ID)
			if err != nil || got.Description != entry.Description || got.Goal != entry.Goal {
				t.Fatal("existing task changed")
			}
			description, goal, err := d.Exploration(task.ExplorationID).Root()
			if err != nil || description != entry.Description || goal != entry.Goal {
				t.Fatal("existing graph input changed")
			}
			var name string
			if err := d.QueryRow(`SELECT name FROM task_categories WHERE id=$1`, row.CategoryID).Scan(&name); err != nil || name != entry.Category {
				t.Fatal("category deleted")
			}
			if err := d.removeUneditedForkTaskTemplates(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestForkTemplateRemovalPreservesEveryUserEditedField(t *testing.T) {
	d, _ := templateFixtureDB(t)
	resetDefaultTaskTemplateSeed(t, d)
	entry := forkTemplateFixtures(t)[0]
	for _, edit := range []string{
		"description=description||' edited'", "goal=goal||' edited'", "name='renamed',nkey='renamed'", "task_name='edited'", "task_name=NULL", "timeout_seconds=10", "timeout_seconds=NULL", "plan_heartbeat_seconds=1200", "seed_first_intent=true", "coverage_enabled=false", "category_id=NULL", "intercept_rules='[{\"pattern\":\"edited\"}]'::jsonb", "test_preset='legacy-user-preset'",
	} {
		t.Run(edit, func(t *testing.T) {
			if _, err := d.Exec(`DELETE FROM settings WHERE key=$1`, forkTaskTemplatesRemovedFlag); err != nil {
				t.Fatal(err)
			}
			if err := d.SetSetting("default_task_templates_v1", "done"); err != nil {
				t.Fatal(err)
			}
			row := insertForkTemplateFixture(t, d, entry)
			if _, err := d.Exec(`UPDATE task_templates SET `+edit+` WHERE id=$1`, row.ID); err != nil {
				t.Fatal(err)
			}
			if err := d.removeUneditedForkTaskTemplates(); err != nil {
				t.Fatal(err)
			}
			var exists bool
			if err := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM task_templates WHERE id=$1)`, row.ID).Scan(&exists); err != nil || !exists {
				t.Fatalf("edited template removed: %s %v", edit, err)
			}
			if _, err := d.DeleteTaskTemplate(row.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestForkTemplateRemovalRollsBackFailureAndNeedsSeedMarker(t *testing.T) {
	d, _ := templateFixtureDB(t)
	resetDefaultTaskTemplateSeed(t, d)
	entries := forkTemplateFixtures(t)
	first := insertForkTemplateFixture(t, d, entries[0])
	second := insertForkTemplateFixture(t, d, entries[1])
	if err := d.removeUneditedForkTaskTemplates(); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.GetTaskTemplate(first.ID); got == nil {
		t.Fatal("user template without automatic seed marker deleted")
	}
	if err := d.SetSetting("default_task_templates_v1", "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`CREATE FUNCTION reject_template_remove() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF OLD.name='운영 · 관찰' THEN RAISE EXCEPTION 'fixture removal failure'; END IF; RETURN OLD; END $$; CREATE TRIGGER reject_template_remove BEFORE DELETE ON task_templates FOR EACH ROW EXECUTE FUNCTION reject_template_remove()`); err != nil {
		t.Fatal(err)
	}
	if err := d.removeUneditedForkTaskTemplates(); err == nil {
		t.Fatal("expected removal error")
	}
	if got, _ := d.GetTaskTemplate(first.ID); got == nil {
		t.Fatal("earlier removal not rolled back")
	}
	if _, err := d.Exec(`DROP TRIGGER reject_template_remove ON task_templates`); err != nil {
		t.Fatal(err)
	}
	if err := d.removeUneditedForkTaskTemplates(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{first.ID, second.ID} {
		if got, _ := d.GetTaskTemplate(id); got != nil {
			t.Fatal("retry did not remove unchanged seed")
		}
	}
}
