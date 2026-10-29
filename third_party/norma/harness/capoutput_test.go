package harness

import (
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/tool"
)

// The global post-tool net caps an oversized text block regardless of the tool.
func TestCapOutputCapsOversizedTextBlock(t *testing.T) {
	tc := &tool.ToolContext{MaxOutputChars: 100} // no OutputDir → head+tail truncation
	long := strings.Repeat("z", 5000)
	res := tool.Result{Content: []llm.ContentBlock{llm.TextBlock(long)}}

	out := capOutput(tc, res)
	got := out.Content[0].Text
	if len(got) >= 5000 || !strings.Contains(got, "bytes truncated]") {
		t.Fatalf("capOutput did not cap oversized block: len=%d", len(got))
	}
}

// Output already within budget is passed through untouched.
func TestCapOutputLeavesSmallOutputIntact(t *testing.T) {
	tc := &tool.ToolContext{MaxOutputChars: 100}
	res := tool.Result{Content: []llm.ContentBlock{llm.TextBlock("fine")}}

	if out := capOutput(tc, res); out.Content[0].Text != "fine" {
		t.Fatalf("small output changed: %q", out.Content[0].Text)
	}
}

// Output captured by the same invocation is not re-processed,
// so the net never double-truncates.
func TestCapOutputIdempotentOnAlreadyCaptured(t *testing.T) {
	tc := &tool.ToolContext{MaxOutputChars: 100}
	pre := tool.Capture(tc, strings.Repeat("a", 9999))
	res := tool.Result{Content: []llm.ContentBlock{llm.TextBlock(pre)}}

	if out := capOutput(tc, res); out.Content[0].Text != pre {
		t.Fatalf("capOutput re-processed already-captured output")
	}
}
