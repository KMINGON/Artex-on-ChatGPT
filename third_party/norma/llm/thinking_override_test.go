package llm

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureReq runs one arbitrary request and returns the raw + parsed request body.
func captureReq(t *testing.T, cfg Config, req CompletionRequest, stub string) (string, map[string]any) {
	t.Helper()
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		w.Write([]byte(stub))
	}))
	defer srv.Close()
	cfg.BaseURL, cfg.APIKey, cfg.Model = srv.URL, "k", "m"
	p, err := NewProvider(cfg)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	collect(t, p, req)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	return string(raw), m
}

func thinkingType(m map[string]any) string {
	if th, ok := m["thinking"].(map[string]any); ok {
		s, _ := th["type"].(string)
		return s
	}
	return ""
}

// Anthropic never receives a thinking switch or a replayed thinking block,
// whatever the per-request hint or the config says: current Claude models
// reject a forced mode, and a block is valid only in the unedited conversation
// that produced it.
func TestAnthropicNeverSendsThinkingSwitchOrBlocks(t *testing.T) {
	stub := sse(`data: {"type":"message_stop"}`, ``)
	for _, hint := range []string{"", "disabled", "enabled"} {
		req := CompletionRequest{
			Thinking: hint,
			Messages: []Message{
				UserText("hi"),
				{Role: RoleAssistant, Content: []ContentBlock{
					{Type: BlockThinking, Thinking: "SECRET_THOUGHT", Signature: "SIG"},
					TextBlock("answer"),
				}},
				UserText("next"),
			},
		}
		raw, m := captureReq(t, Config{Format: FormatAnthropic, ThinkingType: "enabled"}, req, stub)
		if _, ok := m["thinking"]; ok {
			t.Fatalf("hint %q: thinking switch sent: %v", hint, m["thinking"])
		}
		if strings.Contains(raw, "SECRET_THOUGHT") || strings.Contains(raw, "SIG") {
			t.Fatalf("hint %q: thinking block replayed: %s", hint, raw)
		}
		if !strings.Contains(raw, "answer") {
			t.Fatalf("hint %q: sibling text block lost: %s", hint, raw)
		}
	}
}

func TestOpenAIPerRequestThinkingOverride(t *testing.T) {
	// A non-empty stub: an empty completion would trip the empty-response retry
	// (this test only inspects the request body, so give it minimal content).
	stub := sse(`data: {"choices":[{"index":0,"delta":{"content":"ok"}}]}`, ``, `data: [DONE]`, ``)
	req := CompletionRequest{Thinking: "disabled", Messages: []Message{UserText("hi")}}
	_, m := captureReq(t, Config{Format: FormatOpenAI, ThinkingType: "enabled"}, req, stub)
	if thinkingType(m) != "disabled" {
		t.Fatalf("override should force disabled, got %q", thinkingType(m))
	}
	// Empty override inherits config.
	req2 := CompletionRequest{Messages: []Message{UserText("hi")}}
	_, m2 := captureReq(t, Config{Format: FormatOpenAI, ThinkingType: "enabled"}, req2, stub)
	if thinkingType(m2) != "enabled" {
		t.Fatalf("empty override should inherit config, got %q", thinkingType(m2))
	}
}
