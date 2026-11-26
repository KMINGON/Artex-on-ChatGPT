package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Provider names as they appear in the errors a user reads.
const (
	anthropicName = "Anthropic API"
	openaiName    = "OpenAI 호환 API"
)

// StatusError is a reply other than HTTP 200. Its text keeps "status <code>" so a
// host that classifies failures by status can still read it.
type StatusError struct {
	Provider   string
	StatusCode int
	Message    string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s 요청 실패 (status %d): %s", e.Provider, e.StatusCode, e.Message)
}

// IncompleteStreamError is a stream that ended without the provider's completion
// signal. The text received so far is not a finished turn.
type IncompleteStreamError struct{ Provider string }

func (e *IncompleteStreamError) Error() string {
	return e.Provider + " 응답 스트림이 완료 신호 없이 끊겼습니다"
}

// RefusalError is a turn the provider's safety classifiers declined. The same
// request is declined again, so it is neither replayed nor moved to another model.
type RefusalError struct {
	Provider    string
	Category    string
	Explanation string
}

func (e *RefusalError) Error() string {
	msg := e.Provider + " 모델이 요청을 거부했습니다"
	if e.Category != "" {
		msg += " (분류: " + e.Category + ")"
	}
	if e.Explanation != "" {
		msg += ": " + e.Explanation
	}
	return msg
}

func (e *RefusalError) NoReplay() bool   { return true }
func (e *RefusalError) NoFallback() bool { return true }

// errorMessage pulls the readable message out of an error body. Providers send
// {"error":{"message":...}}; some local servers send {"error":"..."}.
func errorMessage(body []byte) string {
	var shaped struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &shaped) == nil && len(shaped.Error) > 0 {
		var detail struct {
			Message string `json:"message"`
		}
		var text string
		if json.Unmarshal(shaped.Error, &detail) == nil && detail.Message != "" {
			return truncate(detail.Message, 500)
		}
		if json.Unmarshal(shaped.Error, &text) == nil && text != "" {
			return truncate(text, 500)
		}
	}
	return truncate(strings.TrimSpace(string(body)), 500)
}

// doStream sends the streaming request with retries on transient failures and
// returns a 200 response (caller reads + closes the body). Only the request
// establishment phase is retried — connection reset / timeout / 429 / 5xx before
// any stream data is consumed — so retrying is safe (it can't duplicate
// partially-streamed assistant output). A mid-stream drop is the caller's to
// surface. Context cancellation is never retried and interrupts the backoff.
// Count comes from cfg.retries(), the wait between attempts from
// cfg.retryDelay() (exponential by default, fixed when Config.RetryInterval is set).
func doStream(ctx context.Context, cfg Config, url string, body []byte, setHeaders func(*http.Request), provider string) (*http.Response, error) {
	// Rate limit once per logical request (outside the retry loop, so retries do
	// not consume additional tokens). Shared across all callers of this provider.
	if err := cfg.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	retries := cfg.retries()
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		setHeaders(req)

		resp, err := cfg.HTTPClient.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			return resp, nil
		}

		retryable := false
		switch {
		case err != nil:
			retryable = ctx.Err() == nil // transient network error (not cancellation)
		case isRetryableStatus(resp.StatusCode):
			retryable = true
		}
		if attempt >= retries || !retryable {
			if err != nil {
				return nil, err
			}
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			return nil, &StatusError{Provider: provider, StatusCode: resp.StatusCode, Message: errorMessage(data)}
		}
		if resp != nil {
			resp.Body.Close()
		}
		if !backoffSleep(ctx, cfg.retryDelay(attempt)) {
			return nil, ctx.Err() // context cancelled during backoff
		}
	}
}

func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, // 408
		http.StatusTooManyRequests,     // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
		return true
	}
	return false
}

// expBackoff is the default retry ladder: 0.5s, 1s, 2s, … capped at 8s. Used
// whenever the caller did not pin a fixed interval on the Config.
func expBackoff(attempt int) time.Duration {
	d := 500 * time.Millisecond * (1 << attempt)
	if d > 8*time.Second {
		d = 8 * time.Second
	}
	return d
}

// backoffSleep waits d, interruptible by ctx. Returns false if the context was
// cancelled (or d is non-positive, which never happens for a real wait).
func backoffSleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
