package agentcore

import (
	"context"
	"encoding/json"
	"iter"
	"testing"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/transcript"
)

type finalMessageProvider struct {
	requests []llm.CompletionRequest
	message  llm.Message
}

func (p *finalMessageProvider) Stream(_ context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	p.requests = append(p.requests, req)
	return func(yield func(llm.StreamEvent, error) bool) {
		for _, ev := range []llm.StreamEvent{
			{Type: llm.SETextDelta, Text: "answer"},
			{Type: llm.SEMessageFinal, FinalMessage: &p.message},
			{Type: llm.SEMessageDelta, StopReason: "end_turn", Usage: llm.Usage{InputTokens: 4, OutputTokens: 3}},
			{Type: llm.SEMessageStop},
		} {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

func (p *finalMessageProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	acc := llm.NewAccumulator()
	for ev, err := range p.Stream(ctx, req) {
		if err != nil {
			return llm.Message{}, "", llm.Usage{}, err
		}
		acc.Add(ev)
	}
	return acc.Message(), acc.StopReason, acc.Usage, nil
}

func TestStreamFinalProviderDataSurvivesSessionRestart(t *testing.T) {
	data := json.RawMessage(`{"output":[{"type":"reasoning","encrypted_content":"opaque"},{"type":"message","phase":"final_answer"}]}`)
	p := &finalMessageProvider{message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("answer")}, ProviderData: data}}
	store := transcript.NewStore(t.TempDir())
	session := NewSession(Options{Provider: p, Transcript: store, PermissionMode: permission.ModeBypass})
	drainPrompt(t, session, "first question")
	if got := session.Messages(); len(got) != 2 || string(got[1].ProviderData) != string(data) {
		t.Fatalf("harness dropped final provider state: %#v", got)
	}
	restarted := NewSession(Options{Provider: p, Transcript: store, PermissionMode: permission.ModeBypass})
	if err := restarted.Resume(session.SessionID()); err != nil {
		t.Fatal(err)
	}
	drainPrompt(t, restarted, "second question")
	if len(p.requests) != 2 {
		t.Fatalf("requests=%d, want 2", len(p.requests))
	}
	var replayed bool
	for _, msg := range p.requests[1].Messages {
		if msg.Role == llm.RoleAssistant && msg.Text() == "answer" {
			replayed = string(msg.ProviderData) == string(data)
		}
	}
	if !replayed {
		t.Fatal("cold restart did not replay final provider state to next request")
	}
}
