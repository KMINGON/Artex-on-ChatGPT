package db

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestBuiltinContractUpgradePreservesUserEdits(t *testing.T) {
	d, _ := templateFixtureDB(t)
	old := json.RawMessage(`{"type":"object","properties":{"summary":{"type":"string","description":"사용자 설명","default":"사용자 값"},"facts":{"type":"array","items":{"type":"object"}}}}`)
	if err := d.SeedTool("fixture_contract", "사용자 도구 설명", old, json.RawMessage(`["custom-agent"]`)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE tools SET enabled=false WHERE key='fixture_contract'`); err != nil {
		t.Fatal(err)
	}
	var next map[string]any
	if err := json.Unmarshal([]byte(`{"type":"object","properties":{"summary":{"type":"string","description":"코드 설명"},"facts":{"type":"array","items":{"type":"object","properties":{"follow_ups":{"type":"array","items":{"type":"string"}}},"required":["summary"]}},"follow_ups":{"type":"array","items":{"type":"string"}}}}`), &next); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := d.SyncBuiltinToolSchema("fixture_contract", next); err != nil {
			t.Fatal(err)
		}
	}
	row, err := d.GetTool("fixture_contract")
	if err != nil {
		t.Fatal(err)
	}
	if row.Enabled || row.Description != "사용자 도구 설명" || !reflect.DeepEqual(row.Agents, []string{"custom-agent"}) {
		t.Fatal("overwrote tool settings")
	}
	var schema map[string]any
	if err := json.Unmarshal(row.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	props := schema["properties"].(map[string]any)
	summary := props["summary"].(map[string]any)
	if summary["description"] != "사용자 설명" || summary["default"] != "사용자 값" || props["follow_ups"] == nil {
		t.Fatal(schema)
	}
	items := props["facts"].(map[string]any)["items"].(map[string]any)
	if items["properties"] == nil || items["required"] == nil {
		t.Fatal("batch schema not upgraded")
	}
}
