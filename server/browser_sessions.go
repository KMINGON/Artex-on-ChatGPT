package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/mcp"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

type browserSessionManager struct {
	ctx              context.Context
	dir              string
	mu               sync.Mutex
	sessions         map[string]*browserSession
	resourceMu       sync.Mutex
	limit, running   int
	resourcesChanged chan struct{}
}

type browserSession struct {
	mu                   sync.Mutex
	op                   sync.Mutex // browser tool calls and human actions must never interleave
	id, directory, agent string
	info                 agent.RunInfo
	port                 int
	env                  map[string]string
	proxy, bypass        string
	active               bool
	deleted              bool
	waiting              *browserHumanWait
	reason               string
	last                 time.Time
	bridge               *browserBridge
	manager              *browserSessionManager
	hasSlot              bool
}

type browserHumanWait struct {
	done chan struct{}
	err  error // finalized before done closes; preserved across later handoffs
}

func (w *browserHumanWait) wait(ctx context.Context) error {
	if w == nil {
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.done:
		if err := ctx.Err(); err != nil {
			return err
		}
		return w.err
	}
}

type browserSessionView struct {
	ID             string           `json:"id"`
	TaskID         int64            `json:"task_id,omitempty"`
	IntentID       int64            `json:"intent_id,omitempty"`
	ConversationID string           `json:"conversation_id,omitempty"`
	Agent          string           `json:"agent"`
	State          string           `json:"state"`
	Reason         string           `json:"reason,omitempty"`
	URL            string           `json:"url"`
	Title          string           `json:"title"`
	Viewport       map[string]int   `json:"viewport"`
	Tabs           []map[string]any `json:"tabs"`
}

func newBrowserSessionManager(ctx context.Context, dir string) *browserSessionManager {
	m := &browserSessionManager{ctx: ctx, dir: dir, sessions: map[string]*browserSession{}, limit: 4, resourcesChanged: make(chan struct{})}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				for _, s := range m.list() {
					s.close()
				}
				return
			case <-ticker.C:
				for _, s := range m.list() {
					s.mu.Lock()
					idle := !s.active && ((s.waiting == nil && time.Since(s.last) > 10*time.Minute) || time.Since(s.last) > 30*time.Minute)
					s.mu.Unlock()
					if idle {
						s.close()
					}
				}
			}
		}
	}()
	return m
}

// CloseBrowsers waits for cookie/session storage persistence before the daemon exits.
func (s *Server) CloseBrowsers() {
	if s.browsers == nil {
		return
	}
	var pending sync.WaitGroup
	for _, session := range s.browsers.list() {
		pending.Add(1)
		go func() { defer pending.Done(); session.close() }()
	}
	pending.Wait()
}

// SetLimit affects future browser launches; an in-flight browser is never killed.
func (m *browserSessionManager) SetLimit(n int) {
	if n < 1 {
		n = 4
	}
	m.resourceMu.Lock()
	defer m.resourceMu.Unlock()
	m.limit = n
	if m.resourcesChanged != nil {
		close(m.resourcesChanged)
	}
	m.resourcesChanged = make(chan struct{})
}
func (m *browserSessionManager) takeSlot(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		m.resourceMu.Lock()
		if m.limit < 1 {
			m.limit = 4
		}
		if m.running < m.limit {
			m.running++
			m.resourceMu.Unlock()
			return nil
		}
		if m.resourcesChanged == nil {
			m.resourcesChanged = make(chan struct{})
		}
		changed := m.resourcesChanged
		m.resourceMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-m.ctx.Done():
			return m.ctx.Err()
		case <-changed:
		}
	}
}
func (m *browserSessionManager) releaseSlot() {
	m.resourceMu.Lock()
	defer m.resourceMu.Unlock()
	if m.running > 0 {
		m.running--
	}
	if m.resourcesChanged != nil {
		close(m.resourcesChanged)
	}
	m.resourcesChanged = make(chan struct{})
}

func (m *browserSessionManager) list() []*browserSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*browserSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out
}

// Only the built-in launch recipe is managed. Custom browser commands retain their semantics.
func managedBrowser(config *db.MCPServer) bool {
	if config.Name != browserMCPName || config.Transport != "stdio" {
		return false
	}
	args := jsonStrSlice(config.Args)
	if len(args) == 0 {
		return false
	}
	builtinNode := config.Command == "node" && filepath.Base(args[0]) == "browser-mcp.cjs"
	builtinNpx := config.Command == "npx" && (args[0] == "@playwright/mcp" || args[0] == "@playwright/mcp@0.0.83")
	if !builtinNode && !builtinNpx {
		return false
	}
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--headless":
		case "--proxy-server", "--proxy-bypass":
			if i+1 >= len(args) {
				return false
			}
			i++
		default:
			return false
		}
	}
	return true
}

func browserSessionID(key string, info agent.RunInfo) string {
	b, _ := json.Marshal([]any{key, info.TaskID, info.ExplorationID, info.IntentID, info.SessionID})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:16])
}

func (m *browserSessionManager) acquire(ctx context.Context, key string, config *db.MCPServer) (mcpClient, error) {
	info := agent.RunInfoFrom(ctx)
	if info.TaskID == 0 && info.SessionID == "" {
		return connectMCP(ctx, config)
	}
	id := browserSessionID(key, info)
	m.mu.Lock()
	s := m.sessions[id]
	if s == nil {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		port := listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
		s = &browserSession{id: id, directory: filepath.Join(m.dir, id), agent: key, info: info, port: port, last: time.Now(), manager: m}
		m.sessions[id] = s
	}
	s.mu.Lock()
	if s.deleted {
		s.mu.Unlock()
		m.mu.Unlock()
		return nil, errors.New("삭제된 브라우저 세션입니다")
	}
	if s.active {
		s.mu.Unlock()
		m.mu.Unlock()
		return nil, errors.New("이 브라우저 세션은 이미 다른 실행에서 사용 중입니다")
	}
	s.active = true
	s.env = jsonStrMap(config.Env)
	s.proxy, s.bypass = "", ""
	args := jsonStrSlice(config.Args)
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--proxy-server" {
			s.proxy = args[i+1]
		}
		if args[i] == "--proxy-bypass" {
			s.bypass = args[i+1]
		}
	}
	s.last = time.Now()
	s.mu.Unlock()
	m.mu.Unlock()
	output := filepath.Join(s.directory, "artifacts")
	if err := os.MkdirAll(output, 0700); err != nil {
		s.release()
		return nil, err
	}
	command, args := browserMCPCommand()
	owner, _ := json.Marshal(info)
	if err := os.WriteFile(filepath.Join(s.directory, "owner.json"), owner, 0600); err != nil {
		s.release()
		return nil, err
	}
	args = append(args, "--cdp-endpoint", fmt.Sprintf("http://127.0.0.1:%d", s.port), "--output-dir", output)
	client, err := mcp.NewStdioClient(ctx, config.Name, command, s.env, args...)
	if err != nil {
		s.release()
		return nil, err
	}
	return &managedBrowserClient{client: client, session: s}, nil
}

func browserScript(name string) string {
	if dir := os.Getenv("ARTEX_BROWSER_SCRIPTS"); dir != "" {
		return filepath.Join(dir, name)
	}
	return filepath.Join("scripts", name)
}

func browserMCPCommand() (string, []string) {
	return "node", []string{browserScript("browser-mcp.cjs")}
}

func (s *browserSession) ensure(ctx context.Context) error {
	s.mu.Lock()
	if s.deleted {
		s.mu.Unlock()
		return errors.New("삭제된 브라우저 세션입니다")
	}
	s.last = time.Now()
	if s.bridge != nil {
		select {
		case <-s.bridge.done:
			s.bridge = nil
			s.finishHumanLocked(errors.New(trMsg("브라우저 세션이 종료되었습니다")))
			if s.hasSlot {
				s.manager.releaseSlot()
				s.hasSlot = false
			}
		default:
			s.mu.Unlock()
			return nil
		}
	}
	config := &browserSession{directory: s.directory, port: s.port, proxy: s.proxy, bypass: s.bypass, env: s.env}
	needsSlot := s.manager != nil && !s.hasSlot
	s.mu.Unlock()
	// Callers serialize launches with op; waiting for capacity must not block the session list.
	if needsSlot {
		if err := s.manager.takeSlot(ctx); err != nil {
			return err
		}
		s.mu.Lock()
		if s.deleted {
			s.mu.Unlock()
			s.manager.releaseSlot()
			return errors.New("삭제된 브라우저 세션입니다")
		}
		s.hasSlot = true
		s.mu.Unlock()
	}
	b, err := startBrowserBridge(ctx, config)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		s.bridge = b
		go s.watchBridge(b)
	} else if s.hasSlot {
		s.manager.releaseSlot()
		s.hasSlot = false
	}
	return err
}

func (s *browserSession) watchBridge(b *browserBridge) {
	<-b.done
	s.mu.Lock()
	if s.bridge != b {
		s.mu.Unlock()
		return
	}
	s.bridge = nil
	hadSlot := s.hasSlot
	s.hasSlot = false
	s.finishHumanLocked(errors.New(trMsg("브라우저 세션이 종료되었습니다")))
	s.mu.Unlock()
	if hadSlot {
		s.manager.releaseSlot()
	}
}

func (s *browserSession) close() {
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	b := s.bridge
	hadSlot := s.hasSlot
	s.hasSlot = false
	s.bridge = nil
	s.finishHumanLocked(errors.New(trMsg("브라우저 세션이 종료되었습니다")))
	s.mu.Unlock()
	if b != nil {
		b.close()
	}
	if hadSlot {
		s.manager.releaseSlot()
	}
}

func (s *browserSession) release() {
	s.close()
	s.mu.Lock()
	s.active = false
	s.last = time.Now()
	s.mu.Unlock()
}

func (m *browserSessionManager) removeWhere(match func(agent.RunInfo) bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var removed []*browserSession
	for _, s := range m.sessions {
		if match(s.info) {
			s.mu.Lock()
			s.deleted = true
			s.mu.Unlock()
			removed = append(removed, s)
		}
	}
	var errs []error
	for _, s := range removed {
		s.close()
		if err := os.RemoveAll(s.directory); err != nil {
			errs = append(errs, err)
		} else {
			delete(m.sessions, s.id)
		}
	}
	// Profiles survive restart; deleted task credentials must not survive it.
	entries, err := os.ReadDir(m.dir)
	if err != nil && !os.IsNotExist(err) {
		errs = append(errs, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(m.dir, entry.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "owner.json"))
		if err != nil {
			continue
		}
		var owner agent.RunInfo
		if json.Unmarshal(raw, &owner) == nil && match(owner) {
			if err := os.RemoveAll(dir); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (m *browserSessionManager) removeTask(id int64) error {
	return m.removeWhere(func(info agent.RunInfo) bool { return info.TaskID == id })
}
func (m *browserSessionManager) removeConversation(id string) error {
	return m.removeWhere(func(info agent.RunInfo) bool { return info.SessionID == id })
}
func (m *browserSessionManager) drop(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if s == nil {
		return errors.New("브라우저 세션을 찾을 수 없습니다")
	}
	s.mu.Lock()
	if s.active {
		s.mu.Unlock()
		return errors.New("실행 중인 작업을 먼저 중지한 뒤 브라우저 세션을 삭제해 주세요")
	}
	s.deleted = true
	s.mu.Unlock()
	s.close()
	if err := os.RemoveAll(s.directory); err != nil {
		// Keep a closed tombstone visible so the administrator can retry cleanup.
		return err
	}
	delete(m.sessions, id)
	return nil
}

func (s *browserSession) beginHuman(reason string) *browserHumanWait {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.waiting == nil {
		s.waiting = &browserHumanWait{done: make(chan struct{})}
	}
	s.reason = reason
	s.last = time.Now()
	return s.waiting
}

func (s *browserSession) finishHumanLocked(err error) {
	if s.waiting != nil {
		s.waiting.err = err
		close(s.waiting.done)
		s.waiting = nil
	}
	s.reason = ""
}

func (s *browserSession) resume() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishHumanLocked(nil)
	s.last = time.Now()
}

func (s *browserSession) wait(ctx context.Context) error {
	s.mu.Lock()
	waiting := s.waiting
	s.mu.Unlock()
	return waiting.wait(ctx)
}

func (s *browserSession) view(ctx context.Context) browserSessionView {
	s.mu.Lock()
	v := browserSessionView{ID: s.id, TaskID: s.info.TaskID, IntentID: s.info.IntentID, ConversationID: s.info.SessionID, Agent: s.agent, State: "idle", Reason: s.reason, Viewport: map[string]int{"width": 1280, "height": 800}, Tabs: []map[string]any{}}
	if s.active {
		v.State = "active"
	}
	if s.waiting != nil {
		v.State = "waiting"
	}
	b := s.bridge
	s.mu.Unlock()
	if b != nil {
		var status browserSessionView
		if b.call(ctx, map[string]any{"action": "status"}, &status) == nil {
			v.URL = status.URL
			v.Title = status.Title
			v.Tabs = status.Tabs
			v.Viewport = status.Viewport
		}
	}
	return v
}

type managedBrowserClient struct {
	client  *mcp.Client
	session *browserSession
	once    sync.Once
}

func (c *managedBrowserClient) Close() error {
	c.once.Do(func() {
		_ = c.client.Close()

		c.session.release()
	})
	return nil
}
func (c *managedBrowserClient) Tools(ctx context.Context) ([]actool.CoreTool, error) {
	ts, err := c.client.Tools(ctx)
	if err != nil {
		return nil, err
	}
	for i, t := range ts {
		ts[i] = &managedBrowserTool{CoreTool: t, session: c.session}
	}
	ts = append(ts, c.session.handoffTool())
	return ts, nil
}

type managedBrowserTool struct {
	actool.CoreTool
	session *browserSession
}

func (t *managedBrowserTool) Call(ctx context.Context, input json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	for {
		if err := ctx.Err(); err != nil {
			return actool.Result{}, err
		}
		if err := t.session.wait(ctx); err != nil {
			return actool.Result{}, err
		}
		t.session.op.Lock()
		t.session.mu.Lock()
		waiting := t.session.waiting != nil
		t.session.mu.Unlock()
		if waiting {
			t.session.op.Unlock()
			continue
		}
		defer t.session.op.Unlock()
		if err := t.session.ensure(ctx); err != nil {
			return actool.Errorf(trMsg(err.Error())), nil
		}
		return t.CoreTool.Call(ctx, input, tc)
	}
}

func (s *browserSession) handoffTool() actool.CoreTool {
	return actool.Build(actool.Spec{
		Name:        "mcp__browser__browser_request_user",
		Description: trMsg("로그인, OTP 또는 CAPTCHA처럼 사람이 완료해야 하는 인증이 있으면 호출하세요. 같은 브라우저 화면을 ARTEX의 브라우저 세션 패널에서 사용자가 조작하고 인증 완료를 누를 때까지 기다립니다. 비밀번호나 OTP를 도구 인자로 요청하지 마세요. 대기 중에도 작업 시간 제한은 적용됩니다."),
		Schema:      map[string]any{"type": "object", "properties": map[string]any{"reason": map[string]any{"type": "string", "description": trMsg("사용자에게 필요한 인증 절차(비밀 값 제외)"), "maxLength": 500}}, "required": []string{"reason"}},
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Decision{Behavior: permission.Allow}
		},
		Run: func(ctx context.Context, input json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
			var args struct {
				Reason string `json:"reason"`
			}
			if json.Unmarshal(input, &args) != nil || len(args.Reason) > 2000 {
				return actool.Errorf(trMsg("인증 안내를 확인해 주세요")), nil
			}
			s.op.Lock()
			if err := s.ensure(ctx); err != nil {
				s.op.Unlock()
				return actool.Errorf(trMsg(err.Error())), nil
			}
			waiting := s.beginHuman(args.Reason)
			s.op.Unlock()
			if tc != nil && tc.Emit != nil {
				tc.Emit(actool.ProgressInfo{Message: trMsg("브라우저 인증을 기다립니다. 브라우저 세션 패널에서 인증을 완료해 주세요.")})
			}
			if err := waiting.wait(ctx); err != nil {
				return actool.Result{}, err
			}
			return actool.Text(trMsg("사용자가 브라우저 제어를 반환했습니다. 현재 페이지의 인증 상태를 확인하고 작업을 이어가세요.")), nil
		},
	})
}

type browserBridge struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	responses chan json.RawMessage
	done      chan struct{}
	serial    chan struct{}
	sequence  int
	once      sync.Once
}

func startBrowserBridge(ctx context.Context, s *browserSession) (*browserBridge, error) {
	options, _ := json.Marshal(map[string]any{"directory": s.directory, "port": s.port, "proxy": s.proxy, "bypass": s.bypass})
	cmd := exec.Command("node", browserScript("browser-session.cjs"))
	isolateBrowserProcess(cmd)
	cmd.Env = append(os.Environ(), "ARTEX_BROWSER_OPTIONS="+string(options))
	for key, value := range s.env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	b := &browserBridge{cmd: cmd, stdin: stdin, responses: make(chan json.RawMessage, 8), done: make(chan struct{}), serial: make(chan struct{}, 1)}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, errors.New("브라우저 런타임을 시작하지 못했습니다. Node와 Playwright 설치를 확인해 주세요")
	}
	go func() {
		defer close(b.done)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 16<<20)
		for scanner.Scan() {
			line := append(json.RawMessage(nil), scanner.Bytes()...)
			select {
			case b.responses <- line:
			default:
			}
		}
		_ = cmd.Wait()
	}()
	timer := time.NewTimer(40 * time.Second)
	defer timer.Stop()
	select {
	case raw := <-b.responses:
		var ready struct {
			Ready bool `json:"ready"`
		}
		if json.Unmarshal(raw, &ready) == nil && ready.Ready {
			return b, nil
		}
	case <-ctx.Done():
	case <-b.done:
	case <-timer.C:
	}
	b.close()
	return nil, errors.New("브라우저 시작에 실패했습니다. Chromium 설치, 세션 프로필 및 프록시 CA를 확인해 주세요")
}
func (b *browserBridge) call(ctx context.Context, input map[string]any, out any) error {
	select {
	case b.serial <- struct{}{}:
		defer func() { <-b.serial }()
	case <-ctx.Done():
		return ctx.Err()
	case <-b.done:
		return errors.New("브라우저 세션이 종료되었습니다")
	}
	b.sequence++
	input["id"] = b.sequence
	raw, _ := json.Marshal(input)
	if _, err := b.stdin.Write(append(raw, '\n')); err != nil {
		return errors.New("브라우저 세션이 종료되었습니다")
	}
	for {
		select {
		case raw := <-b.responses:
			var r struct {
				ID     int             `json:"id"`
				Error  string          `json:"error"`
				Result json.RawMessage `json:"result"`
			}
			if json.Unmarshal(raw, &r) != nil || r.ID != b.sequence {
				continue
			}
			if r.Error != "" {
				return errors.New(r.Error)
			}
			return json.Unmarshal(r.Result, out)
		case <-ctx.Done():
			return ctx.Err()
		case <-b.done:
			return errors.New("브라우저 세션이 종료되었습니다")
		}
	}
}
func (b *browserBridge) close() {
	b.once.Do(func() {
		_ = b.stdin.Close()
		select {
		case <-b.done:
		case <-time.After(5 * time.Second):
			killBrowserProcess(b.cmd.Process)
			<-b.done
		}
	})
}

func parseBrowserTaskFilter(value string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return n
}
