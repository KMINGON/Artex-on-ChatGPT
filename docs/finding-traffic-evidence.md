# 취약점과 트래픽 증거

원본 ARTEX에도 취약점과 HTTP 트래픽의 연결·스냅샷 저장·보고서 버전 관리가 있었습니다. 현재 버전은 이 흐름을 유지하며 선택 인자 처리와 저장·조회 오류를 보완합니다. 증거 연결을 별도의 필수 검증 단계나 작업 완료 조건으로 추가하지 않습니다. [원본 대비 변경점](changes.md)

## 연결 화면과 설정

취약점 상세의 **연결된 트래픽**에서 요청·응답을 여러 개 연결하고 용도·설명·순서를 관리합니다. 트래픽 화면에서도 선택한 기록을 기존 취약점에 연결할 수 있습니다. 다른 작업에서 상속한 취약점은 읽기 전용이며 원본 작업에서 수정합니다.

시스템 설정의 **에이전트 자동 트래픽 바인딩**(`agent_traffic_binding`)은 기본적으로 꺼져 있습니다. 켜면 에이전트가 트래픽을 조회·연결하고 보고서 에이전트가 연결된 근거를 사용합니다. 끄더라도 수동 연결, 캡처, 저장된 증거의 읽기·내보내기는 유지됩니다. 자원 프리셋은 이 설정을 변경하지 않습니다.

자동 연결에서는 실제 수집한 트래픽을 조회해 발견과 연결합니다. 트래픽이 없는 TCP 취약점이나 미수집 항목은 명령 출력·로그 등 다른 근거를 사용할 수 있습니다. 이 설정은 추가 모델 호출·조회 시간을 사용할 수 있으며, 트래픽 존재가 모든 취약점 보고의 필수 조건은 아닙니다.

## 도구와 ID

| 도구 | ID와 역할 |
| --- | --- |
| `report_finding` | 선택적 `traffic_refs`를 함께 저장. 결과의 `finding_id`는 취약점 ID, `finding_node_id`는 탐색 노드 ID |
| `bind_finding_traffic` | **취약점 ID**로 트래픽 추가 |
| `get_finding_traffic` | **취약점 ID**로 연결 목록과 `version` 조회. `binding_id`, `side`, `offset`, `length`로 본문 구간 읽기 |
| `update_finding_report` | 이 도구의 `finding_id`는 **탐색 노드 ID**. 읽은 증거의 `version`을 `evidence_version`으로 전달 |

`report_finding`의 `traffic_refs` 생략·null은 연결 없음으로 처리합니다. `evidence_hint_id`의 생략·null·0도 미지정으로 처리하며, 실제 지정한 ID는 유효성을 확인합니다. 잘못된 ID나 항목 형식을 무시하고 성공 처리하지 않습니다.

`traffic_refs` 항목은 `traffic_id`, `role`, `note`를 사용합니다. 역할은 `baseline`(정상 대조), `proof`(취약점 증명), `verification`(보완 검증), `supporting`(보조 증거, 기본값)입니다. 한 묶음의 연결은 모두 성공하거나 모두 실패하며, 같은 증거를 다시 제출해도 기존 설명을 덮어쓰지 않습니다. 연결·설명·순서가 바뀌면 증거 버전이 바뀌고 보고서 갱신 필요 상태를 표시합니다.

## API

기본 경로 `/api/exploration/findings/{finding_id}/traffic`의 `finding_id`는 **취약점 ID**입니다.

| 요청 | 동작 |
| --- | --- |
| `GET` / `POST` | 연결 목록 조회 / `traffic_refs` 일괄 추가 |
| `PATCH /{binding_id}` | `version`을 확인하고 `role`, `note` 수정 |
| `DELETE /{binding_id}` | `version`을 확인하고 연결 해제 |
| `PUT /order` | `version`과 전체 `binding_ids`로 순서 변경 |
| `GET /{binding_id}` | 메타데이터와 제한된 본문 미리 보기 |
| `GET /{binding_id}/body` | `side`, `offset`, `length`로 구간 조회. `download=1`이면 전체 다운로드 |

도구의 본문 구간 읽기는 최대 8192바이트씩 반환합니다. 연결 버전은 동시 편집 중 다른 변경을 덮어쓰지 않도록 확인하는 값이며, 탐색을 중단시키는 판정 단계가 아닙니다.

## 보존

증거 본문은 `<data>/evidence/blobs/`에 해시로 저장하고, PostgreSQL에는 스냅샷과 연결 정보를 보관합니다. 원본 트래픽 정리와 증거 보존은 분리되어 있으며 읽기·내보내기 시 본문 무결성을 확인합니다. 이전·복구 시에는 DB와 데이터 디렉터리를 함께 보존해야 합니다.

관련 구현: [API·도구](../server/finding_traffic.go), [보고서 연결](../server/finding_workflow.go), [DB](../db/finding_traffic.go), [증거 저장소](../evidence/store.go).
