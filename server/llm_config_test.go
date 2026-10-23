package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestProfileDTOIncludesObservedConnectionWithoutChangingRequest(t *testing.T) {
	p := &db.LLMProfile{Model: "requested-model", ConnectionCheck: &db.LLMConnectionCheck{
		Model: "resolved-model", AccessProgramCyber: "daybreak_blue", CheckedAt: time.Now().UTC(),
	}}
	dto := llmProfileDTO(p)
	if dto.ConnectionCheck == nil || *dto.ConnectionCheck != *p.ConnectionCheck || dto.Model != p.Model || dto.AccessProgramCyber != "" {
		t.Fatalf("connection display metadata lost or applied to request: %+v", dto)
	}
}

func TestSavedAPIKeyRejectsUnsupportedEnvironmentConnection(t *testing.T) {
	for _, cfg := range []agent.Config{
		agent.ConfigFrom("anthropic", "legacy-model", "", "private-fixture", ""),
		agent.ConfigFrom("openai", "legacy-model", "https://vendor.invalid/v1", "private-fixture", ""),
		{AuthMode: "chatgpt_plan", APIKey: "private-fixture"},
		{AuthMode: "unknown", APIKey: "private-fixture"},
	} {
		s := &Server{llmCfg: cfg}
		if key, err := s.savedAPIKey(); err == nil || key != "" {
			t.Fatal("unsupported environment credential could be silently adopted")
		}
	}
	s := &Server{llmCfg: agent.ConfigFrom("openai", "existing-model", "", "api-fixture", "")}
	if key, err := s.savedAPIKey(); err != nil || key != "api-fixture" {
		t.Fatalf("valid environment API key lost: %v", err)
	}
}

func TestOpenAISettingsRejectRetiredFieldsBeforeCredentials(t *testing.T) {
	for _, body := range []string{`{"format":"anthropic"}`, `{"base_url":"https://vendor.example"}`, `{"thinking_type":"enabled"}`, `{"streaming":false}`, `{"max_tokens_field":"max_tokens"}`, `{"session_header_key":"x-custom"}`, `{"provider":"anthropic"}`, `{"auth_mode":"other"}`, `{"retry":{"stream":{"attempts":3}}}`, `{"retry":{"empty":{"attempts":3}}}`, `{"stream":{"attempts":3}}`, `{"intent":{"attempts":3}}`, `{"model":"gpt-existing"} {}`} {
		r := httptest.NewRequest("POST", "/llm/models", strings.NewReader(body))
		var got map[string]any
		if err := decodeOpenAISettings(r, &got); err == nil {
			t.Fatalf("retired payload accepted: %s", body)
		}
	}
	var got struct {
		Model    string `json:"model"`
		AuthMode string `json:"auth_mode"`
	}
	r := httptest.NewRequest("POST", "/llm/profiles", strings.NewReader(`{"model":"gpt-existing","auth_mode":"api_key"}`))
	if err := decodeOpenAISettings(r, &got); err != nil || got.Model != "gpt-existing" {
		t.Fatalf("supported model changed/rejected: %+v %v", got, err)
	}
}

func TestLegacyProfileDTORequiresReselectionAndHidesOptions(t *testing.T) {
	dto := llmProfileDTO(&db.LLMProfile{ID: 18, Name: "legacy", Format: "anthropic", Model: "legacy-model", APIKey: "private", BaseURL: "https://vendor.example"})
	if dto.ID != "18" || dto.Model != "legacy-model" || dto.ConfigurationError == "" {
		t.Fatal("legacy identity/model or guidance lost")
	}
	raw, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"format":`, `"base_url":`, `"thinking_type":`, `"streaming":`, `"max_tokens_field":`, `"session_header_key":`, `private`} {
		if strings.Contains(string(raw), key) {
			t.Fatalf("private/retired value exposed: %s", key)
		}
	}
}

func TestProfileResolutionRejectsUnsupportedConnection(t *testing.T) {
	s := &Server{}
	for _, p := range []*db.LLMProfile{
		{ID: 9, Model: "legacy", Format: "anthropic", APIKey: "key"},
		{ID: 9, Model: "legacy", Format: "openai", BaseURL: "https://vendor.example", APIKey: "key"},
		{ID: 9, Model: "existing", Format: "openai", Proxy: "invalid-proxy-without-scheme", APIKey: "key"},
	} {
		got := s.resolutionFromProfile(p, "task_chain")
		if got.Available || got.Reason == "" || got.ProfileID == nil || *got.ProfileID != p.ID || got.Model != p.Model {
			t.Fatalf("unsupported selection lost or marked ready: %+v", got)
		}
	}
}

func TestKeylessRetiredProfileRemainsSelected(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	id, err := m.pg.SaveProfile(&db.LLMProfile{Name: "keyless-retired-selection", Model: "original-model", AuthMode: "api_key"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = m.pg.Exec(`UPDATE llm_profiles SET is_default=false WHERE id=$1`, id)
		_ = m.pg.DeleteProfile(id)
	})
	_, err = m.pg.Exec(`UPDATE llm_profiles SET format='anthropic' WHERE id=$1`, id)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{m: m}
	cfg, selected := s.loadProfileConfig(id)
	if !selected || cfg.Model != "original-model" {
		t.Fatalf("invalid selection fell through: selected=%v model=%s", selected, cfg.Model)
	}
	if err := m.pg.SetActiveProfile(id); err != nil {
		t.Fatal(err)
	}
	if _, selected := s.loadLLMConfig(); !selected {
		t.Fatal("invalid active selection fell through to environment")
	}
	if key, err := s.savedAPIKey(); err == nil || key != "" {
		t.Fatal("legacy credential could be silently adopted")
	}
}
