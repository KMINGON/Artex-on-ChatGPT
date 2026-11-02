package server

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"testing"
)

func TestRuntimeDefaultsAndSavedSettingsSurviveRestart(t *testing.T) {
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
	open := func() *Manager {
		t.Helper()
		m, err := NewManager(t.TempDir(), "")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { m.Close() })
		return m
	}
	check := func(m *Manager, workers, browsers int, capture bool) {
		t.Helper()
		w := httptest.NewRecorder()
		(&Server{m: m}).getSettings(w, httptest.NewRequest("GET", "/api/settings", nil))
		var got struct {
			Workers     int  `json:"workers"`
			Browsers    int  `json:"browser_concurrency_limit"`
			Capture     bool `json:"traffic_capture"`
			Concurrency bool `json:"task_concurrency_enabled"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || got.Workers != workers || got.Browsers != browsers || got.Capture != capture || got.Concurrency {
			t.Fatalf("settings status=%d values=%+v", w.Code, got)
		}
	}
	m := open()
	check(m, 5, 4, true)
	task, err := m.CreateTask("fixture", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if task.ExecutionProfile == nil || task.ExecutionProfile.Workers != 5 {
		t.Fatalf("new task missed default workers: %+v", task.ExecutionProfile)
	}
	if err := m.SetWorkers(2); err != nil {
		t.Fatal(err)
	}
	if err := pg.SetSetting(settingBrowserLimit, "2"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetTrafficEnabled(false); err != nil {
		t.Fatal(err)
	}
	check(m, 2, 2, false)
	restarted := open()
	check(restarted, 2, 2, false)
	id, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := restarted.pg.GetTask(id)
	if err != nil || saved == nil || saved.ExecutionProfile == nil || saved.ExecutionProfile.Workers != 5 {
		t.Fatalf("existing task worker snapshot changed: task=%+v err=%v", saved, err)
	}
}
