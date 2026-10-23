package chatgptllm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
)

func TestDaybreakOptionsAndObservedSelection(t *testing.T) {
	for _, cyber := range []string{"", "standard", "daybreak_blue", "daybreak_red"} {
		t.Run(cyber, func(t *testing.T) {
			client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				var b map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
					t.Fatal(err)
				}
				if cyber == "" {
					if b["access_programs"] != nil {
						t.Fatal("omitted option changed server default")
					}
				} else {
					var a map[string]string
					_ = json.Unmarshal(b["access_programs"], &a)
					if a["cyber"] != cyber {
						t.Fatalf("wire access=%v", a)
					}
				}
				event(w, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "model": "resolved-model", "access_programs": map[string]string{"cyber": "daybreak_blue"}, "output": []any{textItem("ok")}}})
			})
			p, err := New(Config{Model: "unknown-model", APIKey: "test-secret", AccessProgramCyber: cyber, HTTPClient: client, MaxRetries: -1})
			if err != nil {
				t.Fatal(err)
			}
			var metadata ResponseMetadata
			seen := 0
			ctx := WithResponseObserver(t.Context(), func(m ResponseMetadata) { metadata = m; seen++ })
			if _, _, _, err = p.Complete(ctx, llm.CompletionRequest{}); err != nil {
				t.Fatal(err)
			}
			if seen != 1 || metadata.Model != "resolved-model" || metadata.AccessProgramCyber != "daybreak_blue" || !metadata.AccessProgramsPresent || metadata.UsageReceived {
				t.Fatalf("metadata %+v count=%d", metadata, seen)
			}
		})
	}
	for _, model := range []string{"gpt-6.1-sol", "gpt-6-astra", "gpt-6-sol"} {
		if contains(Capabilities(model, "api_key").AccessProgramsCyber, "daybreak_red") {
			t.Errorf("API documentation incorrectly advertises Red for %s", model)
		}
	}
	if _, err := New(Config{Model: "catalog-model", Authorize: auth, AccessProgramCyber: "daybreak_blue"}); err == nil {
		t.Fatal("unverified subscription option accepted")
	}
}

func TestSIWCCatalogDaybreakModelPassesWithoutAccessOption(t *testing.T) {
	client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `{"models":[{"slug":"account-daybreak-model","display_name":"Account Daybreak","visibility":"list"}]}`)
			return
		}
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		if string(body["model"]) != `"account-daybreak-model"` || body["access_programs"] != nil {
			t.Errorf("catalog selection changed: %s", body)
		}
		completed(w, textItem("ok"))
	})
	models, err := Models(t.Context(), auth, client)
	if err != nil || len(models) != 1 {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	if models[0].Capabilities.DaybreakAvailable != nil {
		t.Fatal("catalog ID falsely asserted explicit-option support")
	}
	p, err := New(Config{Model: models[0].ID, Authorize: auth, HTTPClient: client, MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = p.Complete(t.Context(), llm.CompletionRequest{}); err != nil {
		t.Fatal(err)
	}
}

func TestFailedUsagePreservedAtomicallyAndRetriesCountEachReportOnce(t *testing.T) {
	for _, terminal := range []string{"response.failed", "response.incomplete"} {
		for _, withUsage := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/usage%v/stream%v", terminal, withUsage, stream), func(t *testing.T) {
					client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "text/event-stream")
						response := map[string]any{"status": "failed", "error": map[string]any{"code": "unknown_failure"}, "output": []any{callItem("f1", "call1", "probe", "{}")}, "incomplete_details": map[string]any{"reason": "max_output_tokens"}}
						if withUsage {
							response["usage"] = map[string]any{"input_tokens": 123, "output_tokens": 40, "input_tokens_details": map[string]int{"cached_tokens": 8}}
						}
						event(w, map[string]any{"type": terminal, "response": response})
					})
					p := testProvider(t, client)
					req := llm.CompletionRequest{Tools: []llm.ToolSchema{{Name: "probe"}}}
					var usage llm.Usage
					var err error
					var msg llm.Message
					var meta ResponseMetadata
					ctx := WithResponseObserver(t.Context(), func(m ResponseMetadata) { meta = m })
					if stream {
						for ev, e := range p.Stream(ctx, req) {
							if ev.Type == llm.SEToolUseStart || ev.Type == llm.SEMessageFinal || ev.Type == llm.SEMessageStop {
								t.Fatal("failed output committed")
							}
							usage.Add(ev.Usage)
							if e != nil {
								err = e
							}
						}
					} else {
						msg, _, usage, err = p.Complete(ctx, req)
					}
					var apiErr *Error
					if !errors.As(err, &apiErr) || len(msg.Content) != 0 || meta.UsageReceived != withUsage || apiErr.Diagnostics()["usage_received"] != withUsage {
						t.Fatalf("msg=%+v meta=%+v err=%v", msg, meta, err)
					}
					expected := llm.Usage{}
					if withUsage {
						expected = llm.Usage{InputTokens: 123, OutputTokens: 40, CacheReadTokens: 8}
					}
					if usage != expected {
						t.Fatalf("usage=%+v want=%+v", usage, expected)
					}
				})
			}
		}
	}
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprintf("retry/complete%v", complete), func(t *testing.T) {
			attempts := 0
			client := fakeClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				attempts++
				if attempts == 1 {
					event(w, map[string]any{"type": "response.failed", "response": map[string]any{"error": map[string]string{"code": "server_error"}, "usage": map[string]int{"input_tokens": 3, "output_tokens": 2}}})
					return
				}
				completed(w, textItem("ok"))
			})
			p, err := New(Config{Model: "mock", APIKey: "test-secret", HTTPClient: client, MaxRetries: 1, RetryInterval: time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			var usage llm.Usage
			if complete {
				_, _, usage, err = p.Complete(t.Context(), llm.CompletionRequest{})
			} else {
				for ev, e := range p.Stream(t.Context(), llm.CompletionRequest{}) {
					usage.Add(ev.Usage)
					if e != nil {
						err = e
					}
				}
			}
			if err != nil || attempts != 2 || usage.InputTokens != 23 || usage.OutputTokens != 9 || usage.CacheReadTokens != 5 {
				t.Fatalf("attempts=%d usage=%+v err=%v", attempts, usage, err)
			}
		})
	}
}

func TestResponseMetadataNullAbsentAndZeroUsage(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		present bool
		program string
	}{
		{`{}`, false, ""},
		{`{"access_programs":null}`, true, "standard"},
		{`{"access_programs":{}}`, true, ""},
		{`{"access_programs":{"cyber":"standard"}}`, true, "standard"},
		{`{"access_programs":{"cyber":"daybreak_blue"}}`, true, "daybreak_blue"},
		{`{"usage":{"input_tokens":0,"output_tokens":0}}`, false, ""},
	} {
		var r response
		if err := json.Unmarshal([]byte(tc.raw), &r); err != nil {
			t.Fatal(err)
		}
		meta := r.metadata()
		if meta.AccessProgramsPresent != tc.present || meta.AccessProgramCyber != tc.program || meta.UsageReceived != (r.Usage != nil) {
			t.Fatalf("%s => %+v", tc.raw, meta)
		}
	}
	calls := 0
	ctx := WithResponseObserver(context.Background(), func(ResponseMetadata) { calls++ })
	ctx = WithResponseObserver(ctx, func(ResponseMetadata) { calls++ })
	observeResponse(ctx, ResponseMetadata{})
	if calls != 2 {
		t.Fatal("nested observer lost")
	}
}

func TestDocumentedReasoningOptionsPreserveDefaults(t *testing.T) {
	for _, model := range []string{"gpt-6.1-sol", "gpt-6-astra", "gpt-6-sol", "gpt-6-luna"} {
		c := Capabilities(model, "api_key")
		if !c.Known || c.CheckedAt != capabilityCheckedAt {
			t.Fatalf("capability snapshot %+v", c)
		}
		for _, effort := range append([]string{""}, c.ReasoningEfforts...) {
			if err := ValidateOptions(model, "api_key", effort, ""); err != nil {
				t.Fatalf("model=%s effort=%s err=%v", model, effort, err)
			}
		}
	}
	for _, model := range []string{"gpt-6.1-sol", "gpt-6-astra"} {
		for _, effort := range []string{"none", "minimal"} {
			if contains(Capabilities(model, "api_key").ReasoningEfforts, effort) {
				t.Fatalf("API documentation incorrectly advertises effort %s on %s", effort, model)
			}
		}
	}
	if c := Capabilities("future-model", "api_key"); c.Known {
		t.Fatal("unknown model inferred as known")
	}
	if err := ValidateOptions("future-model", "api_key", "none", "daybreak_red"); err != nil {
		t.Fatal("unknown model should be remotely validated", err)
	}
}

func TestQuotaErrorHookRunsBeforeConsumerStopsOnTerminalUsage(t *testing.T) {
	client := fakeClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		event(w, map[string]any{"type": "response.failed", "response": map[string]any{"error": map[string]string{"code": "subscription_sharing_usage_limit_exceeded"}, "usage": map[string]int{"input_tokens": 4, "output_tokens": 0}}})
	})
	observed := 0
	p, err := New(Config{Model: "mock", Authorize: auth, HTTPClient: client, OnError: func(e *Error) {
		if e.Code == "subscription_sharing_usage_limit_exceeded" {
			observed++
		}
	}, MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	for ev, err := range p.Stream(t.Context(), llm.CompletionRequest{}) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Type == llm.SEMessageDelta {
			break
		}
	}
	if observed != 1 {
		t.Fatalf("quota observation lost at consumer exit: %d", observed)
	}
}

func TestCatalogAndUnknownModelEffortsAreNotRestrictedByAPISnapshot(t *testing.T) {
	for _, cfg := range []Config{{Model: "gpt-daybreak-blue-latest", Authorize: auth, ReasoningEffort: "ultra"}, {Model: "gpt-6.1-sol", Authorize: auth, ReasoningEffort: "ultra"}, {Model: "future-model", APIKey: "test-secret", ReasoningEffort: "ultra"}, {Model: "gpt-6.1-sol", APIKey: "test-secret", ReasoningEffort: "ultra"}} {
		t.Run(cfg.Model+authMode(cfg.APIKey), func(t *testing.T) {
			cfg.HTTPClient = fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				var body struct {
					Reasoning struct {
						Effort string `json:"effort"`
					} `json:"reasoning"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.Reasoning.Effort != "ultra" {
					t.Fatalf("effort lost: %s", body.Reasoning.Effort)
				}
				completed(w, textItem("ok"))
			})
			cfg.MaxRetries = -1
			p, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, _, err = p.Complete(t.Context(), llm.CompletionRequest{}); err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := ValidateOptions("gpt-6.1-sol", "api_key", "ultra", "daybreak_red"); err != nil {
		t.Fatal("static API documentation blocked account-specific options", err)
	}
}
