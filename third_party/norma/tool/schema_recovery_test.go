package tool

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRequiredSchemaFormsAndRecovery(t *testing.T) {
	for _, required := range []any{[]string{"url", "static"}, []any{"url", "static"}} {
		schema := map[string]any{"type": "object", "required": required, "properties": map[string]any{
			"url":    map[string]any{"type": "string", "default": "private-credential"},
			"static": map[string]any{"type": "boolean"},
		}}
		err := ValidateInput(schema, json.RawMessage(`{}`))
		if err == nil {
			t.Fatal("required fields ignored")
		}
		message := InputError("browser_network_requests", schema, err)
		if !strings.Contains(message, "static:boolean") || !strings.Contains(message, "url:string") || strings.Contains(message, "private-credential") {
			t.Fatal(message)
		}
		if err := ValidateInput(schema, json.RawMessage(`{"url":"http://fixture/","static":false}`)); err != nil {
			t.Fatal(err)
		}
	}
}
