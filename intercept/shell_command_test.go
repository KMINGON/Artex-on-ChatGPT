package intercept

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestShellCommandRuleUsesExecutableToolArgument(t *testing.T) {
	rule := compiledRule{InterceptRule: db.InterceptRule{
		MatchTarget: "shell_command", MatchType: "regex", Pattern: `\b(?:shutdown|reboot|halt|poweroff|init\s+[06])\b`,
	}}
	rule.re = regexp.MustCompile(rule.Pattern)
	for _, tc := range []struct {
		name, tool string
		args       any
		want       bool
	}{
		{"shell halt", "Bash", map[string]string{"command": "halt"}, true},
		{"shell sudo", "Bash", map[string]string{"command": "sudo /sbin/reboot"}, true},
		{"shell quoted executable", "Bash", map[string]string{"command": `sh -c 'halt'`}, true},
		{"interactive start", "shell_open", map[string]string{"command": "shutdown now"}, true},
		{"interactive send", "shell_send", map[string]string{"text": "poweroff\n"}, true},
		{"metadata is not command", "Bash", map[string]string{"command": "printf ok", "description": "halt 진단용 문자열 출력"}, false},
		{"worker script regression", "Write", map[string]string{"file_path": "probe.py", "content": `print('halt', type(exc).__name__)`}, false},
		{"edit script", "Edit", map[string]string{"file_path": "probe.py", "new_string": "halt"}, false},
		{"multiple edits", "MultiEdit", map[string]any{"edits": []map[string]string{{"new_string": "shutdown"}}}, false},
		{"document write", "Write", map[string]string{"file_path": "notes.md", "content": "reboot 명령은 실행하지 않았다"}, false},
		{"other tool", "mcp__browser__browser_type", map[string]string{"text": "halt"}, false},
		{"missing command", "Bash", map[string]string{"text": "halt"}, false},
		{"wrong argument type", "Bash", map[string]any{"command": []string{"halt"}}, false},
		{"null command", "Bash", map[string]any{"command": nil}, false},
		// This target deliberately preserves the regex's command-text semantics;
		// it does not claim to parse quoted strings or shell execution syntax.
		{"known quoted text limitation", "Bash", map[string]string{"command": `printf '%s\n' halt`}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, err := json.Marshal(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if got := ruleMatches(rule, tc.tool, input); got != tc.want {
				t.Errorf("ruleMatches(%s) = %v, want %v", tc.tool, got, tc.want)
			}
		})
	}
	if ruleMatches(rule, "Bash", []byte(`{"command":"halt"`)) {
		t.Fatal("invalid arguments must not become a command")
	}
}

func TestCustomFullInputRuleKeepsOriginalScope(t *testing.T) {
	input := []byte(`{"file_path":"probe.py","content":"print('halt')"}`)
	rule := compiledRule{InterceptRule: db.InterceptRule{MatchTarget: "tool_input", MatchType: "string", Pattern: "halt"}}
	if !ruleMatches(rule, "Write", input) {
		t.Fatal("a user full-input rule must still inspect Write contents")
	}
	rule.MatchTarget = "tool_name"
	rule.Pattern = "Write"
	if !ruleMatches(rule, "Write", input) {
		t.Fatal("tool-name rules must keep their existing semantics")
	}
}
