package mcp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"testing"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/tool"
)

func TestStdioMCPImageRoundTrip(t *testing.T) {
	var pixels bytes.Buffer
	if err := png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 16, 16))); err != nil {
		t.Fatal(err)
	}
	cReader, sWriter := io.Pipe()
	sReader, cWriter := io.Pipe()
	defer cReader.Close()
	defer sWriter.Close()
	defer sReader.Close()
	defer cWriter.Close()
	go func() {
		var request rpcRequest
		if json.NewDecoder(sReader).Decode(&request) != nil {
			return
		}
		json.NewEncoder(sWriter).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "screenshot"},
			map[string]any{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(pixels.Bytes())},
		}}})
	}()
	client := &Client{server: "visual", conn: newConn(cReader, cWriter)}
	r, err := client.wrap(remoteTool{Name: "screenshot"}).Call(t.Context(), json.RawMessage(`{}`), &tool.ToolContext{OutputDir: t.TempDir()})
	if err != nil || r.IsError || len(r.Content) != 2 || r.Content[1].Type != llm.BlockImage {
		t.Fatalf("stdio image lost: %v %+v", err, r)
	}
	if _, err := r.Content[1].Image.DataURL(); err != nil {
		t.Fatal(err)
	}
}
