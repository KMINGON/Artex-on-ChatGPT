package server

import (
	"database/sql"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

type defaultRefreshFixture struct {
	Prompts map[string]string `json:"prompts"`
	Tools   []agent.ToolSeed  `json:"tools"`
}

func readDefaultRefreshFixture(t *testing.T) defaultRefreshFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/korean_defaults_legacy.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture defaultRefreshFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestDefaultRefreshGuardsRecognizeOnlyUneditedDefaults(t *testing.T) {
	fixture := readDefaultRefreshFixture(t)
	for key, text := range fixture.Prompts {
		if !isLegacyPromptDefault(key, text) {
			t.Errorf("%s: exact shipped prompt not recognized", key)
		}
		for _, edited := range []string{"", text + " 사용자 지시", "older unknown default"} {
			if isLegacyPromptDefault(key, edited) {
				t.Errorf("%s: edited/unknown prompt accepted", key)
			}
		}
	}
	if isLegacyPromptDefault("unknown", "") || isLegacyToolDefault(nil) {
		t.Fatal("missing defaults must not be eligible for reset")
	}
	for _, seed := range fixture.Tools {
		raw, err := json.MarshalIndent(seed.Schema, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		current := db.Tool{Key: seed.Key, System: true, Kind: "builtin", Description: seed.Desc, Schema: raw}
		if !isLegacyToolDefault(&current) {
			t.Errorf("%s: canonical schema did not match", seed.Key)
		}
		for name, alter := range map[string]func(*db.Tool){
			"description": func(tool *db.Tool) { tool.Description += " 사용자 지시" },
			"schema":      func(tool *db.Tool) { tool.Schema = json.RawMessage(`{"type":"object","default":9007199254740993}`) },
			"invalid":     func(tool *db.Tool) { tool.Schema = json.RawMessage(`{`) },
			"custom":      func(tool *db.Tool) { tool.System = false },
			"kind":        func(tool *db.Tool) { tool.Kind = "script" },
			"unknown":     func(tool *db.Tool) { tool.Key = "unknown" },
		} {
			changed := current
			alter(&changed)
			if isLegacyToolDefault(&changed) {
				t.Errorf("%s/%s: customized tool accepted", seed.Key, name)
			}
		}
	}
}

func TestLegacyStartupRefreshPreservesCustomAndCurrentDefaults(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("isolated PostgreSQL required")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	s := &Server{m: &Manager{pg: pg}}
	fixture := readDefaultRefreshFixture(t)
	for _, seed := range fixture.Tools {
		if seed.Key != "list_tasks" {
			continue
		}
		for _, mode := range []string{"legacy", "custom description", "custom schema", "current"} {
			t.Run("tool/"+mode, func(t *testing.T) {
				preserveDefaultRefreshFlag(t, pg, "tool_schema_refresh_v7_list_facts_paging")
				previous, err := pg.GetTool(seed.Key)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if previous == nil {
						_, _ = pg.Exec(`DELETE FROM tools WHERE key=$1`, seed.Key)
					} else {
						_, _ = pg.Exec(`UPDATE tools SET description=$2,schema=$3,agents=$4,enabled=$5,deferred=$6 WHERE key=$1`, previous.Key, previous.Description, previous.Schema, koreanDefaultsJSON(t, previous.Agents), previous.Enabled, previous.Deferred)
					}
				})
				target := s.koreanToolDefaults()[seed.Key]
				description, schema := seed.Desc, koreanDefaultsJSON(t, seed.Schema)
				switch mode {
				case "custom description":
					description += " 사용자 설명"
				case "custom schema":
					var edited map[string]any
					if err := json.Unmarshal(schema, &edited); err != nil {
						t.Fatal(err)
					}
					edited["default"] = json.Number("9007199254740993")
					schema = koreanDefaultsJSON(t, edited)
				case "current":
					description, schema = target.Desc, koreanDefaultsJSON(t, target.Schema)
				}
				if err := pg.UpsertToolForce(seed.Key, description, schema, json.RawMessage(`["worker"]`)); err != nil {
					t.Fatal(err)
				}
				if _, err := pg.Exec(`UPDATE tools SET enabled=false,deferred=true WHERE key=$1`, seed.Key); err != nil {
					t.Fatal(err)
				}
				s.refreshBuiltinToolSchemas()
				got, err := pg.GetTool(seed.Key)
				if err != nil || got == nil {
					t.Fatalf("tool read: %v", err)
				}
				if mode == "legacy" {
					description, schema = target.Desc, koreanDefaultsJSON(t, target.Schema)
				}
				var schemaEqual bool
				if err := pg.QueryRow(`SELECT $1::jsonb=$2::jsonb`, string(schema), string(got.Schema)).Scan(&schemaEqual); err != nil {
					t.Fatal(err)
				}
				if got.Description != description || !schemaEqual || got.Enabled || !got.Deferred || !reflect.DeepEqual(got.Agents, []string{"worker"}) {
					t.Fatalf("%s: customized content or tool settings changed", mode)
				}
			})
		}
	}

	for _, test := range []struct {
		key, flag string
		run       func()
	}{
		{"goals", "goals_prompt_constraint_step_v1", s.reseedGoalsPrompt},
		{"mainagent", "mainagent_prompt_goalless_intent_v1", s.reseedMainAgentPrompt},
		{"planner", "planner_prompt_compact_realistic_v2", s.reseedPlannerPrompt},
		{"worker", "worker_prompt_compact_v4", s.reseedWorkerPrompt},
	} {
		for _, mode := range []string{"legacy", "custom", "current"} {
			t.Run("prompt/"+test.key+"/"+mode, func(t *testing.T) {
				preserveDefaultRefreshFlag(t, pg, test.flag)
				a, err := pg.GetAgentByKey(test.key)
				if err != nil || a == nil {
					t.Fatalf("agent read: %v", err)
				}
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
				body := fixture.Prompts[test.key]
				if body == "" {
					t.Fatal("missing legacy prompt fixture")
				}
				want := agent.BuiltinPromptSeeds()[test.key]
				if mode == "custom" {
					body += " 사용자 지시"
				} else if mode == "current" {
					body = want
				}
				version, err := pg.SavePrompt(a.ID, body, "fixture", "test")
				if err != nil {
					t.Fatal(err)
				}
				test.run()
				test.run()
				if mode != "legacy" {
					want = body
				} else {
					version++
				}
				got, err := pg.CurrentPrompt(a.ID)
				if err != nil || got != want {
					t.Fatalf("%s prompt unexpectedly changed: %v", mode, err)
				}
				var actualVersion int
				if err := pg.QueryRow(`SELECT MAX(version) FROM agent_prompts WHERE agent_id=$1`, a.ID).Scan(&actualVersion); err != nil || actualVersion != version {
					t.Fatalf("unexpected prompt version %d, want %d: %v", actualVersion, version, err)
				}
			})
		}
	}
}

func preserveDefaultRefreshFlag(t *testing.T, pg *db.DB, flag string) {
	t.Helper()
	old, had, err := pg.GetSetting(flag)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if had {
			_ = pg.SetSetting(flag, old)
		} else {
			_, _ = pg.Exec(`DELETE FROM settings WHERE key=$1`, flag)
		}
	})
	if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, flag); err != nil {
		t.Fatal(err)
	}
}
