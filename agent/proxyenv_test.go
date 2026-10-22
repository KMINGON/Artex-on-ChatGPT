package agent

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/tool"
)

func TestProxyEnvOverridesInheritedSettings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("checks bash environment expansion")
	}
	keys := []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "NO_PROXY", "no_proxy", "NODE_USE_ENV_PROXY"}
	for _, key := range keys {
		t.Setenv(key, "inherited")
	}
	for _, proxy := range []string{"", "http://127.0.0.1:8788"} {
		t.Run(proxy, func(t *testing.T) {
			var command strings.Builder
			for _, key := range keys {
				command.WriteString("printf '%s=%s\\n' " + key + " \"$" + key + "\"\n")
			}
			raw, err := json.Marshal(map[string]any{"command": command.String()})
			if err != nil {
				t.Fatal(err)
			}
			res, err := tool.NewBash().Call(context.Background(), raw, &tool.ToolContext{Env: proxyEnv(proxy, "")})
			if err != nil || res.IsError {
				t.Fatalf("bash: %v; %s", err, res.Flatten())
			}
			values := map[string]string{}
			for _, line := range strings.Split(strings.TrimSpace(res.Flatten()), "\n") {
				key, value, _ := strings.Cut(line, "=")
				values[key] = value
			}
			for _, key := range keys {
				want := proxy
				switch key {
				case "NO_PROXY", "no_proxy":
					want = ""
				case "NODE_USE_ENV_PROXY":
					want = "0"
					if proxy != "" {
						want = "1"
					}
				}
				if got, ok := values[key]; !ok || got != want {
					t.Errorf("%s = %q, want %q (present=%v)", key, got, want, ok)
				}
			}
		})
	}
}

func TestProxyEnvSetsAllProxyForSocks5(t *testing.T) {
	// Capture-off egress path: a socks5 proxy, no MITM CA. ALL_PROXY must be set
	// (curl reads socks5 only from there), and no CA vars should appear.
	env := proxyEnv("socks5://10.0.0.1:1080", "")
	has := func(prefix string) bool {
		for _, e := range env {
			if strings.HasPrefix(e, prefix) {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"HTTP_PROXY=", "HTTPS_PROXY=", "ALL_PROXY=", "all_proxy="} {
		if !has(want) {
			t.Errorf("proxyEnv missing %s: %v", want, env)
		}
	}
	if has("SSL_CERT_FILE=") || has("CURL_CA_BUNDLE=") {
		t.Errorf("proxyEnv without CA must not inject CA vars: %v", env)
	}
}

func TestProxyEnvInjectsCAWhenRecording(t *testing.T) {
	env := proxyEnv("http://127.0.0.1:8788", "/data/ca.pem")
	has := func(prefix string) bool {
		for _, e := range env {
			if strings.HasPrefix(e, prefix) {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"SSL_CERT_FILE=", "CURL_CA_BUNDLE=", "REQUESTS_CA_BUNDLE=", "NODE_EXTRA_CA_CERTS="} {
		if !has(want) {
			t.Errorf("proxyEnv with CA missing %s: %v", want, env)
		}
	}
}
