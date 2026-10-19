package chatgptauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func jsonResponse(status int, body any) *http.Response {
	b, _ := json.Marshal(body)
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(b)))}
}

// Requests retain production URLs. This transport is the only fake: no request
// can reach OpenAI, and callbacks still traverse the real loopback listener.
type fakeProvider struct {
	mu        sync.Mutex
	key       *rsa.PrivateKey
	discovery metadata
	token     func(context.Context, url.Values) (*http.Response, error)
	revoke    func(context.Context, url.Values) (*http.Response, error)
	forms     []url.Values
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeProvider{key: key, discovery: metadata{Issuer: issuer, Authorize: issuer + "/api/accounts/authorize", Token: issuer + "/api/accounts/oauth/token", JWKS: issuer + "/.well-known/jwks.json", Revoke: issuer + "/api/accounts/oauth/revoke"}}
}

func (p *fakeProvider) transport(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.URL.Host != "auth.openai.com" {
		return nil, errors.New("unexpected external request")
	}
	p.mu.Lock()
	d, token, revoke := p.discovery, p.token, p.revoke
	p.mu.Unlock()
	switch req.URL.String() {
	case discoveryURL:
		return jsonResponse(200, d), nil
	case issuer + "/.well-known/jwks.json":
		return jsonResponse(200, map[string]any{"keys": []any{map[string]string{"kty": "RSA", "kid": "test-key", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(p.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(p.key.E)).Bytes())}}}), nil
	case issuer + "/api/accounts/oauth/token", issuer + "/api/accounts/oauth/revoke":
		if req.Method != http.MethodPost || req.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			return nil, errors.New("invalid token request")
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			return nil, err
		}
		p.mu.Lock()
		p.forms = append(p.forms, form)
		p.mu.Unlock()
		if req.URL.Path == "/api/accounts/oauth/revoke" {
			if revoke != nil {
				return revoke(req.Context(), form)
			}
			return jsonResponse(200, map[string]any{}), nil
		}
		if token != nil {
			return token(req.Context(), form)
		}
		return jsonResponse(400, map[string]string{"error": "unexpected_exchange"}), nil
	}
	return nil, errors.New("unexpected authentication endpoint")
}

func (p *fakeProvider) setToken(fn func(context.Context, url.Values) (*http.Response, error)) {
	p.mu.Lock()
	p.token = fn
	p.mu.Unlock()
}

func (p *fakeProvider) sign(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "test-key"
	raw, err := token.SignedString(p.key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type fixture struct {
	m   *Manager
	p   *fakeProvider
	now atomic.Int64
	dir string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{p: newFakeProvider(t), dir: filepath.Join(t.TempDir(), "auth")}
	f.now.Store(time.Now().Truncate(time.Second).Unix())
	var err error
	f.m, err = newManager(f.dir, options{client: &http.Client{Transport: roundTripFunc(f.p.transport)}, now: func() time.Time { return time.Unix(f.now.Load(), 0) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.m.Close(); err != nil {
			t.Error(err)
		}
	})
	return f
}

func (f *fixture) claims(id, nonce string) jwt.MapClaims {
	return jwt.MapClaims{"iss": issuer, "aud": id, "sub": "subject-" + id, "email": "same@example.test", "nonce": nonce, "iat": f.now.Load(), "exp": f.now.Load() + 3600}
}

func (f *fixture) start(t *testing.T, id string) url.Values {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s, err := f.m.Start(ctx, id)
	cancel() // Browser flow must survive the initiating HTTP request.
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(s.URL)
	if err != nil || u.Scheme != "https" || u.Host != "auth.openai.com" {
		t.Fatalf("bad authorization URL: %v", err)
	}
	return u.Query()
}

func callback(t *testing.T, q, response url.Values) (int, string) {
	t.Helper()
	if response.Get("state") == "" {
		response.Set("state", q.Get("state"))
	}
	req, err := http.NewRequest(http.MethodGet, q.Get("redirect_uri")+"?"+response.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	if r.Header.Get("Cache-Control") != "no-store" || r.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Error("callback must prohibit caching/referrers")
	}
	return r.StatusCode, string(body)
}

func (f *fixture) login(t *testing.T, id string) url.Values {
	t.Helper()
	q := f.start(t, "")
	raw := f.p.sign(t, f.claims(id, q.Get("nonce")))
	f.p.setToken(func(_ context.Context, form url.Values) (*http.Response, error) {
		challenge := sha256.Sum256([]byte(form.Get("code_verifier")))
		if form.Get("grant_type") != "authorization_code" || form.Get("client_id") != id || form.Get("redirect_uri") != q.Get("redirect_uri") || form.Get("resource") != resource || base64.RawURLEncoding.EncodeToString(challenge[:]) != q.Get("code_challenge") {
			t.Error("code exchange does not match original authorize request")
		}
		return jsonResponse(200, map[string]any{"access_token": "access-" + id, "refresh_token": "refresh-" + id, "id_token": raw, "token_type": "Bearer", "expires_in": 3600, "scope": requestedScopes}), nil
	})
	status, body := callback(t, q, url.Values{"client_id": {id}, "code": {"test-code"}})
	if status != 200 {
		t.Fatalf("login failed: %d %s", status, body)
	}
	return q
}

func (f *fixture) record(id string) record {
	f.m.mu.Lock()
	defer f.m.mu.Unlock()
	return f.m.store.Accounts[id]
}

func TestStoreProtectionAndHostPersistence(t *testing.T) {
	f := setup(t)
	host := f.m.store.HostID
	if !validHostID(host) {
		t.Fatal("invalid generated host identifier")
	}
	if other, err := New(f.dir); err == nil {
		other.Close()
		t.Fatal("concurrent store access was allowed")
	}
	f.login(t, "client-one")
	if runtime.GOOS != "windows" {
		for path, want := range map[string]os.FileMode{f.dir: 0700, filepath.Join(f.dir, "accounts.json"): 0600} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != want {
				t.Fatalf("unsafe permissions for %s: %v", path, err)
			}
		}
	}
	if err := f.m.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := newManager(f.dir, options{client: &http.Client{Transport: roundTripFunc(f.p.transport)}, now: func() time.Time { return time.Unix(f.now.Load(), 0) }})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if second.store.HostID != host || !second.Snapshot().Accounts[0].Connected {
		t.Fatal("host or renewable login did not survive restart")
	}
	_, cancel, token, err := second.Authorize(context.Background(), "client-one")
	if err != nil || token != "access-client-one" {
		t.Fatalf("stored login not usable: %v", err)
	}
	cancel()
	q, err := second.Start(context.Background(), "client-one")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(q.URL)
	if u.Query().Get("ext_agent_host_id") != host || u.Query().Get("client_id") != "client-one" || u.Query().Get("agent_name_hint") != "" || u.Query().Get("id_token_hint") == "" {
		t.Fatal("reauthentication lost registration or hints")
	}
	encoded, _ := json.Marshal(second.Snapshot())
	for _, secret := range []string{"access-client-one", "refresh-client-one", second.store.Accounts["client-one"].IDToken} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("public state exposes credentials")
		}
	}
}

func TestSymlinkStorageRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	for _, name := range []string{"accounts.json", ".lock"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(t.TempDir(), "target")
			if err := os.WriteFile(target, []byte("untouched"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
			if m, err := New(dir); err == nil {
				m.Close()
				t.Fatal("symlink accepted")
			}
			data, _ := os.ReadFile(target)
			if string(data) != "untouched" {
				t.Fatal("external symlink target changed")
			}
		})
	}
}

func TestLoopbackPKCEAndMultipleRegistrations(t *testing.T) {
	f := setup(t)
	var changes atomic.Int32
	f.m.SetOnChange(func() { _ = f.m.Snapshot(); changes.Add(1) })
	q := f.login(t, "client-one")
	u, _ := url.Parse(q.Get("redirect_uri"))
	if u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Path != callbackPath || q.Get("client_id") != "dynamic_agent_client" || q.Get("agent_name_hint") != "Artex-on-ChatGPT" || q.Get("code_challenge_method") != "S256" || q.Get("scope") != requestedScopes || q.Get("resource") != resource {
		t.Fatal("invalid local OAuth authorization parameters")
	}
	f.login(t, "client-two")
	s := f.m.Snapshot()
	if len(s.Accounts) != 2 || !s.Accounts[0].PlanEnabled || !s.Accounts[1].PlanEnabled || s.Accounts[0].Email != s.Accounts[1].Email {
		t.Fatal("separate registrations were not retained by issued client_id")
	}
	if changes.Load() < 2 {
		t.Fatal("state change notifications missing")
	}
}

func TestCallbackTrustBoundaries(t *testing.T) {
	t.Run("wrong state cannot exchange or consume login", func(t *testing.T) {
		f := setup(t)
		q := f.start(t, "")
		status, _ := callback(t, q, url.Values{"state": {"attacker"}, "client_id": {"attacker"}, "code": {"evil"}})
		if status != 400 || !f.m.Snapshot().Pending || len(f.m.Snapshot().Accounts) != 0 {
			t.Fatal("untrusted state altered login")
		}
		f.p.mu.Lock()
		count := len(f.p.forms)
		f.p.mu.Unlock()
		if count != 0 {
			t.Fatal("untrusted callback reached token exchange")
		}
	})
	for _, test := range []struct {
		name     string
		response url.Values
	}{
		{"denied", url.Values{"error": {"access_denied"}, "error_description": {"secret-token-do-not-display"}}},
		{"missing issued id", url.Values{"code": {"code"}}},
		{"duplicate code", url.Values{"client_id": {"client-one"}, "code": {"a", "b"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := setup(t)
			q := f.start(t, "")
			status, body := callback(t, q, test.response)
			if status != 400 || len(f.m.Snapshot().Accounts) != 0 || strings.Contains(body, "secret-token") {
				t.Fatal("invalid callback activated account or exposed provider error")
			}
		})
	}
	t.Run("registration retained on expired code", func(t *testing.T) {
		f := setup(t)
		q := f.start(t, "")
		f.p.setToken(func(context.Context, url.Values) (*http.Response, error) {
			return jsonResponse(400, map[string]string{"error": "invalid_grant", "error_description": "secret-response"}), nil
		})
		status, body := callback(t, q, url.Values{"client_id": {"issued-id"}, "code": {"expired"}})
		s := f.m.Snapshot()
		if status != 400 || len(s.Accounts) != 1 || s.Accounts[0].ID != "issued-id" || s.Accounts[0].Connected || strings.Contains(body, "secret-response") {
			t.Fatal("registration was lost or failed exchange activated it")
		}
	})
	t.Run("returning registration cannot switch identity", func(t *testing.T) {
		f := setup(t)
		f.login(t, "client-one")
		q := f.start(t, "client-one")
		status, _ := callback(t, q, url.Values{"client_id": {"client-two"}, "code": {"code"}})
		if status != 400 || len(f.m.Snapshot().Accounts) != 1 {
			t.Fatal("callback replaced saved registration")
		}
	})
}

func TestIDTokenValidation(t *testing.T) {
	f := setup(t)
	for _, test := range []struct {
		name   string
		mutate func(jwt.MapClaims)
	}{
		{"issuer", func(c jwt.MapClaims) { c["iss"] = "https://attacker.test" }},
		{"audience", func(c jwt.MapClaims) { c["aud"] = "other-client" }},
		{"expired", func(c jwt.MapClaims) { c["exp"] = f.now.Load() - 60 }},
		{"missing expiration", func(c jwt.MapClaims) { delete(c, "exp") }},
		{"future issued at", func(c jwt.MapClaims) { c["iat"] = f.now.Load() + 300 }},
		{"nonce", func(c jwt.MapClaims) { c["nonce"] = "different" }},
		{"subject", func(c jwt.MapClaims) { c["sub"] = "" }},
		{"multiple audience requires azp", func(c jwt.MapClaims) { c["aud"] = []string{"client-one", "other"} }},
		{"wrong azp", func(c jwt.MapClaims) { c["azp"] = "other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			claims := f.claims("client-one", "nonce")
			test.mutate(claims)
			if _, err := f.m.verifyIdentity(context.Background(), f.p.sign(t, claims), "client-one", "nonce"); err == nil {
				t.Fatal("invalid OIDC token accepted")
			}
		})
	}
	claims := f.claims("client-one", "nonce")
	raw := f.p.sign(t, claims)
	parts := strings.Split(raw, ".")
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sig[0] ^= 1
	parts[2] = base64.RawURLEncoding.EncodeToString(sig)
	if _, err := f.m.verifyIdentity(context.Background(), strings.Join(parts, "."), "client-one", "nonce"); err == nil {
		t.Fatal("bad signature accepted")
	}
	hmac := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	hmac.Header["kid"] = "test-key"
	wrongAlgorithm, _ := hmac.SignedString([]byte("attacker"))
	if _, err := f.m.verifyIdentity(context.Background(), wrongAlgorithm, "client-one", "nonce"); err == nil {
		t.Fatal("HMAC algorithm confusion accepted")
	}
	unknown := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	unknown.Header["kid"] = "unknown"
	unknownRaw, _ := unknown.SignedString(f.p.key)
	if _, err := f.m.verifyIdentity(context.Background(), unknownRaw, "client-one", "nonce"); err == nil {
		t.Fatal("unknown signing key accepted")
	}
	previous := record{ClientID: "client-one", Issuer: issuer, Subject: "original-subject"}
	scope := requestedScopes
	if _, err := f.m.applyTokens(context.Background(), previous, tokenResponse{IDToken: raw, AccessToken: "access", RefreshToken: "refresh", TokenType: "Bearer", ExpiresIn: 3600, Scope: &scope}, "nonce", true); err == nil {
		t.Fatal("returning registration allowed a different subject")
	}
}

func TestUnverifiedIdentityCannotActivateRegistration(t *testing.T) {
	f := setup(t)
	q := f.start(t, "")
	// Even otherwise valid credentials must remain inactive when the ID token
	// belongs to another browser flow.
	raw := f.p.sign(t, f.claims("client-one", "another-flow"))
	f.p.setToken(func(context.Context, url.Values) (*http.Response, error) {
		return jsonResponse(200, map[string]any{"access_token": "untrusted-access", "refresh_token": "untrusted-refresh", "id_token": raw, "token_type": "Bearer", "expires_in": 3600, "scope": requestedScopes}), nil
	})
	status, _ := callback(t, q, url.Values{"client_id": {"client-one"}, "code": {"code"}})
	s := f.m.Snapshot()
	if status != 400 || len(s.Accounts) != 1 || s.Accounts[0].Connected || s.Accounts[0].PlanEnabled || f.record("client-one").RefreshToken != "" {
		t.Fatal("unverified OIDC identity activated returned credentials")
	}
}

func TestDiscoveryOriginAndRedirectProtection(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*metadata)
	}{
		{"issuer", func(d *metadata) { d.Issuer = "https://evil.test" }},
		{"token origin", func(d *metadata) { d.Token = "https://evil.test/token" }},
		{"authorize path", func(d *metadata) { d.Authorize = issuer + "/other" }},
		{"JWKS origin", func(d *metadata) { d.JWKS = "https://evil.test/jwks" }},
		{"revocation HTTP", func(d *metadata) { d.Revoke = "http://auth.openai.com/revoke" }},
		{"userinfo", func(d *metadata) { d.Revoke = "https://evil@auth.openai.com/revoke" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := setup(t)
			test.mutate(&f.p.discovery)
			if _, err := f.m.Start(context.Background(), ""); err == nil {
				t.Fatal("untrusted metadata accepted")
			}
		})
	}
	var requests atomic.Int32
	m, err := newManager(filepath.Join(t.TempDir(), "auth"), options{client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"https://evil.test/stolen"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err := m.Start(context.Background(), ""); err == nil || requests.Load() != 1 {
		t.Fatal("authentication redirect was followed")
	}
}

func TestPlanScopeComesOnlyFromTokenResponse(t *testing.T) {
	for _, identityOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "access without plan", true: "identity only"}[identityOnly], func(t *testing.T) {
			f := setup(t)
			q := f.start(t, "")
			body := map[string]any{"id_token": f.p.sign(t, f.claims("client-one", q.Get("nonce"))), "scope": "openid profile email"}
			if !identityOnly {
				body["access_token"], body["token_type"], body["expires_in"] = "identity-access", "Bearer", 3600
			}
			f.p.setToken(func(context.Context, url.Values) (*http.Response, error) { return jsonResponse(200, body), nil })
			status, _ := callback(t, q, url.Values{"client_id": {"client-one"}, "code": {"code"}, "scope": {requestedScopes}})
			s := f.m.Snapshot()
			if status != 200 || len(s.Accounts) != 1 || !s.Accounts[0].Connected || s.Accounts[0].PlanEnabled {
				t.Fatal("identity login and paid plan consent conflated")
			}
			_, _, token, err := f.m.Authorize(context.Background(), "client-one")
			if !errors.Is(err, ErrPlanDisabled) || token != "" {
				t.Fatal("plan-denied login authorized inference")
			}
			q = f.start(t, "client-one")
			if q.Get("prompt") != "consent" || q.Get("client_id") != "client-one" {
				t.Fatal("reauth cannot request missing consent")
			}
		})
	}
}

func TestRefreshRotationSerializedAndPersisted(t *testing.T) {
	f := setup(t)
	f.login(t, "client-one")
	f.now.Add(3700)
	if !f.m.Snapshot().Accounts[0].Connected {
		t.Fatal("expired access token hid renewable session")
	}
	var refreshes atomic.Int32
	f.p.setToken(func(_ context.Context, form url.Values) (*http.Response, error) {
		refreshes.Add(1)
		if form.Get("grant_type") != "refresh_token" || form.Get("client_id") != "client-one" || form.Get("refresh_token") != "refresh-client-one" || form.Get("resource") != resource || form.Has("scope") {
			t.Error("invalid refresh form")
		}
		time.Sleep(20 * time.Millisecond)
		return jsonResponse(200, map[string]any{"access_token": "rotated-access", "refresh_token": "rotated-refresh", "token_type": "Bearer", "expires_in": 3600}), nil
	})
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, cancel, token, err := f.m.Authorize(context.Background(), "client-one")
			if err != nil || token != "rotated-access" {
				t.Errorf("concurrent authorization failed: %v", err)
			}
			if cancel != nil {
				cancel()
			}
		}()
	}
	wg.Wait()
	if refreshes.Load() != 1 || !f.m.Snapshot().Accounts[0].PlanEnabled {
		t.Fatal("refresh was duplicated or omitted scope lost grant")
	}
	data, err := os.ReadFile(filepath.Join(f.dir, "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved diskState
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	r := saved.Accounts["client-one"]
	if r.AccessToken != "rotated-access" || r.RefreshToken != "rotated-refresh" || !r.RefreshExpiresAt.Equal(time.Unix(f.now.Load(), 0).Add(30*24*time.Hour)) {
		t.Fatal("rotating credentials were not persisted together")
	}
}

func TestRefreshRecovery(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   int
		code     string
		terminal bool
	}{
		{"expired", 400, "invalid_grant", true},
		{"reused", 400, "refresh_token_reused", true},
		{"invalid client", 400, "invalid_client", false},
		{"service failure", 503, "temporarily_unavailable", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := setup(t)
			f.login(t, "client-one")
			request, cancel, _, err := f.m.Authorize(context.Background(), "client-one")
			if err != nil {
				t.Fatal(err)
			}
			defer cancel()
			f.now.Add(3700)
			f.p.setToken(func(context.Context, url.Values) (*http.Response, error) {
				return jsonResponse(test.status, map[string]string{"error": test.code, "error_description": "secret-token"}), nil
			})
			_, _, token, err := f.m.Authorize(context.Background(), "client-one")
			if err == nil || token != "" || strings.Contains(err.Error(), "secret-token") {
				t.Fatal("refresh failure exposed token or error body")
			}
			r := f.record("client-one")
			if test.terminal {
				if r.RefreshToken != "" || f.m.Snapshot().Accounts[0].Connected {
					t.Fatal("terminal refresh left credentials active")
				}
				select {
				case <-request.Done():
				case <-time.After(time.Second):
					t.Fatal("terminal refresh did not stop requests")
				}
			} else if r.RefreshToken != "refresh-client-one" || !f.m.Snapshot().Accounts[0].Connected {
				t.Fatal("transient/configuration failure erased renewable session")
			}
		})
	}
}

func TestDisconnectCancelsRequestsRevokesAndRetainsRegistration(t *testing.T) {
	f := setup(t)
	f.login(t, "client-one")
	request, cancel, _, err := f.m.Authorize(context.Background(), "client-one")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	host := f.m.store.HostID
	revoking := make(chan struct{})
	release := make(chan struct{})
	f.p.revoke = func(_ context.Context, form url.Values) (*http.Response, error) {
		if form.Get("token") != "refresh-client-one" || form.Get("token_type_hint") != "refresh_token" || form.Get("client_id") != "client-one" {
			t.Error("wrong revocation token or registration")
		}
		close(revoking)
		<-release
		return jsonResponse(200, map[string]any{}), nil
	}
	done := make(chan error, 1)
	go func() { done <- f.m.Disconnect(context.Background(), "client-one") }()
	select {
	case <-revoking:
	case <-time.After(time.Second):
		t.Fatal("revocation not attempted")
	}
	select {
	case <-request.Done():
	case <-time.After(time.Second):
		t.Fatal("logout waited for network before canceling inference")
	}
	if f.m.Snapshot().Accounts[0].Connected {
		t.Fatal("disconnecting account remains usable")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	r := f.record("client-one")
	if r.ClientID != "client-one" || r.Subject == "" || r.AccessToken != "" || r.RefreshToken != "" || r.IDToken != "" || f.m.store.HostID != host {
		t.Fatal("logout did not clear credentials and preserve registration")
	}
	data, err := os.ReadFile(filepath.Join(f.dir, "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "refresh-client-one") || strings.Contains(string(data), "access-client-one") {
		t.Fatal("logout credentials remained on disk")
	}
	q := f.start(t, "client-one")
	if q.Get("client_id") != "client-one" || q.Get("ext_agent_host_id") != host {
		t.Fatal("logout lost reusable registration")
	}
}

func TestRevocationFailureStillClearsLocalCredentials(t *testing.T) {
	f := setup(t)
	f.login(t, "client-one")
	var attempts atomic.Int32
	f.p.revoke = func(context.Context, url.Values) (*http.Response, error) {
		attempts.Add(1)
		return jsonResponse(503, map[string]string{"error": "refresh-client-one"}), nil
	}
	err := f.m.Disconnect(context.Background(), "client-one")
	if !errors.Is(err, ErrRemoteRevocation) || attempts.Load() != 2 || f.record("client-one").RefreshToken != "" || f.m.Snapshot().Accounts[0].Connected || strings.Contains(err.Error(), "refresh-client-one") {
		t.Fatal("revocation retry/local-clear behavior incorrect")
	}
}

func TestMalformedRefreshSuccessCannotReusePreviousToken(t *testing.T) {
	for _, responseType := range []string{"missing rotated token", "malformed JSON", "oversized body", "body read failure"} {
		t.Run(responseType, func(t *testing.T) {
			f := setup(t)
			f.login(t, "client-one")
			f.now.Add(3700)
			f.p.setToken(func(context.Context, url.Values) (*http.Response, error) {
				if responseType == "malformed JSON" {
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{"))}, nil
				}
				if responseType == "oversized body" {
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(strings.Repeat("x", (1<<20)+1)))}, nil
				}
				if responseType == "body read failure" {
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(failingReader{})}, nil
				}
				return jsonResponse(200, map[string]any{"access_token": "new-access", "token_type": "Bearer", "expires_in": 3600}), nil
			})
			_, _, token, err := f.m.Authorize(context.Background(), "client-one")
			if err == nil || token != "" || f.record("client-one").RefreshToken != "" || f.m.Snapshot().Accounts[0].Connected {
				t.Fatal("unverifiable successful refresh left previous rotating token reusable")
			}
		})
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestLogoutDuringRefreshRevokesLatestReturnedToken(t *testing.T) {
	f := setup(t)
	f.login(t, "client-one")
	f.now.Add(3700)
	refreshing, released := make(chan struct{}), make(chan struct{})
	f.p.setToken(func(context.Context, url.Values) (*http.Response, error) {
		close(refreshing)
		<-released
		// Model a successful response already on its way when cancellation occurs.
		return jsonResponse(200, map[string]any{"access_token": "latest-access", "refresh_token": "latest-refresh", "token_type": "Bearer", "expires_in": 3600}), nil
	})
	var revoked atomic.Bool
	f.p.revoke = func(_ context.Context, form url.Values) (*http.Response, error) {
		if form.Get("token") != "latest-refresh" {
			t.Error("logout revoked superseded rotating token")
		}
		revoked.Store(true)
		return jsonResponse(200, map[string]any{}), nil
	}
	authorized := make(chan error, 1)
	go func() {
		_, cancel, token, err := f.m.Authorize(context.Background(), "client-one")
		if cancel != nil {
			cancel()
		}
		if token != "" {
			t.Error("logout allowed newly refreshed token to escape")
		}
		authorized <- err
	}()
	<-refreshing
	disconnected := make(chan struct{})
	f.m.SetOnChange(func() {
		if !f.m.Snapshot().Accounts[0].Connected {
			select {
			case <-disconnected:
			default:
				close(disconnected)
			}
		}
	})
	done := make(chan error, 1)
	go func() { done <- f.m.Disconnect(context.Background(), "client-one") }()
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("logout blocked on refreshing request")
	}
	close(released)
	if err := <-authorized; err == nil {
		t.Fatal("canceled request authorized")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !revoked.Load() || f.record("client-one").RefreshToken != "" {
		t.Fatal("latest refresh token was not revoked and cleared")
	}
}

func TestRefreshRemovingPlanCancelsExistingRequests(t *testing.T) {
	f := setup(t)
	f.login(t, "client-one")
	active, cancel, _, err := f.m.Authorize(context.Background(), "client-one")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	f.now.Add(3700)
	f.p.setToken(func(context.Context, url.Values) (*http.Response, error) {
		return jsonResponse(200, map[string]any{"access_token": "identity-access", "refresh_token": "identity-refresh", "token_type": "Bearer", "expires_in": 3600, "scope": "openid email offline_access"}), nil
	})
	_, _, token, err := f.m.Authorize(context.Background(), "client-one")
	if !errors.Is(err, ErrPlanDisabled) || token != "" {
		t.Fatal("reduced scope allowed inference")
	}
	select {
	case <-active.Done():
	case <-time.After(time.Second):
		t.Fatal("reduced scope left old requests active")
	}
	s := f.m.Snapshot().Accounts[0]
	if !s.Connected || s.PlanEnabled {
		t.Fatal("reduced scope should preserve identity but disable plan")
	}
}

func TestCloseCancelsBrowserAndActiveRequests(t *testing.T) {
	f := setup(t)
	f.login(t, "client-one")
	active, cancel, _, err := f.m.Authorize(context.Background(), "client-one")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	f.start(t, "client-one")
	if err := f.m.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-active.Done():
	case <-time.After(time.Second):
		t.Fatal("Close left active request")
	}
	if f.m.Snapshot().Pending || f.m.Snapshot().Accounts[0].Connected {
		t.Fatal("Close left active login state")
	}
	if _, err := f.m.Start(context.Background(), ""); !errors.Is(err, ErrClosed) {
		t.Fatal("Start after Close succeeded")
	}
	if _, _, _, err := f.m.Authorize(context.Background(), "client-one"); !errors.Is(err, ErrClosed) {
		t.Fatal("Authorize after Close succeeded")
	}
}
