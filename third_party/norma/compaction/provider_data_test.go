package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

type noFallbackTestError struct{}

func (*noFallbackTestError) Error() string    { return "provider requires user action" }
func (*noFallbackTestError) NoFallback() bool { return true }

func TestSummarizerDoesNotRetryNoFallbackError(t *testing.T) {
	for _, nonStreaming := range []bool{false, true} {
		t.Run(fmt.Sprint(nonStreaming), func(t *testing.T) {
			original := &noFallbackTestError{}
			p := &captureProvider{failN: 10, failErr: fmt.Errorf("wrapped: %w", original)}
			_, err := ProviderSummarizer(p, 0, nonStreaming)(context.Background(), []llm.Message{llm.UserText("summarize")})
			if !errors.Is(err, original) || p.calls != 1 {
				t.Fatalf("ineligible request replayed: calls=%d error=%v", p.calls, err)
			}
		})
	}
}

func TestSummaryReplayPreservesOnlyUnmodifiedProviderData(t *testing.T) {
	data := json.RawMessage(`{"opaque":"state"}`)
	message := llm.Message{Role: llm.RoleAssistant, ProviderData: data, Content: []llm.ContentBlock{
		llm.TextBlock("keep"), {Type: llm.BlockToolUse, ID: "call-1", Name: "Read", Input: json.RawMessage(`{}`)},
	}}
	result := llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText("call-1", "value", false)}}
	opaque := llm.Message{Role: llm.RoleAssistant, ProviderData: data}
	got := pairForReplay([]llm.Message{opaque, message, result})
	if len(got) != 3 || string(got[0].ProviderData) != string(data) || string(got[1].ProviderData) != string(data) {
		t.Fatalf("unchanged replay lost metadata: %#v", got)
	}
	got = pairForReplay([]llm.Message{message})
	if len(got) != 1 || got[0].Text() != "keep" || len(got[0].ProviderData) != 0 {
		t.Fatalf("orphaned tool can return via metadata: %#v", got)
	}
	if len(message.ProviderData) == 0 || len(message.Content) != 2 {
		t.Fatal("summary projection changed original history")
	}
}
