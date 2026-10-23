package chatgptllm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

func visualFixture(t *testing.T) llm.ContentBlock {
	t.Helper()
	var out bytes.Buffer
	if err := png.Encode(&out, image.NewRGBA(image.Rect(0, 0, 12, 20))); err != nil {
		t.Fatal(err)
	}
	b, err := llm.NewImage(out.Bytes(), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestVisualToolResultRequestAndTranscriptReplay(t *testing.T) {
	for _, api := range []bool{false, true} {
		t.Run(fmt.Sprintf("api=%v", api), func(t *testing.T) {
			visual := visualFixture(t)
			persisted, err := visual.Image.Persist(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			visual.Image = persisted
			messages := []llm.Message{
				llm.UserText("화면을 확인하세요"),
				{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "call-image", Name: "screenshot", Input: json.RawMessage(`{}`)}}},
				{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.BlockToolResult, ToolUseID: "call-image", Content: []llm.ContentBlock{llm.TextBlock("캡처"), visual, llm.TextBlock("계속")}}}},
			}
			store := transcript.NewStore(t.TempDir())
			path := store.MainPath("image-session")
			for _, m := range messages {
				if err := store.Append(path, transcript.Record{Type: "message", Message: &m}); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte("base64")) || bytes.Contains(raw, []byte(`"data":`)) {
				t.Fatal("pixels serialized into saved conversation")
			}
			records, err := store.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			restored, _ := transcript.Messages(records)
			client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Input []json.RawMessage `json:"input"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Fatal(err)
				}
				if len(req.Input) != 3 {
					t.Fatalf("input count=%d", len(req.Input))
				}
				var result struct {
					Type   string           `json:"type"`
					CallID string           `json:"call_id"`
					Output []map[string]any `json:"output"`
				}
				if err := json.Unmarshal(req.Input[2], &result); err != nil {
					t.Fatal(err)
				}
				if result.Type != "function_call_output" || result.CallID != "call-image" || len(result.Output) != 4 {
					t.Fatalf("tool result contract lost: %+v", result)
				}
				if result.Output[0]["text"] != "캡처" || result.Output[2]["type"] != "input_image" || !strings.HasPrefix(result.Output[2]["image_url"].(string), "data:image/png;base64,") || result.Output[3]["text"] != "계속" {
					t.Fatal("mixed visual output lost")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				completed(w, textItem("이미지 확인됨"))
			})
			if _, _, _, err := testProviderMode(t, client, api).Complete(t.Context(), llm.CompletionRequest{Messages: restored}); err != nil {
				t.Fatal(err)
			}
			body, _, err := encodeRequest(Config{Model: "fixture"}, llm.CompletionRequest{Messages: restored})
			if err != nil {
				t.Fatal(err)
			}
			shape := requestShape(body, llm.CompletionRequest{Messages: restored})
			if !shape.ToolCallPairsValid || shape.ToolResultCount != 1 || shape.ToolResultBytes <= 0 {
				t.Fatalf("visual request diagnostics lost pairing: %+v", shape)
			}
		})
	}
}

func TestVisualHistoryKeepsRecentImagesAndMissingArtifactIsRecoverable(t *testing.T) {
	var messages []llm.Message
	for i := 0; i < llm.MaxImagesPerRequest+2; i++ {
		messages = append(messages, llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{visualFixture(t)}})
	}
	encoded, _, err := encodeRequest(Config{Model: "fixture"}, llm.CompletionRequest{Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		Input []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(encoded, &req); err != nil {
		t.Fatal(err)
	}
	for i, item := range req.Input {
		want := "input_image"
		if i < 2 {
			want = "input_text"
		}
		if item.Content[0].Type != want {
			t.Fatalf("image %d type=%s want=%s", i, item.Content[0].Type, want)
		}
	}
	b := visualFixture(t)
	b.Image, err = b.Image.Persist(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(b.Image.Path); err != nil {
		t.Fatal(err)
	}
	encoded, _, err = encodeRequest(Config{Model: "fixture"}, llm.CompletionRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{b}}}})
	if err != nil {
		t.Fatalf("missing artifact must not break resume: %v", err)
	}
	if !bytes.Contains(encoded, []byte("이미지를 다시 읽거나 캡처")) || bytes.Contains(encoded, []byte("base64")) {
		t.Fatal("missing artifact guidance lost")
	}
}

func TestInvalidImageDoesNotReachProvider(t *testing.T) {
	b := visualFixture(t)
	b.Image.Data = "not base64"
	_, _, err := encodeRequest(Config{Model: "fixture"}, llm.CompletionRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{b}}}})
	if err == nil {
		t.Fatal("malformed image accepted")
	}
}
