package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestIsQuotaExhaustedMessage(t *testing.T) {
	t.Parallel()
	positive := []string{
		`{"error":{"code":"insufficient_quota"}}`,
		`RESOURCE_EXHAUSTED`,
		`You exceeded your current quota, please check your plan and billing details.`,
		`billing_not_active`,
		`credit balance is too low`,
		`账户余额不足，请充值`,
	}
	for _, message := range positive {
		if !IsQuotaExhaustedMessage(message) {
			t.Errorf("expected quota classification for %q", message)
		}
	}

	negative := []string{
		`status 429: rate limit exceeded`,
		`RESOURCE_EXHAUSTED: rate limit exceeded`,
		`too many requests per minute`,
		`quota exceeded for quota metric GenerateRequestsPerMinutePerProjectPerBaseModel`,
		`RESOURCE_EXHAUSTED: TPM quota exceeded`,
		`tokens per minute quota exceeded`,
		`rate_limit_exceeded: requests per second`,
		`status 401: invalid api key`,
		`status 401: insufficient_quota`,
		`HTTP 403: billing_hard_limit_reached`,
		`status 500: internal server error`,
		`status=503: insufficient_quota`,
		`context length exceeded`,
	}
	for _, message := range negative {
		if IsQuotaExhaustedMessage(message) {
			t.Errorf("unexpected quota classification for %q", message)
		}
	}
}

func TestProviderDoesNotRetryExplicitQuota429(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":"insufficient_quota","message":"You exceeded your current quota"}}`)
	}))
	defer upstream.Close()

	provider, err := apiFixtureConfig(t, upstream).NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	var streamErr error
	for _, err := range provider.Stream(context.Background(), llm.CompletionRequest{
		Messages: []llm.Message{llm.UserText("ping")},
	}) {
		if err != nil {
			streamErr = err
		}
	}
	if streamErr == nil || !IsQuotaExhaustedMessage(streamErr.Error()) {
		t.Fatalf("expected explicit quota error, got %v", streamErr)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("explicit quota request retried %d times, want exactly one request", got)
	}
}
