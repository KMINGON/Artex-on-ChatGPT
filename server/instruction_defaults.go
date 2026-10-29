package server

import (
	"bytes"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
)

const instructionDefaultsFlag = "instruction_contract_defaults_v1"

// Fingerprints of the released 9535c25 instructions. Refresh only unchanged
// wording; explicit prompt history, tool bindings and user annotations survive.
//
//go:embed instruction_defaults.json
var instructionDefaultsJSON []byte

func (s *Server) refreshInstructionDefaults() error {
	var baseline koreanDefaultBaseline
	if err := json.Unmarshal(instructionDefaultsJSON, &baseline); err != nil {
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
	if err := tx.QueryRow(`SELECT value FROM settings WHERE key=$1`, instructionDefaultsFlag).Scan(&done); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if done == "true" {
		return nil
	}
	if err := refreshStoredPrompts(tx, baseline.Prompts, "도구 계약과 기록 의미에 맞춘 기본 지침 교정"); err != nil {
		return err
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
		// These shipped batch items were empty objects. Publish the existing fields
		// without replacing a user's item schema or adding whole-batch rejection.
		for _, batch := range []string{"goals", "constraints"} {
			if key != "set_"+batch {
				continue
			}
			props, _ := schema["properties"].(map[string]any)
			field, _ := props[batch].(map[string]any)
			items, _ := field["items"].(map[string]any)
			if len(items) == 1 && items["type"] == "object" {
				nextProps, _ := target.Schema["properties"].(map[string]any)
				nextField, _ := nextProps[batch].(map[string]any)
				field["items"] = nextField["items"]
				changed = true
			}
		}
		// The released hints schema had an undescribed integer-array asset field.
		// Fill only that exact shape; a custom description or schema stays intact.
		if key == "add_hint" {
			field, next := schema, target.Schema
			for _, part := range []string{"properties", "hints", "items", "properties", "asset_ids"} {
				field, _ = field[part].(map[string]any)
				next, _ = next[part].(map[string]any)
			}
			items, _ := field["items"].(map[string]any)
			if len(field) == 2 && field["type"] == "array" && len(items) == 1 && items["type"] == "integer" {
				field["description"] = next["description"]
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
	if _, err := tx.Exec(`INSERT INTO settings(key,value) VALUES($1,'true') ON CONFLICT(key) DO UPDATE SET value='true'`, instructionDefaultsFlag); err != nil {
		return err
	}
	return tx.Commit()
}
