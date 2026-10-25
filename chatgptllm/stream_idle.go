package chatgptllm

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"
)

const DefaultStreamIdleTimeout = 15 * time.Minute

// The timer runs only while awaiting response headers or a blocking body Read.
// Bytes (including SSE comments/keepalives) reset the budget. Time spent in yield
// executing a tool or consuming output is not network inactivity.
type streamIdleGuard struct {
	mu         sync.Mutex
	limit      time.Duration
	cancel     context.CancelFunc
	timer      *time.Timer
	generation uint64
	expired    bool
}

func (g *streamIdleGuard) begin() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.generation++
	version := g.generation
	if g.limit <= 0 || g.expired {
		return
	}
	g.timer = time.AfterFunc(g.limit, func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.generation != version {
			return
		}
		g.expired = true
		g.cancel() // cancels the actual HTTP request, releasing its blocking read
	})
}

func (g *streamIdleGuard) end() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.generation++
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
	return g.expired
}

func streamIdleError() *Error {
	return failure("stream_idle_timeout", "모델 연결에서 제한 시간 동안 데이터가 수신되지 않아 요청을 종료했습니다. 기존 도구 실행은 자동으로 반복하지 않습니다", context.DeadlineExceeded)
}

type idleResponseBody struct {
	io.ReadCloser
	guard   *streamIdleGuard
	onBytes func()
}

func (b *idleResponseBody) Read(p []byte) (int, error) {
	b.guard.begin()
	n, err := b.ReadCloser.Read(p)
	expired := b.guard.end()
	if expired {
		return 0, streamIdleError()
	}
	if n > 0 && b.onBytes != nil {
		b.onBytes()
	}
	return n, err
}

func (b *idleResponseBody) Close() error {
	b.guard.end()
	return b.ReadCloser.Close()
}

func (p *provider) openMonitored(ctx context.Context, body []byte) (*http.Response, context.CancelFunc, *streamIdleGuard, error) {
	callID := streamCallID.Add(1)
	attemptCtx, cancel := context.WithCancel(ctx)
	guard := &streamIdleGuard{limit: p.cfg.StreamIdleTimeout, cancel: cancel}
	observeStreamProgress(ctx, callID, "waiting")
	guard.begin()
	resp, release, err := p.open(attemptCtx, body)
	if guard.end() {
		err = streamIdleError()
	}
	var once sync.Once
	finish := func() {
		once.Do(func() {
			guard.end()
			cancel()
			if release != nil {
				release()
			}
			observeStreamProgress(ctx, callID, "finished")
		})
	}
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		finish()
		return resp, func() {}, guard, err
	}
	resp.Body = &idleResponseBody{ReadCloser: resp.Body, guard: guard, onBytes: func() { observeStreamProgress(ctx, callID, "receiving") }}
	return resp, finish, guard, nil
}
