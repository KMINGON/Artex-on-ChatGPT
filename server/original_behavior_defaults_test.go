package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

type originalDefaultsFixture struct {
	SourceCommit  string            `json:"source_commit"`
	Prompts       map[string]string `json:"prompts"`
	Wrapup        map[string]string `json:"wrapup"`
	TimeoutWrapup map[string]string `json:"timeout_wrapup"`
	Tools         []agent.ToolSeed  `json:"tools"`
}

func originalDefaultsTestDB(t *testing.T) *db.DB {
	t.Helper()
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires isolated ARTEX_PG_DSN")
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("artex_original_defaults_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE DATABASE "` + name + `"`); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var pg *db.DB
	t.Cleanup(func() {
		if pg != nil {
			_ = pg.Close()
		}
		if _, err := admin.Exec(`DROP DATABASE "` + name + `" WITH (FORCE)`); err != nil {
			t.Error(err)
		}
		_ = admin.Close()
	})
	parsed.Path = "/" + name
	pg, err = db.Open(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	return pg
}
func readOriginalDefaultsFixture(t *testing.T) originalDefaultsFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/origin_defaults_8d539f9.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture originalDefaultsFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SourceCommit != "8d539f9b5d9cb497812d09e3e889363c639f1933" {
		t.Fatal("baseline commit changed")
	}
	return fixture
}
func TestOriginalBehaviorDefaultsMatchReleasedBaseline(t *testing.T) {
	fixture := readOriginalDefaultsFixture(t)
	var baseline originalBehaviorBaseline
	if err := json.Unmarshal(originalBehaviorDefaultsJSON, &baseline); err != nil {
		t.Fatal(err)
	}
	for key, text := range fixture.Prompts {
		if defaultTextHash(text) != baseline.Prompts[key] {
			t.Fatalf("prompt %s fingerprint mismatch", key)
		}
	}
	for key, text := range fixture.Wrapup {
		if defaultTextHash(text) != baseline.Wrapup[key] {
			t.Fatalf("wrapup %s fingerprint mismatch", key)
		}
	}
	for key, text := range fixture.TimeoutWrapup {
		if defaultTextHash(text) != baseline.TimeoutWrapup[key] {
			t.Fatalf("timeout %s fingerprint mismatch", key)
		}
	}
	for _, tool := range fixture.Tools {
		old := baseline.Tools[tool.Key]
		if defaultTextHash(tool.Desc) != old.Description {
			t.Fatalf("tool %s fingerprint mismatch", tool.Key)
		}
		for path, hash := range old.Schema {
			field := explorationDescriptionField(tool.Schema, path)
			if defaultTextHash(field["description"].(string)) != hash {
				t.Fatalf("tool %s %s fingerprint mismatch", tool.Key, path)
			}
		}
	}
}
func TestOriginalBehaviorDefaultsUpgradeOnlyUneditedCurrentValues(t *testing.T) {
	for _, mode := range []string{"default", "custom", "selected history", "tool deleted", "failure and retry"} {
		t.Run(mode, func(t *testing.T) {
			fixture := readOriginalDefaultsFixture(t)
			pg := originalDefaultsTestDB(t)
			s := &Server{m: &Manager{pg: pg}}
			want := agent.BuiltinPromptSeeds()
			for key, target := range want {
				row, err := pg.GetAgentByKey(key)
				if err != nil || row == nil {
					t.Fatalf("missing agent %s: %v", key, err)
				}
				old := fixture.Prompts[key]
				if mode == "custom" && key == "worker" {
					old += "\nuser question"
					target = old
				}
				if _, err := pg.SavePrompt(row.ID, old, "fixture", "test"); err != nil {
					t.Fatal(err)
				}
				if mode == "selected history" && key == "planner" {
					var selected int64
					if err := pg.QueryRow(`SELECT current_prompt_id FROM agents WHERE id=$1`, row.ID).Scan(&selected); err != nil {
						t.Fatal(err)
					}
					if _, err := pg.SavePrompt(row.ID, old+"\nnew unselected version", "fixture", "test"); err != nil {
						t.Fatal(err)
					}
					if _, err := pg.Exec(`UPDATE agents SET current_prompt_id=$2 WHERE id=$1`, row.ID, selected); err != nil {
						t.Fatal(err)
					}
					target = old
				}
				want[key] = target
				wrapup := fixture.Wrapup[key]
				timeout := fixture.TimeoutWrapup[key]
				if mode == "custom" && key == "worker" {
					wrapup = "custom wrapup"
					timeout = "custom timeout"
				}
				if _, err := pg.Exec(`UPDATE agents SET wrapup_prompt=$2,task_timeout_wrapup_prompt=$3,wrapup_max_turns=7,task_timeout_wrapup_max_turns=8 WHERE id=$1`, row.ID, wrapup, timeout); err != nil {
					t.Fatal(err)
				}
			}
			for _, old := range fixture.Tools {
				if mode == "tool deleted" && old.Key == "add_intent" {
					continue
				}
				raw, err := json.Marshal(old.Schema)
				if err != nil {
					t.Fatal(err)
				}
				description := old.Desc
				if mode == "custom" && old.Key == "add_intent" {
					description = "custom tool description"
					old.Schema["properties"].(map[string]any)["summary"].(map[string]any)["description"] = "custom summary"
					raw, _ = json.Marshal(old.Schema)
				}
				if err := pg.SeedTool(old.Key, description, raw, json.RawMessage(`["auto"]`)); err != nil {
					t.Fatal(err)
				}
				if _, err := pg.Exec(`UPDATE tools SET enabled=false WHERE key=$1`, old.Key); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "failure and retry" {
				if _, err := pg.Exec(`CREATE FUNCTION reject_origin_defaults() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture defaults failure'; END $$; CREATE TRIGGER reject_origin_defaults BEFORE UPDATE ON tools FOR EACH ROW EXECUTE FUNCTION reject_origin_defaults()`); err != nil {
					t.Fatal(err)
				}
				if err := s.refreshOriginalBehaviorDefaults(); err == nil {
					t.Fatal("expected failure")
				}
				planner, _ := pg.GetAgentByKey("planner")
				old, _ := pg.CurrentPrompt(planner.ID)
				if old != fixture.Prompts["planner"] {
					t.Fatal("failed migration changed prompt")
				}
				if _, err := pg.Exec(`DROP TRIGGER reject_origin_defaults ON tools`); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.refreshOriginalBehaviorDefaults(); err != nil {
				t.Fatal(err)
			}
			if err := s.refreshOriginalBehaviorDefaults(); err != nil {
				t.Fatal(err)
			}
			for key, expected := range want {
				row, _ := pg.GetAgentByKey(key)
				got, err := pg.CurrentPrompt(row.ID)
				if err != nil || got != expected {
					t.Fatalf("prompt %s changed incorrectly: %v", key, err)
				}
				if row.WrapupMaxTurns != 7 || row.TaskTimeoutWrapupMaxTurns != 8 {
					t.Fatal("explicit turn budgets changed")
				}
				expectedWrapup := agent.WrapupDefault(key)
				expectedTimeout := agent.TaskTimeoutWrapupDefault(key)
				if mode == "custom" && key == "worker" {
					expectedWrapup = "custom wrapup"
					expectedTimeout = "custom timeout"
				}
				if row.WrapupPrompt != expectedWrapup || row.TaskTimeoutWrapupPrompt != expectedTimeout {
					t.Fatalf("wrapup %s changed incorrectly", key)
				}
			}
			defaults := s.koreanToolDefaults()
			for _, old := range fixture.Tools {
				got, err := pg.GetTool(old.Key)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "tool deleted" && old.Key == "add_intent" {
					if got != nil {
						t.Fatal("deleted tool restored")
					}
					continue
				}
				if got == nil || got.Enabled || !strings.Contains(strings.Join(got.Agents, ","), "auto") {
					t.Fatalf("tool binding altered: %s", old.Key)
				}
				var schema map[string]any
				if err := json.Unmarshal(got.Schema, &schema); err != nil {
					t.Fatal(err)
				}
				wantTool := defaults[old.Key]
				copySchema, _ := json.Marshal(wantTool.Schema)
				wantTool.Schema = nil
				_ = json.Unmarshal(copySchema, &wantTool.Schema)
				if mode == "custom" && old.Key == "add_intent" {
					wantTool.Desc = "custom tool description"
					wantTool.Schema["properties"].(map[string]any)["summary"].(map[string]any)["description"] = "custom summary"
				}
				raw, _ := json.Marshal(wantTool.Schema)
				var expectedSchema map[string]any
				_ = json.Unmarshal(raw, &expectedSchema)
				if got.Description != wantTool.Desc || !reflect.DeepEqual(schema, expectedSchema) {
					t.Fatalf("tool %s not restored", old.Key)
				}
			}
		})
	}
}

func TestOriginalBehaviorDefaultsRemovesLegacyCompletionFieldOnlyWhenUnedited(t *testing.T) {
	fixture := readOriginalDefaultsFixture(t)
	legacy := readRestorationDefaultsFixture(t, "exploration_defaults_ca23d12.json")
	for _, mode := range []string{"legacy default", "edited description", "edited constraint"} {
		t.Run(mode, func(t *testing.T) {
			pg := originalDefaultsTestDB(t)
			s := &Server{m: &Manager{pg: pg}}
			var old agent.ToolSeed
			for _, tool := range fixture.Tools {
				if tool.Key == "prove_goal" {
					old = tool
				}
			}
			raw, _ := json.Marshal(old.Schema)
			var schema map[string]any
			_ = json.Unmarshal(raw, &schema)
			props := schema["properties"].(map[string]any)
			field := map[string]any{"type": "string", "description": legacy.Tools["prove_goal"].SchemaDescriptions["properties/completion_review/description"]}
			if mode == "edited description" {
				field["description"] = "custom completion note"
			}
			if mode == "edited constraint" {
				field["maxLength"] = json.Number("100")
			}
			props["completion_review"] = field
			encoded, _ := json.Marshal(schema)
			if err := pg.SeedTool("prove_goal", old.Desc, encoded, json.RawMessage(`["planner"]`)); err != nil {
				t.Fatal(err)
			}
			if err := s.refreshOriginalBehaviorDefaults(); err != nil {
				t.Fatal(err)
			}
			got, err := pg.GetTool("prove_goal")
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(got.Schema, &schema); err != nil {
				t.Fatal(err)
			}
			_, present := schema["properties"].(map[string]any)["completion_review"]
			if present != (mode != "legacy default") {
				t.Fatalf("legacy field handling: present=%v mode=%s", present, mode)
			}
		})
	}
}
