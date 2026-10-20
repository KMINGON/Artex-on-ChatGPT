package chatgptllm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/Autumn-27/norma/llm"
)

// This data is part of the assistant message, so transcript persistence and
// compaction use the same lifetime as its visible content and tool calls.
type replayState struct {
	Version     int               `json:"version"`
	Provider    string            `json:"provider"`
	ContentHash string            `json:"content_hash"`
	Output      []json.RawMessage `json:"output"`
}

const replayProvider = "artex-chatgpt-responses"

func contentHash(content []llm.ContentBlock) string {
	b, _ := json.Marshal(content)
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:])
}

func attachReplayState(message *llm.Message, output []json.RawMessage) error {
	data, err := json.Marshal(replayState{Version: 1, Provider: replayProvider, ContentHash: contentHash(message.Content), Output: output})
	if err != nil {
		return failure("invalid_completion", "응답 상태를 저장할 수 없습니다", err)
	}
	message.ProviderData = data
	return nil
}

func replayItems(message llm.Message) ([]json.RawMessage, error) {
	var state replayState
	if message.Role != llm.RoleAssistant || json.Unmarshal(message.ProviderData, &state) != nil || state.Version != 1 || state.Provider != replayProvider || state.ContentHash != contentHash(message.Content) || len(state.Output) == 0 {
		return nil, failure("invalid_replay_state", "저장된 OpenAI 응답과 대화 내용이 일치하지 않습니다. 새 대화를 시작해 주세요", nil)
	}
	ids := map[string]bool{}
	for _, raw := range state.Output {
		var item outputItem
		if json.Unmarshal(raw, &item) != nil || item.ID == "" || ids[item.ID] {
			return nil, failure("invalid_replay_state", "저장된 응답 항목이 올바르지 않습니다", nil)
		}
		ids[item.ID] = true
		switch item.Type {
		case "message":
			if item.Role != "assistant" || !validPhase(item.Phase) {
				return nil, failure("invalid_replay_state", "저장된 응답 역할이나 단계가 올바르지 않습니다", nil)
			}
		case "reasoning":
		case "function_call":
			if item.CallID == "" || item.Name == "" || item.Namespace != "artex" || !validArguments([]byte(item.Arguments)) {
				return nil, failure("invalid_replay_state", "저장된 도구 호출이 올바르지 않습니다", nil)
			}
		default:
			return nil, failure("invalid_replay_state", "지원하지 않는 응답 상태입니다", nil)
		}
	}
	return state.Output, nil
}

func validPhase(phase string) bool {
	return phase == "" || phase == "commentary" || phase == "final_answer"
}
