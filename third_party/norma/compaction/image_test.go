package compaction

import (
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func TestImageBudgetCountsVisualInputNotBase64OrPath(t *testing.T) {
	image := llm.ContentBlock{Type: llm.BlockImage, Image: &llm.ImageSource{Data: strings.Repeat("x", 100000)}}
	result := llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.BlockToolResult, ToolUseID: "image", Content: []llm.ContentBlock{image}}}}
	if got := EstimateTokens([]llm.Message{result}); got != llm.ImageTokenBudget {
		t.Fatalf("visual budget=%d", got)
	}
	image.Image.Data = ""
	image.Image.Path = "/short/image.png"
	if got := EstimateTokens([]llm.Message{result}); got != llm.ImageTokenBudget {
		t.Fatalf("artifact budget=%d", got)
	}
}

func TestMicroCompactionPreservesImmutableImageReference(t *testing.T) {
	c := New(Config{ContextWindow: 200000}, nil)
	msgs := manyReadPairs(microKeepRecent + 1)
	image := llm.ContentBlock{Type: llm.BlockImage, Image: &llm.ImageSource{Path: "/session/image-immutable.png", MIMEType: "image/png", Width: 100, Height: 100}}
	msgs[1].Content[0].Content = append(msgs[1].Content[0].Content, image)
	out, changed := c.microCompact(msgs)
	if !changed || !strings.Contains(out[1].Content[0].Content[0].Text, "/session/image-immutable.png") {
		t.Fatal("old image lost its reread artifact")
	}
	if _, changed := c.microCompact(out); changed {
		t.Fatal("artifact marker repeatedly compacted")
	}
	if msgs[1].Content[0].Content[1].Type != llm.BlockImage {
		t.Fatal("source transcript mutated")
	}
	image.Image.Path = ""
	if _, changed := c.microCompact(msgs); changed {
		t.Fatal("in-memory visual evidence cleared without an artifact")
	}
}
