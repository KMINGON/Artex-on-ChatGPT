package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode"
)

func TestDefaultAgentPromptsAndToolDescriptionsAreKorean(t *testing.T) {
	prompts := BuiltinPromptSeeds()
	prompts["assistant"] = DefaultAssistantPrompt
	prompts["reporter"] = ReporterDefaultPrompt
	prompts["retester"] = RetesterDefaultPrompt
	prompts["compression"] = compressionSystemPrompt
	prompts["output_language"] = langDirective("ko")
	prompts["traffic"] = workerTrafficBlock(true)
	for key, prompt := range prompts {
		t.Run(key, func(t *testing.T) {
			if strings.IndexFunc(prompt, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0 {
				t.Fatal("default prompt still contains Chinese text")
			}
			if strings.IndexFunc(prompt, func(r rune) bool { return unicode.Is(unicode.Hangul, r) }) < 0 {
				t.Fatal("default prompt has no Korean instruction text")
			}
		})
	}
	for _, seed := range BuiltinToolSeeds() {
		schema, err := json.Marshal(seed.Schema)
		if err != nil {
			t.Fatalf("%s: %v", seed.Key, err)
		}
		if strings.IndexFunc(seed.Desc+string(schema), func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0 {
			t.Errorf("%s: default description or parameter schema contains Chinese text", seed.Key)
		}
	}
}
