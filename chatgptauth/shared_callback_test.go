package chatgptauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func sharedFixture(t *testing.T, ttl time.Duration) (*fixture, *httptest.Server) {
	t.Helper()
	f := setup(t)
	if err := f.m.Close(); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) { f.m.ServeCallback(w, r) })
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	srv := httptest.NewUnstartedServer(mux)
	var err error
	f.m, err = newManager(f.dir, options{
		client:   &http.Client{Transport: roundTripFunc(f.p.transport)},
		now:      func() time.Time { return time.Unix(f.now.Load(), 0) },
		loginTTL: ttl, callbackURL: "http://" + srv.Listener.Addr().String() + callbackPath,
	})
	if err != nil {
		srv.Close()
		t.Fatal(err)
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return f, srv
}

func assertSharedServerAlive(t *testing.T, srv *httptest.Server) {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("login lifecycle stopped shared server: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("health status=%d", resp.StatusCode)
	}
}

func TestSharedCallbackConfigurationRejectsNonCanonicalURLsBeforeStorage(t *testing.T) {
	for _, raw := range []string{
		"https://127.0.0.1:8787/auth/callback", "http://localhost:8787/auth/callback",
		"http://[::1]:8787/auth/callback", "http://0.0.0.0:8787/auth/callback",
		"http://127.0.0.1/auth/callback", "http://127.0.0.1:0/auth/callback",
		"http://127.0.0.1:65536/auth/callback", "http://127.0.0.1:08787/auth/callback",
		"http://user:password@127.0.0.1:8787/auth/callback", "http://127.0.0.1:8787/other",
		"http://127.0.0.1:8787/auth/callback?", "http://127.0.0.1:8787/auth/callback?x=1",
		"http://127.0.0.1:8787/auth/callback#", "http://127.0.0.1:8787/auth/callback#x",
		"http://127.0.0.1:8787/%61uth/callback", "http://127.0.0.1:8787/auth/callback/",
		"http://127.0.0.1:8787/auth/../auth/callback", "http://127.0.0.1:8787/auth/callback ",
	} {
		t.Run(raw, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "must-not-create")
			m, err := NewWithCallback(dir, raw)
			if err == nil {
				m.Close()
				t.Fatalf("accepted callback %q", raw)
			}
			if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
				t.Fatal("invalid configuration touched credential storage")
			}
			if strings.Contains(err.Error(), "password") {
				t.Fatal("invalid URL echoed credentials")
			}
		})
	}
	for _, raw := range []string{"", "http://127.0.0.1:1/auth/callback", "http://127.0.0.1:65535/auth/callback"} {
		m, err := NewWithCallback(filepath.Join(t.TempDir(), "auth"), raw)
		if err != nil {
			t.Fatalf("valid callback rejected: %v", err)
		}
		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSharedCallbackUsesConfiguredRedirectAndPreservesPKCE(t *testing.T) {
	f, srv := sharedFixture(t, 0)
	q := f.login(t, "shared-one") // Existing fixture checks the complete token-exchange form and PKCE.
	if q.Get("redirect_uri") != srv.URL+callbackPath {
		t.Fatalf("redirect=%s", q.Get("redirect_uri"))
	}
	if f.m.Snapshot().Pending || !f.m.Snapshot().Accounts[0].PlanEnabled {
		t.Fatal("shared callback did not complete login")
	}
	status, _ := callback(t, q, url.Values{"client_id": {"shared-one"}, "code": {"replay"}})
	if status != http.StatusNotFound {
		t.Fatalf("completed shared flow replay status=%d", status)
	}
	assertSharedServerAlive(t, srv)
	if err := f.m.Close(); err != nil {
		t.Fatal(err)
	}
	assertSharedServerAlive(t, srv)
	status, _ = callback(t, q, url.Values{"client_id": {"shared-one"}, "code": {"after-close"}})
	if status != http.StatusGone {
		t.Fatalf("closed callback status=%d", status)
	}
}

func TestSharedCallbackRejectsWrongHostStateAndMethodWithoutConsumingLogin(t *testing.T) {
	f, _ := sharedFixture(t, 0)
	q := f.start(t, "")
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Host = "attacker.test" },
		func(r *http.Request) {
			r.Host = "127.0.0.1:1"
			r.Header.Set("X-Forwarded-Host", strings.TrimPrefix(q.Get("redirect_uri"), "http://"))
		},
		func(r *http.Request) { r.Method = http.MethodPost },
		func(r *http.Request) { r.URL.Path = "/wrong" },
		func(r *http.Request) { r.URL.RawQuery = "state=wrong&client_id=shared-one&code=test" },
		func(r *http.Request) { r.URL.RawQuery += "&state=" + url.QueryEscape(q.Get("state")) },
	} {
		values := url.Values{"state": {q.Get("state")}, "client_id": {"shared-one"}, "code": {"test"}}
		r := httptest.NewRequest(http.MethodGet, q.Get("redirect_uri")+"?"+values.Encode(), nil)
		mutate(r)
		w := httptest.NewRecorder()
		f.m.ServeCallback(w, r)
		if w.Code != http.StatusBadRequest || !f.m.Snapshot().Pending {
			t.Fatalf("untrusted callback consumed pending flow: %d", w.Code)
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("callback cache/referrer headers missing")
		}
	}
	f.p.mu.Lock()
	calls := len(f.p.forms)
	f.p.mu.Unlock()
	if calls != 0 {
		t.Fatalf("invalid callback reached token exchange: %d", calls)
	}
}

func TestSharedCallbackReplacementAndExpiryLeaveServerRunning(t *testing.T) {
	f, srv := sharedFixture(t, time.Second)
	old := f.start(t, "")
	current := f.start(t, "")
	status, _ := callback(t, old, url.Values{"client_id": {"old"}, "code": {"old-code"}})
	if status != http.StatusBadRequest || !f.m.Snapshot().Pending {
		t.Fatalf("old state affected new login: %d", status)
	}
	f.m.mu.Lock()
	pending := f.m.pending
	f.m.mu.Unlock()
	select {
	case <-pending.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("login TTL did not expire")
	}
	status, _ = callback(t, current, url.Values{"client_id": {"expired"}, "code": {"expired-code"}})
	if status != http.StatusBadRequest && status != http.StatusNotFound {
		t.Fatalf("expired callback status=%d", status)
	}
	f.p.mu.Lock()
	calls := len(f.p.forms)
	f.p.mu.Unlock()
	if calls != 0 {
		t.Fatal("expired/replaced login exchanged tokens")
	}
	assertSharedServerAlive(t, srv)
}

func TestSharedCallbackConcurrentReplayExchangesOnlyOnce(t *testing.T) {
	f, srv := sharedFixture(t, 0)
	q := f.start(t, "")
	token := f.p.sign(t, f.claims("shared-one", q.Get("nonce")))
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	f.p.setToken(func(ctx context.Context, form url.Values) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		challenge := sha256.Sum256([]byte(form.Get("code_verifier")))
		if form.Get("redirect_uri") != q.Get("redirect_uri") || base64.RawURLEncoding.EncodeToString(challenge[:]) != q.Get("code_challenge") {
			t.Error("shared exchange lost redirect/PKCE")
		}
		return jsonResponse(200, map[string]any{"access_token": "access", "refresh_token": "refresh", "id_token": token, "token_type": "Bearer", "expires_in": 3600, "scope": requestedScopes}), nil
	})
	values := url.Values{"state": {q.Get("state")}, "client_id": {"shared-one"}, "code": {"test-code"}}
	first := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		f.m.ServeCallback(w, httptest.NewRequest(http.MethodGet, q.Get("redirect_uri")+"?"+values.Encode(), nil))
		first <- w.Code
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("exchange did not start")
	}
	w := httptest.NewRecorder()
	f.m.ServeCallback(w, httptest.NewRequest(http.MethodGet, q.Get("redirect_uri")+"?"+values.Encode(), nil))
	if w.Code != http.StatusBadRequest || calls.Load() != 1 {
		t.Fatalf("concurrent replay status=%d exchanges=%d", w.Code, calls.Load())
	}
	close(release)
	if status := <-first; status != http.StatusOK {
		t.Fatalf("first callback=%d", status)
	}
	assertSharedServerAlive(t, srv)
}

func TestSharedCallbackCloseCancelsInFlightExchangeOnly(t *testing.T) {
	f, srv := sharedFixture(t, 0)
	q := f.start(t, "")
	entered := make(chan struct{})
	f.p.setToken(func(ctx context.Context, _ url.Values) (*http.Response, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	values := url.Values{"state": {q.Get("state")}, "client_id": {"shared-one"}, "code": {"test-code"}}
	result := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		f.m.ServeCallback(w, httptest.NewRequest(http.MethodGet, q.Get("redirect_uri")+"?"+values.Encode(), nil))
		result <- w.Code
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("exchange did not start")
	}
	closed := make(chan error, 1)
	go func() { closed <- f.m.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel shared exchange")
	}
	if status := <-result; status != http.StatusBadRequest {
		t.Fatalf("canceled exchange status=%d", status)
	}
	if f.m.Snapshot().Pending || f.m.Snapshot().Accounts[0].Connected {
		t.Fatal("closed login activated account")
	}
	assertSharedServerAlive(t, srv)
}

func TestSharedCallbackNewLoginCancelsOlderExchangeWithoutClearingNewState(t *testing.T) {
	f, srv := sharedFixture(t, 0)
	old := f.start(t, "")
	entered := make(chan struct{})
	f.p.setToken(func(ctx context.Context, _ url.Values) (*http.Response, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	values := url.Values{"state": {old.Get("state")}, "client_id": {"shared-one"}, "code": {"old-code"}}
	result := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		f.m.ServeCallback(w, httptest.NewRequest(http.MethodGet, old.Get("redirect_uri")+"?"+values.Encode(), nil))
		result <- w.Code
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("exchange did not start")
	}
	current := f.start(t, "")
	select {
	case status := <-result:
		if status != http.StatusBadRequest {
			t.Fatalf("canceled older callback=%d", status)
		}
	case <-time.After(time.Second):
		t.Fatal("new login did not cancel older exchange")
	}
	f.m.mu.Lock()
	currentPending := f.m.pending != nil && f.m.pending.state == current.Get("state")
	f.m.mu.Unlock()
	if !currentPending || !f.m.Snapshot().Pending || f.m.Snapshot().Accounts[0].Connected {
		t.Fatal("older callback altered replacement login")
	}
	assertSharedServerAlive(t, srv)
}
