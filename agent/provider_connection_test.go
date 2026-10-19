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
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/chatgptllm"
)

func TestConnectionUsesOneToolFreeRequestInBothModes(t *testing.T) {
	for _, streaming := range []bool{true, false} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var req map[string]any
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if _, exists := req["tools"]; exists {
					t.Errorf("connection test exposed tools: %#v", req["tools"])
				}
				if req["stream"] != streaming || req["reasoning_effort"] != "high" || req["model"] != "test-model" || req["max_tokens"] != float64(8192) {
					t.Errorf("configured request mode lost: %#v", req)
				}
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				} else {
					_, _ = fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
				}
			}))
			defer srv.Close()
			cfg := ConfigFrom("openai", "test-model", srv.URL, "test-placeholder", "")
			cfg.Stream, cfg.ReasoningEffort = streaming, "high"
			_, reply, err := TestConnection(context.Background(), cfg)
			if err != nil || reply != "OK" || calls.Load() != 1 {
				t.Fatalf("reply=%q err=%v HTTP calls=%d", reply, err, calls.Load())
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
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
				"id": "unexpected", "type": "function", "function": map[string]any{"name": "Write", "arguments": string(args)},
			}}}, "finish_reason": "tool_calls",
		}}})
	}))
	defer srv.Close()
	cfg := ConfigFrom("openai", "test-model", srv.URL, "test-placeholder", "")
	cfg.Stream = false
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
				w.WriteHeader(status)
				_, _ = fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`)
			}))
			defer srv.Close()
			cfg := ConfigFrom("openai", "test-model", srv.URL, "test-placeholder", "")
			cfg.Stream = false
			cfg.Retry.ConnectAttempts, cfg.Retry.EmptyAttempts = 4, 4
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
	cfg := ConfigFrom("openai", "test-model", srv.URL, "test-placeholder", "")
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
	_, _, err := TestConnection(context.Background(), Config{AuthMode: "chatgpt_plan", Model: "test", APIKey: "never-use-this-key", Stream: true})
	if err == nil {
		t.Fatal("signed-out account unexpectedly succeeded")
	}
	line = output.String()
	if !strings.Contains(line, `code="authorization_failed"`) || strings.Contains(line, "未发出任何 HTTP") || strings.Contains(line, "never-use-this-key") {
		t.Fatalf("subscription test used raw/no-request diagnostics: %s", line)
	}
}
