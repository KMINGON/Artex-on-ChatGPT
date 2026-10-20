package llmpool

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/chatgptllm"
	"github.com/Autumn-27/norma/llm"
)

// fakeProv is a scripted provider: script[i] is what the i-th call yields —
// some events, then optionally an error.
type fakeProv struct {
	name   string
	calls  int
	events [][]llm.StreamEvent // events emitted before the error, per call
	errs   []error             // error to end each call with (nil = clean finish)
}

// at returns script entry n, repeating the last one once the script runs out, so
// a provider defined as "always succeeds" / "always 402" keeps behaving that way
// across repeated calls.
func at[T any](s []T, n int) (T, bool) {
	var zero T
	if len(s) == 0 {
		return zero, false
	}
	if n >= len(s) {
		n = len(s) - 1
	}
	return s[n], true
}

func (f *fakeProv) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	n := f.calls
	f.calls++
	return func(yield func(llm.StreamEvent, error) bool) {
		if evs, ok := at(f.events, n); ok {
			for _, e := range evs {
				if !yield(e, nil) {
					return
				}
			}
		}
		err, _ := at(f.errs, n)
		if err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		yield(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
	}
}

func (f *fakeProv) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	acc := llm.NewAccumulator()
	for ev, err := range f.Stream(ctx, req) {
		if err != nil {
			return llm.Message{}, "", llm.Usage{}, err
		}
		acc.Add(ev)
	}
	return acc.Message(), acc.StopReason, acc.Usage, nil
}

// ok builds a provider that always succeeds with one text delta.
func okProv(name, text string) *fakeProv {
	return &fakeProv{name: name, events: [][]llm.StreamEvent{{{Type: llm.SETextDelta, Text: text}}}}
}

// failProv builds a provider that fails immediately (before any event) with status.
func failProv(name string, status int) *fakeProv {
	return &fakeProv{name: name, errs: []error{fmt.Errorf("anthropic: status %d: nope", status)}}
}

func member(id int64, name string, rank int, p llm.Provider) *Member {
	return &Member{ID: id, Name: name, Model: "m" + name, Rank: rank, Prov: p}
}

// drain consumes a stream, returning the concatenated text and terminal error.
func drain(seq iter.Seq2[llm.StreamEvent, error]) (string, error) {
	var sb strings.Builder
	for ev, err := range seq {
		if err != nil {
			return sb.String(), err
		}
		if ev.Type == llm.SETextDelta {
			sb.WriteString(ev.Text)
		}
	}
	return sb.String(), nil
}

func TestFailoverOnNoCredit(t *testing.T) {
	a, b := failProv("a", 402), okProv("b", "hello")
	p := New([]*Member{member(1, "a", 10, a), member(2, "b", 5, b)}, NewRegistry(nil, nil))

	got, err := drain(p.Stream(context.Background(), llm.CompletionRequest{}))
	if err != nil {
		t.Fatalf("expected failover to succeed, got %v", err)
	}
	if got != "hello" {
		t.Fatalf("text = %q, want %q", got, "hello")
	}
	if a.calls != 1 || b.calls != 1 {
		t.Fatalf("calls: a=%d b=%d, want 1/1", a.calls, b.calls)
	}
}

// A 402 is deterministic: one failure must open the breaker, so the NEXT request
// skips that profile entirely instead of paying for another round-trip.
func TestHardFailureTripsBreakerImmediately(t *testing.T) {
	a, b := failProv("a", 402), okProv("b", "x")
	reg := NewRegistry(nil, nil)
	p := New([]*Member{member(1, "a", 10, a), member(2, "b", 5, b)}, reg)

	_, _ = drain(p.Stream(context.Background(), llm.CompletionRequest{}))
	if !reg.IsOpen(1) {
		t.Fatal("402 should have tripped the breaker on the first failure")
	}
	_, _ = drain(p.Stream(context.Background(), llm.CompletionRequest{}))
	if a.calls != 1 {
		t.Fatalf("tripped profile was called again: calls=%d, want 1", a.calls)
	}
	if b.calls != 2 {
		t.Fatalf("fallback calls=%d, want 2", b.calls)
	}
}

// 429 is transient: the SDK already retried, but we shouldn't write a profile off
// until it fails repeatedly.
func TestSoftFailureNeedsRepeats(t *testing.T) {
	reg := NewRegistry(nil, nil)
	for i := 1; i < softTripAfter; i++ {
		if reg.Trip(1, "429", false) {
			t.Fatalf("tripped after %d transient failures, want %d", i, softTripAfter)
		}
	}
	if !reg.Trip(1, "429", false) {
		t.Fatalf("should trip on failure #%d", softTripAfter)
	}
	if !reg.IsOpen(1) {
		t.Fatal("breaker should be open")
	}
}

// A success must fully clear the counters, so an intermittent profile never
// accumulates its way to a trip.
func TestPassResetsCounters(t *testing.T) {
	reg := NewRegistry(nil, nil)
	reg.Trip(1, "429", false)
	reg.Trip(1, "429", false)
	reg.Pass(1)
	if got := reg.Get(1).Fails; got != 0 {
		t.Fatalf("fails=%d after Pass, want 0", got)
	}
	if reg.Trip(1, "429", false) {
		t.Fatal("tripped immediately after a success — counters were not reset")
	}
}

func TestBackoffLadderGrows(t *testing.T) {
	reg := NewRegistry(nil, nil)
	var prev time.Duration
	for i := range 4 {
		reg.Reset(1)
		st := State{Trips: i}
		reg.Restore(1, st)
		reg.Trip(1, "402", true)
		d := time.Until(reg.Get(1).OpenUntil)
		// Non-decreasing, with a second of slack: the ladder plateaus at its last
		// rung, and each Trip stamps its own time.Now().
		if i > 0 && d < prev-time.Second {
			t.Fatalf("trip #%d cools for %v, shorter than the previous %v", i+1, d, prev)
		}
		prev = d
	}
	if prev < 25*time.Minute {
		t.Fatalf("ladder tops out at %v, want ~30m", prev)
	}
}

// The safety rule: once output has reached the caller, a mid-stream failure must
// NOT be retried on another model — that would duplicate the assistant turn.
func TestNoFailoverAfterEmit(t *testing.T) {
	a := &fakeProv{
		name:   "a",
		events: [][]llm.StreamEvent{{{Type: llm.SETextDelta, Text: "partial"}}},
		errs:   []error{fmt.Errorf("anthropic: status 500: mid-stream drop")},
	}
	b := okProv("b", "full")
	p := New([]*Member{member(1, "a", 10, a), member(2, "b", 5, b)}, NewRegistry(nil, nil))

	got, err := drain(p.Stream(context.Background(), llm.CompletionRequest{}))
	if err == nil {
		t.Fatal("mid-stream error should surface, not be swallowed by a failover")
	}
	if got != "partial" {
		t.Fatalf("text = %q, want the partial output %q", got, "partial")
	}
	if b.calls != 0 {
		t.Fatalf("fell over to the backup after emitting output (calls=%d) — would duplicate the turn", b.calls)
	}
}

// Cancelling a task must not burn a backup key, and must not be diagnosed as an
// LLM fault.
func TestNoFailoverOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &fakeProv{name: "a", errs: []error{context.Canceled}}
	b := okProv("b", "x")
	reg := NewRegistry(nil, nil)
	p := New([]*Member{member(1, "a", 10, a), member(2, "b", 5, b)}, reg)

	_, err := drain(p.Stream(ctx, llm.CompletionRequest{}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if b.calls != 0 {
		t.Fatalf("failed over on cancellation (backup calls=%d)", b.calls)
	}
	if reg.Get(1).Fails != 0 {
		t.Fatal("cancellation counted as a profile failure")
	}
}

func TestShouldFailoverByStatus(t *testing.T) {
	cases := []struct {
		status int
		want   bool
	}{
		{400, false}, // bad/over-long request fails identically everywhere
		{401, true}, {402, true}, {403, true}, {404, true},
		{408, true}, {429, true}, {500, true}, {503, true},
		{200, false},
	}
	for _, c := range cases {
		err := fmt.Errorf("openai: status %d: body", c.status)
		if got := shouldFailover(context.Background(), err); got != c.want {
			t.Errorf("status %d: shouldFailover=%v, want %v", c.status, got, c.want)
		}
	}
	// A transport error carries no status and must fail over.
	if !shouldFailover(context.Background(), errors.New("dial tcp: connection reset by peer")) {
		t.Error("network error should fail over")
	}
}

func TestHardVsSoftClassification(t *testing.T) {
	for _, s := range []int{401, 402, 403, 404} {
		if !isHardFailure(fmt.Errorf("status %d: x", s)) {
			t.Errorf("status %d should be a hard failure", s)
		}
	}
	for _, s := range []int{408, 429, 500, 502, 503} {
		if isHardFailure(fmt.Errorf("status %d: x", s)) {
			t.Errorf("status %d should be transient, not hard", s)
		}
	}
}

// A member whose context window can't hold the request is a guaranteed 400 —
// skip it rather than spend a round-trip proving it.
func TestSkipsMembersTooSmallForRequest(t *testing.T) {
	small, big := okProv("small", "s"), okProv("big", "b")
	ms := member(1, "small", 10, small)
	ms.WindowTokens = 1000
	mb := member(2, "big", 5, big)
	mb.WindowTokens = 1_000_000
	p := New([]*Member{ms, mb}, NewRegistry(nil, nil))

	req := llm.CompletionRequest{Messages: []llm.Message{llm.UserText(strings.Repeat("x", 100_000))}}
	got, err := drain(p.Stream(context.Background(), req))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "b" {
		t.Fatalf("text=%q, want the large-window member to serve it", got)
	}
	if small.calls != 0 {
		t.Fatalf("sent a request to a member that can't hold it (calls=%d)", small.calls)
	}
}

// Everything tripped: probing the head beats stalling the engine outright.
func TestAllTrippedStillProbesHead(t *testing.T) {
	a, b := okProv("a", "a"), okProv("b", "b")
	reg := NewRegistry(nil, nil)
	reg.Trip(1, "402", true)
	reg.Trip(2, "402", true)
	p := New([]*Member{member(1, "a", 10, a), member(2, "b", 5, b)}, reg)

	got, err := drain(p.Stream(context.Background(), llm.CompletionRequest{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "a" || a.calls != 1 {
		t.Fatalf("text=%q a.calls=%d — want the head probed as a last resort", got, a.calls)
	}
}

func TestExhaustedChainReportsClearly(t *testing.T) {
	a, b := failProv("a", 402), failProv("b", 402)
	p := New([]*Member{member(1, "a", 10, a), member(2, "b", 5, b)}, NewRegistry(nil, nil))

	_, err := drain(p.Stream(context.Background(), llm.CompletionRequest{}))
	if !errors.Is(err, ErrExhausted) {
		t.Fatalf("err = %v, want it to wrap ErrExhausted", err)
	}
}

func TestExhaustedChainPreservesFinalDiagnosticsAndReplayPolicy(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			last := &chatgptllm.Error{
				APIKeyMode: true, StatusCode: 429, Code: "insufficient_quota",
				Stage: "admission", Attempts: 1, RequestID: "req-final",
				Message: "API 한도 도달",
			}
			a := &fakeProv{errs: []error{&chatgptllm.Error{
				APIKeyMode: true, StatusCode: 429, Code: "insufficient_quota",
				Stage: "admission", RequestID: "req-first", Message: "API 한도 도달",
			}}}
			b := &fakeProv{errs: []error{fmt.Errorf("recorded provider: %w", last)}}
			p := New([]*Member{member(1, "a", 10, a), member(2, "b", 5, b)}, nil)
			var err error
			if complete {
				_, _, _, err = p.Complete(t.Context(), llm.CompletionRequest{})
			} else {
				_, err = drain(p.Stream(t.Context(), llm.CompletionRequest{}))
			}
			var responseErr *chatgptllm.Error
			var policy interface{ NoReplay() bool }
			var diagnostics interface{ Diagnostics() map[string]any }
			if !errors.Is(err, ErrExhausted) || !errors.As(err, &responseErr) || responseErr != last ||
				!errors.As(err, &policy) || !policy.NoReplay() || !errors.As(err, &diagnostics) {
				t.Fatalf("final provider contract lost: %v", err)
			}
			if diagnostics.Diagnostics()["request_id"] != "req-final" || a.calls != 1 || b.calls != 1 {
				t.Fatalf("wrong final diagnostics/calls: %+v calls=%d/%d", diagnostics.Diagnostics(), a.calls, b.calls)
			}
		})
	}
}

func TestTypedLocalFailuresNeverConsumeFallbackProfile(t *testing.T) {
	for _, tc := range []struct{ code, stage string }{
		{"invalid_config", ""},
		{"unsupported_content", "encoding"},
		{"invalid_replay_state", "encoding"},
		{"invalid_reasoning", "encoding"},
		{"authorization_failed", "authorization"},
		{"not_connected", "authorization"},
		{"unknown_local_failure", ""},
		{"interrupted_stream", "stream"},
	} {
		for _, complete := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/complete=%v", tc.code, complete), func(t *testing.T) {
				local := &chatgptllm.Error{APIKeyMode: true, Code: tc.code, Stage: tc.stage, Message: "local failure"}
				a := &fakeProv{errs: []error{fmt.Errorf("provider: %w", local)}}
				b := okProv("b", "must not be requested")
				reg := NewRegistry(nil, nil)
				p := New([]*Member{member(1, "a", 10, a), member(2, "b", 5, b)}, reg)
				var err error
				if complete {
					_, _, _, err = p.Complete(t.Context(), llm.CompletionRequest{})
				} else {
					_, err = drain(p.Stream(t.Context(), llm.CompletionRequest{}))
				}
				if !errors.Is(err, local) || a.calls != 1 || b.calls != 0 || reg.Get(1).Fails != 0 {
					t.Fatalf("local failure fell over or tripped health: %v calls=%d/%d", err, a.calls, b.calls)
				}
			})
		}
	}
}

func TestTypedTransportAndAPIQuotaStillFailOver(t *testing.T) {
	for _, failure := range []*chatgptllm.Error{
		{APIKeyMode: true, Code: "transport_error", Stage: "admission", Attempts: 4},
		{APIKeyMode: true, Code: "insufficient_quota", Stage: "admission", StatusCode: 429, Attempts: 1},
	} {
		for _, complete := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/complete=%v", failure.Code, complete), func(t *testing.T) {
				a := &fakeProv{errs: []error{fmt.Errorf("provider: %w", failure)}}
				b := okProv("b", "fallback answer")
				p := New([]*Member{member(1, "a", 10, a), member(2, "b", 5, b)}, nil)
				var text string
				var err error
				if complete {
					var msg llm.Message
					msg, _, _, err = p.Complete(t.Context(), llm.CompletionRequest{})
					text = msg.Text()
				} else {
					text, err = drain(p.Stream(t.Context(), llm.CompletionRequest{}))
				}
				if err != nil || text != "fallback answer" || a.calls != 1 || b.calls != 1 {
					t.Fatalf("valid fallback blocked: %q %v calls=%d/%d", text, err, a.calls, b.calls)
				}
			})
		}
	}
}

// Equal-rank members take turns leading, so duplicate keys share the load.
func TestEqualRankRotates(t *testing.T) {
	a, b := okProv("a", "a"), okProv("b", "b")
	head := member(1, "head", RankActive, failProv("head", 402))
	p := New([]*Member{head, member(2, "a", 5, a), member(3, "b", 5, b)}, NewRegistry(nil, nil))

	first := map[string]int{}
	for range 4 {
		txt, _ := drain(p.Stream(context.Background(), llm.CompletionRequest{}))
		first[txt]++
	}
	if first["a"] == 0 || first["b"] == 0 {
		t.Fatalf("same-rank members did not rotate: %v", first)
	}
}

// The head keeps its own rank and never joins a rotation group.
func TestHeadAlwaysFirst(t *testing.T) {
	head := okProv("head", "H")
	p := New([]*Member{
		member(1, "head", RankActive, head),
		member(2, "a", 5, okProv("a", "a")),
		member(3, "b", 5, okProv("b", "b")),
	}, NewRegistry(nil, nil))
	for range 5 {
		if txt, _ := drain(p.Stream(context.Background(), llm.CompletionRequest{})); txt != "H" {
			t.Fatalf("head was not tried first, got %q", txt)
		}
	}
}

// A single-member chain must behave exactly like the bare provider.
func TestSingleMemberPassthrough(t *testing.T) {
	a := okProv("a", "solo")
	p := New([]*Member{member(1, "a", RankActive, a)}, NewRegistry(nil, nil))
	got, err := drain(p.Stream(context.Background(), llm.CompletionRequest{}))
	if err != nil || got != "solo" {
		t.Fatalf("got %q / %v, want solo / nil", got, err)
	}
}

func TestNewEmptyChainIsNil(t *testing.T) {
	if New(nil, nil) != nil {
		t.Fatal("empty chain should yield a nil pool so callers use the bare provider")
	}
}

// An expired cooling-off window must not keep a profile out of the chain.
func TestExpiredWindowIsClosed(t *testing.T) {
	reg := NewRegistry(nil, nil)
	reg.Restore(1, State{Trips: 1, OpenUntil: time.Now().Add(-time.Second)})
	if reg.IsOpen(1) {
		t.Fatal("expired window should read as closed")
	}
}

func TestOpaqueStateNeverCrossesPoolConnection(t *testing.T) {
	for _, complete := range []bool{false, true} {
		for _, same := range []bool{false, true} {
			a, b := failProv("a", 402), okProv("b", "next")
			first, next := member(1, "a", 10, a), member(2, "b", 5, b)
			first.ConnectionIdentity = "connection-one"
			next.ConnectionIdentity = "connection-two"
			if same {
				next.ConnectionIdentity = first.ConnectionIdentity
			}
			pool := New([]*Member{first, next}, NewRegistry(nil, nil))
			req := llm.CompletionRequest{Messages: []llm.Message{{Role: llm.RoleAssistant, ProviderData: []byte(`{"opaque":"fixture"}`)}}}
			var err error
			if complete {
				_, _, _, err = pool.Complete(context.Background(), req)
			} else {
				_, err = drain(pool.Stream(context.Background(), req))
			}
			if same && (err != nil || b.calls != 1) {
				t.Fatalf("same connection fallback blocked: %v", err)
			}
			if !same && (err == nil || b.calls != 0) {
				t.Fatal("private state crossed connection boundary")
			}
		}
	}
}
