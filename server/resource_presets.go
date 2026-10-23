package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
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

// Add an installed tool here once; every preset uses its minimum tier. Presets
// never install a dependency, supply a credential, or enable unrelated custom tools.
type presetTool struct {
	Kind    string   `json:"kind"`
	Key     string   `json:"key"`
	MinTier int      `json:"min_tier"`
	Agents  []string `json:"agents,omitempty"`
}

var presetTools = []presetTool{
	{Kind: "mcp", Key: "browser", MinTier: 1, Agents: []string{"worker", "pentest"}},
	{Kind: "skill", Key: "api-recon", MinTier: 1, Agents: []string{"worker", "pentest", "auto"}},
	{Kind: "setting", Key: settingTrafficCapture, MinTier: 2},
	{Kind: "setting", Key: settingAgentTrafficBinding, MinTier: 2},
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
	for _, tool := range presetTools {
		expected := preset.Tier >= tool.MinTier
		if tool.Kind == "setting" && m.pg.GetBool(tool.Key, false) != expected {
			return "custom"
		}
		if tool.Kind == "mcp" {
			var total, matching int
			if err := m.pg.QueryRow(`SELECT COUNT(*),COUNT(*) FILTER(WHERE enabled=$2) FROM mcp_servers WHERE name=$1`, tool.Key, expected).Scan(&total, &matching); err != nil {
				return "custom"
			}
			if total == 0 {
				continue
			}
			if matching != total {
				return "custom"
			}
			for _, agent := range tool.Agents {
				var enabled bool
				if err := m.pg.QueryRow(`SELECT v.enabled FROM agent_visibility v JOIN agents a ON a.id=v.agent_id JOIN mcp_servers m ON m.id=v.resource_id WHERE a.key=$1 AND m.name=$2 AND v.resource_kind='mcp' AND v.mcp_tool_name=''`, agent, tool.Key).Scan(&enabled); err != nil || enabled != expected {
					return "custom"
				}
			}
		}
		if tool.Kind == "skill" {
			if m.resourceSkillDir != "" {
				if _, err := os.Stat(filepath.Join(m.resourceSkillDir, tool.Key, "SKILL.md")); err != nil {
					continue
				}
			}
			for _, agent := range tool.Agents {
				var enabled bool
				if err := m.pg.QueryRow(`SELECT v.enabled FROM agent_skill_visibility v JOIN agents a ON a.id=v.agent_id WHERE a.key=$1 AND v.skill_name=$2`, agent, tool.Key).Scan(&enabled); err != nil || enabled != expected {
					return "custom"
				}
			}
		}
		if tool.Kind == "tool" {
			var enabled bool
			var raw []byte
			err := m.pg.QueryRow(`SELECT enabled,agents FROM tools WHERE key=$1`, tool.Key).Scan(&enabled, &raw)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil || enabled != expected {
				return "custom"
			}
			if expected {
				var agents []string
				if json.Unmarshal(raw, &agents) != nil {
					return "custom"
				}
				for _, agent := range tool.Agents {
					if !slices.Contains(agents, agent) {
						return "custom"
					}
				}
			}
		}
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
	writeJSON(w, 200, map[string]any{"resources": resourcePresets, "tools": presetTools})
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
	s.m.mu.Lock()
	s.m.trafficOn = s.m.pg.GetBool(settingTrafficCapture, false)
	s.m.mu.Unlock()
	s.m.syncBrowserMCPProxy()
	if s.browsers != nil {
		s.browsers.SetLimit(preset.Browsers)
	}
	if err := s.reapplyLLMSettings(); err != nil {
		writeErr(w, 503, err.Error())
		return
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
	for _, tool := range presetTools {
		enabled := preset.Tier >= tool.MinTier
		switch tool.Kind {
		case "setting":
			settings[tool.Key] = strconv.FormatBool(enabled)
		case "mcp":
			// Only configure a resource already registered in ARTEX; preserve its args/env.
			if _, err = tx.Exec(`UPDATE mcp_servers SET enabled=$1 WHERE name=$2`, enabled, tool.Key); err != nil {
				return err
			}
			for _, agent := range tool.Agents {
				if _, err = tx.Exec(`INSERT INTO agent_visibility(agent_id,resource_kind,resource_id,enabled)
SELECT a.id,'mcp',m.id,$3 FROM agents a CROSS JOIN mcp_servers m WHERE a.key=$1 AND m.name=$2
ON CONFLICT (agent_id,resource_kind,resource_id,mcp_tool_name) DO UPDATE SET enabled=EXCLUDED.enabled`, agent, tool.Key, enabled); err != nil {
					return err
				}
			}
		case "skill":
			if _, err = os.Stat(filepath.Join(s.skillDir, tool.Key, "SKILL.md")); err != nil {
				continue
			}
			for _, agent := range tool.Agents {
				if _, err = tx.Exec(`INSERT INTO agent_skill_visibility(agent_id,skill_name,enabled)
SELECT id,$2,$3 FROM agents WHERE key=$1 ON CONFLICT(agent_id,skill_name) DO UPDATE SET enabled=EXCLUDED.enabled`, agent, tool.Key, enabled); err != nil {
					return err
				}
			}
		case "tool":
			if _, err = tx.Exec(`UPDATE tools SET enabled=$1 WHERE key=$2`, enabled, tool.Key); err != nil {
				return err
			}
			for _, agent := range tool.Agents {
				if enabled {
					_, err = tx.Exec(`UPDATE tools SET agents=agents||to_jsonb($1::text) WHERE key=$2 AND NOT(agents ? $1)`, agent, tool.Key)
				}
				if err != nil {
					return err
				}
			}
		default:
			return errors.New("도구 프리셋 등록 종류가 올바르지 않습니다")
		}
	}
	for key, value := range settings {
		if _, err = tx.Exec(`INSERT INTO settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=CURRENT_TIMESTAMP`, key, value); err != nil {
			return err
		}
	}
	return tx.Commit()
}
