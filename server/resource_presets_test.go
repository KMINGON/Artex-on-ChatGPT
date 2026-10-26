package server

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func resourcePresetRig(t *testing.T) *Server {
	t.Helper()
	// One connection and temporary tables keep every fixture isolated.
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
 INSERT INTO settings(key,value) VALUES
 ('traffic_capture','true'),('agent_traffic_binding','false'),
 ('global_proxy','http://fixture.invalid:8888'),('output_language','en'),
 ('llm_profile','fixture-profile'),('llm_auth_mode','chatgpt'),('reasoning_effort','high');
 CREATE TEMP TABLE tools(key TEXT PRIMARY KEY,enabled BOOLEAN,agents JSONB);
 INSERT INTO tools VALUES('list_follow_ups',false,'[]'),('update_follow_ups',false,'[]'),('unrelated',true,'["custom"]');
 CREATE TEMP TABLE mcp_servers(id INTEGER PRIMARY KEY,name TEXT,enabled BOOLEAN,args TEXT,env TEXT);
 INSERT INTO mcp_servers VALUES(1,'browser',false,'["custom-argument"]','{"fixture":"retained"}'),(2,'unrelated',true,'[]','{}');
 CREATE TEMP TABLE agent_visibility(agent_id INTEGER,resource_kind TEXT,resource_id INTEGER,mcp_tool_name TEXT DEFAULT '',enabled BOOLEAN,UNIQUE(agent_id,resource_kind,resource_id,mcp_tool_name));
 INSERT INTO agent_visibility VALUES(1,'mcp',1,'',false),(2,'mcp',1,'browser_snapshot',true);
 CREATE TEMP TABLE agent_skill_visibility(agent_id INTEGER,skill_name TEXT,enabled BOOLEAN,UNIQUE(agent_id,skill_name));
 INSERT INTO agent_skill_visibility VALUES(1,'api-recon',false),(2,'api-recon',true);`
	if _, err := pg.Exec(schema); err != nil {
		t.Fatal(err)
	}
	return &Server{m: &Manager{pg: pg}}
}

func nonResourceSettings(t *testing.T, s *Server) string {
	t.Helper()
	var snapshot string
	err := s.m.pg.QueryRow(`SELECT jsonb_build_object(
 'settings', (SELECT jsonb_object_agg(key,value) FROM settings WHERE key NOT IN
 ('resource_preset','workers','task_concurrency_enabled','task_concurrency_limit','browser_concurrency_limit')),
 'tools', (SELECT jsonb_agg(to_jsonb(t) ORDER BY key) FROM tools t),
 'mcp', (SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM mcp_servers m),
 'visibility', (SELECT jsonb_agg(to_jsonb(v) ORDER BY agent_id,resource_id,mcp_tool_name) FROM agent_visibility v),
 'skills', (SELECT jsonb_agg(to_jsonb(v) ORDER BY agent_id,skill_name) FROM agent_skill_visibility v)
 )::text`).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestResourcePresetPreservesToolBindingsTrafficAndModelSettings(t *testing.T) {
	s := resourcePresetRig(t)
	before := nonResourceSettings(t, s)
	for _, id := range []string{"medium", "high", "low"} {
		preset, _ := findResourcePreset(id)
		if err := s.saveResourcePreset(preset); err != nil {
			t.Fatal(err)
		}
		if s.m.Workers() != preset.Workers || s.m.browserLimit() != preset.Browsers || s.m.resourcePresetID() != id {
			t.Fatalf("preset not applied: %+v", s.m.currentResourceProfile())
		}
		if on, tasks := s.m.ConcurrencyLimit(); !on || tasks != preset.Tasks {
			t.Fatalf("task concurrency not applied: enabled=%t tasks=%d", on, tasks)
		}
		if after := nonResourceSettings(t, s); after != before {
			t.Fatalf("preset %s changed non-resource settings:\nbefore=%s\nafter=%s", id, before, after)
		}
	}
}

func TestResourcePresetSelectionOnlyTracksResourceCounts(t *testing.T) {
	s := resourcePresetRig(t)
	medium, _ := findResourcePreset("medium")
	if err := s.saveResourcePreset(medium); err != nil {
		t.Fatal(err)
	}
	snapshot := s.m.currentResourceProfile()
	if _, err := s.m.pg.Exec(`UPDATE mcp_servers SET enabled=true WHERE name='browser';
 UPDATE agent_skill_visibility SET enabled=NOT enabled;
 UPDATE settings SET value='false' WHERE key='traffic_capture'`); err != nil {
		t.Fatal(err)
	}
	if got := s.m.resourcePresetID(); got != "medium" {
		t.Fatalf("independent tool settings changed resource selection: %s", got)
	}
	high, _ := findResourcePreset("high")
	if err := s.saveResourcePreset(high); err != nil {
		t.Fatal(err)
	}
	if snapshot.ID != "medium" || snapshot.Workers != 5 {
		t.Fatal("existing task snapshot changed")
	}
	if s.m.currentResourceProfile().Workers != 10 {
		t.Fatal("new task misses selected preset")
	}
	if err := s.m.SetWorkers(7); err != nil {
		t.Fatal(err)
	}
	if got := s.m.resourcePresetID(); got != "custom" {
		t.Fatalf("manual resource change ignored: %s", got)
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

func TestResourcePresetRollsBackOnSettingsFailure(t *testing.T) {
	s := resourcePresetRig(t)
	if _, err := s.m.pg.Exec(`ALTER TABLE settings ADD CONSTRAINT reject_fixture_browser_count
 CHECK(key<>'browser_concurrency_limit' OR value<>'4')`); err != nil {
		t.Fatal(err)
	}
	before := nonResourceSettings(t, s)
	medium, _ := findResourcePreset("medium")
	if err := s.saveResourcePreset(medium); err == nil {
		t.Fatal("invalid settings accepted")
	}
	if after := nonResourceSettings(t, s); after != before {
		t.Fatal("unrelated settings changed after rollback")
	}
	var count int
	if err := s.m.pg.QueryRow(`SELECT COUNT(*) FROM settings WHERE key IN
 ('resource_preset','workers','task_concurrency_enabled','task_concurrency_limit','browser_concurrency_limit')`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial settings persisted: count=%d err=%v", count, err)
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
	if result["resources"] == nil || string(result["tools"]) != "[]" {
		t.Fatal("resource catalog must preserve its shape without changing tools")
	}
}
