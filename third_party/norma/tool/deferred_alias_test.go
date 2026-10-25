package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDeferredParameterAliasPreservesValuesAndRejectsConflict(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		calls             int
	}{
		{"canonical", `{"params":{"function":"() => 1"}}`, "ok", 1},
		{"alias", `{"parameters":{"function":"() => 1"}}`, "ok", 1},
		{"equal objects", `{"params":{"function":"() => 1","n":7},"parameters":{"n":7,"function":"() => 1"}}`, "ok", 1},
		{"conflicting", `{"params":{"function":"first-secret"},"parameters":{"function":"second-secret"}}`, "서로 다릅니다", 0},
		{"null conflict", `{"params":null,"parameters":{"function":"() => 1"}}`, "서로 다릅니다", 0},
		{"large integers", `{"params":{"function":"f","n":9007199254740992},"parameters":{"function":"f","n":9007199254740993}}`, "서로 다릅니다", 0},
		{"wrong inner key", `{"parameters":{"code":"() => 1"}}`, "function:string", 0},
		{"wrong inner type", `{"parameters":{"function":false}}`, "function:string", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			inner := Build(Spec{Name: "browser_evaluate", Schema: map[string]any{"type": "object", "properties": map[string]any{"function": map[string]any{"type": "string"}}, "required": []any{"function"}}, Run: func(_ context.Context, raw json.RawMessage, _ *ToolContext) (Result, error) {
				calls++
				var args map[string]any
				_ = json.Unmarshal(raw, &args)
				if args["function"] != "() => 1" {
					t.Fatalf("argument lost: %v", args)
				}
				return Text("ok"), nil
			}})
			invoke := NewExecuteExtraTool(NewRegistry(inner), NewUnlockSet("browser_evaluate"))
			input := `{"tool_name":"browser_evaluate",` + tc.input[1:]
			result, err := invoke.Call(context.Background(), json.RawMessage(input), nil)
			if err != nil || calls != tc.calls || !strings.Contains(result.Content[0].Text, tc.want) {
				t.Fatalf("calls=%d result=%+v err=%v", calls, result, err)
			}
			if strings.Contains(result.Content[0].Text, "first-secret") || strings.Contains(result.Content[0].Text, "second-secret") {
				t.Fatal("conflict leaked arguments")
			}
		})
	}
}
