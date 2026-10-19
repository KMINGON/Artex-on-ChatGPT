package agent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/llmrec"
)

// The transport is the only layer that still sees the wire bodies: norma builds
// the request body internally and decodes the SSE response before the recorder
// gets it. This checks the round trip preserves both directions untouched.
func TestRoundTripCapturesRawBodies(t *testing.T) {
	const sse = "event: message_start\ndata: {\"type\":\"message_start\"}\n\nevent: message_stop\ndata: {}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()

	client, err := quotaAwareHTTPClient("", "")
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	const reqBody = `{"model":"claude","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"t","input_schema":{}}]}`
	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	ctx, capt := llmrec.NewCapture(req.Context())
	req = req.WithContext(ctx)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	_ = resp.Body.Close()

	if string(got) != sse {
		t.Fatal("capture altered the response delivered to norma")
	}
	if capt.RawRequest() != reqBody {
		t.Fatalf("RawRequest()=%q want %q", capt.RawRequest(), reqBody)
	}
	if capt.RawResponse() != sse {
		t.Fatalf("RawResponse()=%q want the raw SSE frames", capt.RawResponse())
	}
}

// A 429 body is read and replaced in-place by the quota check. Capturing must
// still see it, and the replacement body must remain readable downstream.
func TestRoundTripCaptures429BodyAlongsideQuotaRewrite(t *testing.T) {
	const body = `{"error":{"message":"insufficient_quota"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	client, err := quotaAwareHTTPClient("", "")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	ctx, capt := llmrec.NewCapture(req.Context())
	req = req.WithContext(ctx)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	// Quota exhaustion is normalized to 402 so the router fails over.
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("status=%d want 402", resp.StatusCode)
	}
	if capt.RawResponse() != body {
		t.Fatalf("RawResponse()=%q want %q", capt.RawResponse(), body)
	}
	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read replaced body: %v", err)
	}
	if string(rest) != body {
		t.Fatalf("replaced body=%q want it still readable", rest)
	}
}

// Recording off = no Capture on the context. The transport must behave exactly
// as before, including the quota rewrite.
func TestRoundTripWithoutCaptureIsUnchanged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	client, err := quotaAwareHTTPClient("", "")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	resp, err := client.Post(srv.URL, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "ok" {
		t.Fatalf("body=%q", got)
	}
}

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
	transport := client.Transport.(quotaAwareTransport)
	base := transport.base.(*http.Transport)
	req, _ := http.NewRequest(http.MethodGet, "https://api.openai.com/v1/responses", nil)
	proxy, err := base.Proxy(req)
	if err != nil || proxy.String() != "http://127.0.0.1:18080" {
		t.Fatalf("explicit proxy lost: %v %v", proxy, err)
	}
}
