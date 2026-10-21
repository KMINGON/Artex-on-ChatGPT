package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/chatgptauth"
	"github.com/Autumn-27/artex/chatgptllm"
	"github.com/Autumn-27/artex/db"
)

// The credential store belongs to the local OS user, outside the repository and
// database backups. Initialization is lazy so API-key-only installs need no store.
func (s *Server) chatGPTManager() (*chatgptauth.Manager, error) {
	s.chatGPTOnce.Do(func() {
		dir := s.chatGPTDir
		if dir == "" {
			dir, s.chatGPTErr = os.UserConfigDir()
			if s.chatGPTErr != nil {
				return
			}
			dir = filepath.Join(dir, "artex-on-chatgpt")
		}
		s.chatGPT, s.chatGPTErr = chatgptauth.New(dir)
		if s.chatGPTErr == nil && s.ctx != nil {
			go func() { <-s.ctx.Done(); _ = s.chatGPT.Close() }()
		}
	})
	return s.chatGPT, s.chatGPTErr
}

func (s *Server) configureChatGPT(cfg *agent.Config, p *db.LLMProfile) {
	cfg.AuthMode, cfg.ChatGPTAccountID = p.AuthMode, p.ChatGPTAccountID
	if p.AuthMode != "chatgpt_plan" {
		return
	}
	// Keep a signed-out profile selected. Requests fail closed instead of resolving
	// another profile or an environment API key when credentials are unavailable.
	cfg.ChatGPTAuth, _ = s.chatGPTManager()
	cfg.APIKey = ""
	cfg.MaxTokens = 0
}

func (s *Server) chatGPTProfileDTOs(ps []*db.LLMProfile) []LLMProfileDTO {
	out := llmProfileDTOs(ps)
	var state chatgptauth.State
	for i := range out {
		if out[i].AuthMode != "chatgpt_plan" {
			continue
		}
		if state.Accounts == nil {
			if m, err := s.chatGPTManager(); err == nil {
				state = m.Snapshot()
			}
		}
		out[i].Connected = false
		for _, a := range state.Accounts {
			if a.ID == out[i].ChatGPTAccountID {
				out[i].Connected = a.Connected && a.PlanEnabled
				out[i].PlanBlocked = a.PlanBlocked
				break
			}
		}
	}
	return out
}

func (s *Server) chatGPTStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	m, err := s.chatGPTManager()
	if err != nil {
		writeErr(w, 503, "ChatGPT 연결 저장소를 열 수 없습니다. 다른 Artex 프로세스가 실행 중인지 확인해 주세요")
		return
	}
	writeJSON(w, 200, m.Snapshot())
}

// The first release supports a browser and runtime on the same PC. OAuth itself
// returns to a separate ephemeral loopback listener, never an exposed app route.
func localChatGPTMutation(w http.ResponseWriter, r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		writeErr(w, 403, "ChatGPT 연결 관리는 Artex를 실행한 PC에서만 사용할 수 있습니다")
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
			writeErr(w, 403, "같은 Artex 화면에서 요청해 주세요")
			return false
		}
	}
	return true
}

func (s *Server) chatGPTLogin(w http.ResponseWriter, r *http.Request) {
	if !localChatGPTMutation(w, r) {
		return
	}
	var body struct {
		AccountID string `json:"account_id"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, "계정 요청이 올바르지 않습니다")
		return
	}
	m, err := s.chatGPTManager()
	if err != nil {
		writeErr(w, 503, "ChatGPT 연결 저장소를 열 수 없습니다")
		return
	}
	start, err := m.Start(r.Context(), body.AccountID)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, start)
}

func (s *Server) chatGPTLogout(w http.ResponseWriter, r *http.Request) {
	if !localChatGPTMutation(w, r) {
		return
	}
	var body struct {
		AccountID string `json:"account_id"`
	}
	if err := decode(r, &body); err != nil || body.AccountID == "" {
		writeErr(w, 400, "계정을 선택해 주세요")
		return
	}
	m, err := s.chatGPTManager()
	if err != nil {
		writeErr(w, 503, "ChatGPT 연결 저장소를 열 수 없습니다")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	err = m.Disconnect(ctx, body.AccountID)
	if errors.Is(err, chatgptauth.ErrRemoteRevocation) {
		writeJSON(w, 200, map[string]any{"ok": true, "warning": err.Error()})
		return
	}
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) chatGPTModels(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("account_id")
	if id == "" {
		writeErr(w, 400, "계정을 선택해 주세요")
		return
	}
	m, err := s.chatGPTManager()
	if err != nil {
		writeErr(w, 503, "ChatGPT 연결 저장소를 열 수 없습니다")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	client, err := agent.ChatGPTHTTPClient("")
	if err != nil {
		writeErr(w, 500, "연결을 준비할 수 없습니다")
		return
	}
	models, err := chatgptllm.Models(ctx, func(ctx context.Context) (context.Context, context.CancelFunc, string, error) {
		return m.AuthorizeCatalog(ctx, id)
	}, client)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"models": models})
}

// Rechecking is an explicit, single inference using the selected saved profile.
// A successful catalog request is not evidence that inference usage is available.
func (s *Server) chatGPTRecheck(w http.ResponseWriter, r *http.Request) {
	if !localChatGPTMutation(w, r) {
		return
	}
	var body struct {
		ProfileID int64 `json:"profile_id"`
	}
	if err := decode(r, &body); err != nil || body.ProfileID <= 0 {
		writeErr(w, 400, "재확인에 사용할 저장 프로필을 선택해 주세요")
		return
	}
	pg := s.pg(w)
	if pg == nil {
		return
	}
	p, err := pg.ProfileByID(body.ProfileID)
	if err != nil || p == nil || p.AuthMode != "chatgpt_plan" || p.UnsupportedReason() != "" {
		writeErr(w, 400, "사용 가능한 ChatGPT 저장 프로필을 선택해 주세요")
		return
	}
	cfg := s.configForProfile(p)
	m, err := s.chatGPTManager()
	if err != nil {
		writeErr(w, 503, "ChatGPT 연결 저장소를 열 수 없습니다")
		return
	}
	var reply string
	err = m.Recheck(r.Context(), p.ChatGPTAccountID, func(ctx context.Context) error {
		var probeErr error
		_, reply, probeErr = agent.TestConnection(ctx, cfg)
		return probeErr
	})
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "model": cfg.Model, "reply": truncateReply(reply)})
}
