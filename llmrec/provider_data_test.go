package llmrec

import (
	"context"
	"encoding/json"
	"iter"
	"reflect"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func TestRecordedRequestOmitsProviderDataWithoutChangingSource(t *testing.T) {
	req := llm.CompletionRequest{Messages: []llm.Message{{
		Role:         llm.RoleAssistant,
		ProviderData: json.RawMessage(`{"output":[{"type":"reasoning","encrypted_content":"PRIVATE_REPLAY"}]}`),
		Content:      []llm.ContentBlock{{Type: llm.BlockThinking, Thinking: "visible thinking summary", Signature: `artex-chatgpt-reasoning-v1:{"encrypted_content":"PRIVATE_REPLAY_LEGACY"}`}, llm.TextBlock("visible answer"), {Type: llm.BlockToolUse, ID: "call-one", Name: "read", Input: json.RawMessage(`{"path":"fixture"}`)}},
	}}}
	before, _ := json.Marshal(req)
	logged := (&Recorder{}).serializeRequest(req)
	if strings.Contains(logged, "PRIVATE_REPLAY") || strings.Contains(logged, "provider_data") || strings.Contains(logged, "encrypted_content") {
		t.Fatal("private provider replay state exposed in debug record")
	}
	var decoded struct {
		Messages []llm.Message `json:"messages"`
	}
	if err := json.Unmarshal([]byte(logged), &decoded); err != nil {
		t.Fatal(err)
	}
	want := req.Messages[0]
	want.ProviderData = nil
	want.Content = append([]llm.ContentBlock(nil), want.Content...)
	want.Content[0].Signature = ""
	if len(decoded.Messages) != 1 || !reflect.DeepEqual(decoded.Messages[0], want) {
		t.Fatal("logging removed or changed visible content")
	}
	after, _ := json.Marshal(req)
	if string(before) != string(after) {
		t.Fatal("logging erased live replay data")
	}
}

type replayProvider struct{ message llm.Message }

func (p replayProvider) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return p.message, "end_turn", llm.Usage{InputTokens: 2, OutputTokens: 3}, nil
}

func (p replayProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		if !yield(llm.StreamEvent{Type: llm.SEMessageFinal, FinalMessage: &p.message}, nil) {
			return
		}
		yield(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
	}
}

func TestRecorderPreservesProviderDataForInternalCallers(t *testing.T) {
	want := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("visible")}, ProviderData: json.RawMessage(`{"opaque":"PRIVATE_REPLAY"}`)}
	provider := Wrap(replayProvider{want}, nil, "model", "profile", "", "", func() bool { return false })
	got, _, usage, err := provider.Complete(t.Context(), llm.CompletionRequest{})
	if err != nil || !reflect.DeepEqual(got, want) || usage.OutputTokens != 3 {
		t.Fatal("Complete lost internal state or usage")
	}
	acc := llm.NewAccumulator()
	for ev, err := range provider.Stream(t.Context(), llm.CompletionRequest{}) {
		if err != nil {
			t.Fatal(err)
		}
		acc.Add(ev)
	}
	if !reflect.DeepEqual(acc.Message(), want) {
		t.Fatal("Stream lost authoritative final replay state")
	}
}
