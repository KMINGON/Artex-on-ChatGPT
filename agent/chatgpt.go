package agent

import (
	"context"
	"iter"

	"github.com/Autumn-27/artex/chatgptllm"
	"github.com/Autumn-27/norma/llm"
)

// Invalid persisted subscription settings must remain selected, even if the
// caller normally resolves another provider when construction fails.
type unavailableChatGPTProvider struct{}

func (unavailableChatGPTProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) { yield(llm.StreamEvent{}, chatGPTConfigError()) }
}

func (unavailableChatGPTProvider) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return llm.Message{}, "", llm.Usage{}, chatGPTConfigError()
}

func chatGPTConfigError() error {
	return &chatgptllm.Error{Code: "invalid_config", Message: "ChatGPT 모델·프록시 설정을 확인해 주세요"}
}
