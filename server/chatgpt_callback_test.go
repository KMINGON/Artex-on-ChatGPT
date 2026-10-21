package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIntegratedChatGPTMutationRequiresExactOrigin(t *testing.T) {
	t.Setenv(chatGPTCallbackEnv, "http://127.0.0.1:18787/auth/callback")
	for _, tc := range []struct {
		name, host, origin string
		allowed            bool
	}{
		{"docker bridge", "127.0.0.1:18787", "http://127.0.0.1:18787", true},
		{"missing origin", "127.0.0.1:18787", "", false},
		{"null origin", "127.0.0.1:18787", "null", false},
		{"other port", "127.0.0.1:8787", "http://127.0.0.1:8787", false},
		{"localhost alias", "localhost:18787", "http://localhost:18787", false},
		{"private host", "192.168.1.2:18787", "http://192.168.1.2:18787", false},
		{"external host", "evil.example", "http://evil.example", false},
		{"origin path", "127.0.0.1:18787", "http://127.0.0.1:18787/path", false},
		{"different scheme", "127.0.0.1:18787", "https://127.0.0.1:18787", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "http://"+tc.host+"/api/chatgpt/login", strings.NewReader(`{}`))
			r.RemoteAddr = "172.18.0.1:45000"
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("X-Forwarded-Host", "127.0.0.1:18787")
			r.Header.Set("X-Forwarded-For", "127.0.0.1")
			r.Header.Set("Forwarded", "for=127.0.0.1;host=127.0.0.1:18787;proto=http")
			w := httptest.NewRecorder()
			if got := localChatGPTMutation(w, r); got != tc.allowed {
				t.Fatalf("allowed=%v status=%d", got, w.Code)
			}
		})
	}
}

func TestInvalidChatGPTCallbackFailsClosed(t *testing.T) {
	for _, callback := range []string{
		" ", "http://localhost:8787/auth/callback", "https://127.0.0.1:8787/auth/callback",
		"http://127.0.0.1/auth/callback", "http://127.0.0.1:0/auth/callback",
		"http://127.0.0.1:08787/auth/callback", "http://127.0.0.1:65536/auth/callback",
		"http://user@127.0.0.1:8787/auth/callback", "http://127.0.0.1:8787/auth/callback?x=y",
		"http://127.0.0.1:8787/auth/callback#fragment", "http://192.168.1.2:8787/auth/callback",
	} {
		t.Run(callback, func(t *testing.T) {
			t.Setenv(chatGPTCallbackEnv, callback)
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/api/chatgpt/login", nil)
			r.RemoteAddr = "127.0.0.1:1234"
			r.Header.Set("Origin", "http://127.0.0.1:8787")
			w := httptest.NewRecorder()
			if localChatGPTMutation(w, r) || w.Code != http.StatusServiceUnavailable {
				t.Fatalf("invalid callback admitted: %d", w.Code)
			}
			s := &Server{chatGPTDir: t.TempDir()}
			if m, err := s.chatGPTManager(); err == nil {
				m.Close()
				t.Fatal("invalid callback opened a credential manager")
			}
		})
	}
}

func TestIntegratedChatGPTCallbackRouteIsPublicAndRequiresPendingLogin(t *testing.T) {
	t.Setenv(chatGPTCallbackEnv, "http://127.0.0.1:18787/auth/callback")
	s := &Server{chatGPTDir: t.TempDir(), jwtKey: []byte(strings.Repeat("k", 32))}
	t.Cleanup(func() {
		if s.chatGPT != nil {
			s.chatGPT.Close()
		}
	})
	h := s.Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18787/auth/callback?state=invalid&code=unused", nil))
	if w.Code != http.StatusNotFound || s.chatGPT == nil || !strings.Contains(w.Body.String(), "진행 중인 ChatGPT 로그인") {
		t.Fatalf("callback should reach the manager without JWT and reject missing state: %d %s", w.Code, w.Body)
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("callback response lacks privacy headers")
	}
	for _, action := range []string{"login", "logout", "recheck"} {
		for _, authenticated := range []bool{false, true} {
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:18787/api/chatgpt/"+action, strings.NewReader(`{}`))
			r.RemoteAddr = "172.18.0.1:45000"
			r.Header.Set("Origin", "http://127.0.0.1:18787")
			if authenticated {
				token, err := signJWT(s.signingKey(""))
				if err != nil {
					t.Fatal(err)
				}
				r.Header.Set("Authorization", "Bearer "+token)
				// Validate the access boundary without starting OAuth or inference.
				r.Header.Set("Origin", "https://evil.example")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := http.StatusUnauthorized
			if authenticated {
				want = http.StatusForbidden
			}
			if w.Code != want {
				t.Fatalf("%s authenticated=%v status=%d want=%d", action, authenticated, w.Code, want)
			}
		}
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://localhost:18787/auth/callback?state=invalid", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("wrong callback Host accepted: %d", w.Code)
	}
}

func TestDefaultChatGPTCallbackRouteDisabled(t *testing.T) {
	t.Setenv(chatGPTCallbackEnv, "")
	s := &Server{}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/auth/callback", nil))
	if w.Code != http.StatusNotFound || s.chatGPT != nil {
		t.Fatalf("default mode exposed an integrated callback: %d", w.Code)
	}
}
