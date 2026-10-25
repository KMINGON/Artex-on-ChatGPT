package llmrec

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/chatgptllm"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
	"github.com/google/uuid"
)

func TestRuntimeMetadataPersistsWithoutBodyRecording(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires isolated ARTEX_PG_DSN")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if err := pg.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	if err := pg.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []bool{false, true} {
		for _, program := range []string{"absent", "empty", "null", "daybreak_blue", "standard"} {
			t.Run(fmt.Sprintf("stream=%v/%s", stream, program), func(t *testing.T) {
				profile := "runtime-meta-" + uuid.NewString()
				defer pg.Exec(`DELETE FROM llm_usage WHERE profile_name=$1`, profile)
				defer pg.Exec(`DELETE FROM llm_records WHERE profile_name=$1`, profile)
				client := &http.Client{Transport: usageTransport(func(*http.Request) (*http.Response, error) {
					response := map[string]any{"status": "completed", "model": "actual-model", "output": []any{map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "status": "completed", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "PRIVATE-RESPONSE-BODY"}}}}, "usage": map[string]int{"input_tokens": 9, "output_tokens": 4}}
					switch program {
					case "empty":
						response["access_programs"] = map[string]any{}
					case "null":
						response["access_programs"] = nil
					case "daybreak_blue", "standard":
						response["access_programs"] = map[string]string{"cyber": program}
					}
					terminal, _ := json.Marshal(map[string]any{"type": "response.completed", "response": response})
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(terminal) + "\n\n"))}, nil
				})}
				provider, err := chatgptllm.New(chatgptllm.Config{Model: "requested-model", APIKey: "local-fixture-not-a-real-key", HTTPClient: client})
				if err != nil {
					t.Fatal(err)
				}
				recorder := Wrap(provider, pg, "requested-model", profile, "", "medium", func() bool { return false }).WithRuntimeConfig("api_key", "daybreak_blue", "fixture-build")
				temperature := 0.25
				req := llm.CompletionRequest{Thinking: "disabled", Temperature: &temperature, System: []string{"PRIVATE-SYSTEM-BODY"}, Messages: []llm.Message{llm.UserText("PRIVATE-USER-BODY")}, Tools: []llm.ToolSchema{{Name: "fixture_tool", Description: "PRIVATE-TOOL-DESCRIPTION", InputSchema: map[string]any{"type": "object"}}}, MaxTokens: 128}
				if stream {
					for _, err := range recorder.Stream(context.Background(), req) {
						if err != nil {
							t.Fatal(err)
						}
					}
				} else {
					if _, _, _, err := recorder.Complete(context.Background(), req); err != nil {
						t.Fatal(err)
					}
				}
				var raw []byte
				var input, output int
				var usageKnown bool
				if err := pg.QueryRow(`SELECT metadata,input_tokens,output_tokens,usage_received FROM llm_usage WHERE profile_name=$1`, profile).Scan(&raw, &input, &output, &usageKnown); err != nil {
					t.Fatal(err)
				}
				if input != 9 || output != 4 || !usageKnown {
					t.Fatalf("lost body-off metering: %d/%d %v", input, output, usageKnown)
				}
				for _, secret := range []string{"PRIVATE-", "local-fixture-not-a-real-key"} {
					if strings.Contains(string(raw), secret) {
						t.Fatalf("metadata exposed %s", secret)
					}
				}
				var rows int
				if err := pg.QueryRow(`SELECT count(*) FROM llm_records WHERE profile_name=$1`, profile).Scan(&rows); err != nil || rows != 0 {
					t.Fatalf("body recording was not disabled: %d %v", rows, err)
				}
				var metadata map[string]any
				if err := json.Unmarshal(raw, &metadata); err != nil {
					t.Fatal(err)
				}
				if metadata["requested_model"] != "requested-model" || metadata["requested_program"] != "daybreak_blue" || metadata["auth_mode"] != "api_key" || metadata["reasoning_effort"] != "medium" || metadata["build_version"] != "fixture-build" || metadata["max_output_tokens_requested"] != float64(128) || metadata["thinking_requested"] != "disabled" || metadata["temperature_requested"] != temperature {
					t.Fatalf("lost runtime settings %s", raw)
				}
				response := metadata["response"].(map[string]any)
				if response["model"] != "actual-model" || response["access_programs_present"] != (program != "absent") {
					t.Fatalf("lost response identity %s", raw)
				}
				actual, exists := response["access_program_cyber"]
				if program == "absent" || program == "empty" {
					if exists {
						t.Fatalf("invented actual program %s", raw)
					}
				} else {
					want := program
					if program == "null" {
						want = "standard"
					}
					if actual != want {
						t.Fatalf("actual program %v want %s", actual, want)
					}
				}
			})
		}
	}
}
