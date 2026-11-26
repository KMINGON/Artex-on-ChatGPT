package llm

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Format selects the wire protocol of a built-in Provider. The host supplies
// its own Provider for any other protocol.
type Format string

const (
	// FormatAnthropic speaks the Anthropic Messages API (POST /v1/messages).
	FormatAnthropic Format = "anthropic"
	// FormatOpenAI speaks OpenAI-compatible Chat Completions
	// (POST <base>/chat/completions), the protocol local model servers expose.
	FormatOpenAI Format = "openai"
)

// AnthropicBaseURL is the endpoint used when a FormatAnthropic Config names none.
const AnthropicBaseURL = "https://api.anthropic.com"

// MaxTokensFieldCompletion is the Config.MaxTokensField value that moves the
// OpenAI Chat Completions output cap onto "max_completion_tokens". Any other
// value keeps the classic "max_tokens".
const MaxTokensFieldCompletion = "max_completion_tokens"

// Config configures the built-in providers (FR-03.4). Every credential and
// address is explicit: nothing is read from the process environment, so a key
// set for one provider is never sent to another endpoint.
type Config struct {
	Format     Format
	BaseURL    string
	APIKey     string
	Model      string
	APIVersion string // Anthropic only; default 2026-10-21
	HTTPClient *http.Client
	// Proxy, when set, routes all model requests through the given proxy URL —
	// "http://", "https://" or "socks5://" (optionally with user:pass@). It only
	// affects the built-in client: when empty, the built-in client falls back to
	// the standard HTTP_PROXY / HTTPS_PROXY / NO_PROXY environment variables.
	// Ignored when the caller supplies its own HTTPClient.
	Proxy string
	// MaxRetries bounds transient-failure retries on the model request
	// establishment (connection reset / timeout / 429 / 5xx). 0 = default 3;
	// negative disables retrying. Only the pre-stream phase is retried (safe);
	// a mid-stream drop is surfaced as an error.
	MaxRetries int
	// RetryInterval, when > 0, replaces the exponential backoff between
	// establishment retries with this FIXED wait. 0 (default) keeps the
	// 0.5s/1s/2s… ladder capped at 8s. A host that wants a predictable,
	// operator-set cadence sets both MaxRetries and this.
	RetryInterval time.Duration
	// EmptyResponseRetries bounds how many times a completed-but-EMPTY response
	// is re-requested (FormatOpenAI only; see emptyResponseRetries). 0 = default
	// 2; negative disables. Separate from MaxRetries because an empty-response
	// retry re-sends the whole prompt rather than re-establishing a request.
	EmptyResponseRetries int
	// EmptyResponseInterval, when > 0, replaces the exponential backoff before
	// each empty-response retry with this FIXED wait. 0 keeps the ladder.
	EmptyResponseInterval time.Duration
	// RateLimit, when set, bounds the request rate shared across every caller of
	// the built Provider (per-second and/or per-minute). Retries do not count.
	RateLimit *RateLimit

	// ThinkingType sends "thinking":{"type":...} on FormatOpenAI for gateways
	// that take that switch. "" omits it. FormatAnthropic never sends it: current
	// Claude models choose their own thinking mode and reject a forced one.
	ThinkingType string
	// ReasoningEffort sets the reasoning strength. "" omits it. For OpenAI it maps
	// to "reasoning_effort"; for Anthropic to "output_config":{"effort":...}.
	ReasoningEffort string
	// MaxTokensField selects which request key carries the output cap on
	// FormatOpenAI (Chat Completions). "" (default) sends the classic
	// "max_tokens"; MaxTokensFieldCompletion sends "max_completion_tokens"
	// instead. Exactly one key is ever sent. Ignored by FormatAnthropic.
	MaxTokensField string

	limiter *rateLimiter // built from RateLimit by NewProvider; shared via the provider
}

// retries returns the effective retry count (0 → default 3; negative → 0).
func (c Config) retries() int {
	if c.MaxRetries == 0 {
		return 3
	}
	if c.MaxRetries < 0 {
		return 0
	}
	return c.MaxRetries
}

// retryDelay is the wait before establishment retry number attempt (0-based):
// the configured fixed interval when set, the exponential ladder otherwise.
func (c Config) retryDelay(attempt int) time.Duration {
	if c.RetryInterval > 0 {
		return c.RetryInterval
	}
	return expBackoff(attempt)
}

// emptyRetries returns the effective empty-response retry count
// (0 → default emptyResponseRetries; negative → 0).
func (c Config) emptyRetries() int {
	if c.EmptyResponseRetries == 0 {
		return emptyResponseRetries
	}
	if c.EmptyResponseRetries < 0 {
		return 0
	}
	return c.EmptyResponseRetries
}

// emptyRetryDelay is the wait before empty-response retry number attempt.
func (c Config) emptyRetryDelay(attempt int) time.Duration {
	if c.EmptyResponseInterval > 0 {
		return c.EmptyResponseInterval
	}
	return expBackoff(attempt)
}

// NewProvider builds a built-in provider for cfg.Format.
func NewProvider(cfg Config) (Provider, error) {
	if cfg.HTTPClient == nil {
		client, err := defaultHTTPClient(cfg.Proxy)
		if err != nil {
			return nil, err
		}
		cfg.HTTPClient = client
	}
	if cfg.RateLimit != nil {
		cfg.limiter = newRateLimiter(*cfg.RateLimit) // shared by the provider (cfg copies the pointer)
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.Model == "" {
		return nil, fmt.Errorf("llm: model is required")
	}
	switch cfg.Format {
	case FormatAnthropic:
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("llm: anthropic API key is required")
		}
		if cfg.BaseURL == "" {
			cfg.BaseURL = AnthropicBaseURL
		}
		if cfg.APIVersion == "" {
			cfg.APIVersion = "2026-10-21"
		}
		return &anthropicProvider{cfg: cfg}, nil
	case FormatOpenAI:
		// The key is optional: local model servers usually take none.
		if cfg.BaseURL == "" {
			return nil, fmt.Errorf("llm: base URL is required for an OpenAI-compatible endpoint")
		}
		return &openaiProvider{cfg: cfg}, nil
	default:
		return nil, fmt.Errorf("llm: unknown format %q (use anthropic or openai)", cfg.Format)
	}
}

// defaultHTTPClient builds the built-in HTTP client. A non-empty proxy
// ("http://", "https://" or "socks5://") routes every request through it;
// an empty proxy falls back to the standard *_PROXY environment variables
// (net/http's ProxyFromEnvironment). The transport is cloned from
// http.DefaultTransport so it keeps the standard timeouts and connection pool.
func defaultHTTPClient(proxy string) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if proxy == "" {
		tr.Proxy = http.ProxyFromEnvironment
		return &http.Client{Transport: tr}, nil
	}
	u, err := url.Parse(proxy)
	if err != nil {
		return nil, fmt.Errorf("llm: invalid proxy %q: %w", proxy, err)
	}
	switch u.Scheme {
	case "http", "https", "socks5": // supported by net/http's Transport.Proxy
	case "":
		return nil, fmt.Errorf("llm: proxy %q missing scheme (use http://, https:// or socks5://)", proxy)
	default:
		return nil, fmt.Errorf("llm: unsupported proxy scheme %q (use http, https or socks5)", u.Scheme)
	}
	tr.Proxy = http.ProxyURL(u)
	return &http.Client{Transport: tr}, nil
}

// joinSystem renders the system segments into a single string.
func joinSystem(segs []string) string {
	parts := make([]string, 0, len(segs))
	for _, s := range segs {
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n")
}
