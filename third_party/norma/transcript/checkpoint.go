package transcript

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Autumn-27/norma/llm"
)

// Checkpoint groups a complete post-compaction view. Parts deliberately do not
// use Record.Message, so old readers cannot adopt a half-written checkpoint.
type Checkpoint struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	Count   int    `json:"count"`
	Digest  string `json:"digest"`
	Index   int    `json:"index,omitempty"`
}

func checkpointDigest(msgs []llm.Message) string {
	b, _ := json.Marshal(msgs)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (w *Writer) RecordCheckpoint(msgs []llm.Message) {
	meta := Checkpoint{Version: 1, ID: newID(), Count: len(msgs), Digest: checkpointDigest(msgs)}
	recs := []Record{{Type: "checkpoint_begin", Checkpoint: &meta}}
	for i := range msgs {
		part := meta
		part.Index = i
		recs = append(recs, Record{Type: "checkpoint_part", Checkpoint: &part, CheckpointMessage: &msgs[i]})
	}
	recs = append(recs, Record{Type: "checkpoint_commit", Checkpoint: &meta})
	w.write(recs...)
}

func loadRecords(path string) ([]Record, bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	var accepted, pending []Record
	var parts []llm.Message
	recovered := false
	for {
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, false, readErr
		}
		if len(line) == 0 {
			break
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		// A valid final JSON value without its delimiter must be copied before
		// appending, otherwise the next record would be concatenated onto it.
		if errors.Is(readErr, io.EOF) {
			recovered = true
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			// Only a torn final line inside an identified checkpoint is recoverable.
			if errors.Is(readErr, io.EOF) && len(pending) > 0 {
				recovered = true
				break
			}
			return nil, false, fmt.Errorf("transcript: corrupt JSONL record: %w", err)
		}
		switch r.Type {
		case "message", "boundary":
			if r.Type == "message" && r.Message == nil {
				return nil, false, errors.New("transcript: message record has no message")
			}
			if len(pending) > 0 {
				recovered = true
				pending = nil
				parts = nil
			}
			accepted = append(accepted, r)
		case "checkpoint_begin", "checkpoint_part", "checkpoint_commit":
			c := r.Checkpoint
			if c == nil || c.Version != 1 || c.ID == "" || c.Count < 2 || c.Digest == "" {
				return nil, false, errors.New("transcript: unsupported or invalid checkpoint")
			}
			if r.Type == "checkpoint_begin" {
				if len(pending) > 0 {
					recovered = true
				}
				pending, parts = []Record{r}, nil
				continue
			}
			if len(pending) == 0 {
				return nil, false, errors.New("transcript: checkpoint without begin")
			}
			h := pending[0].Checkpoint
			if c.ID != h.ID || c.Count != h.Count || c.Digest != h.Digest {
				return nil, false, errors.New("transcript: checkpoint metadata mismatch")
			}
			pending = append(pending, r)
			if r.Type == "checkpoint_part" {
				if r.CheckpointMessage == nil || c.Index != len(parts) || len(parts) >= c.Count {
					return nil, false, errors.New("transcript: invalid checkpoint part")
				}
				parts = append(parts, *r.CheckpointMessage)
			} else {
				if len(parts) != c.Count || checkpointDigest(parts) != c.Digest || !llm.IsBoundaryMarker(parts[0]) {
					return nil, false, errors.New("transcript: incomplete or corrupt committed checkpoint")
				}
				accepted = append(accepted, pending...)
				pending, parts = nil, nil
			}
		default:
			return nil, false, fmt.Errorf("transcript: unsupported record type %q", r.Type)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	if len(pending) > 0 {
		recovered = true
	}
	// Old checkpoints had no length/commit metadata. A bare last marker is
	// provably incomplete; anything more ambiguous must not be guessed away.
	if n := len(accepted); n > 0 && accepted[n-1].Message != nil && llm.IsBoundaryMarker(*accepted[n-1].Message) {
		accepted = accepted[:n-1]
		recovered = true
	}
	return accepted, recovered, nil
}

// Recovery files preserve their source bytes. A deterministic adjacent name
// keeps logical session IDs and connection bindings unchanged across restarts.
func recoveryPath(path string) (string, error) {
	for i := 0; i < 32; i++ {
		next := path + ".recovered"
		info, err := os.Stat(next)
		if errors.Is(err, os.ErrNotExist) {
			return path, nil
		}
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", errors.New("transcript: recovery path is not a regular file")
		}
		path = next
	}
	return "", errors.New("transcript: too many recovery generations")
}

// ResumeRecords validates before enabling writes. Only recognized incomplete
// checkpoints are removed, in a new file; ordinary corruption fails closed.
func (s *Store) ResumeRecords(sessionID string) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := recoveryPath(s.MainPath(sessionID))
	if err != nil {
		return nil, err
	}
	recs, recovered, err := loadRecords(path)
	if err != nil || !recovered {
		return recs, err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".transcript-recovery-*")
	if err != nil {
		return nil, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range recs {
		if err := enc.Encode(r); err != nil {
			return nil, err
		}
	}
	if err := s.syncFile(f); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path+".recovered"); err != nil {
		return nil, err
	}
	if err := s.syncDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	return recs, nil
}
