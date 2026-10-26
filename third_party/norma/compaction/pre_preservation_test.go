package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func TestPreSummaryReceivesOldToolEvidenceAndSpillReferences(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "expanded", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			messages := manyReadPairs(microKeepRecent + 2)
			messages[0].Content[0].Name = "Bash"
			messages[0].ProviderData = json.RawMessage(`{"opaque":"original-call"}`)
			messages[1].Content[0].Content = []llm.ContentBlock{llm.TextBlock("EARLY_EVIDENCE <persisted-output>/session/evidence.txt</persisted-output> " + strings.Repeat("details ", 500))}
			messages[1].ProviderData = json.RawMessage(`{"opaque":"original-result"}`)
			before, _ := json.Marshal(messages)
			calls := 0
			c := New(Config{ContextWindow: 200000, MaxOutputTokens: 8000, KeepRecent: 2}, func(_ context.Context, head []llm.Message) (string, error) {
				calls++
				if !reflect.DeepEqual(head, messages[:len(messages)-2]) || !strings.Contains(head[1].Content[0].Content[0].Text, "EARLY_EVIDENCE") {
					t.Fatal("tool evidence or replay state was removed before reaching the summarizer")
				}
				switch outcome {
				case "failure":
					return "", errors.New("summary unavailable")
				case "expanded":
					return strings.Repeat("too large ", 10000), nil
				case "cancelled":
					return "", context.Canceled
				default:
					return "EARLY_EVIDENCE; saved at /session/evidence.txt; comparison remains unfinished", nil
				}
			})
			out := c.Pre(t.Context(), messages, 200000)
			if calls != 1 {
				t.Fatalf("expected one summary attempt, got %d", calls)
			}
			if outcome == "success" {
				if boundaryCount(out) != 1 || !strings.Contains(llm.MessagesForAPI(out)[0].Text(), "/session/evidence.txt") {
					t.Fatal("successful summary was not published with its evidence reference")
				}
			} else if !reflect.DeepEqual(out, messages) || boundaryCount(out) != 0 {
				t.Fatal("unsuccessful compaction erased tool evidence or published a boundary")
			}
			after, _ := json.Marshal(messages)
			if string(before) != string(after) {
				t.Fatal("proactive compaction mutated the source history")
			}
		})
	}
}
