package server

import (
	"database/sql"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

type explorationDefaultsFixture struct {
	SourceCommit       string            `json:"source_commit"`
	Prompts            map[string]string `json:"prompts"`
	Description        string            `json:"description"`
	SchemaDescriptions map[string]string `json:"schema_descriptions"`
}

func readExplorationDefaultsFixture(t *testing.T) explorationDefaultsFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/exploration_defaults_a1396fa.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture explorationDefaultsFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestExplorationDefaultsRecognizeReviewedBaseline(t *testing.T) {
	fixture := readExplorationDefaultsFixture(t)
	if fixture.SourceCommit != "a1396fa37e4baad0d3e5497522274d5de9810c5b" {
		t.Fatal("baseline source changed")
	}
	for key, hash := range explorationPromptBaseline {
		if defaultTextHash(fixture.Prompts[key]) != hash || defaultTextHash(fixture.Prompts[key]+" 사용자 편집") == hash {
			t.Fatalf("%s: baseline does not match exact shipped prompt", key)
		}
	}
	if defaultTextHash(fixture.Description) != explorationToolBaseline.Description {
		t.Fatal("tool description baseline changed")
	}
	for path, hash := range explorationToolBaseline.Schema {
		if defaultTextHash(fixture.SchemaDescriptions[path]) != hash {
			t.Fatalf("%s: schema description baseline changed", path)
		}
	}
}

func explorationDescriptionField(schema map[string]any, path string) map[string]any {
	parts := strings.Split(path, "/")
	for _, part := range parts[:len(parts)-1] {
		schema, _ = schema[part].(map[string]any)
		if schema == nil {
			return nil
		}
	}
	return schema
}

func TestExplorationDefaultsUpgradePreservesEditsAndRollback(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires an explicitly configured isolated ARTEX_PG_DSN")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	s := &Server{m: &Manager{pg: pg}}
	fixture := readExplorationDefaultsFixture(t)
	legacy := readDefaultRefreshFixture(t)
	target := s.koreanToolDefaults()["add_intent"]
	for _, mode := range []string{"previous", "custom", "current", "localization pending", "tool deleted"} {
		t.Run(mode, func(t *testing.T) {
			preserveDefaultRefreshFlag(t, pg, explorationDefaultsFlag)
			preserveDefaultRefreshFlag(t, pg, "korean_defaults_v1")
			if mode != "localization pending" {
				if err := pg.SetSetting("korean_defaults_v1", "true"); err != nil {
					t.Fatal(err)
				}
			}
			agentIDs, expectedVersions, previousPromptIDs := map[string]int64{}, map[string]int{}, map[string]int64{}
			wantPrompts := agent.BuiltinPromptSeeds()
			for _, key := range []string{"planner", "worker"} {
				a, err := pg.GetAgentByKey(key)
				if err != nil || a == nil {
					t.Fatalf("agent %s: %v", key, err)
				}
				agentIDs[key] = a.ID
				var pointer sql.NullInt64
				var before int
				if err := pg.QueryRow(`SELECT current_prompt_id FROM agents WHERE id=$1`, a.ID).Scan(&pointer); err != nil {
					t.Fatal(err)
				}
				if err := pg.QueryRow(`SELECT COALESCE(MAX(version),0) FROM agent_prompts WHERE agent_id=$1`, a.ID).Scan(&before); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_, _ = pg.Exec(`UPDATE agents SET current_prompt_id=$2 WHERE id=$1`, a.ID, pointer)
					_, _ = pg.Exec(`DELETE FROM agent_prompts WHERE agent_id=$1 AND version>$2`, a.ID, before)
				})
				body := fixture.Prompts[key]
				updates := 1
				switch {
				case mode == "current":
					body, updates = wantPrompts[key], 0
				case mode == "custom" && key == "planner":
					body += "\n사용자 추가 검증 지침"
					wantPrompts[key], updates = body, 0
				case mode == "localization pending":
					body = legacy.Prompts[key]
					if body == "" {
						t.Fatal("missing pre-localization prompt")
					}
				}
				if body == wantPrompts[key] {
					updates = 0
				}
				version, err := pg.SavePrompt(a.ID, body, "fixture", "test")
				if err != nil {
					t.Fatal(err)
				}
				expectedVersions[key] = version + updates
				var previousID int64
				if err := pg.QueryRow(`SELECT current_prompt_id FROM agents WHERE id=$1`, a.ID).Scan(&previousID); err != nil {
					t.Fatal(err)
				}
				previousPromptIDs[key] = previousID
			}

			previousTool, err := pg.GetTool("add_intent")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if previousTool == nil {
					_, _ = pg.Exec(`DELETE FROM tools WHERE key='add_intent'`)
				} else {
					_ = pg.UpsertToolForce(previousTool.Key, previousTool.Description, previousTool.Schema, koreanDefaultsJSON(t, previousTool.Agents))
					_, _ = pg.Exec(`UPDATE tools SET enabled=$1,deferred=$2 WHERE key='add_intent'`, previousTool.Enabled, previousTool.Deferred)
				}
			})
			var schema map[string]any
			if err := json.Unmarshal(koreanDefaultsJSON(t, target.Schema), &schema); err != nil {
				t.Fatal(err)
			}
			description := fixture.Description
			for path, value := range fixture.SchemaDescriptions {
				explorationDescriptionField(schema, path)["description"] = value
			}
			// Edited defaults and unrelated schema fields must survive every path.
			schema["default"] = json.Number("9007199254740993")
			if mode == "custom" {
				description += " 사용자 설명"
				explorationDescriptionField(schema, "properties/summary/description")["description"] = "사용자 검증 질문 설명"
			}
			wantDescription := target.Desc
			if mode == "custom" {
				wantDescription = description
			}
			if mode == "current" {
				description = target.Desc
				for path := range fixture.SchemaDescriptions {
					explorationDescriptionField(schema, path)["description"] = explorationDescriptionField(target.Schema, path)["description"]
				}
			}
			if mode == "tool deleted" {
				if _, err := pg.Exec(`DELETE FROM tools WHERE key='add_intent'`); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := pg.UpsertToolForce("add_intent", description, koreanDefaultsJSON(t, schema), json.RawMessage(`["worker"]`)); err != nil {
					t.Fatal(err)
				}
				if _, err := pg.Exec(`UPDATE tools SET enabled=false,deferred=true WHERE key='add_intent'`); err != nil {
					t.Fatal(err)
				}
			}
			// Same order as startup: first-insert seed, old localization, new policy.
			seedPrompts(pg)
			if err := s.localizeStoredDefaults(); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := s.refreshExplorationDefaults(); err != nil {
					t.Fatal(err)
				}
			}
			for key, id := range agentIDs {
				got, err := pg.CurrentPrompt(id)
				if err != nil || got != wantPrompts[key] {
					t.Fatalf("%s prompt: %v", key, err)
				}
				var version int
				if err := pg.QueryRow(`SELECT MAX(version) FROM agent_prompts WHERE agent_id=$1`, id).Scan(&version); err != nil || version != expectedVersions[key] {
					t.Fatalf("%s version=%d, want=%d: %v", key, version, expectedVersions[key], err)
				}
			}
			got, err := pg.GetTool("add_intent")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "tool deleted" {
				if got != nil {
					t.Fatal("deleted tool was restored")
				}
			} else {
				for path := range fixture.SchemaDescriptions {
					if mode != "custom" || path != "properties/summary/description" {
						explorationDescriptionField(schema, path)["description"] = explorationDescriptionField(target.Schema, path)["description"]
					}
				}
				var equal bool
				if err := pg.QueryRow(`SELECT $1::jsonb=$2::jsonb`, string(koreanDefaultsJSON(t, schema)), string(got.Schema)).Scan(&equal); err != nil {
					t.Fatal(err)
				}
				if got.Description != wantDescription || !equal || got.Enabled || !got.Deferred || !reflect.DeepEqual(got.Agents, []string{"worker"}) {
					t.Fatal("tool content, binding, enabled/deferred setting or numeric default changed incorrectly")
				}
			}
			if mode == "previous" {
				// An explicit selection of the prior prompt remains selected on restart.
				if _, err := pg.Exec(`UPDATE agents SET current_prompt_id=$1 WHERE id=$2`, previousPromptIDs["planner"], agentIDs["planner"]); err != nil {
					t.Fatal(err)
				}
				if err := s.refreshExplorationDefaults(); err != nil {
					t.Fatal(err)
				}
				if got, err := pg.CurrentPrompt(agentIDs["planner"]); err != nil || got != fixture.Prompts["planner"] {
					t.Fatalf("explicit rollback overwritten: %v", err)
				}
			}
		})
	}
}
