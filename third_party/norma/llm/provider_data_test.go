package llm

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFinalMessageOverridesDeltaOrderWithoutAliasing(t *testing.T) {
	final := Message{Role: RoleAssistant, ProviderData: json.RawMessage(`{"opaque":"encrypted","phase":"commentary"}`), Content: []ContentBlock{
		{Type: BlockThinking, Thinking: "summary"},
		TextBlock("commentary"),
		{Type: BlockToolUse, ID: "call-1", Name: "Read", Input: json.RawMessage(`{"path":"x"}`)},
		{Type: BlockToolResult, ToolUseID: "nested", Content: []ContentBlock{TextBlock("nested")}},
	}}
	acc := NewAccumulator()
	acc.Add(StreamEvent{Type: SEMessageStart, Usage: Usage{InputTokens: 3}})
	acc.Add(StreamEvent{Type: SETextDelta, Text: "commentary"})
	acc.Add(StreamEvent{Type: SEThinkingDelta, Text: "summary"})
	acc.Add(StreamEvent{Type: SEToolUseStart, ToolID: "call-1", ToolName: "Read"})
	acc.Add(StreamEvent{Type: SEToolInputJSON, Text: `{"path":"x"}`})
	acc.Add(StreamEvent{Type: SEMessageFinal, FinalMessage: &final})
	want := cloneMessage(final)
	final.ProviderData[0] = '!'
	final.Content[0].Thinking = "mutated"
	final.Content[2].Input[0] = '!'
	final.Content[3].Content[0].Text = "mutated"
	acc.Add(StreamEvent{Type: SETextDelta, Text: "late delta must not change finalized content"})
	acc.Add(StreamEvent{Type: SEMessageFinal, FinalMessage: &Message{Role: RoleUser}})
	acc.Add(StreamEvent{Type: SEMessageDelta, StopReason: "tool_use", Usage: Usage{OutputTokens: 7}})
	acc.Add(StreamEvent{Type: SEMessageStop})
	got := acc.Message()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("final message lost order/data or aliases provider: %#v", got)
	}
	got.Content[2].Input[0] = '!'
	got.Content[3].Content[0].Text = "consumer mutation"
	got.ProviderData[0] = '!'
	if !reflect.DeepEqual(acc.Message(), want) {
		t.Fatal("returned message aliases accumulator state")
	}
	if acc.StopReason != "tool_use" || acc.Usage != (Usage{InputTokens: 3, OutputTokens: 7}) {
		t.Fatalf("terminal accounting lost: %s %+v", acc.StopReason, acc.Usage)
	}
}

func TestProviderDataPairingAndBoundaryProjection(t *testing.T) {
	opaque := Message{Role: RoleAssistant, ProviderData: json.RawMessage(`{"output":[{"type":"reasoning","encrypted_content":"opaque"}]}`)}
	paired := Message{Role: RoleAssistant, ProviderData: json.RawMessage(`{"output":["original"]}`), Content: []ContentBlock{
		TextBlock("keep"), {Type: BlockToolUse, ID: "paired", Name: "Read", Input: json.RawMessage(`{}`)},
	}}
	result := Message{Role: RoleUser, Content: []ContentBlock{ToolResultText("paired", "result", false)}}
	got := PairToolBlocks([]Message{opaque, paired, result})
	if len(got) != 3 || string(got[0].ProviderData) != string(opaque.ProviderData) || string(got[1].ProviderData) != string(paired.ProviderData) {
		t.Fatalf("unchanged history lost opaque data: %#v", got)
	}
	got = PairToolBlocks([]Message{paired})
	if len(got) != 1 || len(got[0].Content) != 1 || len(got[0].ProviderData) != 0 {
		t.Fatalf("removed tool remains in replay metadata: %#v", got)
	}
	if len(paired.ProviderData) == 0 || len(paired.Content) != 2 {
		t.Fatal("projection mutated original history")
	}
	got = MessagesForAPI([]Message{paired, BoundaryMessage(BoundaryMeta{}), UserText("summary"), opaque})
	if len(got) != 2 || got[0].Text() != "summary" || string(got[1].ProviderData) != string(opaque.ProviderData) {
		t.Fatalf("boundary resurrected old metadata or dropped retained opaque turn: %#v", got)
	}
}
