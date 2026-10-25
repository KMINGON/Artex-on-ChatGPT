package chatgptllm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
)

func idleTestProvider(t *testing.T, client *http.Client, api bool, timeout time.Duration) llm.Provider {
	t.Helper()
	cfg := Config{Model: "account-model", Authorize: auth, HTTPClient: client, StreamIdleTimeout: timeout, MaxRetries: 2, RetryInterval: time.Millisecond}
	if api {
		cfg.APIKey, cfg.Authorize = "test-secret", nil
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestIdleStreamCancelsHTTPReadWithoutReplay(t *testing.T) {
	for _, api := range []bool{false, true} {
		for _, headers := range []bool{false, true} {
			t.Run(fmt.Sprintf("api=%v/headers=%v", api, headers), func(t *testing.T) {
				var calls atomic.Int32
				canceled := make(chan struct{})
				client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					calls.Add(1)
					if headers {
						w.Header().Set("Content-Type", "text/event-stream")
						w.(http.Flusher).Flush()
					}
					<-r.Context().Done()
					close(canceled)
				})
				var progress []StreamProgress
				ctx := WithStreamProgressObserver(context.Background(), func(p StreamProgress) { progress = append(progress, p) })
				_, _, _, err := idleTestProvider(t, client, api, 50*time.Millisecond).Complete(ctx, llm.CompletionRequest{})
				var e *Error
				if !errors.As(err, &e) || e.Code != "stream_idle_timeout" || !e.NoReplay() || calls.Load() != 1 {
					t.Fatalf("err=%v calls=%d", err, calls.Load())
				}
				select {
				case <-canceled:
				case <-time.After(time.Second):
					t.Fatal("HTTP read not canceled")
				}
				if len(progress) < 2 || progress[0].Phase != "waiting" || progress[len(progress)-1].Phase != "finished" {
					t.Fatalf("progress=%+v", progress)
				}
			})
		}
	}
}

func TestStreamKeepaliveAndSlowConsumerDoNotConsumeIdleBudget(t *testing.T) {
	for _, api := range []bool{false, true} {
		t.Run(fmt.Sprint(api), func(t *testing.T) {
			client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for i := 0; i < 8; i++ {
					fmt.Fprint(w, ": keepalive\n\n")
					w.(http.Flusher).Flush()
					time.Sleep(20 * time.Millisecond)
				}
				completed(w, textItem("ok"))
			})
			p := idleTestProvider(t, client, api, 100*time.Millisecond)
			var err error
			for _, e := range p.Stream(context.Background(), llm.CompletionRequest{}) {
				if e != nil {
					err = e
					break
				}
				// A synchronous consumer may be executing a tool after terminal output.
				time.Sleep(110 * time.Millisecond)
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStreamCancellationAndRetryHaveIndependentProgressAndTimers(t *testing.T) {
	var requests atomic.Int32
	client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if requests.Add(1) == 1 {
			event(w, map[string]any{"type": "response.failed", "response": map[string]any{"error": map[string]any{"code": "server_error"}}})
			return
		}
		completed(w, textItem("ok"))
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var events []StreamProgress
	ctx = WithStreamProgressObserver(ctx, func(p StreamProgress) { mu.Lock(); events = append(events, p); mu.Unlock() })
	_, _, _, err := idleTestProvider(t, client, false, 50*time.Millisecond).Complete(ctx, llm.CompletionRequest{})
	if err != nil || requests.Load() != 2 {
		t.Fatalf("err=%v requests=%d", err, requests.Load())
	}
	mu.Lock()
	n := len(events)
	previous := append([]StreamProgress(nil), events...)
	mu.Unlock()
	time.Sleep(80 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if ctx.Err() != nil || len(events) != n {
		t.Fatal("timer/callback remained after completion")
	}
	active := map[uint64]bool{}
	for _, p := range previous {
		switch p.Phase {
		case "waiting":
			active[p.CallID] = true
		case "finished":
			delete(active, p.CallID)
		default:
			if !active[p.CallID] {
				t.Fatal("late callback")
			}
		}
	}
	if len(active) != 0 {
		t.Fatal("unfinished call")
	}
}

func TestParentCancellationWinsOverIdleTimeout(t *testing.T) {
	client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.Copy(io.Discard, r.Body)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _, _, err := idleTestProvider(t, client, false, time.Second).Complete(ctx, llm.CompletionRequest{})
	var e *Error
	if !errors.As(err, &e) || e.Code == "stream_idle_timeout" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("parent cancellation misclassified: %v", err)
	}
}
