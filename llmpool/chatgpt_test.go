package llmpool

import (
	"context"
	"fmt"
	"testing"

	"github.com/Autumn-27/artex/chatgptllm"
)

func TestChatGPTErrorNeverTriggersBillingFallback(t *testing.T) {
	for _, code := range []int{0, 400, 401, 402, 403, 429, 500} {
		err := fmt.Errorf("wrapped: %w", &chatgptllm.Error{StatusCode: code, Message: "quota exhausted"})
		if shouldFailover(context.Background(), err) {
			t.Fatalf("status %d triggered automatic billing fallback", code)
		}
	}
}
