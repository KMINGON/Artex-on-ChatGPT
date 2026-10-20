package llm

// HasProviderState reports opaque state that must not cross a connection change.
func HasProviderState(req CompletionRequest) bool {
	for _, message := range req.Messages {
		if len(message.ProviderData) > 0 {
			return true
		}
		for _, block := range message.Content {
			if block.Signature != "" {
				return true
			}
		}
	}
	return false
}
