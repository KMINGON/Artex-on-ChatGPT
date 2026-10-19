package chatgptauth

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type record struct {
	ClientID         string    `json:"client_id"`
	Issuer           string    `json:"issuer,omitempty"`
	Subject          string    `json:"subject,omitempty"`
	Email            string    `json:"email,omitempty"`
	AccessToken      string    `json:"access_token,omitempty"`
	RefreshToken     string    `json:"refresh_token,omitempty"`
	IDToken          string    `json:"id_token,omitempty"`
	Scopes           []string  `json:"scopes,omitempty"`
	ExpiresAt        time.Time `json:"expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
}

type diskState struct {
	Version  int               `json:"version"`
	HostID   string            `json:"host_id"`
	Accounts map[string]record `json:"accounts"`
}

func openStore(dir string) (diskState, *os.File, error) {
	var state diskState
	if err := os.MkdirAll(dir, 0700); err != nil {
		return state, nil, errStorage
	}
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return state, nil, errStorage
	}
	if err := protectDirectory(dir); err != nil {
		return state, nil, errStorage
	}
	lockPath := filepath.Join(dir, ".lock")
	if err := regularOrAbsent(lockPath); err != nil {
		return state, nil, errStorage
	}
	lock, err := acquireLock(lockPath)
	if err != nil {
		return state, nil, fmt.Errorf("ChatGPT 저장소를 열 수 없습니다. 다른 ARTEX가 같은 저장소를 사용 중인지 확인해 주세요.")
	}
	ok := false
	defer func() {
		if !ok {
			_ = closeLock(lock)
		}
	}()
	path := filepath.Join(dir, "accounts.json")
	if err := regularOrAbsent(path); err != nil {
		return state, nil, errStorage
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return state, nil, errStorage
		}
		id[6] = (id[6] & 0x0f) | 0x40
		id[8] = (id[8] & 0x3f) | 0x80
		state = diskState{Version: 1, HostID: fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16]), Accounts: map[string]record{}}
		if err = writeStore(dir, state); err != nil {
			return state, nil, errStorage
		}
	} else if err != nil {
		return state, nil, errStorage
	} else {
		defer f.Close()
		if err := protectFile(f); err != nil {
			return state, nil, errStorage
		}
		data, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
		if err != nil || len(data) > 4<<20 || json.Unmarshal(data, &state) != nil {
			return state, nil, errStorage
		}
		if state.Version != 1 || !validHostID(state.HostID) || state.Accounts == nil {
			return state, nil, errStorage
		}
		for id, r := range state.Accounts {
			if !validClientID(id) || r.ClientID != id || ((r.AccessToken != "" || r.RefreshToken != "" || r.IDToken != "") && (r.Subject == "" || r.Issuer != issuer)) {
				return state, nil, errStorage
			}
		}
	}
	ok = true
	return state, lock, nil
}

func validHostID(id string) bool {
	if !strings.HasPrefix(id, "urn:uuid:") {
		return false
	}
	s := strings.TrimPrefix(id, "urn:uuid:")
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return false
	}
	for _, c := range strings.ReplaceAll(s, "-", "") {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func regularOrAbsent(path string) error {
	fi, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !fi.Mode().IsRegular() {
		return errStorage
	}
	return nil
}

func (m *Manager) saveLocked() error { return writeStore(m.dir, m.store) }

func writeStore(dir string, state diskState) error {
	path := filepath.Join(dir, "accounts.json")
	if err := regularOrAbsent(path); err != nil {
		return errStorage
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return errStorage
	}
	f, err := os.CreateTemp(dir, ".accounts-*")
	if err != nil {
		return errStorage
	}
	name := f.Name()
	defer os.Remove(name)
	if err = protectFile(f); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errStorage
	}
	if err := os.Rename(name, path); err != nil {
		return errStorage
	}
	return nil
}
