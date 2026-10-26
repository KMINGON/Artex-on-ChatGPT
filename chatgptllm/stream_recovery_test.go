package chatgptllm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
)

func TestInterruptedStreamRetriesOnlyUnexposedOutput(t *testing.T) {
	for _, api := range []bool{false, true} {
		for _, tc := range []struct {
			name  string
			write func(http.ResponseWriter)
		}{
			{"empty EOF", func(http.ResponseWriter) {}},
			{"item added", func(w http.ResponseWriter) { indexedItem(w, "added", 0, textItem("")) }},
			{"empty text delta", func(w http.ResponseWriter) {
				indexedItem(w, "added", 0, textItem(""))
				event(w, map[string]any{"type": "response.output_text.delta", "item_id": "msg1", "delta": ""})
			}},
			{"partial private tool arguments", func(w http.ResponseWriter) {
				indexedItem(w, "added", 0, callItem("discarded", "discarded-call", "Probe", ""))
				event(w, map[string]any{"type": "response.function_call_arguments.delta", "item_id": "discarded", "delta": "{\"n\":"})
			}},
			{"private tool done without completed", func(w http.ResponseWriter) {
				item := callItem("discarded", "discarded-call", "Probe", "{}")
				indexedItem(w, "added", 0, item)
				indexedItem(w, "done", 0, item)
			}},
			{"private text done without completed", func(w http.ResponseWriter) {
				indexedItem(w, "added", 0, textItem(""))
				indexedItem(w, "done", 0, textItem("discarded"))
			}},
			{"body read interrupted", func(w http.ResponseWriter) {
				// A truncated HTTP body makes the transport reader fail after a
				// valid non-output event, like the observed item.added failure.
				w.Header().Set("Content-Length", "1000")
				indexedItem(w, "added", 0, textItem(""))
			}},
		} {
			t.Run(fmt.Sprintf("api=%v/%s", api, tc.name), func(t *testing.T) {
				var calls atomic.Int32
				requests := make(chan string, 2)
				client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					requests <- string(body)
					w.Header().Set("Content-Type", "text/event-stream")
					if calls.Add(1) == 1 {
						tc.write(w)
						return
					}
					completed(w, callItem("final", "final-call", "Probe", "{}"))
				})
				var observed *Error
				cfg := Config{Model: "account-model", Authorize: auth, HTTPClient: client, MaxRetries: 1, RetryInterval: time.Millisecond,
					OnError: func(e *Error) { observed = e }}
				if api {
					cfg.APIKey, cfg.Authorize = "test-secret", nil
				}
				p, err := New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				var tools, finals int
				for ev, err := range p.Stream(t.Context(), llm.CompletionRequest{Tools: []llm.ToolSchema{{Name: "Probe"}}}) {
					if err != nil {
						t.Fatal(err)
					}
					if ev.Type == llm.SEToolUseStart {
						tools++
						if ev.ToolID != "final-call" {
							t.Fatal("uncommitted tool escaped from interrupted attempt")
						}
					}
					if ev.Type == llm.SEMessageFinal {
						finals++
					}
				}
				if calls.Load() != 2 || tools != 1 || finals != 1 {
					t.Fatalf("calls=%d tools=%d finals=%d", calls.Load(), tools, finals)
				}
				if first, second := <-requests, <-requests; first != second {
					t.Fatal("recovery changed the current request body")
				}
				if observed == nil || observed.Code != "interrupted_stream" || observed.OutputEmitted || observed.Attempts != 1 || !observed.NoReplay() || observed.RetryableSameAuth() {
					t.Fatal("recovery lost the provider-owned retry boundary")
				}
			})
		}
	}
}

func TestInterruptedStreamDoesNotRetryVisibleOrRejectedOutput(t *testing.T) {
	for _, api := range []bool{false, true} {
		for _, tc := range []struct {
			name, code string
			write      func(io.Writer)
			emitted    bool
		}{
			{"text delta", "interrupted_stream", func(w io.Writer) {
				indexedItem(w, "added", 0, textItem(""))
				event(w, map[string]any{"type": "response.output_text.delta", "item_id": "msg1", "delta": "visible"})
			}, true},
			{"refusal delta", "interrupted_stream", func(w io.Writer) {
				indexedItem(w, "added", 0, textItem(""))
				event(w, map[string]any{"type": "response.refusal.delta", "item_id": "msg1", "delta": "visible"})
			}, true},
			{"DONE without completed", "interrupted_stream", func(w io.Writer) { fmt.Fprint(w, "data: [DONE]\n\n") }, false},
			{"malformed JSON", "invalid_stream", func(w io.Writer) { fmt.Fprint(w, "data: {\n\n") }, false},
			{"invalid completion", "invalid_completion", func(w io.Writer) {
				event(w, map[string]any{"type": "response.completed", "response": map[string]any{"status": "incomplete"}})
			}, false},
			{"explicit interrupted code", "interrupted_stream", func(w io.Writer) {
				event(w, map[string]any{"type": "error", "code": "interrupted_stream"})
			}, false},
		} {
			t.Run(fmt.Sprintf("api=%v/%s", api, tc.name), func(t *testing.T) {
				var calls atomic.Int32
				client := fakeClient(t, func(w http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					tc.write(w)
				})
				cfg := Config{Model: "account-model", Authorize: auth, HTTPClient: client, MaxRetries: 2, RetryInterval: time.Millisecond}
				if api {
					cfg.APIKey, cfg.Authorize = "test-secret", nil
				}
				p, err := New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				var terminal error
				for ev, err := range p.Stream(t.Context(), llm.CompletionRequest{}) {
					if ev.Type == llm.SEToolUseStart || ev.Type == llm.SEMessageFinal {
						t.Fatal("incomplete result was committed")
					}
					if err != nil {
						terminal = err
					}
				}
				var e *Error
				if !errors.As(terminal, &e) || e.Code != tc.code || e.OutputEmitted != tc.emitted || calls.Load() != 1 || !e.NoReplay() {
					t.Fatalf("err=%v calls=%d", terminal, calls.Load())
				}
			})
		}
	}
}

func TestInterruptedStreamSharesBudgetAndCancellation(t *testing.T) {
	for _, cancelBackoff := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%v", cancelBackoff), func(t *testing.T) {
			var calls atomic.Int32
			client := fakeClient(t, func(w http.ResponseWriter, _ *http.Request) {
				if calls.Add(1) == 1 && !cancelBackoff {
					w.WriteHeader(http.StatusServiceUnavailable)
					fmt.Fprint(w, "{\"error\":{\"code\":\"server_error\"}}")
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
			})
			cfg := Config{Model: "account-model", Authorize: auth, HTTPClient: client, MaxRetries: 2, RetryInterval: time.Millisecond}
			ctx := t.Context()
			if cancelBackoff {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 25*time.Millisecond)
				defer cancel()
				cfg.RetryInterval = time.Second
			}
			p, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			_, _, _, err = p.Complete(ctx, llm.CompletionRequest{})
			var e *Error
			want := int32(3)
			if cancelBackoff {
				want = 1
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("backoff cancellation lost: %v", err)
				}
			}
			if !errors.As(err, &e) || calls.Load() != want || e.Attempts != int(want) || e.OutputEmitted || !e.NoReplay() {
				t.Fatalf("budget escaped: err=%v calls=%d", err, calls.Load())
			}
		})
	}
}

func TestOversizedStreamLineIsNotATransientInterruption(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("data: " + strings.Repeat("x", 16<<20)))}
	err := consume(resp, nil, func(llm.StreamEvent, error) bool { return true })
	var e *Error
	if !errors.As(err, &e) || e.Code != "response_too_large" || !errors.Is(err, bufio.ErrTooLong) || retryable(e) {
		t.Fatalf("local size limit became retryable: %v", err)
	}
}

func TestInterruptedContinuationDoesNotReplayExecutedTool(t *testing.T) {
	for _, api := range []bool{false, true} {
		for _, nonStreaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("api=%v/complete=%v", api, nonStreaming), func(t *testing.T) {
				var requests, executions atomic.Int32
				bodies := make(chan string, 2)
				client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					switch requests.Add(1) {
					case 1:
						completed(w, callItem("probe-item", "probe-call", "Probe", "{}"))
					case 2:
						bodies <- string(body)
						indexedItem(w, "added", 0, textItem(""))
					case 3:
						bodies <- string(body)
						completed(w, textItem("완료"))
					default:
						t.Error("recovery replayed an entire run")
					}
				})
				cfg := Config{Model: "account-model", Authorize: auth, HTTPClient: client, MaxRetries: 1, RetryInterval: time.Millisecond}
				if api {
					cfg.APIKey, cfg.Authorize = "test-secret", nil
				}
				p, err := New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				probe := tool.Build(tool.Spec{Name: "Probe", Schema: map[string]any{"type": "object"}, Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
					executions.Add(1)
					return tool.Text("previous-tool-result"), nil
				}})
				dir := t.TempDir()
				out, err := agentcore.Run(t.Context(), agentcore.Options{Provider: p, Tools: []tool.CoreTool{probe}, PermissionMode: permission.ModeBypass,
					NonStreaming: nonStreaming, WorkingDir: dir, ToolOutputDir: filepath.Join(dir, "cmd-output"), MaxTurns: 3}, "검증 결과를 이어서 정리하세요")
				if err != nil || out != "완료" || requests.Load() != 3 || executions.Load() != 1 {
					t.Fatalf("out=%q err=%v requests=%d executions=%d", out, err, requests.Load(), executions.Load())
				}
				if interrupted, retried := <-bodies, <-bodies; interrupted != retried || !strings.Contains(retried, "previous-tool-result") {
					t.Fatal("current continuation or prior tool result changed during retry")
				}
			})
		}
	}
}
