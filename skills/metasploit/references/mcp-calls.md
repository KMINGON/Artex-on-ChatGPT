# Metasploit MCP 호출과 결과 예시

스킬을 읽은 뒤 `SearchExtraTools`로 해당 도구를 검색합니다. 아래 이름은 서버 이름이 `Metasploit`인 경우이며 실제 검색 결과의 전체 이름과 스키마를 사용합니다. 모든 JSON 호출 예시는 `ExecuteExtraTool`의 입력입니다.

## 모듈 정보에서 입력 확인

검색에서 `auxiliary/scanner/http/title`을 선택한 경우:

```json
{
  "tool_name": "mcp__Metasploit__msf_module_info",
  "params": {"type": "auxiliary", "name": "scanner/http/title"}
}
```

현재 모듈의 필수 옵션·기본값·대상·참조를 읽습니다. `RHOSTS`에는 모듈이 요구하는 호스트를, HTTP 경로는 `TARGETURI`에 전달합니다. 전체 URL을 모든 모듈의 `RHOSTS`에 일괄 넣지 않습니다. 모듈 유형은 `auxiliary`·`exploit` 등의 값이고 `name`에는 유형 접두사를 다시 붙이지 않습니다.

## 실습 HTTP 서비스 실행

같은 Docker 네트워크의 `juiceshop:3000`이 실제 준비되어 있고 해당 모듈에 위 옵션이 있는 경우의 예시입니다. 다른 대상에서는 주소·포트·SSL·경로를 현재 값으로 바꿉니다.

```json
{
  "tool_name": "mcp__Metasploit__msf_module_execute",
  "params": {
    "type": "auxiliary",
    "name": "scanner/http/title",
    "options": {
      "RHOSTS": "juiceshop",
      "RPORT": 3000,
      "SSL": false,
      "TARGETURI": "/"
    }
  }
}
```

이 예시는 HTTP 제목을 조사하는 모듈 실행이며 취약점 재현 예시가 아닙니다. 접수 응답의 형태는 다음과 같습니다. UUID는 아래 문자열을 재사용하지 않고 실제 응답에서 가져옵니다.

```json
{
  "metadata": {"query_time": 0.1},
  "data": {"job_id": 0, "uuid": "<반환된 실행 UUID>"}
}
```

```json
{
  "tool_name": "mcp__Metasploit__msf_module_results",
  "params": {"uuid": "<반환된 실행 UUID>"}
}
```

성공 응답은 `metadata`와 `data`로 감싸집니다. 실제 모듈 반환값은 `data`를 읽습니다.

| `data`의 상태·내용 | 해석 |
| --- | --- |
| `status: ready` | 대기 중이며 아직 시작하지 않음 |
| `status: running` | 실행 중. UUID를 보존하고 이후 상태 확인 |
| `status: completed`, `result` | 실행 종료. `result`의 모듈별 관찰·오류·대상 조건을 해석 |
| `status: errored`, `error` | 실행 중 오류. 메시지와 기존 산출물 전달 |
| check의 `status: unsupported` | 모듈에 check가 없음. 취약/안전 판정 결과가 아님 |

실행 중에는 UUID를 보존하고 필요한 독립 작업을 계속할 수 있습니다. `msf_running_stats`의 waiting/running/results 목록은 실행 UUID를 다시 찾을 때 활용할 수 있습니다.

`completed`의 `result`가 `null`이거나 `#<Mdm::Note:...>` 같은 객체 표시일 수도 있습니다. 실행 종료만 확인된 상태이며 관찰 내용이 모두 반환됐다는 뜻은 아닙니다. `msf_service_info`로 서비스 정보를, `msf_note_info`로 저장된 notes를 조회합니다. HTTP 제목 예시는 `msf_note_info`의 `type: http.title`과 대상·workspace 조건으로 원문을 대조합니다. 해당 조회에도 상세 내용이 없거나 콘솔 출력이 반환되지 않았다면 미확인으로 남기며, 객체 표시만으로 제목을 확인했다고 보고하지 않습니다.

## check·세션·실패 구분

check를 제공하는 모듈은 `msf_module_check`에 동일한 `type`, `name`, `options` 구조를 전달합니다. check 결과의 `Safe`, `Detected`, `Appears`, `Vulnerable`, `Unknown` 등은 모듈의 판정 근거와 대상 조건을 함께 읽습니다. check가 없는 모듈에도 성공적인 MCP 응답으로 `unsupported`가 반환될 수 있습니다.

세션 목록은 `msf_session_list`의 `data`에서 세션 ID를 키로 갖는 객체로 반환됩니다. 해당 세션의 대상·모듈을 확인한 뒤 다음 도구의 `session_id`에는 숫자를 전달합니다. `msf_session_write`는 추가로 `data` 문자열을 받으며, 결과 출력은 `msf_session_read`로 읽습니다. 종료가 필요한 실제 세션에 `msf_session_stop`을 사용합니다. 모듈 `job_id`, 실행 `uuid`, 세션 ID는 서로 대체할 수 없습니다.

| 실패·상황 | 확인할 부분 |
| --- | --- |
| 도구 목록 없음 | 현재 MCP 활성·주소·인증·스킬 연결 상태 |
| 입력 검증 오류 | 검색에서 얻은 스키마, 모듈 type/name, 필수 옵션. options에 배열·중첩 객체를 넣지 않았는지 |
| 실행 제한 오류 | 서버의 `MSF_MCP_DANGEROUS_ACTIONS`. 에이전트 체크박스와 별개 |
| DB 조회 실패 | Framework DB 연결·workspace |
| 연결 실패 또는 역방향 세션 미수립 | 대상 주소·라우팅·모듈 옵션·콜백 주소/포트 |
| 이전 UUID·세션을 찾지 못함 | 서버 재시작·세션 종료·현재 서버 일치 여부. 같은 실행이 계속 남아 있다고 가정하지 않음 |

기존 작업 기록에 모듈과 대상, 실제 반환 상태, 재사용할 UUID/세션 ID, 원문 결과의 절대 경로를 남깁니다. `completed`를 취약점 성공으로 바꾸거나 접수 UUID만 기록하고 검증 결과를 생략하지 않습니다.

공식 계약: [Metasploit MCP 사용법](https://github.com/rapid7/metasploit-framework/blob/master/docs/metasploit-framework.wiki/How-to-use-Metasploit-MCP-Server.md).
