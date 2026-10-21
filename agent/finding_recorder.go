package agent

import (
	"context"

	"github.com/Autumn-27/artex/db"
)

// FindingRecorder is injected by the host; agents never synthesize or copy
// evidence bodies themselves. Its implementation owns the atomic write.
type FindingRecorder interface {
	Record(context.Context, db.RecordFindingInput, []db.TrafficRef) (*db.RecordedFinding, error)
}

// Tool-use guidance is appended without replacing the user's editable prompt.
// It does not require capture or claim that unavailable traffic tools exist.
const findingTrafficGuidance = "\n\n**취약점 트래픽 증거(선택 사항)**: report_finding으로 취약점을 보고할 때, 직접 확인하여 취약점 결론을 뒷받침하는 HTTP 요청/응답이 있으면 traffic_refs로 실제 ID를 재현 순서에 맞춰 연결할 수 있습니다. 도메인과 시각은 후보 검색에만 사용하며 연관성을 단정하지 마세요. TCP 등 HTTP가 아닌 취약점, 미수집 또는 정확히 일치하는 기록이 없는 경우 생략하거나 []를 전달하고, evidence에 명령 출력·로그 등 검증 가능한 다른 증거를 보존하세요. 연결하지 않은 이유를 설명하는 것을 권장합니다. ID를 추측하거나 패킷을 보충하려고 재탐색하지 마세요."

func (t *ToolSet) SetFindingRecorder(r FindingRecorder)   { t.findingRecorder = r }
func (w *Worker) SetFindingRecorder(r FindingRecorder)    { w.findingRecorder = r }
func (p *Planner) SetFindingRecorder(r FindingRecorder)   { p.findingRecorder = r }
func (m *MainAgent) SetFindingRecorder(r FindingRecorder) { m.findingRecorder = r }
