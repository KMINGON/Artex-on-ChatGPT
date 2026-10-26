package server

import (
	"context"
	"encoding/json"
	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
	"iter"
	"testing"
)

type browserObservationProvider struct {
	step func(llm.CompletionRequest) []llm.StreamEvent
}

func (p *browserObservationProvider) Stream(_ context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		for _, event := range p.step(req) {
			if !yield(event, nil) {
				return
			}
		}
	}
}

func (p *browserObservationProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return accumulateStreamForTest(ctx, p.Stream, req)
}

func TestBrowserToolReturnsObservedOutputWithoutAutomaticGraphRegistration(t *testing.T) {
	session := fakeBrowserSession(t)
	calls := 0
	base := actool.Build(actool.Spec{Name: "fixture_browser", Schema: map[string]any{"type": "object"}, Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
		calls++
		return actool.Text("HTTP response and DOM evidence"), nil
	}})
	tool := &managedBrowserTool{CoreTool: base, session: session}
	result, err := tool.Call(t.Context(), json.RawMessage(`{}`), nil)
	if err != nil || result.Flatten() != "HTTP response and DOM evidence" || calls != 1 {
		t.Fatalf("browser result changed: %+v %v calls=%d", result, err, calls)
	}
}
