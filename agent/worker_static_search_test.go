package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/guard"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/hook"
	"github.com/Autumn-27/norma/llm"
)

func TestWorkerStaticSearchKeepsDeletePathTextAsData(t *testing.T) {
	t.Setenv("NORMA_DISABLE_RIPGREP", "1")
	_, task, tools, assigned, _ := graphToolFixture(t)
	intent, err := tools.ts.GetNode(assigned)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	runDir := ensureRunDir(dir, task.ID, assigned)
	const snippet = "const endpoint = '/rest/user/delete';"
	source := strings.Repeat("x", 600_000) + snippet + strings.Repeat("y", 600_000)
	path := filepath.Join(runDir, "main.js")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	search, _ := json.Marshal(map[string]any{"path": "main.js", "pattern": snippet, "output_mode": "content", "only_matching": true})
	fact, _ := json.Marshal(map[string]any{"summary": "로컬 JavaScript에서 삭제 경로 문자열 확인", "detail": path + "\n" + snippet, "confidence": "observed"})
	steps := []struct{ name, input string }{{"Grep", string(search)}, {"record_fact", string(fact)}}
	calls := 0
	// 고정된 도구 호출만 반환하는 로컬 fixture이며 외부 모델을 호출하지 않는다.
	provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		step := calls
		calls++
		if step >= len(steps) {
			yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "end_turn"}, nil)
			return
		}
		if !yield(llm.StreamEvent{Type: llm.SEToolUseStart, ToolID: steps[step].name, ToolName: steps[step].name}, nil) {
			return
		}
		if !yield(llm.StreamEvent{Type: llm.SEToolInputJSON, Text: steps[step].input}, nil) {
			return
		}
		yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "tool_use"}, nil)
	}}
	g := guard.New()
	g.Hooks().On(hook.PreToolUse, func(_ context.Context, event hook.Event) hook.Result {
		if event.ToolName != "Grep" && event.ToolName != "record_fact" {
			return hook.Result{Decision: "block", Message: "로컬 검색 fixture에서 허용하지 않은 도구"}
		}
		return hook.Result{}
	})
	worker := NewWorker(provider, "fixture", dir, nil, 0, 4)
	var searchResult *db.Activity
	reason, counts, err := worker.Execute(t.Context(), "worker-search", task.ID, nil, tools.ts, intent, g.Hooks(), func(activity db.Activity) {
		if activity.Kind == "tool_result" && activity.Tool == "Grep" {
			searchResult = &activity
		}
	}, nil, nil)
	if err != nil || reason != harness.ReasonCompleted {
		t.Fatalf("Worker 검색 실행 실패: reason=%s err=%v", reason, err)
	}
	if searchResult == nil || searchResult.IsError || searchResult.Detail != "main.js:1:"+snippet+"\n" {
		t.Fatalf("정적 검색 결과가 전달되지 않음: %+v", searchResult)
	}
	if counts.Facts != 1 || calls != 3 {
		t.Fatalf("검색 후 기록 흐름 변경: writes=%+v calls=%d", counts, calls)
	}
	outputs, err := tools.ts.OutcomeIDs(assigned)
	if err != nil || len(outputs) != 1 {
		t.Fatalf("검색 사실과 의도 연결 누락: %v %v", outputs, err)
	}
	stored, err := tools.ts.GetNode(outputs[0])
	if err != nil || stored == nil || stored.Kind != db.KindFact || !strings.Contains(string(stored.Payload), "/rest/user/delete") {
		t.Fatalf("후속 조회에서 검색 근거 누락: %+v %v", stored, err)
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != source {
		t.Fatalf("검색이 원본 파일을 변경함: %q %v", after, err)
	}
}
