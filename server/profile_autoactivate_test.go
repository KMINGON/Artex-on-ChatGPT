package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func saveProfileForTest(t *testing.T, s *Server, body string) int64 {
	t.Helper()
	w := httptest.NewRecorder()
	s.pgSaveProfile(w, httptest.NewRequest("POST", "/api/llm/profiles", strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("save: HTTP %d %s", w.Code, w.Body.String())
	}
	var result struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.ID
}

func TestFirstUsableProfileAutomaticallyActivatesAndApplies(t *testing.T) {
	pg := profileSelectionDB(t)
	s := &Server{m: &Manager{pg: pg, dir: t.TempDir()}, provByProfile: map[int64]*provEntry{}}
	id := saveProfileForTest(t, s, `{"name":"draft","model":"fixture-model"}`)
	if active, err := pg.ActiveProfile(); err != nil || active != nil || s.llmOn {
		t.Fatalf("keyless draft activated: %+v %v", active, err)
	}
	// Supplying a key on a later edit also fixes a previously saved draft.
	saveProfileForTest(t, s, fmt.Sprintf(`{"id":%d,"name":"ready","model":"fixture-model","api_key":"fixture"}`, id))
	active, err := pg.ActiveProfile()
	if err != nil || active == nil || active.ID != id || !s.llmOn || s.chatAgentRef() == nil {
		t.Fatalf("first valid profile was not applied: active=%+v on=%v err=%v", active, s.llmOn, err)
	}
	global, _, _ := s.globalProvider()
	cached, _, _ := s.providerForProfile(id)
	if global != cached {
		t.Fatal("automatic activation did not reuse the profile provider cache")
	}
	saveProfileForTest(t, s, `{"name":"later","model":"later-model","api_key":"fixture"}`)
	active, _ = pg.ActiveProfile()
	if active.ID != id || s.llmCfg.Model != "fixture-model" {
		t.Fatal("saving another profile replaced the existing selection")
	}
}

func TestFirstProfileDoesNotReplaceEnvironmentOrUnavailableDefault(t *testing.T) {
	for _, source := range []string{"environment", "disconnected-default"} {
		t.Run(source, func(t *testing.T) {
			pg := profileSelectionDB(t)
			s := &Server{m: &Manager{pg: pg, dir: t.TempDir()}, provByProfile: map[int64]*provEntry{}}
			var previous int64
			if source == "environment" {
				cfg := agent.ConfigFrom("openai-responses", "env-model", "", "fixture-env", "")
				if err := s.applyLLM(cfg); err != nil {
					t.Fatal(err)
				}
			} else {
				var err error
				previous, err = pg.SaveProfile(&db.LLMProfile{Name: "selected", Model: "selected-model"})
				if err != nil {
					t.Fatal(err)
				}
				if err := pg.SetActiveProfile(previous); err != nil {
					t.Fatal(err)
				}
			}
			saveProfileForTest(t, s, `{"name":"new","model":"new-model","api_key":"fixture"}`)
			active, err := pg.ActiveProfile()
			if err != nil {
				t.Fatal(err)
			}
			if source == "environment" {
				if active != nil || s.llmCfg.Model != "env-model" {
					t.Fatal("environment model replaced")
				}
			} else if active == nil || active.ID != previous {
				t.Fatal("existing unavailable selection replaced")
			}
		})
	}
}

func TestFirstSubscriptionProfileActivatesOnlyWhenConnected(t *testing.T) {
	for _, connected := range []bool{false, true} {
		t.Run(fmt.Sprint(connected), func(t *testing.T) {
			pg := profileSelectionDB(t)
			dir := t.TempDir()
			account := map[string]any{"client_id": "fixture-account", "scopes": []string{"chatgpt.tokens.use.direct"}}
			if connected {
				account["subject"] = "fixture-subject"
				account["issuer"] = "https://auth.openai.com"
				account["refresh_token"] = "fixture-refresh"
				account["refresh_expires_at"] = time.Now().Add(time.Hour)
			}
			raw, err := json.Marshal(map[string]any{"version": 1, "host_id": "urn:uuid:12345678-1234-4234-8234-123456789abc", "accounts": map[string]any{"fixture-account": account}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "accounts.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			s := &Server{m: &Manager{pg: pg, dir: t.TempDir()}, chatGPTDir: dir, provByProfile: map[int64]*provEntry{}}
			t.Cleanup(func() {
				if s.chatGPT != nil {
					_ = s.chatGPT.Close()
				}
			})
			id := saveProfileForTest(t, s, `{"name":"subscription","model":"fixture-model","auth_mode":"chatgpt_plan","chatgpt_account_id":"fixture-account"}`)
			active, err := pg.ActiveProfile()
			if err != nil {
				t.Fatal(err)
			}
			if connected && (active == nil || active.ID != id || s.llmCfg.AuthMode != "chatgpt_plan") {
				t.Fatal("connected subscription was not activated")
			}
			if !connected && active != nil {
				t.Fatal("disconnected subscription activated")
			}
		})
	}
}

func TestConcurrentFirstProfileSavesKeepSingleAppliedDefault(t *testing.T) {
	pg := profileSelectionDB(t)
	s := &Server{m: &Manager{pg: pg, dir: t.TempDir()}, provByProfile: map[int64]*provEntry{}}
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := httptest.NewRecorder()
			s.pgSaveProfile(w, httptest.NewRequest("POST", "/api/llm/profiles", strings.NewReader(fmt.Sprintf(`{"name":"profile-%d","model":"model-%d","api_key":"fixture"}`, i, i))))
			results <- w
		}(i)
	}
	wg.Wait()
	close(results)
	for w := range results {
		if w.Code != 200 {
			t.Fatalf("concurrent save: HTTP %d %s", w.Code, w.Body.String())
		}
	}
	profiles, err := pg.ListProfiles()
	if err != nil {
		t.Fatal(err)
	}
	defaults := 0
	for _, p := range profiles {
		if p.IsDefault {
			defaults++
			if p.Model != s.llmCfg.Model {
				t.Fatal("stored and applied model diverged")
			}
		}
	}
	if defaults != 1 || len(profiles) != 2 {
		t.Fatalf("profiles=%d defaults=%d", len(profiles), defaults)
	}
}

func TestFirstProfileSaveRecoversAfterActiveReadFailure(t *testing.T) {
	var fail atomic.Bool
	driverName := fmt.Sprintf("first-profile-read-fault-%d", profileReadFaultSequence.Add(1))
	sql.Register(driverName, profileReadFaultDriver{fail: &fail})
	pg := profileSelectionDB(t, driverName)
	s := &Server{m: &Manager{pg: pg, dir: t.TempDir()}, provByProfile: map[int64]*provEntry{}}
	fail.Store(true)
	w := httptest.NewRecorder()
	s.pgSaveProfile(w, httptest.NewRequest("POST", "/api/llm/profiles", strings.NewReader(`{"name":"first","model":"fixture-model","api_key":"fixture"}`)))
	if w.Code != 503 {
		t.Fatalf("failed active lookup: HTTP %d", w.Code)
	}
	fail.Store(false)
	saveProfileForTest(t, s, `{"id":1,"name":"first","model":"fixture-model"}`)
	active, err := pg.ActiveProfile()
	if err != nil || active == nil || active.ID != 1 || s.llmCfg.Model != "fixture-model" {
		t.Fatalf("could not recover first selection: %+v %v", active, err)
	}
}
