package chatgptllm

import (
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func TestChatGPTStreamWithoutContentType(t *testing.T) {
	p := testProvider(t, fakeClient(t, func(w http.ResponseWriter, _ *http.Request) {
		// Suppress net/http's automatic text/plain detection, matching the live
		// ChatGPT response: HTTP 200, no Content-Type, otherwise normal SSE.
		w.Header()["Content-Type"] = nil
		event(w, map[string]any{"type": "response.output_item.added", "item": textItem("")})
		event(w, map[string]any{"type": "response.output_text.delta", "item_id": "msg1", "delta": "OK"})
		event(w, map[string]any{"type": "response.metadata", "metadata": map[string]any{"diagnostic": "not assistant text"}})
		completed(w, textItem("OK"))
	}))
	msg, stop, _, err := p.Complete(t.Context(), llm.CompletionRequest{})
	if err != nil || msg.Text() != "OK" || stop != "end_turn" || len(msg.ProviderData) == 0 {
		t.Fatalf("valid headerless SSE rejected: text=%q stop=%q error=%v", msg.Text(), stop, err)
	}
}

func TestMissingHeaderDoesNotAcceptInvalidOrIncompleteBodies(t *testing.T) {
	for name, write := range map[string]func(http.ResponseWriter){
		"JSON":          func(w http.ResponseWriter) { io.WriteString(w, `{"output":"OK","status":"completed"}`) },
		"HTML":          func(w http.ResponseWriter) { io.WriteString(w, "<html>OK</html>") },
		"metadata only": func(w http.ResponseWriter) { event(w, map[string]any{"type": "response.metadata"}) },
		"no completion": func(w http.ResponseWriter) {
			event(w, map[string]any{"type": "response.output_item.added", "item": textItem("")})
			event(w, map[string]any{"type": "response.output_text.delta", "item_id": "msg1", "delta": "partial"})
		},
		"unknown event": func(w http.ResponseWriter) { event(w, map[string]any{"type": "response.not_supported"}) },
	} {
		t.Run(name, func(t *testing.T) {
			p := testProvider(t, fakeClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header()["Content-Type"] = nil
				write(w)
			}))
			msg, _, _, err := p.Complete(t.Context(), llm.CompletionRequest{})
			var e *Error
			if !errors.As(err, &e) || !e.NoFallback() || !e.OutputRejected() || e.StatusCode != 200 || len(msg.Content) != 0 {
				t.Fatalf("bad body accepted: message=%+v err=%v", msg, err)
			}
		})
	}
}

func TestExplicitNonStreamContentTypeStillRejected(t *testing.T) {
	p := testProvider(t, fakeClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "test-request")
		io.WriteString(w, `{"status":"completed"}`)
	}))
	_, _, _, err := p.Complete(t.Context(), llm.CompletionRequest{})
	var e *Error
	if !errors.As(err, &e) || e.Code != "invalid_stream" || e.StatusCode != 200 || e.RequestID != "test-request" {
		t.Fatalf("unexpected error: %+v", err)
	}
}
