package mcphttp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/tool"
)

func TestHTTPMCPVisualResult(t *testing.T) {
	for _, sse := range []bool{false, true} {
		t.Run(fmt.Sprintf("sse=%v", sse), func(t *testing.T) {
			var pixels bytes.Buffer
			if err := png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 16, 16))); err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request rpcRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				result := map[string]any{"content": []any{map[string]any{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(pixels.Bytes())}}}
				response := map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}
				if sse {
					w.Header().Set("Content-Type", "text/event-stream")
					raw, _ := json.Marshal(response)
					fmt.Fprintf(w, "data: %s\n\n", raw)
				} else {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(response)
				}
			}))
			defer srv.Close()
			client := &Client{server: "visual", url: srv.URL, http: srv.Client()}
			res, err := client.wrap(remoteTool{Name: "screenshot"}).Call(t.Context(), json.RawMessage(`{}`), &tool.ToolContext{OutputDir: t.TempDir()})
			if err != nil || res.IsError || len(res.Content) != 1 || res.Content[0].Type != llm.BlockImage {
				t.Fatalf("image lost: %v %+v", err, res)
			}
			if res.Content[0].Image.Path == "" || res.Content[0].Image.Data != "" {
				t.Fatal("HTTP image not persisted")
			}
		})
	}
}
