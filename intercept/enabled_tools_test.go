package intercept

import "testing"

func TestMCPInterceptionRespectsExplicitScope(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tools map[string]bool
		tool  string
		want  bool
	}{
		{"mcp group", map[string]bool{"mcp__*": true}, "mcp__browser__browser_navigate", true},
		{"http mcp group", map[string]bool{"mcp__*": true}, "mcp__remote__action", true},
		{"not a general wildcard", map[string]bool{"mcp__*": true}, "Bash", false},
		{"explicit off", map[string]bool{}, "mcp__browser__browser_navigate", false},
		{"existing saved scope", map[string]bool{"Bash": true}, "mcp__browser__browser_navigate", false},
		{"one MCP tool", map[string]bool{"mcp__browser__browser_navigate": true}, "mcp__browser__browser_navigate", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := &Interceptor{enabledTools: tc.tools}
			if got := i.IsToolEnabled(tc.tool); got != tc.want {
				t.Fatalf("enabled=%v want %v", got, tc.want)
			}
		})
	}
}
