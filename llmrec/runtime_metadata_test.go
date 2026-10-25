package llmrec

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/chatgptllm"
	"github.com/Autumn-27/norma/llm"
)

func TestRuntimeMetadataPreservesActualResponseWithoutPromptBodies(t *testing.T) {
	r := Wrap(completeProvider{}, nil, "requested", "fixture", "", "medium", func() bool { return false }).WithRuntimeConfig("chatgpt", "", "fixture-build")
	req := llm.CompletionRequest{System: []string{"private-system-token"}, Tools: []llm.ToolSchema{{Name: "fixture", Description: "private-tool-description", InputSchema: map[string]any{"type": "object"}}}}
	raw := r.runtimeMetadata(req, &chatgptllm.ResponseMetadata{Model: "actual", AccessProgramCyber: "daybreak_blue", AccessProgramsPresent: true, UsageReceived: true})
	for _, secret := range []string{"private-system-token", "private-tool-description"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("body leaked: %s", secret)
		}
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	response := meta["response"].(map[string]any)
	if meta["requested_model"] != "requested" || meta["requested_program"] != "" || response["model"] != "actual" || response["access_program_cyber"] != "daybreak_blue" {
		t.Fatal(meta)
	}
	if len(meta["system_sha256"].(string)) != 64 || len(meta["tools_sha256"].(string)) != 64 {
		t.Fatal("missing hashes")
	}
	before := meta["system_sha256"]
	req.System[0] += " changed"
	if err := json.Unmarshal(r.runtimeMetadata(req, nil), &meta); err != nil {
		t.Fatal(err)
	}
	if meta["system_sha256"] == before {
		t.Fatal("changed prompt kept old hash")
	}
	// Absence of an observed response must stay unknown, not become Standard.
	var missing map[string]any
	if err := json.Unmarshal(r.runtimeMetadata(req, nil), &missing); err != nil {
		t.Fatal(err)
	}
	if _, exists := missing["response"]; exists {
		t.Fatal("invented response metadata")
	}
}
