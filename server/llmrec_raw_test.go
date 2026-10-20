package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/chatgptllm"
	"github.com/Autumn-27/artex/llmrec"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

type recordFixtureTransport func(*http.Request) (*http.Response, error)

func (f recordFixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestRecorderKeepsOpaqueResponsesOutOfDatabase(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer m.Close()
	pg := m.PG()
	if err := pg.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: recordFixtureTransport(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer fixture-secret-key" {
			t.Error("API authentication lost")
		}
		stream := `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","id":"rs_fixture","summary":[],"encrypted_content":"PRIVATE_OPAQUE"},{"type":"message","id":"message_fixture","role":"assistant","phase":"final_answer","status":"completed","content":[{"type":"output_text","text":"visible result"}]}],"usage":{"input_tokens":5,"output_tokens":9}}}` + "\n\n"
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}, nil
	})}
	inner, err := chatgptllm.New(chatgptllm.Config{APIKey: "fixture-secret-key", Model: "fixture-model", HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	const session = "exp999999-opaque-test"
	defer pg.Exec(`DELETE FROM llm_records WHERE session_id=$1`, session)
	rec := llmrec.Wrap(inner, pg, "fixture-model", "fixture-profile", "", "", func() bool { return true })
	ctx := transcript.WithSessionID(context.Background(), session)
	msg, _, _, err := rec.Complete(ctx, llm.CompletionRequest{Messages: []llm.Message{llm.UserText("fixture")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ProviderData) == 0 {
		t.Fatal("runtime replay was erased")
	}
	var id int64
	if err := pg.QueryRow(`SELECT id FROM llm_records WHERE session_id=$1 ORDER BY id DESC LIMIT 1`, session).Scan(&id); err != nil {
		t.Fatal(err)
	}
	got, err := pg.GetLLMRecord(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.RawRequest != "" || got.RawResponse != "" {
		t.Fatal("opaque raw HTTP entered recording")
	}
	combined := got.RequestBody + got.ResponseBody
	if strings.Contains(combined, "PRIVATE_OPAQUE") || strings.Contains(combined, "fixture-secret-key") {
		t.Fatal("private model state entered database diagnostics")
	}
	if !strings.Contains(got.ResponseBody, "visible result") {
		t.Fatal("normal model output recording lost")
	}
}
