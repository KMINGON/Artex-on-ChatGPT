package server

import (
	"fmt"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func taskReport(s *Server, taskID string) (int, string) {
	rec := httptest.NewRecorder()
	s.getReport(rec, httptest.NewRequest("GET", "/api/report?task="+taskID, nil))
	return rec.Code, rec.Body.String()
}

// A task report describes its own task: every finding and only that task's
// assets. A failed query must not be rendered as an empty result.
func TestTaskReportCoversOwnTaskAndSurfacesQueryFailure(t *testing.T) {
	m := stateGraphManager(t)
	s := newAdmissionTestServer(m, nil)
	stamp := time.Now().UnixNano()
	newTask := func(name string, endpoints int) *Task {
		t.Helper()
		task, err := m.CreateTask(fmt.Sprintf("report %s %d", name, stamp), "", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { m.DeleteTask(task.ID, DeleteTaskOptions{DeleteAssets: true}) })
		id, _ := strconv.ParseInt(task.ID, 10, 64)
		for i := range endpoints {
			url := fmt.Sprintf("https://report-%s-%d.invalid/item/%d", name, stamp, i)
			if _, err := m.Assets().UpsertEndpoint(db.UpsertEndpointReq{URL: url, Method: "GET", TaskID: id}); err != nil {
				t.Fatal(err)
			}
		}
		return task
	}
	first, second, empty := newTask("a", 2), newTask("b", 3), newTask("c", 0)

	t.Run("assets of another task are not counted", func(t *testing.T) {
		// Registering an endpoint also registers its root domain for the task.
		for task, want := range map[*Task]string{first: "- 자산: endpoint 2, root_domain 1\n", second: "- 자산: endpoint 3, root_domain 1\n"} {
			code, body := taskReport(s, task.ID)
			if code != 200 || !strings.Contains(body, want) {
				t.Fatalf("task %s HTTP %d want %q\n%s", task.ID, code, want, body)
			}
		}
		if got, want := s.fallbackChat(second, ""), fmt.Sprintf(fallbackChatStatus, 3+1, 0, 0); got != want {
			t.Fatalf("rule-mode status=%q want %q", got, want)
		}
	})

	t.Run("finding 1001 is reported", func(t *testing.T) {
		const total = 1001
		for i := range total {
			payload := map[string]any{"name": fmt.Sprintf("finding-%04d", i), "severity": "low", "summary": "s"}
			if _, err := first.Store.AddNode(db.KindFinding, payload, 5, "confirmed", "worker", nil); err != nil {
				t.Fatal(err)
			}
		}
		code, body := taskReport(s, first.ID)
		if code != 200 {
			t.Fatalf("HTTP %d", code)
		}
		if !strings.Contains(body, fmt.Sprintf("확인된 취약점: **%d**건", total)) {
			t.Fatalf("summary does not count %d findings: %s", total, strings.SplitN(body, "## 취약점", 2)[0])
		}
		// The oldest finding is the one a newest-first cap drops.
		if got := strings.Count(body, "finding-"); got != total || !strings.Contains(body, "finding-0000") {
			t.Fatalf("rendered %d findings, oldest present=%v", got, strings.Contains(body, "finding-0000"))
		}
		if got, want := s.fallbackChat(first, ""), fmt.Sprintf(fallbackChatStatus, 2+1, 0, total); got != want {
			t.Fatalf("rule-mode status=%q want %q", got, want)
		}
	})

	t.Run("empty task is a successful empty report", func(t *testing.T) {
		code, body := taskReport(s, empty.ID)
		if code != 200 || !strings.Contains(body, "확인된 취약점: **0**건") || !strings.Contains(body, "_이번에 확인된 취약점이 없습니다._") {
			t.Fatalf("HTTP %d\n%s", code, body)
		}
		if !strings.Contains(body, "- 자산: 없음\n") {
			t.Fatalf("empty task asset summary:\n%s", body)
		}
	})

	t.Run("query failure is not an empty report", func(t *testing.T) {
		closed, err := db.Open(os.Getenv("ARTEX_PG_DSN"))
		if err != nil {
			t.Fatal(err)
		}
		closed.Close()

		const statusFailed = "작업 현황 조회 실패: "
		store := empty.Store
		empty.Store = closed.Exploration(empty.ExpID)
		code, body := taskReport(s, empty.ID)
		status := s.fallbackChat(empty, "")
		empty.Store = store
		if code != 500 {
			t.Fatalf("finding query failure: HTTP %d\n%s", code, body)
		}
		if !strings.HasPrefix(status, statusFailed) {
			t.Fatalf("finding query failure status=%q", status)
		}

		assets := m.assets
		m.assets = closed.Assets()
		code, body = taskReport(s, empty.ID)
		status = s.fallbackChat(empty, "")
		m.assets = assets
		if code != 500 {
			t.Fatalf("asset query failure: HTTP %d\n%s", code, body)
		}
		if !strings.HasPrefix(status, statusFailed) {
			t.Fatalf("asset query failure status=%q", status)
		}
	})
}
