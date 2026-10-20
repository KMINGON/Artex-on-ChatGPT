package agent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/llmrec"
)

func TestChatGPTTransportNeverCapturesOpaqueWireBodies(t *testing.T) {
	const requestBody = `{"input":[{"type":"reasoning","encrypted_content":"PRIVATE_REQUEST_REPLAY"}]}`
	const responseBody = "data: {\"type\":\"response.completed\",\"output\":[{\"type\":\"reasoning\",\"encrypted_content\":\"PRIVATE_RESPONSE_REPLAY\"}]}\n\n"
	for _, status := range []int{http.StatusOK, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if string(body) != requestBody || r.Header.Get("Authorization") != "Bearer fixture-only" {
					t.Error("privacy protection altered outgoing protocol request")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, responseBody)
			}))
			defer srv.Close()
			client, err := ChatGPTHTTPClient("")
			if err != nil {
				t.Fatal(err)
			}
			ctx, capture := llmrec.NewCapture(t.Context())
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, strings.NewReader(requestBody))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer fixture-only")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || string(body) != responseBody || resp.StatusCode != status {
				t.Fatalf("response changed: status=%d err=%v", resp.StatusCode, err)
			}
			if capture.RawRequest() != "" || capture.RawResponse() != "" || len(capture.Attempts()) != 0 {
				t.Fatal("subscription wire bodies entered debug capture")
			}
		})
	}
}

func TestChatGPTTransportKeepsExplicitProxy(t *testing.T) {
	client, err := ChatGPTHTTPClient("http://127.0.0.1:18080")
	if err != nil {
		t.Fatal(err)
	}
	base := client.Transport.(*http.Transport)
	req, _ := http.NewRequest(http.MethodGet, "https://api.openai.com/v1/responses", nil)
	proxy, err := base.Proxy(req)
	if err != nil || proxy.String() != "http://127.0.0.1:18080" {
		t.Fatalf("explicit proxy lost: %v %v", proxy, err)
	}
}
