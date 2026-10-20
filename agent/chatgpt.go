package agent

import (
	"context"
	"iter"

	"github.com/Autumn-27/norma/llm"
)

// Invalid persisted subscription settings must remain selected, even if the
// caller normally resolves another provider when construction fails.
type unavailableProvider struct{ err error }

func (p unavailableProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) { yield(llm.StreamEvent{}, p.configError()) }
}

func (p unavailableProvider) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return llm.Message{}, "", llm.Usage{}, p.configError()
}

type configError struct{ error }

func (configError) NoFallback() bool { return true }
func (configError) Retryable() bool  { return false }

func (p unavailableProvider) configError() error {
	return configError{p.err}
}
