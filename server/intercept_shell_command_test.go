package server

import "testing"

func TestInterceptRuleAcceptsShellCommandTarget(t *testing.T) {
	req := interceptRuleReq{Name: "셸 명령 검사", MatchTarget: "shell_command", MatchType: "regex", Pattern: `\bhalt\b`, Action: "deny"}
	if err := validateInterceptRuleReq(req); err != nil {
		t.Fatal(err)
	}
	req.MatchTarget = "arbitrary_command"
	if err := validateInterceptRuleReq(req); err == nil {
		t.Fatal("unknown match target accepted")
	}
}
