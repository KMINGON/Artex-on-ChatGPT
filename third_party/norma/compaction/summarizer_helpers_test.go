package compaction

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func toolUse(id, name string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: id, Name: name}}}
}
func toolResult(id string) llm.Message {
	return llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText(id, "body", false)}}
}

// --- pairForReplay ---

func TestPairForReplayKeepsPaired(t *testing.T) {
	msgs := []llm.Message{llm.UserText("q"), toolUse("a", "Read"), toolResult("a"), asstText("done")}
	out := pairForReplay(msgs)
	if len(out) != 4 {
		t.Fatalf("paired blocks must be kept, got %d: %+v", len(out), out)
	}
}

func TestPairForReplayKeepsDanglingUseAsUncertainText(t *testing.T) {
	msgs := []llm.Message{asstText("hi"), toolUse("a", "Read")} // result missing
	out := pairForReplay(msgs)
	if len(out) != 2 || out[0].Text() != "hi" || !strings.Contains(out[1].Text(), `도구: "Read"`) || !strings.Contains(out[1].Text(), "미확인") || len(out[1].ToolUses()) != 0 {
		t.Fatalf("unpaired tool call must be kept as uncertain summary text: %+v", out)
	}
}

func TestPairForReplayKeepsDanglingResultAsUncertainText(t *testing.T) {
	msgs := []llm.Message{llm.UserText("q"), toolResult("z")} // use missing
	out := pairForReplay(msgs)
	if len(out) != 2 || out[0].Text() != "q" || !strings.Contains(out[1].Text(), "body") || !strings.Contains(out[1].Text(), "추론하지 마세요") {
		t.Fatalf("unpaired result evidence must remain available: %+v", out)
	}
}

func TestPairForReplayKeepsSiblingTextOfUnpairedResult(t *testing.T) {
	msgs := []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{
		llm.TextBlock("keep"),
		llm.ToolResultText("gone", "x", false), // dangling
	}}}
	out := pairForReplay(msgs)
	if len(out) != 1 || len(out[0].Content) != 3 || out[0].Content[0].Text != "keep" || out[0].Content[2].Text != "x" {
		t.Fatalf("sibling text should survive: %+v", out)
	}
}

// --- groupByAPIRound ---

func TestGroupByAPIRoundShapes(t *testing.T) {
	u := llm.UserText("u")
	a := asstText("a")
	cases := []struct {
		name string
		in   []llm.Message
		want int
	}{
		{"empty", nil, 0},
		{"one user", []llm.Message{u}, 1},
		{"two user", []llm.Message{u, u}, 1},
		{"one assistant", []llm.Message{a}, 1},
		{"assistant then user", []llm.Message{a, u}, 1},
		{"user then assistant", []llm.Message{u, a}, 2},
		{"alternating", []llm.Message{u, a, u, a}, 3},
		{"consecutive assistants", []llm.Message{a, a}, 2},
	}
	for _, tc := range cases {
		if got := len(groupByAPIRound(tc.in)); got != tc.want {
			t.Errorf("%s: groups=%d, want %d", tc.name, got, tc.want)
		}
	}
}

// --- splitSummaryReplay ---

func TestSummarySplitKeepsIndivisibleRound(t *testing.T) {
	for _, messages := range [][]llm.Message{
		{llm.UserText("solo")},
		{toolUse("call", "Read"), toolResult("call")},
	} {
		if _, _, ok := splitSummaryReplay(messages); ok {
			t.Fatal("an indivisible round cannot be shortened without losing its context")
		}
	}
}

func TestSummarySplitRetainsEveryMessage(t *testing.T) {
	msgs := []llm.Message{llm.UserText("user conditions"), asstText("first check"), llm.UserText("remaining question"), asstText("latest attempt")}
	left, right, ok := splitSummaryReplay(msgs)
	if !ok {
		t.Fatal("independent rounds should be splittable")
	}
	if len(left) == 0 || len(right) == 0 || !reflect.DeepEqual(append(append([]llm.Message{}, left...), right...), msgs) {
		t.Fatal("splitting removed or changed source messages")
	}
}

func TestSummarySplitKeepsInterleavedToolExchange(t *testing.T) {
	msgs := []llm.Message{
		llm.UserText("scope"), toolUse("call", "Read"),
		asstText("interleaved progress"), toolResult("call"),
		asstText("later independent result"),
	}
	left, right, ok := splitSummaryReplay(msgs)
	if !ok {
		t.Fatal("expected a boundary outside the interleaved tool exchange")
	}
	for _, part := range [][]llm.Message{left, right} {
		if !reflect.DeepEqual(pairForReplay(part), part) {
			t.Fatal("a split separated tool use from its result")
		}
	}
}

// --- formatCompactSummary ---

func TestFormatCompactSummaryCases(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain passthrough", "just text", "just text"},
		{"analysis stripped, no summary tags", "<analysis>secret</analysis>\nHello", "Hello"},
		{"summary extracted ignoring surroundings", "before <summary>THE BODY</summary> after", "Summary:\nTHE BODY"},
		{"multiline analysis then summary", "<analysis>l1\nl2</analysis>\n<summary>ok</summary>", "Summary:\nok"},
		{"blank lines collapsed", "<summary>a\n\n\n\nb</summary>", "Summary:\na\n\nb"},
	}
	for _, tc := range cases {
		if got := formatCompactSummary(tc.in); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// --- ProviderSummarizer maxOut cap ---

func TestProviderSummarizerMaxOutCap(t *testing.T) {
	cases := []struct{ mo, want int }{
		{0, 20000},     // unset → cap
		{8000, 8000},   // below cap → passthrough
		{20000, 20000}, // at cap
		{25000, 20000}, // above cap → clamped
		{19999, 19999}, // just below cap
	}
	for _, tc := range cases {
		p := &captureProvider{reply: "<summary>x</summary>"}
		_, _ = ProviderSummarizer(p, tc.mo, false)(context.Background(), []llm.Message{llm.UserText("hi")})
		if p.reqs[0].MaxTokens != tc.want {
			t.Errorf("mo=%d: MaxTokens=%d, want %d", tc.mo, p.reqs[0].MaxTokens, tc.want)
		}
	}
}

// --- retry / cancellation edges ---

func TestSummarizerContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &captureProvider{reply: "x"}
	_, err := ProviderSummarizer(p, 0, false)(ctx, []llm.Message{llm.UserText("hi")})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if p.calls != 0 {
		t.Fatalf("no stream should be issued on a cancelled ctx, calls=%d", p.calls)
	}
}

func TestSummarizerPTLExhausts(t *testing.T) {
	p := &captureProvider{failN: 999} // every attempt overflows
	head := []llm.Message{llm.UserText("u0"), asstText("a1"), llm.UserText("u1"), asstText("a2")}
	_, err := ProviderSummarizer(p, 0, false)(context.Background(), head)
	if err == nil || !IsOverflow(err) {
		t.Fatalf("persistent overflow should surface an overflow error, got %v", err)
	}
	if p.calls < 2 {
		t.Fatalf("should have tried a smaller complete segment, calls=%d", p.calls)
	}
}
