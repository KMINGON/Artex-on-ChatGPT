package server

import (
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/traffic"
)

// Hashes of the evaluated defaults in a6d1c41. Matching individual descriptions
// preserves user edits, bindings, disabled tools and deleted rows during upgrade.
//
//go:embed korean_defaults_v1.json
var koreanDefaultsV1 []byte

type koreanToolBaseline struct {
	Description string            `json:"description"`
	Schema      map[string]string `json:"schema"`
	SchemaHash  string            `json:"schema_hash"`
	Contract    string            `json:"contract"` // Structural fingerprint for regression tests.
}

type koreanDefaultBaseline struct {
	Prompts         map[string]string             `json:"prompts"`
	Tools           map[string]koreanToolBaseline `json:"tools"`
	ReporterTrigger string                        `json:"reporter_trigger"`
}

func defaultTextHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (s *Server) koreanToolDefaults() map[string]agent.ToolSeed {
	out := make(map[string]agent.ToolSeed)
	for _, seed := range agent.BuiltinToolSeeds() {
		out[seed.Key] = seed
	}
	tools := append(s.orchestrationTools(), s.platformTools()...)
	tools = append(tools, s.findingRetestTools()...)
	tools = append(tools, traffic.SeedToolMetas()...)
	for _, tool := range tools {
		out[tool.Name()] = agent.ToolSeed{Key: tool.Name(), Desc: tool.Description(), Schema: tool.InputSchema()}
	}
	return out
}

// Only known description leaves can change. Missing fields, user defaults and
// every structural schema field (required, type, enum, etc.) remain untouched.
func localizeSchemaDescriptions(current, target map[string]any, baseline map[string]string) bool {
	changed := false
	for path, oldHash := range baseline {
		parts := strings.Split(path, "/")
		cur, next := current, target
		for _, part := range parts[:len(parts)-1] {
			cur, _ = cur[part].(map[string]any)
			next, _ = next[part].(map[string]any)
		}
		key := parts[len(parts)-1]
		old, ok := cur[key].(string)
		translated, targetOK := next[key].(string)
		if ok && targetOK && old != translated && defaultTextHash(old) == oldHash {
			cur[key] = translated
			changed = true
		}
	}
	return changed
}

func (s *Server) localizeStoredDefaults() error {
	var baseline koreanDefaultBaseline
	if err := json.Unmarshal(koreanDefaultsV1, &baseline); err != nil {
		return err
	}
	tx, err := s.m.pg.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(7337741011)`); err != nil {
		return err
	}
	const flag = "korean_defaults_v1"
	var done string
	if err := tx.QueryRow(`SELECT value FROM settings WHERE key=$1`, flag).Scan(&done); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if done == "true" {
		return nil
	}
	for key, target := range s.koreanToolDefaults() {
		old, ok := baseline.Tools[key]
		if !ok {
			continue
		}
		var desc string
		var raw []byte
		err := tx.QueryRow(`SELECT description,schema FROM tools WHERE key=$1 AND system AND kind='builtin' FOR UPDATE`, key).Scan(&desc, &raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		var current map[string]any
		// Decode numbers as json.Number so custom numeric defaults keep precision.
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		if err := decoder.Decode(&current); err != nil {
			return fmt.Errorf("%s 도구 스키마 읽기: %w", key, err)
		}
		changed := localizeSchemaDescriptions(current, target.Schema, old.Schema)
		if desc != target.Desc && defaultTextHash(desc) == old.Description {
			desc = target.Desc
			changed = true
		}
		if changed {
			updated, err := json.Marshal(current)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE tools SET description=$2,schema=$3::jsonb,updated_at=now() WHERE key=$1`, key, desc, string(updated)); err != nil {
				return err
			}
		}
	}
	if err := refreshStoredPrompts(tx, baseline.Prompts, "기본 프롬프트 한국어화"); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT id,tool_call_message FROM agent_triggers WHERE agent_key='reporter' AND on_tool_call FOR UPDATE`)
	if err != nil {
		return err
	}
	var triggerIDs []int64
	for rows.Next() {
		var id int64
		var message string
		if err := rows.Scan(&id, &message); err != nil {
			rows.Close()
			return err
		}
		if defaultTextHash(message) == baseline.ReporterTrigger {
			triggerIDs = append(triggerIDs, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, id := range triggerIDs {
		if _, err := tx.Exec(`UPDATE agent_triggers SET tool_call_message=$2 WHERE id=$1`, id, reporterToolCallMessage); err != nil {
			return err
		}
	}
	// The optional retester is first-insert-only; keep a customized name or description.
	if _, err := tx.Exec(`UPDATE agents SET
		name=CASE WHEN name='漏洞复测' THEN '취약점 재검증' ELSE name END,
		description=CASE WHEN description='从漏洞详情手动启动，读取原证据并保存独立复测结论。' THEN '취약점 상세 화면에서 직접 시작하여 원본 증거를 읽고 독립적인 재검증 결론을 저장합니다.' ELSE description END
		WHERE key=$1`, db.FindingRetestAgentKey); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO settings(key,value) VALUES ($1,'true') ON CONFLICT(key) DO UPDATE SET value='true'`, flag); err != nil {
		return err
	}
	return tx.Commit()
}

func refreshStoredPrompts(tx *sql.Tx, hashes map[string]string, note string) error {
	defaults := agent.BuiltinPromptSeeds()
	defaults["reporter"] = agent.ReporterDefaultPrompt
	defaults[db.FindingRetestAgentKey] = agent.RetesterDefaultPrompt
	rows, err := tx.Query(`SELECT a.id,a.key,a.builtin,p.template_text FROM agents a JOIN agent_prompts p ON p.id=a.current_prompt_id ORDER BY a.id FOR UPDATE OF a`)
	if err != nil {
		return err
	}
	type replacement struct {
		id   int64
		text string
	}
	var replacements []replacement
	for rows.Next() {
		var id int64
		var key, text string
		var builtin bool
		if err := rows.Scan(&id, &key, &builtin, &text); err != nil {
			rows.Close()
			return err
		}
		target := ""
		hash := defaultTextHash(text)
		if value, ok := defaults[key]; ok && hash == hashes[key] {
			target = value
		} else if !builtin && hash == hashes["assistant"] {
			target = agent.DefaultAssistantPrompt
		}
		if target != "" && target != text {
			replacements = append(replacements, replacement{id, target})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range replacements {
		var pid int64
		err := tx.QueryRow(`INSERT INTO agent_prompts(agent_id,version,template_text,note,updated_by)
			SELECT $1,COALESCE(MAX(version),0)+1,$2,$3,'system' FROM agent_prompts WHERE agent_id=$1 RETURNING id`, item.id, item.text, note).Scan(&pid)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE agents SET current_prompt_id=$2 WHERE id=$1`, item.id, pid); err != nil {
			return err
		}
	}
	return nil
}
