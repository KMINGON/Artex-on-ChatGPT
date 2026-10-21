package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func TestIncompleteCheckpointPreservesCommittedViewAndLaterBirths(t *testing.T) {
	fixture := NewStore(t.TempDir())
	w := fixture.NewWriter("s", "")
	w.RecordMessage(llm.UserText("old"), llm.Usage{})
	w.RecordCheckpoint([]llm.Message{llm.BoundaryMessage(llm.BoundaryMeta{}), llm.UserText("SUMMARY"), llm.UserText("tail")})
	w.RecordMessage(llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "call", Name: "Read", Input: []byte(`{}`)}}}, llm.Usage{OutputTokens: 5})
	w.RecordMessage(llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText("call", "REAL_RESULT", false)}}, llm.Usage{})
	base, err := os.ReadFile(fixture.MainPath("s"))
	if err != nil {
		t.Fatal(err)
	}
	w.RecordCheckpoint([]llm.Message{llm.BoundaryMessage(llm.BoundaryMeta{}), llm.UserText("NEW_SUMMARY"), llm.UserText("new tail")})
	all, _ := os.ReadFile(fixture.MainPath("s"))
	lines := bytes.SplitAfter(all[len(base):], []byte("\n"))
	for cut := 1; cut < len(lines)-1; cut++ {
		for _, torn := range []bool{false, true} {
			s := NewStore(t.TempDir())
			path := s.MainPath("s")
			original := append(append([]byte(nil), base...), bytes.Join(lines[:cut], nil)...)
			if torn {
				original = append(original, lines[cut][:len(lines[cut])/2]...)
			}
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			recs, err := s.ResumeRecords("s")
			if err != nil {
				t.Fatalf("cut=%d torn=%v: %v", cut, torn, err)
			}
			msgs, usage := Messages(recs)
			view := llm.MessagesForAPI(msgs)
			if len(view) != 4 || view[0].Text() != "SUMMARY" || view[3].Content[0].Content[0].Text != "REAL_RESULT" || usage.OutputTokens != 5 {
				t.Fatalf("lost real births: %+v %+v", view, usage)
			}
			rw := s.ResumeWriter("s", LastUUID(recs))
			rw.RecordMessage(llm.UserText("continued"), llm.Usage{})
			if rw.Err() != nil {
				t.Fatal(rw.Err())
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(original, after) {
				t.Fatal("damaged source was modified")
			}
			for i := 0; i < 2; i++ {
				again, err := s.ResumeRecords("s")
				if err != nil {
					t.Fatal(err)
				}
				m, _ := Messages(again)
				v := llm.MessagesForAPI(m)
				if len(v) != 5 || v[4].Text() != "continued" {
					t.Fatalf("restart duplicated or lost messages: %d", len(v))
				}
			}
		}
	}
}

func TestIncompleteCheckpointDoesNotDiscardFollowingOrdinaryMessages(t *testing.T) {
	s := NewStore(t.TempDir())
	w := s.NewWriter("s", "")
	w.RecordMessage(llm.UserText("before"), llm.Usage{})
	meta := Checkpoint{Version: 1, ID: "cp", Count: 2, Digest: "unfinished"}
	w.write(Record{Type: "checkpoint_begin", Checkpoint: &meta}, Record{Type: "checkpoint_part", Checkpoint: &meta, CheckpointMessage: messagePtr(llm.BoundaryMessage(llm.BoundaryMeta{}))})
	w.RecordMessage(llm.UserText("after"), llm.Usage{})
	r, err := s.ResumeRecords("s")
	if err != nil {
		t.Fatal(err)
	}
	m, _ := Messages(r)
	if len(m) != 2 || m[0].Text() != "before" || m[1].Text() != "after" {
		t.Fatalf("messages=%+v", m)
	}
}

func TestTranscriptCorruptionFailsClosed(t *testing.T) {
	for _, suffix := range []string{"{torn", "{broken}\n", `{"type":"future"}` + "\n", `{"type":"checkpoint_begin","checkpoint":{"version":99,"id":"x","count":2,"digest":"x"}}` + "\n"} {
		s := NewStore(t.TempDir())
		w := s.NewWriter("s", "")
		w.RecordMessage(llm.UserText("safe"), llm.Usage{})
		if err := appendRaw(s.MainPath("s"), suffix); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(s.MainPath("s"))
		if _, err := s.ResumeRecords("s"); err == nil {
			t.Fatalf("accepted corrupt suffix %q", suffix)
		}
		after, _ := os.ReadFile(s.MainPath("s"))
		if !bytes.Equal(before, after) {
			t.Fatal("source changed")
		}
		if _, err := os.Stat(s.MainPath("s") + ".recovered"); !os.IsNotExist(err) {
			t.Fatal("published uncertain recovery")
		}
	}
}

func TestCommittedCheckpointDigestValidated(t *testing.T) {
	s := NewStore(t.TempDir())
	w := s.NewWriter("s", "")
	w.RecordCheckpoint([]llm.Message{llm.BoundaryMessage(llm.BoundaryMeta{}), llm.UserText("original")})
	b, _ := os.ReadFile(s.MainPath("s"))
	b = bytes.ReplaceAll(b, []byte("original"), []byte("changed"))
	os.WriteFile(s.MainPath("s"), b, 0600)
	if _, err := s.ResumeRecords("s"); err == nil {
		t.Fatal("accepted corrupted committed checkpoint")
	}
}

func TestLegacyBareBoundaryRecoveryAndWriterStickyError(t *testing.T) {
	s := NewStore(t.TempDir())
	w := s.NewWriter("s", "")
	w.RecordMessage(llm.UserText("kept"), llm.Usage{})
	w.RecordMessage(llm.BoundaryMessage(llm.BoundaryMeta{}), llm.Usage{})
	r, err := s.ResumeRecords("s")
	if err != nil {
		t.Fatal(err)
	}
	m, _ := Messages(r)
	if len(m) != 1 || m[0].Text() != "kept" {
		t.Fatal("legacy prefix lost")
	}
	w2 := s.NewWriter("blocked", "")
	os.Mkdir(w2.path, 0700)
	w2.RecordMessage(llm.UserText("first"), llm.Usage{})
	if w2.Err() == nil {
		t.Fatal("write failure hidden")
	}
	os.Remove(w2.path)
	w2.RecordMessage(llm.UserText("second"), llm.Usage{})
	if _, err := os.Stat(w2.path); !os.IsNotExist(err) {
		t.Fatal("writer continued after failure")
	}
}

func TestRecoveryPublicationFailureLeavesOriginal(t *testing.T) {
	s := NewStore(t.TempDir())
	w := s.NewWriter("s", "")
	w.RecordMessage(llm.UserText("kept"), llm.Usage{})
	w.RecordMessage(llm.BoundaryMessage(llm.BoundaryMeta{}), llm.Usage{})
	p := s.MainPath("s")
	before, _ := os.ReadFile(p)
	os.Mkdir(p+".recovered", 0700)
	if _, err := s.ResumeRecords("s"); err == nil {
		t.Fatal("invalid publication path accepted")
	}
	after, _ := os.ReadFile(p)
	if !bytes.Equal(before, after) {
		t.Fatal("source changed")
	}
	matches, _ := filepath.Glob(filepath.Join(s.dir, ".transcript-recovery-*"))
	if len(matches) > 0 {
		t.Fatal("temporary file leaked")
	}
}

func messagePtr(m llm.Message) *llm.Message { return &m }

func TestCheckpointPartsAreInvisibleToLegacyReader(t *testing.T) {
	s := NewStore(t.TempDir())
	w := s.NewWriter("s", "")
	w.RecordCheckpoint([]llm.Message{llm.BoundaryMessage(llm.BoundaryMeta{}), llm.UserText("summary")})
	b, _ := os.ReadFile(s.MainPath("s"))
	for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		var r struct {
			Message *llm.Message `json:"message"`
		}
		if err := json.Unmarshal(line, &r); err != nil || r.Message != nil {
			t.Fatal("legacy reader could adopt partial checkpoint")
		}
	}
}

func TestSyncFailuresAreReportedAndRecoverySourceIsPreserved(t *testing.T) {
	for _, failFile := range []bool{true, false} {
		s := NewStore(t.TempDir())
		w := s.NewWriter("s", "")
		w.RecordMessage(llm.UserText("kept"), llm.Usage{})
		w.RecordMessage(llm.BoundaryMessage(llm.BoundaryMeta{}), llm.Usage{})
		before, _ := os.ReadFile(s.MainPath("s"))
		failure := errors.New("injected durability failure")
		if failFile {
			s.syncFile = func(*os.File) error { return failure }
		} else {
			s.syncDir = func(string) error { return failure }
		}
		if _, err := s.ResumeRecords("s"); !errors.Is(err, failure) {
			t.Fatalf("recovery hid sync failure: %v", err)
		}
		after, _ := os.ReadFile(s.MainPath("s"))
		if !bytes.Equal(before, after) {
			t.Fatal("source changed after failed sync")
		}
		if failFile {
			if _, err := os.Stat(s.MainPath("s") + ".recovered"); !os.IsNotExist(err) {
				t.Fatal("unsynced recovery was published")
			}
		}
		w2 := s.NewWriter("fresh", "")
		w2.RecordMessage(llm.UserText("new"), llm.Usage{})
		if !errors.Is(w2.Err(), failure) {
			t.Fatal("writer hid sync failure")
		}
	}
}

func TestResumeRepairsMissingFinalDelimiterWithoutChangingSource(t *testing.T) {
	s := NewStore(t.TempDir())
	w := s.NewWriter("s", "")
	w.RecordMessage(llm.UserText("first"), llm.Usage{})
	p := s.MainPath("s")
	b, _ := os.ReadFile(p)
	original := bytes.TrimSuffix(b, []byte("\n"))
	os.WriteFile(p, original, 0600)
	r, err := s.ResumeRecords("s")
	if err != nil {
		t.Fatal(err)
	}
	w = s.ResumeWriter("s", LastUUID(r))
	w.RecordMessage(llm.UserText("second"), llm.Usage{})
	if w.Err() != nil {
		t.Fatal(w.Err())
	}
	r, err = s.Load(p)
	if err != nil || len(r) != 2 {
		t.Fatalf("records concatenated: %d %v", len(r), err)
	}
	after, _ := os.ReadFile(p)
	if !bytes.Equal(original, after) {
		t.Fatal("source changed")
	}
}
