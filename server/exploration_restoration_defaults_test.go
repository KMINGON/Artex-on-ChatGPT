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

type restorationDefaultsFixture struct {
	SourceCommit string            `json:"source_commit"`
	Prompts      map[string]string `json:"prompts"`
	Tools        map[string]struct {
		Description        string            `json:"description"`
		SchemaDescriptions map[string]string `json:"schema_descriptions"`
		SchemaFields       map[string]any    `json:"schema_fields"`
	} `json:"tools"`
}

func readRestorationDefaultsFixture(t *testing.T, filename string) restorationDefaultsFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + filename)
	if err != nil {
		t.Fatal(err)
	}
	var fixture restorationDefaultsFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestExplorationRestorationReviewedBaseline(t *testing.T) {
	fixture := readRestorationDefaultsFixture(t, "exploration_defaults_ca23d12.json")
	if fixture.SourceCommit != "ca23d1234ba6276d06dd6f2656a629cfa949a551" {
		t.Fatal("baseline source changed")
	}
	for key, hash := range explorationRestorationPromptBaseline {
		if defaultTextHash(fixture.Prompts[key]) != hash {
			t.Fatalf("%s shipped prompt mismatch", key)
		}
	}
	latest := readRestorationDefaultsFixture(t, "exploration_defaults_17e0fca.json")
	if latest.SourceCommit != "17e0fca59aef7d2e8f8e31355a3665cfd606e1dc" {
		t.Fatal("latest baseline source changed")
	}
	for key, hash := range explorationParityPromptBaseline {
		if defaultTextHash(latest.Prompts[key]) != hash {
			t.Fatalf("%s latest shipped prompt mismatch", key)
		}
	}
	previous := readRestorationDefaultsFixture(t, "exploration_defaults_7f38dea.json")
	if previous.SourceCommit != "7f38dea2329b44ba85bbd50cbc2de81ae3edc39d" {
		t.Fatal("guidance baseline source changed")
	}
	for key, hash := range explorationGuidancePromptBaseline {
		if defaultTextHash(previous.Prompts[key]) != hash || defaultTextHash(previous.Prompts[key]+" 사용자 편집") == hash {
			t.Fatalf("%s guidance shipped prompt mismatch", key)
		}
	}
	for key, baseline := range explorationRestorationToolBaseline {
		if baseline.Description != "" && defaultTextHash(fixture.Tools[key].Description) != baseline.Description {
			t.Fatalf("%s description mismatch", key)
		}
		for path, hash := range baseline.Schema {
			if defaultTextHash(fixture.Tools[key].SchemaDescriptions[path]) != hash {
				t.Fatalf("%s %s description mismatch", key, path)
			}
		}
	}
	traffic, err := json.Marshal(fixture.Tools["report_finding"].SchemaFields["traffic_refs"])
	if err != nil || defaultTextHash(string(traffic)) != explorationTrafficRefsBaselineHash {
		t.Fatalf("original traffic_refs field mismatch: %v", err)
	}
}

func TestExplorationRestorationUpgradePreservesCustomAndSelectedHistory(t *testing.T) {
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
	fixture := readRestorationDefaultsFixture(t, "exploration_defaults_ca23d12.json")
	latest := readRestorationDefaultsFixture(t, "exploration_defaults_17e0fca.json")
	previous := readRestorationDefaultsFixture(t, "exploration_defaults_7f38dea.json")
	older := readExplorationDefaultsFixture(t)
	defaults := s.koreanToolDefaults()
	for _, mode := range []string{"ca23d12", "a1396fa", "17e0fca", "7f38dea", "current", "custom", "custom latest", "custom 7f38dea", "selected history", "selected latest history", "selected 7f38dea history", "tool deleted", "localized description", "retry after schema error"} {
		t.Run(mode, func(t *testing.T) {
			preserveDefaultRefreshFlag(t, pg, explorationRestorationDefaultsFlag)
			preserveDefaultRefreshFlag(t, pg, "exploration_persistence_defaults_v2")
			if err := pg.SetSetting("exploration_persistence_defaults_v2", "true"); err != nil {
				t.Fatal(err)
			}
			wantPrompts := agent.BuiltinPromptSeeds()
			agentIDs, expectedVersions := map[string]int64{}, map[string]int{}
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
				body, updates := fixture.Prompts[key], 1
				switch mode {
				case "a1396fa":
					body = older.Prompts[key]
				case "17e0fca":
					body = latest.Prompts[key]
				case "7f38dea", "retry after schema error":
					body = previous.Prompts[key]
				case "current":
					body, updates = wantPrompts[key], 0
				case "custom":
					body += "\n사용자의 추가 검증 지침"
					wantPrompts[key], updates = body, 0
				case "custom latest":
					body = latest.Prompts[key] + "\n사용자의 추가 검증 지침"
					wantPrompts[key], updates = body, 0
				case "custom 7f38dea":
					body = previous.Prompts[key] + "\n사용자의 추가 검증 지침"
					wantPrompts[key], updates = body, 0
				case "selected history":
					wantPrompts[key], updates = body, 0
				case "selected latest history":
					body = latest.Prompts[key]
					wantPrompts[key], updates = body, 0
				case "selected 7f38dea history":
					body = previous.Prompts[key]
					wantPrompts[key], updates = body, 0
				}
				if body == wantPrompts[key] {
					updates = 0
				}
				version, err := pg.SavePrompt(a.ID, body, "fixture", "test")
				if err != nil {
					t.Fatal(err)
				}
				if mode == "selected history" || mode == "selected latest history" || mode == "selected 7f38dea history" {
					var selected int64
					if err := pg.QueryRow(`SELECT current_prompt_id FROM agents WHERE id=$1`, a.ID).Scan(&selected); err != nil {
						t.Fatal(err)
					}
					if _, err := pg.SavePrompt(a.ID, body+"\n선택하지 않은 새 버전", "fixture", "test"); err != nil {
						t.Fatal(err)
					}
					version++
					if _, err := pg.Exec(`UPDATE agents SET current_prompt_id=$2 WHERE id=$1`, a.ID, selected); err != nil {
						t.Fatal(err)
					}
				}
				expectedVersions[key] = version + updates
			}
			wantTools := map[string]map[string]any{}
			wantDescriptions := map[string]string{}
			for key, baseline := range fixture.Tools {
				previous, err := pg.GetTool(key)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if previous == nil {
						_, _ = pg.Exec(`DELETE FROM tools WHERE key=$1`, key)
					} else {
						_ = pg.UpsertToolForce(key, previous.Description, previous.Schema, koreanDefaultsJSON(t, previous.Agents))
						_, _ = pg.Exec(`UPDATE tools SET enabled=$2,deferred=$3 WHERE key=$1`, key, previous.Enabled, previous.Deferred)
					}
				})
				var schema map[string]any
				if err := json.Unmarshal(koreanDefaultsJSON(t, defaults[key].Schema), &schema); err != nil {
					t.Fatal(err)
				}
				description := defaults[key].Desc
				if baseline.Description != "" && mode != "current" {
					description = baseline.Description
				}
				for path, value := range baseline.SchemaDescriptions {
					if mode != "current" && explorationDescriptionField(schema, path) != nil {
						explorationDescriptionField(schema, path)["description"] = value
					}
				}
				if key == "report_finding" && mode != "current" {
					props := schema["properties"].(map[string]any)
					var refs map[string]any
					if err := json.Unmarshal(koreanDefaultsJSON(t, baseline.SchemaFields["traffic_refs"]), &refs); err != nil {
						t.Fatal(err)
					}
					props["traffic_refs"] = refs
					field := explorationDescriptionField(schema, "properties/evidence_hint_id/description")
					field["type"] = "integer"
					if mode == "localized description" {
						field["description"] = explorationDescriptionField(defaults[key].Schema, "properties/evidence_hint_id/description")["description"]
					}
					if mode == "custom" {
						field["minimum"] = json.Number("10")
						refs["maxItems"] = json.Number("1")
					}
				}
				schema["default"] = json.Number("9007199254740993")
				if mode == "custom" {
					description += " 사용자 도구 설명"
					for path := range baseline.SchemaDescriptions {
						if explorationDescriptionField(schema, path) == nil {
							continue
						}
						explorationDescriptionField(schema, path)["description"] = "사용자 필드 설명"
					}
				}
				if mode == "tool deleted" && key == "report_finding" {
					if _, err := pg.Exec(`DELETE FROM tools WHERE key=$1`, key); err != nil {
						t.Fatal(err)
					}
					continue
				}
				if err := pg.UpsertToolForce(key, description, koreanDefaultsJSON(t, schema), json.RawMessage(`["worker"]`)); err != nil {
					t.Fatal(err)
				}
				if _, err := pg.Exec(`UPDATE tools SET enabled=false,deferred=true WHERE key=$1`, key); err != nil {
					t.Fatal(err)
				}
				wantDescriptions[key] = description
				if mode != "custom" {
					wantDescriptions[key] = defaults[key].Desc
					for path := range baseline.SchemaDescriptions {
						if explorationDescriptionField(defaults[key].Schema, path) == nil {
							continue
						}
						explorationDescriptionField(schema, path)["description"] = explorationDescriptionField(defaults[key].Schema, path)["description"]
					}
					if key == "report_finding" {
						explorationDescriptionField(schema, "properties/evidence_hint_id/description")["type"] = []string{"integer", "null"}
						schema["properties"].(map[string]any)["traffic_refs"] = defaults[key].Schema["properties"].(map[string]any)["traffic_refs"]
					}
				}
				wantTools[key] = schema
			}
			if mode == "retry after schema error" {
				if _, err := pg.Exec(`UPDATE tools SET schema='42'::jsonb WHERE key='prove_goal'`); err != nil {
					t.Fatal(err)
				}
				if err := s.refreshExplorationRestorationDefaults(); err == nil {
					t.Fatal("invalid persisted tool schema did not fail the migration")
				}
				for key, id := range agentIDs {
					if got, err := pg.CurrentPrompt(id); err != nil || got != previous.Prompts[key] {
						t.Fatalf("%s prompt partially migrated after failure: %v", key, err)
					}
					var version int
					if err := pg.QueryRow(`SELECT MAX(version) FROM agent_prompts WHERE agent_id=$1`, id).Scan(&version); err != nil || version != expectedVersions[key]-1 {
						t.Fatalf("%s prompt history partially migrated: %v", key, err)
					}
				}
				var completed bool
				if err := pg.QueryRow(`SELECT EXISTS(SELECT 1 FROM settings WHERE key=$1 AND value='true')`, explorationRestorationDefaultsFlag).Scan(&completed); err != nil || completed {
					t.Fatalf("failed migration recorded as complete: %v", err)
				}
				if _, err := pg.Exec(`UPDATE tools SET schema=$1::jsonb WHERE key='prove_goal'`, string(koreanDefaultsJSON(t, wantTools["prove_goal"]))); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err := s.refreshExplorationRestorationDefaults(); err != nil {
					t.Fatal(err)
				}
			}
			for key, id := range agentIDs {
				got, err := pg.CurrentPrompt(id)
				if err != nil || got != wantPrompts[key] {
					t.Fatalf("%s prompt changed incorrectly: %v", key, err)
				}
				var version int
				if err := pg.QueryRow(`SELECT MAX(version) FROM agent_prompts WHERE agent_id=$1`, id).Scan(&version); err != nil || version != expectedVersions[key] {
					t.Fatalf("%s version=%d want=%d: %v", key, version, expectedVersions[key], err)
				}
			}
			for key := range fixture.Tools {
				got, err := pg.GetTool(key)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "tool deleted" && key == "report_finding" {
					if got != nil {
						t.Fatal("deleted tool restored")
					}
					continue
				}
				var equal bool
				if err := pg.QueryRow(`SELECT $1::jsonb=$2::jsonb`, string(koreanDefaultsJSON(t, wantTools[key])), string(got.Schema)).Scan(&equal); err != nil {
					t.Fatal(err)
				}
				if !equal || got.Description != wantDescriptions[key] || got.Enabled || !got.Deferred || !reflect.DeepEqual(got.Agents, []string{"worker"}) {
					t.Fatalf("%s schema, user settings or numeric defaults changed incorrectly", key)
				}
			}
		})
	}
}
