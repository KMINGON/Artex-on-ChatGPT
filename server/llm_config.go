package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"

	"github.com/Autumn-27/artex/db"
)

// Reject retired connection knobs before any saved key is read or request sent.
func decodeOpenAISettings(r *http.Request, dst any) error {
	var fields map[string]json.RawMessage
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(&fields); err != nil {
		return err
	}
	if fields == nil {
		return fmt.Errorf("설정 객체가 필요합니다")
	}
	var mode string
	if raw, ok := fields["auth_mode"]; ok {
		if json.Unmarshal(raw, &mode) != nil || (mode != "" && mode != "api_key" && mode != "chatgpt_plan" && !db.RestoredProvider(mode)) {
			return fmt.Errorf("지원하지 않는 인증 방식입니다")
		}
	}
	for _, key := range []string{"format", "base_url", "thinking_type", "streaming", "max_tokens_field", "session_header_key", "stream", "intent"} {
		if key == "base_url" && db.RestoredProvider(mode) {
			continue // a restored provider names its own address
		}
		if _, ok := fields[key]; ok {
			return fmt.Errorf("%s 설정은 지원하지 않습니다. OpenAI API 또는 ChatGPT 연결을 다시 설정해 주세요", key)
		}
	}
	if raw, ok := fields["retry"]; ok {
		var retry map[string]json.RawMessage
		if err := json.Unmarshal(raw, &retry); err != nil {
			return fmt.Errorf("재시도 설정 객체가 필요합니다")
		}
		for _, key := range []string{"empty", "stream"} {
			if _, exists := retry[key]; exists {
				return fmt.Errorf("retry.%s 설정은 지원하지 않습니다", key)
			}
		}
	}
	if raw, ok := fields["provider"]; ok {
		var provider string
		if json.Unmarshal(raw, &provider) != nil || (provider != "" && provider != "openai" && provider != "openai-responses") {
			return fmt.Errorf("OpenAI 제공자만 지원합니다")
		}
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("하나의 설정 객체만 허용됩니다")
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

// Reasoning efforts each restored provider accepts. Anthropic rejects any other
// value with a 400; OpenAI-compatible servers take the OpenAI names.
var providerEfforts = map[string][]string{
	db.AuthAnthropic:        {"low", "medium", "high", "xhigh", "max"},
	db.AuthOpenAICompatible: {"none", "minimal", "low", "medium", "high", "xhigh", "max"},
}

// validateProviderOptions checks a restored provider's options before a key is
// read or a request sent. The OpenAI access program does not apply to them.
func validateProviderOptions(mode, effort, program string) error {
	if program != "" {
		return fmt.Errorf("액세스 프로그램은 OpenAI 연결에서만 지정할 수 있습니다")
	}
	if effort != "" && !slices.Contains(providerEfforts[mode], effort) {
		return fmt.Errorf("이 연결에서 지원하지 않는 추론 강도입니다: %s", effort)
	}
	return nil
}

// sameProviderConnection reports whether a saved row may lend its key to a
// request: same connection kind and same address, and the row is usable.
func sameProviderConnection(saved *db.LLMProfile, mode, rawBaseURL string) bool {
	if saved == nil || saved.AuthMode != mode || saved.UnsupportedReason() != "" {
		return false
	}
	base, err := db.NormalizeProviderBaseURL(mode, rawBaseURL)
	return err == nil && base == saved.BaseURL
}

// savedAPIKey only reuses credentials from a supported API connection. An old
// provider's key is never silently adopted by the fixed OpenAI endpoint.
func (s *Server) savedAPIKey() (string, error) {
	if s.m != nil && s.m.pg != nil {
		p, err := s.m.pg.ActiveProfile()
		if err != nil {
			return "", fmt.Errorf("저장된 모델 연결을 확인할 수 없습니다")
		}
		if p != nil {
			if reason := p.UnsupportedReason(); reason != "" {
				return "", fmt.Errorf("%s", reason)
			}
			if p.AuthMode != "" && p.AuthMode != "api_key" {
				return "", fmt.Errorf("OpenAI API 키를 직접 입력해 주세요")
			}
			return p.APIKey, nil
		}
	}
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	return s.llmCfg.APIKeyForReuse()
}
