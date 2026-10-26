package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
)

const explorationRestorationDefaultsFlag = "exploration_persistence_defaults_v3"

const explorationTrafficRefsBaselineHash = "8f012599a7749cf54e9964e6fe337fb0103893ea966ef562d706a52dad448cf6"

// Exact ca23d12 defaults. The preceding migration recognizes a1396fa; checking
// both also covers installations that already ran that migration.
var explorationRestorationPromptBaseline = map[string]string{
	"planner": "88c53989219e06b500b71ae6dab5bc651766f77b2368ac584986c32c2a7e5481",
	"worker":  "80b7523fafa537424c66229fa16fb6869698e7a521564fb2e31f5ebaf4ebf2d7",
}

// Exact 17e0fca defaults; do not replace edited or explicitly selected history.
var explorationParityPromptBaseline = map[string]string{
	"planner": "aa4fe0d0dcdfb44a3993b9320f89b0ec39eb98fdf7cc828c24f62c487a4584ad",
	"worker":  "6ccb26d55e78ae0e31c5e485ecf260e9a7b8b965fccf5328174eab0f13204807",
}

// Exact 7f38dea defaults; reuse the existing guards for edits and selected history.
var explorationGuidancePromptBaseline = map[string]string{
	"planner": "1dc35c951124410609f64c77c8bf084a7e526ed0709ac33e4e0b7b94bc96e5ef",
	"worker":  "7b9be156b38db7679ba242550d4c499b4478d0cddf6bc0f04bf297e51d1aa1c2",
}

var explorationRestorationToolBaseline = map[string]koreanToolBaseline{
	"add_intent": {
		Description: "5fcfcc1590f7d51f6b1310eadcc27ebc29633f0cdc1e32b828bfc1410fd33d3a",
		Schema: map[string]string{
			"properties/summary/description":                          "81611a9ac065a47e003e7101e7cceb486acf43f4db3ac1468cda1a023f95af78",
			"properties/intents/items/properties/summary/description": "81611a9ac065a47e003e7101e7cceb486acf43f4db3ac1468cda1a023f95af78",
		},
	},
	"prove_goal": {
		Description: "fa2dcb039f47a22e1dc9238d1794451c7a302e436f2747c632dfb2d36184ab5d",
		Schema: map[string]string{
			"properties/completion_review/description": "b02dd81040ce7cfac3e1531b3a77358fe2ddfbe77a73de6b69967576d0e5756b",
		},
	},
	"report_finding": {
		Schema: map[string]string{
			"properties/evidence_hint_id/description": "fbe7312a4059fdeb155f24b2ddd73aaaed1d3755f2a7942584976c05dd22ac42",
		},
	},
	"get_worker_output": {
		Description: "7379fbd778b8c3c06784bc92b822d7bc00f69bf8554b5f6e4f4f9b9cd41b0457",
	},
}

func (s *Server) refreshExplorationRestorationDefaults() error {
	tx, err := s.m.pg.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(7337741011)`); err != nil {
		return err
	}
	var done string
	if err := tx.QueryRow(`SELECT value FROM settings WHERE key=$1`, explorationRestorationDefaultsFlag).Scan(&done); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if done == "true" {
		return nil
	}
	for _, baseline := range []map[string]string{explorationPromptBaseline, explorationRestorationPromptBaseline, explorationParityPromptBaseline, explorationGuidancePromptBaseline} {
		if err := refreshStoredPrompts(tx, baseline, "탐색 지속성과 미완료 검증 인계 기본값 갱신"); err != nil {
			return err
		}
	}
	defaults := s.koreanToolDefaults()
	for key, baseline := range explorationRestorationToolBaseline {
		var description string
		var raw []byte
		err := tx.QueryRow(`SELECT description,schema FROM tools WHERE key=$1 AND system AND kind='builtin' FOR UPDATE`, key).Scan(&description, &raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		var current map[string]any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&current); err != nil {
			return err
		}
		target := defaults[key]
		changed := false
		if key == "report_finding" {
			// Only the untouched optional-ID field may gain null support. Custom
			// constraints/defaults/descriptions remain the user's schema contract.
			props, _ := current["properties"].(map[string]any)
			field, _ := props["evidence_hint_id"].(map[string]any)
			text, _ := field["description"].(string)
			nextProps, _ := target.Schema["properties"].(map[string]any)
			nextField, _ := nextProps["evidence_hint_id"].(map[string]any)
			// An earlier localization step may already have updated the wording.
			if len(field) == 2 && field["type"] == "integer" && (defaultTextHash(text) == baseline.Schema["properties/evidence_hint_id/description"] || text == nextField["description"]) {
				field["type"], changed = nextField["type"], true
			}
			// The original handler also accepted null traffic references. Match
			// the whole old field so custom item/array constraints remain intact.
			refs, err := json.Marshal(props["traffic_refs"])
			if err != nil {
				return err
			}
			if defaultTextHash(string(refs)) == explorationTrafficRefsBaselineHash {
				props["traffic_refs"], changed = nextProps["traffic_refs"], true
			}
		}
		changed = localizeSchemaDescriptions(current, target.Schema, baseline.Schema) || changed
		if description != target.Desc && defaultTextHash(description) == baseline.Description {
			description, changed = target.Desc, true
		}
		if changed {
			updated, err := json.Marshal(current)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE tools SET description=$2,schema=$3::jsonb,updated_at=now() WHERE key=$1`, key, description, string(updated)); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`INSERT INTO settings(key,value) VALUES ($1,'true') ON CONFLICT(key) DO UPDATE SET value='true'`, explorationRestorationDefaultsFlag); err != nil {
		return err
	}
	return tx.Commit()
}
