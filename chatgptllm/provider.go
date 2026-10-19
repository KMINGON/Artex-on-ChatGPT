// Package chatgptllm adapts ChatGPT plan usage to Norma without replacing its
// tool executor. Credentials are obtained for each request and never stored here.
package chatgptllm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/norma/llm"
)

const responsesURL = "https://api.openai.com/v1/responses"
const modelsURL = "https://api.openai.com/v1/models"

// Authorize returns a request context whose lifetime is tied to the selected
// connection. The caller always releases it, including on request failures.
type Authorize func(context.Context) (context.Context, context.CancelFunc, string, error)

type Config struct {
	Model           string
	HTTPClient      *http.Client
	Authorize       Authorize
	ReasoningEffort string
	// Zero uses two retries; negative disables retries. Only failed HTTP admission
	// or transport establishment is retried, never an opened response stream.
	MaxRetries    int
	RetryInterval time.Duration
}

// Error retains diagnostics without putting response bodies or credentials in
// its printable form. Every ChatGPT error stops automatic billing-path fallback.
type Error struct {
	StatusCode int
	Code       string
	Param      string
	RequestID  string
	Body       []byte
	Message    string
	cause      error
}

func (e *Error) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("ChatGPT 요청 실패 (status %d): %s", e.StatusCode, e.Message)
	}
	return "ChatGPT 요청 실패: " + e.Message
}
func (e *Error) Unwrap() error    { return e.cause }
func (e *Error) NoFallback() bool { return true }

// OutputRejected distinguishes an unusable model response from a connection
// failure, even when no safe text delta could be exposed to the caller.
func (e *Error) OutputRejected() bool {
	switch e.Code {
	case "invalid_completion", "invalid_tool_call", "invalid_stream", "interrupted_stream",
		"unsupported_output", "unsupported_event", "incomplete_response", "empty_response",
		"response_too_large", "auxiliary_output_limit", "auxiliary_timeout":
		return true
	default:
		return false
	}
}

func failure(code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, cause: cause}
}

type provider struct{ cfg Config }

func New(cfg Config) (llm.Provider, error) {
	cfg.Model = strings.TrimSpace(cfg.Model)
	if cfg.Model == "" || cfg.Authorize == nil {
		return nil, failure("invalid_config", "모델과 ChatGPT 연결이 필요합니다", nil)
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 2
	}
	if cfg.MaxRetries > 5 {
		cfg.MaxRetries = 5
	}
	if cfg.RetryInterval <= 0 {
		cfg.RetryInterval = 500 * time.Millisecond
	}
	cfg.RetryInterval = min(cfg.RetryInterval, 30*time.Second)
	cfg.HTTPClient = fixedClient(cfg.HTTPClient)
	return &provider{cfg: cfg}, nil
}

func fixedClient(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	copy := *client
	// Even same-origin redirects must not resend an OAuth credential elsewhere.
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copy
}

func (p *provider) Stream(parent context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		ctx := parent
		budget := outputBudget{}
		cancel := func() {}
		if auxiliaryRequest(req) {
			ctx, cancel = context.WithTimeout(ctx, auxiliaryTimeout)
			budget.limit = auxiliaryByteLimit(req)
		}
		defer cancel()
		requestError := func(err error) error {
			if budget.limit > 0 && parent.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return failure("auxiliary_timeout", "판정·요약 응답이 90초 제한을 초과했습니다", ctx.Err())
			}
			if ctx.Err() != nil {
				return failure("canceled", "요청이 취소되었습니다", ctx.Err())
			}
			return err
		}
		body, names, err := encodeRequest(p.cfg, req)
		if err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		resp, release, err := p.open(ctx, body)
		if err != nil {
			yield(llm.StreamEvent{}, requestError(err))
			return
		}
		defer release()
		defer resp.Body.Close()
		var budgetErr error
		err = consume(resp, names, func(ev llm.StreamEvent, err error) bool {
			if !budget.accept(ev) {
				budgetErr = failure("auxiliary_output_limit", "판정·요약 출력이 로컬 길이 제한을 초과했습니다", nil)
				cancel()
				return false
			}
			return yield(ev, err)
		})
		if budgetErr != nil {
			if e, ok := budgetErr.(*Error); ok {
				e.StatusCode, e.RequestID = resp.StatusCode, resp.Header.Get("X-Request-Id")
			}
			yield(llm.StreamEvent{}, budgetErr)
		} else if err != nil {
			err = requestError(err)
			var e *Error
			if errors.As(err, &e) && e.StatusCode == 0 {
				e.StatusCode, e.RequestID = resp.StatusCode, resp.Header.Get("X-Request-Id")
			}
			yield(llm.StreamEvent{}, err)
		}
	}
}

// Complete preserves Norma's atomic return contract, but unlike its built-in
// providers it buffers an HTTP SSE request (SIWC requires stream:true).
func (p *provider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	acc := llm.NewAccumulator()
	for ev, err := range p.Stream(ctx, req) {
		if err != nil {
			return llm.Message{}, "", llm.Usage{}, err
		}
		acc.Add(ev)
	}
	return acc.Message(), acc.StopReason, acc.Usage, nil
}

func (p *provider) open(ctx context.Context, body []byte) (*http.Response, context.CancelFunc, error) {
	for attempt := 0; ; attempt++ {
		resp, release, err := authorizedRequest(ctx, p.cfg.Authorize, p.cfg.HTTPClient, http.MethodPost, responsesURL, body)
		if err == nil && resp.StatusCode == http.StatusOK {
			// The ChatGPT plan route can omit Content-Type on a valid SSE body.
			// consume still validates every event and requires response.completed.
			contentType := strings.TrimSpace(strings.ToLower(resp.Header.Get("Content-Type")))
			if contentType != "" && !strings.HasPrefix(contentType, "text/event-stream") {
				resp.Body.Close()
				release()
				err := failure("invalid_stream", "서버가 이벤트 스트림을 반환하지 않았습니다", nil)
				err.StatusCode, err.RequestID = resp.StatusCode, resp.Header.Get("X-Request-Id")
				return nil, nil, err
			}
			return resp, release, nil
		}
		delay := p.cfg.RetryInterval
		if resp != nil {
			err = httpFailure(resp)
			if seconds, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil && seconds > 0 {
				delay = time.Duration(min(seconds, 30)) * time.Second
			}
			resp.Body.Close()
		}
		if release != nil {
			release()
		}
		if ctx.Err() != nil {
			return nil, nil, failure("canceled", "요청이 취소되었습니다", ctx.Err())
		}
		if attempt >= p.cfg.MaxRetries || !retryable(err) {
			return nil, nil, err
		}
		delay = min(delay*time.Duration(1<<attempt), 30*time.Second)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, nil, failure("canceled", "요청이 취소되었습니다", ctx.Err())
		case <-timer.C:
		}
	}
}

func authorizedRequest(ctx context.Context, authorize Authorize, client *http.Client, method, url string, body []byte) (*http.Response, context.CancelFunc, error) {
	if authorize == nil {
		return nil, nil, failure("not_connected", "ChatGPT 연결이 필요합니다", nil)
	}
	rctx, release, token, err := authorize(ctx)
	if err != nil {
		if release != nil {
			release()
		}
		return nil, nil, failure("authorization_failed", "ChatGPT 연결을 확인해 주세요", err)
	}
	if release == nil {
		release = func() {}
	}
	if rctx == nil || token == "" {
		release()
		return nil, nil, failure("not_connected", "ChatGPT 연결이 필요합니다", nil)
	}
	req, err := http.NewRequestWithContext(rctx, method, url, bytes.NewReader(body))
	if err != nil {
		release()
		return nil, nil, failure("invalid_request", "요청을 만들 수 없습니다", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
	}
	resp, err := client.Do(req)
	if err != nil {
		release()
		return nil, nil, failure("transport_error", "OpenAI에 연결할 수 없습니다", err)
	}
	return resp, release, nil
}

func retryable(err error) bool {
	var e *Error
	if !errors.As(err, &e) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if strings.HasPrefix(e.Code, "subscription_sharing_") {
		return e.Code == "subscription_sharing_usage_unavailable" || e.Code == "subscription_sharing_user_unavailable"
	}
	return e.Code == "transport_error" || e.StatusCode == 408 || e.StatusCode == 429 || e.StatusCode >= 500
}

func httpFailure(resp *http.Response) *Error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	e := responseFailure(body)
	e.StatusCode, e.RequestID, e.cause = resp.StatusCode, resp.Header.Get("X-Request-Id"), err
	return e
}

func responseFailure(body []byte) *Error {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Param   string `json:"param"`
			Message string `json:"message"`
		} `json:"error"`
		Code  string `json:"code"`
		Param string `json:"param"`
	}
	_ = json.Unmarshal(body, &envelope)
	code, param := envelope.Error.Code, envelope.Error.Param
	if code == "" {
		code, param = envelope.Code, envelope.Param
	}
	message := "OpenAI가 요청을 완료하지 못했습니다"
	switch code {
	case "subscription_sharing_usage_limit_exceeded":
		message = "ChatGPT 사용 한도에 도달했습니다. ChatGPT 사용량 설정을 확인해 주세요"
	case "subscription_sharing_user_not_eligible", "subscription_sharing_route_not_supported", "chatpass_v2_scope_not_authorized", "chatpass_v2_invalid_authorization_context":
		message = "이 연결에서 ChatGPT 플랜 사용이 허용되지 않았습니다"
	case "subscription_sharing_invalid_user":
		message = "ChatGPT 연결의 인증 상태를 확인해 주세요"
	case "subscription_sharing_unsupported_capability":
		message = "ChatGPT 플랜에서 지원하지 않는 요청입니다"
	case "subscription_sharing_usage_unavailable", "subscription_sharing_user_unavailable":
		message = "ChatGPT 사용 가능 여부를 일시적으로 확인할 수 없습니다"
	case "context_length_exceeded":
		message = "입력이 모델 문맥 한도를 초과했습니다 (context_length_exceeded)"
	}
	// Norma and side-question compaction recognize overflow by its printable
	// marker. Retain that signal without exposing arbitrary upstream messages.
	if code == "" && strings.Contains(strings.ToLower(envelope.Error.Message), "maximum context length") {
		message = "입력이 모델 문맥 한도를 초과했습니다 (context_length_exceeded)"
	}
	return &Error{Code: code, Param: param, Body: append([]byte(nil), body...), Message: message}
}

type Model struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextWindow int    `json:"context_window,omitempty"`
}

// Models returns account-visible models in server order, without API-key or
// bundled-catalog fallback. The same selected connection must be used to infer.
func Models(ctx context.Context, authorize Authorize, client *http.Client) ([]Model, error) {
	resp, release, err := authorizedRequest(ctx, authorize, fixedClient(client), http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, err
	}
	defer release()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, httpFailure(resp)
	}
	var catalog struct {
		Models []struct {
			Slug          string `json:"slug"`
			DisplayName   string `json:"display_name"`
			Visibility    string `json:"visibility"`
			ContextWindow int    `json:"context_window"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&catalog); err != nil || catalog.Models == nil {
		return nil, failure("invalid_catalog", "ChatGPT 모델 목록을 읽을 수 없습니다", err)
	}
	out := make([]Model, 0, len(catalog.Models))
	for _, model := range catalog.Models {
		if model.Visibility == "list" && model.Slug != "" {
			out = append(out, Model{ID: model.Slug, Name: model.DisplayName, ContextWindow: model.ContextWindow})
		}
	}
	return out, nil
}
