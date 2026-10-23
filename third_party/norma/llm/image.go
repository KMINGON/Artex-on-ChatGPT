package llm

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	MaxImageBytes       = 8 << 20
	MaxImagePixels      = 40_000_000
	MaxImagesPerResult  = 4
	MaxImagesPerRequest = 8
	// A conservative local budgeting estimate, not a provider billing formula.
	ImageTokenBudget = 8192
)

// ImageSource either carries in-memory bytes or an immutable local artifact.
// Data is never included in flattened tool logs. Path references must have the
// content-derived filename and matching digest before they can be replayed.
type ImageSource struct {
	MIMEType string `json:"mime_type"`
	Data     string `json:"data,omitempty"`
	Path     string `json:"path,omitempty"`
	SHA256   string `json:"sha256"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

func imageExtension(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	}
	return ""
}

// NewImage validates bytes and their actual format before accepting a tool's
// declared MIME type. No remote URLs or arbitrary file references are accepted.
func NewImage(data []byte, mime string) (ContentBlock, error) {
	if len(data) == 0 || len(data) > MaxImageBytes {
		return ContentBlock{}, fmt.Errorf("이미지는 1바이트 이상 8 MiB 이하여야 합니다")
	}
	actual := http.DetectContentType(data)
	if mime == "" {
		mime = actual
	}
	if imageExtension(mime) == "" || actual != mime {
		return ContentBlock{}, fmt.Errorf("지원하지 않거나 실제 파일과 다른 이미지 형식입니다: %s", mime)
	}
	if mime == "image/gif" && !staticGIF(data) {
		return ContentBlock{}, fmt.Errorf("애니메이션 또는 손상된 GIF는 지원하지 않습니다. 단일 프레임 PNG/JPEG로 변환해 주세요")
	}
	w, h := 0, 0
	if mime == "image/webp" {
		w, h = webpDimensions(data)
	} else if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		w, h = cfg.Width, cfg.Height
	}
	if w <= 0 || h <= 0 || w > 16384 || h > 16384 || int64(w)*int64(h) > MaxImagePixels {
		return ContentBlock{}, fmt.Errorf("이미지 크기를 확인할 수 없거나 허용 해상도(한 변 16,384픽셀, 4천만 픽셀)를 초과했습니다")
	}
	sum := sha256.Sum256(data)
	return ContentBlock{Type: BlockImage, Image: &ImageSource{
		MIMEType: mime, Data: base64.StdEncoding.EncodeToString(data), SHA256: hex.EncodeToString(sum[:]), Width: w, Height: h,
	}}, nil
}

// Count GIF frames without decoding pixels: a small compressed animation can
// otherwise allocate many full-size frames before its pixel budget is checked.
func staticGIF(data []byte) bool {
	if len(data) < 13 {
		return false
	}
	at := 13
	if data[10]&0x80 != 0 {
		at += 3 * (1 << ((data[10] & 7) + 1))
	}
	skipBlocks := func() bool {
		for at < len(data) {
			n := int(data[at])
			at++
			if n == 0 {
				return true
			}
			if n > len(data)-at {
				return false
			}
			at += n
		}
		return false
	}
	frames := 0
	for at < len(data) {
		tag := data[at]
		at++
		switch tag {
		case 0x3b:
			return frames == 1
		case 0x21:
			if at >= len(data) {
				return false
			}
			at++
			if !skipBlocks() {
				return false
			}
		case 0x2c:
			frames++
			if frames > 1 || len(data)-at < 9 {
				return false
			}
			flags := data[at+8]
			at += 9
			if flags&0x80 != 0 {
				at += 3 * (1 << ((flags & 7) + 1))
			}
			if at >= len(data) {
				return false
			}
			at++ // LZW minimum code size
			if !skipBlocks() {
				return false
			}
		default:
			return false
		}
	}
	return false
}

// webpDimensions reads only the bounded RIFF image header. Pixel decoding is
// left to the vision provider; no new image-decoder dependency is required.
func webpDimensions(b []byte) (int, int) {
	if len(b) < 30 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return 0, 0
	}
	switch string(b[12:16]) {
	case "VP8X":
		// Animated WebP is not a supported still-image input.
		if b[20]&2 != 0 {
			return 0, 0
		}
		return 1 + int(b[24]) + int(b[25])<<8 + int(b[26])<<16, 1 + int(b[27]) + int(b[28])<<8 + int(b[29])<<16
	case "VP8 ":
		if string(b[23:26]) != "\x9d\x01\x2a" {
			return 0, 0
		}
		return int(binary.LittleEndian.Uint16(b[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(b[28:30]) & 0x3fff)
	case "VP8L":
		if b[20] != 0x2f {
			return 0, 0
		}
		x := binary.LittleEndian.Uint32(b[21:25])
		return int(x&0x3fff) + 1, int((x>>14)&0x3fff) + 1
	}
	return 0, 0
}

func (s *ImageSource) bytes() ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("이미지 데이터가 없습니다")
	}
	var data []byte
	var err error
	if s.Path != "" {
		expected := "image-" + s.SHA256 + imageExtension(s.MIMEType)
		if s.Data != "" || len(s.SHA256) != 64 || filepath.Base(s.Path) != expected || !filepath.IsAbs(s.Path) {
			return nil, fmt.Errorf("이미지 파일 참조가 올바르지 않습니다")
		}
		info, statErr := os.Lstat(s.Path)
		if statErr != nil || !info.Mode().IsRegular() || info.Size() > MaxImageBytes {
			return nil, fmt.Errorf("이미지 원본 파일을 읽을 수 없습니다")
		}
		f, openErr := os.Open(s.Path)
		if openErr != nil {
			return nil, fmt.Errorf("이미지 원본 파일을 열 수 없습니다")
		}
		defer f.Close()
		data, err = io.ReadAll(io.LimitReader(f, MaxImageBytes+1))
	} else {
		if len(s.Data) > base64.StdEncoding.EncodedLen(MaxImageBytes) {
			return nil, fmt.Errorf("이미지 데이터가 8 MiB를 초과했습니다")
		}
		data, err = base64.StdEncoding.DecodeString(s.Data)
	}
	if err != nil {
		return nil, fmt.Errorf("이미지 데이터를 읽을 수 없습니다")
	}
	checked, err := NewImage(data, s.MIMEType)
	if err != nil {
		return nil, err
	}
	if checked.Image.SHA256 != s.SHA256 || checked.Image.Width != s.Width || checked.Image.Height != s.Height {
		return nil, fmt.Errorf("이미지 원본이 변경되었거나 이미지 메타데이터가 올바르지 않습니다")
	}
	return data, nil
}

// DataURL is only used at the provider boundary, never as a text block.
func (s *ImageSource) DataURL() (string, error) {
	data, err := s.bytes()
	if err != nil {
		return "", err
	}
	return "data:" + s.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// Persist moves an image out of transcript JSON and into a content-addressed,
// private file. Publication by rename is atomic, including concurrent captures.
func (s *ImageSource) Persist(dir string) (*ImageSource, error) {
	data, err := s.bytes()
	if err != nil {
		return nil, err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	dest := filepath.Join(dir, "image-"+s.SHA256+imageExtension(s.MIMEType))
	f, err := os.CreateTemp(dir, ".image-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	if err = os.Rename(f.Name(), dest); err != nil {
		return nil, err
	}
	copy := *s
	copy.Path, copy.Data = dest, ""
	return &copy, nil
}

// Description is safe for text logs and for compacted visual history.
func (s *ImageSource) Description() string {
	if s == nil {
		return "[이미지 데이터 없음]"
	}
	path := s.Path
	if path == "" {
		path = "메모리 이미지 " + s.SHA256
	}
	return fmt.Sprintf("[이미지 %s · %dx%d · %s]", s.MIMEType, s.Width, s.Height, strings.ReplaceAll(path, "\n", " "))
}
