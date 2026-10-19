package chatgptllm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/compaction"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fakeClient(t *testing.T, handler http.HandlerFunc) *http.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	local, _ := url.Parse(srv.URL)
	return &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.openai.com" {
			t.Errorf("unexpected production target: %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("authorization missing")
		}
		clone := r.Clone(r.Context())
		u := *r.URL
		u.Scheme, u.Host = local.Scheme, local.Host
		clone.URL = &u
		return http.DefaultTransport.RoundTrip(clone)
	})}
}

func auth(ctx context.Context) (context.Context, context.CancelFunc, string, error) {
	ctx, cancel := context.WithCancel(ctx)
	return ctx, cancel, "test-secret", nil
}

func testProvider(t *testing.T, client *http.Client) llm.Provider {
	t.Helper()
	p, err := New(Config{Model: "account-model", Authorize: auth, HTTPClient: client, MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func event(w io.Writer, value any) {
	encoded, _ := json.Marshal(value)
	fmt.Fprintf(w, "data: %s\n\n", encoded)
}

func completed(w io.Writer, items ...map[string]any) {
	event(w, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": items, "usage": map[string]any{"input_tokens": 20, "output_tokens": 7, "input_tokens_details": map[string]any{"cached_tokens": 5}}}})
}

func textItem(text string) map[string]any {
	return map[string]any{"type": "message", "id": "msg1", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}
}

func callItem(id, call, name, args string) map[string]any {
	return map[string]any{"type": "function_call", "id": id, "call_id": call, "name": name, "namespace": "artex", "arguments": args}
}

func TestNormaToolRoundTrip(t *testing.T) {
	for _, nonStreaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("complete=%v", nonStreaming), func(t *testing.T) {
			var calls, executions atomic.Int32
			var terminalSent atomic.Bool
			client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				var request map[string]json.RawMessage
				if json.NewDecoder(r.Body).Decode(&request) != nil {
					t.Error("bad request")
					return
				}
				if string(request["stream"]) != "true" || string(request["store"]) != "false" {
					t.Error("incorrect SIWC flags")
				}
				for _, key := range []string{"temperature", "max_output_tokens", "max_tokens", "previous_response_id", "thinking", "stop"} {
					if _, ok := request[key]; ok {
						t.Errorf("forbidden field %s", key)
					}
				}
				if calls.Add(1) == 1 {
					var namespaces []struct {
						Type, Name string
						Tools      []struct{ Type, Name string }
					}
					_ = json.Unmarshal(request["tools"], &namespaces)
					if len(namespaces) != 1 || namespaces[0].Type != "namespace" || namespaces[0].Name != "artex" {
						t.Errorf("namespace=%s", request["tools"])
						return
					}
					name := ""
					for _, fn := range namespaces[0].Tools {
						if fn.Name == wireToolName("probe.read") {
							name = fn.Name
						}
					}
					if name == "" {
						t.Error("mapped probe missing")
						return
					}
					a := callItem("item-a", "call-a", name, "")
					b := callItem("item-b", "call-b", name, "")
					event(w, map[string]any{"type": "response.output_item.added", "item": a})
					event(w, map[string]any{"type": "response.output_item.added", "item": b})
					// Interleaving would corrupt Norma's single current-tool accumulator.
					for _, d := range []struct{ id, text string }{{"item-a", `{"n":`}, {"item-b", `{"n":`}, {"item-a", `1}`}, {"item-b", `2}`}} {
						event(w, map[string]any{"type": "response.function_call_arguments.delta", "item_id": d.id, "delta": d.text})
					}
					w.(http.Flusher).Flush()
					if executions.Load() != 0 {
						t.Error("tool ran before completed")
					}
					terminalSent.Store(true)
					a["arguments"], b["arguments"] = `{"n":1}`, `{"n":2}`
					commentary := textItem("도구를 확인합니다")
					commentary["phase"] = "commentary"
					completed(w, map[string]any{"id": "rs0", "type": "reasoning", "summary": []any{}, "encrypted_content": "tool-state"}, commentary, a, b)
					return
				}
				var input []map[string]json.RawMessage
				_ = json.Unmarshal(request["input"], &input)
				resultIDs, callIDs := map[string]bool{}, map[string]bool{}
				for _, item := range input {
					var typ, id, output, args, namespace string
					_ = json.Unmarshal(item["type"], &typ)
					_ = json.Unmarshal(item["call_id"], &id)
					if typ == "function_call_output" {
						_ = json.Unmarshal(item["output"], &output)
						resultIDs[id] = strings.Contains(output, "result-"+id)
					}
					if typ == "function_call" {
						_ = json.Unmarshal(item["arguments"], &args)
						_ = json.Unmarshal(item["namespace"], &namespace)
						callIDs[id] = json.Valid([]byte(args)) && namespace == "artex"
					}
				}
				if !resultIDs["call-a"] || !resultIDs["call-b"] || !callIDs["call-a"] || !callIDs["call-b"] {
					t.Errorf("lost round trip: %s", request["input"])
				}
				if !strings.Contains(string(request["input"]), `"encrypted_content":"tool-state"`) || !strings.Contains(string(request["input"]), `"phase":"commentary"`) {
					t.Errorf("lost opaque reasoning/phase in tool round trip: %s", request["input"])
				}
				completed(w, textItem("완료"))
			})
			probe := tool.Build(tool.Spec{Name: "probe.read", Schema: map[string]any{"type": "object", "properties": map[string]any{"n": map[string]any{"type": "integer"}}, "required": []string{"n"}}, Run: func(_ context.Context, raw json.RawMessage, tc *tool.ToolContext) (tool.Result, error) {
				if !terminalSent.Load() {
					t.Error("side effect before response.completed")
				}
				var arg struct {
					N int `json:"n"`
				}
				_ = json.Unmarshal(raw, &arg)
				if arg.N != 1 && arg.N != 2 {
					t.Errorf("corrupt arguments: %s", raw)
				}
				executions.Add(1)
				return tool.Text("result-" + tc.ToolUseID), nil
			}})
			out, err := agentcore.Run(t.Context(), agentcore.Options{Provider: testProvider(t, client), Tools: []tool.CoreTool{probe}, PermissionMode: permission.ModeBypass, NonStreaming: nonStreaming, WorkingDir: t.TempDir(), MaxTurns: 3, MaxTokens: 17}, "검사")
			if err != nil || out != "완료" || executions.Load() != 2 || calls.Load() != 2 {
				t.Fatalf("out=%q err=%v executions=%d calls=%d", out, err, executions.Load(), calls.Load())
			}
		})
	}
}

func TestCompleteAndCompaction(t *testing.T) {
	client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "max_output_tokens") || !strings.Contains(string(body), `"stream":true`) {
			t.Errorf("wire=%s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		item := textItem("")
		event(w, map[string]any{"type": "response.output_item.added", "item": item})
		event(w, map[string]any{"type": "response.output_text.delta", "item_id": "msg1", "content_index": 0, "delta": "요약"})
		completed(w, textItem("요약"))
	})
	p := testProvider(t, client)
	msg, stop, usage, err := p.Complete(t.Context(), llm.CompletionRequest{MaxTokens: 1500, Messages: []llm.Message{llm.UserText("요약해 주세요")}})
	if err != nil || msg.Text() != "요약" || stop != "end_turn" || usage.InputTokens != 20 || usage.CacheReadTokens != 5 || usage.OutputTokens != 7 {
		t.Fatalf("message=%+v stop=%s usage=%+v err=%v", msg, stop, usage, err)
	}
	summary, err := compaction.ProviderSummarizer(p, 1500, true)(t.Context(), []llm.Message{llm.UserText("긴 기록")})
	if err != nil || summary != "요약" {
		t.Fatalf("summary=%q err=%v", summary, err)
	}
}

func TestTerminalFailuresAreAtomicAndNeverExecuteTools(t *testing.T) {
	for _, terminal := range []string{"failed", "incomplete", "eof", "malformed-tool"} {
		t.Run(terminal, func(t *testing.T) {
			var calls atomic.Int32
			client := fakeClient(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				event(w, map[string]any{"type": "response.output_item.added", "item": textItem("")})
				event(w, map[string]any{"type": "response.output_text.delta", "item_id": "msg1", "delta": "partial"})
				a := callItem("tool1", "call1", "Probe", "")
				event(w, map[string]any{"type": "response.output_item.added", "item": a})
				event(w, map[string]any{"type": "response.function_call_arguments.delta", "item_id": "tool1", "delta": `{}`})
				switch terminal {
				case "failed":
					event(w, map[string]any{"type": "response.failed", "response": map[string]any{"error": map[string]any{"code": "subscription_sharing_usage_limit_exceeded", "param": "model"}}})
				case "incomplete":
					event(w, map[string]any{"type": "response.incomplete", "response": map[string]any{"incomplete_details": map[string]any{"reason": "max_output_tokens"}}})
				case "malformed-tool":
					a["arguments"] = `{`
					completed(w, textItem("partial"), a)
				}
			})
			p, initErr := New(Config{Model: "account-model", Authorize: auth, HTTPClient: client, MaxRetries: 2, RetryInterval: time.Millisecond})
			if initErr != nil {
				t.Fatal(initErr)
			}
			req := llm.CompletionRequest{Tools: []llm.ToolSchema{{Name: "Probe"}}}
			var terminalErr error
			for ev, err := range p.Stream(t.Context(), req) {
				if ev.Type == llm.SEToolUseStart || ev.Type == llm.SEMessageStop {
					t.Errorf("committed after %s: %+v", terminal, ev)
				}
				if err != nil {
					terminalErr = err
				}
			}
			var apiErr *Error
			if !errors.As(terminalErr, &apiErr) || !apiErr.NoFallback() {
				t.Fatalf("missing protected error: %v", terminalErr)
			}
			msg, _, usage, err := p.Complete(t.Context(), req)
			if err == nil || len(msg.Content) != 0 || usage != (llm.Usage{}) || calls.Load() != 2 {
				t.Fatalf("non-atomic result: %+v %+v %v calls=%d", msg, usage, err, calls.Load())
			}
		})
	}
}

func TestRetryBoundariesAndDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		code         string
		status, want int
	}{{"subscription_sharing_usage_unavailable", 503, 3}, {"subscription_sharing_usage_limit_exceeded", 429, 1}, {"subscription_sharing_invalid_user", 401, 1}} {
		t.Run(tc.code, func(t *testing.T) {
			var calls, releases atomic.Int32
			client := fakeClient(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("X-Request-Id", "request-123")
				w.WriteHeader(tc.status)
				fmt.Fprintf(w, `{"error":{"code":%q,"param":"model","message":"diagnostic"}}`, tc.code)
			})
			p, _ := New(Config{Model: "model", HTTPClient: client, MaxRetries: 2, RetryInterval: time.Millisecond, Authorize: func(ctx context.Context) (context.Context, context.CancelFunc, string, error) {
				return ctx, func() { releases.Add(1) }, "test-secret", nil
			}})
			_, _, _, err := p.Complete(t.Context(), llm.CompletionRequest{})
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Code != tc.code || apiErr.RequestID != "request-123" || apiErr.Param != "model" || calls.Load() != int32(tc.want) || releases.Load() != int32(tc.want) {
				t.Fatalf("err=%+v calls=%d releases=%d", err, calls.Load(), releases.Load())
			}
			if strings.Contains(err.Error(), "test-secret") || strings.Contains(err.Error(), "diagnostic") {
				t.Fatal("unsafe printable diagnostics")
			}
		})
	}
}

func TestCancellationAndRedirect(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			cancel()
			<-r.Context().Done()
		})
		_, _, _, err := testProvider(t, client).Complete(ctx, llm.CompletionRequest{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("redirect", func(t *testing.T) {
		var calls atomic.Int32
		client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			http.Redirect(w, r, "https://untrusted.example/token", http.StatusTemporaryRedirect)
		})
		_, _, _, err := testProvider(t, client).Complete(t.Context(), llm.CompletionRequest{})
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.StatusCode != 307 || calls.Load() != 1 {
			t.Fatalf("err=%v calls=%d", err, calls.Load())
		}
	})
}

func TestCatalogAndUnsupportedInput(t *testing.T) {
	var calls atomic.Int32
	client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/models" {
			t.Errorf("path=%s", r.URL.Path)
		}
		io.WriteString(w, `{"models":[{"slug":"b","display_name":"B","visibility":"list","context_window":128000},{"slug":"hidden","visibility":"hidden"},{"slug":"a","display_name":"A","visibility":"list"}]}`)
	})
	models, err := Models(t.Context(), auth, client)
	if err != nil || len(models) != 2 || models[0].ID != "b" || models[1].ID != "a" || models[0].ContextWindow != 128000 {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	_, _, _, err = testProvider(t, client).Complete(t.Context(), llm.CompletionRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: "image"}}}}})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("unsupported block reached network: err=%v calls=%d", err, calls.Load())
	}
}

func TestReasoningAndPhaseReplayAfterTranscriptReload(t *testing.T) {
	answer := textItem("answer")
	answer["phase"] = "final_answer"
	commentary := textItem("checking")
	commentary["id"], commentary["phase"] = "comment1", "commentary"
	items := []map[string]any{
		{"id": "rs1", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "실제 요약"}}, "encrypted_content": "opaque"},
		commentary,
		{"id": "rs2", "type": "reasoning", "summary": []any{}, "encrypted_content": "no-summary"},
		answer,
	}
	p := testProvider(t, fakeClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		completed(w, items...)
	}))
	msg, _, _, err := p.Complete(t.Context(), llm.CompletionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Content) != 3 || msg.Content[0].Thinking != "실제 요약" || msg.Content[0].Signature != "" || len(msg.ProviderData) == 0 {
		t.Fatalf("message=%+v", msg)
	}
	store := transcript.NewStore(t.TempDir())
	path := store.MainPath("test")
	if err := store.Append(path, transcript.Record{Type: "message", Message: &msg}); err != nil {
		t.Fatal(err)
	}
	records, err := store.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	messages, _ := transcript.Messages(records)
	body, _, err := encodeRequest(Config{Model: "m"}, llm.CompletionRequest{Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(body, &request); err != nil || !reflect.DeepEqual(request.Input, items) {
		t.Fatalf("body=%s err=%v", body, err)
	}
	// A changed visible message must never replay stale hidden calls or state.
	messages[0].Content[1].Text = "edited"
	if _, _, err := encodeRequest(Config{Model: "m"}, llm.CompletionRequest{Messages: messages}); err == nil {
		t.Fatal("changed message replayed old provider state")
	}
}

func TestOverflowKeepsNormaRecoverySignal(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"context_length_exceeded","message":"private diagnostic"}}`,
		`{"error":{"message":"Maximum context length exceeded"}}`,
	} {
		err := responseFailure([]byte(body))
		if !compaction.IsOverflow(err) {
			t.Fatalf("overflow was hidden from Norma: %v", err)
		}
		if strings.Contains(err.Error(), "private diagnostic") {
			t.Fatal("upstream message exposed")
		}
	}
}

func TestSummaryRequestDoesNotInheritExpensiveReasoning(t *testing.T) {
	var bodies []map[string]json.RawMessage
	client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "text/event-stream")
		completed(w, textItem("summary"))
	})
	p, _ := New(Config{Model: "m", HTTPClient: client, Authorize: auth, ReasoningEffort: "high", MaxRetries: -1})
	_, err := compaction.ProviderSummarizer(p, 1500, true)(t.Context(), []llm.Message{llm.UserText("history")})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = p.Complete(t.Context(), llm.CompletionRequest{Messages: []llm.Message{llm.UserText("normal")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0]["reasoning"] != nil || !strings.Contains(string(bodies[1]["reasoning"]), `"effort":"high"`) {
		t.Fatalf("reasoning policy: %+v", bodies)
	}
}
