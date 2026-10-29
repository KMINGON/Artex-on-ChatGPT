# Norma 원본 대비 변경점

이 디렉터리는 [`github.com/Autumn-27/norma`](https://github.com/Autumn-27/norma)의 **v0.4.3**을 기반으로 ARTEX에 포함한 실행 엔진입니다.

- 원본 커밋: `320ae32989da97dd91469bb89164cbfe87d83d5d`
- 원본 모듈 체크섬: `h1:DnmOhJPL6OZSMnCVYO5Ujh4yVINuDarEickVi9jx1hI=`
- 모듈 경로와 기존 저작권 표기를 유지합니다. 해당 배포물에는 별도 LICENSE·NOTICE 파일이 없었으며, 이 문서는 새 라이선스를 부여하거나 원저작자의 권리를 변경하지 않습니다.

아래는 원본과 현재 사본의 차이입니다. ARTEX의 계획·작업 배정은 루트 `agent`·`server`에 있으며, 프로젝트 전체 차이는 [ARTEX 변경점](../../docs/changes.md)에 정리합니다.

## OpenAI 연결과 응답 상태

| 구분 | 현재 동작 |
| --- | --- |
| 공급자 연결 | Norma의 Anthropic·Chat Completions·기존 Responses 어댑터를 제거하고 ARTEX가 `llm.Provider`를 주입합니다. API 키·ChatGPT 구독의 HTTP·인증·응답 해석은 [`chatgptllm`](../../chatgptllm/)과 [`chatgptauth`](../../chatgptauth/)에서 처리합니다. |
| 메시지 상태 | `llm.Message.ProviderData`로 공급자 재전송 상태를 대화 기록과 함께 보존합니다. 표시용 본문과 구분하고, 내용을 수정하는 압축·투영에서는 해당 상태를 제거합니다. |
| 스트림 완료 | `SEMessageFinal`로 완성된 메시지를 전달합니다. accumulator는 복사본을 소유하며 사용량·종료 이유를 함께 누적합니다. 도구 실행은 기존 도구 이벤트를 사용합니다. |
| 제외 구성 | 사용하지 않는 독립 CLI·예제·Noa 압축 엔진과 어댑터를 제거했습니다. DeepSeek 전용 검색은 제외하고 DDGS·Brave·Tavily는 유지합니다. |

관련 구현: [`llm/types.go`](llm/types.go), [`llm/provider.go`](llm/provider.go), [`llm/accumulate.go`](llm/accumulate.go), [`llm/boundary.go`](llm/boundary.go).

## 대화·계획 상태 보존

원본의 정상 요약 흐름을 사용하면서, 앞부분을 버리거나 불완전한 저장·요약을 성공으로 처리하는 경로를 수정했습니다.

| 영역 | 원본 대비 변경 |
| --- | --- |
| 요약 전 도구 결과 | 오래된 결과를 먼저 지우는 MicroCompact를 실행 경로에서 사용하지 않습니다. 결과 본문과 저장 파일 참조를 요약기에 전달합니다. |
| 문맥 초과 요약 | 앞 API round를 버리는 대신 완전한 round 경계로 나누어 모든 구간을 시간순으로 요약합니다. 한 구간이라도 실패하면 새 요약을 적용하지 않습니다. |
| 호출·결과 연결 | 유지할 tail은 해당 도구 호출 전체를 포함합니다. 짝이 없는 기록도 ID·인자·본문·이미지를 실행 여부 미확인의 요약 자료로 보존합니다. |
| 완료 확인 | 취소·빈 본문·비정상 종료·도구 호출이 포함된 요약은 적용하지 않습니다. `NoReplay`·`NoFallback` 오류는 자동 재요청하지 않습니다. 합친 요약이 더 커지면 기존 뷰를 유지합니다. |
| 문맥 계산 | 현재 메시지와 system·도구 스키마 비용을 반영합니다. 성공한 새 압축 경계 뒤에는 이전 입력 사용량을 무효화합니다. |
| Todo 저장 | 선택적 저장 콜백이 성공한 뒤 메모리 목록을 갱신합니다. 실패·취소는 기존 목록을 유지합니다. |
| Todo 재개 | 전체 원본 기록의 성공한 `TodoWrite`만 복원합니다. 실패·미응답 호출과 checkpoint 중복은 목록을 바꾸지 않습니다. 호스트가 관리하는 목록은 이력 재생으로 덮어쓰지 않습니다. |
| Todo 전달 | 복원된 목록이 현재 모델 입력에서 사라졌으면 다시 전달합니다. 이미 보이는 목록에는 원본 reminder 간격을 유지하며, 도구나 저장 콜백을 재실행하지 않습니다. |

관련 구현: [`compaction/compaction.go`](compaction/compaction.go), [`compaction/summarizer.go`](compaction/summarizer.go), [`tool/todo.go`](tool/todo.go), [`agentcore/agentcore.go`](agentcore/agentcore.go), [`harness/query.go`](harness/query.go).

### 대화 파일의 저장과 복구

Checkpoint는 `checkpoint_begin`·`checkpoint_part`·`checkpoint_commit`으로 저장합니다. 버전·ID·순서·개수·전체 SHA-256을 확인하며, part에는 일반 메시지와 다른 `checkpoint_message` 필드를 사용합니다. 파일을 동기화하고 Unix에서는 상위 디렉터리도 동기화합니다.

재개 시 이전 형식의 메시지와 완전한 checkpoint를 읽습니다. 새 형식의 불완전한 checkpoint는 제외하되 이전 완전한 뷰와 이후의 일반 메시지를 보존합니다. 복구는 원본을 덮어쓰지 않고 동기화한 임시 파일을 `<source>.recovered`로 원자적으로 게시합니다. 이후 읽기·쓰기는 최대 32단계의 복구 파일을 따릅니다.

진행 중인 checkpoint의 마지막 JSON 줄 손상, 마지막 줄바꿈 누락, 이전 형식의 단독 최종 boundary는 복구할 수 있습니다. 그 밖의 손상·알 수 없는 형식·커밋된 digest 불일치는 재개 오류로 반환합니다. 저장 오류는 `storage_error`로 종료되며 전체 실행 재시도·다른 공급자로의 전환을 허용하지 않습니다. `Session.Close`도 저장 실패를 반환합니다.

새 기록을 이전 엔진과 동시에 쓰면 안 됩니다. 이전 리더는 새 checkpoint와 복구 파일을 해석하지 못하므로, 이전 바이너리로 되돌릴 때는 그 버전에 맞는 대화 기록과 연결 정보가 필요합니다. Windows의 디렉터리 내구성은 실제 Windows 환경에서 검증하지 않았습니다.

관련 구현: [`transcript/checkpoint.go`](transcript/checkpoint.go), [`transcript/transcript.go`](transcript/transcript.go), [`harness/storage.go`](harness/storage.go).

## 도구 실행·인자·출력

| 영역 | 원본 대비 변경 |
| --- | --- |
| 취소 | 대기 중인 호출은 취소 가능한 동시 실행 슬롯을 사용합니다. 취소된 묶음의 호출이 마무리 단계의 새 context로 실행되지 않으며 늦은 결과를 다시 게시하지 않습니다. |
| 출력 한도 | 출력 연장 후에도 한도에 걸리면 `completed` 대신 `output_limit`로 종료합니다. 잘린 도구 인자는 실행하지 않습니다. |
| 인자 오류 | 직접 호출과 `ExecuteExtraTool`에서 현재 필수 필드·타입을 안내합니다. 누락 값을 만들어 실행하거나 전달된 비밀 값을 오류 안내에 복사하지 않습니다. |
| wrapper 호환 | `params`와 `parameters`를 같은 인자 객체로 받습니다. 둘을 함께 보내 값이 다르면 실행하지 않습니다. 내부 필드 이름은 추측하거나 변환하지 않습니다. |
| 선택값 | JSON Schema의 union·`null`과 두 가지 `required` 배열 표현을 처리합니다. nullable 객체의 하위 필수 필드 검사는 유지합니다. |
| 출력 캡처 | 같은 호출에서 실제로 캡처한 결과만 중복 캡처에서 제외합니다. 외부 출력에 포함된 marker 문자열은 신뢰하지 않습니다. 잘림 안내의 크기는 실제 계산 단위인 바이트로 표시합니다. |
| 파일 검색 | `Grep.only_matching`은 `content` 모드에서 일치 부분과 파일·행 번호를 반환합니다. 기본 전체 행 출력은 유지하며 Go 검색·ripgrep 양쪽에 pagination을 적용합니다. 이 옵션에서는 `-A`·`-B`·`-C`의 전체 문맥 행을 제외합니다. Grep와 LS의 안내는 실제 제공된 검색 도구와 Bash를 사용하도록 하며, ARTEX에 없는 SDK Agent 도구를 권하지 않습니다. |
| Shell | 대화형 Shell의 설명·인자 도움말·오류·완료 알림을 한국어로 제공합니다. 프롬프트·출력 정지 등에 따른 대기 종료, PTY 세션의 running/exit 상태, 개별 명령 성공을 구분합니다. `Monitor`에도 작업의 Bash 환경을 전달합니다. 도구 이름·JSON 필드·상태값은 유지합니다. |

관련 구현: [`harness/streaming.go`](harness/streaming.go), [`harness/tools.go`](harness/tools.go), [`tool/schema.go`](tool/schema.go), [`tool/deferred.go`](tool/deferred.go), [`tool/helpers.go`](tool/helpers.go), [`tool/search.go`](tool/search.go), [`tool/ripgrep.go`](tool/ripgrep.go).

## 이미지·MCP·네트워크

- MCP의 텍스트·이미지 혼합 결과와 `Read`의 이미지 파일을 실제 `llm.BlockImage`로 전달합니다. PNG·JPEG·정적 GIF·WebP의 MIME·크기·해상도와 결과당 이미지 수를 검사합니다. 텍스트가 없는 MCP 리소스에는 서버가 조회 도구를 제공하는 경우 이를 사용하라고 조건부로 안내합니다. 출력 폴더가 있으면 SHA-256 이름의 0600 파일로 저장하고, 재전송할 때 형식·해시를 확인합니다. 일반 로그에는 픽셀 데이터를 넣지 않으며 문맥 계산은 별도의 이미지 예산을 사용합니다.
- MCP stdio 종료·초기화 실패 시 하위 프로세스도 정리합니다. Unix는 별도 process group, Windows는 `taskkill /T`를 사용합니다. Windows 프로세스 종료는 실제 환경에서 검증하지 않았습니다.
- WebFetch와 WebSearch의 공통 HTTP client는 미지정 프록시를 명시적인 직접 연결로 처리하고 잘못된 프록시 설정은 오류로 반환합니다. 지정된 기록 프록시 때문에 실패한 경우에만 기록기를 우회하며 외부 egress 프록시는 유지합니다. 일반 프록시를 조용히 우회하지 않습니다.
- WebFetch는 본문 수신 중단을 오류로 반환하며 받은 부분을 함께 제공합니다. 2 MiB 수신 한도와 50,000바이트 표시 한도를 구분하고 UTF-8 경계에서 자릅니다.

관련 구현: [`llm/image.go`](llm/image.go), [`tool/images.go`](tool/images.go), [`tool/files.go`](tool/files.go), [`mcp/mcp.go`](mcp/mcp.go), [`mcp/stdio.go`](mcp/stdio.go), [`tool/webfetch.go`](tool/webfetch.go).

## 검증 범위

로컬 회귀 검사는 응답 상태 누적·투영, checkpoint 부분 저장·복구, 요약 실패·구간 보존, Todo 재개, 취소된 도구 대기열, 출력 한도, 스키마·wrapper 인자, 이미지 전달, MCP 하위 프로세스, 네트워크 오류를 다룹니다. 모의 공급자·임시 파일·로컬 HTTP fixture를 사용하며 실계정 모델 성능을 증명하는 검사는 아닙니다. Norma는 별도 Go 모듈이므로 루트 ARTEX 검사와 구분해서 실행해야 합니다.
