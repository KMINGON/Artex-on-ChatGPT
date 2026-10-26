package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

type segmentSummaryProvider struct {
	requests []llm.CompletionRequest
	step     func(context.Context, llm.CompletionRequest) (string, string, error)
}

func (p *segmentSummaryProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.requests = append(p.requests, req)
	text, stop, err := p.step(ctx, req)
	return asstText(text), stop, llm.Usage{}, err
}

func (p *segmentSummaryProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		msg, stop, _, err := p.Complete(ctx, req)
		if err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: msg.Text()}, nil) {
			return
		}
		yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: stop}, nil)
	}
}

func summaryRequestSource(req llm.CompletionRequest) []llm.Message {
	msgs := req.Messages[:len(req.Messages)-1] // final summary instruction
	if len(msgs) > 0 && msgs[0].Text() == summaryPartMarker {
		msgs = msgs[1:]
	}
	return msgs
}

func TestSegmentedSummaryPreservesEarlyScopeAndUnfinishedWork(t *testing.T) {
	for _, nonStreaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "stream", true: "complete"}[nonStreaming], func(t *testing.T) {
			head := []llm.Message{
				llm.UserText("EARLY_SCOPE"),
				toolUse("control", "Read"), toolResult("control"),
				asstText("UNFINISHED_COMPARISON"), llm.UserText("ALLOWED_PREPARATION"),
				asstText("LATEST_PARTIAL_RESULT"),
			}
			head[1].ProviderData = json.RawMessage(`{"opaque":"unchanged"}`)
			before, _ := json.Marshal(head)
			var successful []llm.Message
			p := &segmentSummaryProvider{step: func(_ context.Context, req llm.CompletionRequest) (string, string, error) {
				source := summaryRequestSource(req)
				if len(source) > 2 {
					return "", "", errors.New("context length exceeded")
				}
				if !reflect.DeepEqual(pairForReplay(source), source) {
					t.Fatal("summary request split a tool exchange")
				}
				successful = append(successful, source...)
				var parts []string
				for _, message := range source {
					parts = append(parts, message.Text())
				}
				return "<summary>" + strings.Join(parts, " / ") + "</summary>", "end_turn", nil
			}}
			text, err := ProviderSummarizer(p, 0, nonStreaming)(t.Context(), head)
			if err != nil {
				t.Fatal(err)
			}
			for _, marker := range []string{"EARLY_SCOPE", "UNFINISHED_COMPARISON", "ALLOWED_PREPARATION", "LATEST_PARTIAL_RESULT"} {
				if !strings.Contains(text, marker) {
					t.Fatalf("lost %s in completed segmented summary: %q", marker, text)
				}
			}
			if !reflect.DeepEqual(successful, head) {
				t.Fatalf("successful requests did not cover the complete source once: %+v", successful)
			}
			after, _ := json.Marshal(head)
			if string(before) != string(after) {
				t.Fatal("summary retries changed the original history")
			}
		})
	}
}

func TestFailedLaterSummaryPartKeepsOriginalBoundaryAndHistory(t *testing.T) {
	for _, failure := range []string{"error", "cancelled", "output_limit"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			messages := []llm.Message{llm.UserText("EARLY_SCOPE " + strings.Repeat("scope ", 2000))}
			for _, label := range []string{"FIRST_RESULT", "UNFINISHED", "LATE_FAILURE", "TAIL_A", "TAIL_B"} {
				messages = append(messages, asstText(label+strings.Repeat(" detail", 1000)))
			}
			original, _ := json.Marshal(messages)
			accepted := 0
			p := &segmentSummaryProvider{step: func(_ context.Context, req llm.CompletionRequest) (string, string, error) {
				source := summaryRequestSource(req)
				if len(source) > 1 {
					return "", "", errors.New("prompt too long")
				}
				if accepted == 0 {
					accepted++
					if failure == "cancelled" {
						cancel()
					}
					return "<summary>early scope retained</summary>", "end_turn", nil
				}
				if failure == "output_limit" {
					return "partial late summary", "max_tokens", nil
				}
				return "", "", errors.New("later summary failed")
			}}
			c := New(smallWindow(), ProviderSummarizer(p, 0, false))
			out, changed := c.Reactive(ctx, messages)
			if accepted != 1 {
				t.Fatal("fixture did not reach a completed early segment")
			}
			got, _ := json.Marshal(out)
			after, _ := json.Marshal(messages)
			if changed || string(got) != string(original) || string(after) != string(original) || llm.LastBoundaryIndex(out) >= 0 {
				t.Fatal("a failed later segment published a partial summary or changed history")
			}
		})
	}
}

type protectedSummaryOverflow struct{ noReplay, noFallback bool }

func (*protectedSummaryOverflow) Error() string      { return "context length exceeded" }
func (e *protectedSummaryOverflow) NoReplay() bool   { return e.noReplay }
func (e *protectedSummaryOverflow) NoFallback() bool { return e.noFallback }

func TestSummaryOverflowRespectsNoReplayAndNoFallback(t *testing.T) {
	for _, nonStreaming := range []bool{false, true} {
		for _, original := range []*protectedSummaryOverflow{{noReplay: true}, {noFallback: true}} {
			p := &captureProvider{failN: 999, failErr: original}
			text, err := ProviderSummarizer(p, 0, nonStreaming)(t.Context(), []llm.Message{llm.UserText("scope"), asstText("result")})
			if !errors.Is(err, original) || text != "" || p.calls != 1 {
				t.Fatalf("protected overflow was retried/split: calls=%d text=%q err=%v", p.calls, text, err)
			}
		}
	}
}

func TestEmptyFormattedSummaryPreservesOriginal(t *testing.T) {
	for _, reply := range []string{"<analysis>internal reasoning only</analysis>", "<summary> \n </summary>"} {
		p := &captureProvider{reply: reply}
		text, err := ProviderSummarizer(p, 0, false)(t.Context(), []llm.Message{llm.UserText("unfinished work")})
		if err == nil || text != "" {
			t.Fatalf("empty normalized summary accepted: %q %v", text, err)
		}
	}
}

func TestExpandedSummaryDoesNotHideOriginalView(t *testing.T) {
	messages := []llm.Message{llm.UserText(strings.Repeat("original ", 200)), asstText("remaining check"), llm.UserText("tail"), asstText("still running")}
	original, _ := json.Marshal(messages)
	c := New(smallWindow(), func(context.Context, []llm.Message) (string, error) {
		return strings.Repeat("larger partial summaries ", 1000), nil
	})
	out, changed := c.autoCompact(t.Context(), messages, 1000, "auto")
	got, _ := json.Marshal(out)
	if changed || string(got) != string(original) || llm.LastBoundaryIndex(out) >= 0 {
		t.Fatal("larger summary hid source without reducing the actual input view")
	}
}

func TestCancellationAfterSummaryResponseDoesNotPublishBoundary(t *testing.T) {
	for _, nonStreaming := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		p := &segmentSummaryProvider{step: func(context.Context, llm.CompletionRequest) (string, string, error) {
			cancel()
			return "<summary>late completion</summary>", "end_turn", nil
		}}
		messages := []llm.Message{llm.UserText(strings.Repeat("source ", 200)), asstText("still unresolved"), llm.UserText("tail"), asstText("continuing")}
		c := New(smallWindow(), ProviderSummarizer(p, 0, nonStreaming))
		out, changed := c.Reactive(ctx, messages)
		if changed || !reflect.DeepEqual(out, messages) || len(p.requests) != 1 {
			t.Fatal("cancelled summary response changed the conversation view")
		}
	}
}

func TestSummaryProviderReceivesUnpairedEvidenceWithoutReplayingTools(t *testing.T) {
	image := llm.ContentBlock{Type: llm.BlockImage, Image: &llm.ImageSource{Path: "/session/evidence.png", MIMEType: "image/png", Width: 100, Height: 100}}
	call := toolUse("unconfirmed-call", "Bash")
	call.Content[0].Input = json.RawMessage(`{"command":"read the preserved evidence"}`)
	call.ProviderData = json.RawMessage(`{"opaque":"old-tool-shape"}`)
	result := llm.Message{Role: llm.RoleUser, ProviderData: json.RawMessage(`{"opaque":"old-result-shape"}`), Content: []llm.ContentBlock{
		llm.TextBlock("sibling text"),
		{Type: llm.BlockToolResult, ToolUseID: "unknown-call", IsError: true, Content: []llm.ContentBlock{llm.TextBlock("PARTIAL_EVIDENCE /session/full-output.txt"), image}},
	}}
	messages := []llm.Message{call, result}
	before, _ := json.Marshal(messages)
	p := &segmentSummaryProvider{step: func(_ context.Context, req llm.CompletionRequest) (string, string, error) {
		source := summaryRequestSource(req)
		if len(source) != 2 || !reflect.DeepEqual(llm.MessagesForAPI(source), source) {
			t.Fatal("summary request still contains orphan tool protocol blocks")
		}
		for _, message := range source {
			if len(message.ProviderData) != 0 {
				t.Fatal("old provider data could resurrect the orphan tool shape")
			}
			for _, block := range message.Content {
				if block.Type == llm.BlockToolUse || block.Type == llm.BlockToolResult {
					t.Fatal("unpaired record remained a replayable protocol block")
				}
			}
		}
		for _, marker := range []string{"unconfirmed-call", "Bash", string(call.Content[0].Input), "미확인"} {
			if !strings.Contains(source[0].Text(), marker) {
				t.Fatalf("unpaired call lost %q", marker)
			}
		}
		for _, marker := range []string{"sibling text", "unknown-call", "true", "PARTIAL_EVIDENCE", "/session/full-output.txt", "추론하지 마세요"} {
			if !strings.Contains(source[1].Text(), marker) {
				t.Fatalf("unpaired result lost %q", marker)
			}
		}
		if !reflect.DeepEqual(source[1].Content[len(source[1].Content)-1], image) {
			t.Fatal("unpaired visual evidence was lost")
		}
		return "<summary>unconfirmed tool record and partial evidence preserved</summary>", "end_turn", nil
	}}
	if _, err := ProviderSummarizer(p, 0, false)(t.Context(), messages); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(messages)
	if string(before) != string(after) {
		t.Fatal("orphan summary projection changed the raw transcript")
	}
}
