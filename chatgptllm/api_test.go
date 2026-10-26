package chatgptllm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/compaction"
	"github.com/Autumn-27/norma/llm"
)

func TestCredentialModesAndRequestControls(t *testing.T) {
	for _, cfg := range []Config{
		{Model: "m"},
		{Model: "m", APIKey: "key", Authorize: auth},
		{APIKey: "key"},
	} {
		if _, err := New(cfg); err == nil {
			t.Fatal("ambiguous or incomplete credential config accepted")
		}
	}
	temperature := 0.25
	for _, api := range []bool{false, true} {
		t.Run(fmt.Sprint(api), func(t *testing.T) {
			client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if r.Header.Get("X-API-Key") != "" || r.Header.Get("Anthropic-Version") != "" || r.Header.Get("ChatGPT-Account-ID") != "" {
					t.Error("foreign auth headers")
				}
				if api {
					if string(body["max_output_tokens"]) != "73" || string(body["temperature"]) != "0.25" {
						t.Error("API controls lost")
					}
				} else if body["max_output_tokens"] != nil || body["temperature"] != nil {
					t.Error("API controls leaked to subscription")
				}
				if string(body["stream"]) != "true" || string(body["store"]) != "false" || body["previous_response_id"] != nil || body["include"] != nil {
					t.Error("stateless SSE contract changed")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				completed(w, textItem("ok"))
			})
			p := testProviderMode(t, client, api)
			if _, _, _, err := p.Complete(t.Context(), llm.CompletionRequest{MaxTokens: 73, Temperature: &temperature}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func incomplete(w http.ResponseWriter, items ...map[string]any) {
	event(w, map[string]any{"type": "response.incomplete", "response": map[string]any{
		"id": "resp-cut", "status": "incomplete", "incomplete_details": map[string]any{"reason": "max_output_tokens"},
		"output": items, "usage": map[string]any{"input_tokens": 12, "output_tokens": 31},
	}})
}

func TestAPIIncompleteAcceptanceBoundary(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		api, auxiliary, tool, done bool
		wantSuccess                bool
	}{
		{name: "API text", api: true, wantSuccess: true},
		{name: "API done text", api: true, done: true, wantSuccess: true},
		{name: "API tool", api: true, tool: true},
		{name: "API summary", api: true, auxiliary: true},
		{name: "subscription text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				indexedItem(w, "added", 0, textItem(""))
				event(w, map[string]any{"type": "response.output_text.delta", "item_id": "msg1", "delta": "partial"})
				item := textItem("partial")
				item["status"] = "incomplete"
				if tc.done {
					indexedItem(w, "done", 0, item)
					incomplete(w)
				} else if tc.tool {
					incomplete(w, item, callItem("f1", "call1", "probe", "{}"))
				} else {
					incomplete(w, item)
				}
			})
			p := testProviderMode(t, client, tc.api)
			req := llm.CompletionRequest{MaxTokens: 31}
			if tc.auxiliary {
				req.Thinking = "disabled"
			}
			msg, stop, usage, err := p.Complete(t.Context(), req)
			if tc.wantSuccess {
				if err != nil || stop != "max_tokens" || msg.Text() != "partial" || len(msg.ProviderData) != 0 || usage.OutputTokens != 31 {
					t.Fatalf("bad partial result: stop=%s usage=%+v err=%v", stop, usage, err)
				}
			} else {
				var e *Error
				if !errors.As(err, &e) || !e.OutputRejected() || e.NoFallback() == tc.api || len(msg.Content) != 0 || stop != "" {
					t.Fatalf("incomplete accepted: stop=%s err=%v", stop, err)
				}
			}
		})
	}
}

func TestAPIIncompleteSummaryIsAtomicAndNotRetried(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			var calls atomic.Int32
			p := testProviderMode(t, fakeClient(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				incomplete(w, textItem("truncated summary"))
			}), true)
			original := []llm.Message{llm.UserText("retain this history")}
			summary, err := compaction.ProviderSummarizer(p, 31, !streaming)(t.Context(), original)
			if err == nil || summary != "" || calls.Load() != 1 || original[0].Text() != "retain this history" {
				t.Fatalf("partial summary accepted/retried: summary=%q err=%v calls=%d", summary, err, calls.Load())
			}
		})
	}
}

func TestRetryBudgetCoversHTTPAndSSEWithoutReplayingOutput(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		emit       bool
		want       int32
	}{
		{"known transient", "server_error", false, 3},
		{"model overloaded", "server_is_overloaded", false, 3},
		{"unknown error", "new_unclassified_code", false, 2},
		{"transient after output", "server_error", true, 2},
		{"overloaded after output", "server_is_overloaded", true, 2},
		{"quota exhausted", "insufficient_quota", false, 2},
		{"missing code", "", false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				w.Header().Set("X-Request-Id", "req-safe")
				if n == 1 {
					w.WriteHeader(503)
					fmt.Fprint(w, `{"error":{"code":"server_error"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if tc.emit {
					indexedItem(w, "added", 0, textItem(""))
					event(w, map[string]any{"type": "response.output_text.delta", "item_id": "msg1", "delta": "visible"})
				}
				event(w, map[string]any{"type": "response.failed", "response": map[string]any{
					"id": "resp-safe", "status": "failed",
					"error": map[string]any{"code": tc.code, "type": "server_error", "param": "input[2]", "message": "PRIVATE-BODY"},
				}})
			})
			p, err := New(Config{Model: "model", APIKey: "test-secret", HTTPClient: client, MaxRetries: 2, RetryInterval: time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			var final error
			for _, err := range p.Stream(t.Context(), llm.CompletionRequest{Messages: []llm.Message{llm.UserText("PRIVATE-PROMPT")}}) {
				if err != nil {
					final = err
				}
			}
			var e *Error
			if !errors.As(final, &e) || calls.Load() != tc.want || e.Attempts != int(tc.want) || e.OutputEmitted != tc.emit || e.NoFallback() || !e.NoReplay() || e.RetryableSameAuth() {
				t.Fatalf("bad retry policy: err=%v calls=%d", final, calls.Load())
			}
			if e.ResponseID != "resp-safe" || e.RequestID != "req-safe" || e.Type != "server_error" || e.EventType != "response.failed" {
				t.Fatalf("diagnostics missing: %+v", e.Diagnostics())
			}
			raw, _ := json.Marshal(e.Diagnostics())
			for _, secret := range []string{"test-secret", "PRIVATE-BODY", "PRIVATE-PROMPT"} {
				if strings.Contains(string(raw), secret) || strings.Contains(e.Error(), secret) {
					t.Fatal("private request/response escaped diagnostics")
				}
			}
		})
	}
}

func TestQuotaAdmissionDoesNotRetryAndRemainsClassifiable(t *testing.T) {
	for _, api := range []bool{false, true} {
		var calls atomic.Int32
		client := fakeClient(t, func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"code":"insufficient_quota","message":"PRIVATE-BILLING"}}`)
		})
		cfg := Config{Model: "m", Authorize: auth, HTTPClient: client, MaxRetries: 2}
		if api {
			cfg.APIKey, cfg.Authorize = "test-secret", nil
		}
		p, _ := New(cfg)
		_, _, _, err := p.Complete(t.Context(), llm.CompletionRequest{})
		var e *Error
		if !errors.As(err, &e) || e.NoFallback() == api || calls.Load() != 1 || !strings.Contains(e.Error(), "insufficient_quota") {
			t.Fatalf("quota classification lost: err=%v calls=%d", err, calls.Load())
		}
	}
}

func TestErrorEnvelopesAndSafeShape(t *testing.T) {
	for _, tc := range []struct{ body, code, typ, class string }{
		{`{"error":{"code":"server_error","type":"server_error"}}`, "server_error", "server_error", "known_code"},
		{`{"error":{"code":"server_is_overloaded","type":"service_unavailable_error"}}`, "server_is_overloaded", "service_unavailable_error", "known_code"},
		{`{"type":"error","code":"server_error"}`, "server_error", "", "known_code"},
		{`{"type":"error"}`, "", "", "missing_code"},
		{`{"error":{"code":"future_failure"}}`, "future_failure", "", "unknown_code"},
		{`{"error":`, "", "", "malformed_error"},
		{`{"error":{"code":"unsafe secret\nvalue","param":"sk-secret"}}`, "", "", "missing_code"},
	} {
		e := responseFailure([]byte(tc.body))
		if e.Code != tc.code || e.Type != tc.typ || e.ErrorClass != tc.class {
			t.Fatalf("bad envelope classification: %+v", e.Diagnostics())
		}
	}
	msg := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "PRIVATE-ID", Name: "probe", Input: json.RawMessage(`{}`)}}}
	raw, _ := json.Marshal(callItem("PRIVATE-ITEM", "PRIVATE-ID", "probe", "{}"))
	if err := attachReplayState(&msg, []json.RawMessage{raw}); err != nil {
		t.Fatal(err)
	}
	req := llm.CompletionRequest{Messages: []llm.Message{llm.UserText("PRIVATE-PROMPT"), msg,
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText("PRIVATE-ID", strings.Repeat("x", 30000), false)}}}}
	body, _, err := encodeRequest(Config{Model: "m"}, req)
	if err != nil {
		t.Fatal(err)
	}
	shape := requestShape(body, req)
	if shape.Bytes != len(body) || shape.InputItems != 3 || shape.ToolResultBytes != 30000 || shape.ToolResultCount != 1 || shape.ReplayMessages != 1 || !shape.ToolCallPairsValid {
		t.Fatalf("bad shape: %+v", shape)
	}
	encoded, _ := json.Marshal(shape)
	if strings.Contains(string(encoded), "PRIVATE") || strings.Contains(string(encoded), strings.Repeat("x", 10)) {
		t.Fatal("request contents escaped shape")
	}
}

func TestAPITextLimitContinuesThroughNorma(t *testing.T) {
	var calls atomic.Int32
	p := testProviderMode(t, fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		var req struct {
			Input []json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if calls.Add(1) == 1 {
			incomplete(w, textItem("partial answer"))
			return
		}
		body, _ := json.Marshal(req.Input)
		if !strings.Contains(string(body), "partial answer") {
			t.Error("truncated text missing from continuation")
		}
		completed(w, textItem("finished answer"))
	}), true)
	out, err := agentcore.Run(t.Context(), agentcore.Options{Provider: p, WorkingDir: t.TempDir(), MaxTurns: 2, MaxTokens: 31}, "answer")
	if err != nil || !strings.Contains(out, "finished answer") || calls.Load() != 2 {
		t.Fatalf("Norma did not continue: output=%q err=%v calls=%d", out, err, calls.Load())
	}
}

func TestCredentialSpecificRetryDefaults(t *testing.T) {
	for _, api := range []bool{false, true} {
		t.Run(fmt.Sprint(api), func(t *testing.T) {
			var calls atomic.Int32
			client := fakeClient(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(503)
				fmt.Fprint(w, `{"error":{"code":"server_error"}}`)
			})
			cfg := Config{Model: "m", Authorize: auth, HTTPClient: client, RetryInterval: time.Millisecond}
			want := int32(3)
			if api {
				cfg.APIKey, cfg.Authorize = "test-secret", nil
				want = 4
			}
			p, _ := New(cfg)
			_, _, _, err := p.Complete(t.Context(), llm.CompletionRequest{})
			var e *Error
			if !errors.As(err, &e) || calls.Load() != want || e.Attempts != int(want) {
				t.Fatalf("default budget changed: err=%v calls=%d", err, calls.Load())
			}
		})
	}
}

func TestAuthorizationFailureHasNoHTTPAttempt(t *testing.T) {
	p, err := New(Config{Model: "m", Authorize: func(ctx context.Context) (context.Context, context.CancelFunc, string, error) {
		return nil, nil, "", errors.New("private authorization detail")
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = p.Complete(t.Context(), llm.CompletionRequest{})
	var e *Error
	if !errors.As(err, &e) || e.Attempts != 0 || e.Stage != "authorization" || !e.NoFallback() {
		t.Fatalf("authorization was counted as inference: %v", err)
	}
}

func TestRetrySettingsRemainCredentialSpecific(t *testing.T) {
	for _, api := range []bool{false, true} {
		var calls atomic.Int32
		client := fakeClient(t, func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":{"code":"server_error"}}`)
		})
		cfg := Config{Model: "m", Authorize: auth, HTTPClient: client, MaxRetries: 99, RetryInterval: time.Nanosecond}
		want := int32(6)
		if api {
			cfg.APIKey, cfg.Authorize, want = "test-secret", nil, 21
		}
		p, _ := New(cfg)
		if _, _, _, err := p.Complete(t.Context(), llm.CompletionRequest{}); err == nil || calls.Load() != want {
			t.Fatalf("credential retry cap changed: api=%v calls=%d err=%v", api, calls.Load(), err)
		}
	}
	api, _ := New(Config{Model: "m", APIKey: "key", RetryInterval: 3 * time.Millisecond})
	plan, _ := New(Config{Model: "m", Authorize: auth, RetryInterval: 3 * time.Millisecond})
	if api.(*provider).retryDelay(3) != 3*time.Millisecond || plan.(*provider).retryDelay(3) != 24*time.Millisecond {
		t.Fatal("explicit API fixed interval or subscription backoff changed")
	}
	api, _ = New(Config{Model: "m", APIKey: "key"})
	if api.(*provider).retryDelay(8) != 8*time.Second {
		t.Fatal("default API backoff cap changed")
	}
}

func TestPermanentHTTPStatusDoesNotRetryTransientLookingCode(t *testing.T) {
	for _, status := range []int{400, 401, 403} {
		var calls atomic.Int32
		client := fakeClient(t, func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":{"code":"server_error"}}`)
		})
		p, _ := New(Config{Model: "m", APIKey: "test-secret", HTTPClient: client, MaxRetries: 2, RetryInterval: time.Nanosecond})
		if _, _, _, err := p.Complete(t.Context(), llm.CompletionRequest{}); err == nil || calls.Load() != 1 {
			t.Fatalf("permanent HTTP status replayed: status=%d calls=%d err=%v", status, calls.Load(), err)
		}
	}
}
