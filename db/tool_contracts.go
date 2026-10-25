package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
)

// SyncBuiltinToolSchema updates the code-owned parameter contract, preserving
// descriptions/defaults editable in the UI and all tool-level user settings.
func (d *DB) SyncBuiltinToolSchema(key string, current map[string]any) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw []byte
	if err := tx.QueryRow(`SELECT schema FROM tools WHERE key=$1 AND system AND kind='builtin' FOR UPDATE`, key).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		return err
	}
	merged := preserveSchemaAnnotations(current, stored)
	encoded, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	var normalized map[string]any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return err
	}
	if !reflect.DeepEqual(stored, normalized) {
		if _, err := tx.Exec(`UPDATE tools SET schema=$2,updated_at=now() WHERE key=$1`, key, string(encoded)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func preserveSchemaAnnotations(current, stored map[string]any) map[string]any {
	out := make(map[string]any, len(current))
	for key, value := range current {
		out[key] = value
	}
	for _, key := range []string{"description", "default"} {
		if value, ok := stored[key]; ok {
			out[key] = value
		}
	}
	if props, ok := current["properties"].(map[string]any); ok {
		oldProps, _ := stored["properties"].(map[string]any)
		fields := make(map[string]any, len(props))
		for key, value := range props {
			field, ok := value.(map[string]any)
			if !ok {
				fields[key] = value
				continue
			}
			oldField, _ := oldProps[key].(map[string]any)
			fields[key] = preserveSchemaAnnotations(field, oldField)
		}
		out["properties"] = fields
	}
	if item, ok := current["items"].(map[string]any); ok {
		oldItem, _ := stored["items"].(map[string]any)
		out["items"] = preserveSchemaAnnotations(item, oldItem)
	}
	return out
}
