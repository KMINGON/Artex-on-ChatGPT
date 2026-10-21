package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/Autumn-27/norma/transcript"
)

// ConnectionIdentity describes the current connection, not the origin of old
// replay items. Never write credentials or account IDs into a transcript.
func (c Config) ConnectionIdentity() string {
	mode := c.AuthMode
	if mode == "" {
		mode = "api_key"
	}
	credential := c.APIKey
	if mode == "chatgpt_plan" {
		credential = c.ChatGPTAccountID
	}
	identity := []string{mode, credential, c.Model}
	// Keep pre-Daybreak bindings unchanged unless an explicit program is selected.
	if c.AccessProgramCyber != "" {
		identity = append(identity, c.AccessProgramCyber)
	}
	raw, _ := json.Marshal(identity)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func (c *ChatAgent) SetConnectionIdentity(fn func() string) { c.connectionIdentity = fn }
func (m *MainAgent) SetConnectionIdentity(fn func() string) { m.connectionIdentity = fn }

func (w *Worker) SetConnectionIdentity(fn func() string)  { w.connectionIdentity = fn }
func (p *Planner) SetConnectionIdentity(fn func() string) { p.connectionIdentity = fn }

var sessionConnectionMu sync.Mutex

type sessionConnection struct {
	Identity string `json:"last_connection"`
	Session  string `json:"session"`
}

// Record the last observed connection separately from v1 replay. An existing
// unbound transcript remains readable; its historical account is not inferred.
// Once a change is known, use a fresh transcript, retaining both old history
// and conversation working files.
func connectedSession(workDir, base string, identity func() string) (string, bool, error) {
	if identity == nil {
		return base, false, nil
	}
	key := identity()
	if key == "" {
		return "", false, errors.New("모델 연결을 확인할 수 없습니다. 프로필을 다시 선택해 주세요")
	}
	if base == "" || filepath.Base(base) != base {
		return "", false, errors.New("대화 세션 이름이 올바르지 않습니다")
	}
	sessionConnectionMu.Lock()
	defer sessionConnectionMu.Unlock()
	dir := filepath.Join(workDir, "transcripts", ".connections")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", false, err
	}
	path := filepath.Join(dir, base+".json")
	binding := sessionConnection{Identity: key, Session: base}
	changed := false
	raw, err := os.ReadFile(path)
	if err == nil {
		if json.Unmarshal(raw, &binding) != nil || binding.Identity == "" || binding.Session == "" || filepath.Base(binding.Session) != binding.Session {
			return "", false, errors.New("대화 연결 기록이 올바르지 않습니다. 새 대화를 시작해 주세요")
		}
		if binding.Identity == key {
			return binding.Session, false, nil
		}
		binding = sessionConnection{Identity: key, Session: base + "-connection-" + transcript.NewSessionID()}
		changed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	raw, _ = json.Marshal(binding)
	tmp, err := os.CreateTemp(dir, ".connection-")
	if err != nil {
		return "", false, err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(raw); err != nil {
		tmp.Close()
		return "", false, err
	}
	if err = tmp.Close(); err != nil {
		return "", false, err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return "", false, err
	}
	return binding.Session, changed, nil
}
