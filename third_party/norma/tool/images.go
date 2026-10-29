package tool

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/Autumn-27/norma/llm"
)

// Four base64-encoded 8 MiB images plus JSON/text metadata fit in this bound.
const MaxMCPResponseBytes = 48 << 20

// Image validates visual output and stores only an artifact reference in
// persisted conversations when the host supplied its per-session output path.
func Image(tc *ToolContext, data []byte, mime string) (llm.ContentBlock, error) {
	b, err := llm.NewImage(data, mime)
	if err != nil {
		return llm.ContentBlock{}, err
	}
	if tc != nil && tc.OutputDir != "" {
		b.Image, err = b.Image.Persist(tc.OutputDir)
	}
	return b, err
}

// DecodeMCPResult is shared by stdio and HTTP transports. Image bytes remain
// structured visual input; unsupported content is reported instead of silently
// turning an image-only or resource-only response into empty success.
func DecodeMCPResult(raw json.RawMessage, tc *ToolContext) Result {
	var wire struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Data     string `json:"data"`
			MIMEType string `json:"mimeType"`
			Resource struct {
				Text string `json:"text"`
				URI  string `json:"uri"`
			} `json:"resource"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return Errorf("MCP 응답을 해석할 수 없습니다")
	}
	out := Result{IsError: wire.IsError}
	images := 0
	appendError := func(message string) {
		out.IsError = true
		out.Content = append(out.Content, llm.TextBlock(message))
	}
	for _, b := range wire.Content {
		switch b.Type {
		case "text":
			out.Content = append(out.Content, llm.TextBlock(Capture(tc, b.Text)))
		case "image":
			images++
			if images > llm.MaxImagesPerResult {
				if images == llm.MaxImagesPerResult+1 {
					appendError("도구 결과의 이미지가 4개를 초과하여 나머지를 생략했습니다. 필요한 이미지를 나누어 요청하세요.")
				}
				continue
			}
			if len(b.Data) > base64.StdEncoding.EncodedLen(llm.MaxImageBytes) {
				appendError("MCP 이미지가 8 MiB를 초과했습니다")
				continue
			}
			data, err := base64.StdEncoding.DecodeString(b.Data)
			if err != nil {
				appendError("MCP 이미지의 base64 데이터가 올바르지 않습니다")
				continue
			}
			block, err := Image(tc, data, b.MIMEType)
			if err != nil {
				appendError("MCP 이미지 처리 실패: " + err.Error())
				continue
			}
			out.Content = append(out.Content, block)
		case "resource":
			if b.Resource.Text != "" {
				out.Content = append(out.Content, llm.TextBlock(Capture(tc, b.Resource.Text)))
			} else {
				appendError("이 MCP 리소스에는 직접 표시할 텍스트가 없습니다. 서버가 제공하는 해당 리소스나 파일의 조회 도구가 있다면 사용하세요.")
			}
		default:
			appendError(fmt.Sprintf("지원하지 않는 MCP 콘텐츠 형식입니다: %s", b.Type))
		}
	}
	return out
}
