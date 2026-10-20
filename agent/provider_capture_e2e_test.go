package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/llmrec"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// Exercise the production API wiring, a real harmless tool, durable transcript,
// cold resume and the raw-recording exclusion together.
func TestAPIResponsesToolReplayAndColdResumeWithoutRawCapture(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req struct {
			Input []json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if calls > 1 {
			raw, _ := json.Marshal(req.Input)
			for _, want := range []string{"PRIVATE_OPAQUE_FIXTURE", `"phase":"commentary"`, `"call_id":"call_fixture"`, "ARTEX_TOOL_OK"} {
				if !strings.Contains(string(raw), want) {
					t.Errorf("replay missing expected state at turn %d", calls)
				}
			}
		}
		if calls == 1 {
			writeCompleted(w, []any{
				map[string]any{"type": "reasoning", "id": "reasoning_fixture", "summary": []any{}, "encrypted_content": "PRIVATE_OPAQUE_FIXTURE"},
				map[string]any{"type": "message", "id": "message_fixture", "role": "assistant", "phase": "commentary", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Checking the local tool."}}},
				map[string]any{"type": "function_call", "id": "function_fixture", "call_id": "call_fixture", "name": "Bash", "namespace": "artex", "arguments": `{"command":"printf 'ARTEX_TOOL_OK\\n'"}`, "status": "completed"},
			})
		} else {
			writeCompleted(w, textOutput("done"))
		}
	}))
	defer srv.Close()
	cfg := apiFixtureConfig(t, srv)
	provider, err := cfg.NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	store := transcript.NewStore(t.TempDir())
	opts := agentcore.Options{Provider: provider, Tools: []tool.CoreTool{tool.NewBash()}, PermissionMode: permission.ModeBypass, WorkingDir: t.TempDir(), Transcript: store, SessionID: "api-replay"}
	ctx, capture := llmrec.NewCapture(context.Background())
	session := agentcore.NewSession(opts)
	var reply string
	for ev, err := range session.Prompt(ctx, "Run the fixture tool") {
		if ev.Kind == harness.KindText {
			reply += ev.Text
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(reply, "done") {
		t.Fatalf("model did not complete tool roundtrip: %q", reply)
	}
	if calls != 2 {
		t.Fatalf("tool roundtrip calls=%d", calls)
	}
	records, err := store.Load(store.MainPath("api-replay"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range records {
		if record.Message != nil && len(record.Message.ProviderData) > 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("transcript lost provider state")
	}
	resumed := agentcore.NewSession(opts)
	if err := resumed.Resume("api-replay"); err != nil {
		t.Fatal(err)
	}
	reply = ""
	for ev, err := range resumed.Prompt(ctx, "Continue") {
		if ev.Kind == harness.KindText {
			reply += ev.Text
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if reply != "done" {
		t.Fatalf("resumed model did not complete: %q", reply)
	}
	if calls != 3 {
		t.Fatalf("resume calls=%d", calls)
	}
	if capture.RawRequest() != "" || capture.RawResponse() != "" {
		t.Fatal("opaque state entered raw recording")
	}
}
