package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strings"
)

type anthropicProvider struct{ cfg Config }

type anthropicSystemBlock struct {
	Type         string         `json:"type"`
	Text         string         `json:"text"`
	CacheControl map[string]any `json:"cache_control,omitempty"`
}

// anthropicReq carries no sampling parameters and no thinking switch: current
// Claude models reject both, and choose their own thinking mode when it is omitted.
type anthropicReq struct {
	Model         string                 `json:"model"`
	System        []anthropicSystemBlock `json:"system,omitempty"`
	Messages      []anthropicMessage     `json:"messages"`
	Tools         []anthropicTool        `json:"tools,omitempty"`
	MaxTokens     int                    `json:"max_tokens"`
	StopSequences []string               `json:"stop_sequences,omitempty"`
	OutputConfig  *anthropicOutputConfig `json:"output_config,omitempty"`
	Stream        bool                   `json:"stream"`
}

type anthropicMessage struct {
	Role    Role             `json:"role"`
	Content []map[string]any `json:"content"`
}

// anthropicOutputConfig carries the reasoning strength via
// "output_config":{"effort":"low"|"medium"|"high"|"xhigh"|"max"}.
type anthropicOutputConfig struct {
	Effort string `json:"effort"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// Output caps used when the host sets none. max_tokens is required and also
// covers the model's thinking, so a low cap truncates ordinary turns.
const (
	anthropicStreamMaxTokens   = 64000
	anthropicCompleteMaxTokens = 16000 // keeps a non-streaming reply inside HTTP timeouts
)

func (p *anthropicProvider) buildBody(req CompletionRequest, stream bool) ([]byte, error) {
	maxTok := req.MaxTokens
	if maxTok == 0 {
		maxTok = anthropicCompleteMaxTokens
		if stream {
			maxTok = anthropicStreamMaxTokens
		}
	}
	body := anthropicReq{
		Model:         p.cfg.Model,
		Messages:      toAnthropicMessages(req.Messages),
		MaxTokens:     maxTok,
		StopSequences: req.Stop,
		Stream:        stream,
		System:        buildAnthropicSystem(req),
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, anthropicTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	if p.cfg.ReasoningEffort != "" {
		body.OutputConfig = &anthropicOutputConfig{Effort: p.cfg.ReasoningEffort}
	}
	return json.Marshal(body)
}

// toAnthropicMessages renders the neutral history on the Messages API wire.
// Thinking blocks are never replayed: a block is valid only in the unedited
// conversation that produced it, and this harness compacts and rewrites
// history. Dropping every one of them is always accepted. A turn left with no
// content is dropped (an empty content array is rejected), and the neighbours
// it leaves adjacent are merged because roles must alternate.
func toAnthropicMessages(msgs []Message) []anthropicMessage {
	out := make([]anthropicMessage, 0, len(msgs))
	for _, m := range msgs {
		content := anthropicContent(m.Content)
		if len(content) == 0 {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Role == m.Role {
			out[n-1].Content = append(out[n-1].Content, content...)
			continue
		}
		out = append(out, anthropicMessage{Role: m.Role, Content: content})
	}
	return out
}

func anthropicContent(blocks []ContentBlock) []map[string]any {
	out := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case BlockText:
			if strings.TrimSpace(b.Text) != "" { // blank text blocks are rejected
				out = append(out, map[string]any{"type": "text", "text": b.Text})
			}
		case BlockImage:
			out = append(out, anthropicImage(b.Image))
		case BlockToolUse:
			out = append(out, map[string]any{"type": "tool_use", "id": b.ID, "name": b.Name, "input": toolInputObject(b.Input)})
		case BlockToolResult:
			result := map[string]any{"type": "tool_result", "tool_use_id": b.ToolUseID}
			if content := anthropicContent(b.Content); len(content) > 0 {
				result["content"] = content
			}
			if b.IsError {
				result["is_error"] = true
			}
			out = append(out, result)
		}
	}
	return out
}

// anthropicImage sends the image bytes; an image that can no longer be read is
// described in text so the model knows something was there.
func anthropicImage(src *ImageSource) map[string]any {
	dataURL, err := src.DataURL()
	_, data, ok := strings.Cut(dataURL, ";base64,")
	if err != nil || !ok {
		return map[string]any{"type": "text", "text": src.Description()}
	}
	return map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": src.MIMEType, "data": data}}
}

// toolInputObject returns the recorded tool input as the JSON object the API
// requires. Anything else (a truncated call kept in history) is carried as text.
func toolInputObject(input json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) == 0 {
		return json.RawMessage("{}")
	}
	if trimmed[0] == '{' && json.Valid(trimmed) {
		return trimmed
	}
	wrapped, _ := json.Marshal(map[string]string{"unparsed_input": string(input)})
	return wrapped
}

// buildAnthropicSystem splits the system prompt at DynamicBoundary, placing a
// cache breakpoint on the static prefix (FR-02.2).
func buildAnthropicSystem(req CompletionRequest) []anthropicSystemBlock {
	if len(req.System) == 0 {
		return nil
	}
	b := req.DynamicBoundary
	if b <= 0 {
		// No boundary set → no cache breakpoint.
		return []anthropicSystemBlock{{Type: "text", Text: joinSystem(req.System)}}
	}
	if b >= len(req.System) {
		// Boundary at/after the end → the whole system prompt is static; cache it
		// all in one block (an all-static prompt should still be cacheable).
		return []anthropicSystemBlock{{Type: "text", Text: joinSystem(req.System), CacheControl: map[string]any{"type": "ephemeral"}}}
	}
	return []anthropicSystemBlock{
		{Type: "text", Text: joinSystem(req.System[:b]), CacheControl: map[string]any{"type": "ephemeral"}},
		{Type: "text", Text: joinSystem(req.System[b:])},
	}
}

func (p *anthropicProvider) headers(accept string) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Set("content-type", "application/json")
		r.Header.Set("x-api-key", p.cfg.APIKey)
		r.Header.Set("anthropic-version", p.cfg.APIVersion)
		r.Header.Set("accept", accept)
	}
}

func (p *anthropicProvider) Stream(ctx context.Context, req CompletionRequest) iter.Seq2[StreamEvent, error] {
	return func(yield func(StreamEvent, error) bool) {
		body, err := p.buildBody(req, true)
		if err != nil {
			yield(StreamEvent{}, err)
			return
		}
		resp, err := doStream(ctx, p.cfg, p.cfg.BaseURL+"/v1/messages", body, p.headers("text/event-stream"), anthropicName)
		if err != nil {
			yield(StreamEvent{}, err)
			return
		}
		defer resp.Body.Close()
		scan := newSSEScanner(resp.Body)
		for {
			_, data, serr := scan.next()
			if serr == io.EOF {
				// HTTP 200 is not completion: only message_stop ends a turn.
				serr = &IncompleteStreamError{Provider: anthropicName}
			}
			if serr != nil {
				yield(StreamEvent{}, serr)
				return
			}
			if data == "" {
				continue
			}
			ev, ok, perr := parseAnthropicFrame(data)
			if perr != nil {
				yield(StreamEvent{}, perr)
				return
			}
			if !ok {
				continue
			}
			if !yield(ev, nil) || ev.Type == SEMessageStop {
				return
			}
		}
	}
}

// Complete performs a real non-streaming completion: it sends stream:false and
// parses the single JSON response from /v1/messages into a full assistant
// Message, returning it with the stop reason and normalized usage.
func (p *anthropicProvider) Complete(ctx context.Context, req CompletionRequest) (Message, string, Usage, error) {
	body, err := p.buildBody(req, false)
	if err != nil {
		return Message{}, "", Usage{}, err
	}
	resp, err := doStream(ctx, p.cfg, p.cfg.BaseURL+"/v1/messages", body, p.headers("application/json"), anthropicName)
	if err != nil {
		return Message{}, "", Usage{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Message{}, "", Usage{}, err
	}
	return parseAnthropicResponse(raw)
}

// anthropicStopDetails explains a refusal; it is absent for every other stop.
type anthropicStopDetails struct {
	Category    string `json:"category"`
	Explanation string `json:"explanation"`
}

func (d *anthropicStopDetails) refusal() *RefusalError {
	e := &RefusalError{Provider: anthropicName}
	if d != nil {
		e.Category, e.Explanation = d.Category, d.Explanation
	}
	return e
}

// parseAnthropicResponse decodes a non-streaming Messages API response into the
// neutral Message model. Thinking is kept as display text only: its signature
// is never stored because the block is never replayed.
func parseAnthropicResponse(raw []byte) (Message, string, Usage, error) {
	var r struct {
		Type    string `json:"type"`
		Content []struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			Thinking string          `json:"thinking"`
			ID       string          `json:"id"`
			Name     string          `json:"name"`
			Input    json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason  string                `json:"stop_reason"`
		StopDetails *anthropicStopDetails `json:"stop_details"`
		Usage       *anthropicUsage       `json:"usage"`
		Error       *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return Message{}, "", Usage{}, fmt.Errorf("%s 응답을 해석하지 못했습니다: %w (body: %s)", anthropicName, err, truncate(string(raw), 500))
	}
	if r.Error != nil || r.Type == "error" {
		msg := "error response"
		if r.Error != nil {
			msg = r.Error.Message
		}
		return Message{}, "", Usage{}, fmt.Errorf("%s 요청 실패: %s", anthropicName, msg)
	}
	var usage Usage
	if r.Usage != nil {
		usage = r.Usage.norm()
	}
	if r.StopReason == "refusal" {
		return Message{}, "", usage, r.StopDetails.refusal()
	}
	var blocks []ContentBlock
	for _, b := range r.Content {
		switch b.Type {
		case "text":
			if b.Text != "" {
				blocks = append(blocks, TextBlock(b.Text))
			}
		case "thinking":
			if b.Thinking != "" {
				blocks = append(blocks, ContentBlock{Type: BlockThinking, Thinking: b.Thinking})
			}
		case "tool_use":
			in := b.Input
			if len(in) == 0 {
				in = json.RawMessage("{}")
			}
			blocks = append(blocks, ContentBlock{Type: BlockToolUse, ID: b.ID, Name: b.Name, Input: in})
		}
	}
	return Message{Role: RoleAssistant, Content: blocks}, r.StopReason, usage, nil
}

func parseAnthropicFrame(data string) (StreamEvent, bool, error) {
	var f struct {
		Type         string `json:"type"`
		ContentBlock struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"content_block"`
		Delta struct {
			Type        string                `json:"type"`
			Text        string                `json:"text"`
			PartialJSON string                `json:"partial_json"`
			Thinking    string                `json:"thinking"`
			StopReason  string                `json:"stop_reason"`
			StopDetails *anthropicStopDetails `json:"stop_details"`
		} `json:"delta"`
		Usage   *anthropicUsage `json:"usage"`
		Message struct {
			Usage *anthropicUsage `json:"usage"`
		} `json:"message"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(data), &f); err != nil {
		return StreamEvent{}, false, nil // tolerate non-JSON keepalives
	}
	switch f.Type {
	case "message_start":
		if f.Message.Usage != nil {
			// Anthropic reports the full input side (input + cache read/creation) in
			// BOTH message_start and message_delta. To avoid double-counting when the
			// accumulator folds both events, message_start carries ONLY the input side
			// and message_delta carries ONLY output. The initial output_tokens here is
			// a placeholder (≈1), so drop it — the real output comes from message_delta.
			u := f.Message.Usage.norm()
			u.OutputTokens = 0
			return StreamEvent{Type: SEMessageStart, Usage: u}, true, nil
		}
	case "content_block_start":
		if f.ContentBlock.Type == "tool_use" {
			return StreamEvent{Type: SEToolUseStart, ToolID: f.ContentBlock.ID, ToolName: f.ContentBlock.Name}, true, nil
		}
	case "content_block_delta":
		switch f.Delta.Type {
		case "text_delta":
			return StreamEvent{Type: SETextDelta, Text: f.Delta.Text}, true, nil
		case "thinking_delta":
			return StreamEvent{Type: SEThinkingDelta, Text: f.Delta.Thinking}, true, nil
		case "input_json_delta":
			return StreamEvent{Type: SEToolInputJSON, Text: f.Delta.PartialJSON}, true, nil
		}
		// signature_delta is dropped: thinking blocks are never replayed.
	case "message_delta":
		if f.Delta.StopReason == "refusal" {
			return StreamEvent{}, false, f.Delta.StopDetails.refusal()
		}
		ev := StreamEvent{Type: SEMessageDelta, StopReason: f.Delta.StopReason}
		if f.Usage != nil {
			// Only output here — the input side (input/cache) was already counted at
			// message_start; message_delta re-echoes it, so counting it again would
			// double the input/cache totals.
			ev.Usage = Usage{OutputTokens: f.Usage.OutputTokens}
		}
		return ev, true, nil
	case "message_stop":
		return StreamEvent{Type: SEMessageStop}, true, nil
	case "error":
		msg := "stream error"
		if f.Error != nil {
			msg = f.Error.Message
		}
		return StreamEvent{}, false, fmt.Errorf("%s 스트림 오류: %s", anthropicName, msg)
	}
	return StreamEvent{}, false, nil
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// norm maps Anthropic usage to the canonical Usage. InputTokens is the TOTAL input
// INCLUDING cached tokens (OpenAI-style: prompt_tokens semantics) — Anthropic's raw
// input_tokens counts only the non-cached portion, so cache read+creation are folded
// in. CacheReadTokens/CacheWriteTokens are the cached SUBSET (⊆ InputTokens).
func (u anthropicUsage) norm() Usage {
	return Usage{
		InputTokens:      u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
	}
}
