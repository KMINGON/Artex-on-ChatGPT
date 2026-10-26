package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
)

// Exact a1396fa defaults only. Older localization runs first; edited prompts,
// tool annotations and bindings are not replaced by this policy update.
var explorationPromptBaseline = map[string]string{
	"planner": "0a485eed932840901267e69935e6649dbb1274fc57f374d55f4c1f3fe92ab704",
	"worker":  "101410c3eb5bb40db91f3eff2a810c906af2a00b010b1c62c44c11c567ef9d54",
}

var explorationToolBaseline = koreanToolBaseline{
	Description: "79a90663542e45ba5364d080177e6589656484aeeb29aa2c00ecb7008dc8211d",
	Schema: map[string]string{
		"properties/intents/description":                          "74243d94c19bd10fa81b1f94edcc42839e067f2fc49e0763b3e1f68179328baf",
		"properties/summary/description":                          "dbd46918a209653b7742d37275a4a08b5b810f9ee94caa0c9bf5e264c4efb7f0",
		"properties/intents/items/properties/summary/description": "dbd46918a209653b7742d37275a4a08b5b810f9ee94caa0c9bf5e264c4efb7f0",
	},
}

const explorationDefaultsFlag = "exploration_intent_scope_defaults_v1"

func (s *Server) refreshExplorationDefaults() error {
	tx, err := s.m.pg.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(7337741011)`); err != nil {
		return err
	}
	var done string
	if err := tx.QueryRow(`SELECT value FROM settings WHERE key=$1`, explorationDefaultsFlag).Scan(&done); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if done == "true" {
		return nil
	}
	if err := refreshStoredPrompts(tx, explorationPromptBaseline, "저장된 탐색 기본 지침 갱신"); err != nil {
		return err
	}
	var description string
	var raw []byte
	err = tx.QueryRow(`SELECT description,schema FROM tools WHERE key='add_intent' AND system AND kind='builtin' FOR UPDATE`).Scan(&description, &raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		target := s.koreanToolDefaults()["add_intent"]
		var current map[string]any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&current); err != nil {
			return err
		}
		changed := localizeSchemaDescriptions(current, target.Schema, explorationToolBaseline.Schema)
		if defaultTextHash(description) == explorationToolBaseline.Description && description != target.Desc {
			description, changed = target.Desc, true
		}
		if changed {
			updated, err := json.Marshal(current)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE tools SET description=$1,schema=$2::jsonb,updated_at=now() WHERE key='add_intent'`, description, string(updated)); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`INSERT INTO settings(key,value) VALUES ($1,'true') ON CONFLICT(key) DO UPDATE SET value='true'`, explorationDefaultsFlag); err != nil {
		return err
	}
	return tx.Commit()
}
