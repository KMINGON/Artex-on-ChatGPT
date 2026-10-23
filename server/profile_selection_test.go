package server

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
	"modernc.org/sqlite"
)

func TestProfileInvalidationRejectsStaleChatAgent(t *testing.T) {
	m := &Manager{dir: t.TempDir()}
	oldCfg := agent.ConfigFrom("openai-responses", "old-model", "", "fixture-old", "")
	newCfg := agent.ConfigFrom("openai-responses", "new-model", "", "fixture-new", "")
	s := &Server{m: m, profChatAgents: map[int64]*agent.ChatAgent{}, provByProfile: map[int64]*provEntry{1: {cfg: oldCfg}}}
	m.mu.Lock()
	done := make(chan *agent.ChatAgent, 1)
	go func() { done <- s.chatAgentForProfile(1) }()
	deadline := time.Now().Add(5 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		if profileTestProxyBlocked() {
			blocked = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !blocked {
		m.mu.Unlock()
		t.Fatal("did not reach deterministic block")
	}
	s.invalidateProfileAgents()
	s.provCacheMu.Lock()
	s.provByProfile[1] = &provEntry{cfg: newCfg}
	s.provCacheMu.Unlock()
	m.mu.Unlock()
	ca := <-done
	cached := s.chatAgentForProfile(1)
	model := reflect.ValueOf(cached).Elem().FieldByName("model").String()
	if cached != ca || model != "new-model" {
		t.Fatalf("stale chat agent survived invalidation: model=%s", model)
	}

}

func profileSelectionDB(t *testing.T, drivers ...string) *db.DB {
	t.Helper()
	name := "sqlite"
	if len(drivers) > 0 {
		name = drivers[0]
	}
	sq, err := sql.Open(name, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sq.Close() })
	sq.SetMaxOpenConns(1)
	schema := `CREATE TABLE settings(key TEXT PRIMARY KEY,value TEXT);
 CREATE TABLE llm_profiles(id INTEGER PRIMARY KEY AUTOINCREMENT,name TEXT,format TEXT,base_url TEXT,proxy TEXT,model TEXT,api_key TEXT,api_key_hint TEXT,rate_per_second REAL DEFAULT 0,rate_per_minute REAL DEFAULT 0,context_window_k INTEGER DEFAULT 0,reasoning_effort TEXT,is_default BOOLEAN DEFAULT false,priority INTEGER DEFAULT 0,pool_exclude BOOLEAN DEFAULT false,thinking_type TEXT,streaming BOOLEAN DEFAULT true,max_tokens INTEGER DEFAULT 0,max_tokens_field TEXT,session_header_key TEXT,retry_connect_attempts INTEGER DEFAULT 0,retry_connect_interval_ms INTEGER DEFAULT 0,retry_empty_attempts INTEGER DEFAULT 0,retry_empty_interval_ms INTEGER DEFAULT 0,retry_stream_attempts INTEGER DEFAULT 0,retry_stream_interval_ms INTEGER DEFAULT 0,auth_mode TEXT,chatgpt_account_id TEXT DEFAULT '',access_program_cyber TEXT NOT NULL DEFAULT '',connection_check TEXT);`
	if _, err = sq.Exec(schema); err != nil {
		t.Fatal(err)
	}
	return &db.DB{DB: sq}
}

func TestActivateKeylessDraftKeepsPreviousSelection(t *testing.T) {
	for _, mode := range []string{"api_key", "chatgpt_plan"} {
		t.Run(mode, func(t *testing.T) {
			pg := profileSelectionDB(t)
			id, err := pg.SaveProfile(&db.LLMProfile{Name: "previous", Model: "previous-model", AuthMode: mode, APIKey: "fixture", ChatGPTAccountID: "registered-fixture"})
			if err != nil {
				t.Fatal(err)
			}
			if err = pg.SetActiveProfile(id); err != nil {
				t.Fatal(err)
			}
			m := &Manager{pg: pg, dir: t.TempDir()}
			s := &Server{m: m, chatGPTDir: t.TempDir(), profChatAgents: map[int64]*agent.ChatAgent{}, provByProfile: map[int64]*provEntry{}}
			t.Cleanup(func() {
				if s.chatGPT != nil {
					_ = s.chatGPT.Close()
				}
			})
			create := httptest.NewRecorder()
			s.pgSaveProfile(create, httptest.NewRequest("POST", "/api/llm/profiles", strings.NewReader(`{"name":"draft","model":"new-model","auth_mode":"api_key"}`)))
			if create.Code != 200 {
				t.Fatalf("draft save: %d %s", create.Code, create.Body.String())
			}
			prior := s.chatAgentRef()
			activate := httptest.NewRecorder()
			s.pgActivateProfile(activate, httptest.NewRequest("POST", "/api/llm/profiles/active", strings.NewReader(`{"id":2}`)))
			if activate.Code != 400 {
				t.Fatalf("keyless activation: %d %s", activate.Code, activate.Body.String())
			}
			selected, err := pg.ActiveProfile()
			if err != nil {
				t.Fatal(err)
			}
			if selected.ID != id || s.chatAgentRef() != prior {
				t.Fatal("rejected activation changed the previous connection")
			}
		})
	}
}

func TestPersistedKeylessSelectionFailsClosed(t *testing.T) {
	pg := profileSelectionDB(t)
	id, err := pg.SaveProfile(&db.LLMProfile{Name: "keyless", Model: "selected-model", AuthMode: "api_key"})
	if err != nil {
		t.Fatal(err)
	}
	if err = pg.SetActiveProfile(id); err != nil {
		t.Fatal(err)
	}
	m := &Manager{pg: pg, dir: t.TempDir()}
	prior := agent.NewChatAgent(nil, "previous-model", m.dir, nil, 0)
	s := &Server{m: m, chatAgent: prior, profChatAgents: map[int64]*agent.ChatAgent{}, provByProfile: map[int64]*provEntry{}}
	s.reapplyActiveProfile()
	if s.chatAgentRef() == prior || s.llmCfg.Model != "selected-model" {
		t.Fatal("unavailable selection retained the previous connection")
	}
	// An explicit conversation pin must not fall through to a usable global agent.
	s.chatAgent = prior
	pinned := s.resolveChatAgent(&db.Conversation{LLMProfileID: &id})
	if pinned == nil || pinned == prior || reflect.ValueOf(pinned).Elem().FieldByName("model").String() != "selected-model" {
		t.Fatal("unavailable conversation selection fell back to the previous connection")
	}
	provider, _, selected := s.providerForProfile(id)
	if !selected {
		t.Fatal("explicit unavailable profile was dropped")
	}
	_, _, _, err = provider.Complete(context.Background(), llm.CompletionRequest{})
	var policy interface{ NoFallback() bool }
	if err == nil || !errors.As(err, &policy) || !policy.NoFallback() {
		t.Fatalf("unavailable selection did not fail closed: %v", err)
	}
}

func TestAccessProgramProfileRoundTripAndValidation(t *testing.T) {
	pg := profileSelectionDB(t)
	s := &Server{m: &Manager{pg: pg, dir: t.TempDir()}, profChatAgents: map[int64]*agent.ChatAgent{}, provByProfile: map[int64]*provEntry{}}
	save := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.pgSaveProfile(w, httptest.NewRequest("POST", "/api/llm/profiles", strings.NewReader(body)))
		return w
	}
	w := save(`{"name":"explicit-program","model":"gpt-6-sol","api_key":"fixture","auth_mode":"api_key","reasoning_effort":"none","access_program_cyber":"daybreak_blue"}`)
	if w.Code != 200 {
		t.Fatalf("API save: %d %s", w.Code, w.Body.String())
	}
	p, err := pg.ProfileByID(1)
	if err != nil {
		t.Fatal(err)
	}
	if p.AccessProgramCyber != "daybreak_blue" || p.ReasoningEffort != "none" || p.Model != "gpt-6-sol" {
		t.Fatal("explicit options/model were not preserved")
	}
	cfg, ok := s.loadProfileConfig(p.ID)
	if !ok || cfg.AccessProgramCyber != "daybreak_blue" {
		t.Fatal("program not propagated to runtime")
	}
	dto := llmProfileDTO(p)
	if dto.AccessProgramCyber != "daybreak_blue" || !dto.Capabilities.Known {
		t.Fatal("program/capabilities not exposed in public profile")
	}
	w = save(`{"id":1,"name":"same-registration","model":"gpt-6-sol","auth_mode":"api_key","reasoning_effort":"none","access_program_cyber":""}`)
	if w.Code != 200 {
		t.Fatalf("reset: %d %s", w.Code, w.Body.String())
	}
	changed, err := pg.ProfileByID(1)
	if err != nil {
		t.Fatal(err)
	}
	if changed.AccessProgramCyber != "" || changed.APIKey != "fixture" || changed.ID != p.ID {
		t.Fatal("reset did not preserve profile ID/key")
	}
	for _, body := range []string{
		`{"name":"bad","model":"gpt-6-sol","auth_mode":"api_key","access_program_cyber":"invented"}`,
		`{"name":"bad","model":"account-model","auth_mode":"chatgpt_plan","access_program_cyber":"daybreak_blue"}`,
	} {
		if got := save(body); got.Code != 400 {
			t.Errorf("unsupported explicit options accepted: %d", got.Code)
		}
	}
}

func TestChatGPTRecheckRejectsNonSubscriptionAndRemoteRequests(t *testing.T) {
	pg := profileSelectionDB(t)
	_, err := pg.SaveProfile(&db.LLMProfile{Name: "api", Model: "existing", AuthMode: "api_key", APIKey: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{pg: pg}}
	for _, tc := range []struct {
		remote string
		want   int
	}{{"127.0.0.1:1234", 400}, {"192.0.2.1:1234", 403}} {
		r := httptest.NewRequest("POST", "/api/chatgpt/recheck", strings.NewReader(`{"profile_id":1}`))
		r.RemoteAddr = tc.remote
		w := httptest.NewRecorder()
		s.chatGPTRecheck(w, r)
		if w.Code != tc.want || s.chatGPT != nil {
			t.Fatalf("recheck crossed auth boundary: HTTP %d", w.Code)
		}
	}
}

// The connection wrapper fails only the post-commit active-profile read. All
// writes and ordinary profile reads still reach SQLite unchanged.
var profileReadFaultSequence atomic.Uint64

type profileReadFaultDriver struct{ fail *atomic.Bool }

func (d profileReadFaultDriver) Open(name string) (driver.Conn, error) {
	c, err := (&sqlite.Driver{}).Open(name)
	if err != nil {
		return nil, err
	}
	return &profileReadFaultConn{Conn: c, fail: d.fail}, nil
}

type profileReadFaultConn struct {
	driver.Conn
	fail *atomic.Bool
}

func (c *profileReadFaultConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if c.fail.Load() && strings.Contains(q, "FROM llm_profiles WHERE is_default") {
		return nil, errors.New("injected active-profile read failure")
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}

func TestProfileActivationReadFailureBlocksPreviousConnection(t *testing.T) {
	var fail atomic.Bool
	driverName := fmt.Sprintf("profile-read-fault-%d", profileReadFaultSequence.Add(1))
	sql.Register(driverName, profileReadFaultDriver{fail: &fail})
	pg := profileSelectionDB(t, driverName)
	first, err := pg.SaveProfile(&db.LLMProfile{Name: "first", Model: "first-model", APIKey: "fixture-first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := pg.SaveProfile(&db.LLMProfile{Name: "second", Model: "second-model", APIKey: "fixture-second"})
	if err != nil {
		t.Fatal(err)
	}
	if err := pg.SetActiveProfile(first); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{pg: pg, dir: t.TempDir()}, provByProfile: map[int64]*provEntry{}}
	if err := s.reapplyActiveProfile(); err != nil {
		t.Fatal(err)
	}
	previous := s.chatAgentRef()
	fail.Store(true)
	result := httptest.NewRecorder()
	s.pgActivateProfile(result, httptest.NewRequest("POST", "/api/llm/profiles/active", strings.NewReader(fmt.Sprintf(`{"id":%d}`, second))))
	if result.Code != 503 {
		t.Fatalf("reload failure returned %d: %s", result.Code, result.Body.String())
	}
	if previous == s.chatAgentRef() {
		t.Fatal("previous ChatAgent survived failed reload")
	}
	prov, _, selected := s.globalProvider()
	if !selected {
		t.Fatal("unavailable selection permits unrelated fallback")
	}
	_, _, _, err = prov.Complete(context.Background(), llm.CompletionRequest{})
	var noFallback interface{ NoFallback() bool }
	var noReplay interface{ NoReplay() bool }
	if !errors.As(err, &noFallback) || !noFallback.NoFallback() || !errors.As(err, &noReplay) || !noReplay.NoReplay() {
		t.Fatalf("reload error permits retry/fallback: %v", err)
	}
	fail.Store(false)
	active, err := pg.ActiveProfile()
	if err != nil || active.ID != second {
		t.Fatalf("committed selection lost: %#v %v", active, err)
	}
	recovery := httptest.NewRecorder()
	s.pgActivateProfile(recovery, httptest.NewRequest("POST", "/api/llm/profiles/active", strings.NewReader(fmt.Sprintf(`{"id":%d}`, second))))
	if recovery.Code != 200 || s.llmCfg.Model != "second-model" {
		t.Fatalf("could not recover selection: %d %s", recovery.Code, recovery.Body.String())
	}
}

func TestProfileActivationSerializesCommitAndApply(t *testing.T) {
	pg := profileSelectionDB(t)
	for _, name := range []string{"first", "second"} {
		if _, err := pg.SaveProfile(&db.LLMProfile{Name: name, Model: name + "-model", APIKey: "fixture-" + name}); err != nil {
			t.Fatal(err)
		}
	}
	m := &Manager{pg: pg, dir: t.TempDir()}
	s := &Server{m: m, provByProfile: map[int64]*provEntry{}}
	m.mu.Lock()
	unlocked := false
	defer func() {
		if !unlocked {
			m.mu.Unlock()
		}
	}()
	first, second := httptest.NewRecorder(), httptest.NewRecorder()
	firstDone, secondDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(firstDone)
		s.pgActivateProfile(first, httptest.NewRequest("POST", "/api/llm/profiles/active", strings.NewReader(`{"id":1}`)))
	}()
	deadline := time.Now().Add(5 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		if profileTestProxyBlocked() {
			blocked = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !blocked {
		t.Fatal("first activation did not reach apply block")
	}
	go func() {
		defer close(secondDone)
		s.pgActivateProfile(second, httptest.NewRequest("POST", "/api/llm/profiles/active", strings.NewReader(`{"id":2}`)))
	}()
	// The first writer owns the mutex until its live connection is installed.
	if s.profileUpdateMu.TryLock() {
		s.profileUpdateMu.Unlock()
		t.Fatal("commit/apply not serialized")
	}
	active, err := pg.ActiveProfile()
	if err != nil || active.ID != 1 {
		t.Fatalf("second commit overtook first apply: %#v %v", active, err)
	}
	m.mu.Unlock()
	unlocked = true
	for _, done := range []chan struct{}{firstDone, secondDone} {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("profile activation deadlocked")
		}
	}
	active, err = pg.ActiveProfile()
	if first.Code != 200 || second.Code != 200 || err != nil || active.ID != 2 || s.llmCfg.Model != "second-model" {
		t.Fatalf("commit/apply diverged: first=%d second=%d active=%#v model=%s error=%v", first.Code, second.Code, active, s.llmCfg.Model, err)
	}
}

func profileTestProxyBlocked() bool {
	// Grow until the complete goroutine dump fits; full-package tests can have
	// many database workers and a fixed 64 KiB buffer missed the target waiter.
	for size := 64 * 1024; ; size *= 2 {
		b := make([]byte, size)
		n := runtime.Stack(b, true)
		if n < len(b) {
			return strings.Contains(string(b[:n]), "server.(*Manager).ProxyAddr(")
		}
	}
}
