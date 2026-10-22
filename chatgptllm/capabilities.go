package chatgptllm

import (
	"strings"
	"time"
)

// ModelCapabilities distinguishes account catalog values from documented model
// compatibility. Documentation never establishes the selected account's access.
type ModelCapabilities struct {
	Known                  bool     `json:"known"`
	ReasoningEfforts       []string `json:"reasoning_efforts"`
	ReasoningEffortsKnown  bool     `json:"reasoning_efforts_known"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty"`
	AccessProgramsCyber    []string `json:"access_programs_cyber"`
	AccessProgramsKnown    bool     `json:"access_programs_known"`
	DaybreakAvailable      *bool    `json:"daybreak_available"`
	Source                 string   `json:"source"`
	SourceURL              string   `json:"source_url"`
	CheckedAt              string   `json:"checked_at"`
}

const capabilityCheckedAt = "2026-10-22"

// Access-program options: https://developers.openai.com/api/docs/guides/daybreak
// SIWC contract: https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference

func authMode(key string) string {
	if key != "" {
		return "api_key"
	}
	return "chatgpt_plan"
}

// Capabilities is a fallback for API catalogs without capability metadata.
// Subscription capabilities must come from that account's catalog, not API docs.
func Capabilities(model, authMode string) ModelCapabilities {
	c := ModelCapabilities{Source: "unknown", SourceURL: "https://developers.openai.com/api/docs/guides/latest-model", ReasoningEfforts: []string{}, AccessProgramsCyber: []string{}}
	if authMode != "api_key" {
		c.SourceURL = "https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference"
		return c // Account catalog capabilities take precedence over API model docs.
	}
	name := strings.ToLower(strings.TrimSpace(model))
	for _, prefix := range []string{"gpt-6.1-sol", "gpt-6-astra", "gpt-6-sol", "gpt-6-luna"} {
		if documentedModel(name, prefix) {
			c.ReasoningEffortsKnown = true
			c.ReasoningEfforts = []string{"low", "medium", "high", "xhigh", "max"}
			if prefix == "gpt-6-sol" || prefix == "gpt-6-luna" {
				c.ReasoningEfforts = append([]string{"none"}, c.ReasoningEfforts...)
			}
			break
		}
	}
	for _, prefix := range []string{"gpt-6.1-sol", "gpt-6-astra", "gpt-6-sol", "gpt-5.6-cyber"} {
		if documentedModel(name, prefix) {
			c.AccessProgramsCyber = []string{"standard", "daybreak_blue"}
			if prefix == "gpt-5.6-cyber" {
				c.AccessProgramsCyber = []string{"daybreak_red"}
			}
			c.AccessProgramsKnown = true
			available := true
			c.DaybreakAvailable = &available
			break
		}
	}
	c.Known = c.ReasoningEffortsKnown || c.AccessProgramsKnown
	if c.Known {
		c.Source, c.CheckedAt = "documentation", capabilityCheckedAt
	}
	return c
}

func documentedModel(name, family string) bool {
	if name == family {
		return true
	}
	snapshot, ok := strings.CutPrefix(name, family+"-")
	if !ok {
		return false
	}
	_, err := time.Parse(time.DateOnly, snapshot)
	return err == nil
}

// ValidateOptions checks the program enum and the verified subscription modes.
// Model-specific documentation is a UI fallback, never an execution gate: an
// account catalog can advertise newer options. OpenAI validates those combinations.
func ValidateOptions(model, authMode, effort, cyber string) error {
	fail := func(code, msg string) error {
		e := failure(code, msg, nil)
		e.APIKeyMode = authMode == "api_key"
		return e
	}
	if cyber != "" {
		if !contains([]string{"standard", "daybreak_blue", "daybreak_red"}, cyber) {
			return fail("invalid_access_program", "지원하지 않는 OpenAI 접근 프로그램입니다")
		}
		if authMode != "api_key" && cyber != "standard" {
			return fail("unverified_access_program", "ChatGPT 구독의 Daybreak 명시 요청은 실제 적용값과 일치하지 않을 수 있습니다. 자동 선택 또는 Standard를 사용해 주세요")
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
