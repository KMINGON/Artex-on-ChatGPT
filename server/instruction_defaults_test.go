package server

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
)

type instructionDefaultsFixture struct {
	SourceCommit string                    `json:"source_commit"`
	Prompts      map[string]string         `json:"prompts"`
	Tools        map[string]agent.ToolSeed `json:"tools"`
}

func readInstructionDefaultsFixture(t *testing.T) instructionDefaultsFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/instruction_defaults_9535c25.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture instructionDefaultsFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestInstructionDefaultsMatchReleasedBaseline(t *testing.T) {
	fixture := readInstructionDefaultsFixture(t)
	if fixture.SourceCommit != "9535c2503b767f70dcbbbcc5717e13962e8b8241" {
		t.Fatal("baseline commit changed")
	}
	var baseline koreanDefaultBaseline
	if err := json.Unmarshal(instructionDefaultsJSON, &baseline); err != nil {
		t.Fatal(err)
	}
	for key, body := range fixture.Prompts {
		if defaultTextHash(body) != baseline.Prompts[key] {
			t.Fatalf("prompt %s fingerprint mismatch", key)
		}
	}
	for key, tool := range fixture.Tools {
		old := baseline.Tools[key]
		if defaultTextHash(tool.Desc) != old.Description {
			t.Fatalf("tool %s fingerprint mismatch", key)
		}
		for path, hash := range old.Schema {
			field := explorationDescriptionField(tool.Schema, path)
			text, _ := field["description"].(string)
			if defaultTextHash(text) != hash {
				t.Fatalf("tool %s %s fingerprint mismatch", key, path)
			}
		}
	}
}

func TestInstructionDefaultsUpgradePreservesChoices(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "defaults", true: "customizations"}[custom], func(t *testing.T) {
			pg := originalDefaultsTestDB(t)
			s := &Server{m: &Manager{pg: pg}}
			fixture := readInstructionDefaultsFixture(t)
			wantPrompts := agent.BuiltinPromptSeeds()
			for key, text := range fixture.Prompts {
				a, err := pg.GetAgentByKey(key)
				if err != nil || a == nil {
					t.Fatalf("agent %s: %v", key, err)
				}
				if custom && key == "worker" {
					text += "\n사용자 지정 지침"
					wantPrompts[key] = text
				}
				if _, err := pg.SavePrompt(a.ID, text, "released fixture", "test"); err != nil {
					t.Fatal(err)
				}
				if custom && key == "planner" {
					var selected int64
					if err := pg.QueryRow(`SELECT current_prompt_id FROM agents WHERE id=$1`, a.ID).Scan(&selected); err != nil {
						t.Fatal(err)
					}
					if _, err := pg.SavePrompt(a.ID, text+"\n선택하지 않은 버전", "fixture", "test"); err != nil {
						t.Fatal(err)
					}
					if _, err := pg.Exec(`UPDATE agents SET current_prompt_id=$2 WHERE id=$1`, a.ID, selected); err != nil {
						t.Fatal(err)
					}
					wantPrompts[key] = text
				}
			}
			for key, tool := range fixture.Tools {
				if custom && key == "record_fact" {
					continue // A missing tool must not be recreated by this migration.
				}
				if custom && key == "list_assets" {
					tool.Desc = "사용자 도구 설명"
					props := tool.Schema["properties"].(map[string]any)
					props["dsl"].(map[string]any)["description"] = "사용자 검색 설명"
					props["limit"].(map[string]any)["default"] = json.Number("9007199254740993")
				}
				if custom && key == "set_goals" {
					field := tool.Schema["properties"].(map[string]any)["goals"].(map[string]any)
					field["items"] = map[string]any{"type": "object", "description": "사용자 항목 설명", "properties": map[string]any{"custom": map[string]any{"type": "string"}}}
				}
				if custom && key == "add_hint" {
					explorationDescriptionField(tool.Schema, "properties/hints/items/properties/asset_ids/description")["description"] = "사용자 힌트 자산 설명"
				}
				raw, err := json.Marshal(tool.Schema)
				if err != nil {
					t.Fatal(err)
				}
				if err := pg.SeedTool(key, tool.Desc, raw, json.RawMessage(`["auto"]`)); err != nil {
					t.Fatal(err)
				}
				if _, err := pg.Exec(`UPDATE tools SET enabled=false WHERE key=$1`, key); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.refreshInstructionDefaults(); err != nil {
				t.Fatal(err)
			}
			if err := s.refreshInstructionDefaults(); err != nil {
				t.Fatal(err)
			}
			for key := range fixture.Prompts {
				a, _ := pg.GetAgentByKey(key)
				text, err := pg.CurrentPrompt(a.ID)
				if err != nil || text != wantPrompts[key] {
					t.Fatalf("prompt %s incorrectly upgraded: %v", key, err)
				}
			}
			defaults := s.koreanToolDefaults()
			for key := range fixture.Tools {
				got, err := pg.GetTool(key)
				if err != nil {
					t.Fatal(err)
				}
				if custom && key == "record_fact" {
					if got != nil {
						t.Fatal("missing tool recreated")
					}
					continue
				}
				if got == nil || got.Enabled || !reflect.DeepEqual(got.Agents, []string{"auto"}) {
					t.Fatalf("tool state/binding changed: %s", key)
				}
				var schema map[string]any
				decoder := json.NewDecoder(strings.NewReader(string(got.Schema)))
				decoder.UseNumber()
				if err := decoder.Decode(&schema); err != nil {
					t.Fatal(err)
				}
				if custom && key == "list_assets" {
					props := schema["properties"].(map[string]any)
					if got.Description != "사용자 도구 설명" || props["dsl"].(map[string]any)["description"] != "사용자 검색 설명" || props["limit"].(map[string]any)["default"] != json.Number("9007199254740993") {
						t.Fatal("user tool annotations changed")
					}
					continue
				}
				if got.Description != defaults[key].Desc {
					t.Errorf("stale tool description: %s", key)
				}
				if custom && key == "set_goals" {
					items := schema["properties"].(map[string]any)["goals"].(map[string]any)["items"].(map[string]any)
					if items["description"] != "사용자 항목 설명" || items["properties"].(map[string]any)["custom"] == nil {
						t.Fatal("custom batch schema replaced")
					}
					continue
				}
				if custom && key == "add_hint" {
					field := explorationDescriptionField(schema, "properties/hints/items/properties/asset_ids/description")
					if field["description"] != "사용자 힌트 자산 설명" {
						t.Fatal("custom hint asset description replaced")
					}
					continue
				}
				actual, _ := json.Marshal(schema)
				expected, _ := json.Marshal(defaults[key].Schema)
				if string(actual) != string(expected) {
					t.Errorf("stale schema: %s", key)
				}
			}
			// A later explicit rollback must survive another startup.
			worker, _ := pg.GetAgentByKey("worker")
			if _, err := pg.SavePrompt(worker.ID, fixture.Prompts["worker"], "rollback", "user"); err != nil {
				t.Fatal(err)
			}
			if err := s.refreshInstructionDefaults(); err != nil {
				t.Fatal(err)
			}
			text, _ := pg.CurrentPrompt(worker.ID)
			if text != fixture.Prompts["worker"] {
				t.Fatal("explicit rollback overwritten")
			}
		})
	}
}

func TestInstructionDefaultsRollbackAndRetry(t *testing.T) {
	pg := originalDefaultsTestDB(t)
	s := &Server{m: &Manager{pg: pg}}
	fixture := readInstructionDefaultsFixture(t)
	worker, _ := pg.GetAgentByKey("worker")
	if _, err := pg.SavePrompt(worker.ID, fixture.Prompts["worker"], "fixture", "test"); err != nil {
		t.Fatal(err)
	}
	old := fixture.Tools["list_assets"]
	raw, _ := json.Marshal(old.Schema)
	if err := pg.SeedTool(old.Key, old.Desc, raw, json.RawMessage(`["worker"]`)); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(`CREATE FUNCTION reject_instruction_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture failure'; END $$;
CREATE TRIGGER reject_instruction_fixture BEFORE UPDATE ON tools FOR EACH ROW EXECUTE FUNCTION reject_instruction_fixture()`); err != nil {
		t.Fatal(err)
	}
	if err := s.refreshInstructionDefaults(); err == nil {
		t.Fatal("expected migration failure")
	}
	text, _ := pg.CurrentPrompt(worker.ID)
	if text != fixture.Prompts["worker"] {
		t.Fatal("partial prompt upgrade committed")
	}
	if _, exists, err := pg.GetSetting(instructionDefaultsFlag); err != nil || exists {
		t.Fatalf("failed migration marked complete: %v", err)
	}
	if _, err := pg.Exec(`DROP TRIGGER reject_instruction_fixture ON tools`); err != nil {
		t.Fatal(err)
	}
	if err := s.refreshInstructionDefaults(); err != nil {
		t.Fatal(err)
	}
	text, _ = pg.CurrentPrompt(worker.ID)
	if text != agent.BuiltinPromptSeeds()["worker"] {
		t.Fatal("retry failed to update prompt")
	}
}
