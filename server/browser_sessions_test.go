package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/config"
	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

func TestBrowserSessionIsolation(t *testing.T) {
	base := agent.RunInfo{TaskID: 2, ExplorationID: 4, IntentID: 8}
	id := browserSessionID("worker", base)
	if id != browserSessionID("worker", base) {
		t.Fatal("resuming the same worker changed identity")
	}
	for _, other := range []agent.RunInfo{{TaskID: 3, ExplorationID: 4, IntentID: 8}, {TaskID: 2, ExplorationID: 5, IntentID: 8}, {TaskID: 2, ExplorationID: 4, IntentID: 9}, {SessionID: "conversation-1"}} {
		if id == browserSessionID("worker", other) {
			t.Fatal("distinct task/worker shared a profile")
		}
	}
	if id == browserSessionID("pentest", base) {
		t.Fatal("distinct agent shared a profile")
	}
}

func TestBrowserCustomConfigurationPreserved(t *testing.T) {
	for _, tc := range []struct {
		command, args string
		want          bool
	}{
		{"node", `["scripts/browser-mcp.cjs","--headless"]`, true},
		{"npx", `["@playwright/mcp","--headless","--proxy-server","http://127.0.0.1:8788"]`, true},
		{"npx", `["@playwright/mcp","--isolated"]`, false},
		{"node", `["scripts/browser-mcp.cjs","--user-data-dir","/custom"]`, false},
		{"npx", `["@playwright/mcp","--proxy-server"]`, false},
		{"custom-browser", `[]`, false},
	} {
		got := managedBrowser(&db.MCPServer{Name: "browser", Transport: "stdio", Command: tc.command, Args: json.RawMessage(tc.args)})
		if got != tc.want {
			t.Errorf("%s %s managed=%v", tc.command, tc.args, got)
		}
	}
}

func TestBrowserActionValidation(t *testing.T) {
	for _, raw := range []string{`{"action":"navigate","url":"file:///app/auth/accounts.json"}`, `{"action":"navigate","url":"javascript:alert(1)"}`, `{"action":"navigate","url":"http://user:pass@localhost"}`, `{"action":"evaluate","text":"process.env"}`, `{"action":"click","x":1290,"y":10}`, `{"action":"key","key":"F12"}`, `{"action":"select_tab","index":0.5}`} {
		var v map[string]any
		_ = json.Unmarshal([]byte(raw), &v)
		if validateBrowserAction(v) == nil {
			t.Errorf("accepted invalid action %s", raw)
		}
	}
	for _, raw := range []string{`{"action":"navigate","url":"http://host.docker.internal:3000"}`, `{"action":"drag","x":10,"y":10,"to_x":200,"to_y":100}`, `{"action":"key","key":"ControlOrMeta+A"}`, `{"action":"type","text":"fixture-secret","id":42}`} {
		var v map[string]any
		_ = json.Unmarshal([]byte(raw), &v)
		if err := validateBrowserAction(v); err != nil {
			t.Fatal(err)
		}
		if _, ok := v["id"]; ok {
			t.Fatal("caller controlled RPC id")
		}
	}
}

func fakeBrowserSession(t *testing.T) *browserSession {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required for private stdio bridge lifecycle test")
	}
	dir := t.TempDir()
	script := `const readline=require('node:readline');process.stdout.write('{"ready":true}\n');readline.createInterface({input:process.stdin}).on('line',line=>{const r=JSON.parse(line);process.stdout.write(JSON.stringify({id:r.id,result:{url:'http://fixture/',title:'fixture',viewport:{width:1280,height:800},tabs:[]}})+'\n')}).on('close',()=>process.exit(0));`
	if err := os.WriteFile(filepath.Join(dir, "browser-session.cjs"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARTEX_BROWSER_SCRIPTS", dir)
	s := &browserSession{id: "fixture", directory: t.TempDir(), port: 12345, last: time.Now()}
	t.Cleanup(s.close)
	return s
}

func TestBrowserBridgeSurvivesRequestCancellationAndCloses(t *testing.T) {
	s := fakeBrowserSession(t)
	ctx, cancel := context.WithCancel(t.Context())
	if err := s.ensure(ctx); err != nil {
		t.Fatal(err)
	}
	b := s.bridge
	cancel() // Ending the takeover HTTP request must not disable later UI actions.
	var status browserSessionView
	if err := b.call(t.Context(), map[string]any{"action": "status"}, &status); err != nil {
		t.Fatal(err)
	}
	if status.Title != "fixture" {
		t.Fatalf("bad status %#v", status)
	}
	s.close()
	select {
	case <-b.done:
	default:
		t.Fatal("bridge process survived close")
	}
	if s.bridge != nil {
		t.Fatal("closed bridge retained")
	}
}

func TestBrowserHumanTakeoverBlocksToolsUntilResume(t *testing.T) {
	s := fakeBrowserSession(t)
	s.beginHuman("fixture authentication")
	called := make(chan struct{}, 1)
	base := actool.Build(actool.Spec{Name: "test", Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
		called <- struct{}{}
		return actool.Text("ok"), nil
	}})
	tool := &managedBrowserTool{CoreTool: base, session: s}
	done := make(chan error, 1)
	go func() { _, err := tool.Call(t.Context(), json.RawMessage(`{}`), nil); done <- err }()
	select {
	case <-called:
		t.Fatal("agent operated while human owns browser")
	case <-time.After(20 * time.Millisecond):
	}
	s.resume()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("resume failed")
	}
	select {
	case <-called:
	default:
		t.Fatal("tool did not resume")
	}
	s.beginHuman("cancel fixture")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := tool.Call(ctx, json.RawMessage(`{}`), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wait=%v", err)
	}
}

func TestBrowserLimitResizeAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m := newBrowserSessionManager(ctx, t.TempDir())
	m.SetLimit(2)
	if err := m.takeSlot(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.takeSlot(ctx); err != nil {
		t.Fatal(err)
	}
	m.SetLimit(1)
	short, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	if err := m.takeSlot(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("full capacity: %v", err)
	}
	if m.running != 2 {
		t.Fatalf("canceled wait reserved a slot: %d", m.running)
	}
	m.releaseSlot()
	short2, stop2 := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop2()
	if err := m.takeSlot(short2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lowered limit ignored: %v", err)
	}
	m.releaseSlot()
	if err := m.takeSlot(ctx); err != nil {
		t.Fatal(err)
	}
	m.SetLimit(2)
	if err := m.takeSlot(ctx); err != nil {
		t.Fatal(err)
	}
	m.releaseSlot()
	m.releaseSlot()
}

func TestBrowserProfileRemovalIncludesRestartedOwners(t *testing.T) {
	dir := t.TempDir()
	m := &browserSessionManager{dir: dir, sessions: map[string]*browserSession{}}
	for name, info := range map[string]agent.RunInfo{"deleted": {TaskID: 7}, "kept": {TaskID: 8}} {
		profile := filepath.Join(dir, name)
		if err := os.MkdirAll(profile, 0700); err != nil {
			t.Fatal(err)
		}
		owner, _ := json.Marshal(info)
		if err := os.WriteFile(filepath.Join(profile, "owner.json"), owner, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.removeTask(7); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "deleted")); !os.IsNotExist(err) {
		t.Fatal("deleted task retained authentication profile")
	}
	if _, err := os.Stat(filepath.Join(dir, "kept")); err != nil {
		t.Fatal("other task lost authentication profile")
	}
	active := &browserSession{id: "active", directory: filepath.Join(dir, "active"), active: true}
	m.sessions[active.id] = active
	if err := m.drop(active.id); err == nil {
		t.Fatal("active browser deleted")
	}
	active.active = false
	if err := m.drop(active.id); err != nil {
		t.Fatal(err)
	}
	if err := active.ensure(t.Context()); err == nil {
		t.Fatal("deleted handle launched a browser")
	}
}

func TestBrowserUnexpectedExitReleasesCapacity(t *testing.T) {
	s := fakeBrowserSession(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m := newBrowserSessionManager(ctx, t.TempDir())
	m.SetLimit(1)
	s.manager = m
	if err := s.ensure(ctx); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	bridge := s.bridge
	s.mu.Unlock()
	if err := bridge.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	short, stop := context.WithTimeout(ctx, 2*time.Second)
	defer stop()
	if err := m.takeSlot(short); err != nil {
		t.Fatalf("dead process retained capacity: %v", err)
	}
	m.releaseSlot()
}

func TestBrowserFailedDeletionRemainsRetryable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("file permission failure cannot be reproduced as root")
	}
	dir := t.TempDir()
	profile := filepath.Join(dir, "fixture")
	if err := os.Mkdir(profile, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "session.json"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(profile, 0500); err != nil {
		t.Skipf("filesystem does not support permissions: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(profile, 0700) })
	session := &browserSession{id: "fixture", directory: profile}
	m := &browserSessionManager{dir: dir, sessions: map[string]*browserSession{"fixture": session}}
	if err := m.drop("fixture"); err == nil {
		t.Fatal("expected profile deletion failure")
	}
	if m.sessions["fixture"] != session || !session.deleted {
		t.Fatal("failed deletion lost its retryable, closed session")
	}
	if err := session.ensure(t.Context()); err == nil {
		t.Fatal("partially deleted profile reopened")
	}
	if err := os.Chmod(profile, 0700); err != nil {
		t.Fatal(err)
	}
	if err := m.drop("fixture"); err != nil {
		t.Fatal(err)
	}
	if _, exists := m.sessions["fixture"]; exists {
		t.Fatal("successful retry retained session")
	}
}

func TestBrowserControlResponseDoesNotEchoSecrets(t *testing.T) {
	session := fakeBrowserSession(t)
	if err := session.ensure(t.Context()); err != nil {
		t.Fatal(err)
	}
	session.beginHuman("사용자가 작성한 안내")
	s := &Server{browsers: &browserSessionManager{sessions: map[string]*browserSession{session.id: session}}}
	req := httptest.NewRequest("POST", "/api/browser/sessions/fixture/action", strings.NewReader(`{"action":"type","text":"fixture-secret-never-echo"}`))
	req.SetPathValue("id", session.id)
	w := httptest.NewRecorder()
	s.browserAction(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "fixture-secret-never-echo") {
		t.Fatal("typed secret echoed in browser response")
	}
	old := config.CurrentLanguage
	t.Cleanup(func() { config.CurrentLanguage = old })
	config.CurrentLanguage = func() string { return "en" }
	req = httptest.NewRequest("POST", "/api/browser/sessions/fixture/action", strings.NewReader(`{"action":"evaluate"}`))
	req.SetPathValue("id", session.id)
	w = httptest.NewRecorder()
	s.browserAction(w, req)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "Check the browser action") {
		t.Fatalf("English browser error: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(session.handoffTool().Description(), "authentication requires a human") {
		t.Fatal("handoff tool did not follow selected language")
	}
}
