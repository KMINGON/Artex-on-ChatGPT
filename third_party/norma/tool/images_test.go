package tool

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func imageFixture(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := png.Encode(&out, image.NewRGBA(image.Rect(0, 0, 20, 12))); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestMCPImageMixedResultPreservesStructureAndPersists(t *testing.T) {
	data := imageFixture(t)
	raw, _ := json.Marshal(map[string]any{"content": []any{
		map[string]any{"type": "text", "text": "before"},
		map[string]any{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(data)},
		map[string]any{"type": "text", "text": "after"},
	}})
	r := DecodeMCPResult(raw, &ToolContext{OutputDir: t.TempDir()})
	if r.IsError || len(r.Content) != 3 || r.Content[1].Type != llm.BlockImage {
		t.Fatalf("mixed result lost: %+v", r)
	}
	if r.Content[1].Image.Data != "" || r.Content[1].Image.Path == "" {
		t.Fatal("image not externalized")
	}
	if strings.Contains(r.Flatten(), base64.StdEncoding.EncodeToString(data)) || !strings.Contains(r.Flatten(), "20x12") {
		t.Fatal("bad image text projection")
	}
	if _, err := r.Content[1].Image.DataURL(); err != nil {
		t.Fatal(err)
	}
}

func TestMCPImageValidationAndCountLimit(t *testing.T) {
	image := map[string]any{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(imageFixture(t))}
	raw, _ := json.Marshal(map[string]any{"content": []any{image, image, image, image, image}})
	r := DecodeMCPResult(raw, nil)
	count := 0
	for _, b := range r.Content {
		if b.Type == llm.BlockImage {
			count++
		}
	}
	if !r.IsError || count != llm.MaxImagesPerResult {
		t.Fatalf("count cap not enforced: %d", count)
	}
	for _, raw := range []string{
		`{"content":[{"type":"image","mimeType":"image/png","data":"not base64"}]}`,
		`{"content":[{"type":"audio","data":"hidden"}]}`,
		`{"content":[{"type":"resource","resource":{"uri":"file:///etc/passwd"}}]}`,
	} {
		if r := DecodeMCPResult([]byte(raw), nil); !r.IsError || len(r.Content) == 0 {
			t.Fatal("unsupported or invalid content silently accepted")
		}
	}
}

func TestReadImageUsesVisualInputAndLimitsSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "screen.png")
	if err := os.WriteFile(path, imageFixture(t), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"file_path": path})
	r, err := NewRead().Call(context.Background(), raw, &ToolContext{WorkingDir: dir, OutputDir: filepath.Join(dir, "artifacts")})
	if err != nil || r.IsError || len(r.Content) != 2 || r.Content[1].Type != llm.BlockImage {
		t.Fatalf("Read did not return pixels: %v %+v", err, r)
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(llm.MaxImageBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	r, err = NewRead().Call(context.Background(), raw, &ToolContext{WorkingDir: dir})
	if err != nil || !r.IsError || !strings.Contains(r.Flatten(), "8 MiB") {
		t.Fatalf("large image accepted: %v %s", err, r.Flatten())
	}
}
