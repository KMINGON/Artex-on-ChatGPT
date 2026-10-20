package llm

import (
	"context"
	"iter"
)

// StreamEventType enumerates the normalized streaming events every adapter
// emits, regardless of wire format.
type StreamEventType string

const (
	SEMessageStart      StreamEventType = "message_start"
	SETextDelta         StreamEventType = "text_delta"
	SEThinkingDelta     StreamEventType = "thinking_delta"
	SEThinkingSignature StreamEventType = "thinking_signature"
	SEToolUseStart      StreamEventType = "tool_use_start"
	SEToolInputJSON     StreamEventType = "tool_input_delta"
	SEMessageFinal      StreamEventType = "message_final"
	SEMessageDelta      StreamEventType = "message_delta" // carries stop_reason / usage
	SEMessageStop       StreamEventType = "message_stop"
)

// StreamEvent is one normalized model-level streaming event (FR-03.5).
type StreamEvent struct {
	Type       StreamEventType
	Text       string // text/thinking/tool_input JSON fragment
	ToolID     string // tool_use_start
	ToolName   string // tool_use_start
	StopReason string // message_delta
	Usage      Usage  // message_start / message_delta
	// FinalMessage supplies an authoritative completed turn after live deltas.
	// Emit at most once, after all content/tool events and before message_stop.
	// Consumers must still process the usual tool events for tool execution.
	FinalMessage *Message // message_final; never display as an event payload
}

// Provider streams normalized events or returns one atomic assembled result.
// Complete may aggregate a streaming transport; its caller sees no partial
// message on failure. A streamed terminal error ends iteration as (zero, err).
type Provider interface {
	Stream(ctx context.Context, req CompletionRequest) iter.Seq2[StreamEvent, error]
	Complete(ctx context.Context, req CompletionRequest) (Message, string, Usage, error)
}
