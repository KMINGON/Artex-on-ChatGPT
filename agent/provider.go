// Package agent connects ARTEX agents to OpenAI Responses through API keys or ChatGPT subscriptions.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Autumn-27/artex/chatgptauth"
	"github.com/Autumn-27/artex/chatgptllm"
	"github.com/Autumn-27/norma/compaction"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

// Config describes the LLM backend resolved from the environment.
type Config struct {
	AuthMode         string
	ChatGPTAccountID string
	ChatGPTAuth      *chatgptauth.Manager
	APIKey           string
	Model            string
	// Proxy routes all LLM requests through the given proxy URL (http/https/socks5,
	// optionally with user:pass@ credentials). Empty means direct — it does NOT
	// fall back to the standard *_PROXY environment variables.
	Proxy string
	// RatePerSecond / RatePerMinute cap the shared request rate across ALL agents
	// using the provider (0 = that window unlimited).
	RatePerSecond float64
	RatePerMinute float64
	// ContextWindowK is the model's context window in K tokens (user-configured),
	// used to size compaction thresholds. 0 = default; see CompactionWindow.
	ContextWindowK int
	// ReasoningEffort is omitted when empty, preserving the selected model's default.
	ReasoningEffort    string
	AccessProgramCyber string
	// MaxTokens is the API output cap. Subscription requests omit this server option.
	MaxTokens  int
	configErr  error
	httpClient *http.Client // injected by package tests; runtime endpoints are fixed
	// Retry 是该配置解析后的重试参数(profile 覆盖 → 全局策略 → 内置默认,由
	// server 侧解析)。三层的含义见 RetryConfig;零值 = 完全沿用内置默认。
	Retry RetryConfig
}

// RetryConfig is resolved from profile overrides and global settings. Zero uses
// the provider default, a negative count disables retries, and a positive
// interval selects a fixed delay. ConnectAttempts covers admission and safe SSE
// failures in one budget: API defaults to 3 retries, subscription to 2.
type RetryConfig struct {
	ConnectAttempts int
	ConnectInterval time.Duration
	// Legacy internal policy for providers without their own retry budget.
	// Responses errors carry NoReplay, so these do not add retries on top.
	StreamAttempts int
	StreamInterval time.Duration
}

// compaction window resolution bounds (in K tokens). Below the floor the
// threshold math (window − summary reserve − buffer) would go non-positive and
// compaction would fire every turn; above the cap it would never fire.
const (
	defaultWindowK = 200  // unset → assume a 200K window (existing local default)
	minWindowK     = 32   // floor so effectiveWindow stays comfortably positive
	maxWindowK     = 1000 // cap at 1M tokens (user request)
)

// CompactionWindow returns the model context window in TOKENS for compaction
// thresholds, resolved from the user-configured size (ContextWindowK). 0/unset →
// a 200K default; otherwise clamped to [32K, 1M] so compaction stays effective.
func (c Config) CompactionWindow() int {
	k := c.ContextWindowK
	if k <= 0 {
		k = defaultWindowK
	}
	if k < minWindowK {
		k = minWindowK
	}
	if k > maxWindowK {
		k = maxWindowK
	}
	return k * 1000
}

// compactionConfig builds the agent-core compaction config for a context window
// in tokens. agentcore.NewSession wires the summarizer (same provider) when this
// is set on Options.Compaction.
func compactionConfig(windowTokens int) *compaction.Config {
	if windowTokens <= 0 {
		windowTokens = defaultWindowK * 1000
	}
	return &compaction.Config{ContextWindow: windowTokens}
}

// FromEnv accepts only the official OpenAI API. Removed provider settings never
// silently select a different model or send a key to a third-party endpoint.
func FromEnv() (Config, bool) {
	key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if key == "" {
		return Config{}, false
	}
	provider := strings.TrimSpace(os.Getenv("ARTEX_LLM_PROVIDER"))
	if provider == "" {
		provider = "openai"
	}
	c := ConfigFrom(provider, os.Getenv("ARTEX_LLM_MODEL"), os.Getenv("ARTEX_LLM_BASE_URL"), key, os.Getenv("ARTEX_LLM_PROXY"))
	return c, true
}

// ConfigFrom validates legacy profile fields before connecting them to Responses.
// The old arguments remain solely to reject unsupported persisted profiles.
func ConfigFrom(provider, model, baseURL, apiKey, proxy string) Config {
	c := Config{AuthMode: "api_key", Model: strings.TrimSpace(model), APIKey: strings.TrimSpace(apiKey), Proxy: strings.TrimSpace(proxy)}
	switch strings.TrimSpace(provider) {
	case "openai":
		if c.Model == "" {
			c.Model = "gpt-4o"
		}
	case "openai-responses":
		if c.Model == "" {
			c.Model = "gpt-5"
		}
	default:
		c.configErr = errors.New("지원이 종료된 LLM 설정입니다. OpenAI API 또는 ChatGPT 구독 프로필을 선택해 주세요")
	}
	switch strings.TrimRight(strings.TrimSpace(baseURL), "/") {
	case "", "https://api.openai.com/v1", "https://api.openai.com/v1/responses", "https://api.openai.com/v1/chat/completions":
	default:
		c.configErr = errors.New("이전 사용자 지정 LLM 주소는 지원하지 않습니다. 공식 OpenAI 프로필을 새로 설정해 주세요")
	}
	return c
}

func (c Config) Provider() string { return "openai-responses" }

// APIKeyForReuse rejects legacy connection credentials before a settings request
// can silently adopt them for the official OpenAI endpoint.
func (c Config) APIKeyForReuse() (string, error) {
	if c.configErr != nil {
		return "", c.configErr
	}
	if c.AuthMode != "" && c.AuthMode != "api_key" {
		return "", errors.New("OpenAI API 키를 직접 입력해 주세요")
	}
	return c.APIKey, nil
}

// NewProvider shares completion validation and replay across both credentials.
func (c Config) NewProvider() (llm.Provider, error) {
	if c.configErr != nil {
		return unavailableProvider{err: c.configErr}, nil
	}
	client := c.httpClient
	if client == nil {
		var err error
		client, err = ChatGPTHTTPClient(c.Proxy)
		if err != nil {
			return unavailableProvider{err: errors.New("LLM 프록시 설정을 확인해 주세요")}, nil
		}
	}
	cfg := chatgptllm.Config{Model: c.Model, HTTPClient: client, ReasoningEffort: c.ReasoningEffort, AccessProgramCyber: c.AccessProgramCyber,
		MaxRetries: c.Retry.ConnectAttempts, RetryInterval: c.Retry.ConnectInterval}
	switch c.AuthMode {
	case "chatgpt_plan":
		cfg.OnError = func(err *chatgptllm.Error) {
			if c.ChatGPTAuth != nil && err.Code == chatgptauth.PlanUsageLimitCode {
				if blockErr := c.ChatGPTAuth.BlockPlan(c.ChatGPTAccountID, err.Code); blockErr != nil {
					log.Printf("[chatgpt] 한도 중지 상태 저장 실패: %v", blockErr)
				}
			}
		}
		cfg.Authorize = func(ctx context.Context) (context.Context, context.CancelFunc, string, error) {
			if c.ChatGPTAuth == nil {
				return ctx, func() {}, "", chatgptauth.ErrLoginRequired
			}
			return c.ChatGPTAuth.Authorize(ctx, c.ChatGPTAccountID)
		}
	case "", "api_key":
		cfg.APIKey = c.APIKey
	default:
		return unavailableProvider{err: errors.New("지원하지 않는 모델 인증 방식입니다")}, nil
	}
	p, err := chatgptllm.New(cfg)
	if err != nil {
		return unavailableProvider{err: err}, nil
	}
	if c.RatePerSecond > 0 || c.RatePerMinute > 0 {
		p = llm.RateLimited(p, llm.RateLimit{PerSecond: c.RatePerSecond, PerMinute: c.RatePerMinute})
	}
	return p, nil
}

// IsQuotaExhaustedMessage deliberately recognizes only explicit balance,
// billing, credit, or quota-exhaustion signals. Generic 429/rate-limit text,
// authentication failures, network errors, and server failures are excluded.
var nonFailoverHTTPStatus = regexp.MustCompile(`(?:status(?:\s+code)?|http(?:\s+status)?)\s*[=:]?\s*(?:401|403|5\d\d)\b`)
var transientQuotaLimit = regexp.MustCompile(`(?i)(?:\b(?:rpm|tpm|rpd|qps)\b|quota[_\s-]*metric|rate[_\s-]*limit|too many requests|(?:requests?|tokens?)\s+(?:per|/)\s*(?:second|minute)|(?:per|/)\s*(?:second|minute)\s+(?:requests?|tokens?)|generate[_\s-]*requests[_\s-]*per[_\s-]*(?:minute|second)|tokens?[_\s-]*per[_\s-]*(?:minute|second))`)

func IsQuotaExhaustedMessage(message string) bool {
	message = strings.ToLower(message)
	// Authentication/authorization and provider-side 5xx failures never rotate,
	// even when a gateway happens to echo a quota-looking phrase in the body.
	if nonFailoverHTTPStatus.MatchString(message) {
		return false
	}
	// Provider APIs frequently describe an ordinary rate limit as "quota
	// exceeded", especially Google-style responses containing a quota metric.
	// These limits recover with time and must stay on the current provider.
	if transientQuotaLimit.MatchString(message) {
		return false
	}
	markers := []string{
		"insufficient_quota", "quota_exceeded", "quota exceeded", "quota exhausted",
		"exceeded your current quota", "billing_hard_limit_reached",
		"billing hard limit", "billing_not_active", "credit balance", "insufficient credit",
		"insufficient balance", "balance is too low", "payment required", "status 402",
		"余额不足", "额度不足", "额度已用尽", "欠费",
	}
	for _, marker := range markers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	// gRPC RESOURCE_EXHAUSTED is overloaded for both account quota and ordinary
	// request-rate limiting. Preserve it as an explicit exhaustion signal only
	// when the same error does not identify a transient rate limit.
	return strings.Contains(message, "resource_exhausted") &&
		!strings.Contains(message, "rate limit") &&
		!strings.Contains(message, "too many requests")
}

// ChatGPTHTTPClient uses the explicit proxy for both OpenAI authentication modes.
// It preserves status codes and never records opaque wire bodies.
func ChatGPTHTTPClient(proxy string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		transport.Proxy = nil // 留空=直连,不回退 HTTP_PROXY/HTTPS_PROXY 环境变量
	} else {
		proxyURL, err := url.Parse(proxy)
		if err != nil {
			return nil, fmt.Errorf("llm: invalid proxy %q: %w", proxy, err)
		}
		switch proxyURL.Scheme {
		case "http", "https", "socks5":
		case "":
			return nil, fmt.Errorf("llm: proxy %q missing scheme (use http://, https:// or socks5://)", proxy)
		default:
			return nil, fmt.Errorf("llm: unsupported proxy scheme %q (use http, https or socks5)", proxyURL.Scheme)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return &http.Client{Transport: transport}, nil
}

// Connection tests log only structured diagnostics, never response bodies.
func logChatGPTTestConnection(err error) {
	status, code, param, requestID := 0, "test_failed", "", ""
	var responseErr *chatgptllm.Error
	switch {
	case err == nil:
		status, code = http.StatusOK, "ok"
	case errors.As(err, &responseErr):
		status, code, param, requestID = responseErr.StatusCode, responseErr.Code, responseErr.Param, responseErr.RequestID
	case errors.Is(err, context.DeadlineExceeded):
		code = "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		code = "canceled"
	}
	log.Printf("[llm-test] ChatGPT result status=%d code=%q param=%q request_id=%q", status, code, param, requestID)
}

// TestConnection makes a minimal real completion to verify the provider/model/
// endpoint/key actually work. Returns the round-trip latency and the model's
// reply text.
func TestConnection(ctx context.Context, c Config) (lat time.Duration, reply string, err error) {
	defer func() { logChatGPTTestConnection(err) }()
	c.Retry.ConnectAttempts = -1
	prov, err := c.NewProvider()
	if err != nil {
		return 0, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ctx = transcript.WithSessionID(ctx, "conntest-"+transcript.NewSessionID())
	start := time.Now()
	// This tool-free request uses the provider directly, with no executable agent session.
	req := llm.CompletionRequest{
		System:    []string{"Reply with exactly OK, without explanation."},
		Messages:  []llm.Message{llm.UserText("ping")},
		MaxTokens: 8192,
	}
	msg, stop, _, completeErr := prov.Complete(ctx, req)
	err = completeErr
	if err == nil && stop != "end_turn" {
		err = errors.New("연결 테스트 응답이 완성되지 않았습니다")
	}
	reply = msg.Text()
	lat = time.Since(start)
	if err != nil {
		return lat, "", err
	}
	// err==nil 还不够：请求通了但模型一个字都不吐的情况真实存在(思考把预算烧光、
	// 正文被安全策略吞掉、兼容层把 content 丢了)。这种配置在会话里就是"不回话",
	// 测试却报成功——正是本项要消除的落差。没有可见正文一律判失败。
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return lat, "", fmt.Errorf("요청은 완료됐지만 모델이 표시할 텍스트를 반환하지 않았습니다")
	}
	return lat, reply, nil
}
