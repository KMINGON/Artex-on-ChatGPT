package tool

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWebFetchIgnoresInheritedProxy(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "direct target")
	}))
	defer target.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "unexpected inherited proxy")
	}))
	defer proxy.Close()
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(key, proxy.URL)
	}
	// Force the default transport to proxy loopback too; ProxyFromEnvironment
	// otherwise exempts httptest targets and caches environment across tests.
	original := http.DefaultTransport
	poisoned := original.(*http.Transport).Clone()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	poisoned.Proxy = http.ProxyURL(proxyURL)
	http.DefaultTransport = poisoned
	t.Cleanup(func() {
		http.DefaultTransport = original
		poisoned.CloseIdleConnections()
	})
	for _, cfg := range []WebFetchConfig{{}, {Proxy: "http://127.0.0.1:1", RecordingProxy: true}} {
		raw, _ := json.Marshal(map[string]any{"url": target.URL})
		res, err := NewWebFetch(cfg).Call(context.Background(), raw, &ToolContext{})
		if err != nil || res.IsError || !strings.Contains(res.Flatten(), "direct target") {
			t.Fatalf("proxy %q: err=%v, result=%q", cfg.Proxy, err, res.Flatten())
		}
	}
}

func TestWebFetchExplicitProxyIgnoresInheritedBypass(t *testing.T) {
	t.Setenv("NO_PROXY", "*")
	t.Setenv("no_proxy", "*")
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != "target.invalid" {
			t.Errorf("proxy target = %q", r.URL.Host)
		}
		io.WriteString(w, "captured via configured proxy")
	}))
	defer proxy.Close()
	raw := json.RawMessage(`{"url":"http://target.invalid/"}`)
	res, err := NewWebFetch(WebFetchConfig{Proxy: proxy.URL}).Call(context.Background(), raw, &ToolContext{})
	if err != nil || res.IsError || !strings.Contains(res.Flatten(), "captured via configured proxy") {
		t.Fatalf("err=%v, result=%q", err, res.Flatten())
	}
}

func TestWebFetchReportsIncompleteBody(t *testing.T) {
	for _, body := range []string{"", "일부 응답"} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.Header().Set("Content-Length", "100")
				w.WriteHeader(http.StatusOK)
				io.WriteString(w, body)
			}))
			defer srv.Close()
			raw, _ := json.Marshal(map[string]any{"url": srv.URL})
			res, err := NewWebFetch(WebFetchConfig{}).Call(context.Background(), raw, &ToolContext{})
			if err != nil || !res.IsError || !strings.Contains(res.Flatten(), "본문 수신 실패") || !strings.Contains(res.Flatten(), "unexpected EOF") {
				t.Fatalf("err=%v, result=%q, isError=%v", err, res.Flatten(), res.IsError)
			}
			if !strings.HasSuffix(res.Flatten(), body) {
				t.Fatalf("partial body lost: %q", res.Flatten())
			}
		})
	}
}

func TestWebFetchBodyAndDisplayLimits(t *testing.T) {
	for _, tc := range []struct {
		name        string
		body        string
		contentType string
		bodyCut     bool
		displayCut  bool
	}{
		{name: "exact body limit", body: "<!--" + strings.Repeat("x", maxFetchBytes-7) + "-->", contentType: "text/html"},
		{name: "body over limit", body: "<!--" + strings.Repeat("x", maxFetchBytes) + "-->", contentType: "text/html", bodyCut: true},
		{name: "exact display limit", body: strings.Repeat("한", 16666) + "ab", contentType: "text/plain"},
		{name: "Korean display limit", body: "처음" + strings.Repeat("한글", 30000) + "끝", contentType: "text/plain", displayCut: true},
		{name: "Korean body limit", body: strings.Repeat("한", maxFetchBytes/3+1), contentType: "text/plain", bodyCut: true, displayCut: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.Header().Set("Content-Length", strconv.Itoa(len(tc.body)))
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			raw, _ := json.Marshal(map[string]any{"url": srv.URL})
			res, err := NewWebFetch(WebFetchConfig{}).Call(context.Background(), raw, &ToolContext{})
			out := res.Flatten()
			if err != nil || res.IsError {
				t.Fatalf("err=%v, result=%q", err, out)
			}
			if strings.Contains(out, "본문 수신 제한") != tc.bodyCut || strings.Contains(out, "출력 표시 제한") != tc.displayCut {
				t.Fatalf("wrong truncation notices: %.300s", out)
			}
			if !utf8.ValidString(out) {
				t.Fatal("output splits a UTF-8 character")
			}
			if strings.ContainsRune(out, utf8.RuneError) {
				t.Fatal("truncation introduced a replacement character")
			}
			if len(out) > 51000 {
				t.Fatalf("output exceeds display budget: %d bytes", len(out))
			}
			if tc.name == "Korean display limit" && (!strings.Contains(out, "처음") || !strings.HasSuffix(out, "끝")) {
				t.Fatal("display cap must preserve head and tail")
			}
		})
	}
}

func TestWebFetchRecordingFallbackPreservesExternalProxy(t *testing.T) {
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != "target.invalid" {
			t.Errorf("target = %q", r.URL.Host)
		}
		io.WriteString(w, "external egress preserved")
	}))
	defer external.Close()
	res, err := NewWebFetch(WebFetchConfig{
		Proxy: "http://127.0.0.1:1", RecordingProxy: true, FallbackProxy: external.URL,
	}).Call(context.Background(), json.RawMessage(`{"url":"http://target.invalid/"}`), &ToolContext{})
	if err != nil || res.IsError || !strings.Contains(res.Flatten(), "external egress preserved") || !strings.Contains(res.Flatten(), "트래픽 기록 누락") {
		t.Fatalf("err=%v result=%q", err, res.Flatten())
	}
}

func TestWebFetchNeverBypassesOrdinaryProxy(t *testing.T) {
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		io.WriteString(w, "must not reach directly")
	}))
	defer target.Close()
	raw, _ := json.Marshal(map[string]string{"url": target.URL})
	for _, proxy := range []string{"http://127.0.0.1:1", "://invalid", "http://", "file:///tmp/proxy"} {
		res, err := NewWebFetch(WebFetchConfig{Proxy: proxy}).Call(context.Background(), raw, &ToolContext{})
		if err != nil || !res.IsError || called {
			t.Fatalf("proxy bypassed: called=%v err=%v result=%q", called, err, res.Flatten())
		}
	}
}
