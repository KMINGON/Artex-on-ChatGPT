// Package chatgptllm adapts OpenAI Responses to Norma for API keys and ChatGPT
// plan usage, preserving the same tool executor and response replay contract.
package chatgptllm

import (
	"bytes"
	"context"
	"errors"
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
	Model              string
	HTTPClient         *http.Client
	Authorize          Authorize
	APIKey             string // Mutually exclusive with Authorize.
	ReasoningEffort    string
	AccessProgramCyber string
	// OnError observes failures before cancellation can hide an upstream quota
	// denial. It must not retry or issue another inference request.
	OnError func(*Error)
	// Zero uses two plan retries or three API retries; negative disables retries.
	// One budget covers HTTP
	// admission, interrupted reads, and known transient SSE failures before any
	// output is exposed.
	MaxRetries    int
	RetryInterval time.Duration
	// Network silence only; not a total generation or tool execution deadline.
	// Zero uses DefaultStreamIdleTimeout. Negative disables this local limit.
	StreamIdleTimeout time.Duration
}

type provider struct {
	cfg        Config
	fixedRetry bool // API's explicitly configured delay historically stayed fixed.
}

func New(cfg Config) (llm.Provider, error) {
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	if cfg.Model == "" || (cfg.Authorize == nil) == (cfg.APIKey == "") {
		e := failure("invalid_config", "모델과 하나의 OpenAI API 키 또는 ChatGPT 연결이 필요합니다", nil)
		e.APIKeyMode = cfg.APIKey != ""
		return nil, e
	}
	if err := ValidateOptions(cfg.Model, authMode(cfg.APIKey), cfg.ReasoningEffort, cfg.AccessProgramCyber); err != nil {
		return nil, err
	}
	if cfg.APIKey != "" {
		key := cfg.APIKey
		cfg.Authorize = func(ctx context.Context) (context.Context, context.CancelFunc, string, error) {
			return ctx, func() {}, key, nil
		}
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 2
		if cfg.APIKey != "" {
			cfg.MaxRetries = 3
		}
	}
	limit := 5
	if cfg.APIKey != "" {
		limit = 20
	}
	cfg.MaxRetries = min(cfg.MaxRetries, limit)
	fixedRetry := cfg.APIKey != "" && cfg.RetryInterval > 0
	if cfg.RetryInterval <= 0 {
		cfg.RetryInterval = 500 * time.Millisecond
	}
	if cfg.APIKey == "" {
		cfg.RetryInterval = min(cfg.RetryInterval, 30*time.Second)
	}
	cfg.HTTPClient = fixedClient(cfg.HTTPClient)
	if cfg.StreamIdleTimeout == 0 {
		cfg.StreamIdleTimeout = DefaultStreamIdleTimeout
	}
	return &provider{cfg: cfg, fixedRetry: fixedRetry}, nil
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
		if p.cfg.APIKey == "" && auxiliaryRequest(req) {
			ctx, cancel = context.WithTimeout(ctx, auxiliaryTimeout)
			budget.limit = auxiliaryByteLimit(req)
		}
		defer cancel()
		body, names, err := encodeRequest(p.cfg, req)
		shape := requestShape(body, req)
		if err != nil {
			yield(llm.StreamEvent{}, p.annotate(err, "encoding", shape, 0, false))
			return
		}
		var observed ResponseMetadata
		for attempt := 0; ; attempt++ {
			resp, release, idle, err := p.openMonitored(ctx, body)
			attempts := attempt
			var admissionErr *Error
			if resp != nil || (errors.As(err, &admissionErr) && admissionErr.Attempts > 0) {
				attempts++
			}
			emitted, stopped, errorObserved := false, false, false
			if err != nil && p.cfg.OnError != nil {
				p.cfg.OnError(p.annotate(err, "admission", shape, attempts, false))
				errorObserved = true
			}
			stage := "admission"
			if err == nil {
				stage = "stream"
				var budgetErr error
				err = consumeObserved(resp, names, p.cfg.APIKey != "" && !auxiliaryRequest(req), func(metadata ResponseMetadata) {
					known := observed.UsageReceived || metadata.UsageReceived
					observed = metadata
					observed.UsageReceived = known
					observeResponse(ctx, metadata)
				}, func(e *Error) {
					if p.cfg.OnError != nil {
						p.cfg.OnError(p.annotate(e, "stream", shape, attempts, emitted))
					}
					errorObserved = true
				}, func(ev llm.StreamEvent, err error) bool {
					if !budget.accept(ev) {
						budgetErr = failure("auxiliary_output_limit", "판정·요약 출력이 로컬 길이 제한을 초과했습니다", nil)
						return false
					}
					if ev.Text != "" || ev.Type == llm.SEToolUseStart || ev.Type == llm.SEMessageFinal {
						emitted = true
					}
					if !yield(ev, err) {
						stopped = true
						return false
					}
					return true
				})
				resp.Body.Close()
				release()
				if idle.end() {
					err = streamIdleError()
				}
				if stopped {
					return
				}
				if budgetErr != nil {
					err = budgetErr
				} else if err == nil {
					return
				}
			}
			if budget.limit > 0 && parent.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
				err = failure("auxiliary_timeout", "판정·요약 응답이 90초 제한을 초과했습니다", ctx.Err())
			} else if ctx.Err() != nil && (err == nil || !errors.Is(err, ctx.Err())) {
				err = failure("canceled", "요청이 취소되었습니다", ctx.Err())
			}
			e := p.annotate(err, stage, shape, attempts, emitted)
			e.Metadata = observed
			if resp != nil {
				e.StatusCode = resp.StatusCode
				e.RequestID = safeToken(resp.Header.Get("X-Request-Id"))
			}
			if p.cfg.OnError != nil && !errorObserved {
				p.cfg.OnError(e)
			}
			if ctx.Err() != nil || emitted || attempt >= p.cfg.MaxRetries || !retryable(e) {
				yield(llm.StreamEvent{}, e)
				return
			}
			delay := p.retryDelay(attempt)
			if resp != nil {
				var canWait bool
				delay, canWait = retryAfterDelay(resp.Header.Get("Retry-After"), delay)
				if !canWait {
					yield(llm.StreamEvent{}, e)
					return
				}
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				e := p.annotate(failure("canceled", "요청이 취소되었습니다", ctx.Err()), "backoff", shape, attempt+1, false)
				e.Metadata = observed
				yield(llm.StreamEvent{}, e)
				return
			case <-timer.C:
			}
		}
	}
}

func retryAfterDelay(header string, fallback time.Duration) (time.Duration, bool) {
	seconds, err := strconv.ParseUint(strings.TrimSpace(header), 10, 64)
	// An unrepresentable server delay must not overflow or cause an early retry.
	if errors.Is(err, strconv.ErrRange) || err == nil && seconds > uint64((1<<63-1)/time.Second) {
		return 0, false
	}
	if err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second, true
	}
	return fallback, true
}

func (p *provider) retryDelay(attempt int) time.Duration {
	if p.fixedRetry {
		return p.cfg.RetryInterval
	}
	limit := 30 * time.Second
	if p.cfg.APIKey != "" {
		limit = 8 * time.Second
	}
	return min(p.cfg.RetryInterval*time.Duration(1<<attempt), limit)
}

// Complete atomically aggregates the same SSE transport used by Stream.
func (p *provider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	acc := llm.NewAccumulator()
	for ev, err := range p.Stream(ctx, req) {
		if err != nil {
			return llm.Message{}, "", acc.Usage, err
		}
		acc.Add(ev)
	}
	return acc.Message(), acc.StopReason, acc.Usage, nil
}

func (p *provider) open(ctx context.Context, body []byte) (*http.Response, context.CancelFunc, error) {
	resp, release, err := authorizedRequest(ctx, p.cfg.Authorize, p.cfg.HTTPClient, http.MethodPost, responsesURL, body)
	if err != nil {
		return resp, nil, err
	}
	if resp.StatusCode == http.StatusOK {
		// Some valid plan streams omit Content-Type. Event validation still applies.
		contentType := strings.TrimSpace(strings.ToLower(resp.Header.Get("Content-Type")))
		if contentType == "" || strings.HasPrefix(contentType, "text/event-stream") {
			return resp, release, nil
		}
		err = failure("invalid_stream", "서버가 이벤트 스트림을 반환하지 않았습니다", nil)
	} else {
		err = httpFailure(resp)
	}
	resp.Body.Close()
	release()
	return resp, nil, err
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
		var blocked interface{ ChatGPTPlanBlocked() bool }
		if errors.As(err, &blocked) && blocked.ChatGPTPlanBlocked() {
			return nil, nil, failure("plan_usage_blocked", "이 ChatGPT 연결의 사용 한도가 중지되어 있습니다. Usage 설정을 확인한 뒤 한도 재확인을 실행해 주세요", err)
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
		e := failure("transport_error", "OpenAI에 연결할 수 없습니다", err)
		e.Attempts = 1
		return nil, nil, e
	}
	return resp, release, nil
}

type Model struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	ContextWindow int               `json:"context_window,omitempty"`
	Capabilities  ModelCapabilities `json:"capabilities"`
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
	return DecodeModels(resp.Body, "chatgpt_plan")
}
