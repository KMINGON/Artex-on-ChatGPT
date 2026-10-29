package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/norma/transcript"
)

func TestCaptureOnceDoesNotTrustMarkers(t *testing.T) {
	for _, marker := range []string{"<persisted-output>", "bytes truncated]", "characters truncated]"} {
		for _, s := range []string{marker + strings.Repeat("x", 200000), strings.Repeat("x", 100000) + marker + strings.Repeat("x", 100000), `{"data":"` + marker + strings.Repeat("x", 200000) + `"}`} {
			got := CaptureOnce(&ToolContext{MaxOutputChars: 100}, s)
			if len(got) > 200 {
				t.Fatalf("marker bypassed cap: %d bytes", len(got))
			}
		}
	}
}

func TestMonitorUsesBashEnvironment(t *testing.T) {
	t.Setenv("NORMA_ENV_FIXTURE", "parent")
	t.Setenv("NORMA_INHERITED_FIXTURE", "inherited")
	m, err := NewManager("env-" + transcript.NewSessionID())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Cleanup()
	tc := &ToolContext{Tasks: m, WorkingDir: t.TempDir(), Env: []string{"NORMA_ENV_FIXTURE=override"}}
	in := json.RawMessage(`{"command":"printf '%s/%s' \"$NORMA_ENV_FIXTURE\" \"$NORMA_INHERITED_FIXTURE\""}`)
	bash, err := NewBash().Call(context.Background(), in, tc)
	if err != nil {
		t.Fatal(err)
	}
	res, err := NewMonitor().Call(context.Background(), in, tc)
	if err != nil || res.IsError {
		t.Fatalf("monitor: %v %s", err, res.Flatten())
	}
	list := m.List()
	if len(list) != 1 {
		t.Fatalf("tasks=%d", len(list))
	}
	if _, ok := m.Wait(context.Background(), list[0].ID, 3*time.Second); !ok {
		t.Fatal("monitor did not exit")
	}
	out, err := m.Output(list[0].ID, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bash.Flatten(), "override/inherited") || out != "override/inherited" {
		t.Fatalf("Bash=%q Monitor=%q", bash.Flatten(), out)
	}
}
