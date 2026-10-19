package transcript

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func TestProviderDataPersistsThroughTranscriptReload(t *testing.T) {
	store := NewStore(t.TempDir())
	writer := store.NewWriter("opaque-session", "")
	want := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("answer")}, ProviderData: json.RawMessage(`{"output":[{"type":"reasoning","encrypted_content":"opaque"},{"type":"message","phase":"final_answer"}]}`)}
	writer.RecordMessage(want, llm.Usage{OutputTokens: 12})
	if err := writer.Err(); err != nil {
		t.Fatal(err)
	}
	reopened := NewStore(store.dir)
	records, err := reopened.Load(reopened.MainPath("opaque-session"))
	if err != nil {
		t.Fatal(err)
	}
	messages, usage := Messages(records)
	if len(messages) != 1 || !reflect.DeepEqual(messages[0], want) || usage.OutputTokens != 12 {
		t.Fatalf("provider state changed across disk roundtrip: %#v %+v", messages, usage)
	}
}
