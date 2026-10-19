package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func TestInvalidChatGPTConfigCannotFallBack(t *testing.T) {
	for _, cfg := range []Config{
		{AuthMode: "chatgpt_plan", Model: "test", Proxy: "://invalid"},
		{AuthMode: "chatgpt_plan"},
	} {
		provider, err := cfg.NewProvider()
		if err != nil || provider == nil {
			t.Fatalf("invalid subscription returned fallback signal: %v", err)
		}
		_, _, _, err = provider.Complete(context.Background(), llm.CompletionRequest{})
		var policy interface{ NoFallback() bool }
		if !errors.As(err, &policy) || !policy.NoFallback() {
			t.Fatalf("missing no-fallback policy: %v", err)
		}
	}
}

func TestChatGPTTransportPreservesQuotaStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"code":"insufficient_quota","message":"quota exhausted"}}`))
	}))
	defer srv.Close()
	client, err := ChatGPTHTTPClient("")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 429 {
		t.Fatalf("subscription status rewritten to %d", resp.StatusCode)
	}
}
