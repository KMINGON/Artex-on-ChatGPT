package server

import (
	"bytes"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"

	"github.com/Autumn-27/artex/agent"
)

const originalBehaviorDefaultsFlag = "original_behavior_defaults_v1"

// Fingerprints of the 8d539f9 defaults. Only unchanged fields are restored;
// explicit edits, selected prompt history, bindings and execution limits survive.
//
//go:embed original_behavior_defaults.json
var originalBehaviorDefaultsJSON []byte

type originalBehaviorBaseline struct {
	Prompts       map[string]string             `json:"prompts"`
	Wrapup        map[string]string             `json:"wrapup"`
	TimeoutWrapup map[string]string             `json:"timeout_wrapup"`
	Tools         map[string]koreanToolBaseline `json:"tools"`
}

func (s *Server) refreshOriginalBehaviorDefaults() error {
	var baseline originalBehaviorBaseline
	if err := json.Unmarshal(originalBehaviorDefaultsJSON, &baseline); err != nil {
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
	var done string
	if err := tx.QueryRow(`SELECT value FROM settings WHERE key=$1`, originalBehaviorDefaultsFlag).Scan(&done); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if done == "true" {
		return nil
	}
	for _, hashes := range []map[string]string{explorationPromptBaseline, explorationRestorationPromptBaseline, explorationParityPromptBaseline, explorationGuidancePromptBaseline, baseline.Prompts} {
		if err := refreshStoredPrompts(tx, hashes, "원본 탐색 방식의 기본 지침 적용"); err != nil {
			return err
		}
	}
	for key, old := range baseline.Wrapup {
		var wrapup, timeout string
		err := tx.QueryRow(`SELECT wrapup_prompt,task_timeout_wrapup_prompt FROM agents WHERE key=$1 AND builtin FOR UPDATE`, key).Scan(&wrapup, &timeout)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		nextWrapup, nextTimeout := wrapup, timeout
		if wrapup != "" && defaultTextHash(wrapup) == old {
			nextWrapup = agent.WrapupDefault(key)
		}
		if timeout != "" && defaultTextHash(timeout) == baseline.TimeoutWrapup[key] {
			nextTimeout = agent.TaskTimeoutWrapupDefault(key)
		}
		if nextWrapup != wrapup || nextTimeout != timeout {
			if _, err := tx.Exec(`UPDATE agents SET wrapup_prompt=$2,task_timeout_wrapup_prompt=$3 WHERE key=$1 AND builtin`, key, nextWrapup, nextTimeout); err != nil {
				return err
			}
		}
	}
	defaults := s.koreanToolDefaults()
	for key, old := range baseline.Tools {
		target, ok := defaults[key]
		if !ok {
			continue
		}
		var description string
		var raw []byte
		err := tx.QueryRow(`SELECT description,schema FROM tools WHERE key=$1 AND system AND kind='builtin' FOR UPDATE`, key).Scan(&description, &raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		var schema map[string]any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&schema); err != nil {
			return err
		}
		changed := localizeSchemaDescriptions(schema, target.Schema, old.Schema)
		if defaultTextHash(description) == old.Description && description != target.Desc {
			description = target.Desc
			changed = true
		}
		if key == "prove_goal" {
			props, _ := schema["properties"].(map[string]any)
			field, _ := props["completion_review"].(map[string]any)
			description, _ := field["description"].(string)
			known := defaultTextHash(description) == old.Schema["properties/completion_review/description"] || defaultTextHash(description) == explorationRestorationToolBaseline["prove_goal"].Schema["properties/completion_review/description"]
			if len(field) == 2 && field["type"] == "string" && known {
				delete(props, "completion_review")
				changed = true
			}
		}
		if changed {
			encoded, err := json.Marshal(schema)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE tools SET description=$2,schema=$3::jsonb,updated_at=now() WHERE key=$1`, key, description, string(encoded)); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`INSERT INTO settings(key,value) VALUES($1,'true') ON CONFLICT(key) DO UPDATE SET value='true'`, originalBehaviorDefaultsFlag); err != nil {
		return err
	}
	return tx.Commit()
}
