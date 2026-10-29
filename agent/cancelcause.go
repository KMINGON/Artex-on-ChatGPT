package agent

import (
	"context"
	"errors"
	"fmt"
)

// AbortCause names why an agent run's context was cancelled. Every cancellation
// site should attach one so the activity trace can report the real initiator.
type AbortCause struct {
	Code  string
	Short string
	Text  string
}

func (c *AbortCause) Error() string { return c.Text }

func cause(code, short, text string) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: text}
}

// Causef builds a cause that includes runtime-specific detail.
func Causef(code, short, format string, args ...any) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: fmt.Sprintf(format, args...)}
}

var (
	// Task-level execution context.
	AbortPausedByUser = cause("paused_by_user", "사용자가 작업을 일시 중지했습니다",
		"사용자가 작업 제어 API(POST /api/tasks/{id}/control, action=pause)를 통해 작업을 일시 중지했습니다. 이번 Planner/Worker 실행은 취소되며, 실행 중인 의도는 frontier(open)로 돌아가 재개 후 다시 할당됩니다. 선택된 세션의 저장 기록을 읽을 수 있으면 이전 맥락에서 이어가며, 기록이 없거나 연결 변경으로 새 세션을 선택하면 새 컨텍스트로 시작합니다. 연결 확인이나 기록 읽기 실패는 실행 오류로 처리합니다")
	AbortPausedByOrchestrator = cause("paused_by_orchestrator", "오케스트레이션 에이전트가 작업을 일시 중지했습니다",
		"오케스트레이션 에이전트가 pause_task 도구로 이 작업을 일시 중지했습니다. 이번 Planner/Worker 실행은 취소되며, 실행 중인 의도는 frontier(open)로 돌아가 재개 후 다시 실행됩니다")
	AbortTaskDeleted = cause("task_deleted", "작업이 삭제되었습니다",
		"작업을 삭제하고 있습니다(DELETE /api/tasks/{id}). 삭제 보호 절차가 이 작업에서 실행 중인 Planner, Worker, 메인 에이전트를 취소했으며, 이번 실행 결과는 더 이상 사용되지 않습니다")
	AbortPausedOnReload = cause("paused_on_reload", "서버가 작업의 일시 중지 상태를 복원했습니다",
		"서버 시작 시 DB에 저장된 작업의 일시 중지 상태를 복원했습니다. 이번 실행은 취소됩니다. 정상적인 복원 단계에는 실행 중인 에이전트가 없습니다")
	AbortGoalMet = cause("goal_met", "계획자가 작업 목표 달성으로 판단했습니다",
		"계획자가 작업 목표가 달성되었다고 판단하여 작업을 done으로 설정한 뒤, 아직 실행 중인 Worker를 취소했습니다. 해당 의도는 실패가 아닌 stopped로 표시됩니다")
	AbortSettleDrainTimeout = cause("settle_drain_timeout", "작업 시간 초과 후 마무리 대기 시간이 소진되었습니다",
		"작업이 timeout에 도달한 뒤 실행 중인 Worker의 정상 마무리를 기다렸으나 90초 drain 유예 시간이 부족하여 강제로 취소합니다. 의도는 exhausted로 표시되며, 마무리 단계에서 이미 저장한 사실과 자산은 유지됩니다")

	// Per-work context.
	AbortKilledByPlanner = cause("killed_by_planner", "계획자가 이 의도를 종료했습니다",
		"계획자가 kill_work를 호출하여 이 의도를 종료했습니다. 보통 방향이 어긋났거나 계속할 가치가 없다는 뜻입니다. 의도는 stopped로 표시되며 자동으로 다시 할당되지 않습니다")
	AbortWorkPausedByUser = cause("work_paused_by_user", "사용자가 이 Worker 의도를 일시 중지했습니다",
		"사용자가 실행 중인 Worker를 일시 중지했습니다. 이번 호출은 취소되고 의도는 paused로 전환됩니다. 이미 등록한 의도, 사실, 취약점, 활동 기록은 모두 유지됩니다. 재개 시 선택된 세션의 저장 기록을 읽을 수 있으면 이전 맥락에서 이어가며, 기록이 없거나 연결 변경으로 새 세션을 선택하면 새 컨텍스트로 시작합니다. 연결 확인이나 기록 읽기 실패는 실행 오류로 처리합니다")
	AbortWorkCancelledByUser = cause("work_cancelled_by_user", "사용자가 이 Worker 의도를 삭제했습니다",
		"사용자가 실행 중인 Worker를 삭제했습니다. 이번 호출은 취소됩니다. Worker가 쓰기 구간을 벗어나면 서버가 사용자가 선택한 삭제 방식으로 처리합니다. 논리 삭제는 삭제됨으로 표시하고 모든 산출물을 보존하며, 영구 삭제는 해당 의도와 이 의도에만 근거한 하위 노드를 함께 제거합니다")
	AbortWorkFinished = cause("work_finished", "Worker가 정상 종료되어 context를 해제했습니다",
		"Worker가 정상 종료되었으며 엔진이 detachWork에서 context 자원을 해제했습니다. 실행 중단이 아닙니다. 중단 메시지에 표시되었다면 취소와 종료 이벤트 사이에 경합이 발생한 것입니다")
	AbortPausedRaceGuard = cause("paused_race_guard", "작업 일시 중지 중 새 실행 시작을 거부했습니다",
		"작업이 일시 중지된 동안 엔진이 새 실행 context 발급을 거부했습니다. 할당과 일시 중지 사이의 경합으로 Worker가 시작되는 것을 방지하며, 이미 할당된 의도는 frontier로 돌아갑니다")

	// Main Agent and standalone conversation contexts.
	AbortChatStoppedByUser = cause("chat_stopped_by_user", "사용자가 이번 대화를 중지했습니다",
		"사용자가 중지 버튼으로 이번 메인 에이전트 또는 대화 에이전트 실행을 중단했습니다. 이미 생성된 활동 기록은 유지되며 다음 메시지를 보낼 수 있습니다")
	AbortChatPausedWithTask = cause("chat_paused_with_task", "작업 일시 중지와 함께 메인 에이전트 대화를 중단했습니다",
		"사용자가 작업을 일시 중지하면서 실행 중인 메인 에이전트 대화도 취소되었습니다. 이미 생성된 활동 기록은 유지되며, 작업 재개 후 이번 메시지를 자동으로 다시 실행하지 않습니다")
	AbortChatTurnFinished = cause("chat_turn_finished", "이번 대화가 정상 종료되어 context를 해제했습니다",
		"이번 대화가 정상 종료되어 서버가 해당 회차의 context 자원을 해제하고 있습니다. 실행 중단이 아닙니다. 중단 메시지에 표시되었다면 취소와 종료 이벤트 사이에 경합이 발생한 것입니다")

	// Process-level and per-run hard backstop.
	AbortShutdown = cause("shutdown", "서버 프로세스를 종료하고 있습니다",
		"서버 프로세스가 SIGINT 또는 SIGTERM을 받아 재시작, 업데이트 또는 종료 중입니다. 실행 중인 모든 에이전트가 취소됩니다. 재시작 후 남아 있는 running 의도는 open으로 초기화되어 다시 실행됩니다")
	AbortRunHardTimeout = cause("run_hard_timeout", "단일 실행의 강제 시간 제한이 발동했습니다",
		"단일 실행이 정상 실행 시간 예산과 추가 유예 시간을 초과했습니다. 모델 요청이나 도구가 오랫동안 응답하지 않아 회차 경계에서 정상 마무리를 수행할 수 없다는 뜻입니다. 중단 전 마지막으로 결과가 반환되지 않은 도구 호출을 우선 확인하세요")
)

// AbortReason resolves the named cause attached to a cancelled run context.
func AbortReason(ctx context.Context) (code, short, text string, ok bool) {
	c := context.Cause(ctx)
	if c == nil {
		return "", "", "", false
	}
	var ac *AbortCause
	if errors.As(c, &ac) {
		return ac.Code, ac.Short, ac.Text, true
	}
	switch {
	case errors.Is(c, context.DeadlineExceeded):
		return "deadline_exceeded", "상위 context가 deadline에 도달했습니다",
			"상위 context가 deadline에 도달했으나 설정 측에서 WithTimeoutCause로 명시적인 원인을 첨부하지 않았습니다: " + c.Error(), true
	case errors.Is(c, context.Canceled):
		return "canceled_no_cause", "취소한 측에서 명시적인 원인을 첨부하지 않았습니다",
			"상위 context가 취소되었으나 취소한 측에서 context.WithCancelCause로 명시적인 원인을 첨부하지 않았습니다. agent/cancelcause.go에 원인을 등록하고 해당 취소 지점에 연결하세요", true
	default:
		return "other", firstLine(c.Error(), 80), c.Error(), true
	}
}
