package chatgptllm

import (
	"time"

	"github.com/Autumn-27/norma/llm"
)

const auxiliaryTimeout = 90 * time.Second

// Only bounded, text-only helper calls (Judge and summaries) use this guard.
// This is a local acceptance limit, not a server token or billing limit.
func auxiliaryRequest(req llm.CompletionRequest) bool {
	return req.Thinking == "disabled" && req.MaxTokens > 0 && len(req.Tools) == 0
}

func auxiliaryByteLimit(req llm.CompletionRequest) int {
	return min(max(req.MaxTokens, 1024), 8192) * 8
}

type outputBudget struct {
	limit int
	seen  int
}

func (b *outputBudget) accept(ev llm.StreamEvent) bool {
	if b.limit == 0 {
		return true
	}
	switch ev.Type {
	case llm.SETextDelta, llm.SEThinkingDelta:
		b.seen += len(ev.Text)
	case llm.SEMessageFinal:
		// The final message can contain content absent from intermediate deltas.
		if ev.FinalMessage != nil {
			size := 0
			for _, block := range ev.FinalMessage.Content {
				size += len(block.Text) + len(block.Thinking)
			}
			if size > b.limit {
				return false
			}
		}
	}
	return b.seen <= b.limit
}
