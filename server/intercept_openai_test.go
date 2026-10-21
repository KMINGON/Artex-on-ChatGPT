package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/chatgptllm"
)

type judgeOpenAITransport func(*http.Request) (*http.Response, error)

func (f judgeOpenAITransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestJudgeOpenAIRequestOmitsTemperature(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-6.1-sol"} {
		t.Run(model, func(t *testing.T) {
			requests := 0
			client := &http.Client{Transport: judgeOpenAITransport(func(r *http.Request) (*http.Response, error) {
				requests++
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body["temperature"] != nil || body["top_p"] != nil || body["reasoning"] != nil {
					t.Fatalf("Judge changed model-default sampling/reasoning: %s", body)
				}
				if string(body["max_output_tokens"]) != "1024" || string(body["store"]) != "false" || string(body["stream"]) != "true" {
					t.Fatalf("Judge wire contract: %s", body)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":3,"output_tokens":1}}}` + "\n\n"))}, nil
			})}
			p, err := chatgptllm.New(chatgptllm.Config{Model: model, APIKey: "mock-key", HTTPClient: client, MaxRetries: -1})
			if err != nil {
				t.Fatal(err)
			}
			got, err := streamCollectText(t.Context(), p, "Judge policy", "check tool")
			if err != nil || got != "ok" || requests != 1 {
				t.Fatalf("got=%q err=%v requests=%d", got, err, requests)
			}
		})
	}
}
