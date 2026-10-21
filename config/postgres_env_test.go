package config

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestPostgresComponentEnvPrecedenceAndEncoding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"database":{"dsn":"postgres://file/ignored"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARTEX_CONFIG", path)
	t.Setenv("ARTEX_PG_DSN", "")
	t.Setenv("ARTEX_PG_HOST", "::1")
	t.Setenv("ARTEX_PG_PORT", "5544")
	t.Setenv("ARTEX_PG_USER", "user:@/% ?한글")
	t.Setenv("ARTEX_PG_PASSWORD", " pa:ss@/?#%&한글 ")
	t.Setenv("ARTEX_PG_DATABASE", "db/one?#% 한글")
	t.Setenv("ARTEX_PG_SSLMODE", "require")
	dsn, _, err := PostgresDSN()
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	if u.Host != "[::1]:5544" || u.User.Username() != os.Getenv("ARTEX_PG_USER") || password != os.Getenv("ARTEX_PG_PASSWORD") || u.Path != "/"+os.Getenv("ARTEX_PG_DATABASE") || u.Query().Get("sslmode") != "require" || u.Fragment != "" {
		t.Fatal("component environment values did not round-trip")
	}
	t.Setenv("ARTEX_PG_DSN", "postgres://explicit/wins")
	if got, _, err := PostgresDSN(); err != nil || got != "postgres://explicit/wins" {
		t.Fatalf("DSN precedence: %q %v", got, err)
	}
}

func TestPostgresComponentEnvDefaultsAndInvalidConfig(t *testing.T) {
	t.Setenv("ARTEX_CONFIG", filepath.Join(t.TempDir(), "absent.json"))
	t.Setenv("ARTEX_PG_DSN", "")
	t.Setenv("ARTEX_PG_HOST", "postgres")
	t.Setenv("ARTEX_PG_USER", "artex")
	t.Setenv("ARTEX_PG_PASSWORD", "")
	t.Setenv("ARTEX_PG_DATABASE", "artex")
	t.Setenv("ARTEX_PG_PORT", "")
	t.Setenv("ARTEX_PG_SSLMODE", "")
	if got, _, err := PostgresDSN(); err != nil || got != "postgres://artex@postgres:5432/artex?sslmode=disable" {
		t.Fatalf("defaults: %q %v", got, err)
	}
	for _, tc := range []struct{ key, value string }{
		{"ARTEX_PG_PORT", "0"}, {"ARTEX_PG_PORT", "65536"}, {"ARTEX_PG_PORT", "bad"},
		{"ARTEX_PG_HOST", " "}, {"ARTEX_PG_HOST", "postgres:5432"}, {"ARTEX_PG_HOST", "host/path"},
		{"ARTEX_PG_USER", ""}, {"ARTEX_PG_DATABASE", ""},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, _, err := PostgresDSN(); err == nil {
				t.Fatal("invalid explicit component config fell through")
			}
		})
	}
}
