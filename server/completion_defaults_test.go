package server

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/Autumn-27/artex/agent"
)

func completionPlannerFixture(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/completion_planner_35a93b2.txt")
	if err != nil {
		t.Fatal(err)
	}
	if defaultTextHash(string(raw)) != completionPlannerBaseline {
		t.Fatal("35a93b2 Planner fingerprint changed")
	}
	return string(raw)
}

func TestCompletionDefaultsPreserveChoices(t *testing.T) {
	for _, choice := range []string{"default", "edited", "history", "missing"} {
		t.Run(choice, func(t *testing.T) {
			pg := originalDefaultsTestDB(t)
			s := &Server{m: &Manager{pg: pg}}
			old := completionPlannerFixture(t)
			planner, _ := pg.GetAgentByKey("planner")
			want := old
			if choice == "edited" {
				want += "\n사용자 지침"
			}
			if choice != "missing" {
				if _, err := pg.SavePrompt(planner.ID, want, "fixture", "test"); err != nil {
					t.Fatal(err)
				}
			}
			if choice == "history" {
				var selected int64
				if err := pg.QueryRow(`SELECT current_prompt_id FROM agents WHERE id=$1`, planner.ID).Scan(&selected); err != nil {
					t.Fatal(err)
				}
				if _, err := pg.SavePrompt(planner.ID, old+"\n선택하지 않은 버전", "fixture", "test"); err != nil {
					t.Fatal(err)
				}
				if _, err := pg.Exec(`UPDATE agents SET current_prompt_id=$2 WHERE id=$1`, planner.ID, selected); err != nil {
					t.Fatal(err)
				}
			}
			if choice == "default" {
				want = agent.BuiltinPromptSeeds()["planner"]
				if want == old {
					t.Fatal("new Planner body still equals released baseline")
				}
			}
			if choice == "missing" {
				want = ""
			}
			worker, _ := pg.GetAgentByKey("worker")
			if _, err := pg.SavePrompt(worker.ID, "다른 역할 사용자 지침", "fixture", "test"); err != nil {
				t.Fatal(err)
			}
			if err := pg.SeedTool("prove_goal", "사용자 도구 설명", json.RawMessage(`{"type":"object"}`), json.RawMessage(`["planner"]`)); err != nil {
				t.Fatal(err)
			}
			if err := pg.SetSetting(instructionDefaultsFlag, "true"); err != nil {
				t.Fatal(err)
			}
			const unaffected = `SELECT jsonb_build_object(
				'prompts',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM agent_prompts p WHERE agent_id<>$1),
				'tools',(SELECT jsonb_agg(to_jsonb(t) ORDER BY key) FROM tools t))`
			var before string
			if err := pg.QueryRow(unaffected, planner.ID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			var oldVersion int
			if err := pg.QueryRow(`SELECT COALESCE(MAX(version),0) FROM agent_prompts WHERE agent_id=$1`, planner.ID).Scan(&oldVersion); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := s.refreshCompletionDefaults(); err != nil {
					t.Fatal(err)
				}
			}
			text, err := pg.CurrentPrompt(planner.ID)
			if err != nil || text != want {
				t.Fatalf("Planner choice changed incorrectly: %v", err)
			}
			var version int
			if err := pg.QueryRow(`SELECT COALESCE(MAX(version),0) FROM agent_prompts WHERE agent_id=$1`, planner.ID).Scan(&version); err != nil {
				t.Fatal(err)
			}
			if choice == "default" {
				oldVersion++
			}
			if version != oldVersion {
				t.Fatal("migration appended an unexpected prompt version")
			}
			var after string
			if err := pg.QueryRow(unaffected, planner.ID).Scan(&after); err != nil || after != before {
				t.Fatalf("another agent or tool changed: %v", err)
			}
			if _, err := pg.SavePrompt(planner.ID, old, "명시적 이전 지침 복원", "user"); err != nil {
				t.Fatal(err)
			}
			if err := s.refreshCompletionDefaults(); err != nil {
				t.Fatal(err)
			}
			if text, err := pg.CurrentPrompt(planner.ID); err != nil || text != old {
				t.Fatalf("explicit rollback overwritten: %v", err)
			}
		})
	}
}

func TestCompletionDefaultsRollbackAndRetry(t *testing.T) {
	pg := originalDefaultsTestDB(t)
	s := &Server{m: &Manager{pg: pg}}
	old := completionPlannerFixture(t)
	planner, _ := pg.GetAgentByKey("planner")
	if _, err := pg.SavePrompt(planner.ID, old, "fixture", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(`CREATE FUNCTION reject_completion_marker() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.key='planner_completion_defaults_v1' THEN RAISE EXCEPTION 'fixture failure'; END IF; RETURN NEW; END $$;
CREATE TRIGGER reject_completion_marker BEFORE INSERT ON settings FOR EACH ROW EXECUTE FUNCTION reject_completion_marker()`); err != nil {
		t.Fatal(err)
	}
	if err := s.refreshCompletionDefaults(); err == nil {
		t.Fatal("expected migration failure")
	}
	if text, err := pg.CurrentPrompt(planner.ID); err != nil || text != old {
		t.Fatalf("partial prompt upgrade committed: %v", err)
	}
	var versions int
	if err := pg.QueryRow(`SELECT count(*) FROM agent_prompts WHERE agent_id=$1`, planner.ID).Scan(&versions); err != nil || versions != 1 {
		t.Fatalf("failed migration left a prompt version: %d %v", versions, err)
	}
	if _, exists, err := pg.GetSetting(completionDefaultsFlag); err != nil || exists {
		t.Fatalf("failed migration marked complete: %v", err)
	}
	if _, err := pg.Exec(`DROP TRIGGER reject_completion_marker ON settings`); err != nil {
		t.Fatal(err)
	}
	if err := s.refreshCompletionDefaults(); err != nil {
		t.Fatal(err)
	}
	if text, err := pg.CurrentPrompt(planner.ID); err != nil || text != agent.BuiltinPromptSeeds()["planner"] {
		t.Fatalf("retry failed to upgrade Planner: %v", err)
	}
}
