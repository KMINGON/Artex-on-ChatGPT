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

func TestRenderedExplorationDefaultsFollowOriginalWorkflow(t *testing.T) {
	previous := PromptOverride
	PromptOverride = nil
	t.Cleanup(func() { PromptOverride = previous })
	planner := plannerSystem("남은 질문을 검증한다", "/data", "/data/tasks/1")
	worker := workerSystem("http://proxy", "ca", "/data", "/data/tasks/1", "/data/tasks/1/i2")
	for _, original := range []string{
		"같은 부정적 방향의 재검증은 최대 한 번",
		"기본적으로 하나만 쓰고 detail에 합칠",
		"같은 URL을 curl로 반복 요청하지 마세요",
		"done(정상 완료)",
		"직렬 익스플로잇 체인은 단계별 배정",
	} {
		if !strings.Contains(planner+worker, original) {
			t.Fatalf("rendered default lost original exploration guidance: %s", original)
		}
	}
	for _, removed := range []string{"새로 답할 질문의 가치로 우선순위", "같은 전제와 판단 기준을 공유하는 질문 하나", "DOM 실행·사용자 동작·브라우저 세션이 필요한 부분은 실제 브라우저로 검증", "브라우저 응답 관찰", "결과와 미검증 질문 확인 → 필요한 후속 의도 배정 → 목표 판단"} {
		if strings.Contains(planner+worker, removed) {
			t.Fatalf("rendered default retained additional exploration guidance: %s", removed)
		}
	}
}
