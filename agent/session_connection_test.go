package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDaybreakConnectionIdentityPreservesLegacyDefault(t *testing.T) {
	cfg := Config{Model: "model", AuthMode: "api_key", APIKey: "fixture"}
	digest := sha256.Sum256([]byte(`["api_key","fixture","model"]`))
	legacy := hex.EncodeToString(digest[:])
	if cfg.ConnectionIdentity() != legacy {
		t.Fatal("default program changed existing session binding")
	}
	cfg.AccessProgramCyber = "daybreak_blue"
	blue := cfg.ConnectionIdentity()
	if blue == legacy {
		t.Fatal("program change reused the old context")
	}
	cfg.AccessProgramCyber = "standard"
	if cfg.ConnectionIdentity() == blue || cfg.ConnectionIdentity() == legacy {
		t.Fatal("explicit program selections must have distinct bindings")
	}
}

func TestConnectionChangeStartsNewContextAndKeepsLegacyHistory(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "transcripts", "conv-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(legacy), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("legacy replay retained"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Model: "model", AuthMode: "api_key", APIKey: "fixture-private-key"}
	identity := func() string { return cfg.ConnectionIdentity() }
	first, changed, err := connectedSession(dir, "conv-1", identity)
	if err != nil || changed || first != "conv-1" {
		t.Fatalf("legacy unreadable: %q %v %v", first, changed, err)
	}
	cfg.Proxy = "http://localhost:18080"
	cfg.ReasoningEffort = "high"
	same, changed, err := connectedSession(dir, "conv-1", identity)
	if err != nil || changed || same != first {
		t.Fatal("non-identity setting discarded history")
	}
	cfg.APIKey = "other-private-key"
	second, changed, err := connectedSession(dir, "conv-1", identity)
	if err != nil || !changed || second == first {
		t.Fatal("changed key retained old context")
	}
	third, changed, err := connectedSession(dir, "conv-1", identity)
	if err != nil || changed || third != second {
		t.Fatal("restart lost connection binding")
	}
	cfg.AuthMode = "chatgpt_plan"
	cfg.ChatGPTAccountID = "account-fixture"
	fourth, changed, err := connectedSession(dir, "conv-1", identity)
	if err != nil || !changed || fourth == second {
		t.Fatal("changed auth retained context")
	}
	raw, err := os.ReadFile(legacy)
	if err != nil || string(raw) != "legacy replay retained" {
		t.Fatal("old transcript modified")
	}
	binding, err := os.ReadFile(filepath.Join(dir, "transcripts", ".connections", "conv-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-key", "account-fixture"} {
		if strings.Contains(string(binding), secret) {
			t.Fatal("connection binding exposed credentials")
		}
	}
}

func TestCorruptConnectionBindingFailsClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcripts", ".connections")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "conv-1.json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := connectedSession(dir, "conv-1", func() string { return "fixture" }); err == nil {
		t.Fatal("corrupt binding reused context")
	}
}
