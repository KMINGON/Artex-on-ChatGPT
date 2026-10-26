package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/Autumn-27/artex/db"
)

const settingResourcePreset = "resource_preset"
const settingBrowserLimit = "browser_concurrency_limit"
const defaultBrowserLimit = 4

type resourcePreset struct {
	ID       string `json:"id"`
	Version  int    `json:"version"`
	Workers  int    `json:"workers"`
	Tasks    int    `json:"tasks"`
	Browsers int    `json:"browsers"`
	Tier     int    `json:"tier"`
}

var resourcePresets = []resourcePreset{
	{ID: "low", Version: 1, Workers: 2, Tasks: 2, Browsers: 2, Tier: 1},
	{ID: "medium", Version: 1, Workers: 5, Tasks: 3, Browsers: 4, Tier: 2},
	{ID: "high", Version: 1, Workers: 10, Tasks: 5, Browsers: 8, Tier: 3},
}

func findResourcePreset(id string) (resourcePreset, bool) {
	for _, preset := range resourcePresets {
		if preset.ID == id {
			return preset, true
		}
	}
	return resourcePreset{}, false
}
func (m *Manager) browserLimit() int {
	if m.pg != nil {
		value, _, _ := m.pg.GetSetting(settingBrowserLimit)
		if n, err := strconv.Atoi(value); err == nil && n > 0 && n <= 64 {
			return n
		}
	}
	return defaultBrowserLimit
}
func (m *Manager) resourcePresetID() string {
	if m.pg == nil {
		return "custom"
	}
	id, _, _ := m.pg.GetSetting(settingResourcePreset)
	preset, ok := findResourcePreset(id)
	if !ok {
		return "custom"
	}
	on, limit := m.ConcurrencyLimit()
	if m.Workers() != preset.Workers || !on || limit != preset.Tasks || m.browserLimit() != preset.Browsers {
		return "custom"
	}

	return id
}
func (m *Manager) currentResourceProfile() *db.ExecutionProfile {
	m.resourceSettingsMu.Lock()
	defer m.resourceSettingsMu.Unlock()
	workers := defaultWorkers
	if m.pg != nil {
		workers = m.Workers()
	}
	return &db.ExecutionProfile{ID: m.resourcePresetID(), Version: 1, Workers: workers, BrowserLimit: m.browserLimit()}
}
func (s *Server) getPresets(w http.ResponseWriter, r *http.Request) {
	// Keep the old catalog shape without implying any tool configuration changes.
	writeJSON(w, 200, map[string]any{"resources": resourcePresets, "tools": []any{}})
}

func (s *Server) applyResourcePreset(w http.ResponseWriter, r *http.Request) {
	s.m.resourceSettingsMu.Lock()
	defer s.m.resourceSettingsMu.Unlock()
	var input struct {
		ID string `json:"id"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input) != nil {
		writeErr(w, 400, "자원 프리셋을 확인해 주세요")
		return
	}
	preset, ok := findResourcePreset(input.ID)
	if !ok && input.ID != "custom" {
		writeErr(w, 400, "알 수 없는 자원 프리셋입니다")
		return
	}
	if input.ID == "custom" {
		if err := s.m.pg.SetSetting(settingResourcePreset, "custom"); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, s.settingsPayload())
		return
	}
	if err := s.saveResourcePreset(preset); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if s.browsers != nil {
		s.browsers.SetLimit(preset.Browsers)
	}
	go s.reconcileConcurrency()
	writeJSON(w, 200, s.settingsPayload())
}
func (s *Server) saveResourcePreset(preset resourcePreset) error {
	tx, err := s.m.pg.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	settings := map[string]string{
		settingResourcePreset: preset.ID, settingWorkers: strconv.Itoa(preset.Workers),
		settingConcurrencyOn: "true", settingConcurrencyLimit: strconv.Itoa(preset.Tasks),
		settingBrowserLimit: strconv.Itoa(preset.Browsers),
	}
	for key, value := range settings {
		if _, err = tx.Exec(`INSERT INTO settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=CURRENT_TIMESTAMP`, key, value); err != nil {
			return err
		}
	}
	return tx.Commit()
}
