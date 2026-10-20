package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
)

type diagnosticFixtureError struct{}

func (diagnosticFixtureError) Error() string { return "OpenAI 요청을 완료하지 못했습니다" }
func (diagnosticFixtureError) Diagnostics() map[string]any {
	return map[string]any{"status": 200, "code": "server_error", "attempts": 3, "event_type": "response.failed", "request_id": "req_fixture", "response_id": "resp_fixture"}
}

func TestCaptureFailureKeepsBodyFreeDiagnostics(t *testing.T) {
	provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, diagnosticFixtureError{})
	}}
	var results []db.Activity
	_, _, err := captureRun(WithRunInfo(t.Context(), RunInfo{TaskID: 7}), agentcore.Options{
		Provider: provider, SessionID: "exp7-main-s1", MaxTurns: 1,
	}, "PRIVATE_PROMPT_FIXTURE", func(a db.Activity) {
		if a.Kind == "result" {
			results = append(results, a)
		}
	})
	if err == nil || len(results) != 1 {
		t.Fatalf("expected exactly one failure result: count=%d err=%v", len(results), err)
	}
	result := results[0]
	var metadata map[string]any
	if err := json.Unmarshal(result.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["role"] != "mainagent" || metadata["session_id"] != "exp7-main-s1" || metadata["run_id"] == "" {
		t.Fatalf("missing run attribution: %s", result.Metadata)
	}
	for _, want := range []string{"server_error", "response.failed", "req_fixture", "resp_fixture"} {
		if !strings.Contains(string(result.Metadata), want) {
			t.Fatalf("missing %s: %s", want, result.Metadata)
		}
	}
	if strings.Contains(string(result.Metadata), "PRIVATE_PROMPT_FIXTURE") || !strings.Contains(result.Detail, "재시도 2회") || !strings.Contains(result.Summary, "메인 에이전트") || strings.Contains(result.Detail, "blocked") {
		t.Fatalf("unsafe or misleading failure result: %+v", result)
	}
}

func TestRunOutputProtectsWholeRunReplay(t *testing.T) {
	original := errors.New("temporary failure")
	for _, trace := range []*runTrace{{toolCalls: 1}, {outputEmitted: true}} {
		err := protectRunOutput(original, trace)
		var noReplay interface{ NoReplay() bool }
		if !errors.Is(err, original) || !errors.As(err, &noReplay) || !noReplay.NoReplay() {
			t.Fatal("a run with effects may be replayed")
		}
	}
	if protectRunOutput(original, &runTrace{}) != original {
		t.Fatal("an untouched run should preserve its original retry policy")
	}
}

func TestModelFailureTextDoesNotInventRetryOrBlockedState(t *testing.T) {
	sum, detail := terminalText(t.Context(), &harness.Terminal{Reason: harness.ReasonModelError, Err: errors.New("failure"), Turns: 1}, &runTrace{role: "worker", toolCalls: 1})
	if !strings.Contains(sum, "Worker") || !strings.Contains(detail, "확인할 수 없습니다") || !strings.Contains(detail, "반영되었을 수") {
		t.Fatalf("missing factual failure detail: %s\n%s", sum, detail)
	}
	for _, unsupported := range []string{"blocked", "재시도 소진", "전송 계층", "아직 아무 작업"} {
		if strings.Contains(detail, unsupported) {
			t.Fatalf("failure invented %q: %s", unsupported, detail)
		}
	}
}

func TestCaptureFailureAfterToolPreventsWholeRunReplay(t *testing.T) {
	calls, executions := 0, 0
	provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		calls++
		if calls > 1 {
			yield(llm.StreamEvent{}, errors.New("connection reset before next response"))
			return
		}
		for _, ev := range []llm.StreamEvent{
			{Type: llm.SEToolUseStart, ToolID: "fixture-call", ToolName: "Count"},
			{Type: llm.SEToolInputJSON, Text: `{}`},
			{Type: llm.SEMessageDelta, StopReason: "tool_use"},
		} {
			if !yield(ev, nil) {
				return
			}
		}
	}}
	probe := tool.Build(tool.Spec{Name: "Count", Schema: map[string]any{"type": "object"},
		Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
			executions++
			return tool.Text("counted"), nil
		},
	})
	_, reason, err := captureRun(t.Context(), agentcore.Options{
		Provider: provider, Tools: []tool.CoreTool{probe}, PermissionMode: permission.ModeBypass, MaxTurns: 3,
	}, "fixture", nil)
	var policy interface{ NoReplay() bool }
	if calls != 2 || executions != 1 || reason != harness.ReasonModelError || !errors.As(err, &policy) || !policy.NoReplay() {
		t.Fatalf("tool effects were not protected: calls=%d executions=%d reason=%s err=%v", calls, executions, reason, err)
	}
}

func TestCaptureResponsesFailureWithoutBodyRecording(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "req_fixture")
		fmt.Fprint(w, "data: "+`{"type":"response.failed","response":{"id":"resp_fixture","status":"failed","error":{"code":"unknown_fixture","type":"fixture_error","param":"input","message":"PRIVATE_UPSTREAM_MESSAGE"}}}`+"\n\n")
	}))
	defer srv.Close()
	cfg := apiFixtureConfig(t, srv)
	provider, err := cfg.NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	var result db.Activity
	// No llmrec Capture or body-recording setting is installed.
	_, _, err = captureRun(t.Context(), agentcore.Options{Provider: provider, SessionID: "exp1-main", MaxTurns: 1},
		"PRIVATE_PROMPT", func(a db.Activity) {
			if a.Kind == "result" {
				result = a
			}
		})
	if err == nil || !result.IsError {
		t.Fatal("expected captured model failure")
	}
	for _, want := range []string{"unknown_fixture", "fixture_error", "response.failed", "req_fixture", "resp_fixture", `"attempts":1`, `"request_shape"`, `"usage_received":false`} {
		if !strings.Contains(string(result.Metadata), want) {
			t.Fatalf("missing diagnostic %s: %s", want, result.Metadata)
		}
	}
	combined := string(result.Metadata) + result.Detail + result.Summary
	for _, secret := range []string{"PRIVATE_PROMPT", "PRIVATE_UPSTREAM_MESSAGE", "test-placeholder"} {
		if strings.Contains(combined, secret) {
			t.Fatalf("private content leaked into failure diagnostics: %s", secret)
		}
	}
}
