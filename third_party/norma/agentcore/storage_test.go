package agentcore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

func storageTerminal(t *testing.T, s *Session) *harness.Terminal {
	t.Helper()
	var term *harness.Terminal
	for ev, err := range s.Prompt(context.Background(), "continue") {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Terminal != nil {
			term = ev.Terminal
		}
	}
	if term == nil || term.Reason != harness.ReasonStorageError || term.Err == nil {
		t.Fatalf("storage failure hidden: %+v", term)
	}
	var marker interface{ NoReplay() bool }
	if !errors.As(term.Err, &marker) || !marker.NoReplay() {
		t.Fatal("storage failure may replay")
	}
	if s.Close() == nil {
		t.Fatal("Close hid storage error")
	}
	return term
}

func TestResumeFailureStopsEvenIfCallerIgnoresError(t *testing.T) {
	store := transcript.NewStore(t.TempDir())
	path := store.MainPath("s")
	before := []byte("{broken\n")
	os.WriteFile(path, before, 0600)
	calls := 0
	s := NewSession(Options{Transcript: store, SessionID: "s", Tools: []tool.CoreTool{}, DisableBackgroundTasks: true, Provider: fixtureProvider(func(req llm.CompletionRequest) llm.Message { calls++; return textProvider("bad")(req) })})
	_ = s.Resume("s")
	storageTerminal(t, s)
	after, _ := os.ReadFile(path)
	if calls != 0 || !bytes.Equal(before, after) {
		t.Fatalf("calls=%d original modified=%v", calls, !bytes.Equal(before, after))
	}
}

func TestWriteFailureBeforeModelAndAfterResponse(t *testing.T) {
	for _, before := range []bool{true, false} {
		store := transcript.NewStore(t.TempDir())
		path := store.MainPath("s")
		calls := 0
		if before {
			os.Mkdir(path, 0700)
		}
		s := NewSession(Options{Transcript: store, SessionID: "s", Tools: []tool.CoreTool{}, DisableBackgroundTasks: true, Provider: fixtureProvider(func(req llm.CompletionRequest) llm.Message {
			calls++
			os.Rename(path, path+".original")
			os.Mkdir(path, 0700)
			return textProvider("visible answer")(req)
		})})
		term := storageTerminal(t, s)
		want := 1
		if before {
			want = 0
		}
		if calls != want {
			t.Fatalf("calls=%d want=%d", calls, want)
		}
		if !before && term.Text != "visible answer" {
			t.Fatal("lost already visible answer")
		}
		storageTerminal(t, s)
		if calls != want {
			t.Fatal("failure retried model")
		}
	}
}
