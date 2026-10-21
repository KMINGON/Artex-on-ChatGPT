package chatgptauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	issuer          = "https://auth.openai.com"
	discoveryURL    = issuer + "/.well-known/openid-configuration"
	resource        = "https://api.openai.com/v1"
	callbackPath    = "/auth/callback"
	requestedScopes = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
)

type metadata struct {
	Issuer    string `json:"issuer"`
	Authorize string `json:"authorization_endpoint"`
	Token     string `json:"token_endpoint"`
	JWKS      string `json:"jwks_uri"`
	Revoke    string `json:"revocation_endpoint"`
}

func authURLAllowed(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "auth.openai.com" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && strings.HasPrefix(u.Path, "/")
}

func (m *Manager) discover(ctx context.Context) (metadata, error) {
	m.metadataMu.Lock()
	defer m.metadataMu.Unlock()
	if m.metadata != nil {
		return *m.metadata, nil
	}
	var d metadata
	if err := m.getJSON(ctx, discoveryURL, &d); err != nil {
		return d, err
	}
	if d.Issuer != issuer || d.Authorize != issuer+"/api/accounts/authorize" || d.Token != issuer+"/api/accounts/oauth/token" ||
		!authURLAllowed(d.JWKS) || !authURLAllowed(d.Revoke) {
		return metadata{}, errLogin
	}
	m.metadata = &d
	return d, nil
}

func (m *Manager) getJSON(ctx context.Context, endpoint string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errNetwork
	}
	req.Header.Set("Accept", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return errNetwork
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errNetwork
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 || json.Unmarshal(data, dst) != nil {
		return errLogin
	}
	return nil
}

type pendingLogin struct {
	mu          sync.Mutex
	used        bool
	accountID   string
	state       string
	nonce       string
	verifier    string
	redirectURI string
	ctx         context.Context
	cancel      context.CancelFunc
}

func randomString() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", errLogin
	}
	return base64.RawURLEncoding.EncodeToString(data[:]), nil
}

// Start uses ctx only for setup. The browser flow outlives the initiating HTTP
// request and expires independently, or is canceled by a newer Start or Close.
func (m *Manager) Start(ctx context.Context, accountID string) (LoginStart, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return LoginStart{}, ErrClosed
	}
	m.wg.Add(1)
	defer m.wg.Done()
	r, exists := m.store.Accounts[accountID]
	if accountID != "" && (!exists || m.accounts[accountID].disconnecting) {
		m.mu.Unlock()
		return LoginStart{}, ErrLoginRequired
	}
	m.mu.Unlock()
	d, err := m.discover(ctx)
	if err != nil {
		return LoginStart{}, err
	}
	p := &pendingLogin{accountID: accountID}
	if p.state, err = randomString(); err != nil {
		return LoginStart{}, err
	}
	if p.nonce, err = randomString(); err != nil {
		return LoginStart{}, err
	}
	if p.verifier, err = randomString(); err != nil {
		return LoginStart{}, err
	}
	// Only the private-listener mode owns a server. Shared callbacks are routed
	// by the application's server, whose lifetime must not follow a login flow.
	p.redirectURI = m.callbackURL
	var ln net.Listener
	var srv *http.Server
	if p.redirectURI == "" {
		ln, err = net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return LoginStart{}, errors.New("ChatGPT 로그인용 로컬 포트를 열 수 없습니다.")
		}
		p.redirectURI = "http://" + ln.Addr().String() + callbackPath
		srv = &http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
		srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { m.callback(p, w, req) })
	}
	p.ctx, p.cancel = context.WithTimeout(m.ctx, m.loginTTL)
	m.mu.Lock()
	if m.closed || (accountID != "" && m.accounts[accountID].disconnecting) {
		m.mu.Unlock()
		p.cancel()
		if ln != nil {
			_ = ln.Close()
		}
		return LoginStart{}, ErrClosed
	}
	if m.pending != nil {
		m.pending.cancel()
	}
	if accountID != "" {
		r = m.store.Accounts[accountID]
	}
	m.pending, m.lastError = p, ""
	hostID := m.store.HostID
	if srv != nil {
		m.wg.Add(1)
		go func() { defer m.wg.Done(); _ = srv.Serve(ln) }()
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		<-p.ctx.Done()
		if srv != nil {
			shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdown)
			_ = srv.Close()
		}
		m.mu.Lock()
		notify := m.pending == p
		if notify {
			m.pending = nil
			if !m.closed {
				m.lastError = "ChatGPT 로그인 시간이 만료되었거나 취소되었습니다. 다시 연결해 주세요."
			}
		}
		m.mu.Unlock()
		if notify {
			m.changed()
		}
	}()
	m.mu.Unlock()

	challenge := sha256.Sum256([]byte(p.verifier))
	clientID := accountID
	if clientID == "" {
		clientID = "dynamic_agent_client"
	}
	query := url.Values{"client_id": {clientID}, "ext_agent_host_id": {hostID}, "redirect_uri": {p.redirectURI}, "response_type": {"code"}, "scope": {requestedScopes}, "resource": {resource}, "state": {p.state}, "nonce": {p.nonce}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}}
	if accountID == "" {
		query.Set("agent_name_hint", "Artex-on-ChatGPT")
	} else {
		if r.IDToken != "" {
			query.Set("id_token_hint", r.IDToken)
		}
		if r.Email != "" {
			query.Set("login_hint", r.Email)
		}
		if !hasScope(r.Scopes, planScope) {
			query.Set("prompt", "consent")
		}
	}
	m.changed()
	return LoginStart{URL: d.Authorize + "?" + query.Encode(), ExpiresAt: m.now().Add(m.loginTTL)}, nil
}

// validateCallbackURL deliberately accepts one canonical form. Besides avoiding
// arbitrary redirect origins, this keeps the exact Host comparison unambiguous.
func validateCallbackURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err == nil {
		port, portErr := strconv.ParseUint(u.Port(), 10, 16)
		if portErr == nil && port > 0 && raw == "http://127.0.0.1:"+strconv.FormatUint(port, 10)+callbackPath {
			return nil
		}
	}
	return errors.New("ChatGPT 콜백 주소는 http://127.0.0.1:<포트>/auth/callback 형식이어야 합니다.")
}

// ServeCallback handles the current shared-server login without an ARTEX JWT.
// OAuth state, nonce, PKCE and the configured Host provide the callback checks.
// The caller owns its HTTP server and must avoid logging callback query strings.
func (m *Manager) ServeCallback(w http.ResponseWriter, req *http.Request) {
	callbackHeaders(w)
	m.mu.Lock()
	p, closed, shared := m.pending, m.closed, m.callbackURL != ""
	m.mu.Unlock()
	if closed {
		http.Error(w, "연결 관리자가 종료되었습니다.", http.StatusGone)
		return
	}
	if !shared || p == nil {
		http.Error(w, "진행 중인 ChatGPT 로그인이 없습니다.", http.StatusNotFound)
		return
	}
	m.callback(p, w, req)
}

func callbackHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
}

func (m *Manager) callback(p *pendingLogin, w http.ResponseWriter, req *http.Request) {
	callbackHeaders(w)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		http.Error(w, "연결 관리자가 종료되었습니다.", http.StatusGone)
		return
	}
	m.wg.Add(1)
	m.mu.Unlock()
	defer m.wg.Done()
	u, _ := url.Parse(p.redirectURI)
	if req.Method != http.MethodGet || req.URL.Path != callbackPath || req.Host != u.Host || len(req.URL.RawQuery) > 16<<10 {
		http.Error(w, "잘못된 로그인 응답입니다.", http.StatusBadRequest)
		return
	}
	q, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil || len(q["state"]) != 1 || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(p.state)) != 1 {
		http.Error(w, "로그인 상태를 확인할 수 없습니다.", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	if p.used || p.ctx.Err() != nil {
		p.mu.Unlock()
		http.Error(w, "이미 처리되었거나 만료된 로그인입니다.", http.StatusBadRequest)
		return
	}
	p.used = true
	p.mu.Unlock()
	err = m.completeLogin(p, q)
	m.mu.Lock()
	current := m.pending == p
	if current {
		m.pending = nil
		if err != nil {
			m.lastError = err.Error()
		} else {
			m.lastError = ""
		}
	}
	m.mu.Unlock()
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, err.Error())
	} else {
		_, _ = io.WriteString(w, "ChatGPT 연결을 확인했습니다. ARTEX 화면으로 돌아가 주세요.")
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	p.cancel()
	if current {
		m.changed()
	}
}

func validClientID(id string) bool {
	if id == "" || id == "dynamic_agent_client" || len(id) > 256 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func (m *Manager) completeLogin(p *pendingLogin, q url.Values) error {
	if len(q["error"]) > 1 || len(q["code"]) > 1 || len(q["client_id"]) > 1 {
		return errLogin
	}
	if q.Get("error") != "" {
		return errors.New("ChatGPT 연결이 승인되지 않았습니다. 필요하면 다시 연결해 주세요.")
	}
	id := q.Get("client_id")
	if p.accountID != "" {
		if id != "" && id != p.accountID {
			return errLogin
		}
		id = p.accountID
	}
	if !validClientID(id) || q.Get("code") == "" {
		return errLogin
	}
	m.mu.Lock()
	if m.closed || m.pending != p || p.ctx.Err() != nil {
		m.mu.Unlock()
		return errLogin
	}
	r, exists := m.store.Accounts[id]
	if !exists {
		// Retain the issued registration even if code exchange expires or fails.
		r = record{ClientID: id}
		m.store.Accounts[id] = r
		a := &account{}
		a.ctx, a.cancel = context.WithCancel(m.ctx)
		m.accounts[id] = a
		if err := m.saveLocked(); err != nil {
			m.mu.Unlock()
			return errStorage
		}
	}
	a := m.accounts[id]
	m.mu.Unlock()
	a.op.Lock()
	defer a.op.Unlock()
	m.mu.Lock()
	r = m.store.Accounts[id]
	if m.closed || m.pending != p || a.disconnecting {
		m.mu.Unlock()
		return errLogin
	}
	m.mu.Unlock()
	d, err := m.discover(p.ctx)
	if err != nil {
		return err
	}
	tokens, err := m.exchange(p.ctx, d.Token, url.Values{"grant_type": {"authorization_code"}, "client_id": {id}, "code": {q.Get("code")}, "code_verifier": {p.verifier}, "redirect_uri": {p.redirectURI}, "resource": {resource}})
	if err != nil {
		return safeOAuthError(err)
	}
	next, err := m.applyTokens(p.ctx, r, tokens, p.nonce, true)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.pending != p || p.ctx.Err() != nil || a.disconnecting {
		return errLogin
	}
	latest := m.store.Accounts[id]
	next.PlanBlockReason, next.PlanBlockedAt = latest.PlanBlockReason, latest.PlanBlockedAt
	m.store.Accounts[id] = next
	if err := m.saveLocked(); err != nil {
		m.store.Accounts[id] = latest
		return errStorage
	}
	a.cancel()
	a.ctx, a.cancel = context.WithCancel(m.ctx)
	return nil
}

type tokenResponse struct {
	AccessToken  string  `json:"access_token"`
	RefreshToken string  `json:"refresh_token"`
	IDToken      string  `json:"id_token"`
	TokenType    string  `json:"token_type"`
	ExpiresIn    int64   `json:"expires_in"`
	Scope        *string `json:"scope"`
}

type oauthError struct {
	code   string
	status int
}

func (e *oauthError) Error() string { return "OpenAI 인증 요청을 완료하지 못했습니다." }

func safeOAuthError(err error) error {
	var e *oauthError
	if errors.As(err, &e) && e.code == "invalid_client" {
		return errors.New("ChatGPT 앱 등록 정보를 확인해 주세요.")
	}
	if errors.As(err, &e) && e.code == "invalid_grant" {
		return ErrLoginRequired
	}
	return errNetwork
}

func isTerminalRefresh(err error) bool {
	var e *oauthError
	if !errors.As(err, &e) {
		return false
	}
	switch e.code {
	case "invalid_grant", "invalid_refresh_token", "token_expired", "refresh_token_expired", "refresh_token_invalidated", "refresh_token_reused":
		return true
	}
	return false
}

func (m *Manager) postForm(ctx context.Context, endpoint string, form url.Values) ([]byte, int, error) {
	if !authURLAllowed(endpoint) {
		return nil, 0, errLogin
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, errNetwork
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, 0, errNetwork
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, resp.StatusCode, errNetwork
	}
	return data, resp.StatusCode, nil
}

func (m *Manager) exchange(ctx context.Context, endpoint string, form url.Values) (tokenResponse, error) {
	var tokens tokenResponse
	data, status, err := m.postForm(ctx, endpoint, form)
	if err != nil {
		// A successful response may already have rotated the refresh token even
		// when its body was truncated or could not be read. Never reuse the old one.
		if status == http.StatusOK {
			return tokens, errLogin
		}
		return tokens, err
	}
	if status != http.StatusOK {
		var body struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &body)
		return tokens, &oauthError{code: body.Error, status: status}
	}
	if json.Unmarshal(data, &tokens) != nil {
		return tokens, errLogin
	}
	return tokens, nil
}

func (m *Manager) refresh(ctx context.Context, r record) (tokenResponse, error) {
	d, err := m.discover(ctx)
	if err != nil {
		return tokenResponse{}, err
	}
	return m.exchange(ctx, d.Token, url.Values{"grant_type": {"refresh_token"}, "client_id": {r.ClientID}, "refresh_token": {r.RefreshToken}, "resource": {resource}})
}

func (m *Manager) revoke(ctx context.Context, r record) error {
	if r.RefreshToken == "" {
		return nil
	}
	d, err := m.discover(ctx)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		_, status, err := m.postForm(ctx, d.Revoke, url.Values{"token": {r.RefreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {r.ClientID}})
		if err == nil && status == http.StatusOK {
			return nil
		}
		if attempt == 1 || (err == nil && status < 500) {
			return ErrRemoteRevocation
		}
		select {
		case <-ctx.Done():
			return ErrRemoteRevocation
		case <-time.After(200 * time.Millisecond):
		}
	}
	return ErrRemoteRevocation
}

type identityClaims struct {
	jwt.RegisteredClaims
	Nonce           string `json:"nonce"`
	Email           string `json:"email"`
	AuthorizedParty string `json:"azp"`
}

func (m *Manager) verifyIdentity(ctx context.Context, raw, clientID, nonce string) (*identityClaims, error) {
	d, err := m.discover(ctx)
	if err != nil {
		return nil, err
	}
	var keys struct {
		Keys []struct{ Kty, Kid, Use, Alg, N, E string } `json:"keys"`
	}
	if err := m.getJSON(ctx, d.JWKS, &keys); err != nil {
		return nil, err
	}
	claims := &identityClaims{}
	parsed, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		kid, ok := t.Header["kid"].(string)
		if !ok || kid == "" {
			return nil, errLogin
		}
		for _, key := range keys.Keys {
			if key.Kid != kid || key.Kty != "RSA" || (key.Use != "" && key.Use != "sig") || (key.Alg != "" && key.Alg != "RS256") {
				continue
			}
			n, e1 := base64.RawURLEncoding.DecodeString(key.N)
			e, e2 := base64.RawURLEncoding.DecodeString(key.E)
			if e1 != nil || e2 != nil || len(n) < 256 || len(n) > 1024 || len(e) == 0 || len(e) > 4 {
				return nil, errLogin
			}
			exponent := new(big.Int).SetBytes(e).Int64()
			if exponent < 3 || exponent > 2147483647 || exponent%2 == 0 {
				return nil, errLogin
			}
			return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exponent)}, nil
		}
		return nil, errLogin
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(issuer), jwt.WithAudience(clientID), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithLeeway(30*time.Second), jwt.WithTimeFunc(m.now))
	if err != nil || !parsed.Valid || claims.Subject == "" || len(claims.Subject) > 1024 || len(claims.Email) > 1024 {
		return nil, errLogin
	}
	if nonce != "" && subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(nonce)) != 1 {
		return nil, errLogin
	}
	if (len(claims.Audience) > 1 || claims.AuthorizedParty != "") && claims.AuthorizedParty != clientID {
		return nil, errLogin
	}
	return claims, nil
}

func (m *Manager) applyTokens(ctx context.Context, previous record, tokens tokenResponse, nonce string, login bool) (record, error) {
	next := previous
	if login && (tokens.IDToken == "" || tokens.Scope == nil) {
		return record{}, errLogin
	}
	if tokens.IDToken != "" {
		identity, err := m.verifyIdentity(ctx, tokens.IDToken, previous.ClientID, nonce)
		if err != nil {
			return record{}, err
		}
		if previous.Subject != "" && (previous.Subject != identity.Subject || previous.Issuer != identity.Issuer) {
			return record{}, errLogin
		}
		next.Subject, next.Issuer, next.Email, next.IDToken = identity.Subject, identity.Issuer, identity.Email, tokens.IDToken
		if login {
			next.ExpiresAt = identity.ExpiresAt.Time
		}
	}
	if tokens.Scope != nil {
		next.Scopes = strings.Fields(*tokens.Scope)
	}
	if login && tokens.AccessToken == "" && tokens.RefreshToken == "" && !hasScope(next.Scopes, planScope) {
		next.AccessToken, next.RefreshToken = "", ""
		next.RefreshExpiresAt = time.Time{}
		return next, nil
	}
	if tokens.AccessToken == "" || !strings.EqualFold(tokens.TokenType, "Bearer") || tokens.ExpiresIn <= 0 || tokens.ExpiresIn > 86400 {
		return record{}, errLogin
	}
	if tokens.RefreshToken == "" && (!login || hasScope(next.Scopes, "offline_access")) {
		return record{}, errLogin
	}
	next.AccessToken, next.RefreshToken = tokens.AccessToken, tokens.RefreshToken
	next.ExpiresAt = m.now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	next.RefreshExpiresAt = time.Time{}
	if tokens.RefreshToken != "" {
		next.RefreshExpiresAt = m.now().Add(30 * 24 * time.Hour)
	}
	return next, nil
}
