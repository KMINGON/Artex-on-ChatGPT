# Artex-on-ChatGPT

한국어를 지원하는 [artex-global](https://github.com/jiwoochris/artex-global)에 **개인 ChatGPT 계정의 요금제로 모델을 사용하는 연결 방식**을 추가한 포크입니다. 기준 소스는 `main`의 [`fdfb7a1`](https://github.com/jiwoochris/artex-global/commit/fdfb7a17b7da5e2de716c0e98475ec77a41c64c6)입니다.

Norma v0.4.3 기반 에이전트·도구 실행과 API 키 설정을 유지하며, OpenAI의 [오픈소스 Sign in with ChatGPT](https://developers.openai.com/siwc/token-sharing-open-source/sign-in)로 계정을 연결합니다. Codex 설치나 API 키 입력은 필요하지 않습니다. **실계정 단문 응답과 테스트용 함수 호출 왕복을 로컬에서 확인했습니다. 실제 작업·장시간 안정성은 추가 검증이 필요합니다.**

ARTEX의 기능·사용법·방어 자료는 [원본 한국어 안내](https://github.com/jiwoochris/artex-global/blob/fdfb7a17b7da5e2de716c0e98475ec77a41c64c6/README.ko.md)를 참고하세요. 이 도구는 학습·연구와 로컬 격리 환경 검증에 사용하세요. 원저자의 [사용 제한과 면책 고지](https://github.com/jiwoochris/artex-global/blob/fdfb7a17b7da5e2de716c0e98475ec77a41c64c6/README.zh.md#许可与免责声明)도 확인하세요.

## 원본 대비 변경점

| 항목 | 원본 artex-global | 이 포크 |
| --- | --- | --- |
| 모델 인증 | 제공자 API 키 | 기존 방식에 개인 ChatGPT 계정 연결 추가. ARTEX 관리자 로그인은 별도 유지 |
| 에이전트·도구 | Norma v0.4.3 기반 로컬 도구, MCP, 하위 에이전트, 대화 압축 | 기존 실행 구조 유지. 응답 상태 보존을 위한 소규모 Norma 확장을 포함하고 ChatGPT 모델을 같은 흐름에 연결 |
| 모델 선택 | 제공자별 모델 목록 조회·모델명 입력 | ChatGPT 계정이 반환한 목록에서 선택. 제공되는 문맥 길이도 프로필에 반영 |
| OpenAI 추론 이력 | Responses 연결에서 표시용 추론 텍스트만 읽고, 다음 요청에서 추론 블록 제외. 암호화 추론·응답 단계는 보존하지 않음 | 새 ChatGPT 연결에서는 완료 응답의 암호화 추론을 요약 유무와 관계없이 보존. 응답 단계(`phase`)·항목 순서도 도구 왕복과 세션 저장·재개 후 재전송. 기존 API 키 어댑터는 그대로 유지 |
| 인증·한도 오류 | 기존 API 키 프로필·풀 설정에 따라 처리 | ChatGPT 프로필은 오류를 표시하고 다른 API 키 과금 경로로 자동 전환하지 않음 |
| 요약·압축 요청 | 제공자에게 출력 토큰 상한·샘플링 설정 전달 | ChatGPT 보조 요청에 짧은 출력 지시와 로컬 시간·수신 텍스트 길이 제한 추가. 제한 초과 시 같은 요약 호출을 자동 재시도하지 않음 |
| 도구 실행 판정 | 모델 응답을 판정으로 해석 | JSON 형식·완료 상태·수신량을 검증. 잘못된 응답·수신량 초과·중도 종료는 허용 설정과 관계없이 거부 |
| 화면·안내 | 다국어 화면과 원본 설치·기능 문서 | 기존 언어 체계를 유지하면서 한국어 계정 연결 UI와 개인 PC 설치 안내 추가 |

## 설치와 실행

현재 소스 폴더에서 **개인 PC의 Linux 또는 WSL**을 기준으로 빌드합니다. Go 1.26.3 이상, Node.js 24.15 이상과 npm, `rsync`, PostgreSQL 16이 필요합니다. 아래 DB 예제를 쓰려면 Docker도 필요합니다. 기존 원본 배포 이미지에는 이 포크의 ChatGPT 연결 코드가 포함되어 있지 않습니다.

1. PostgreSQL을 준비합니다. 이미 사용하는 PostgreSQL 16이 있으면 이 단계는 건너뛰고 해당 연결 정보를 사용하세요. 예제의 `CHANGE_ME_LOCAL_PASSWORD`를 직접 정한 영문·숫자 비밀번호로 바꿉니다.

   ```sh
   docker run -d --name artex-chatgpt-postgres \
     -e POSTGRES_USER=artex \
     -e POSTGRES_PASSWORD=CHANGE_ME_LOCAL_PASSWORD \
     -e POSTGRES_DB=artex \
     -p 127.0.0.1:5433:5432 \
     -v artex-chatgpt-pg:/var/lib/postgresql/data \
     postgres:16
   docker exec artex-chatgpt-postgres pg_isready -U artex -d artex
   ```

   `accepting connections`가 나오면 준비된 상태입니다. 데이터는 Docker 볼륨에 남습니다. 나중에 컨테이너를 다시 시작할 때는 `docker start artex-chatgpt-postgres`를 사용합니다.

2. 웹과 실행 파일을 빌드합니다. 스크립트가 `web/`에서 `npm ci`와 정적 빌드를 실행하고 웹 화면을 실행 파일에 포함합니다.

   ```sh
   ARTEX_OUTPUT=./artex ARTEX_BUILD_VERSION=chatgpt-local ./build.sh --no-compress
   ```

3. DB 주소를 설정하고 실행합니다. 위 단계에서 바꾼 비밀번호를 동일하게 넣으세요.

   ```sh
   export ARTEX_PG_DSN='postgres://artex:CHANGE_ME_LOCAL_PASSWORD@127.0.0.1:5433/artex?sslmode=disable'
   ARTEX_LANG=ko ./start.sh
   ```

   환경 변수 대신 [config.example.json](config.example.json)을 `config.json`으로 복사해 `database`를 채워도 됩니다. `language`는 `ko`, `skill_dir`는 `./skills`로 둡니다. 두 방식이 함께 있으면 `ARTEX_PG_DSN`이 우선합니다.

4. 같은 PC에서 [http://127.0.0.1:8787](http://127.0.0.1:8787)을 엽니다. 첫 실행에서는 서버 콘솔의 `[auth]` 설정 토큰을 입력하고 **ARTEX 관리자 비밀번호**를 만듭니다. ChatGPT 연결과는 별개입니다. 화면 언어는 시스템 설정에서 한국어를 선택할 수 있습니다.

`Ctrl+C`로 종료합니다. 주소를 바꿀 때는 `ARTEX_LANG=ko ./start.sh -addr 127.0.0.1:9000`처럼 인자를 전달합니다. ChatGPT 연결 관리는 서버와 브라우저가 같은 PC에 있는 구성을 지원합니다. 원격 서버·컨테이너에서의 OAuth 연결은 이 안내의 지원 범위에 포함되지 않습니다.

## ChatGPT 연결

1. **시스템 → LLM**에서 **ChatGPT로 계속**을 누릅니다. 새 창이 차단되면 **로그인 화면 열기** 링크를 사용합니다.
2. OpenAI 화면에서 계정을 선택하고 이 앱의 ChatGPT 요금제 사용에 동의합니다. 로그인 완료 후 ARTEX 화면의 계정 상태가 **연결됨**으로 바뀌는지 확인합니다. 요금제 사용 권한이 없으면 모델을 사용할 수 없습니다.
3. 모델 프로필을 새로 만들고 **모델 연결 방식 → ChatGPT 구독 계정**을 선택합니다. 연결된 계정을 고른 다음 모델 목록 새로고침 버튼으로 **계정에서 제공하는 모델**을 불러와 선택합니다.
4. 이름을 입력해 저장한 뒤 카드의 **활성으로 설정**을 누릅니다. **연결 테스트**는 도구 없는 모델 요청을 한 번 보내며, 30초 제한과 재시도 없음이 적용됩니다. 실제 모델 호출이므로 요금제 사용량에 포함됩니다.

사용량과 앱 접근 권한은 ChatGPT 설정에서 확인합니다. 인증 만료 시 계정의 **다시 연결**, 사용 중단 시 **연결 해제**를 누릅니다. 연결 해제는 로컬 토큰을 지우고 원격 세션 폐기를 시도하며, 재연결에 필요한 등록 ID는 유지합니다. 원격 폐기 실패 경고가 나오면 ChatGPT 설정에서도 앱 접근을 해제하세요. [OpenAI 계정·세션 안내](https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions)

추론용 access token과 refresh token은 브라우저나 PostgreSQL에 저장하지 않고 실행한 OS 사용자의 설정 폴더 아래 `artex-on-chatgpt/`에 보관합니다. Linux·WSL에서는 일반적으로 `~/.config/artex-on-chatgpt/`이며 `XDG_CONFIG_HOME`을 설정했다면 그 경로를 따릅니다. 폴더 권한은 `0700`, 자격증명 파일은 `0600`입니다. 이 폴더를 Git에 올리거나 일반 프로젝트 백업에 넣지 마세요.

## 첫 사용 확인

1. **시스템 → LLM**에서 사용할 ChatGPT 프로필이 **활성됨**인지 확인합니다.
2. **기능 → 대화 → 새 대화**를 누릅니다. 에이전트는 **Auto**로 두고, 입력창 아래 **변경**에서 해당 ChatGPT 프로필을 선택합니다.
3. `도구 없이 “연결 확인 완료”라고 답하세요. 이 대화의 기억값은 사과-42입니다.`를 입력하고 Enter로 보냅니다. 텍스트 응답이 나오는지 확인합니다.
4. 같은 대화에서 `Bash 도구로 printf 'ARTEX_TOOL_OK\n' 명령만 한 번 실행하고 결과를 알려주세요. 파일 변경이나 네트워크 요청은 하지 마세요.`를 보냅니다. 도구 호출·출력에 `ARTEX_TOOL_OK`가 나타나는지 확인합니다. 중단하려면 **이번 실행 중지**를 누릅니다.
5. 페이지를 새로고침하고 왼쪽에서 **같은 대화**를 선택합니다. `앞서 정한 기억값을 도구 없이 알려주세요.`에 `사과-42`라고 답하면 대화 재개가 동작한 것입니다. 서버를 재시작한 뒤 같은 확인을 반복하면 저장된 세션 복원도 확인할 수 있습니다.

이 확인 과정도 ChatGPT 요금제 사용량을 소비합니다.

## ChatGPT 연결에서 달라지는 점

아래 제약은 ChatGPT 요금제 연결에 적용됩니다. API 키 프로필에는 기존 제공자 설정을 계속 사용할 수 있습니다. 서버가 지원하지 않는 옵션을 로컬 처리로 동일하게 복원할 수는 없습니다. [OpenAI 미리보기 제한사항](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations)

| 항목 | 기존 API 키 방식 | ChatGPT 계정 방식과 현재 대응 |
| --- | --- | --- |
| `temperature` | 지원하는 API·모델에는 호출별 값을 전달 | 요청에서 제외. 보조 요청에는 간결한 출력을 지시하고 판정 결과의 JSON·허용값을 검증하지만 항상 같은 응답을 보장하지 않음 |
| 출력 토큰 상한 | API 형식에 맞춰 `max_tokens`·`max_completion_tokens`·`max_output_tokens` 전달 | 서버 상한 설정 미지원. 요약·판정 보조 요청은 로컬 시간·수신 텍스트 길이를 제한하지만 서버의 생성 토큰 수·사용량 상한은 보장하지 않음 |
| 추론 설정 | API별 `thinking`·추론 강도 설정 사용 | 모델이 지원하는 추론 강도는 사용. 요약·압축에서 추론 토큰 사용을 완전히 끈다고 보장하지 않음 |
| 응답 전송 | 스트리밍 또는 비스트리밍 | HTTP/SSE 스트리밍만 사용 |
| 도구·입력 | 기존 제공자와 Norma 지원 범위 | 텍스트와 로컬 function·MCP 도구 지원. 이미지·파일 입력과 OpenAI 호스팅 도구는 지원하지 않음 |
| 추론 이력의 수명 | 제공자마다 재사용 방식이 다름 | 원형을 유지한 메시지의 응답 상태를 보존. 압축·편집·도구 이력 정리로 내용이 바뀌면 해당 상태도 제거. 기존 Anthropic 추론 이력은 변환하지 않음 |
| 모델·사용량 | API 제공자와 계정의 한도 적용 | ChatGPT 계정·요금제의 모델 목록과 한도 적용. 오류 시 API 키로 자동 전환하지 않음 |

**다른 제공자에서 전환하면 새 대화를 시작하세요.** 로컬 MCP 실행은 OpenAI 호스팅 MCP와 별개입니다. 기존 모델과 같은 응답 품질·속도를 보장하지 않으며 정량 성능·사용량 한도 검증은 아직 수행하지 않았습니다.

보조 요청 제한은 도구가 없고 호출자가 추론 비활성화와 출력 토큰 수를 요청한 경우에만 적용됩니다. 요청 전체를 90초로 제한하고, 받은 텍스트·추론 요약은 요청 크기에 따라 8–64KiB로 제한합니다. 초과 시 호출을 취소하고 오류로 처리합니다. 일반 도구 실행 턴에는 이 제한을 새로 적용하지 않습니다. 주 대화 압축은 완성된 요약을 얻었을 때만 적용하며, 이 장치로 모델의 기본 추론이나 이미 발생한 서버 사용량을 없앨 수는 없습니다.

도구 실행 판정의 검증 강화는 API 키 방식에도 적용됩니다. 다만 응답을 받기 전 연결 실패에는 기존 판정 실패 정책이 적용되므로, 실패 시 허용으로 설정했다면 허용될 수 있습니다.

## 출처와 라이선스

원본은 [Autumn-27/ARTEX](https://github.com/Autumn-27/ARTEX), 한국어 기반판은 [jiwoochris/artex-global](https://github.com/jiwoochris/artex-global)입니다.

루트 [GNU AGPL v3 LICENSE](LICENSE)와 웹 코드의 [MIT 라이선스·저작권 고지](web/LICENSE)를 원문 그대로 보존합니다. 별도 고지가 있는 제3자 코드의 라이선스를 변경하지 않습니다.
