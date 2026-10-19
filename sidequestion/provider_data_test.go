package sidequestion

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func TestSnapshotSummaryExcludesProviderDataWithoutChangingReplay(t *testing.T) {
	base := []llm.Message{
		{Role: llm.RoleAssistant, ProviderData: json.RawMessage(`{"output":[{"type":"reasoning","encrypted_content":"EARLY_OPAQUE_SECRET"}]}`), Content: []llm.ContentBlock{{Type: llm.BlockThinking, Thinking: "EARLY_VISIBLE_THINKING", Signature: `artex-chatgpt-reasoning-v1:{"encrypted_content":"LEGACY_OPAQUE_SECRET"}`}, llm.TextBlock("EARLY_READABLE_EVIDENCE")}},
		{Role: llm.RoleAssistant, ProviderData: json.RawMessage(`{"output":[{"type":"reasoning","encrypted_content":"RECENT_OPAQUE_SECRET"}]}`), Content: []llm.ContentBlock{{Type: llm.BlockThinking, Thinking: "recent", Signature: "retain-original-signature"}, llm.TextBlock("RECENT_READABLE_EVIDENCE")}},
	}
	before := string(mustJSON(t, base))
	calls := 0
	p := fakeProvider{complete: func(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		calls++
		body := string(mustJSON(t, req))
		if strings.Contains(body, "OPAQUE_SECRET") || strings.Contains(body, "provider_data") || strings.Contains(body, "encrypted_content") || strings.Contains(body, "artex-chatgpt-reasoning-v1") {
			t.Fatal("opaque replay data entered summary prompt")
		}
		if !strings.Contains(body, "EARLY_READABLE_EVIDENCE") || !strings.Contains(body, "EARLY_VISIBLE_THINKING") || strings.Contains(body, "RECENT_READABLE_EVIDENCE") {
			t.Fatal("summary changed the selected visible prefix")
		}
		return assistant("visible evidence summary"), "end_turn", llm.Usage{}, nil
	}}
	b := contextBuilder{service: SideQuestionService{Provider: p}, window: 32000}
	keep := EstimateInputTokens(llm.CompletionRequest{Messages: base[1:]})
	got, err := b.compactSnapshot(t.Context(), base, keep, "snapshot")
	if err != nil || calls != 1 {
		t.Fatalf("compaction: calls=%d err=%v", calls, err)
	}
	if len(got) != 2 || !reflect.DeepEqual(got[1], base[1]) {
		t.Fatal("retained structured tail lost replay data")
	}
	if string(mustJSON(t, base)) != before {
		t.Fatal("summary sanitization mutated original snapshot")
	}
}
