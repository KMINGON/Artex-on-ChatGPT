package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

// providerStub is a model endpoint that records which key reached it.
type providerStub struct {
	*httptest.Server
	mu   sync.Mutex
	keys []string
	path []string
}

func newProviderStub(t *testing.T) *providerStub {
	t.Helper()
	stub := &providerStub{}
	stub.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		stub.keys = append(stub.keys, r.Header.Get("x-api-key")+r.Header.Get("Authorization"))
		stub.path = append(stub.path, r.URL.Path)
		stub.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/v1/messages") {
			_, _ = w.Write([]byte(`{"type":"message","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`))
	}))
	t.Cleanup(stub.Close)
	return stub
}

func (p *providerStub) seen() ([]string, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.keys...), append([]string(nil), p.path...)
}

func TestRestoredProviderSettingsAcceptOnlyTheirOwnAddress(t *testing.T) {
	for body, ok := range map[string]bool{
		`{"auth_mode":"anthropic","base_url":"https://api.anthropic.com"}`:     true,
		`{"auth_mode":"openai_compatible","base_url":"http://127.0.0.1:1/v1"}`: true,
		`{"auth_mode":"api_key","base_url":"https://vendor.example"}`:          false,
		`{"auth_mode":"chatgpt_plan","base_url":"https://vendor.example"}`:     false,
		`{"base_url":"https://vendor.example"}`:                                false,
	} {
		var got map[string]any
		err := decodeOpenAISettings(httptest.NewRequest("POST", "/llm/test", strings.NewReader(body)), &got)
		if (err == nil) != ok {
			t.Fatalf("%s: err=%v", body, err)
		}
	}
	for raw, want := range map[string]string{
		"https://api.anthropic.com/v1/messages": "https://api.anthropic.com",
		"https://api.anthropic.com/v1/":         "https://api.anthropic.com",
		"":                                      "",
	} {
		if got, err := db.NormalizeProviderBaseURL(db.AuthAnthropic, raw); err != nil || got != want {
			t.Fatalf("anthropic %q -> %q %v", raw, got, err)
		}
	}
	for _, bad := range []string{"", "ftp://h/v1", "http://user:pass@h/v1", "http://h/v1?key=x", "h:11434/v1"} {
		if _, err := db.NormalizeProviderBaseURL(db.AuthOpenAICompatible, bad); err == nil {
			t.Fatalf("local base URL %q accepted", bad)
		}
	}
}

// Rows from before the OpenAI-only release keep failing: they are never revived
// as a restored provider and never send their stored key.
func TestLegacyProviderRowsStayUnavailable(t *testing.T) {
	s := &Server{}
	for _, p := range []*db.LLMProfile{
		{AuthMode: "api_key", Format: "anthropic", Model: "legacy", APIKey: "private-fixture"},
		{AuthMode: "api_key", Format: "openai", BaseURL: "http://127.0.0.1:9/v1", Model: "legacy", APIKey: "private-fixture"},
		{AuthMode: db.AuthAnthropic, Format: "openai", Model: "tampered", APIKey: "private-fixture"},
	} {
		if p.UnsupportedReason() == "" {
			t.Fatalf("legacy row accepted: %+v", p)
		}
		prov, err := s.configForProfile(p).NewProvider()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := prov.Complete(context.Background(), llm.CompletionRequest{Messages: []llm.Message{llm.UserText("ping")}}); err == nil {
			t.Fatalf("legacy row produced a working provider: %+v", p)
		}
	}
}

func TestRestoredProfilesReachTheirOwnEndpoint(t *testing.T) {
	stub := newProviderStub(t)
	s := &Server{}
	for _, p := range []*db.LLMProfile{
		{AuthMode: db.AuthAnthropic, Format: "anthropic", BaseURL: stub.URL, Model: "claude-test", APIKey: "anthropic-fixture"},
		{AuthMode: db.AuthOpenAICompatible, Format: "openai", BaseURL: stub.URL + "/v1", Model: "local-test"},
	} {
		prov, err := s.configForProfile(p).NewProvider()
		if err != nil {
			t.Fatal(err)
		}
		msg, stop, _, err := prov.Complete(context.Background(), llm.CompletionRequest{Messages: []llm.Message{llm.UserText("ping")}})
		if err != nil || stop != "end_turn" || msg.Text() != "OK" {
			t.Fatalf("%s: msg=%q stop=%q err=%v", p.AuthMode, msg.Text(), stop, err)
		}
	}
	keys, paths := stub.seen()
	if fmt.Sprint(paths) != "[/v1/messages /v1/chat/completions]" || keys[0] != "anthropic-fixture" || keys[1] != "" {
		t.Fatalf("requests: paths=%v keys=%v", paths, keys)
	}
}

func testLLMRequest(s *Server, body map[string]any) map[string]any {
	raw, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	s.testLLM(rec, httptest.NewRequest("POST", "/api/llm/test", strings.NewReader(string(raw))))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out == nil {
		out = map[string]any{"status": rec.Code, "body": rec.Body.String()}
	}
	return out
}

// A saved key is lent only to the same connection kind and address: never to a
// new address, never to the OpenAI endpoint, never to the model list.
func TestSavedRestoredKeyIsNotReusedElsewhere(t *testing.T) {
	m := stateGraphManager(t)
	s := newAdmissionTestServer(m, nil)
	saved, other := newProviderStub(t), newProviderStub(t)
	name := fmt.Sprintf("restored key %d", time.Now().UnixNano())
	id, err := m.pg.SaveProfile(&db.LLMProfile{Name: name, AuthMode: db.AuthAnthropic, BaseURL: saved.URL, Model: "claude-test", APIKey: "saved-anthropic-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.pg.DeleteProfile(id) })

	same := testLLMRequest(s, map[string]any{"profile_id": id, "auth_mode": "anthropic", "base_url": saved.URL, "model": "claude-test"})
	if same["ok"] != true {
		t.Fatalf("same connection test: %v", same)
	}
	moved := testLLMRequest(s, map[string]any{"profile_id": id, "auth_mode": "anthropic", "base_url": other.URL, "model": "claude-test"})
	if moved["ok"] != false || !strings.Contains(fmt.Sprint(moved["error"]), "Anthropic API 키") {
		t.Fatalf("moved address test: %v", moved)
	}
	// Without a key of its own the OpenAI test stops before any request.
	openai := testLLMRequest(s, map[string]any{"profile_id": id, "auth_mode": "api_key", "model": "gpt-test"})
	if openai["ok"] != false || openai["error"] != trMsg(errLLMTestNoAPIKey) {
		t.Fatalf("Anthropic key lent to an OpenAI test: %v", openai)
	}
	if keys, _ := other.seen(); len(keys) != 0 {
		t.Fatalf("a request reached the new address: %v", keys)
	}
	if keys, _ := saved.seen(); len(keys) != 1 || keys[0] != "saved-anthropic-fixture" {
		t.Fatalf("saved endpoint requests: %v", keys)
	}

	rec := httptest.NewRecorder()
	s.pgListModels(rec, httptest.NewRequest("POST", "/api/llm/models", strings.NewReader(fmt.Sprintf(`{"profile_id":%d}`, id))))
	if rec.Code != 400 {
		t.Fatalf("model list with a restored profile: HTTP %d %s", rec.Code, rec.Body.String())
	}

	// Editing the address without a new key is refused for Anthropic.
	rec = httptest.NewRecorder()
	body := fmt.Sprintf(`{"id":%d,"name":%q,"auth_mode":"anthropic","base_url":%q,"model":"claude-test"}`, id, name, other.URL)
	s.pgSaveProfile(rec, httptest.NewRequest("POST", "/api/llm/profiles", strings.NewReader(body)))
	if rec.Code != 400 {
		t.Fatalf("address change without a key: HTTP %d %s", rec.Code, rec.Body.String())
	}

	// A keyless local profile keeps its key on the same address and drops it on a new one.
	local, err := m.pg.SaveProfile(&db.LLMProfile{Name: name + " local", AuthMode: db.AuthOpenAICompatible, BaseURL: saved.URL + "/v1", Model: "local-test", APIKey: "local-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.pg.DeleteProfile(local) })
	for _, step := range []struct {
		base, want string
	}{{saved.URL + "/v1", "local-fixture"}, {other.URL + "/v1", ""}} {
		if _, err := m.pg.SaveProfile(&db.LLMProfile{ID: local, Name: name + " local", AuthMode: db.AuthOpenAICompatible, BaseURL: step.base, Model: "local-test"}); err != nil {
			t.Fatal(err)
		}
		p, err := m.pg.ProfileByID(local)
		if err != nil || p.APIKey != step.want || p.BaseURL != step.base || !p.PoolExclude {
			t.Fatalf("base %s: key=%q base=%q pool_exclude=%v err=%v", step.base, p.APIKey, p.BaseURL, p.PoolExclude, err)
		}
	}
}
