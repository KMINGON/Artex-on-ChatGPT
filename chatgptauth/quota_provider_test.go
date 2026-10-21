package chatgptauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/chatgptllm"
	"github.com/Autumn-27/norma/llm"
)

func TestPlanLimitBlocksNewInferenceBeforeHTTPAndRecheckMakesOneRequest(t *testing.T) {
	for _, streamFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "HTTP", true: "SSE"}[streamFailure], func(t *testing.T) {
			f := setup(t)
			f.login(t, "quota-a")
			attempts := 0
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "api.openai.com" {
					t.Fatal("unexpected inference target")
				}
				attempts++
				if attempts > 1 {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}` + "\n\n"))}, nil
				}
				if streamFailure {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(`data: {"type":"response.failed","response":{"error":{"code":"subscription_sharing_usage_limit_exceeded"}}}` + "\n\n"))}, nil
				}
				return jsonResponse(429, map[string]any{"error": map[string]string{"code": PlanUsageLimitCode}}), nil
			})}
			p, err := chatgptllm.New(chatgptllm.Config{Model: "mock", HTTPClient: client, MaxRetries: 2, Authorize: func(ctx context.Context) (context.Context, context.CancelFunc, string, error) {
				return f.m.Authorize(ctx, "quota-a")
			}, OnError: func(e *chatgptllm.Error) {
				if err := f.m.BlockPlan("quota-a", e.Code); err != nil {
					t.Error(err)
				}
			}})
			if err != nil {
				t.Fatal(err)
			}
			req := llm.CompletionRequest{Messages: []llm.Message{llm.UserText("ping")}}
			if _, _, _, err = p.Complete(t.Context(), req); err == nil {
				t.Fatal("quota response accepted")
			}
			if _, _, _, err = p.Complete(t.Context(), req); err == nil {
				t.Fatal("quota gate bypassed")
			}
			var failure *chatgptllm.Error
			if !errors.As(err, &failure) || failure.Code != "plan_usage_blocked" || failure.Attempts != 0 || !failure.NoFallback() || !failure.NoReplay() || attempts != 1 {
				t.Fatalf("gate diagnostic: %v attempts=%d", err, attempts)
			}
			if err = f.m.Recheck(t.Context(), "quota-a", func(ctx context.Context) error { _, _, _, err := p.Complete(ctx, req); return err }); err != nil {
				t.Fatal(err)
			}
			if attempts != 2 || f.record("quota-a").PlanBlockReason != "" {
				t.Fatalf("recheck attempts=%d state=%s", attempts, f.record("quota-a").PlanBlockReason)
			}
		})
	}
}
