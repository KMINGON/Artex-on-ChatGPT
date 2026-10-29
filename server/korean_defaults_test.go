package server

import (
	"database/sql"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestKoreanToolContractsMatchReviewedChanges(t *testing.T) {
	var baseline koreanDefaultBaseline
	if err := json.Unmarshal(koreanDefaultsV1, &baseline); err != nil {
		t.Fatal(err)
	}
	// Keep the production localization baseline immutable: its hashes recognize
	// historical defaults without overwriting user edits. These reviewed contracts
	// cover fact/intent linking, required goal reasons without completion_review,
	// nullable optional finding evidence, and batch goal/constraint field exposure; all
	// other tool schemas must still match the original translation contract.
	functionalUpdates := map[string]string{
		"record_fact":     "33486853960f5f14a8bfbe6a822c6d52633ef3496ada96eff4065ca0de23f32e",
		"add_intent":      "cd849d60948f02d4779622d246be160883c503817cdc5cdf47af27790aa7f5ce",
		"prove_goal":      "4742a0d1ccf9a0b864b3a860cba896b184887f0eed0dab697053461c308448ff",
		"report_finding":  "9d78621ed7c485bdc0865addb48a871d197f077cc5f482d5b6ff8bd0acc32f88",
		"set_goals":       "a903e89bf379922de680a7092b87e935632a08f638376d549e52639ba7011fc4",
		"set_constraints": "3d31c5e5e26cb8efe1efe84b938aeeabeaa3fb8fe57e52776ed4f4f4aba831ed",
	}
	strip := func(value any) any { return value }
	strip = func(value any) any {
		switch v := value.(type) {
		case map[string]any:
			out := make(map[string]any)
			for key, child := range v {
				if _, isString := child.(string); key != "description" || !isString {
					out[key] = strip(child)
				}
			}
			return out
		case []any:
			out := make([]any, len(v))
			for i, child := range v {
				out[i] = strip(child)
			}
			return out
		default:
			return value
		}
	}
	for key, target := range (&Server{}).koreanToolDefaults() {
		raw, err := json.Marshal(target.Schema)
		if err != nil {
			t.Fatal(err)
		}
		var normalized map[string]any
		if err := json.Unmarshal(raw, &normalized); err != nil {
			t.Fatal(err)
		}
		contract := strip(normalized).(map[string]any)
		structural, err := json.Marshal(contract)
		if err != nil {
			t.Fatal(err)
		}
		expected := baseline.Tools[key].Contract
		if updated, ok := functionalUpdates[key]; ok {
			expected = updated
			delete(functionalUpdates, key)
		}
		if got := defaultTextHash(string(structural)); got != expected {
			t.Errorf("%s: unreviewed schema contract change: got=%s want=%s", key, got, expected)
		}
		if strings.IndexFunc(target.Desc+string(raw), func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0 {
			t.Errorf("%s: Chinese remains in a supplied tool description/schema", key)
		}
	}
	if len(functionalUpdates) != 0 {
		t.Fatalf("reviewed tool contracts missing from catalog: %v", functionalUpdates)
	}
	for key, required := range map[string][]string{
		"record_fact": {"facts", "summary", "intent_id", "detail", "evidence", "confidence", "asset_ids"},
		"add_intent":  {"intents", "summary", "asset_ids", "parent_ids", "priority"},
	} {
		properties, _ := (&Server{}).koreanToolDefaults()[key].Schema["properties"].(map[string]any)
		if len(properties) == 0 {
			t.Fatalf("%s: empty active schema", key)
		}
		for _, field := range required {
			if _, ok := properties[field]; !ok {
				t.Errorf("%s: core field %s removed", key, field)
			}
		}
		if _, ok := properties["follow_ups"]; ok {
			t.Errorf("%s: retired follow_ups field remains active", key)
		}
		if _, ok := properties["follow_up_refs"]; ok {
			t.Errorf("%s: retired follow_up_refs field remains active", key)
		}
	}
}

func TestLocalizeSchemaDescriptionsKeepsUserEditsAndNumbers(t *testing.T) {
	current := map[string]any{"properties": map[string]any{
		"name":   map[string]any{"description": "old", "default": json.Number("9007199254740993")},
		"custom": map[string]any{"description": "edited"},
	}, "required": []any{"name"}}
	target := map[string]any{"properties": map[string]any{
		"name":    map[string]any{"description": "새 설명"},
		"custom":  map[string]any{"description": "덮어쓰면 안 됨"},
		"missing": map[string]any{"description": "추가하면 안 됨"},
	}}
	baseline := map[string]string{"properties/name/description": defaultTextHash("old"), "properties/custom/description": defaultTextHash("old"), "properties/missing/description": defaultTextHash("old")}
	if !localizeSchemaDescriptions(current, target, baseline) {
		t.Fatal("default not localized")
	}
	props := current["properties"].(map[string]any)
	if props["name"].(map[string]any)["description"] != "새 설명" || props["name"].(map[string]any)["default"] != json.Number("9007199254740993") || props["custom"].(map[string]any)["description"] != "edited" || props["missing"] != nil || !reflect.DeepEqual(current["required"], []any{"name"}) {
		t.Fatalf("custom content/structure changed: %#v", current)
	}
	if localizeSchemaDescriptions(current, target, baseline) {
		t.Fatal("not idempotent")
	}
}

func TestKoreanStoredDefaultsUpgradePreservesCustomization(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("isolated PostgreSQL required")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	var legacy struct {
		Assistant       string           `json:"assistant"`
		Auto            string           `json:"auto"`
		ReporterTrigger string           `json:"reporter_trigger"`
		Tools           []agent.ToolSeed `json:"tools"`
	}
	raw, err := os.ReadFile("testdata/korean_defaults_legacy.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{pg: pg}}
	previousFlag, hadFlag, err := pg.GetSetting("korean_defaults_v1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = pg.Exec(`DELETE FROM settings WHERE key='korean_defaults_v1'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if hadFlag {
			_ = pg.SetSetting("korean_defaults_v1", previousFlag)
		} else {
			_, _ = pg.Exec(`DELETE FROM settings WHERE key='korean_defaults_v1'`)
		}
	}()
	for _, seed := range legacy.Tools {
		previous, err := pg.GetTool(seed.Key)
		if err != nil {
			t.Fatal(err)
		}
		defer func(key string, old *db.Tool) {
			if old == nil {
				_, _ = pg.Exec(`DELETE FROM tools WHERE key=$1`, key)
				return
			}
			_, _ = pg.Exec(`UPDATE tools SET description=$2,schema=$3,agents=$4,enabled=$5 WHERE key=$1`, key, old.Description, old.Schema, koreanDefaultsJSON(t, old.Agents), old.Enabled)
		}(seed.Key, previous)
		if seed.Key == "create_skill" {
			seed.Desc = "사용자가 편집한 도구 설명"
			props := seed.Schema["properties"].(map[string]any)
			props["name"].(map[string]any)["description"] = "내 이름 규칙"
			props["name"].(map[string]any)["default"] = "my-skill"
		}
		if err := pg.UpsertToolForce(seed.Key, seed.Desc, koreanDefaultsJSON(t, seed.Schema), json.RawMessage(`["auto"]`)); err != nil {
			t.Fatal(err)
		}
		if _, err := pg.Exec(`UPDATE tools SET enabled=false WHERE key=$1`, seed.Key); err != nil {
			t.Fatal(err)
		}
	}
	var agentIDs []int64
	defer func() {
		for _, id := range agentIDs {
			_, _ = pg.Exec(`DELETE FROM agents WHERE id=$1`, id)
		}
	}()
	for _, key := range []string{"ko_default_test", "ko_custom_test", "assistant"} {
		a, err := pg.CreateAgent(key, key, "test")
		if err != nil {
			t.Fatal(err)
		}
		agentIDs = append(agentIDs, a.ID)
		body := legacy.Assistant
		if key == "ko_custom_test" {
			body += " 사용자 지시"
		}
		if _, err := pg.SavePrompt(a.ID, body, "old", "test"); err != nil {
			t.Fatal(err)
		}
	}
	// A built-in prompt upgrades as a new version; its original version stays available.
	auto, err := pg.GetAgentByKey("auto")
	if err != nil || auto == nil {
		t.Fatalf("auto: %v", err)
	}
	var originalPID sql.NullInt64
	if err := pg.QueryRow(`SELECT current_prompt_id FROM agents WHERE id=$1`, auto.ID).Scan(&originalPID); err != nil {
		t.Fatal(err)
	}
	var originalVersion int
	if err := pg.QueryRow(`SELECT COALESCE(MAX(version),0) FROM agent_prompts WHERE agent_id=$1`, auto.ID).Scan(&originalVersion); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pg.Exec(`UPDATE agents SET current_prompt_id=$2 WHERE id=$1`, auto.ID, originalPID)
		_, _ = pg.Exec(`DELETE FROM agent_prompts WHERE agent_id=$1 AND version>$2`, auto.ID, originalVersion)
	}()
	if _, err := pg.SavePrompt(auto.ID, legacy.Auto, "old", "test"); err != nil {
		t.Fatal(err)
	}
	trigger, err := pg.CreateTrigger(&db.AgentTrigger{AgentKey: "reporter", OnToolCall: true, ToolNames: []string{"report_finding"}, ToolCallMessage: legacy.ReporterTrigger})
	if err != nil {
		t.Fatal(err)
	}
	defer pg.DeleteTrigger(trigger.ID)
	if err := s.localizeStoredDefaults(); err != nil {
		t.Fatal(err)
	}
	if body, err := pg.CurrentPrompt(agentIDs[0]); err != nil || body != agent.DefaultAssistantPrompt {
		t.Fatalf("assistant migration: %q %v", body, err)
	}
	if body, err := pg.CurrentPrompt(agentIDs[2]); err != nil || body != agent.DefaultAssistantPrompt {
		t.Fatalf("agent named assistant: %q %v", body, err)
	}
	if body, err := pg.CurrentPrompt(agentIDs[1]); err != nil || body != legacy.Assistant+" 사용자 지시" {
		t.Fatalf("custom prompt overwritten: %q %v", body, err)
	}
	if body, err := pg.CurrentPrompt(auto.ID); err != nil || body != agent.BuiltinPromptSeeds()["auto"] {
		t.Fatalf("auto migration: %q %v", body, err)
	}
	versions, err := pg.ListPromptVersions(auto.ID)
	if err != nil || len(versions) < 2 || versions[1].Template != legacy.Auto {
		t.Fatalf("prompt history lost: %v", err)
	}
	tool, err := pg.GetTool("create_skill")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	props := schema["properties"].(map[string]any)
	if tool.Enabled || !reflect.DeepEqual(tool.Agents, []string{"auto"}) || tool.Description != "사용자가 편집한 도구 설명" || props["name"].(map[string]any)["description"] != "내 이름 규칙" || props["name"].(map[string]any)["default"] != "my-skill" {
		t.Fatalf("custom tool settings lost: %+v", tool)
	}
	target := s.koreanToolDefaults()["create_skill"].Schema["properties"].(map[string]any)
	if props["description"].(map[string]any)["description"] != target["description"].(map[string]any)["description"] {
		t.Fatal("untouched schema field not localized")
	}
	list, err := pg.GetTool("list_tasks")
	if err != nil || list.Description != s.toolListTasks().Description() || list.Enabled {
		t.Fatalf("default tool upgrade: %+v %v", list, err)
	}
	var message string
	if err := pg.QueryRow(`SELECT tool_call_message FROM agent_triggers WHERE id=$1`, trigger.ID).Scan(&message); err != nil || message != reporterToolCallMessage {
		t.Fatalf("trigger upgrade: %q %v", message, err)
	}
	if err := s.localizeStoredDefaults(); err != nil {
		t.Fatal(err)
	}
	after, err := pg.ListPromptVersions(auto.ID)
	if err != nil || len(after) != len(versions) {
		t.Fatal("repeat startup added prompt versions")
	}
}

func koreanDefaultsJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
