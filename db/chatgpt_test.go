package db

import (
	"fmt"
	"testing"
	"time"
)

func TestChatGPTProfileRoundTripAndBillingIsolation(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	p := &LLMProfile{Name: fmt.Sprintf("chatgpt-mode-%d", time.Now().UnixNano()), Format: "openai", Model: "test", APIKey: "old-api-key"}
	id, err := d.SaveProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id)
	p.ID = id
	p.APIKey = ""
	if _, err := d.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	stored, err := d.ProfileByID(id)
	if err != nil || stored.APIKey != "old-api-key" || stored.AuthMode != "api_key" {
		t.Fatalf("legacy key preservation: %+v %v", stored, err)
	}
	p.AuthMode, p.ChatGPTAccountID, p.Format = "chatgpt_plan", "client-reference-only", "openai-responses"
	p.BaseURL = "https://example.invalid"
	if _, err := d.SaveProfile(p); err == nil {
		t.Fatal("custom endpoint accepted")
	}
	p.BaseURL, p.SessionHeaderKey, p.MaxTokens = "", "Authorization", 123
	if _, err := d.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	stored, err = d.ProfileByID(id)
	if err != nil || stored.APIKey != "" || stored.AuthMode != "chatgpt_plan" || stored.ChatGPTAccountID != p.ChatGPTAccountID || !stored.Streaming || !stored.PoolExclude || stored.MaxTokens != 0 || stored.BaseURL != "" || stored.SessionHeaderKey != "" {
		t.Fatalf("ChatGPT roundtrip: %+v %v", stored, err)
	}
	profiles, err := d.ListProfiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range profiles {
		if profile.ID == id && profile.APIKeyHint != "" {
			t.Fatal("stale API key hint")
		}
	}
	chain, err := d.PoolProfiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range chain {
		if profile.ID == id {
			t.Fatal("ChatGPT entered API failover pool")
		}
	}
	p.AuthMode = "api_key"
	if _, err := d.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	stored, err = d.ProfileByID(id)
	if err != nil || stored.APIKey != "" || stored.ChatGPTAccountID != "" {
		t.Fatalf("switch resurrected old credentials: %+v %v", stored, err)
	}
	p.APIKey = "replacement-api-key"
	if _, err := d.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	stored, err = d.ProfileByID(id)
	if err != nil || stored.APIKey != p.APIKey {
		t.Fatalf("explicit API key update failed: %v", err)
	}
}
