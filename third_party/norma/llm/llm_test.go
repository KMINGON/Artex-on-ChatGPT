package llm

import "testing"

func TestAccumulator(t *testing.T) {
	acc := NewAccumulator()
	for _, ev := range []StreamEvent{
		{Type: SETextDelta, Text: "Hel"},
		{Type: SETextDelta, Text: "lo"},
		{Type: SEToolUseStart, ToolID: "t1", ToolName: "Bash"},
		{Type: SEToolInputJSON, Text: `{"command":`},
		{Type: SEToolInputJSON, Text: `"ls"}`},
		{Type: SEMessageStop},
	} {
		acc.Add(ev)
	}
	msg := acc.Message()
	if msg.Text() != "Hello" {
		t.Fatalf("text=%q", msg.Text())
	}
	uses := msg.ToolUses()
	if len(uses) != 1 || uses[0].Name != "Bash" || string(uses[0].Input) != `{"command":"ls"}` {
		t.Fatalf("tooluse=%+v", uses)
	}
}
