package llm

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, width, height))
	im.Set(0, 0, color.RGBA{R: 255, A: 255})
	var out bytes.Buffer
	if err := png.Encode(&out, im); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestImageRejectsAnimatedAndTruncatedGIF(t *testing.T) {
	frame := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})
	for _, frames := range []int{1, 2} {
		animation := &gif.GIF{}
		for i := 0; i < frames; i++ {
			animation.Image = append(animation.Image, frame)
			animation.Delay = append(animation.Delay, 1)
		}
		var out bytes.Buffer
		if err := gif.EncodeAll(&out, animation); err != nil {
			t.Fatal(err)
		}
		_, err := NewImage(out.Bytes(), "image/gif")
		if (err == nil) != (frames == 1) {
			t.Fatalf("frames=%d error=%v", frames, err)
		}
		if _, err := NewImage(out.Bytes()[:len(out.Bytes())-1], "image/gif"); err == nil {
			t.Fatal("truncated GIF accepted")
		}
	}
}

func TestImageArtifactRoundTripAndTampering(t *testing.T) {
	b, err := NewImage(testPNG(t, 32, 24), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	wantURL, err := b.Image.DataURL()
	if err != nil {
		t.Fatal(err)
	}
	stored, err := b.Image.Persist(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b.Image = stored
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "base64") || stored.Data != "" {
		t.Fatal("image bytes leaked into artifact transcript")
	}
	var restored ContentBlock
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if got, err := restored.Image.DataURL(); err != nil || got != wantURL {
		t.Fatalf("artifact replay failed: %v", err)
	}
	info, err := os.Stat(stored.Path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("artifact mode: %v %v", info, err)
	}
	if err := os.WriteFile(stored.Path, testPNG(t, 16, 16), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.Image.DataURL(); err == nil {
		t.Fatal("mutated image artifact replayed")
	}
	if err := os.Remove(stored.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.Image.DataURL(); err == nil {
		t.Fatal("missing image artifact replayed")
	}
}

func TestImageRejectsInvalidInputAndFileReferences(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		mime string
	}{
		{"not image", []byte("secret credentials"), "image/png"},
		{"wrong MIME", testPNG(t, 2, 2), "image/jpeg"},
		{"oversize", make([]byte, MaxImageBytes+1), "image/png"},
		{"dimensions", testPNG(t, 16385, 1), "image/png"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewImage(tc.data, tc.mime); err == nil {
				t.Fatal("invalid image accepted")
			}
		})
	}
	b, _ := NewImage(testPNG(t, 1, 1), "image/png")
	b.Image.Path, b.Image.Data = "/etc/passwd", ""
	if _, err := b.Image.DataURL(); err == nil {
		t.Fatal("arbitrary file reference accepted")
	}
	b, _ = NewImage(testPNG(t, 1, 1), "image/png")
	stored, err := b.Image.Persist(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), filepath.Base(stored.Path))
	if err := os.Symlink(stored.Path, target); err != nil {
		t.Skip("symlink unavailable")
	}
	stored.Path = target
	if _, err := stored.DataURL(); err == nil {
		t.Fatal("symlink artifact accepted")
	}
}

func TestImageAccumulatorOwnsMetadata(t *testing.T) {
	b, _ := NewImage(testPNG(t, 1, 1), "image/png")
	m := Message{Role: RoleUser, Content: []ContentBlock{b}}
	copy := cloneMessage(m)
	m.Content[0].Image.Path = "changed"
	if copy.Content[0].Image.Path != "" {
		t.Fatal("image metadata alias")
	}
}
