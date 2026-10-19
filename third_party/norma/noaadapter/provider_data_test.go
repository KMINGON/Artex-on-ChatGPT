package noaadapter

import (
	"encoding/json"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func TestReassemblePreservesOnlyUnmodifiedProviderData(t *testing.T) {
	msg := assistantWith(llm.TextBlock("commentary"), toolUse("call-1", "Read", `{"path":"x"}`))
	msg.ProviderData = json.RawMessage(`{"output":[{"type":"reasoning","encrypted_content":"opaque"}]}`)
	messages := []llm.Message{msg, toolResult("call-1", "result")}
	cores, sidecar := Project(messages)
	out := Reassemble(cores, messages, sidecar, ReassembleOptions{Tag: true})
	if len(out) != 2 || string(out[0].ProviderData) != string(msg.ProviderData) {
		t.Fatalf("unchanged assistant replay lost metadata: %#v", out)
	}
	pruned := Reassemble(cores[1:], messages, sidecar, ReassembleOptions{})
	if len(pruned) != 2 || len(pruned[0].ProviderData) != 0 || len(pruned[0].Content) != 1 {
		t.Fatalf("pruned commentary can be resurrected: %#v", pruned)
	}
	cores[1].Text = `{"path":"changed"}`
	rewritten := Reassemble(cores, messages, sidecar, ReassembleOptions{})
	if len(rewritten) != 2 || len(rewritten[0].ProviderData) != 0 || string(rewritten[0].Content[1].Input) != cores[1].Text {
		t.Fatalf("rewritten call carries stale original input: %#v", rewritten)
	}
	if len(msg.ProviderData) == 0 || string(msg.Content[1].Input) != `{"path":"x"}` {
		t.Fatal("projection mutated raw history")
	}
}
