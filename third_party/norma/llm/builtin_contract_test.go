package llm

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// streamErr drains a stream and returns its terminal error.
func streamErr(t *testing.T, cfg Config, body string) (Message, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer srv.Close()
	cfg.BaseURL, cfg.Model, cfg.MaxRetries, cfg.EmptyResponseRetries = srv.URL, "m", -1, -1
	p, err := NewProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	acc := NewAccumulator()
	for ev, err := range p.Stream(context.Background(), CompletionRequest{Messages: []Message{UserText("go")}}) {
		if err != nil {
			return acc.Message(), err
		}
		acc.Add(ev)
	}
	return acc.Message(), nil
}

// HTTP 200 is not completion: a cut stream is an error, not a short reply.
func TestBuiltinStreamsRequireCompletionSignal(t *testing.T) {
	var incomplete *IncompleteStreamError
	cut := sse(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`, ``)
	if _, err := streamErr(t, Config{Format: FormatAnthropic, APIKey: "k"}, cut); !errors.As(err, &incomplete) {
		t.Fatalf("anthropic cut stream: %v", err)
	}
	cut = sse(`data: {"choices":[{"delta":{"content":"partial"}}]}`, ``)
	if _, err := streamErr(t, Config{Format: FormatOpenAI}, cut); !errors.As(err, &incomplete) {
		t.Fatalf("openai cut stream: %v", err)
	}
	done := sse(`data: {"choices":[{"delta":{"content":"whole"},"finish_reason":"stop"}]}`, ``, `data: [DONE]`, ``)
	if msg, err := streamErr(t, Config{Format: FormatOpenAI}, done); err != nil || msg.Text() != "whole" {
		t.Fatalf("finished stream: %q %v", msg.Text(), err)
	}
}

// A refusal or an error sent inside a 200 stream reaches the caller with its
// reason; a refusal is never replayed or moved to another provider.
func TestBuiltinStreamsSurfaceRefusalsAndErrorFrames(t *testing.T) {
	refused := sse(
		`data: {"type":"message_start","message":{"usage":{"input_tokens":5}}}`, ``,
		`data: {"type":"message_delta","delta":{"stop_reason":"refusal","stop_details":{"type":"refusal","category":"cyber","explanation":"declined"}},"usage":{"output_tokens":0}}`, ``,
		`data: {"type":"message_stop"}`, ``)
	_, err := streamErr(t, Config{Format: FormatAnthropic, APIKey: "k"}, refused)
	var refusal *RefusalError
	if !errors.As(err, &refusal) || refusal.Category != "cyber" || !refusal.NoReplay() || !refusal.NoFallback() {
		t.Fatalf("refusal: %v", err)
	}
	frame := sse(`data: {"error":{"message":"model qwen3 not found"}}`, ``)
	if _, err := streamErr(t, Config{Format: FormatOpenAI}, frame); err == nil || !strings.Contains(err.Error(), "model qwen3 not found") {
		t.Fatalf("error frame: %v", err)
	}
}

func TestBuiltinStatusErrorKeepsReadableMessageAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	defer srv.Close()
	p, _ := NewProvider(Config{Format: FormatAnthropic, BaseURL: srv.URL, APIKey: "k", Model: "m", MaxRetries: -1})
	_, _, _, err := p.Complete(context.Background(), CompletionRequest{Messages: []Message{UserText("go")}})
	var status *StatusError
	if !errors.As(err, &status) || status.StatusCode != 401 || status.Message != "invalid x-api-key" || !strings.Contains(err.Error(), "status 401") {
		t.Fatalf("status error: %v", err)
	}
}

// Credentials and addresses are explicit: no key is read from the environment
// and a local server without a key receives no Authorization header.
func TestBuiltinProvidersUseOnlyConfiguredCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-anthropic")
	t.Setenv("OPENAI_API_KEY", "env-openai")
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:1/v1")
	if _, err := NewProvider(Config{Format: FormatAnthropic, Model: "m"}); err == nil {
		t.Fatal("anthropic provider built from an environment key")
	}
	if _, err := NewProvider(Config{Format: FormatOpenAI, Model: "m"}); err == nil {
		t.Fatal("openai-compatible provider built from an environment address")
	}
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	for _, key := range []string{"", "local-key"} {
		p, err := NewProvider(Config{Format: FormatOpenAI, BaseURL: srv.URL, APIKey: key, Model: "m"})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := p.Complete(context.Background(), CompletionRequest{Messages: []Message{UserText("go")}}); err != nil {
			t.Fatal(err)
		}
	}
	if len(auth) != 2 || auth[0] != "" || auth[1] != "Bearer local-key" {
		t.Fatalf("authorization headers: %q", auth)
	}
}

// Anthropic receives the image bytes; the text-only Chat Completions format
// receives a description instead of silently dropping the image.
func TestBuiltinProvidersDeliverImages(t *testing.T) {
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=")
	img, err := NewImage(png, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	history := []Message{{Role: RoleUser, Content: []ContentBlock{
		TextBlock("look"),
		{Type: BlockToolResult, ToolUseID: "t1", Content: []ContentBlock{TextBlock("shot"), img}},
	}}}
	anthropic := toAnthropicMessages(history)
	blocks := anthropic[0].Content
	nested, _ := blocks[1]["content"].([]map[string]any)
	if len(nested) != 2 || nested[1]["type"] != "image" {
		t.Fatalf("anthropic image: %+v", blocks)
	}
	chat := toOpenAIMessages("", history)
	if len(chat) != 2 || chat[0].Role != "tool" || !strings.Contains(chat[0].Content, "image/png") {
		t.Fatalf("chat completions image: %+v", chat)
	}
}
