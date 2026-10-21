package chatgptllm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/Autumn-27/norma/llm"
)

// Error keeps safe operational fields separate from the private upstream body.
// Attempts counts HTTP attempts in this one provider invocation, not model turns.
type Error struct {
	StatusCode    int
	Code          string
	Type          string
	Param         string
	RequestID     string
	ResponseID    string
	Stage         string
	EventType     string
	ErrorClass    string
	Attempts      int
	OutputEmitted bool
	APIKeyMode    bool
	Metadata      ResponseMetadata
	Shape         RequestShape
	Body          []byte `json:"-"`
	Message       string
	cause         error
}

func (e *Error) Error() string {
	name := "ChatGPT"
	if e.APIKeyMode {
		name = "OpenAI API"
	}
	detail := ""
	if e.StatusCode != 0 {
		detail = fmt.Sprintf("status %d", e.StatusCode)
	}
	if code := safeToken(e.Code); code != "" {
		if detail != "" {
			detail += ", "
		}
		detail += "code " + code
	}
	if detail != "" {
		detail = " (" + detail + ")"
	}
	return name + " 요청 실패" + detail + ": " + e.Message
}

func (e *Error) Unwrap() error    { return e.cause }
func (e *Error) NoFallback() bool { return !e.APIKeyMode }

// The provider owns the complete retry budget. Upper layers must not replay an
// entire tool run or start another same-auth budget after this final error.
func (e *Error) NoReplay() bool          { return true }
func (e *Error) RetryableSameAuth() bool { return false }
func (e *Error) OutputRejected() bool {
	if e.Stage == "stream" {
		return true
	}
	switch e.Code {
	case "invalid_completion", "invalid_tool_call", "invalid_stream", "interrupted_stream",
		"unsupported_output", "unsupported_event", "incomplete_response", "empty_response",
		"response_too_large", "auxiliary_output_limit", "auxiliary_timeout":
		return true
	default:
		return false
	}
}

// Diagnostics is the only representation intended for activity metadata/logs.
// It excludes credentials, upstream messages, bodies, and opaque replay data.
func (e *Error) Diagnostics() map[string]any {
	mode := "chatgpt_plan"
	if e.APIKeyMode {
		mode = "api_key"
	}
	return map[string]any{
		"status": e.StatusCode, "code": safeToken(e.Code), "type": safeToken(e.Type),
		"param": safeToken(e.Param), "request_id": safeToken(e.RequestID),
		"response_id": safeToken(e.ResponseID), "stage": safeToken(e.Stage),
		"event_type": safeToken(e.EventType), "error_class": safeToken(e.ErrorClass),
		"attempts": e.Attempts, "retries": max(e.Attempts-1, 0),
		"retry_layer": "provider", "output_emitted": e.OutputEmitted,
		"usage_received":    e.Metadata.UsageReceived,
		"response_metadata": e.Metadata,
		"auth_mode":         mode, "request_shape": e.Shape,
	}
}

func failure(code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, cause: cause, ErrorClass: "local"}
}

func (p *provider) annotate(err error, stage string, shape RequestShape, attempts int, emitted bool) *Error {
	var e *Error
	if !errors.As(err, &e) {
		e = failure("transport_error", "OpenAI에 연결할 수 없습니다", err)
	}
	e.APIKeyMode, e.Attempts, e.OutputEmitted, e.Shape = p.cfg.APIKey != "", attempts, emitted, shape
	if e.Code == "authorization_failed" || e.Code == "not_connected" || e.Code == "plan_usage_blocked" {
		e.Stage = "authorization"
	}
	if e.Stage == "" {
		e.Stage = stage
	}
	return e
}

var diagnosticToken = regexp.MustCompile(`^[a-zA-Z0-9_.:\[\]-]{1,128}$`)

func safeToken(s string) string {
	if !diagnosticToken.MatchString(s) || strings.HasPrefix(s, "sk-") {
		return ""
	}
	return s
}

func knownTransient(code string) bool {
	switch code {
	case "server_error", "internal_error", "internal_server_error", "service_unavailable",
		"rate_limit_exceeded", "subscription_sharing_usage_unavailable", "subscription_sharing_user_unavailable":
		return true
	default:
		return false
	}
}

func retryable(err error) bool {
	var e *Error
	if !errors.As(err, &e) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || e.OutputEmitted {
		return false
	}
	if e.Stage == "stream" {
		return (e.EventType == "response.failed" || e.EventType == "error") && knownTransient(e.Code)
	}
	if e.Code == "transport_error" {
		return true
	}
	if e.StatusCode != 408 && e.StatusCode != 429 && e.StatusCode < 500 {
		return false
	}
	if e.Code != "" {
		return knownTransient(e.Code)
	}
	// HTTP admission failures with no provider code retain the previous bounded
	// transport policy. Unknown explicit codes are never guessed to be transient.
	return e.StatusCode == 408 || e.StatusCode == 429 || e.StatusCode >= 500
}

func httpFailure(resp *http.Response) *Error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	e := responseFailure(body)
	e.StatusCode, e.RequestID, e.cause = resp.StatusCode, safeToken(resp.Header.Get("X-Request-Id")), err
	e.Stage = "admission"
	return e
}

func responseFailure(body []byte) *Error {
	type detail struct {
		Code    string `json:"code"`
		Type    string `json:"type"`
		Param   string `json:"param"`
		Message string `json:"message"`
	}
	var envelope struct {
		detail
		Error *detail `json:"error"`
	}
	e := &Error{Body: append([]byte(nil), body...), Message: "OpenAI가 요청을 완료하지 못했습니다"}
	if !strings.HasPrefix(strings.TrimSpace(string(body)), "{") || json.Unmarshal(body, &envelope) != nil {
		e.ErrorClass = "malformed_error"
		return e
	}
	d := envelope.detail
	if envelope.Error != nil {
		d = *envelope.Error
	} else if d.Type == "error" || d.Type == "response.failed" {
		d.Type = "" // Event type is not the upstream error classification.
	}
	e.Code, e.Type, e.Param = safeToken(d.Code), safeToken(d.Type), safeToken(d.Param)
	e.ErrorClass = "unknown_code"
	if e.Code == "" {
		e.ErrorClass = "missing_code"
	}
	switch e.Code {
	case "subscription_sharing_usage_limit_exceeded":
		e.Message = "ChatGPT 사용 한도에 도달했습니다. ChatGPT 사용량 설정을 확인해 주세요"
	case "insufficient_quota", "billing_hard_limit_reached":
		e.Message = "OpenAI API 사용 한도 또는 결제 한도에 도달했습니다"
	case "subscription_sharing_user_not_eligible", "subscription_sharing_route_not_supported", "chatpass_v2_scope_not_authorized", "chatpass_v2_invalid_authorization_context":
		e.Message = "이 연결에서 ChatGPT 플랜 사용이 허용되지 않았습니다"
	case "subscription_sharing_invalid_user":
		e.Message = "ChatGPT 연결의 인증 상태를 확인해 주세요"
	case "subscription_sharing_unsupported_capability":
		e.Message = "ChatGPT 플랜에서 지원하지 않는 요청입니다"
	case "subscription_sharing_usage_unavailable", "subscription_sharing_user_unavailable":
		e.Message = "ChatGPT 사용 가능 여부를 일시적으로 확인할 수 없습니다"
	case "context_length_exceeded":
		e.Message = "입력이 모델 문맥 한도를 초과했습니다 (context_length_exceeded)"
	case "invalid_access_program":
		e.Message = "선택한 모델과 Daybreak 프로그램의 조합을 확인해 주세요"
	case "unsupported_access_program":
		e.Message = "이 모델에서 선택한 Daybreak 프로그램을 지원하지 않습니다"
	case "access_program_not_enabled":
		e.Message = "API 키의 조직 승인과 프로젝트 Daybreak 접근 설정을 확인해 주세요"
	case "invalid_api_key":
		e.Message = "OpenAI API 키를 확인해 주세요"
	}
	if e.Message != "OpenAI가 요청을 완료하지 못했습니다" || knownTransient(e.Code) {
		e.ErrorClass = "known_code"
	}
	// Preserve the existing local compaction signal without printing the message.
	if e.Code == "" && strings.Contains(strings.ToLower(d.Message), "maximum context length") {
		e.Message = "입력이 모델 문맥 한도를 초과했습니다 (context_length_exceeded)"
	}
	return e
}

// RequestShape records sizes and structure only. It deliberately has no raw
// prompt, tool argument, call ID, model output, or encrypted state fields.
type RequestShape struct {
	Bytes              int      `json:"bytes"`
	InputItems         int      `json:"input_items"`
	InputTypes         []string `json:"input_types,omitempty"`
	ToolResultBytes    int      `json:"tool_result_bytes"`
	ToolResultCount    int      `json:"tool_result_count"`
	ToolCallPairsValid bool     `json:"tool_call_pairs_valid"`
	Phases             []string `json:"phases,omitempty"`
	ReplayMessages     int      `json:"replay_messages"`
}

func requestShape(body []byte, req llm.CompletionRequest) RequestShape {
	shape := RequestShape{Bytes: len(body), ToolCallPairsValid: true}
	for _, msg := range req.Messages {
		if len(msg.ProviderData) > 0 {
			shape.ReplayMessages++
		}
	}
	var request struct {
		Input []struct {
			Type, Phase, Output string
			CallID              string `json:"call_id"`
		} `json:"input"`
	}
	if json.Unmarshal(body, &request) != nil {
		shape.ToolCallPairsValid = false
		return shape
	}
	shape.InputItems = len(request.Input)
	calls := map[string]bool{}
	for _, item := range request.Input {
		if len(shape.InputTypes) < 128 {
			shape.InputTypes = append(shape.InputTypes, safeToken(item.Type))
		}
		if validPhase(item.Phase) && item.Phase != "" && len(shape.Phases) < 128 {
			shape.Phases = append(shape.Phases, item.Phase)
		}
		switch item.Type {
		case "function_call":
			if item.CallID == "" || calls[item.CallID] {
				shape.ToolCallPairsValid = false
			}
			calls[item.CallID] = true
		case "function_call_output":
			shape.ToolResultCount++
			shape.ToolResultBytes += len(item.Output)
			if !calls[item.CallID] {
				shape.ToolCallPairsValid = false
			}
			delete(calls, item.CallID)
		}
	}
	shape.ToolCallPairsValid = shape.ToolCallPairsValid && len(calls) == 0
	return shape
}
