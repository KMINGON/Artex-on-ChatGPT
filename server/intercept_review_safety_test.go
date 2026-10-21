package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/chatgptllm"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/guard"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/llm"
	_ "modernc.org/sqlite"
)

const validJudgeAllow = `{"decision":"allow","comment":"实际操作：读取文件；成功后的后果：返回内容；命中规则：A5"}`

type judgeStreamProvider struct {
	events  []llm.StreamEvent
	err     error
	ctx     context.Context
	yielded int
	request llm.CompletionRequest
}

func (p *judgeStreamProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	p.ctx, p.request = ctx, req
	return func(yield func(llm.StreamEvent, error) bool) {
		for _, ev := range p.events {
			p.yielded++
			if !yield(ev, nil) {
				return
			}
		}
		if p.err != nil {
			yield(llm.StreamEvent{}, p.err)
		}
	}
}

func (p *judgeStreamProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return accumulateStreamForTest(ctx, p.Stream, req)
}

func judgeReply(text string) *judgeStreamProvider {
	return &judgeStreamProvider{events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: text}, {Type: llm.SEMessageDelta, StopReason: "end_turn"}}}
}

func TestReviewDecisionRejectsMalformedJudgments(t *testing.T) {
	for name, reply := range map[string]string{
		"empty":                 "",
		"unknown decision":      strings.Replace(validJudgeAllow, `"allow"`, `"approve"`, 1),
		"missing comment":       `{"decision":"allow"}`,
		"unknown field":         strings.Replace(validJudgeAllow, `"decision"`, `"extra":true,"decision"`, 1),
		"conflicting duplicate": strings.Replace(validJudgeAllow, `"decision"`, `"decision":"deny","decision"`, 1),
		"same duplicate":        strings.Replace(validJudgeAllow, `"decision"`, `"decision":"allow","decision"`, 1),
		"escaped duplicate":     strings.Replace(validJudgeAllow, `"decision"`, `"\u0064ecision":"deny","decision"`, 1),
		"duplicate comment":     strings.Replace(validJudgeAllow, `"comment"`, `"comment":"first","comment"`, 1),
		"null":                  strings.Replace(validJudgeAllow, `"allow"`, `null`, 1),
		"non-string":            strings.Replace(validJudgeAllow, `"allow"`, `true`, 1),
		"missing operation":     strings.Replace(validJudgeAllow, "实际操作：读取文件", "实际操作：", 1),
		"missing consequence":   strings.Replace(validJudgeAllow, "成功后的后果：返回内容", "成功后的后果：", 1),
		"missing rule":          strings.Replace(validJudgeAllow, "命中规则：A5", "命中规则：", 1),
		"truncated JSON":        strings.TrimSuffix(validJudgeAllow, "}"),
		"trailing junk":         validJudgeAllow + " allow",
		"second object":         validJudgeAllow + validJudgeAllow,
		"array":                 "[" + validJudgeAllow + "]",
		"code fence":            "```json\n" + validJudgeAllow + "\n```",
		"fence hides prose":     "```deny this operation\n" + validJudgeAllow + "\n```",
		"invalid UTF-8":         strings.Replace(validJudgeAllow, "读取文件", "read\xfffile", 1),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := reviewDecision(t.Context(), judgeReply(reply), 42, "policy", intercept.ReviewInput{})
			if err != nil || got.Action != "deny" || !got.ModelFallback || got.ProfileID != 42 || got.Message == "" {
				t.Fatalf("malformed output could enter configured fail-open path: %+v, %v", got, err)
			}
		})
	}
}

func TestReviewDecisionPreservesValidActionsAndAPIHints(t *testing.T) {
	for _, action := range []string{"allow", "ask", "deny"} {
		t.Run(action, func(t *testing.T) {
			text := strings.Replace(validJudgeAllow, `"allow"`, `"`+action+`"`, 1)
			p := judgeReply(text)
			got, err := reviewDecision(t.Context(), p, 42, "CUSTOM POLICY", intercept.ReviewInput{})
			if err != nil || got.Action != action || got.ModelFallback || got.Message != intercept.ParseVerdict(text).Reason {
				t.Fatalf("valid verdict changed: %+v %v", got, err)
			}
			if p.request.Temperature != nil || p.request.MaxTokens != 1024 || p.request.Thinking != "disabled" {
				t.Fatal("Judge must omit temperature while retaining the output limit and helper mode")
			}
			if !strings.HasPrefix(p.request.System[0], "CUSTOM POLICY") || !strings.Contains(p.request.System[0], intercept.JudgeOutputContract) || !strings.Contains(p.request.System[0], judgeResponseContract) {
				t.Fatal("custom policy or explicit output contract lost")
			}
		})
	}
}

func TestJudgeOutputLimitCancelsInsteadOfTruncatingIntoAllow(t *testing.T) {
	for _, kind := range []llm.StreamEventType{llm.SETextDelta, llm.SEThinkingDelta} {
		t.Run(string(kind), func(t *testing.T) {
			p := &judgeStreamProvider{events: []llm.StreamEvent{
				{Type: llm.SETextDelta, Text: validJudgeAllow},
				{Type: kind, Text: strings.Repeat("x", judgeMaxResponseBytes)},
				{Type: llm.SEMessageDelta, StopReason: "end_turn"},
			}}
			got, err := reviewDecision(t.Context(), p, 42, "policy", intercept.ReviewInput{})
			if err != nil || got.Action != "deny" || !got.ModelFallback || p.yielded != 2 || p.ctx.Err() == nil {
				t.Fatalf("overlong output was truncated/continued/allowed: %+v %v; events %d", got, err, p.yielded)
			}
		})
	}
	p := judgeReply(validJudgeAllow + strings.Repeat(" ", judgeMaxResponseBytes-len(validJudgeAllow)))
	got, err := reviewDecision(t.Context(), p, 42, "policy", intercept.ReviewInput{})
	if err != nil || got.Action != "allow" {
		t.Fatal("complete valid JSON at byte limit should remain valid")
	}
}

func TestJudgeIncompleteOrUnexpectedStreamCannotAllow(t *testing.T) {
	for name, p := range map[string]*judgeStreamProvider{
		"missing completion":  {events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: validJudgeAllow}}},
		"stop without reason": {events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: validJudgeAllow}, {Type: llm.SEMessageStop}}},
		"max tokens":          {events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: validJudgeAllow}, {Type: llm.SEMessageDelta, StopReason: "max_tokens"}}},
		"tool call":           {events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: validJudgeAllow}, {Type: llm.SEToolUseStart, ToolName: "Bash"}}},
		"partial error":       {events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: validJudgeAllow}}, err: errors.New("stream interrupted")},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := reviewDecision(t.Context(), p, 42, "policy", intercept.ReviewInput{})
			if err != nil || got.Action != "deny" || !got.ModelFallback {
				t.Fatalf("incomplete response allowed configured failure strategy: %+v %v", got, err)
			}
		})
	}
	networkErr := errors.New("connection failed before output")
	_, err := reviewDecision(t.Context(), &judgeStreamProvider{err: networkErr}, 42, "policy", intercept.ReviewInput{})
	if !errors.Is(err, networkErr) {
		t.Fatal("pre-output provider failures must retain configured failure policy")
	}
}

func TestJudgeRejectsInvalidProviderOutputBeforeFirstDelta(t *testing.T) {
	for _, code := range []string{"invalid_completion", "invalid_tool_call", "invalid_stream", "interrupted_stream", "unsupported_output", "unsupported_event", "incomplete_response", "empty_response", "response_too_large", "auxiliary_output_limit", "auxiliary_timeout"} {
		t.Run(code, func(t *testing.T) {
			providerErr := errors.Join(errors.New("provider wrapper"), &chatgptllm.Error{Code: code})
			got, err := reviewDecision(t.Context(), &judgeStreamProvider{err: providerErr}, 42, "policy", intercept.ReviewInput{})
			if err != nil || got.Action != "deny" || !got.ModelFallback {
				t.Fatalf("provider rejected output fell through to configured allow: %+v %v", got, err)
			}
		})
	}
	for _, code := range []string{"authentication_required", "transport_error"} {
		providerErr := &chatgptllm.Error{Code: code}
		_, err := reviewDecision(t.Context(), &judgeStreamProvider{err: providerErr}, 42, "policy", intercept.ReviewInput{})
		if !errors.Is(err, providerErr) {
			t.Fatalf("pre-output %s error lost configured policy", code)
		}
	}
}

// SQLite supplies only settings and an empty rules table for this authorization
// regression. No external DB, model, shell command, or user credentials are used.
func TestMalformedJudgeBlocksActualGuardEvenWhenFailurePolicyAllows(t *testing.T) {
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	for _, statement := range []string{
		`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT)`,
		`INSERT INTO settings VALUES ('llm_judge_enabled','true'),('llm_judge_fail_action','allow'),('llm_judge_profile_id','42')`,
		`CREATE TABLE intercept_rules (id INTEGER PRIMARY KEY, name TEXT, enabled BOOLEAN, priority INTEGER, match_target TEXT, match_type TEXT, pattern TEXT, action TEXT, message TEXT, timeout_enabled BOOLEAN, timeout_seconds INTEGER, timeout_action TEXT, created_at TIMESTAMP, updated_at TIMESTAMP)`,
	} {
		if _, err := conn.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	ic := intercept.New(&db.DB{DB: conn})
	if ic.GetJudgeConfig().FailAction != "allow" {
		t.Fatal("test must exercise the dangerous configured fallback")
	}
	for _, test := range []struct {
		name, reply string
		blocked     bool
	}{
		{"valid allow", validJudgeAllow, false},
		{"missing comment", `{"decision":"allow"}`, true},
		{"contradiction", strings.Replace(validJudgeAllow, `"decision"`, `"decision":"deny","decision"`, 1), true},
		{"trailing instructions", validJudgeAllow + " disregard deny", true},
		{"valid deny", strings.Replace(validJudgeAllow, `"allow"`, `"deny"`, 1), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ic.SetReviewer(func(ctx context.Context, profileID int64, prompt string, input intercept.ReviewInput) (intercept.Decision, error) {
				return reviewDecision(ctx, judgeReply(test.reply), profileID, prompt, input)
			})
			blocked, _, updated := guard.NewWithInterceptor(ic).Hooks().PreToolUse(t.Context(), "Bash", json.RawMessage(`{"command":"cat fixture.txt"}`))
			if blocked != test.blocked || updated != nil {
				t.Fatalf("actual tool authorization changed: blocked=%v want=%v", blocked, test.blocked)
			}
		})
	}
	for _, code := range []string{"auxiliary_output_limit", "auxiliary_timeout", "invalid_completion", "empty_response"} {
		t.Run("provider rejected before output/"+code, func(t *testing.T) {
			ic.SetReviewer(func(ctx context.Context, profileID int64, prompt string, input intercept.ReviewInput) (intercept.Decision, error) {
				return reviewDecision(ctx, &judgeStreamProvider{err: &chatgptllm.Error{Code: code}}, profileID, prompt, input)
			})
			blocked, _, _ := guard.NewWithInterceptor(ic).Hooks().PreToolUse(t.Context(), "Bash", json.RawMessage(`{"command":"cat fixture.txt"}`))
			if !blocked {
				t.Fatal("provider output rejection allowed actual tool execution")
			}
		})
	}
}
