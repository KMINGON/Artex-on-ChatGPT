package server

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
)

func TestWebSearchDefaultsReachRuntimeAndSettings(t *testing.T) {
	pg := originalDefaultsTestDB(t)
	var database string
	if err := pg.QueryRow(`SELECT current_database()`).Scan(&database); err != nil {
		t.Fatal(err)
	}
	dsn, err := url.Parse(os.Getenv("ARTEX_PG_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	dsn.Path = "/" + database
	t.Setenv("ARTEX_PG_DSN", dsn.String())
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	s := &Server{m: m}
	for _, key := range []string{"pentest", "worker", "goals", "planner", "mainagent", "auto"} {
		got := s.webSearchFor(key)
		if got.Enabled || got.Backend != "ddgs" {
			t.Fatalf("%s runtime search: enabled=%v backend=%s", key, got.Enabled, got.Backend)
		}
	}
	w := httptest.NewRecorder()
	s.getSettings(w, httptest.NewRequest("GET", "/api/settings", nil))
	var settings struct {
		Enabled bool   `json:"web_search_enabled"`
		Backend string `json:"web_search_backend"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &settings); err != nil || w.Code != 200 || settings.Enabled || settings.Backend != "ddgs" {
		t.Fatalf("settings API did not expose defaults: status=%d settings=%+v err=%v", w.Code, settings, err)
	}
	if err := m.SetWebSearch(true, "ddgs", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"pentest", "worker", "goals", "planner", "mainagent", "auto"} {
		if got, want := s.webSearchFor(key).Enabled, key == "pentest" || key == "worker"; got != want {
			t.Fatalf("%s runtime search after enabling: enabled=%v, want %v", key, got, want)
		}
	}
	if _, err := pg.Exec(`UPDATE agents SET web_search=false WHERE key='pentest'`); err != nil {
		t.Fatal(err)
	}
	if s.webSearchFor("pentest").Enabled || !s.webSearchFor("worker").Enabled {
		t.Fatal("agent-level switch did not gate runtime search")
	}
	if err := m.SetWebSearch(false, "ddgs", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if s.webSearchFor("worker").Enabled {
		t.Fatal("global switch did not gate runtime search")
	}
	restarted, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restarted.Close() })
	if (&Server{m: restarted}).webSearchFor("worker").Enabled {
		t.Fatal("restart re-enabled disabled runtime search")
	}
}
