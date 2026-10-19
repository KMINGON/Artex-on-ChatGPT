// Package chatgptauth manages local Sign in with ChatGPT registrations.
// OAuth credentials remain in the local runtime; State never exposes them.
package chatgptauth

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

var (
	ErrClosed           = errors.New("ChatGPT 연결 관리자가 종료되었습니다.")
	ErrLoginRequired    = errors.New("ChatGPT 계정을 다시 연결해 주세요.")
	ErrPlanDisabled     = errors.New("이 연결은 ChatGPT 구독 사용 권한이 없습니다. 구독 사용에 동의한 뒤 다시 연결해 주세요.")
	ErrRemoteRevocation = errors.New("로컬 연결은 해제했지만 원격 세션 해제를 확인하지 못했습니다. ChatGPT 설정에서 앱 연결을 확인해 주세요.")
	errStorage          = errors.New("ChatGPT 자격증명을 안전하게 저장하거나 읽을 수 없습니다.")
	errLogin            = errors.New("ChatGPT 로그인 응답을 검증하지 못했습니다. 다시 연결해 주세요.")
	errNetwork          = errors.New("OpenAI 인증 서비스에 연결하지 못했습니다. 잠시 후 다시 시도해 주세요.")
)

const planScope = "chatgpt.tokens.use.direct"

type AccountState struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	Connected   bool   `json:"connected"`
	PlanEnabled bool   `json:"plan_enabled"`
	ExpiresAt   string `json:"expires_at"`
}

type State struct {
	Accounts []AccountState `json:"accounts"`
	Pending  bool           `json:"pending"`
	Error    string         `json:"error,omitempty"`
}

type LoginStart struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type account struct {
	op            sync.Mutex // Serializes refresh and revocation for this renewable session.
	ctx           context.Context
	cancel        context.CancelFunc
	disconnecting bool
}

// Options are deliberately private: production callers cannot redirect credentials.
type options struct {
	client   *http.Client
	now      func() time.Time
	loginTTL time.Duration
}

type Manager struct {
	mu         sync.Mutex
	dir        string
	store      diskState
	accounts   map[string]*account
	lock       *os.File
	ctx        context.Context
	cancel     context.CancelFunc
	closed     bool
	pending    *pendingLogin
	lastError  string
	onChange   func()
	client     *http.Client
	now        func() time.Time
	loginTTL   time.Duration
	metadataMu sync.Mutex
	metadata   *metadata
	wg         sync.WaitGroup
}

func New(dir string) (*Manager, error) { return newManager(dir, options{}) }

func newManager(dir string, opts options) (*Manager, error) {
	if opts.now == nil {
		opts.now = time.Now
	}
	if opts.loginTTL == 0 {
		opts.loginTTL = 10 * time.Minute
	}
	if opts.client == nil {
		opts.client = &http.Client{Timeout: 20 * time.Second}
	}
	client := *opts.client
	// Never follow redirects while carrying a code, token, or login hint.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if client.Timeout == 0 {
		client.Timeout = 20 * time.Second
	}
	state, lock, err := openStore(dir)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{dir: dir, store: state, lock: lock, accounts: make(map[string]*account), ctx: ctx, cancel: cancel, client: &client, now: opts.now, loginTTL: opts.loginTTL}
	for id := range state.Accounts {
		a := &account{}
		a.ctx, a.cancel = context.WithCancel(ctx)
		m.accounts[id] = a
	}
	return m, nil
}

// SetOnChange installs a callback that may safely call Snapshot.
func (m *Manager) SetOnChange(fn func()) { m.mu.Lock(); m.onChange = fn; m.mu.Unlock() }

func (m *Manager) changed() {
	m.mu.Lock()
	fn := m.onChange
	m.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (m *Manager) Snapshot() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := State{Accounts: []AccountState{}, Pending: m.pending != nil, Error: m.lastError}
	for id, r := range m.store.Accounts {
		connected := m.connectedLocked(id, r)
		expiry := ""
		if !r.ExpiresAt.IsZero() {
			expiry = r.ExpiresAt.UTC().Format(time.RFC3339)
		}
		s.Accounts = append(s.Accounts, AccountState{ID: id, Email: r.Email, Connected: connected, PlanEnabled: connected && hasScope(r.Scopes, planScope), ExpiresAt: expiry})
	}
	sort.Slice(s.Accounts, func(i, j int) bool { return s.Accounts[i].ID < s.Accounts[j].ID })
	return s
}

func (m *Manager) connectedLocked(id string, r record) bool {
	a := m.accounts[id]
	return !m.closed && a != nil && !a.disconnecting && a.ctx.Err() == nil && r.Subject != "" &&
		(((r.AccessToken != "" || r.IDToken != "") && r.ExpiresAt.After(m.now())) || (r.RefreshToken != "" && r.RefreshExpiresAt.After(m.now())))
}

// Authorize returns a request context canceled when this account is disconnected.
// Its cancel function must be called after the request (including its stream) ends.
func (m *Manager) Authorize(ctx context.Context, id string) (context.Context, context.CancelFunc, string, error) {
	m.mu.Lock()
	r, ok := m.store.Accounts[id]
	if m.closed {
		m.mu.Unlock()
		return nil, nil, "", ErrClosed
	}
	if !ok || !m.connectedLocked(id, r) {
		m.mu.Unlock()
		return nil, nil, "", ErrLoginRequired
	}
	if !hasScope(r.Scopes, planScope) {
		m.mu.Unlock()
		return nil, nil, "", ErrPlanDisabled
	}
	a := m.accounts[id]
	m.wg.Add(1)
	defer m.wg.Done()
	session := a.ctx
	reqCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(session, cancel)
	cleanup := func() { stop(); cancel() }
	m.mu.Unlock()

	a.op.Lock()
	token, changed, err := m.authorizeLocked(reqCtx, id, a, session)
	a.op.Unlock()
	if changed {
		m.changed()
	}
	if err != nil {
		cleanup()
		return nil, nil, "", err
	}
	if err = reqCtx.Err(); err != nil {
		cleanup()
		return nil, nil, "", err
	}
	return reqCtx, cleanup, token, nil
}

func (m *Manager) authorizeLocked(ctx context.Context, id string, a *account, session context.Context) (string, bool, error) {
	m.mu.Lock()
	r := m.store.Accounts[id]
	if m.closed || a.ctx != session || !m.connectedLocked(id, r) {
		m.mu.Unlock()
		return "", false, ErrLoginRequired
	}
	if !hasScope(r.Scopes, planScope) {
		m.mu.Unlock()
		return "", false, ErrPlanDisabled
	}
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return "", false, err
	}
	if r.AccessToken != "" && r.ExpiresAt.After(m.now().Add(time.Minute)) {
		m.mu.Unlock()
		return r.AccessToken, false, nil
	}
	if r.RefreshToken == "" || !r.RefreshExpiresAt.After(m.now()) {
		// A short-lived, non-renewable token remains usable until its actual expiry.
		if r.AccessToken != "" && r.ExpiresAt.After(m.now()) {
			m.mu.Unlock()
			return r.AccessToken, false, nil
		}
		m.mu.Unlock()
		return "", false, ErrLoginRequired
	}
	m.mu.Unlock()

	tokens, err := m.refresh(ctx, r)
	if err != nil {
		if isTerminalRefresh(err) || errors.Is(err, errLogin) {
			m.mu.Lock()
			if a.ctx == session {
				m.clearLocked(id)
				m.lastError = ErrLoginRequired.Error()
				_ = m.saveLocked()
			}
			m.mu.Unlock()
			return "", true, ErrLoginRequired
		}
		return "", false, safeOAuthError(err)
	}
	next, err := m.applyTokens(ctx, r, tokens, "", false)
	if err != nil {
		// Refresh may already have rotated the token. Never retry the old one after
		// a malformed or unverifiable successful token response.
		m.mu.Lock()
		if a.ctx == session {
			m.clearLocked(id)
			m.lastError = errLogin.Error()
			_ = m.saveLocked()
		}
		m.mu.Unlock()
		return "", true, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if a.ctx != session || m.closed {
		return "", false, ErrLoginRequired
	}
	// Save the latest rotating refresh token even if disconnect began meanwhile;
	// Disconnect waits on a.op and revokes this latest token before clearing it.
	m.store.Accounts[id] = next
	if err := m.saveLocked(); err != nil {
		m.clearLocked(id)
		m.lastError = errStorage.Error()
		return "", true, errStorage
	}
	if a.disconnecting || session.Err() != nil {
		return "", true, ErrLoginRequired
	}
	if !hasScope(next.Scopes, planScope) {
		a.cancel()
		a.ctx, a.cancel = context.WithCancel(m.ctx)
		return "", true, ErrPlanDisabled
	}
	m.lastError = ""
	return next.AccessToken, true, nil
}

// Disconnect stops in-flight inference immediately, then attempts remote revocation.
// The registration and host ID are retained; credentials are cleared even on failure.
func (m *Manager) Disconnect(ctx context.Context, id string) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	a, ok := m.accounts[id]
	if !ok {
		m.mu.Unlock()
		return ErrLoginRequired
	}
	m.wg.Add(1)
	defer m.wg.Done()
	a.disconnecting = true
	a.cancel()
	if m.pending != nil && m.pending.accountID == id {
		m.pending.cancel()
		m.pending = nil
	}
	m.mu.Unlock()
	m.changed()
	a.op.Lock()
	m.mu.Lock()
	r := m.store.Accounts[id]
	m.mu.Unlock()
	err := m.revoke(ctx, r)
	m.mu.Lock()
	m.clearLocked(id)
	a.disconnecting = false
	if err != nil {
		m.lastError = ErrRemoteRevocation.Error()
	} else {
		m.lastError = ""
	}
	saveErr := m.saveLocked()
	if saveErr != nil {
		m.lastError = errStorage.Error()
	}
	m.mu.Unlock()
	a.op.Unlock()
	m.changed()
	if saveErr != nil {
		return errStorage
	}
	if err != nil {
		return ErrRemoteRevocation
	}
	return nil
}

func (m *Manager) clearLocked(id string) {
	r := m.store.Accounts[id]
	r.AccessToken, r.RefreshToken, r.IDToken = "", "", ""
	r.Scopes = nil
	r.ExpiresAt, r.RefreshExpiresAt = time.Time{}, time.Time{}
	m.store.Accounts[id] = r
	if a := m.accounts[id]; a != nil {
		a.cancel()
	}
}

func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.cancel()
	m.mu.Unlock()
	m.wg.Wait()
	// Wait for any refresh/revocation before releasing the process-wide file lock.
	for _, a := range m.accounts {
		a.op.Lock()
		a.op.Unlock()
	}
	return closeLock(m.lock)
}

func hasScope(scopes []string, want string) bool {
	for _, scope := range scopes {
		if scope == want {
			return true
		}
	}
	return false
}
