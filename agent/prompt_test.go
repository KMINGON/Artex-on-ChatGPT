package agent

import (
	"strings"
	"testing"
)

func TestRenderSystemOverrideAndFallback(t *testing.T) {
	t.Cleanup(func() { PromptOverride = nil })

	// no override → built-in default
	PromptOverride = nil
	if got := renderSystem("planner", "DEFAULT", PlannerVars{Goal: "g"}); got != "DEFAULT" {
		t.Fatalf("no override should give default, got %q", got)
	}

	// override → rendered with vars
	PromptOverride = func(k string) (string, bool) {
		if k == "planner" {
			return "目标:{{.Goal}} 范围:{{.Scope}}", true
		}
		return "", false
	}
	if got := renderSystem("planner", "DEFAULT", PlannerVars{Goal: "拿下X", Scope: "*.x.com"}); got != "目标:拿下X 范围:*.x.com" {
		t.Fatalf("override render: %q", got)
	}

	// override referencing a non-catalog var → execution error → fallback to default
	PromptOverride = func(k string) (string, bool) { return "{{.NotInCatalog}}", true }
	if got := renderSystem("planner", "DEFAULT", PlannerVars{Goal: "x"}); got != "DEFAULT" {
		t.Fatalf("bad var should fall back to default, got %q", got)
	}

	// full plannerSystem path: DB body [A] is honored, then the code-owned tail
	// [C] (중간 산출물 저장 규칙) is ALWAYS appended — editing the body can't drop it.
	PromptOverride = func(k string) (string, bool) { return "PLANNER {{.Goal}}", true }
	got := plannerSystem("拿下X", "/data", "/data")
	if !strings.HasPrefix(got, "PLANNER 拿下X") {
		t.Fatalf("plannerSystem body not honored: %q", got)
	}
	if !strings.Contains(got, "중간 산출물 저장 규칙") || !strings.Contains(got, "/data") {
		t.Fatalf("plannerSystem missing code-owned artifact tail: %q", got)
	}

	// worker dual-text via {{if .ProxyAddr}} in a user template, plus the code tail:
	// [B] trafficTool present only when RECORDING (caCert set — the MITM is on, so
	// the traffic_* tools exist), [C] artifact spec always present. The trafficTool
	// block is gated on the CA (arg 2), NOT on ProxyAddr — a global egress proxy
	// with capture off routes traffic but records nothing.
	PromptOverride = func(k string) (string, bool) {
		return "{{if .ProxyAddr}}走代理 {{.ProxyAddr}}{{else}}手动{{end}}", true
	}
	recording := workerSystem("127.0.0.1:8080", "/ca.pem", "/data", "/data", "/data")
	if !strings.HasPrefix(recording, "走代理 127.0.0.1:8080") {
		t.Fatalf("worker proxy branch body: %q", recording)
	}
	if !strings.Contains(recording, "traffic_search") {
		t.Fatalf("worker while recording should inject trafficTool: %q", recording)
	}
	if strings.Contains(recording, "traffic_refs") {
		t.Fatalf("worker bypassed shared optional evidence policy: %q", recording)
	}
	if !strings.Contains(recording, "중간 산출물 저장 규칙") {
		t.Fatalf("worker missing artifact tail: %q", recording)
	}
	// Egress proxy set but capture OFF (no CA): the ProxyAddr template branch still
	// renders, but the trafficTool block must NOT — those tools are not registered.
	egressOnly := workerSystem("127.0.0.1:8080", "", "/data", "/data", "/data")
	if !strings.HasPrefix(egressOnly, "走代理 127.0.0.1:8080") {
		t.Fatalf("worker egress-only branch body: %q", egressOnly)
	}
	if strings.Contains(egressOnly, "traffic_search") {
		t.Fatalf("worker without recording must NOT inject trafficTool: %q", egressOnly)
	}
	noProxy := workerSystem("", "", "/data", "/data", "/data")
	if !strings.HasPrefix(noProxy, "手动") {
		t.Fatalf("worker no-proxy branch body: %q", noProxy)
	}
	if strings.Contains(noProxy, "traffic_search") {
		t.Fatalf("worker without proxy must NOT inject trafficTool: %q", noProxy)
	}
}

func TestPlannerGraphContractCorrectsLegacyPromptWithoutReplacingUserBody(t *testing.T) {
	prior := PromptOverride
	t.Cleanup(func() { PromptOverride = prior })
	PromptOverride = nil
	defaults := plannerSystem("사용자 지정 목표", "/data", "/data/tasks/7")
	judgment := strings.Index(defaults, "2. **목표 판단")
	directions := strings.Index(defaults, "4. **보완할 새 방향 결정")
	if judgment < 0 || directions < 0 || judgment > directions {
		t.Fatal("Planner no longer judges goals before choosing new directions")
	}
	for _, stale := range []string{"탐색 노드(goals/의도/facts/findings)는 이 작업만 포함합니다", "어떤 사실/의도에서 파생", "커버리지 100% 요구에 실제 coverage.pct=40%", "coverage는 하한/완료 조건"} {
		if strings.Contains(defaults, stale) {
			t.Fatalf("default retains incompatible graph guidance: %s", stale)
		}
	}
	const edited = "사용자가 편집한 범위는 유지한다. 과거 지침: coverage 100%면 완료, 부모에 intent ID 사용."
	PromptOverride = func(string) (string, bool) { return edited, true }
	got := plannerSystem("사용자 지정 목표", "/data", "/data/tasks/7")
	if !strings.HasPrefix(got, edited) {
		t.Fatal("contract update replaced a user-edited prompt")
	}
	for _, contract := range []string{"parent_ids와 parents는 근거 fact/finding ID", "source_task_id/inherited=true", "읽기 전용 근거", "자산 관찰 비율", "값이 없으면 미측정"} {
		if strings.Index(got, contract) < len(edited) {
			t.Fatalf("code-owned contract missing after user body: %s", contract)
		}
	}
}

func TestUserPromptDoesNotReceiveAdditionalExplorationProcedure(t *testing.T) {
	prior := PromptOverride
	t.Cleanup(func() { PromptOverride = prior })
	PromptOverride = func(string) (string, bool) { return "USER WORKER BODY", true }
	got := workerSystem("", "", "/data", "/data/tasks/7", "/data/tasks/7/i8")
	if !strings.HasPrefix(got, "USER WORKER BODY") {
		t.Fatal("worker override lost")
	}
	for _, prompt := range []string{got, plannerSystem("fixture", "/data", "/data/tasks/7")} {
		for _, removed := range []string{"결과 인계", "브라우저 응답 관찰", "기존 후속 단서 호환", "finding 등록은 의도의 종료 조건이 아닙니다", "원래 질문·근거 ID·재개 조건"} {
			if strings.Contains(prompt, removed) {
				t.Fatalf("custom prompt received removed exploration procedure: %s", removed)
			}
		}
	}
}

// langMarker identifies the selected display language in the Korean directive.
var langMarker = map[string]string{
	"ko": "한국어",
	"en": "English",
}

// TestLangDirectiveLocalized pins the output-language tail across all
// supported display languages: each language's directive names that language, preserves
// raw technical strings, forbids mirroring the target/material language, and names
// the planner situation summary. Technical evidence retains its original language.
func TestLangDirectiveLocalized(t *testing.T) {
	for _, code := range SupportedOutputLangs {
		dir := langDirective(code)
		if !strings.Contains(dir, langMarker[code]) {
			t.Fatalf("%s: langDirective must name the display language (%q), got %q", code, langMarker[code], dir)
		}
		if !strings.Contains(dir, "payload") || !strings.Contains(dir, "글자 그대로 보존") {
			t.Fatalf("%s: langDirective must keep commands/payloads verbatim, got %q", code, dir)
		}
		if !strings.Contains(dir, "대상이나 자료의 언어를 따라 쓰거나 그대로 옮기지 마세요") {
			t.Fatalf("%s: langDirective must forbid mirroring the target/material language, got %q", code, dir)
		}
		if !strings.Contains(dir, "현황") {
			t.Fatalf("%s: langDirective must name the planner situation summary as user-facing, got %q", code, dir)
		}
		hasNoZhClause := strings.Contains(dir, "중국어를 사용자에게 출력해서는 안 됩니다")
		if !hasNoZhClause {
			t.Fatalf("%s: langDirective must forbid leaking Chinese to the user, got %q", code, dir)
		}
	}
}

// TestLangDirectiveAppendedToUserFacingRoles pins the localization tail wiring:
// every user-facing role's system prompt must end with the code-owned
// output-language directive in the configured language, and a DB-edited body must
// NOT be able to drop it. Exercised for each supported display language by driving
// the OutputLanguage hook the roles resolve through.
func TestLangDirectiveAppendedToUserFacingRoles(t *testing.T) {
	t.Cleanup(func() { PromptOverride = nil; OutputLanguage = nil })

	// Even with a DB body that is pure non-directive text, the code-owned tail is
	// still appended for each user-facing builder — identical guarantee to the
	// artifact tail. A custom body can never translate away the output-language mandate.
	PromptOverride = func(string) (string, bool) { return "BODY-ONLY", true }

	for _, code := range SupportedOutputLangs {
		OutputLanguage = func() string { return code }
		marker := langMarker[code]
		cases := map[string]string{
			"worker":    workerSystem("", "", "/data", "/data", "/data"),
			"planner":   plannerSystem("g", "/data", "/data"),
			"mainagent": mainAgentSystem("g", "/data", "/data"),
			"chat":      chatSystem("chat", "/data", "/data"),
			// goals is user-facing too: set_goals/set_constraints persist goal and
			// constraint nodes shown in the UI graph/plan tab. withScope=true exercises
			// the longer assembly (body + scope tail), so the language tail must still
			// land last — after both the body and the code-owned scope tail.
			"goals": goalsSystem("/data", true),
		}
		for role, sys := range cases {
			if !strings.HasPrefix(sys, "BODY-ONLY") {
				t.Fatalf("%s/%s: DB body not honored: %q", code, role, sys)
			}
			if !strings.Contains(sys, marker) {
				t.Fatalf("%s/%s: missing output-language tail (%q): %q", code, role, marker, sys)
			}
			// The directive is the tail — it must come AFTER the body (recency).
			if strings.Index(sys, marker) <= strings.Index(sys, "BODY-ONLY") {
				t.Fatalf("%s/%s: langDirective must be appended after the body: %q", code, role, sys)
			}
		}
	}
}
