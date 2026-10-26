package tool

import (
	"encoding/json"
	"testing"
)

func TestValidateNullableInteger(t *testing.T) {
	for _, types := range []any{[]string{"integer", "null"}, []any{"integer", "null"}} {
		schema := obj(map[string]any{"hint_id": map[string]any{"type": types}})
		for _, body := range []string{`{}`, `{"hint_id":null}`, `{"hint_id":0}`, `{"hint_id":42}`} {
			if err := ValidateInput(schema, json.RawMessage(body)); err != nil {
				t.Errorf("valid optional input %s: %v", body, err)
			}
		}
		for _, body := range []string{`{"hint_id":"42"}`, `{"hint_id":true}`, `{"hint_id":[]}`, `{"hint_id":{}}`, `{"hint_id":1.5}`} {
			if err := ValidateInput(schema, json.RawMessage(body)); err == nil {
				t.Errorf("invalid optional input accepted: %s", body)
			}
		}
	}
}

func TestValidateNullableObjectKeepsRequiredChildren(t *testing.T) {
	schema := obj(map[string]any{"options": map[string]any{
		"type": []string{"object", "null"}, "properties": map[string]any{"id": map[string]any{"type": "integer"}}, "required": []string{"id"},
	}}, "options")
	for _, body := range []string{`{"options":null}`, `{"options":{"id":1}}`} {
		if err := ValidateInput(schema, json.RawMessage(body)); err != nil {
			t.Errorf("valid input %s: %v", body, err)
		}
	}
	for _, body := range []string{`{}`, `{"options":{}}`, `{"options":{"id":null}}`, `{"options":{"id":"1"}}`} {
		if err := ValidateInput(schema, json.RawMessage(body)); err == nil {
			t.Errorf("invalid input accepted: %s", body)
		}
	}
}
