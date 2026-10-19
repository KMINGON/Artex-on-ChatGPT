package chatgptllm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/norma/compaction"
	"github.com/Autumn-27/norma/llm"
)

func TestAuxiliaryBudgetRejectsWholeSummaryWithoutRetry(t *testing.T) {
	for _, streamed := range []bool{false, true} {
		t.Run(map[bool]string{false: "final-only", true: "deltas"}[streamed], func(t *testing.T) {
			var calls atomic.Int32
			p := testProvider(t, fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if !strings.Contains(string(body["instructions"]), "1024 tokens and 8192 UTF-8 bytes") || body["temperature"] != nil || body["max_output_tokens"] != nil {
					t.Errorf("unexpected helper request: %s", body)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				// Count bytes, not runes; Korean text exceeds the 8 KiB guard.
				text := strings.Repeat("가", 2800)
				if streamed {
					event(w, map[string]any{"type": "response.output_item.added", "item": textItem("")})
					event(w, map[string]any{"type": "response.output_text.delta", "item_id": "msg1", "delta": text[:300]})
					event(w, map[string]any{"type": "response.output_text.delta", "item_id": "msg1", "delta": text[300:]})
				}
				completed(w, textItem(text))
			}))
			summary, err := compaction.ProviderSummarizer(p, 1024, true)(t.Context(), []llm.Message{llm.UserText("history")})
			var e *Error
			if summary != "" || !errors.As(err, &e) || e.Code != "auxiliary_output_limit" || !e.NoFallback() || calls.Load() != 1 {
				t.Fatalf("summary=%q error=%v calls=%d", summary, err, calls.Load())
			}
		})
	}
}

func TestAuxiliaryDeadlineAndNormalTurnUnaffected(t *testing.T) {
	var remaining []time.Duration
	p := testProvider(t, &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if ok {
			remaining = append(remaining, time.Until(deadline))
		} else {
			remaining = append(remaining, 0)
		}
		return nil, context.Canceled
	})})
	_, _, _, _ = p.Complete(context.Background(), llm.CompletionRequest{Thinking: "disabled", MaxTokens: 1024})
	_, _, _, _ = p.Complete(context.Background(), llm.CompletionRequest{MaxTokens: 1024})
	_, _, _, _ = p.Complete(context.Background(), llm.CompletionRequest{Thinking: "disabled", MaxTokens: 1024, Tools: []llm.ToolSchema{{Name: "Probe"}}})
	if len(remaining) != 3 || remaining[0] < 85*time.Second || remaining[0] > auxiliaryTimeout || remaining[1] != 0 || remaining[2] != 0 {
		t.Fatalf("deadline policy=%v", remaining)
	}
	// A normal agent turn can exceed helper size limits.
	p = testProvider(t, fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		completed(w, textItem(strings.Repeat("x", 70<<10)))
	}))
	msg, _, _, err := p.Complete(t.Context(), llm.CompletionRequest{MaxTokens: 1024})
	if err != nil || len(msg.Text()) != 70<<10 {
		t.Fatalf("normal turn limited: %v", err)
	}
}

func TestFinalMessageBudgetAndPrunedState(t *testing.T) {
	budget := outputBudget{limit: 8}
	if !budget.accept(llm.StreamEvent{Type: llm.SETextDelta, Text: "12345678"}) || budget.accept(llm.StreamEvent{Type: llm.SEMessageFinal, FinalMessage: &llm.Message{Content: []llm.ContentBlock{llm.TextBlock("123456789")}}}) {
		t.Fatal("final authoritative content bypassed guard")
	}
	msg := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockThinking, Thinking: "summary"}, llm.TextBlock("kept"), {Type: llm.BlockToolUse, ID: "orphan", Name: "Probe", Input: json.RawMessage(`{}`)}}}
	raw, _ := json.Marshal(callItem("item", "orphan", "Probe", `{}`))
	if err := attachReplayState(&msg, []json.RawMessage{raw}); err != nil {
		t.Fatal(err)
	}
	pruned := llm.PairToolBlocks([]llm.Message{msg})
	body, _, err := encodeRequest(Config{Model: "m"}, llm.CompletionRequest{Messages: pruned})
	if err != nil || strings.Contains(string(body), "orphan") || strings.Contains(string(body), `"text":"summary"`) || !strings.Contains(string(body), "kept") {
		t.Fatalf("pruned replay=%s err=%v", body, err)
	}
}
