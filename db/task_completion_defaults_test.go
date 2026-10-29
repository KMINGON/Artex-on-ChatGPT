package db

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
)

func completionTemplateFixture(t *testing.T) TaskTemplateInput {
	t.Helper()
	raw, err := os.ReadFile("testdata/completion_template_35a93b2.json")
	if err != nil {
		t.Fatal(err)
	}
	var input TaskTemplateInput
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256([]byte(input.Description+"\x00"+input.Goal))) != taskCompletionTemplateBaseline {
		t.Fatal("35a93b2 template fingerprint changed")
	}
	return input
}

func installCompletionTemplateFixture(t *testing.T, d *DB, change string) *TaskTemplate {
	t.Helper()
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM settings WHERE key=$1`, taskCompletionDefaultsFlag); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM task_templates WHERE name LIKE '실습%';
UPDATE settings SET updated_at=now() WHERE key='default_task_templates_v1'`); err != nil {
		t.Fatal(err)
	}
	input := completionTemplateFixture(t)
	var categoryID int64
	if err := tx.QueryRow(`SELECT id FROM task_categories WHERE name='실습'`).Scan(&categoryID); err != nil {
		t.Fatal(err)
	}
	input.CategoryID = &categoryID
	item, err := insertTaskTemplate(tx, input)
	if err != nil {
		t.Fatal(err)
	}
	// Apply fixture differences in the seed transaction so timestamp checks do
	// not hide a missing check on an individual field.
	if change != "" {
		if _, err := tx.Exec(`UPDATE task_templates SET `+change+` WHERE id=$1`, item.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return item
}

func completionTemplateRows(t *testing.T, d *DB) []map[string]any {
	t.Helper()
	var raw []byte
	if err := d.QueryRow(`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY id),'[]') FROM task_templates t`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestTaskCompletionDefaultsPreserveChoicesAndExistingTasks(t *testing.T) {
	d, dsn := templateFixtureDB(t)
	legacy := completionTemplateFixture(t)
	task, err := d.CreateTask(legacy.Description, legacy.Goal, nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE tasks SET paused=true WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.QueueTaskArchive(task.ID); err != nil {
		t.Fatal(err)
	}
	const existingRows = `SELECT jsonb_build_object(
		'tasks',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM tasks t),
		'nodes',(SELECT jsonb_agg(to_jsonb(n) ORDER BY id) FROM exploration_nodes n),
		'archives',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM task_archives a))`
	var existingBefore string
	if err := d.QueryRow(existingRows).Scan(&existingBefore); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, change string }{
		{"default", ""},
		{"description", "description=description || ' 사용자 수정'"},
		{"goal", "goal=goal || ' 사용자 수정'"},
		{"name", "name='실습 사용자',nkey='실습 사용자'"},
		{"category", "category_id=NULL"},
		{"task_name", "task_name='사용자 작업명'"},
		{"timeout_zero", "timeout_seconds=0"},
		{"heartbeat_zero", "plan_heartbeat_seconds=0"},
		{"seed_false", "seed_first_intent=false"},
		{"coverage_false", "coverage_enabled=false"},
		{"intercept_rules", `intercept_rules='[{"action":"deny","content":"사용자 제약"}]'::jsonb`},
		{"legacy_preset", "test_preset='lab'"},
		{"migrated_policy", "test_preset_migrated_at=now()"},
		{"edited_then_restored", "created_at=created_at-interval '1 second'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := installCompletionTemplateFixture(t, d, tc.change)
			before := completionTemplateRows(t, d)
			if tc.name == "default" {
				reopened, err := Open(dsn)
				if err != nil {
					t.Fatal(err)
				}
				reopened.Close()
			} else if err := d.refreshTaskCompletionDefaults(); err != nil {
				t.Fatal(err)
			}
			after := completionTemplateRows(t, d)
			if tc.name == "default" {
				for i := range before {
					if before[i]["id"] == float64(item.ID) {
						if after[i]["goal"] == legacy.Goal {
							t.Fatal("unchanged released goal was not upgraded")
						}
						before[i]["goal"] = defaultTaskTemplates[0].Goal
						before[i]["updated_at"] = after[i]["updated_at"]
					}
				}
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatal("migration changed fields outside the unchanged default goal")
			}
			if err := d.refreshTaskCompletionDefaults(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(completionTemplateRows(t, d), after) {
				t.Fatal("repeated migration changed template rows")
			}
		})
	}
	for _, recreate := range []bool{false, true} {
		item := installCompletionTemplateFixture(t, d, "")
		if _, err := d.DeleteTaskTemplate(item.ID); err != nil {
			t.Fatal(err)
		}
		if recreate {
			// Even an exact user-created copy must not be treated as the seed.
			legacy.CategoryID = item.CategoryID
			if _, err := d.CreateTaskTemplate(legacy); err != nil {
				t.Fatal(err)
			}
		}
		before := completionTemplateRows(t, d)
		if err := d.refreshTaskCompletionDefaults(); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(completionTemplateRows(t, d), before) {
			t.Fatal("deleted or user-created default changed")
		}
	}
	var existingAfter string
	if err := d.QueryRow(existingRows).Scan(&existingAfter); err != nil || existingAfter != existingBefore {
		t.Fatalf("existing task, graph or archive changed: %v", err)
	}
}

func TestTaskCompletionDefaultsRollbackAndRetry(t *testing.T) {
	d, _ := templateFixtureDB(t)
	installCompletionTemplateFixture(t, d, "")
	before := completionTemplateRows(t, d)
	if _, err := d.Exec(`CREATE FUNCTION reject_completion_template() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture failure'; END $$;
CREATE TRIGGER reject_completion_template BEFORE UPDATE ON task_templates FOR EACH ROW EXECUTE FUNCTION reject_completion_template()`); err != nil {
		t.Fatal(err)
	}
	if err := d.refreshTaskCompletionDefaults(); err == nil {
		t.Fatal("expected migration failure")
	}
	if !reflect.DeepEqual(completionTemplateRows(t, d), before) {
		t.Fatal("failed migration changed templates")
	}
	if _, exists, err := d.GetSetting(taskCompletionDefaultsFlag); err != nil || exists {
		t.Fatalf("failed migration marked complete: %v", err)
	}
	if _, err := d.Exec(`DROP TRIGGER reject_completion_template ON task_templates`); err != nil {
		t.Fatal(err)
	}
	if err := d.refreshTaskCompletionDefaults(); err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(completionTemplateRows(t, d), before) {
		t.Fatal("retry did not upgrade template")
	}
}
