package chatgptauth

import (
	"context"
	"errors"
	"time"
)

const PlanUsageLimitCode = "subscription_sharing_usage_limit_exceeded"

var (
	ErrPlanBlocked    error = planBlockedError{}
	ErrRecheckRunning       = errors.New("이 ChatGPT 연결의 한도를 이미 재확인하고 있습니다.")
)

type recheckKey struct{}
type recheckPermit struct {
	manager    *Manager
	id         string
	session    context.Context
	generation uint64
	claimed    bool // guarded by manager.mu; authorizes exactly one inference request
}

// BlockPlan affects this registration only. It neither revokes credentials nor
// cancels requests already admitted. Unknown errors must not latch this gate.
func (m *Manager) BlockPlan(id, code string) error {
	if code != PlanUsageLimitCode {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	r, ok := m.store.Accounts[id]
	a := m.accounts[id]
	if !ok || a == nil {
		m.mu.Unlock()
		return ErrLoginRequired
	}
	a.blockGeneration++
	r.PlanBlockReason, r.PlanBlockedAt = code, m.now()
	m.store.Accounts[id] = r
	err := m.saveLocked()
	if err != nil {
		m.lastError = errStorage.Error()
	}
	m.mu.Unlock()
	m.changed()
	return err
}

func (m *Manager) admitPlanLocked(ctx context.Context, id string, a *account) (bool, error) {
	if p, ok := ctx.Value(recheckKey{}).(*recheckPermit); ok && p.manager == m && p.id == id {
		if p.claimed || !a.rechecking || p.session != a.ctx || p.generation != a.blockGeneration {
			return false, ErrPlanBlocked
		}
		p.claimed = true
		return true, nil
	}
	if m.store.Accounts[id].PlanBlockReason != "" {
		return false, ErrPlanBlocked
	}
	return false, nil
}

// Recheck grants one inference authorization to an explicit user-requested
// probe. Normal callers remain blocked until that probe succeeds and the cleared
// state is persisted. A concurrent, newer limit response wins over probe success.
func (m *Manager) Recheck(ctx context.Context, id string, probe func(context.Context) error) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	r, ok := m.store.Accounts[id]
	a := m.accounts[id]
	if !ok || a == nil || !m.connectedLocked(id, r) {
		m.mu.Unlock()
		return ErrLoginRequired
	}
	if !hasScope(r.Scopes, planScope) {
		m.mu.Unlock()
		return ErrPlanDisabled
	}
	if a.rechecking {
		m.mu.Unlock()
		return ErrRecheckRunning
	}
	if probe == nil {
		m.mu.Unlock()
		return errors.New("한도 재확인 요청이 필요합니다.")
	}
	a.rechecking = true
	p := &recheckPermit{manager: m, id: id, session: a.ctx, generation: a.blockGeneration}
	m.wg.Add(1)
	m.mu.Unlock()
	m.changed()
	defer m.wg.Done()
	err := probe(context.WithValue(ctx, recheckKey{}, p))
	m.mu.Lock()
	a.rechecking = false
	if err == nil {
		switch {
		case m.closed:
			err = ErrClosed
		case ctx.Err() != nil:
			err = ctx.Err()
		case !p.claimed || a.ctx != p.session || !m.connectedLocked(id, m.store.Accounts[id]):
			err = ErrLoginRequired
		case a.blockGeneration != p.generation:
			err = ErrPlanBlocked
		default:
			before := m.store.Accounts[id]
			next := before
			next.PlanBlockReason, next.PlanBlockedAt = "", time.Time{}
			m.store.Accounts[id] = next
			if saveErr := m.saveLocked(); saveErr != nil {
				m.store.Accounts[id] = before
				m.lastError, err = errStorage.Error(), saveErr
			}
		}
	}
	m.mu.Unlock()
	m.changed()
	return err
}

type planBlockedError struct{}

func (planBlockedError) Error() string {
	return "이 ChatGPT 연결의 사용 한도에 도달했습니다. Usage 설정을 확인한 뒤 한도 재확인을 실행해 주세요."
}
func (planBlockedError) ChatGPTPlanBlocked() bool { return true }
