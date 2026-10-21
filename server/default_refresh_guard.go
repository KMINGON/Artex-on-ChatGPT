package server

import (
	"bytes"
	"encoding/json"

	"github.com/Autumn-27/artex/db"
)

// Legacy startup refreshes must not overwrite edits before the field-level
// Korean migration runs. Unknown older defaults are preserved as well.
func isLegacyPromptDefault(key, text string) bool {
	var baseline koreanDefaultBaseline
	if json.Unmarshal(koreanDefaultsV1, &baseline) != nil {
		return false
	}
	hash, ok := baseline.Prompts[key]
	return ok && defaultTextHash(text) == hash
}

func isLegacyToolDefault(current *db.Tool) bool {
	if current == nil || !current.System || current.Kind != "builtin" {
		return false
	}
	var baseline koreanDefaultBaseline
	if json.Unmarshal(koreanDefaultsV1, &baseline) != nil {
		return false
	}
	old, ok := baseline.Tools[current.Key]
	if !ok || defaultTextHash(current.Description) != old.Description {
		return false
	}
	var schema map[string]any
	decoder := json.NewDecoder(bytes.NewReader(current.Schema))
	decoder.UseNumber()
	if decoder.Decode(&schema) != nil {
		return false
	}
	canonical, err := json.Marshal(schema)
	return err == nil && defaultTextHash(string(canonical)) == old.SchemaHash
}

func (s *Server) refreshUneditedToolDefault(key, description string, schema json.RawMessage) error {
	current, err := s.m.pg.GetTool(key)
	if err != nil || !isLegacyToolDefault(current) {
		return err
	}
	// Compare again in the UPDATE so another instance's concurrent edit survives.
	_, err = s.m.pg.Exec(`UPDATE tools SET description=$2,schema=$3::jsonb,updated_at=now()
		WHERE key=$1 AND system AND kind='builtin' AND description=$4 AND schema=$5::jsonb`,
		key, description, string(schema), current.Description, string(current.Schema))
	return err
}
