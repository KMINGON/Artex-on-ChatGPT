package config

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestStartupMessageSwitchesLanguage pins that the DB-config startup error and
// source labels follow ARTEX_LANG: Korean by default, English when configured.
func TestStartupMessageSwitchesLanguage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ARTEX_PG_DSN", "")
	t.Setenv("ARTEX_CONFIG", filepath.Join(dir, "nope.json"))

	// Default (no ARTEX_LANG) → Korean.
	t.Setenv("ARTEX_LANG", "")
	def := mustDSNErr(t)
	assertKorean(t, "default startup error", def)
	for _, id := range []string{"ARTEX_PG_DSN", "database", "dsn", "host/user/dbname"} {
		if !containsStr(def, id) {
			t.Errorf("default startup error must keep identifier %q: %q", id, def)
		}
	}

	// Explicit Korean → Korean.
	t.Setenv("ARTEX_LANG", "ko")
	assertKorean(t, "ko startup error", mustDSNErr(t))

	// English → no Hangul, keeps identifiers.
	t.Setenv("ARTEX_LANG", "en")
	en := mustDSNErr(t)
	if hasHangul(en) {
		t.Errorf("en startup error should have no Hangul: %q", en)
	}
	for _, id := range []string{"ARTEX_PG_DSN", "database", "dsn", "host/user/dbname"} {
		if !containsStr(en, id) {
			t.Errorf("en startup error must keep identifier %q: %q", id, en)
		}
	}

	// Removed display languages fall back to Korean without altering machine identifiers.
	for _, lang := range []string{"zh", "es", "ja"} {
		t.Setenv("ARTEX_LANG", lang)
		assertKorean(t, lang+" startup fallback", mustDSNErr(t))
	}
}

func mustDSNErr(t *testing.T) string {
	t.Helper()
	_, _, err := PostgresDSN()
	if err == nil {
		t.Fatal("missing config should error")
	}
	return err.Error()
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestCatalogFormatVerbsMatch pins that every translation keeps the Korean
// template's format verbs in the same order. Callers wrap only the template
// (log.Printf(T(ko), args...)), so a dropped or reordered verb would print
// %!s(MISSING) or swap arguments in the operator's console.
func TestCatalogFormatVerbsMatch(t *testing.T) {
	verbs := regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z%]`)
	for name, cat := range map[string]map[string]map[string]string{"cfgCatalog": cfgCatalog, "logCatalog": logCatalog} {
		for ko, tr := range cat {
			want := strings.Join(verbs.FindAllString(ko, -1), " ")
			for lang, v := range tr {
				if got := strings.Join(verbs.FindAllString(v, -1), " "); got != want {
					t.Errorf("%s[%q][%s] verbs = %q, want %q", name, ko, lang, got, want)
				}
			}
		}
	}
}
