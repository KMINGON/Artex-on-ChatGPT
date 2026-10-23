package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/chatgptllm"
	"github.com/Autumn-27/artex/db"
)

func TestAPIModelLookupPreservesCatalogAndReusesSelectedProfileKey(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer catalog-fixture" {
			t.Errorf("wrong model catalog request or key")
		}
		fmt.Fprint(w, `{"data":[{"id":"gpt-6-sol","available_access_programs":{"cyber":["standard"]},"supported_reasoning_levels":[{"effort":"medium"}],"default_reasoning_level":"medium"},{"id":"future-model"}]}`)
	}))
	t.Cleanup(upstream.Close)
	previous := http.DefaultTransport
	transport := previous.(*http.Transport).Clone()
	transport.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "api.openai.com:443" {
			t.Errorf("nonofficial catalog endpoint: %s", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous; transport.CloseIdleConnections() })
	pg := profileSelectionDB(t)
	id, err := pg.SaveProfile(&db.LLMProfile{Name: "saved", Model: "gpt-6-sol", AuthMode: "api_key", APIKey: "catalog-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{pg: pg}}
	for _, body := range []string{`{"api_key":"catalog-fixture"}`, fmt.Sprintf(`{"profile_id":%d}`, id)} {
		w := httptest.NewRecorder()
		s.pgListModels(w, httptest.NewRequest("POST", "/api/llm/models", strings.NewReader(body)))
		var result struct {
			OK      bool               `json:"ok"`
			Models  []string           `json:"models"`
			Details []chatgptllm.Model `json:"model_details"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || !result.OK || !reflect.DeepEqual(result.Models, []string{"gpt-6-sol", "future-model"}) {
			t.Fatalf("model lookup: %d %s", w.Code, w.Body.String())
		}
		c := result.Details[0].Capabilities
		if c.Source != "account_catalog" || !c.AccessProgramsKnown || c.DaybreakAvailable == nil || *c.DaybreakAvailable || c.DefaultReasoningEffort != "medium" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("catalog metadata dropped: %+v", c)
		}
		if strings.Contains(w.Body.String(), "catalog-fixture") {
			t.Fatal("API credential exposed by model catalog")
		}
	}
}

func TestSubscriptionStandardProfileSaveReloadAndRuntimeConfig(t *testing.T) {
	pg := profileSelectionDB(t)
	dir := t.TempDir()
	raw, err := json.Marshal(map[string]any{
		"version": 1, "host_id": "urn:uuid:12345678-1234-4234-8234-123456789abc",
		"accounts": map[string]any{"fixture-account": map[string]any{
			"client_id": "fixture-account", "issuer": "https://auth.openai.com", "subject": "fixture-subject",
			"refresh_token": "fixture-refresh", "refresh_expires_at": time.Now().Add(time.Hour),
			"scopes": []string{"chatgpt.tokens.use.direct"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "accounts.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{pg: pg, dir: t.TempDir()}, chatGPTDir: dir, provByProfile: map[int64]*provEntry{}}
	t.Cleanup(func() {
		if s.chatGPT != nil {
			_ = s.chatGPT.Close()
		}
	})
	id := saveProfileForTest(t, s, `{"name":"subscription","model":"gpt-6-sol","auth_mode":"chatgpt_plan","chatgpt_account_id":"fixture-account","access_program_cyber":"standard","reasoning_effort":"ultra"}`)
	for _, cyber := range []string{"standard", ""} {
		if cyber == "" {
			saveProfileForTest(t, s, fmt.Sprintf(`{"id":%d,"name":"subscription","model":"gpt-6-sol","auth_mode":"chatgpt_plan","chatgpt_account_id":"fixture-account","access_program_cyber":"","reasoning_effort":"ultra"}`, id))
		}
		p, err := pg.ProfileByID(id)
		if err != nil || p == nil {
			t.Fatalf("profile reload: %v", err)
		}
		cfg, ok := s.loadProfileConfig(id)
		if !ok || cfg.AccessProgramCyber != cyber || cfg.ReasoningEffort != "ultra" || cfg.AuthMode != "chatgpt_plan" || cfg.APIKey != "" || cfg.ChatGPTAccountID != "fixture-account" || p.UnsupportedReason() != "" {
			t.Fatal("subscription options were rejected or lost between persistence and runtime")
		}
		dto := llmProfileDTO(p)
		if dto.AccessProgramCyber != cyber || dto.Capabilities.Source != "unknown" || dto.Capabilities.DaybreakAvailable != nil {
			t.Fatalf("stored option inferred account capability without lookup: %+v", dto)
		}
	}
}

func TestAPIProfilePreservesOptionsBeyondDocumentationSnapshot(t *testing.T) {
	pg := profileSelectionDB(t)
	s := &Server{m: &Manager{pg: pg, dir: t.TempDir()}, provByProfile: map[int64]*provEntry{}}
	id := saveProfileForTest(t, s, `{"name":"account-options","model":"gpt-6.1-sol","auth_mode":"api_key","api_key":"fixture-key","access_program_cyber":"daybreak_blue","reasoning_effort":"ultra","connection_check":{"model":"resolved-model","access_program_cyber":"daybreak_blue","checked_at":"2026-10-23T01:02:03Z"}}`)
	p, err := pg.ProfileByID(id)
	if err != nil || p == nil {
		t.Fatalf("profile reload: %v", err)
	}
	cfg, ok := s.loadProfileConfig(id)
	if !ok || cfg.ReasoningEffort != "ultra" || cfg.AccessProgramCyber != "daybreak_blue" || cfg.APIKey != "fixture-key" {
		t.Fatal("account-specific API options lost while saving or loading")
	}
	dto := llmProfileDTO(p)
	if dto.ReasoningEffort != "ultra" || dto.Capabilities.Source != "documentation" {
		t.Fatal("stored API setting was replaced or static capabilities claimed account access")
	}
	w := httptest.NewRecorder()
	s.pgListProfiles(w, httptest.NewRequest("GET", "/api/llm/profiles", nil))
	var listed struct {
		Profiles []LLMProfileDTO `json:"profiles"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &listed) != nil || len(listed.Profiles) != 1 {
		t.Fatalf("list profiles: %d %s", w.Code, w.Body.String())
	}
	check := listed.Profiles[0].ConnectionCheck
	if check == nil || check.Model != "resolved-model" || check.AccessProgramCyber != "daybreak_blue" || check.CheckedAt.Format(time.RFC3339) != "2026-10-23T01:02:03Z" {
		t.Fatalf("saved connection observation lost on list: %+v", check)
	}
	if strings.Contains(w.Body.String(), "fixture-key") || cfg.Model != "gpt-6.1-sol" {
		t.Fatal("connection observation exposed credentials or replaced requested model")
	}
	saveProfileForTest(t, s, fmt.Sprintf(`{"id":%d,"name":"account-options","model":"gpt-6.1-sol","auth_mode":"api_key","access_program_cyber":"standard","reasoning_effort":"ultra"}`, id))
	p, err = pg.ProfileByID(id)
	if err != nil || p == nil || p.ConnectionCheck != nil || p.AccessProgramCyber != "standard" {
		t.Fatalf("saving without a new observation retained stale metadata: %+v, %v", p, err)
	}
}
