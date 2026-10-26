package agent

import (
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestPlannerTriggerPreservesUnfinishedQuestionAtEndOfWorkerResult(t *testing.T) {
	_, _, tools, assigned, _ := graphToolFixture(t)
	if _, err := tools.ts.AppendActivity(db.Activity{NodeID: &assigned, Kind: "result", Detail: "이전 결과"}); err != nil {
		t.Fatal(err)
	}
	const remaining = "아직 저장 후 재표시를 검증하지 못했다. 공유 파일의 테스트 계정으로 이어서 확인해야 한다."
	complete := strings.Repeat("확인한 요청과 정상 대조 결과. ", 160) + "\n미검증 질문: " + remaining
	if _, err := tools.ts.AppendActivity(db.Activity{NodeID: &assigned, Kind: "result", Summary: "일부 검증 완료", Detail: complete}); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.ts.AppendActivity(db.Activity{NodeID: &assigned, Kind: "text", Detail: "결과 뒤의 상태 출력"}); err != nil {
		t.Fatal(err)
	}
	if got := workerOutput(tools.ts, assigned); got != complete {
		t.Fatal("latest complete result was replaced or truncated")
	}
	input := renderTriggers(tools.ts, []TriggerEvent{{Kind: "done", IntentID: assigned}})
	if !strings.Contains(input, complete) || !strings.Contains(input, remaining) {
		t.Fatal("Planner launch context lost the unfinished question at the end of the result")
	}
}

func TestRenderedExplorationDefaultsDoNotReintroducePrematureStopPolicies(t *testing.T) {
	previous := PromptOverride
	PromptOverride = nil
	t.Cleanup(func() { PromptOverride = previous })
	planner := plannerSystem("남은 질문을 검증한다", "/data", "/data/tasks/1")
	worker := workerSystem("http://proxy", "ca", "/data", "/data/tasks/1", "/data/tasks/1/i2")
	for _, stale := range []string{
		"같은 부정적 방향의 재검증은 최대 한 번",
		"확장 영향은 별도 의도로 검증하세요",
		"기본적으로 하나만 쓰고 detail에 합칠",
		"같은 URL을 curl로 반복 요청하지 마세요",
		"전제가 없어 막혔다면 같은 검증·상태 정리 의도를",
	} {
		if strings.Contains(planner+worker, stale) {
			t.Fatalf("rendered default retains a conflicting stop policy: %s", stale)
		}
	}
	if !strings.Contains(planner, "사전 삭제 보장") || !strings.Contains(worker, "정상 대조·재현성·입력·계정·상태 변화·오류 복구") {
		t.Fatal("effective prompts lost user-permitted preparation or repeat validation guidance")
	}
	for role, prompt := range map[string]string{"planner": planner, "worker": worker} {
		if !strings.Contains(prompt, "실제 활성 도구 목록") || !strings.Contains(prompt, "명시적") {
			t.Fatalf("%s lost the existing tool and explicit-scope contract", role)
		}
	}
}
