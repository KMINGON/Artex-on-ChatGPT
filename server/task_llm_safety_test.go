package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

type exhaustedProviderError struct{}

func (exhaustedProviderError) Error() string           { return "server_error after provider retries" }
func (exhaustedProviderError) NoReplay() bool          { return true }
func (exhaustedProviderError) NoFallback() bool        { return false }
func (exhaustedProviderError) RetryableSameAuth() bool { return false }

func TestProviderRetryBudgetCannotMultiplyAtTaskOrWorkerLayer(t *testing.T) {
	for _, complete := range []bool{false, true} {
		p := &scriptedLLMProvider{err: exhaustedProviderError{}}
		hooks := taskLLMStreamHooks{current: func() (taskLLMSelection, error) {
			return taskLLMSelection{provider: p}, nil
		}}
		var err error
		if complete {
			_, _, _, err = completeTaskLLM(t.Context(), "7", llm.CompletionRequest{}, hooks)
		} else {
			_, err = collectTaskLLMStream(streamTaskLLM(t.Context(), "7", llm.CompletionRequest{}, hooks))
		}
		if err == nil || p.calls != 1 || retryableWorkerModelError(harness.ReasonModelError, err) {
			t.Fatalf("retry budget multiplied: complete=%v calls=%d err=%v", complete, p.calls, err)
		}
	}
}

func TestTaskRetryExhaustionPreventsWholeWorkerReplay(t *testing.T) {
	defer withZeroRetryBackoff()()
	p := &scriptedLLMProvider{err: errors.New("connection reset by peer")}
	hooks := taskLLMStreamHooks{current: func() (taskLLMSelection, error) {
		return taskLLMSelection{provider: p}, nil
	}}
	_, err := collectTaskLLMStream(streamTaskLLM(t.Context(), "7", llm.CompletionRequest{}, hooks))
	if p.calls != sameProviderStreamRetries+1 || retryableWorkerModelError(harness.ReasonModelError, err) {
		t.Fatalf("exhausted task retry entered worker loop: calls=%d err=%v", p.calls, err)
	}
	if isRetryableStreamError(errors.New("unclassified provider failure")) {
		t.Fatal("unknown failure classified as transient")
	}
}

func TestTaskFailoverDoesNotSendOpaqueStateAcrossConnections(t *testing.T) {
	for _, complete := range []bool{false, true} {
		for _, sameConnection := range []bool{false, true} {
			first := &scriptedLLMProvider{err: errors.New("status 402: insufficient_quota")}
			second := &scriptedLLMProvider{events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: "ok"}, {Type: llm.SEMessageDelta, StopReason: "end_turn"}}}
			active := 1
			hooks := taskLLMStreamHooks{
				current: func() (taskLLMSelection, error) {
					if active == 1 {
						return taskLLMSelection{profileID: 1, provider: first, connectionIdentity: "connection-a"}, nil
					}
					identity := "connection-b"
					if sameConnection {
						identity = "connection-a"
					}
					return taskLLMSelection{profileID: 2, provider: second, connectionIdentity: identity}, nil
				},
				exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
					active = 2
					next := int64(2)
					return db.TaskLLMTransition{Advanced: true, NextProfileID: &next}, nil
				},
			}
			req := llm.CompletionRequest{Messages: []llm.Message{{Role: llm.RoleAssistant, ProviderData: json.RawMessage(`{"opaque":"fixture"}`)}}}
			var err error
			if complete {
				_, _, _, err = completeTaskLLM(context.Background(), "7", req, hooks)
			} else {
				_, err = collectTaskLLMStream(streamTaskLLM(context.Background(), "7", req, hooks))
			}
			if sameConnection {
				if err != nil || second.calls != 1 {
					t.Fatalf("same connection failover rejected: calls=%d err=%v", second.calls, err)
				}
			} else if err == nil || second.calls != 0 || retryableWorkerModelError(harness.ReasonModelError, err) {
				t.Fatalf("opaque request crossed connection: complete=%v calls=%d err=%v", complete, second.calls, err)
			}
		}
	}
}

func TestTaskRuntimeTracksObservedConnectionPerSession(t *testing.T) {
	runtime := &taskLLMRuntime{taskID: "7"}
	opaque := llm.CompletionRequest{Messages: []llm.Message{{Role: llm.RoleAssistant, ProviderData: json.RawMessage(`{"opaque":"fixture"}`)}}}
	if err := runtime.observeConnection("session-a", opaque, "connection-a"); err != nil {
		t.Fatal("first legacy observation must remain readable")
	}
	if err := runtime.observeConnection("session-a", opaque, "connection-b"); err == nil || retryableWorkerModelError(harness.ReasonModelError, err) {
		t.Fatal("a mid-run connection change must not replay old state")
	}
	if err := runtime.observeConnection("session-b", opaque, "connection-b"); err != nil {
		t.Fatal("one session's connection must not block another session")
	}
	if err := runtime.observeConnection("session-a", opaque, "connection-a"); err != nil {
		t.Fatal("rejected connection change must not overwrite the observed identity")
	}
	if err := runtime.observeConnection("session-a", llm.CompletionRequest{}, "connection-b"); err != nil {
		t.Fatal("ordinary history retains existing API failover behavior")
	}
	if err := runtime.observeConnection("session-a", opaque, "connection-b"); err != nil {
		t.Fatal("new responses should use the last observed connection")
	}
}

func TestTaskFailoverCannotChangeBillingWithoutOpaqueState(t *testing.T) {
	for _, complete := range []bool{false, true} {
		first := &scriptedLLMProvider{err: errors.New("status 402: insufficient_quota")}
		second := &scriptedLLMProvider{}
		active := 1
		hooks := taskLLMStreamHooks{
			current: func() (taskLLMSelection, error) {
				if active == 1 {
					return taskLLMSelection{profileID: 1, provider: first}, nil
				}
				return taskLLMSelection{profileID: 2, provider: second, chatgptPlan: true}, nil
			},
			exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
				active = 2
				next := int64(2)
				return db.TaskLLMTransition{Advanced: true, NextProfileID: &next}, nil
			},
		}
		var err error
		if complete {
			_, _, _, err = completeTaskLLM(t.Context(), "7", llm.CompletionRequest{}, hooks)
		} else {
			_, err = collectTaskLLMStream(streamTaskLLM(t.Context(), "7", llm.CompletionRequest{}, hooks))
		}
		if !errors.Is(err, db.ErrTaskLLMBillingTransition) || second.calls != 0 {
			t.Fatalf("billing changed: complete=%v calls=%d err=%v", complete, second.calls, err)
		}
	}
}
