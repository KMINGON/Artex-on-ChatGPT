package chatgptllm

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/Autumn-27/norma/llm"
)

type outputItem struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Role      string `json:"role"`
	Phase     string `json:"phase"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
	Summary []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary"`
}

type response struct {
	ID             string            `json:"id"`
	Status         string            `json:"status"`
	Output         []json.RawMessage `json:"output"`
	Model          string            `json:"model"`
	AccessPrograms json.RawMessage   `json:"access_programs"`
	Usage          *struct {
		Input   int `json:"input_tokens"`
		Output  int `json:"output_tokens"`
		Details struct {
			Cached int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	} `json:"usage"`
	Incomplete struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

type streamEvent struct {
	Type         string          `json:"type"`
	ItemID       string          `json:"item_id"`
	ContentIndex int             `json:"content_index"`
	OutputIndex  *int            `json:"output_index"`
	Delta        string          `json:"delta"`
	Item         json.RawMessage `json:"item"`
	Response     json.RawMessage `json:"response"`
}

type streamState struct {
	items        map[string]outputItem
	done         map[string]json.RawMessage
	indexes      map[string]int
	indexIDs     map[int]string
	text         map[string]*strings.Builder
	arguments    map[string]*strings.Builder
	size         int
	allowPartial bool
}

// Tools stay private until the terminal response validates. Norma otherwise
// starts executing a tool when the next normalized event arrives.
func consume(resp *http.Response, names map[string]string, yield func(llm.StreamEvent, error) bool) error {
	return consumeWithOptions(resp, names, false, yield)
}

func consumeWithOptions(resp *http.Response, names map[string]string, allowPartial bool, yield func(llm.StreamEvent, error) bool) (resultErr error) {
	return consumeObserved(resp, names, allowPartial, nil, nil, yield)
}

func consumeObserved(resp *http.Response, names map[string]string, allowPartial bool, observe func(ResponseMetadata), onError func(*Error), yield func(llm.StreamEvent, error) bool) (resultErr error) {
	state := streamState{
		items: map[string]outputItem{}, done: map[string]json.RawMessage{},
		indexes: map[string]int{}, indexIDs: map[int]string{},
		text: map[string]*strings.Builder{}, arguments: map[string]*strings.Builder{},
		allowPartial: allowPartial,
	}
	var lastEvent, responseID string
	defer func() {
		if e, ok := resultErr.(*Error); ok {
			e.Stage, e.EventType = "stream", safeToken(lastEvent)
			if e.ResponseID == "" {
				e.ResponseID = safeToken(responseID)
			}
		}
	}()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	var data strings.Builder
	var eventName string
	stopped := false
	process := func() (bool, error) {
		if data.Len() == 0 {
			eventName = ""
			return false, nil
		}
		raw := strings.TrimSuffix(data.String(), "\n")
		data.Reset()
		lastEvent = eventName
		if raw == "[DONE]" {
			return false, failure("interrupted_stream", "완료 확인 전에 응답이 끝났습니다", nil)
		}
		var event streamEvent
		if json.Unmarshal([]byte(raw), &event) != nil {
			return false, failure("invalid_stream", "이벤트 JSON이 올바르지 않습니다", nil)
		}
		if event.Type == "" {
			event.Type = eventName
		}
		lastEvent = event.Type
		var identity struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(event.Response, &identity) == nil && identity.ID != "" {
			responseID = identity.ID
		}
		eventName = ""
		state.size += len(raw)
		if state.size > 64<<20 {
			return false, failure("response_too_large", "응답이 처리 한도를 초과했습니다", nil)
		}
		// A quota denial must latch even if the consumer cancels while receiving
		// the terminal usage event or cancellation races with the parsed failure.
		if onError != nil && (event.Type == "response.failed" || event.Type == "error") {
			body := event.Response
			if len(body) == 0 {
				body = json.RawMessage(raw)
			}
			e := responseFailure(body)
			e.StatusCode, e.RequestID = resp.StatusCode, safeToken(resp.Header.Get("X-Request-Id"))
			e.ResponseID, e.EventType, e.Stage = safeToken(responseID), safeToken(event.Type), "stream"
			onError(e)
		}
		// Meter terminal usage independently of output acceptance. Rejected output
		// must not discard billed tokens or accidentally commit a tool.
		switch event.Type {
		case "response.completed", "response.incomplete", "response.failed", "error":
			var terminal response
			if json.Unmarshal(event.Response, &terminal) == nil {
				if observe != nil {
					observe(terminal.metadata())
				}
				if terminal.Usage != nil && !yield(llm.StreamEvent{Type: llm.SEMessageDelta, Usage: terminal.usage()}, nil) {
					stopped = true
					return true, nil
				}
			} else if observe != nil {
				observe(ResponseMetadata{})
			}
		}
		switch event.Type {
		case "response.output_item.added", "response.output_item.done":
			if err := state.recordItem(event); err != nil {
				return false, err
			}
		case "response.output_text.delta", "response.refusal.delta":
			if item, ok := state.items[event.ItemID]; !ok || item.Type != "message" {
				return false, failure("invalid_stream", "텍스트 항목 ID를 찾을 수 없습니다", nil)
			}
			if event.ContentIndex < 0 || state.done[event.ItemID] != nil {
				return false, failure("invalid_stream", "텍스트 이벤트 순서가 올바르지 않습니다", nil)
			}
			if err := state.recordIndex(event.ItemID, event.OutputIndex); err != nil {
				return false, err
			}
			key := textKey(event.ItemID, event.ContentIndex)
			appendBuffer(state.text, key, event.Delta)
			if event.Delta != "" && !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: event.Delta}, nil) {
				stopped = true
				return true, nil
			}
		case "response.function_call_arguments.delta":
			if item, ok := state.items[event.ItemID]; !ok || item.Type != "function_call" {
				return false, failure("invalid_stream", "도구 항목 ID를 찾을 수 없습니다", nil)
			}
			if state.done[event.ItemID] != nil {
				return false, failure("invalid_stream", "도구 이벤트 순서가 올바르지 않습니다", nil)
			}
			if err := state.recordIndex(event.ItemID, event.OutputIndex); err != nil {
				return false, err
			}
			appendBuffer(state.arguments, event.ItemID, event.Delta)
		case "response.completed":
			var final response
			if json.Unmarshal(event.Response, &final) != nil || final.Status != "completed" {
				return false, failure("invalid_completion", "완료 응답이 올바르지 않습니다", nil)
			}
			events, err := state.finish(final, names)
			if err != nil {
				return false, err
			}
			for _, ev := range events {
				if !yield(ev, nil) {
					stopped = true
					break
				}
			}
			return true, nil
		case "response.failed", "error":
			body := event.Response
			if len(body) == 0 {
				body = json.RawMessage(raw)
			}
			err := responseFailure(body)
			err.StatusCode, err.RequestID = resp.StatusCode, resp.Header.Get("X-Request-Id")
			return false, err
		case "response.incomplete":
			var final response
			if json.Unmarshal(event.Response, &final) != nil {
				return false, failure("invalid_completion", "미완료 응답이 올바르지 않습니다", nil)
			}
			if allowPartial && final.Status == "incomplete" && final.Incomplete.Reason == "max_output_tokens" {
				events, err := state.finish(final, names)
				if err != nil {
					return false, err
				}
				for _, ev := range events {
					if !yield(ev, nil) {
						stopped = true
						break
					}
				}
				return true, nil
			}
			err := failure("incomplete_response", "응답이 완성되지 않았습니다", nil)
			err.StatusCode = resp.StatusCode
			err.Param, err.Body, err.RequestID = final.Incomplete.Reason, append([]byte(nil), event.Response...), resp.Header.Get("X-Request-Id")
			return false, err
		case "response.created", "response.queued", "response.in_progress", "response.metadata", "response.content_part.added", "response.content_part.done", "response.output_text.done", "response.refusal.done", "response.function_call_arguments.done", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done", "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done", "response.reasoning_text.delta", "response.reasoning_text.done":
			// Final output is authoritative; intermediate summaries are not replayed.
		default:
			return false, failure("unsupported_event", "지원하지 않는 응답 이벤트입니다", nil)
		}
		return false, nil
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			done, err := process()
			if err != nil || done {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			data.WriteByte('\n')
			if data.Len() > 16<<20 {
				return failure("response_too_large", "이벤트가 처리 한도를 초과했습니다", nil)
			}
		} else if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
	}
	if stopped {
		return nil
	}
	if scanner.Err() != nil {
		if errors.Is(scanner.Err(), bufio.ErrTooLong) {
			return failure("response_too_large", "이벤트가 처리 한도를 초과했습니다", scanner.Err())
		}
		return failure("interrupted_stream", "응답 스트림이 중단되었습니다", scanner.Err())
	}
	if done, err := process(); done || err != nil {
		return err
	}
	return failure("interrupted_stream", "완료 확인 전에 응답이 끝났습니다", io.ErrUnexpectedEOF)
}

func textKey(id string, index int) string { return fmt.Sprintf("%s:%d", id, index) }

func appendBuffer(buffers map[string]*strings.Builder, key, value string) {
	if buffers[key] == nil {
		buffers[key] = &strings.Builder{}
	}
	buffers[key].WriteString(value)
}

func bufferText(buffers map[string]*strings.Builder, key string) string {
	if buffers[key] == nil {
		return ""
	}
	return buffers[key].String()
}

func (s *streamState) recordIndex(id string, index *int) error {
	if index == nil {
		return nil // Older streams with authoritative final output omit indexes.
	}
	if prior, ok := s.indexes[id]; *index < 0 || (ok && prior != *index) {
		return failure("invalid_stream", "출력 항목 순서가 변경되었습니다", nil)
	}
	if other, ok := s.indexIDs[*index]; ok && other != id {
		return failure("invalid_stream", "출력 항목 순서가 중복되었습니다", nil)
	}
	s.indexes[id], s.indexIDs[*index] = *index, id
	return nil
}

func (s *streamState) recordItem(event streamEvent) error {
	var item outputItem
	if json.Unmarshal(event.Item, &item) != nil || item.ID == "" {
		return failure("invalid_stream", "출력 항목 ID가 올바르지 않습니다", nil)
	}
	if item.Type != "message" && item.Type != "function_call" && item.Type != "reasoning" {
		return failure("unsupported_output", "지원하지 않는 응답 항목입니다", nil)
	}
	prior, exists := s.items[item.ID]
	if event.Type == "response.output_item.added" {
		if exists {
			return failure("invalid_stream", "출력 항목 ID가 중복되었습니다", nil)
		}
		s.items[item.ID] = item
		if item.Arguments != "" {
			appendBuffer(s.arguments, item.ID, item.Arguments)
		}
	} else {
		if !exists || s.done[item.ID] != nil || !sameItemIdentity(prior, item) {
			return failure("invalid_stream", "완료 항목 식별자가 올바르지 않습니다", nil)
		}
		if item.Status != "" && item.Status != "completed" && !(s.allowPartial && item.Status == "incomplete" && item.Type != "function_call") {
			return failure("invalid_completion", "출력 항목이 완성되지 않았습니다", nil)
		}
		// Keep the original object, including phase and opaque reasoning fields.
		s.done[item.ID] = append(json.RawMessage(nil), event.Item...)
	}
	return s.recordIndex(item.ID, event.OutputIndex)
}

func sameItemIdentity(prior, item outputItem) bool {
	if prior.Type != item.Type {
		return false
	}
	switch item.Type {
	case "message":
		return (prior.Role == "" || prior.Role == item.Role) && (prior.Phase == "" || prior.Phase == item.Phase)
	case "function_call":
		return prior.CallID == item.CallID && prior.Name == item.Name && prior.Namespace == item.Namespace
	default:
		return true
	}
}

// Some ChatGPT streams omit completed.output. Only a complete, indexed set of
// done items can replace it, and only after the response itself has completed.
func (s *streamState) completedOutput() ([]json.RawMessage, error) {
	if len(s.items) == 0 || len(s.done) != len(s.items) || len(s.indexes) != len(s.items) {
		return nil, failure("invalid_completion", "완료 응답에서 출력 항목이 누락되었습니다", nil)
	}
	output := make([]json.RawMessage, len(s.items))
	for id := range s.items {
		index := s.indexes[id]
		if index >= len(output) || s.done[id] == nil {
			return nil, failure("invalid_completion", "완료 응답에서 출력 순서가 누락되었습니다", nil)
		}
		output[index] = s.done[id]
	}
	return output, nil
}

func (s *streamState) finish(final response, names map[string]string) ([]llm.StreamEvent, error) {
	partial := final.Status == "incomplete"
	if partial {
		for _, item := range s.items {
			if item.Type == "function_call" {
				return nil, failure("incomplete_response", "미완료 도구 호출은 실행하지 않습니다", nil)
			}
		}
	}
	if len(final.Output) == 0 {
		var err error
		final.Output, err = s.completedOutput()
		if err != nil {
			return nil, err
		}
	}
	var events []llm.StreamEvent
	message := llm.Message{Role: llm.RoleAssistant}
	seenIDs, seenCalls, seenText := map[string]bool{}, map[string]bool{}, map[string]bool{}
	hasTool, hasContent := false, false
	for index, raw := range final.Output {
		var item outputItem
		if json.Unmarshal(raw, &item) != nil || item.ID == "" || seenIDs[item.ID] {
			return nil, failure("invalid_completion", "완료 항목 ID가 올바르지 않습니다", nil)
		}
		if item.Status != "" && item.Status != "completed" && !(partial && item.Status == "incomplete" && item.Type != "function_call") {
			return nil, failure("invalid_completion", "출력 항목이 완성되지 않았습니다", nil)
		}
		seenIDs[item.ID] = true
		if prior, ok := s.items[item.ID]; ok && !sameItemIdentity(prior, item) {
			return nil, failure("invalid_completion", "출력 항목 식별자가 변경되었습니다", nil)
		}
		if prior, ok := s.indexes[item.ID]; ok && prior != index {
			return nil, failure("invalid_completion", "완료 항목 순서가 변경되었습니다", nil)
		}
		if rawDone, ok := s.done[item.ID]; ok {
			var done outputItem
			_ = json.Unmarshal(rawDone, &done) // Validated when recorded.
			// A terminal item can omit status; both forms mean completed here.
			done.Status, item.Status = "", ""
			if !reflect.DeepEqual(done, item) {
				return nil, failure("invalid_completion", "완료 항목 내용이 변경되었습니다", nil)
			}
		}
		switch item.Type {
		case "message":
			if item.Role != "assistant" || !validPhase(item.Phase) {
				return nil, failure("invalid_completion", "응답 역할이 올바르지 않습니다", nil)
			}
			for index, part := range item.Content {
				text := part.Text
				if part.Type == "refusal" {
					text = part.Refusal
				} else if part.Type != "output_text" {
					return nil, failure("unsupported_output", "지원하지 않는 응답 콘텐츠입니다", nil)
				}
				key := textKey(item.ID, index)
				seenText[key] = true
				if !strings.HasPrefix(text, bufferText(s.text, key)) {
					return nil, failure("invalid_completion", "완료 텍스트가 스트림과 일치하지 않습니다", nil)
				}
				if rest := strings.TrimPrefix(text, bufferText(s.text, key)); rest != "" {
					events = append(events, llm.StreamEvent{Type: llm.SETextDelta, Text: rest})
				}
				if text != "" {
					message.Content = append(message.Content, llm.TextBlock(text))
				}
				hasContent = hasContent || text != ""
			}
		case "function_call":
			if partial {
				return nil, failure("incomplete_response", "미완료 도구 호출은 실행하지 않습니다", nil)
			}
			name, ok := names[item.Name]
			if !ok || item.Namespace != "artex" || item.CallID == "" || seenCalls[item.CallID] || !validArguments([]byte(item.Arguments)) {
				return nil, failure("invalid_tool_call", "완료된 도구 호출이 올바르지 않습니다", nil)
			}
			if prior, ok := s.items[item.ID]; ok && (prior.CallID != item.CallID || prior.Name != item.Name || prior.Namespace != item.Namespace) {
				return nil, failure("invalid_tool_call", "도구 호출 식별자가 변경되었습니다", nil)
			}
			if !strings.HasPrefix(item.Arguments, bufferText(s.arguments, item.ID)) {
				return nil, failure("invalid_tool_call", "도구 인수가 스트림과 일치하지 않습니다", nil)
			}
			seenCalls[item.CallID], hasTool, hasContent = true, true, true
			message.Content = append(message.Content, llm.ContentBlock{Type: llm.BlockToolUse, ID: item.CallID, Name: name, Input: json.RawMessage(item.Arguments)})
			events = append(events, llm.StreamEvent{Type: llm.SEToolUseStart, ToolID: item.CallID, ToolName: name}, llm.StreamEvent{Type: llm.SEToolInputJSON, Text: item.Arguments})
		case "reasoning":
			var parts []string
			for _, summary := range item.Summary {
				if summary.Type != "summary_text" {
					return nil, failure("unsupported_output", "지원하지 않는 추론 요약입니다", nil)
				}
				parts = append(parts, summary.Text)
			}
			if text := strings.Join(parts, "\n"); text != "" {
				message.Content = append(message.Content, llm.ContentBlock{Type: llm.BlockThinking, Thinking: text})
				// Empty text events flush a preceding thinking block in Norma; they do
				// not fabricate text or expose encrypted reasoning as a visible summary.
				events = append(events, llm.StreamEvent{Type: llm.SETextDelta}, llm.StreamEvent{Type: llm.SEThinkingDelta, Text: text})
			}
		default:
			return nil, failure("unsupported_output", "지원하지 않는 완료 항목입니다", nil)
		}
	}
	for id := range s.items {
		if !seenIDs[id] {
			return nil, failure("invalid_completion", "완료 응답에서 출력 항목이 누락되었습니다", nil)
		}
	}
	for key := range s.text {
		if !seenText[key] {
			return nil, failure("invalid_completion", "완료 응답에서 텍스트가 누락되었습니다", nil)
		}
	}
	if !hasContent {
		return nil, failure("empty_response", "모델이 빈 응답을 반환했습니다", nil)
	}
	if !partial {
		if err := attachReplayState(&message, final.Output); err != nil {
			return nil, err
		}
	}
	stop := "end_turn"
	if hasTool {
		stop = "tool_use"
	} else if partial {
		// A truncated text continuation is usable, but incomplete opaque state
		// must not be promoted to completed replay data.
		stop = "max_tokens"
	}
	events = append(events, llm.StreamEvent{Type: llm.SEMessageFinal, FinalMessage: &message}, llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: stop}, llm.StreamEvent{Type: llm.SEMessageStop})
	return events, nil
}
