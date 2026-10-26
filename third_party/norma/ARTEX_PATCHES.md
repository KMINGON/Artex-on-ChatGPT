# Norma runtime snapshot

This directory contains a modified runtime snapshot of
[`github.com/Autumn-27/norma`](https://github.com/Autumn-27/norma) **v0.4.3**,
retrieved from the Go module cache. Upstream commit:
`320ae32989da97dd91469bb89164cbfe87d83d5d` (tag `v0.4.3`).

Original module checksum:
`h1:DnmOhJPL6OZSMnCVYO5Ujh4yVINuDarEickVi9jx1hI=`.

The module identity and original copyright notices are retained. Unused
provider adapters, standalone applications/examples, and experimental Noa
packages have been removed. The source distribution contained no LICENSE or
NOTICE file; this file does not supply a new license or change the upstream
author's rights.

Artex uses a local module replacement for these limited runtime extensions:

- `llm.Message.ProviderData` persists opaque provider replay state in normal
  transcript messages. It is not user-facing text.
- `llm.SEMessageFinal` supplies an authoritative completed message after live
  deltas. The accumulator owns a deep copy while continuing to track usage and
  stop reason. Providers still emit normal tool events for execution.
- History projections preserve unchanged provider state and clear it when
  content changes, preventing removed tool calls from returning through raw
  replay data.
- 요약은 `NoReplay()`·`NoFallback()` 오류를 자동 재요청하지 않습니다. 길이 초과는
  완전한 API round와 도구 호출·결과의 경계로 분할하고 모든 조각을 시간순으로
  요약합니다. 앞부분을 버리거나 일부 조각만으로 성공 처리하지 않습니다.
  나눌 수 없는 큰 round, 조각 실패·취소·비정상 종료·빈 본문, 합친 요약의
  문맥 추정량 비감소 때는 원문과 기존 API view를 유지합니다.
- 자동 압축은 요약 전에 오래된 도구 결과를 지우지 않습니다. 결과 본문과 저장
  파일 경로를 요약기에 전달하고, 보존할 tail의 도구 호출·결과도 함께 유지합니다.
  짝이 없는 기록은 ID·입력·본문·이미지를 실행 여부가 미확인된 요약 자료로
  변환하며, 바뀐 내용의 ProviderData는 지워 이전 도구 구조가 되살아나지 않게
  합니다. 문맥 판단은 현재 메시지와 system/tool schema 비용을 포함하고,
  성공한 새 boundary 뒤에는 이전 usage 추정량을 무효화합니다.
- A cancelled tool batch retains its original context. Waiting for a concurrency
  slot is cancellable, and queued handlers cannot inherit a settlement context.
- The final tool-output cap trusts only results captured by the same invocation,
  not marker strings embedded in untrusted output. Monitor receives BashEnv.
- Stdio MCP processes use an isolated process group on Unix. Close and failed
  initialization terminate that group; Windows uses taskkill /T (not runtime
  tested here).
- Exhausted output continuation returns `output_limit`, not `completed`.
  Incomplete tool input is rejected without invoking its handler.
- Transcript read/write failures return `storage_error`; storage errors prohibit
  whole-run replay and provider fallback. Session.Close also reports the first
  persistence failure. ARTEX's Chat/Main/Worker callers now check Resume/Close.
- Interactive-shell descriptions, parameter help, errors, notifications, and
  background-task completion messages use Korean. Tool identifiers, schemas,
  task status values, and existing stored output remain unchanged.
- `Grep`의 선택 입력 `only_matching`은 `content` 모드에서 일치 부분만
  파일·행 번호와 함께 출력합니다. 긴 한 줄 JavaScript의 중간 문맥은
  `.{0,100}검색어.{0,100}`처럼 검색 패턴에 포함할 수 있습니다. 기본 전체 행
  출력과 다른 출력 모드는 유지하며, 이 옵션에서는 `-A`·`-B`·`-C`의 전체
  문맥 행을 출력하지 않습니다. Go 검색과 ripgrep의 `-o` 경로 모두 출력
  항목에 `offset`·`head_limit`를 적용합니다. 검색 문자열은 실행하지 않으며
  기존 도구 설명의 Bash 검색 금지 문구는 Grep 우선 사용 권고로 바꿨습니다.
- Experimental `noa` and `noaadapter` packages and ARTEX's toggle/integration
  are removed. All ARTEX agents use built-in compaction. Generic compactor,
  context-view, and tool contracts remain tested; old user archives and
  transcripts are not deleted.
- WebFetch (and WebSearch's shared HTTP client) uses an explicitly direct
  transport when no proxy is configured and rejects malformed explicit proxies.
  Only a designated recording proxy can be bypassed after a proxy-caused error;
  the retry preserves the configured external egress proxy and reports that
  traffic was not recorded. Ordinary proxies are never silently bypassed.
  WebFetch reports interrupted body reads as errors with any received partial
  content, marks its 2 MiB receive limit separately from the 50,000-byte display
  limit, and preserves UTF-8 boundaries when shortening either output.
- 이미지 입력은 `llm.BlockImage`로 보존한다. MCP의 텍스트·이미지 혼합
  결과와 `Read` 이미지 파일을 실제 시각 입력으로 전달하며 MIME·크기·해상도와
  도구 결과당 이미지 개수를 검사한다. 세션 출력 폴더가 있으면 이미지는 0600
  권한의 SHA-256 파일로 저장하고 대화에는 경로·해시만 남긴다. 파일을 다시
  전송할 때 해시와 형식을 확인하며, 일반 도구 로그에는 픽셀 데이터가 없다.
  압축 예산도 base64 길이가 아닌 별도의 이미지 예산을 반영한다. OpenAI 요청
  형식·최근 이미지 유지 한도·누락된 파일의 복구 안내는 ARTEX `chatgptllm`에서
  처리한다. MCP stdio·HTTP, 파일 읽기, 대화 저장/재개, 요청 변환 회귀 검사로
  관련 경로를 검증한다.

Checkpoints now use `checkpoint_begin`, `checkpoint_part`, and
`checkpoint_commit` records with version 1, an ID, count, ordered indices, and a
SHA-256 digest of the complete working view. Parts use `checkpoint_message`
instead of `message`, so an old reader cannot mistake an incomplete part for a
normal message. File contents are synced, followed by the containing directory
on Unix. Windows directory durability still needs native verification.

Resume accepts legacy messages and complete legacy checkpoints. A recognized
incomplete new checkpoint is excluded while keeping the previous complete
checkpoint and subsequent ordinary messages. Recovery writes a synced temporary
file and atomically publishes `<source>.recovered`, preserving the source bytes;
subsequent reads/writes follow that file (up to 32 recovery generations). A torn
last JSON line is recoverable only inside a known pending checkpoint. Other
corruption, unknown record types/versions, or invalid committed digests stop
resume. A complete final JSON record missing its newline is copied before
appending so two records cannot concatenate. A legacy bare final boundary is recoverable; partially saved legacy
summary/tail sequences cannot be reliably identified without commit metadata.

Do not let old and new binaries write these files concurrently. Older readers
do not understand committed checkpoints or recovery files. Rollback requires
the preserved pre-upgrade transcript backup and matching connection bindings;
pointing an old binary at newly written history can restore an incorrect view.

Provider-specific formats, HTTP, authentication, and replay validation stay in
Artex's `chatgptllm` and `chatgptauth` packages. Local regression tests cover
accumulation, projection, transcript persistence, and summary retry behavior.
The reliability regressions also cover checkpoint interruption/durability
failures, preserved post-checkpoint tool results, source immutability, repeated
resume, cancelled queues, output limits, spill markers, MCP children, and
Monitor environment propagation. These tests use temporary files and local
fixtures, with no live model or production database access.

원본 대비 작업 상태·도구 입력 계약:

- `TodoStore`에 초기 목록과 선택적 저장 콜백을 추가했습니다. 저장 성공 후에만
  메모리 목록을 갱신하며 실패·취소 시 기존 목록을 보존합니다. ARTEX Planner는
  작업 시작 사실의 payload와 revision으로 저장·재시작·보관 복원을 처리합니다.
  메모리 Todo를 쓰는 Worker·Main·Chat은 Resume 때 압축 전 원본 기록의 마지막
  성공 `TodoWrite`를 복원합니다. 실패·결과 없는 호출과 checkpoint 중복은
  목록을 바꾸지 않으며 성공한 빈 목록은 반영합니다. `ExecuteExtraTool`의
  `params`·`parameters`도 같은 계약으로 읽습니다. Planner의 host-owned 목록과
  사용자 편집은 기록 재생으로 덮어쓰지 않습니다. 현재 API view에 목록이 없으면
  다음 모델 요청 전에 일반 transcript 메시지로 다시 전달하고, 이미 보이는
  목록에는 기존 리마인더 간격을 유지합니다. 저장 콜백이나 과거 도구를 재실행하지
  않습니다.
- 직접 호출과 `ExecuteExtraTool`의 인자 검증 오류에 현재 스키마의 필수 필드와
  타입을 안내합니다. 전달된 값·기본값은 오류 안내에 복사하지 않고, 빠진 값을
  임의로 만들어 실행하지 않습니다. `required`의 Go `[]string`과 JSON `[]any`
  표현을 모두 검사합니다.
- `ExecuteExtraTool` wrapper는 기존 `params`와 모델이 보내기도 하는
  `parameters`를 같은 인자 객체로 해석합니다. 둘 다 있으면 JSON 값을 비교해
  다를 때 도구 실행을 거부합니다. 내부 도구 필드 이름은 추론하거나 변경하지
  않고 실제 도구 스키마로 검증합니다. alias·충돌·타입 회귀는 `./tool`에서
  검사했습니다.

원본 대비 선택 입력 계약:

- 도구 스키마 검증에서 `type: ["integer", "null"]` 같은 union과 `null`을
  지원합니다. 미지정 선택값을 표현한 `null`은 통과하고, 배열·객체·문자열 등
  선언하지 않은 형태는 거부합니다. nullable 객체도 하위 필수 필드 검사를
  유지합니다. ARTEX의 `report_finding.evidence_hint_id`에서 힌트가 없는 경우
  생략·`null`·`0`을 허용하며, 양수 ID의 실제 힌트/증거 귀속 검증은 유지합니다.
  provider 요청 형식과 인증 방식은 바꾸지 않습니다.
