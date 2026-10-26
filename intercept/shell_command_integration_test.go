package intercept_test

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/guard"
	"github.com/Autumn-27/artex/intercept"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestStoredShellRulesReachGuardWithoutBlockingWorkerFiles(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires explicitly configured isolated ARTEX_PG_DSN")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("artex_intercept_fixture_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE DATABASE "` + name + `"`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(`DROP DATABASE "` + name + `" WITH (FORCE)`); err != nil {
			t.Error(err)
		}
	}()
	u.Path = "/" + name
	d, err := db.Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ic := intercept.New(d)
	g := guard.NewWithInterceptor(ic)
	// Only execute the pre-tool hook: no shell or target request is executed.
	for _, tc := range []struct {
		tool, input string
		blocked     bool
	}{
		{"Write", `{"file_path":"probe.py","content":"print('halt', type(exc).__name__)"}`, false},
		{"Write", `{"file_path":"notes.md","content":"mkfs / reboot / rm -rf 명령은 실행하지 않았다"}`, false},
		{"Bash", `{"command":"printf ok","description":"shutdown 동작 설명"}`, false},
		{"Bash", `{"command":"halt"}`, true},
		{"shell_send", `{"text":"sudo reboot\n"}`, true},
		{"Bash", `{"command":"curl -X DELETE http://fixture.invalid/own-object"}`, true},
	} {
		blocked, message, _ := g.Hooks().PreToolUse(t.Context(), tc.tool, []byte(tc.input))
		if blocked != tc.blocked {
			t.Fatalf("%s pre-tool hook blocked=%v, want %v; message=%s", tc.tool, blocked, tc.blocked, message)
		}
		if blocked && !strings.Contains(message, "ARTEX 플랫폼 정책") {
			t.Fatalf("blocked call lost platform attribution: %s", message)
		}
	}
	var count int
	if err := d.QueryRow(`SELECT count(*) FROM intercept_pending WHERE tool_name='Bash' AND status='denied' AND tool_input->>'command'='halt'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("original call input not preserved in history: count=%d err=%v", count, err)
	}
	if _, err := d.CreateInterceptRule("사용자 전체 입력 검사", "tool_input", "string", "halt", "deny", "사용자 규칙", 200, true, false, 60, "deny"); err != nil {
		t.Fatal(err)
	}
	ic.Invalidate()
	blocked, _, _ := g.Hooks().PreToolUse(t.Context(), "Write", []byte(`{"file_path":"probe.py","content":"print('halt')"}`))
	if !blocked {
		t.Fatal("custom full-input policy lost")
	}
}
