package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/Autumn-27/norma/permission"
)

// The interactive-shell tool family (opt-in; NOT in DefaultTools). ShellSessionTools
// returns all five. They require a task Manager on the ToolContext (tc.Tasks) — that is
// where the PTY sessions live and get cleaned up. See docs/交互式shell设计.md.

// ShellSessionTools returns the five interactive-shell tools. Callers add them to
// Options.Tools only when interactive shell is enabled for the agent.
func ShellSessionTools() []CoreTool {
	return []CoreTool{NewShellOpen(), NewShellSend(), NewShellRead(), NewShellClose(), NewShellList()}
}

func sessTool(name, desc string, schema map[string]any, readOnly bool, run func(context.Context, json.RawMessage, *ToolContext) (Result, error)) CoreTool {
	return Build(Spec{
		Name: name, Description: desc, Schema: schema,
		ReadOnly:   func(json.RawMessage) bool { return readOnly },
		Concurrent: func(json.RawMessage) bool { return false },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: run,
	})
}

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}
func strProp(d string) map[string]any  { return map[string]any{"type": "string", "description": d} }
func boolProp(d string) map[string]any { return map[string]any{"type": "boolean", "description": d} }
func intProp(d string) map[string]any  { return map[string]any{"type": "integer", "description": d} }
func arrStr(d string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": d}
}

func jsonResult(v any) (Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return Errorf("marshal: " + err.Error()), nil
	}
	return Text(string(b)), nil
}

func sessManager(tc *ToolContext) (*Manager, *Result) {
	if InteractiveShellDisabled() {
		r := Errorf("대화형 shell이 전역 설정으로 비활성화되어 있습니다(AGENT_CORE_DISABLE_INTERACTIVE_SHELL)")
		return nil, &r
	}
	if tc == nil || tc.Tasks == nil {
		r := Errorf("대화형 shell에는 백그라운드 작업 관리자가 필요합니다(활성화되지 않음)")
		return nil, &r
	}
	return tc.Tasks, nil
}

func ms(v int, def int) time.Duration {
	if v <= 0 {
		v = def
	}
	return time.Duration(v) * time.Millisecond
}

// NewShellOpen 开一个交互会话。
func NewShellOpen() CoreTool {
	desc := "지속되는 PTY 대화형 세션을 열어 대화형 입력이나 실제 TTY가 필요한 프로그램(msfconsole/대화형 ssh/mysql·python 등의 REPL/비밀번호 입력/nc 세션)을 실행하고 session_id를 반환합니다. **일회성 비대화형 명령은 Bash를 사용하세요.** mode: interactive=첫 프롬프트 또는 출력 중단까지 기다린 뒤 시작 출력을 반환; hands-free(기본값)=즉시 반환하고 이후 shell_read로 확인; dispatch=즉시 반환하고 완료 시 알림; monitor=즉시 반환하고 백그라운드에서 watch 키워드가 감지되면 알림(세션은 종료하지 않으며 이번 실행 동안만 유지). prompt_regex(예: 'msf6? >\\s*$')로 완료 여부를 정확히 판단할 수 있습니다. 사용 후 반드시 shell_close로 종료하세요."
	schema := obj(map[string]any{
		"command":          strProp("실행할 대화형 프로그램. 예: 'msfconsole -q' / 'ssh user@host' / 'python3'"),
		"mode":             strProp("interactive | hands-free(기본값) | dispatch | monitor"),
		"watch":            arrStr("monitor 모드 필수: 감시할 키워드/정규식 배열. 출력에서 하나라도 일치하면 알림(항목별 한 번만 알림)"),
		"prompt_regex":     strProp("선택: 명령 완료를 판단할 프롬프트 정규식(출력 끝부분에 적용)"),
		"rows":             intProp("선택: PTY 행 수(기본값 24)"),
		"cols":             intProp("선택: PTY 열 수(기본값 80)"),
		"quiet_ms":         intProp("선택: 출력이 멈춘 뒤 완료로 판단하기까지의 시간(ms, 기본값 8000)"),
		"startup_grace_ms": intProp("선택: 시작 유예 시간(ms). 이 동안은 출력 중단으로 완료를 판단하지 않음(기본값 15000)"),
	}, "command")
	return sessTool("shell_open", desc, schema, false, func(ctx context.Context, in json.RawMessage, tc *ToolContext) (Result, error) {
		m, errR := sessManager(tc)
		if errR != nil {
			return *errR, nil
		}
		var a struct {
			Command        string   `json:"command"`
			Mode           string   `json:"mode"`
			PromptRegex    string   `json:"prompt_regex"`
			Watch          []string `json:"watch"`
			Rows           int      `json:"rows"`
			Cols           int      `json:"cols"`
			QuietMs        int      `json:"quiet_ms"`
			StartupGraceMs int      `json:"startup_grace_ms"`
		}
		_ = json.Unmarshal(in, &a)
		if a.Command == "" {
			return Errorf("command는 필수입니다"), nil
		}
		mode := a.Mode
		if mode == "" {
			mode = "hands-free"
		}
		switch mode {
		case "hands-free", "interactive", "dispatch", "monitor":
		default:
			return Errorf("mode는 interactive | hands-free | dispatch | monitor 중 하나여야 합니다"), nil
		}
		var watch []*regexp.Regexp
		if mode == "monitor" {
			if len(a.Watch) == 0 {
				return Errorf("monitor 모드에는 watch(감시할 키워드/정규식)가 필요합니다"), nil
			}
			for _, w := range a.Watch {
				re, err := regexp.Compile(w)
				if err != nil {
					return Errorf("watch 정규식이 올바르지 않습니다 " + w + ": " + err.Error()), nil
				}
				watch = append(watch, re)
			}
		}
		var pr *regexp.Regexp
		if a.PromptRegex != "" {
			re, err := regexp.Compile(a.PromptRegex)
			if err != nil {
				return Errorf("prompt_regex가 올바르지 않습니다: " + err.Error()), nil
			}
			pr = re
		}
		workDir := ""
		var env []string
		if tc != nil {
			workDir, env = tc.WorkingDir, tc.Env
		}
		s, err := m.openSession(a.Command, workDir, env, a.Rows, a.Cols, mode, pr)
		if err != nil {
			return Errorf("세션을 열지 못했습니다: " + err.Error()), nil
		}
		quiet, grace := ms(a.QuietMs, sessDefQuietMs), ms(a.StartupGraceMs, sessDefGraceMs)
		switch mode {
		case "interactive":
			by := s.waitComplete(ctx, quiet, grace, ms(0, sessWaitCapMs), pr, "")
			out, cur, _ := s.readSince(0, 0, sessDefReadByte*4, false)
			return jsonResult(map[string]any{"session_id": s.id, "state": "running", "done_by": by, "output": out, "cursor": cur, "running": s.running()})
		case "dispatch":
			go m.dispatchWaiter(s, quiet, grace, ms(0, sessWaitCapMs))
			return jsonResult(map[string]any{"session_id": s.id, "state": "running", "dispatched": true})
		case "monitor":
			go m.monitorWaiter(s, watch)
			return jsonResult(map[string]any{"session_id": s.id, "state": "running", "monitoring": len(watch)})
		default: // hands-free
			return jsonResult(map[string]any{"session_id": s.id, "state": "running"})
		}
	})
}

// NewShellSend 向会话发送输入/按键。
func NewShellSend() CoreTool {
	desc := "대화형 세션에 입력을 보냅니다. text/submit/keys/hex/paste를 조합할 수 있으며 나열된 순서로 전송합니다(각 인자의 의미는 필드 설명 참조). wait=true(기본값)이면 명령이 완료될 때까지 기다린 뒤 새로 추가된 출력을 반환합니다."
	schema := obj(map[string]any{
		"session_id":   strProp("세션 ID(shell_open에서 반환)"),
		"text":         strProp("입력할 텍스트(자동 줄바꿈 없음)"),
		"submit":       boolProp("텍스트 뒤에 Enter를 추가하여 실행할지 여부"),
		"keys":         arrStr("키 이름: ctrl+c/ctrl+d/tab/enter/esc/up/down/left/right/..."),
		"hex":          arrStr("원시 바이트의 16진수 표기. 예: ['0x1b','0x5b','0x41']"),
		"paste":        strProp("여러 줄 붙여넣기(bracketed paste, 줄마다 실행되지 않음)"),
		"wait":         boolProp("명령이 완료될 때까지 동기적으로 기다릴지 여부(기본값 true)"),
		"prompt_regex": strProp("선택: 세션의 기본 완료 판단 프롬프트를 재정의"),
		"quiet_ms":     intProp("선택: 출력 중단 판단 기준 시간(ms, 기본값 8000)"),
		"timeout_ms":   intProp("선택: wait의 최대 대기 시간(ms, 기본값 120000)"),
	}, "session_id")
	return sessTool("shell_send", desc, schema, false, func(ctx context.Context, in json.RawMessage, tc *ToolContext) (Result, error) {
		m, errR := sessManager(tc)
		if errR != nil {
			return *errR, nil
		}
		var a struct {
			SessionID   string   `json:"session_id"`
			Text        string   `json:"text"`
			Paste       string   `json:"paste"`
			PromptRegex string   `json:"prompt_regex"`
			Submit      bool     `json:"submit"`
			Keys        []string `json:"keys"`
			Hex         []string `json:"hex"`
			Wait        *bool    `json:"wait"`
			QuietMs     int      `json:"quiet_ms"`
			TimeoutMs   int      `json:"timeout_ms"`
		}
		_ = json.Unmarshal(in, &a)
		s, ok := m.session(a.SessionID)
		if !ok {
			return Errorf("세션을 찾을 수 없습니다: " + a.SessionID), nil
		}
		if !s.running() {
			return Errorf("세션이 종료되었습니다(exit " + fmtExit(s.exit()) + "). shell_read로 마지막 출력을 읽거나 shell_close로 정리하세요"), nil
		}
		before := s.total
		// build & write input in order: text, submit(\r), keys, hex, paste
		var payload []byte
		if a.Text != "" {
			payload = append(payload, a.Text...)
		}
		if a.Submit {
			payload = append(payload, '\r')
		}
		for _, k := range a.Keys {
			if b, ok := keyToBytes(k); ok {
				payload = append(payload, b...)
			} else {
				return Errorf("알 수 없는 키: " + k), nil
			}
		}
		if len(a.Hex) > 0 {
			payload = append(payload, hexToBytes(a.Hex)...)
		}
		if a.Paste != "" {
			payload = append(payload, "\x1b[200~"...)
			payload = append(payload, a.Paste...)
			payload = append(payload, "\x1b[201~"...)
		}
		if len(payload) == 0 {
			return Errorf("전송할 입력이 없습니다(text/submit/keys/hex/paste 중 하나 이상 필요)"), nil
		}
		if _, err := s.ptmx.Write(payload); err != nil {
			return Errorf("쓰기에 실패했습니다: " + err.Error()), nil
		}
		wait := a.Wait == nil || *a.Wait
		if !wait {
			return jsonResult(map[string]any{"session_id": s.id, "sent": len(payload), "running": s.running()})
		}
		pr := s.prompt
		if a.PromptRegex != "" {
			if re, err := regexp.Compile(a.PromptRegex); err == nil {
				pr = re
			}
		}
		by := s.waitComplete(ctx, ms(a.QuietMs, sessDefQuietMs), 0, ms(a.TimeoutMs, sessWaitCapMs), pr, "")
		out, cur, omitted := s.readSince(before, 0, sessDefReadByte, false)
		return jsonResult(map[string]any{"session_id": s.id, "output": out, "done_by": by, "cursor": cur, "running": s.running(), "omitted": omitted})
	})
}

// NewShellRead 读输出：增量流 或 整屏快照。
func NewShellRead() CoreTool {
	desc := "입력을 보내지 않고 대화형 세션의 출력을 읽습니다. view=stream(기본값)은 새로 추가된 원시 출력 스트림(스트리밍에 권장, 이어 읽기 가능하며 drain으로 전체 출력 조회); view=screen은 전체 화면 VT 스냅샷(vim/htop 같은 전체 화면 프로그램에 적합)입니다."
	schema := obj(map[string]any{
		"session_id":   strProp("세션 ID"),
		"view":         strProp("stream(기본값) | screen"),
		"since_cursor": intProp("stream: 지정한 커서 이후부터 읽음. 생략하면 세션의 내부 커서에서 이어 읽음"),
		"max_lines":    intProp("stream: 반환할 최대 행 수(기본값 200)"),
		"max_bytes":    intProp("stream: 반환할 최대 바이트 수(기본값 8192)"),
		"drain":        boolProp("stream: true이면 마지막 조회 이후의 원시 출력 전체 반환(상한 무시)"),
	}, "session_id")
	// per-session read cursor kept here (tool-side) so unspecified reads continue.
	return sessTool("shell_read", desc, schema, true, func(_ context.Context, in json.RawMessage, tc *ToolContext) (Result, error) {
		m, errR := sessManager(tc)
		if errR != nil {
			return *errR, nil
		}
		var a struct {
			SessionID   string `json:"session_id"`
			View        string `json:"view"`
			SinceCursor *int64 `json:"since_cursor"`
			MaxLines    int    `json:"max_lines"`
			MaxBytes    int    `json:"max_bytes"`
			Drain       bool   `json:"drain"`
		}
		_ = json.Unmarshal(in, &a)
		s, ok := m.session(a.SessionID)
		if !ok {
			return Errorf("세션을 찾을 수 없습니다: " + a.SessionID), nil
		}
		if a.View == "screen" {
			grid, rows, cols, cx, cy := s.screen()
			return jsonResult(map[string]any{"view": "screen", "screen": grid, "rows": rows, "cols": cols,
				"cursor": map[string]int{"row": cy, "col": cx}, "running": s.running()})
		}
		var from int64
		if a.SinceCursor != nil {
			from = *a.SinceCursor
		} else {
			from = s.readCursor()
		}
		maxLines, maxBytes := a.MaxLines, a.MaxBytes
		if maxLines == 0 {
			maxLines = sessDefReadLine
		}
		if maxBytes == 0 {
			maxBytes = sessDefReadByte
		}
		out, cur, omitted := s.readSince(from, maxLines, maxBytes, a.Drain)
		s.setReadCursor(cur)
		res := map[string]any{"view": "stream", "output": out, "cursor": cur, "running": s.running(), "omitted": omitted}
		if ec := s.exit(); ec != nil {
			res["exit_code"] = *ec
		}
		return jsonResult(res)
	})
}

// NewShellClose 关闭会话。
func NewShellClose() CoreTool {
	schema := obj(map[string]any{"session_id": strProp("세션 ID")}, "session_id")
	return sessTool("shell_close", "대화형 세션을 닫습니다(프로세스 종료 및 PTY 해제).", schema, false, func(_ context.Context, in json.RawMessage, tc *ToolContext) (Result, error) {
		m, errR := sessManager(tc)
		if errR != nil {
			return *errR, nil
		}
		var a struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(in, &a)
		s, ok := m.session(a.SessionID)
		if !ok {
			return Errorf("세션을 찾을 수 없습니다: " + a.SessionID), nil
		}
		ec := s.exit()
		s.kill()
		m.sessMu.Lock()
		delete(m.sessions, a.SessionID)
		m.sessMu.Unlock()
		return jsonResult(map[string]any{"closed": a.SessionID, "exit_code": exitOrNil(ec)})
	})
}

// NewShellList 列出会话。
func NewShellList() CoreTool {
	return sessTool("shell_list", "현재 모든 대화형 세션과 상태를 조회합니다.", obj(map[string]any{}), true, func(_ context.Context, _ json.RawMessage, tc *ToolContext) (Result, error) {
		m, errR := sessManager(tc)
		if errR != nil {
			return *errR, nil
		}
		m.reapSessions()
		m.sessMu.Lock()
		defer m.sessMu.Unlock()
		out := make([]map[string]any, 0, len(m.sessions))
		for _, s := range m.sessions {
			s.mu.Lock()
			out = append(out, map[string]any{"session_id": s.id, "command": s.command, "state": s.state,
				"mode": s.mode, "idle_ms": time.Since(s.lastOut).Milliseconds()})
			s.mu.Unlock()
		}
		return jsonResult(map[string]any{"sessions": out})
	})
}

// --- small helpers ---

func fmtExit(ec *int) string {
	if ec == nil {
		return "?"
	}
	return fmt.Sprintf("%d", *ec)
}
func exitOrNil(ec *int) any {
	if ec == nil {
		return nil
	}
	return *ec
}
