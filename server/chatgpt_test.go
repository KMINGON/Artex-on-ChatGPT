package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/chatgptllm"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

func TestDisconnectedChatGPTStaysSelectedWithoutAPIFallback(t *testing.T) {
	s := &Server{chatGPTDir: t.TempDir()}
	cfg := agent.ConfigFrom("openai", "test-model", "", "never-use-this-key", "")
	s.configureChatGPT(&cfg, &db.LLMProfile{AuthMode: "chatgpt_plan", ChatGPTAccountID: "disconnected"})
	defer s.chatGPT.Close()
	if cfg.APIKey != "" || cfg.Provider() != "openai-responses" {
		t.Fatal("unsupported API configuration retained")
	}
	provider, err := cfg.NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	if s.poolChain(1, provider, cfg) != nil {
		t.Fatal("subscription joined automatic API pool")
	}
	_, _, _, err = provider.Complete(context.Background(), llm.CompletionRequest{})
	var policy interface{ NoFallback() bool }
	if err == nil || !errors.As(err, &policy) || !policy.NoFallback() {
		t.Fatalf("expected terminal account error, got %v", err)
	}
	if isQuotaExhaustedError(fmt.Errorf("wrapped: %w", &chatgptllm.Error{StatusCode: 429, Message: "quota exhausted"})) {
		t.Fatal("ChatGPT quota failure advanced task billing chain")
	}
}

func TestPersistedChatGPTProfileLoadsWithoutAPIKey(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("isolated PostgreSQL configuration required")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	previous, err := d.ActiveProfile()
	if err != nil {
		t.Fatal(err)
	}
	id, err := d.SaveProfile(&db.LLMProfile{Name: fmt.Sprintf("chatgpt-reload-%d", time.Now().UnixNano()), Format: "openai-responses", Model: "test-model", AuthMode: "chatgpt_plan", ChatGPTAccountID: "registered-reference"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id)
		if previous != nil {
			_ = d.SetActiveProfile(previous.ID)
		}
	}()
	if err := d.SetActiveProfile(id); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{pg: d}, chatGPTDir: t.TempDir()}
	cfg, ok := s.loadLLMConfig()
	if s.chatGPT != nil {
		defer s.chatGPT.Close()
	}
	if !ok || cfg.AuthMode != "chatgpt_plan" || cfg.APIKey != "" || cfg.ChatGPTAccountID != "registered-reference" {
		t.Fatalf("keyless active profile was discarded: %+v %v", cfg, ok)
	}
	pinned, ok := s.loadProfileConfig(id)
	if !ok || pinned != cfg {
		t.Fatal("active and pinned profiles use different authentication")
	}
	profile, err := d.ProfileByID(id)
	if err != nil {
		t.Fatal(err)
	}
	resolved := s.resolutionFromProfile(profile, "global_profile")
	if resolved.Available || resolved.ProfileID == nil || *resolved.ProfileID != id {
		t.Fatal("disconnected selected profile should remain selected and unavailable")
	}
}

func TestChatGPTMutationRequiresLocalSameOrigin(t *testing.T) {
	for _, tc := range []struct {
		remote, origin string
		allowed        bool
	}{
		{"127.0.0.1:1234", "http://127.0.0.1:8787", true},
		{"[::1]:1234", "", true},
		{"192.0.2.1:1234", "", false},
		{"127.0.0.1:1234", "https://other.invalid", false},
	} {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8787/api/chatgpt/login", strings.NewReader(`{}`))
		r.RemoteAddr = tc.remote
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		if got := localChatGPTMutation(w, r); got != tc.allowed {
			t.Fatalf("%+v: allowed=%v", tc, got)
		}
	}
}

func TestChatGPTRoutesRequireArtexLogin(t *testing.T) {
	s := &Server{}
	for _, path := range []string{"status", "login", "logout", "models"} {
		called := false
		h := s.requireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/api/chatgpt/"+path, nil))
		if called || w.Code != http.StatusUnauthorized {
			t.Fatalf("%s bypassed administrator authentication", path)
		}
	}
}

func TestChatGPTFailureDoesNotReplayThroughTaskRetries(t *testing.T) {
	for _, failure := range []*chatgptllm.Error{
		{StatusCode: 200, Code: "subscription_sharing_usage_limit_exceeded", Message: "late stream limit"},
		{StatusCode: 503, Code: "subscription_sharing_usage_unavailable", Message: "admission retries exhausted"},
		{Code: "interrupted_stream", Message: "stream ended before completion"},
	} {
		if isRetryableStreamError(fmt.Errorf("wrapped: %w", failure)) {
			t.Fatalf("task replayed protected ChatGPT failure: %s", failure.Code)
		}
	}
	if !isRetryableStreamError(errors.New("connection reset by peer")) {
		t.Fatal("existing API-provider transport retry was disabled")
	}
}
