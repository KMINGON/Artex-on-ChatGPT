package chatgptllm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/Autumn-27/norma/llm"
)

const reasoningPrefix = "artex-chatgpt-reasoning-v1:"

var validToolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func wireToolName(name string) string {
	if validToolName.MatchString(name) {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return "tool_" + hex.EncodeToString(sum[:16])
}

func encodeRequest(cfg Config, req llm.CompletionRequest) ([]byte, map[string]string, error) {
	names := make(map[string]string, len(req.Tools))
	functions := make([]map[string]any, 0, len(req.Tools))
	for _, tool := range req.Tools {
		wire := wireToolName(tool.Name)
		if tool.Name == "" || names[wire] != "" {
			return nil, nil, failure("invalid_tool", "도구 이름이 비어 있거나 중복되었습니다", nil)
		}
		names[wire] = tool.Name
		parameters := tool.InputSchema
		if parameters == nil {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		functions = append(functions, map[string]any{"type": "function", "name": wire, "description": tool.Description, "parameters": parameters, "strict": false})
	}
	input := make([]any, 0, len(req.Messages))
	for _, msg := range req.Messages {
		if msg.Role != llm.RoleUser && msg.Role != llm.RoleAssistant {
			return nil, nil, failure("unsupported_role", "지원하지 않는 대화 역할입니다", nil)
		}
		if len(msg.ProviderData) > 0 {
			items, err := replayItems(msg)
			if err != nil {
				return nil, nil, err
			}
			for _, item := range items {
				input = append(input, item)
			}
			continue
		}
		for _, block := range msg.Content {
			switch block.Type {
			case llm.BlockText:
				kind := "input_text"
				if msg.Role == llm.RoleAssistant {
					kind = "output_text"
				}
				input = append(input, map[string]any{"type": "message", "role": string(msg.Role), "content": []any{map[string]any{"type": kind, "text": block.Text}}})
			case llm.BlockToolUse:
				if msg.Role != llm.RoleAssistant || block.ID == "" || block.Name == "" || !validArguments(block.Input) {
					return nil, nil, failure("invalid_tool_call", "도구 호출 기록이 올바르지 않습니다", nil)
				}
				input = append(input, map[string]any{"type": "function_call", "call_id": block.ID, "name": wireToolName(block.Name), "namespace": "artex", "arguments": string(block.Input)})
			case llm.BlockToolResult:
				if msg.Role != llm.RoleUser || block.ToolUseID == "" {
					return nil, nil, failure("invalid_tool_result", "도구 결과 기록이 올바르지 않습니다", nil)
				}
				var parts []string
				for _, content := range block.Content {
					if content.Type != llm.BlockText {
						return nil, nil, unsupportedBlock(content.Type)
					}
					parts = append(parts, content.Text)
				}
				output := strings.Join(parts, "\n")
				if block.IsError {
					output = "도구 실행 오류:\n" + output
				}
				input = append(input, map[string]any{"type": "function_call_output", "call_id": block.ToolUseID, "output": output})
			case llm.BlockThinking:
				// Context projection may intentionally discard opaque replay state
				// after pruning tools. A visible summary alone is not model state.
				if msg.Role == llm.RoleAssistant && block.Signature == "" {
					continue
				}
				if msg.Role != llm.RoleAssistant || !strings.HasPrefix(block.Signature, reasoningPrefix) {
					return nil, nil, failure("unsupported_reasoning", "이 제공자의 추론 기록을 ChatGPT로 재전송할 수 없습니다. 새 대화를 시작해 주세요", nil)
				}
				raw := json.RawMessage(strings.TrimPrefix(block.Signature, reasoningPrefix))
				var item struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(raw, &item) != nil || item.Type != "reasoning" {
					return nil, nil, failure("invalid_reasoning", "추론 기록이 올바르지 않습니다", nil)
				}
				input = append(input, raw)
			default:
				return nil, nil, unsupportedBlock(block.Type)
			}
		}
	}
	// An allowlist avoids leaking Norma's temperature/output caps into this route.
	body := map[string]any{"model": cfg.Model, "input": input, "store": false, "stream": true, "instructions": strings.Join(req.System, "\n\n")}
	if auxiliaryRequest(req) {
		body["instructions"] = fmt.Sprintf("%s\n\nReturn a concise result within approximately %d tokens and %d UTF-8 bytes. Follow the required output format exactly; omit preambles and repeated source material.", body["instructions"], req.MaxTokens, auxiliaryByteLimit(req))
	}
	if len(functions) > 0 {
		body["tools"] = []any{map[string]any{"type": "namespace", "name": "artex", "description": "ARTEX local tools", "tools": functions}}
	}
	if req.Thinking != "disabled" {
		reasoning := map[string]any{"summary": "auto"}
		if cfg.ReasoningEffort != "" {
			reasoning["effort"] = cfg.ReasoningEffort
		}
		body["reasoning"] = reasoning
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, nil, failure("invalid_request", "요청을 JSON으로 변환할 수 없습니다", err)
	}
	return encoded, names, nil
}

func unsupportedBlock(kind llm.BlockType) error {
	return failure("unsupported_content", fmt.Sprintf("현재 ChatGPT 연결에서 지원하지 않는 콘텐츠입니다: %q", kind), nil)
}

func validArguments(raw []byte) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}
