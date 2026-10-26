# 취약점과 트래픽 증거

취약점 상세 화면의 **연결된 트래픽**에서 HTTP 요청·응답을 여러 개 연결하고, 용도·설명·순서를 관리할 수 있습니다. 트래픽 화면에서도 선택한 기록을 기존 취약점에 연결할 수 있습니다. 다른 작업에서 상속한 취약점은 읽기 전용이며, 수정은 원본 작업에서 합니다.

## 자동 연결과 수동 연결

시스템 설정의 **에이전트 자동 트래픽 바인딩**(`agent_traffic_binding`)은 기본적으로 꺼져 있습니다. 켜면 에이전트가 실제 트래픽을 조회하고 취약점에 연결할 수 있어 모델 사용량과 처리 시간이 늘어날 수 있습니다. 끄더라도 수동 연결, 캡처, 저장된 증거 읽기·내보내기는 유지됩니다.

자동 연결 시에는 발견 사항과 검증 근거를 저장하고, 보고서 에이전트가 `traffic_search` / `traffic_get`으로 대조한 트래픽을 연결한 뒤 보고서를 작성합니다. 트래픽이 없는 TCP 취약점이나 미수집 항목도 명령 출력·로그 등 다른 근거로 보고할 수 있습니다. 트래픽 ID를 추측하거나 연결 개수를 채우기 위해 요청할 필요는 없습니다. 정상 대조·재현·상태 변화 등 실제 검증에 필요한 요청과 근거는 계속 확보합니다. 확인된 발견의 기록·보고서 작성 자체가 같은 질문의 후속 검증을 종료하는 조건은 아닙니다.

| 도구 | ID와 역할 |
|---|---|
| `report_finding` | 선택적인 `traffic_refs`를 함께 저장합니다. 결과 JSON의 `finding_id`는 취약점 ID, `finding_node_id`는 탐색 노드 ID입니다. |
| `bind_finding_traffic` | **취약점 ID**로 기존 취약점에 트래픽을 추가합니다. |
| `get_finding_traffic` | **취약점 ID**로 연결 목록과 `version`을 읽습니다. `binding_id`, `side`, `offset`, `length`로 본문을 최대 8192바이트씩 읽습니다. |
| `update_finding_report` | 이 도구의 `finding_id`는 **탐색 노드 ID**입니다. 읽은 증거의 `version`을 `evidence_version`으로 전달합니다. |

`report_finding`에서 `traffic_refs` 생략·null은 연결 없음으로 처리합니다. 선택 인자인 `evidence_hint_id`의 생략·null·0도 미지정으로 처리하며, 실제로 지정한 근거 ID는 유효성을 확인합니다. 잘못된 ID나 항목 형식을 임의로 무시하지 않습니다.

`traffic_refs`의 항목은 `traffic_id`, `role`, `note`를 사용합니다. `role`은 `baseline`(정상 대조), `proof`(취약점 증명), `verification`(보완 검증), `supporting`(보조 증거, 기본값) 중 하나입니다. 한 묶음의 연결은 모두 성공하거나 모두 실패하며, 동일한 증거를 다시 제출해도 기존 설명을 덮어쓰지 않습니다. 연결이나 설명·순서가 바뀌면 보고서에 갱신이 필요하다고 표시됩니다.

## API와 보존

기본 경로는 `/api/exploration/findings/{finding_id}/traffic`이며 **취약점 ID**를 사용합니다.

| 요청 | 동작 |
|---|---|
| `GET` / `POST` | 연결 목록 조회 / `traffic_refs` 일괄 추가 |
| `PATCH /{binding_id}` | `version`, `role`, `note` 수정 |
| `DELETE /{binding_id}` | `version`을 확인한 뒤 연결 해제 |
| `PUT /order` | `version`, 전체 `binding_ids`로 순서 변경 |
| `GET /{binding_id}` | 메타데이터와 제한된 본문 미리 보기 |
| `GET /{binding_id}/body` | `side`, `offset`, `length`로 구간 조회. `download=1`이면 전체 다운로드 |

증거 본문은 `<data>/evidence/blobs/`에 해시로 저장하고 PostgreSQL에 스냅샷과 연결 정보를 보관합니다. 원본 트래픽 정리와 증거 보존은 분리되어 있으며, 읽기·내보내기 시 본문 무결성을 확인합니다. 데이터 이전이나 복구 시에는 **DB와 데이터 디렉터리를 함께 보존**해야 합니다.

관련 구현: [API·도구](../server/finding_traffic.go), [자동 연결](../server/finding_workflow.go), [DB](../db/finding_traffic.go), [증거 저장소](../evidence/store.go).
