package agent

import (
	"context"
	"errors"
	"github.com/Autumn-27/norma/llm"
	"testing"
)

func TestConfigOnlyAcceptsOfficialOpenAI(t *testing.T) {
	for _, provider := range []string{"openai", "openai-responses"} {
		c := ConfigFrom(provider, "existing-model", "https://api.openai.com/v1/responses", "fixture-key", "")
		if c.Provider() != "openai-responses" || c.Model != "existing-model" || c.configErr != nil {
			t.Fatalf("config not migrated: %+v", c)
		}
	}
	for _, c := range []Config{
		ConfigFrom("anthropic", "old-model", "", "fixture-key", ""),
		ConfigFrom("unknown", "old-model", "", "fixture-key", ""),
		ConfigFrom("openai", "old-model", "https://gateway.invalid/v1", "fixture-key", ""),
	} {
		p, err := c.NewProvider()
		if err != nil || p == nil {
			t.Fatal("unsupported profile must stay selected")
		}
		_, _, _, err = p.Complete(context.Background(), llm.CompletionRequest{})
		var policy interface{ NoFallback() bool }
		if !errors.As(err, &policy) || !policy.NoFallback() {
			t.Fatalf("unsupported profile silently fell back: %v", err)
		}
	}
}

func TestFromEnvOnlyOpenAI(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "ignored")
	t.Setenv("OPENAI_API_KEY", "")
	if _, ok := FromEnv(); ok {
		t.Fatal("removed key enabled inference")
	}
	t.Setenv("OPENAI_API_KEY", "fixture")
	t.Setenv("ARTEX_LLM_PROVIDER", "openai")
	t.Setenv("ARTEX_LLM_MODEL", "kept-model")
	t.Setenv("ARTEX_LLM_BASE_URL", "")
	c, ok := FromEnv()
	if !ok || c.Model != "kept-model" || c.configErr != nil {
		t.Fatal("official config rejected")
	}
	t.Setenv("ARTEX_LLM_PROVIDER", "anthropic")
	c, ok = FromEnv()
	if !ok || c.configErr == nil {
		t.Fatal("unsupported provider silently changed")
	}
}
