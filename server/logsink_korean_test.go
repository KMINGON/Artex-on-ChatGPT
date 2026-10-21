package server

import "testing"

func TestKoreanLogSeverityPreservesLegacy(t *testing.T) {
	for _, tc := range []struct{ line, level string }{
		{"[worker] 실행 실패: disconnected", "error"},
		{"[planner] 계획 오류: disconnected", "error"},
		{"[activity] 활동 기록 폐기", "error"},
		{"[seed] 대상 거부", "error"},
		{"[update] 부트스트랩 건너뜀", "warn"},
		{"[worker] 재시도 중", "warn"},
		{"[planner] 계획 완료", "info"},
		{"[worker] 执行失败", "error"},
		{"[worker] 重试", "warn"},
		{"[worker] request error", "error"},
	} {
		if got := parseLog(tc.line); got.Level != tc.level {
			t.Errorf("%q level=%s, want %s", tc.line, got.Level, tc.level)
		}
	}
}
