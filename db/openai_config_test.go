package db

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenAIProfileContractRetainsLegacyPrivately(t *testing.T) {
	p := LLMProfile{ID: 71, Format: "anthropic", BaseURL: "https://legacy.example", Model: "legacy-model", APIKey: "secret-key", ThinkingType: "enabled", Streaming: false, MaxTokensField: "max_completion_tokens", SessionHeaderKey: "x-legacy", Retry: RetryOverride{Empty: RetryRule{Attempts: 4}, Stream: RetryRule{Attempts: 3}}}
	if p.UnsupportedReason() == "" {
		t.Fatal("legacy provider accepted")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"format", "base_url", "thinking_type", "streaming", "max_tokens_field", "session_header_key", "api_key"} {
		if _, ok := fields[key]; ok {
			t.Fatalf("retired/private field %s exposed", key)
		}
	}
	if strings.Contains(string(raw), "secret-key") || strings.Contains(string(fields["retry"]), "empty") || strings.Contains(string(fields["retry"]), "stream") {
		t.Fatal("secret or retired retry exposed")
	}
	if p.ID != 71 || p.Model != "legacy-model" || p.BaseURL != "https://legacy.example" {
		t.Fatal("inspection rewrote the legacy profile")
	}
	for _, p := range []LLMProfile{{Format: "openai", Model: "gpt-existing"}, {Format: "openai-responses", BaseURL: "https://api.openai.com/v1", Model: "gpt-existing"}, {AuthMode: "chatgpt_plan", Format: "openai-responses", ChatGPTAccountID: "account", Model: "model"}} {
		if p.UnsupportedReason() != "" {
			t.Fatal("official profile rejected")
		}
	}
	for _, p := range []LLMProfile{{Format: "unknown"}, {BaseURL: "https://api.openai.com.evil/v1"}, {BaseURL: "http://api.openai.com/v1"}, {AuthMode: "other"}} {
		if p.UnsupportedReason() == "" {
			t.Fatal("unsupported profile accepted")
		}
	}
}

func TestProfileEditPreservesRetiredColumns(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	id, err := d.SaveProfile(&LLMProfile{Name: "retired-column-preservation", Format: "openai", Model: "existing-model", APIKey: "old-key"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id) })
	_, err = d.Exec(`UPDATE llm_profiles SET thinking_type='enabled',streaming=false,max_tokens_field='max_completion_tokens',session_header_key='x-legacy',retry_empty_attempts=7,retry_empty_interval_ms=123,retry_stream_attempts=5,retry_stream_interval_ms=321 WHERE id=$1`, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.SaveProfile(&LLMProfile{ID: id, Name: "edited", Model: "existing-model", AuthMode: "api_key", MaxTokens: 400}); err != nil {
		t.Fatal(err)
	}
	p, err := d.ProfileByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != id || p.Model != "existing-model" || p.APIKey != "old-key" || p.MaxTokens != 400 {
		t.Fatal("identity, model, key or output budget lost")
	}
	if p.ThinkingType != "enabled" || p.Streaming || p.MaxTokensField != "max_completion_tokens" || p.SessionHeaderKey != "x-legacy" || p.Retry.Empty.Attempts != 7 || p.Retry.Empty.IntervalMS != 123 || p.Retry.Stream.Attempts != 5 || p.Retry.Stream.IntervalMS != 321 {
		t.Fatal("retired database values were erased")
	}
	_, err = d.Exec(`UPDATE llm_profiles SET format='anthropic',base_url='https://legacy.example',model='legacy-model' WHERE id=$1`, id)
	if err != nil {
		t.Fatal(err)
	}
	p, err = d.ProfileByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if p.ConfigurationError == "" || p.ID != id || p.Model != "legacy-model" || p.BaseURL != "https://legacy.example" {
		t.Fatal("legacy profile hidden or silently converted")
	}
}

func TestRetryPolicyEditPreservesRetiredSettings(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	before, existed, err := d.GetSetting(settingLLMRetryPolicy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = d.SetSetting(settingLLMRetryPolicy, before)
		} else {
			_, _ = d.Exec(`DELETE FROM settings WHERE key=$1`, settingLLMRetryPolicy)
		}
	})
	legacy := `{"stream":{"attempts":7,"interval_ms":432},"intent":{"attempts":4,"interval_ms":987},"future_legacy":{"keep":true}}`
	if err := d.SetSetting(settingLLMRetryPolicy, legacy); err != nil {
		t.Fatal(err)
	}
	if err := d.SetLLMRetryPolicy(LLMRetryPolicy{Connect: RetryRule{Attempts: 3}, Empty: RetryRule{Attempts: 5}}); err != nil {
		t.Fatal(err)
	}
	got := d.LLMRetryPolicy()
	if got.Connect.Attempts != 3 || got.Empty.Attempts != 5 || got.Stream != (RetryRule{Attempts: 7, IntervalMS: 432}) || got.Intent != (RetryRule{Attempts: 4, IntervalMS: 987}) {
		t.Fatalf("legacy or active rules changed: %+v", got)
	}
	public, _ := json.Marshal(got)
	if strings.Contains(string(public), "stream") || strings.Contains(string(public), "intent") {
		t.Fatalf("retired controls exposed: %s", public)
	}
	saved, _, err := d.GetSetting(settingLLMRetryPolicy)
	if err != nil {
		t.Fatal(err)
	}
	var beforeFields, afterFields map[string]json.RawMessage
	_ = json.Unmarshal([]byte(legacy), &beforeFields)
	_ = json.Unmarshal([]byte(saved), &afterFields)
	for key, value := range beforeFields {
		if string(afterFields[key]) != string(value) {
			t.Fatalf("retired field %s changed", key)
		}
	}
}

func TestProfileAccessProgramRoundTrip(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	id, err := d.SaveProfile(&LLMProfile{Name: "access-program-roundtrip", Model: "existing-model", APIKey: "fixture-key", AuthMode: "api_key", AccessProgramCyber: "daybreak_blue"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id) })
	p, err := d.ProfileByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if p.AccessProgramCyber != "daybreak_blue" {
		t.Fatal("keyed profile lost access program")
	}
	listed, err := d.ListProfiles()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range listed {
		if entry.ID == id {
			found = true
			if entry.AccessProgramCyber != "daybreak_blue" || entry.APIKey != "" {
				t.Fatal("public profile lost program or exposed key")
			}
		}
	}
	if !found {
		t.Fatal("profile missing from list")
	}
	p.AccessProgramCyber = "standard"
	p.APIKey = ""
	if _, err = d.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	p, err = d.ProfileByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if p.AccessProgramCyber != "standard" || p.APIKey != "fixture-key" || p.ID != id || p.Model != "existing-model" {
		t.Fatal("editing program changed connection identity fields or discarded key")
	}
	p.AccessProgramCyber = ""
	if _, err = d.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	p, err = d.ProfileByID(id)
	if err != nil || p.AccessProgramCyber != "" {
		t.Fatal("default did not omit explicit program")
	}
	for _, entry := range []LLMProfile{{AuthMode: "chatgpt_plan", ChatGPTAccountID: "saved", AccessProgramCyber: "daybreak_blue"}, {AuthMode: "api_key", AccessProgramCyber: "invented"}} {
		if _, err = d.SaveProfile(&entry); err == nil {
			t.Fatal("unsupported program persisted")
		}
	}
}
