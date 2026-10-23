package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"sort"
	"time"
)

func (s *Server) registerBrowserRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/browser/sessions", s.listBrowserSessions)
	mux.HandleFunc("POST /api/browser/sessions/{id}/takeover", s.takeoverBrowserSession)
	mux.HandleFunc("GET /api/browser/sessions/{id}/screenshot", s.browserScreenshot)
	mux.HandleFunc("POST /api/browser/sessions/{id}/action", s.browserAction)
	mux.HandleFunc("POST /api/browser/sessions/{id}/resume", s.resumeBrowserSession)
	mux.HandleFunc("DELETE /api/browser/sessions/{id}", s.deleteBrowserSession)
}

func (s *Server) deleteBrowserSession(w http.ResponseWriter, r *http.Request) {
	session := s.browserFromRequest(w, r)
	if session == nil {
		return
	}
	if err := s.browsers.drop(session.id); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"deleted": true})
}

func (s *Server) browserFromRequest(w http.ResponseWriter, r *http.Request) *browserSession {
	if s.browsers == nil {
		writeErr(w, 503, "브라우저 세션을 사용할 수 없습니다")
		return nil
	}
	s.browsers.mu.Lock()
	session := s.browsers.sessions[r.PathValue("id")]
	s.browsers.mu.Unlock()
	if session == nil {
		writeErr(w, 404, "브라우저 세션을 찾을 수 없습니다")
	}
	return session
}

func (s *Server) listBrowserSessions(w http.ResponseWriter, r *http.Request) {
	views := []browserSessionView{}
	if s.browsers != nil {
		for _, session := range s.browsers.list() {
			if task := r.URL.Query().Get("task_id"); task != "" && session.info.TaskID != parseBrowserTaskFilter(task) {
				continue
			}
			if conv := r.URL.Query().Get("conversation_id"); conv != "" && session.info.SessionID != conv {
				continue
			}
			ctx, cancel := context.WithTimeout(r.Context(), 1500*time.Millisecond)
			views = append(views, session.view(ctx))
			cancel()
		}
	}
	sort.Slice(views, func(i, j int) bool { return views[i].ID < views[j].ID })
	writeJSON(w, 200, map[string]any{"sessions": views})
}

func (s *Server) takeoverBrowserSession(w http.ResponseWriter, r *http.Request) {
	session := s.browserFromRequest(w, r)
	if session == nil {
		return
	}
	session.op.Lock()
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	err := session.ensure(ctx)
	if err == nil {
		session.beginHuman(trMsg("사용자가 브라우저를 직접 조작하고 있습니다"))
	}
	session.op.Unlock()
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, session.view(r.Context()))
}

func (s *Server) resumeBrowserSession(w http.ResponseWriter, r *http.Request) {
	session := s.browserFromRequest(w, r)
	if session == nil {
		return
	}
	session.resume()
	writeJSON(w, 200, session.view(r.Context()))
}

func (s *Server) browserScreenshot(w http.ResponseWriter, r *http.Request) {
	session := s.browserFromRequest(w, r)
	if session == nil {
		return
	}
	session.mu.Lock()
	bridge := session.bridge
	session.mu.Unlock()
	if bridge == nil {
		writeErr(w, 409, "브라우저 제어를 시작한 뒤 화면을 확인해 주세요")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var result struct {
		Image string `json:"image"`
	}
	if err := bridge.call(ctx, map[string]any{"action": "screenshot"}, &result); err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	data, err := base64.StdEncoding.DecodeString(result.Image)
	if err != nil || len(data) > 10<<20 {
		writeErr(w, 502, "브라우저 화면을 읽지 못했습니다")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

func validateBrowserAction(input map[string]any) error {
	action, _ := input["action"].(string)
	coordinate := func(key string, min, max float64) bool {
		v, ok := input[key].(float64)
		return ok && v >= min && v <= max
	}
	valid := false
	switch action {
	case "click":
		valid = coordinate("x", 0, 1280) && coordinate("y", 0, 800)
	case "drag":
		valid = coordinate("x", 0, 1280) && coordinate("y", 0, 800) && coordinate("to_x", 0, 1280) && coordinate("to_y", 0, 800)
	case "scroll":
		valid = coordinate("x", -5000, 5000) && coordinate("y", -5000, 5000)
	case "type":
		text, ok := input["text"].(string)
		valid = ok && len(text) > 0 && len(text) <= 16384
	case "key":
		key, _ := input["key"].(string)
		for _, allowed := range []string{"Enter", "Tab", "Shift+Tab", "Backspace", "Delete", "Escape", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Home", "End", "PageUp", "PageDown", "ControlOrMeta+A", "Space"} {
			if key == allowed {
				valid = true
			}
		}
	case "select_tab":
		index, _ := input["index"].(float64)
		valid = coordinate("index", 0, 100) && math.Trunc(index) == index
	case "navigate":
		raw, _ := input["url"].(string)
		parsed, err := url.Parse(raw)
		valid = err == nil && len(raw) <= 8192 && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil
	}
	if !valid {
		return errors.New("브라우저 동작 또는 입력값을 확인해 주세요")
	}
	// The bridge accepts only this validated action, never caller-supplied RPC ids.
	delete(input, "id")
	return nil
}

func (s *Server) browserAction(w http.ResponseWriter, r *http.Request) {
	session := s.browserFromRequest(w, r)
	if session == nil {
		return
	}
	var input map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768)).Decode(&input); err != nil {
		writeErr(w, 400, "브라우저 입력을 읽지 못했습니다")
		return
	}
	if err := validateBrowserAction(input); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	session.op.Lock()
	defer session.op.Unlock()
	session.mu.Lock()
	waiting, bridge := session.waiting != nil, session.bridge
	session.last = time.Now()
	session.mu.Unlock()
	if !waiting || bridge == nil {
		writeErr(w, 409, "먼저 브라우저 제어를 시작해 주세요")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	var result json.RawMessage
	if err := bridge.call(ctx, input, &result); err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	// Typed credentials stay in the browser; neither the response nor the tool transcript echoes input.
	writeJSON(w, 200, map[string]any{"ok": true, "session": session.view(ctx)})
}
