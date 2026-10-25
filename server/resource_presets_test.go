package server

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func resourcePresetRig(t *testing.T) *Server {
	t.Helper()
	// Preset tool bindings use PostgreSQL JSONB operators. Temporary tables on
	// one connection isolate the fixture without modifying persisted settings.
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires an explicitly configured isolated ARTEX_PG_DSN")
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = conn.Close() })
	pg := &db.DB{DB: conn}
	schema := `SET search_path TO pg_temp;
 CREATE TEMP TABLE settings(key TEXT PRIMARY KEY,value TEXT,updated_at TIMESTAMPTZ);
 CREATE TEMP TABLE tools(key TEXT PRIMARY KEY,enabled BOOLEAN,agents JSONB);
 INSERT INTO tools VALUES('list_follow_ups',false,'[]'),('update_follow_ups',false,'[]'),('unrelated',false,'["custom"]');
 CREATE TEMP TABLE agents(id INTEGER PRIMARY KEY,key TEXT);
 INSERT INTO agents VALUES(1,'worker'),(2,'pentest'),(3,'auto'),(4,'mainagent'),(5,'planner');
 CREATE TEMP TABLE mcp_servers(id INTEGER PRIMARY KEY,name TEXT,enabled BOOLEAN,args TEXT,env TEXT);
 INSERT INTO mcp_servers VALUES(1,'browser',false,'["custom-argument"]','{"fixture":"retained"}'),(2,'unrelated',false,'[]','{}');
 CREATE TEMP TABLE agent_visibility(agent_id INTEGER,resource_kind TEXT,resource_id INTEGER,mcp_tool_name TEXT DEFAULT '',enabled BOOLEAN,UNIQUE(agent_id,resource_kind,resource_id,mcp_tool_name));
 CREATE TEMP TABLE agent_skill_visibility(agent_id INTEGER,skill_name TEXT,enabled BOOLEAN,UNIQUE(agent_id,skill_name));`
	if _, err := pg.Exec(schema); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "api-recon"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "api-recon", "SKILL.md"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	return &Server{m: &Manager{pg: pg, resourceSkillDir: dir}, skillDir: dir}
}
func TestResourcePresetAppliesAtomicallyAndPreservesUnrelatedSettings(t *testing.T) {
	s := resourcePresetRig(t)
	if _, err := s.m.pg.Exec(`INSERT INTO settings(key,value) VALUES('global_proxy','http://fixture.invalid:8888'),('output_language','en')`); err != nil {
		t.Fatal(err)
	}
	medium, _ := findResourcePreset("medium")
	if err := s.saveResourcePreset(medium); err != nil {
		t.Fatal(err)
	}
	if s.m.Workers() != 5 || s.m.browserLimit() != 4 || s.m.resourcePresetID() != "medium" {
		t.Fatalf("preset not applied: %+v", s.m.currentResourceProfile())
	}
	if !s.m.pg.GetBool(settingTrafficCapture, false) || !s.m.pg.GetBool(settingAgentTrafficBinding, false) {
		t.Fatal("evidence tools not enabled")
	}
	var args, env string
	var unrelated bool
	if err := s.m.pg.QueryRow(`SELECT args,env FROM mcp_servers WHERE name='browser'`).Scan(&args, &env); err != nil {
		t.Fatal(err)
	}
	if args != `["custom-argument"]` || env != `{"fixture":"retained"}` {
		t.Fatal("custom MCP config overwritten")
	}
	if err := s.m.pg.QueryRow(`SELECT enabled FROM mcp_servers WHERE name='unrelated'`).Scan(&unrelated); err != nil || unrelated {
		t.Fatal("unrelated tool enabled")
	}
	if value, _, _ := s.m.pg.GetSetting("global_proxy"); value != "http://fixture.invalid:8888" {
		t.Fatal("external proxy changed")
	}
	if value, _, _ := s.m.pg.GetSetting("output_language"); value != "en" {
		t.Fatal("language changed")
	}
	assertLegacyToolsUnchanged(t, s)
	snapshot := s.m.currentResourceProfile()
	high, _ := findResourcePreset("high")
	if err := s.saveResourcePreset(high); err != nil {
		t.Fatal(err)
	}
	if snapshot.Workers != 5 || snapshot.ID != "medium" {
		t.Fatal("existing task snapshot changed")
	}
	if s.m.currentResourceProfile().Workers != 10 {
		t.Fatal("new task misses selected preset")
	}
	if _, err := s.m.pg.Exec(`UPDATE mcp_servers SET enabled=false WHERE name='browser'`); err != nil {
		t.Fatal(err)
	}
	if s.m.resourcePresetID() != "custom" {
		t.Fatal("manual tool change still shown as preset")
	}
	low, _ := findResourcePreset("low")
	if err := s.saveResourcePreset(low); err != nil {
		t.Fatal(err)
	}
	if s.m.pg.GetBool(settingTrafficCapture, true) || s.m.browserLimit() != 2 {
		t.Fatal("low did not apply its independent capture/resource settings")
	}
	assertLegacyToolsUnchanged(t, s)
	if s.m.resourcePresetID() != "low" {
		t.Fatal("low must keep browser available")
	}
}
func TestResourcePresetCatalogAndDefaults(t *testing.T) {
	if _, ok := findResourcePreset("missing"); ok {
		t.Fatal("invalid preset accepted")
	}
	got := (&Manager{}).currentResourceProfile()
	if got.ID != "custom" || got.Workers != 3 || got.BrowserLimit != 4 {
		t.Fatalf("legacy defaults=%+v", got)
	}
}

func TestResourcePresetRollsBackOnBindingFailure(t *testing.T) {
	s := resourcePresetRig(t)
	if _, err := s.m.pg.Exec(`DROP TABLE agent_skill_visibility`); err != nil {
		t.Fatal(err)
	}
	medium, _ := findResourcePreset("medium")
	if err := s.saveResourcePreset(medium); err == nil {
		t.Fatal("broken binding accepted")
	}
	var enabled bool
	if err := s.m.pg.QueryRow(`SELECT enabled FROM mcp_servers WHERE name='browser'`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("MCP update leaked from rolled back preset")
	}
	if err := s.m.pg.QueryRow(`SELECT enabled FROM tools WHERE key='list_follow_ups'`).Scan(&enabled); err != nil || enabled {
		t.Fatalf("tool update leaked from rolled back preset: enabled=%t err=%v", enabled, err)
	}
	if _, present, err := s.m.pg.GetSetting(settingResourcePreset); err != nil || present {
		t.Fatalf("partial settings persisted: %v", err)
	}
}

func TestResourcePresetSkipsUninstalledSkills(t *testing.T) {
	s := resourcePresetRig(t)
	if err := os.RemoveAll(filepath.Join(s.skillDir, "api-recon")); err != nil {
		t.Fatal(err)
	}
	medium, _ := findResourcePreset("medium")
	if err := s.saveResourcePreset(medium); err != nil {
		t.Fatal(err)
	}
	if got := s.m.resourcePresetID(); got != "medium" {
		t.Fatalf("uninstalled skill changed selection: %s", got)
	}
}
func TestResourcePresetCatalogToolDetectsManualChanges(t *testing.T) {
	s := resourcePresetRig(t)
	medium, _ := findResourcePreset("medium")
	if err := s.saveResourcePreset(medium); err != nil {
		t.Fatal(err)
	}
	if _, err := s.m.pg.Exec(`INSERT INTO tools VALUES('fixture',true,'["worker"]')`); err != nil {
		t.Fatal(err)
	}
	previous := presetTools
	presetTools = append(append([]presetTool(nil), presetTools...), presetTool{Kind: "tool", Key: "fixture", MinTier: 2, Agents: []string{"worker"}})
	defer func() { presetTools = previous }()
	if got := s.m.resourcePresetID(); got != "medium" {
		t.Fatalf("valid built-in tool failed: %s", got)
	}
	if _, err := s.m.pg.Exec(`UPDATE tools SET agents='[]' WHERE key='fixture'`); err != nil {
		t.Fatal(err)
	}
	if got := s.m.resourcePresetID(); got != "custom" {
		t.Fatalf("removed tool binding ignored: %s", got)
	}
}

func TestPresetCatalogContainsResourceSettingsOnly(t *testing.T) {
	request := httptest.NewRequest("GET", "/api/presets", nil)
	request.Header.Set("Accept-Language", "en")
	response := httptest.NewRecorder()
	(&Server{}).getPresets(response, request)
	var result map[string]json.RawMessage
	if response.Code != 200 {
		t.Fatalf("catalog status=%d", response.Code)
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if _, ok := result["policies"]; ok {
		t.Fatal("task policies must not be exposed as resource settings")
	}
	if result["resources"] == nil || result["tools"] == nil {
		t.Fatal("resource catalog is incomplete")
	}
	var tools []presetTool
	if err := json.Unmarshal(result["tools"], &tools); err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.Key == "list_follow_ups" || tool.Key == "update_follow_ups" {
			t.Fatalf("retired follow-up state tool remains in preset catalog: %s", tool.Key)
		}
	}
}

func assertLegacyToolsUnchanged(t *testing.T, s *Server) {
	t.Helper()
	for key, want := range map[string][]string{
		"list_follow_ups":   {},
		"update_follow_ups": {},
		"unrelated":         {"custom"},
	} {
		var enabled bool
		var raw []byte
		if err := s.m.pg.QueryRow(`SELECT enabled,agents FROM tools WHERE key=$1`, key).Scan(&enabled, &raw); err != nil {
			t.Fatal(err)
		}
		var roles []string
		if err := json.Unmarshal(raw, &roles); err != nil {
			t.Fatal(err)
		}
		slices.Sort(roles)
		slices.Sort(want)
		if enabled || !slices.Equal(roles, want) {
			t.Fatalf("%s enabled=%t roles=%v want=%v", key, enabled, roles, want)
		}
	}
}
