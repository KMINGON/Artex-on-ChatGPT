package agent

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// constraintBlock renders this task's operation constraints (task_constraints) as a
// high-priority block appended to the planner/worker system prompt. allow/deny are
// grouped; empty string when there are no constraints (or ts is nil). The framing
// deliberately puts these ABOVE the exploration/expansion heuristics so a declared
// boundary wins the tug-of-war against "chase another entry surface".
func constraintBlock(ts *db.ExplorationStore) string {
	if ts == nil {
		return ""
	}
	rows, err := ts.ListConstraints()
	if err != nil || len(rows) == 0 {
		return ""
	}
	var allow, deny []string
	for _, c := range rows {
		text := strings.TrimSpace(c.Text)
		if text == "" {
			continue
		}
		if c.Kind == "allow" {
			allow = append(allow, "- "+text)
		} else {
			deny = append(deny, "- "+text)
		}
	}
	if len(allow) == 0 && len(deny) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【작업 제약(최우선이며 아래 모든 탐색/범위 확장 지침보다 우선합니다. 의도를 생성하거나 동작을 실행하기 전에 위반 여부를 반드시 확인하고, 위반하는 경우 진행하지 마세요)】: ")
	if len(allow) > 0 {
		b.WriteString("\n허용된 작업:\n")
		b.WriteString(strings.Join(allow, "\n"))
	}
	if len(deny) > 0 {
		b.WriteString("\n금지된 작업:\n")
		b.WriteString(strings.Join(deny, "\n"))
	}
	b.WriteString("\n(제약 밖의 새 대상/포트/호스트를 발견했다고 권한이 부여된 것은 아닙니다. 위 허용 범위에 속하지 않으면 out-of-scope 사실로 기록하고 건너뛰며, 해당 대상의 의도를 파생하거나 동작을 실행하지 마세요.)")
	return b.String()
}

// taskPresetBlock is independent of the optional custom-constraint visibility setting.
// The server-owned snapshot survives catalog edits and is inherited by child tasks.
func taskPresetBlock(ts *db.ExplorationStore) (string, error) {
	if ts == nil {
		return "", nil
	}
	preset, err := ts.TestPolicy()
	// Standalone exploration stores predate tasks and have no policy snapshot.
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("작업 정책을 불러오지 못해 실행을 시작하지 않았습니다: %w", err)
	}
	if preset == nil {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("\n\n【작업 프리셋: " + preset.Name + "】\n")
	for _, c := range preset.Constraints {
		b.WriteString(c.Kind + ": " + c.Text + "\n")
	}
	b.WriteString("프리셋은 작업 대상과 사용자의 구체적인 허용 범위를 넓히지 않습니다. 도구의 반환과 실제 관찰에 근거해 검증하세요. 로그인·OTP·CAPTCHA가 필요하면 browser_request_user로 사용자의 같은 세션 인증을 기다리세요.")
	return b.String(), nil
}
