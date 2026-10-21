package chatgptllm

import "strings"

// ModelCapabilities is a small documented capability snapshot, not account
// entitlement. Unknown models remain selectable and are validated by OpenAI.
type ModelCapabilities struct {
	Known               bool     `json:"known"`
	ReasoningEfforts    []string `json:"reasoning_efforts"`
	AccessProgramsCyber []string `json:"access_programs_cyber"`
	DaybreakAvailable   bool     `json:"daybreak_available"`
	SourceURL           string   `json:"source_url"`
	CheckedAt           string   `json:"checked_at"`
}

const capabilityCheckedAt = "2026-10-21"

// Access-program options: https://developers.openai.com/api/docs/guides/daybreak
// SIWC contract: https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference

func authMode(key string) string {
	if key != "" {
		return "api_key"
	}
	return "chatgpt_plan"
}

// Capabilities describes explicit options this application can send. SIWC's
// published contract does not establish access_programs support: catalog model
// IDs (including any Daybreak model returned by that account) still pass through.
func Capabilities(model, authMode string) ModelCapabilities {
	c := ModelCapabilities{CheckedAt: capabilityCheckedAt, SourceURL: "https://developers.openai.com/api/docs/guides/latest-model", ReasoningEfforts: []string{}, AccessProgramsCyber: []string{}}
	if authMode != "api_key" {
		c.SourceURL = "https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference"
		return c // Account catalog capabilities take precedence over API model docs.
	}
	name := strings.ToLower(strings.TrimSpace(model))
	for _, prefix := range []string{"gpt-6.1-sol", "gpt-6-astra", "gpt-6-sol", "gpt-6-luna"} {
		if name == prefix || strings.HasPrefix(name, prefix+"-") {
			c.Known = true
			c.ReasoningEfforts = []string{"low", "medium", "high", "xhigh", "max"}
			if prefix == "gpt-6-sol" || prefix == "gpt-6-luna" {
				c.ReasoningEfforts = append([]string{"none"}, c.ReasoningEfforts...)
			}
			break
		}
	}
	if authMode == "api_key" {
		c.DaybreakAvailable = true
		c.AccessProgramsCyber = []string{"standard", "daybreak_blue", "daybreak_red"}
		if c.Known && !strings.HasPrefix(name, "gpt-6-luna") {
			c.AccessProgramsCyber = []string{"standard", "daybreak_blue"}
		}
	}
	return c
}

// ValidateOptions checks only documented local constraints. Empty values keep
// OpenAI defaults; eligibility and unknown model capabilities remain remote.
func ValidateOptions(model, authMode, effort, cyber string) error {
	fail := func(code, msg string) error {
		e := failure(code, msg, nil)
		e.APIKeyMode = authMode == "api_key"
		return e
	}
	c := Capabilities(model, authMode)
	if cyber != "" {
		if authMode != "api_key" {
			return fail("unverified_access_program", "ChatGPT 구독의 Daybreak 명시 선택은 ARTEX에서 검증되지 않았습니다. 계정 모델 목록에서 제공되는 모델을 선택해 주세요")
		}
		if !contains(c.AccessProgramsCyber, cyber) {
			return fail("invalid_access_program", "이 모델에서 선택할 수 없는 OpenAI 접근 프로그램입니다")
		}
	}
	if effort != "" {
		if c.Known && !contains(c.ReasoningEfforts, effort) {
			return fail("invalid_reasoning_effort", "이 모델에서 지원하지 않는 추론 강도입니다")
		}
	}
	return nil
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
