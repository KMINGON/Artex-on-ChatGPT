package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/chatgptllm"
)

// A rewritten transport keeps production URL validation intact while using a local fixture.
func apiFixtureConfig(t *testing.T, srv *httptest.Server) Config {
	t.Helper()
	cfg := ConfigFrom("openai-responses", "test-model", "", "test-placeholder", "")
	cfg.httpClient = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		cloned := req.Clone(req.Context())
		target, _ := url.Parse(srv.URL)
		cloned.URL.Scheme, cloned.URL.Host = target.Scheme, target.Host
		return http.DefaultTransport.RoundTrip(cloned)
	})}
	return cfg
}

func writeCompleted(w http.ResponseWriter, output any) {
	w.Header().Set("Content-Type", "text/event-stream")
	body, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": output}})
	fmt.Fprintf(w, "data: %s\n\n", body)
}

func textOutput(text string) []any {
	return []any{map[string]any{"type": "message", "id": "message_text", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": text}}}}
}

func TestConnectionUsesOneToolFreeResponsesRequest(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if _, exists := req["tools"]; exists {
			t.Error("connection test exposed tools")
		}
		if r.URL.Path != "/v1/responses" || req["stream"] != true || req["model"] != "test-model" || req["max_output_tokens"] != float64(8192) {
			t.Error("Responses configuration lost")
		}
		writeCompleted(w, textOutput("OK"))
	}))
	defer srv.Close()
	cfg := apiFixtureConfig(t, srv)
	cfg.ReasoningEffort = "high"
	cfg.MaxTokens = 8192
	_, reply, err := TestConnection(context.Background(), cfg)
	if err != nil || reply != "OK" || calls.Load() != 1 {
		t.Fatalf("reply=%q err=%v calls=%d", reply, err, calls.Load())
	}
}

func TestConnectionUsesConfiguredOutputLimit(t *testing.T) {
	for _, limit := range []int{0, 256} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req map[string]any
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				got, present := req["max_output_tokens"]
				if (limit == 0 && present) || (limit > 0 && got != float64(limit)) {
					t.Errorf("max_output_tokens=%v present=%v, configured=%d", got, present, limit)
				}
				writeCompleted(w, textOutput("OK"))
			}))
			defer srv.Close()
			cfg := apiFixtureConfig(t, srv)
			cfg.MaxTokens = limit
			if _, _, err := TestConnection(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConnectionNeverExecutesUnexpectedToolCall(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "unexpected-tool-write")
	args, _ := json.Marshal(map[string]string{"file_path": marker, "content": "must not execute"})
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeCompleted(w, []any{map[string]any{"type": "function_call", "id": "function_unexpected", "call_id": "unexpected", "name": "Write", "namespace": "artex", "arguments": string(args), "status": "completed"}})
	}))
	defer srv.Close()
	cfg := apiFixtureConfig(t, srv)
	_, reply, err := TestConnection(context.Background(), cfg)
	if err == nil || reply != "" || calls.Load() != 1 {
		t.Fatalf("unexpected tool response accepted or repeated: reply=%q err=%v calls=%d", reply, err, calls.Load())
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("connection test executed a tool: %v", err)
	}
}

func TestConnectionDoesNotRetryFailureOrEmptyReply(t *testing.T) {
	for _, status := range []int{200, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if status == 200 {
					writeCompleted(w, textOutput(""))
				} else {
					w.WriteHeader(status)
				}
			}))
			defer srv.Close()
			cfg := apiFixtureConfig(t, srv)
			cfg.Retry.ConnectAttempts = 4
			_, reply, err := TestConnection(context.Background(), cfg)
			if err == nil || reply != "" || calls.Load() != 1 {
				t.Fatalf("failed test was accepted/retried: reply=%q err=%v calls=%d", reply, err, calls.Load())
			}
		})
	}
}

func TestConnectionHonorsEarlierCallerDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer srv.Close()
	cfg := apiFixtureConfig(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, _, err := TestConnection(ctx, cfg)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("caller deadline lost: %v", err)
	}
}

func TestChatGPTConnectionLogsOnlySafeResultFields(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	logChatGPTTestConnection(fmt.Errorf("wrapped: %w", &chatgptllm.Error{
		StatusCode: 400, Code: "unsupported_parameter", Param: "reasoning.effort", RequestID: "request-test",
		Message: "private upstream message", Body: []byte("private response body and credential"),
	}))
	line := output.String()
	for _, expected := range []string{"status=400", `code="unsupported_parameter"`, `param="reasoning.effort"`, `request_id="request-test"`} {
		if !strings.Contains(line, expected) {
			t.Fatalf("missing diagnostic %q: %s", expected, line)
		}
	}
	if strings.Contains(line, "private") || strings.Contains(line, "credential") {
		t.Fatalf("private diagnostic leaked: %s", line)
	}
	output.Reset()
	_, _, err := TestConnection(context.Background(), Config{AuthMode: "chatgpt_plan", Model: "test", APIKey: "never-use-this-key"})
	if err == nil {
		t.Fatal("signed-out account unexpectedly succeeded")
	}
	line = output.String()
	if !strings.Contains(line, `code="authorization_failed"`) || strings.Contains(line, "未发出任何 HTTP") || strings.Contains(line, "never-use-this-key") {
		t.Fatalf("subscription test used raw/no-request diagnostics: %s", line)
	}
}
