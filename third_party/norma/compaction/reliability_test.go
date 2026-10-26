package compaction

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func TestCompactionKeepsWholeInterleavedToolBatch(t *testing.T) {
	for _, priorBoundary := range []bool{false, true} {
		msgs := []llm.Message{llm.UserText(strings.Repeat("old 1", 100)), llm.UserText(strings.Repeat("old 2", 100))}
		if priorBoundary {
			msgs = append(msgs, llm.BoundaryMessage(llm.BoundaryMeta{}), llm.UserText(strings.Repeat("prior summary", 100)))
		}
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			{Type: llm.BlockToolUse, ID: "a", Name: "Graph", Input: json.RawMessage(`{}`)},
			{Type: llm.BlockToolUse, ID: "b", Name: "Graph", Input: json.RawMessage(`{}`)},
		}}, llm.UserText("background notice"),
			llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText("a", "RESULT_A", false)}},
			llm.SkillInvocationMessage("s", "/skills/s", "instructions"),
			llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText("b", "RESULT_B", false)}}, llm.UserText("recent"))
		before, _ := json.Marshal(msgs)
		var summarized []llm.Message
		c := New(Config{ContextWindow: 200000, KeepRecent: 2}, func(_ context.Context, m []llm.Message) (string, error) { summarized = m; return "summary", nil })
		out, ok := c.autoCompact(context.Background(), msgs, 180000, "auto")
		if !ok {
			t.Fatal("expected compaction")
		}
		all := append(append([]llm.Message(nil), summarized...), llm.MessagesForAPI(out)...)
		seen := map[string]int{}
		for _, m := range all {
			for _, b := range m.Content {
				if b.Type == llm.BlockToolResult {
					seen[b.ToolUseID]++
				}
			}
		}
		if !reflect.DeepEqual(seen, map[string]int{"a": 1, "b": 1}) {
			t.Fatalf("lost/duplicated result: %v", seen)
		}
		after, _ := json.Marshal(msgs)
		if string(before) != string(after) {
			t.Fatal("source history changed")
		}
	}
}

func TestCurrentGrowthAndOverheadOverrideStaleUsage(t *testing.T) {
	var calls int
	c := New(Config{ContextWindow: 200000, KeepRecent: 2}, func(context.Context, []llm.Message) (string, error) { calls++; return "summary", nil })
	msgs := []llm.Message{llm.UserText(strings.Repeat("x", 615000)), llm.UserText("recent1"), llm.UserText("recent2")}
	out := c.Pre(context.Background(), msgs, 160000)
	if calls != 1 || llm.LastBoundaryIndex(out) < 0 {
		t.Fatalf("growth missed: summaries=%d", calls)
	}
	c.Pre(context.Background(), out, 0)
	if calls != 1 {
		t.Fatal("compacted view immediately summarized again")
	}
	c.SetInputOverhead(170000)
	c.Pre(context.Background(), []llm.Message{llm.UserText("old"), llm.UserText("old2"), llm.UserText("tail")}, 1)
	if calls != 2 {
		t.Fatal("fixed overhead ignored")
	}
}
