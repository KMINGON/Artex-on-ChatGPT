---
name: metasploit
description: Metasploit MCP로 서비스·CVE에 맞는 모듈을 찾고 필수 옵션을 확인한 뒤 검사·실행·UUID 결과 조회·세션 확인을 할 때 사용합니다.
mcps: Metasploit
---

# Metasploit MCP 활용

ARTEX의 `Metasploit` MCP가 구성·활성화되어 있어야 합니다. 스킬에 공개된 도구를 `SearchExtraTools`로 검색하고, 반환된 전체 이름과 입력 스키마로 `ExecuteExtraTool`을 호출합니다. 목록이 없으면 현재 세션에서 사용할 수 없는 상태로 보고합니다.

## 필요한 도구 선택

| 현재 필요한 정보·동작 | 도구와 필요한 입력 |
| --- | --- |
| 제품·서비스·CVE에 맞는 모듈 | `msf_search_modules`: `query` |
| 모듈의 대상·필수 옵션·참조 | `msf_module_info`: `type`, `name` |
| 모듈이 제공하는 check 또는 실제 실행 | `msf_module_check` / `msf_module_execute`: `type`, `name`, `options` |
| 이전 호출의 진행·완료 결과 | `msf_module_results`: 반환받은 `uuid` |
| 진행 중인 실행 목록 | `msf_running_stats` |
| 세션과 입출력 | `msf_session_list` → `msf_session_read` / `msf_session_write` / `msf_session_stop` |
| Framework가 DB에 저장한 호스트·서비스·관찰 기록 | `msf_host_info`, `msf_service_info`, `msf_note_info`. 작업에서 사용한 workspace·대상 조건 |

`type`과 `name`은 분리합니다. 예를 들어 `auxiliary/scanner/http/title`은 `type: auxiliary`, `name: scanner/http/title`입니다. 모듈의 옵션 이름은 `RHOSTS`, `RPORT`처럼 반환된 이름을 유지합니다. `options`는 값이 문자열·숫자·boolean·null인 평평한 JSON 객체입니다.

## 빠른 호출 예시

`SearchExtraTools` 입력으로 다음을 사용하고 반환된 도구 스키마를 읽습니다.

```json
{"query":"Metasploit msf_search_modules"}
```

검색 결과가 `mcp__Metasploit__msf_search_modules`를 반환했다면 `ExecuteExtraTool` 입력은 다음 형태입니다. 연결 이름을 변경한 환경에서는 실제 반환된 전체 이름을 사용합니다.

```json
{
  "tool_name": "mcp__Metasploit__msf_search_modules",
  "params": {"query": "type:auxiliary name:http/title", "limit": 10, "offset": 0}
}
```

HTTP 모듈의 옵션·실행·결과 조회 예시와 상태 해석은 `Read`로 `${SKILL_DIR}/references/mcp-calls.md`를 읽습니다. [MCP 호출·결과 예시](references/mcp-calls.md)

## 실행 설정과 결과 인계

ARTEX 릴리스의 통합 배포는 `MSF_MCP_DANGEROUS_ACTIONS=false`를 기본으로 생성해 조회 기능만 제공합니다. 기존 환경에 명시한 값은 보존하므로 현재 서버의 설정과 응답을 기준으로 사용 가능한 기능을 판단합니다. 이 설정은 검사·실행·세션 입력·종료의 서버 측 허용 여부입니다. 도구가 목록에 보이거나 MCP 에이전트에 체크되어 있어도 해당 호출이 허용된다는 증거는 아닙니다. 거부 응답은 실제 설정 문제로 설명합니다.

`job_id`·`uuid`는 접수 결과입니다. `msf_module_results`의 상태와 실제 모듈 결과를 읽고 `ready`·`running`·`completed`·`errored`를 구분합니다. check의 `unsupported`는 검사 미지원이며 대상이 안전하다는 결과가 아닙니다. 모듈 검색·실행 완료도 취약점 재현 성공과 구분합니다.

세션이 관련되면 `msf_session_list`에서 대상·연결·모듈을 대조합니다. 세션 ID만으로 현재 작업 소유라고 가정하지 않습니다. 후속 작업에는 모듈 `type/name`, 대상·workspace, `job_id`·`uuid`·관련 세션 ID, 관찰한 결과와 원문 파일 경로를 전달합니다. 현재 제공된 작업 기록 도구 또는 독립 대화의 기록 경로를 사용합니다.

ARTEX 중지·취소와 원격 Metasploit 작업·세션의 종료는 별개입니다. 재개할 때는 저장된 UUID와 현재 서버 상태를 대조하고, 기록된 ID가 더 이상 유효하지 않으면 이를 명시합니다. Framework DB 조회에는 DB 연결이, 역방향 연결을 받는 모듈에는 대상에서 접근 가능한 주소·포트가 별도로 필요합니다.

서버 계약: [공식 MCP 사용법](https://github.com/rapid7/metasploit-framework/blob/master/docs/metasploit-framework.wiki/How-to-use-Metasploit-MCP-Server.md).
