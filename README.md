# Artex-on-ChatGPT

**개인 ChatGPT 구독 또는 OpenAI API 키로 사용하는 한국어 ARTEX 포크입니다.**

한국어 기반판 [artex-global](https://github.com/jiwoochris/artex-global)에 OpenAI의 공식 [Sign in with ChatGPT](https://developers.openai.com/siwc/token-sharing-open-source/sign-in) 연결을 추가하고, 모델 통신을 **OpenAI Responses**로 통합했습니다. 기존 Norma 엔진의 작업·도구·MCP·대화 압축을 유지합니다. ChatGPT 연결에는 Codex 설치나 API 키가 필요하지 않습니다.

> 현재 안내는 **개인 PC의 Linux·WSL 로컬 실행** 기준입니다. 1차 변경은 `KMINGON/openai-migration` 브랜치에 있습니다. ARTEX 관리자 로그인과 ChatGPT 계정 연결은 별개입니다.

[빠른 시작](#빠른-시작) · [ChatGPT 연결](#chatgpt-구독으로-연결) · [API 키 연결](#openai-api-키로-연결) · [인증별 차이](#인증별-차이) · [원본 사용법](https://github.com/jiwoochris/artex-global/blob/main/README.ko.md)

## 원본 대비 주요 변경점

| 구분 | 원본·기존 경로 | 이 포크 |
| --- | --- | --- |
| 개인 계정 | 제공자 API 키 | **ChatGPT 구독 로그인 추가**, OpenAI API 키도 유지 |
| 모델 연결 | Claude·OpenAI 등 여러 제공자와 프로토콜 | **OpenAI 두 인증 방식 + Responses**로 통합. Claude·Chat Completions·임의 LLM 주소·전용 설정 제거 |
| OpenAI 컨텍스트 | 텍스트·도구 이력은 유지하지만 기존 API 어댑터에서 opaque reasoning·`phase` 누락 | 구독 경로의 **암호화 추론·응답 단계·항목 순서 보존을 API에도 적용**. 도구 왕복과 저장·재개에서 재전송 |
| 대화 압축 | 일반 압축·Noa는 공급자 공통 기능 | 기존 구조 유지. **잘린 요약을 성공으로 저장하지 않도록 보완**, 요약 실패 시 기존 그래프 요약 보존 |
| 완료·도구 판정 | 제공자별 처리 | 정상 완료와 도구 인수 검증 뒤 함수 호출 확정. 불완전한 도구·Judge 응답 차단 |
| 오류 진단 | HTTP 200 내부 실패가 일반 오류로만 표시되는 경우 | SSE 오류 코드·단계·요청 ID·요청 크기 등을 안전하게 기록. 출력·도구 실행 후 전체 작업을 자동 재실행하지 않음 |
| 연결 변경 | 같은 대화 이력을 다시 사용하는 경로 | 관측한 계정·API 키·모델 변경 시 새 컨텍스트 시작. 이전 대화 파일과 작업 데이터 보존 |
| 실행 기능 | Norma 기반 작업·로컬 도구·MCP·Skills | 유지. 검색은 DDGS·Brave·Tavily 유지, DeepSeek 전용 검색 제거 |

**컨텍스트 개선은 모델 자체의 성능 향상을 보장하는 기능이 아닙니다.** 원형 메시지의 서버 반환 상태를 보존하는 수정입니다. 압축·편집으로 내용이 바뀌면 해당 opaque 상태를 제거하며, 과거에 저장하지 않은 상태나 Claude 추론을 복원하지 않습니다. 기존 API에서도 일반 대화 압축과 Noa는 동작했습니다.

## 빠른 시작

준비물: **Go 1.26.3 이상, Node.js 24.15 이상·npm, rsync, PostgreSQL 16**. 아래 PostgreSQL 예제를 쓰려면 Docker도 필요합니다.

### 1. 소스 받기

```sh
git clone --branch KMINGON/openai-migration https://github.com/KMINGON/Artex-on-ChatGPT.git
cd Artex-on-ChatGPT
```

이미 해당 브랜치의 워크트리에 있다면 그 폴더에서 이어서 진행합니다. 기존 원본 배포 이미지에는 이 변경이 포함되어 있지 않습니다.

### 2. PostgreSQL 준비

`CHANGE_ME_LOCAL_PASSWORD`를 직접 정한 영문·숫자 비밀번호로 바꿉니다. 기존 PostgreSQL을 사용한다면 해당 연결 정보로 다음 단계부터 진행하세요.

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

**`accepting connections`가 나오면 준비 완료입니다.** 첫 초기화 중 `no response`가 나오면 잠시 후 확인 명령을 다시 실행하세요. 컨테이너를 나중에 다시 시작할 때는 `docker start artex-chatgpt-postgres`를 사용합니다.

### 3. 빌드하고 실행

빌드 스크립트가 웹 의존성 설치·정적 빌드 후 화면을 실행 파일에 포함합니다.

```sh
ARTEX_OUTPUT=./artex ARTEX_BUILD_VERSION=openai-local ./build.sh --no-compress

export ARTEX_PG_DSN='postgres://artex:CHANGE_ME_LOCAL_PASSWORD@127.0.0.1:5433/artex?sslmode=disable'
ARTEX_LANG=ko ./start.sh
```

[http://127.0.0.1:8787](http://127.0.0.1:8787)을 열고 서버 콘솔의 `[auth]` 설정 토큰으로 **ARTEX 관리자 비밀번호**를 만듭니다. 환경 변수 대신 [config.example.json](config.example.json)을 `config.json`으로 복사해 DB 정보를 설정할 수도 있습니다. 둘 다 있으면 `ARTEX_PG_DSN`이 우선합니다.

종료는 `Ctrl+C`, DB 정지는 `docker stop artex-chatgpt-postgres`입니다. 데이터는 프로젝트 `data/`와 PostgreSQL 볼륨에 남습니다. 포트를 바꾸려면 `ARTEX_LANG=ko ./start.sh -addr 127.0.0.1:9000`처럼 실행합니다.

## ChatGPT 구독으로 연결

1. **시스템 → LLM → ChatGPT 계정 연결**에서 **ChatGPT로 계속**을 누릅니다. 팝업이 차단되면 **로그인 화면 열기**를 사용합니다.
2. OpenAI 화면에서 계정을 선택하고 요금제 사용에 동의합니다. ARTEX에서 **연결됨**을 확인합니다.
3. 모델 프로필을 추가하고 **모델 연결 방식 → ChatGPT 구독 계정**을 선택합니다. 계정을 고르고 목록을 새로고침해 **계정에 제공되는 모델**을 선택합니다.
4. 이름을 입력해 저장하고 **활성으로 설정 → 연결 테스트**를 실행합니다.

연결 테스트는 도구 없는 실제 모델 요청 한 번입니다. **30초 제한·재시도 없음**이 적용되며 구독 사용량을 소비합니다. 모델·effort 기본값을 이 포크가 임의로 최신 모델이나 높은 강도로 바꾸지는 않습니다.

추론용 access token과 refresh token은 브라우저나 PostgreSQL에 저장하지 않고 OS 사용자 설정 폴더에 저장합니다. Linux·WSL의 기본 경로는 `~/.config/artex-on-chatgpt/`이며 `XDG_CONFIG_HOME` 설정을 따릅니다. 폴더는 `0700`, 자격증명 파일은 `0600` 권한입니다. 연결 관리는 서버와 브라우저가 **같은 PC**에 있는 구성을 지원합니다.

인증 만료 시 **다시 연결**, 사용 중단 시 **연결 해제**를 누릅니다. 원격 세션 폐기 실패가 안내되면 ChatGPT 설정에서도 앱 접근을 해제하세요. [OpenAI 계정·세션 안내](https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions)

## OpenAI API 키로 연결

1. **시스템 → LLM**에서 프로필을 추가하고 **OpenAI API 키** 방식을 선택합니다.
2. API 키와 사용할 모델을 입력하거나 모델 목록에서 선택합니다. 필요한 경우 프록시·추론 강도·출력 상한을 설정합니다.
3. 저장 후 **활성으로 설정 → 연결 테스트**로 확인합니다.

공식 `api.openai.com`으로 연결하며, API 사용량은 **ChatGPT 구독과 별도로 과금**됩니다. 이전 Claude·타사 주소 프로필은 삭제하지 않고 설정 변경이 필요하다고 표시합니다. 해당 프로필을 명시적으로 수정하거나 새 OpenAI 프로필을 선택하세요. 지원하지 않는 모델을 다른 모델로 자동 치환하지 않습니다.

## 첫 사용 확인

1. **기능 → 대화 → 새 대화**를 열고 사용할 프로필을 선택합니다.
2. `도구 없이 “연결 확인 완료”라고 답하세요. 이 대화의 기억값은 사과-42입니다.`를 보냅니다.
3. 같은 대화에서 `Bash 도구로 printf 'ARTEX_TOOL_OK\n' 명령만 한 번 실행하고 결과를 알려주세요. 파일 변경이나 네트워크 요청은 하지 마세요.`를 보냅니다.
4. 페이지를 새로고침하고 같은 대화에서 기억값을 묻습니다. 서버 재시작 후에도 같은 확인을 반복할 수 있습니다.

이 과정도 모델 사용량을 소비합니다. 기억값 응답만으로 opaque 상태 보존을 검증할 수는 없으며, 자동 테스트에서는 다음 요청에 포함된 항목·순서·도구 ID도 검사합니다. 프로젝트·작업 생성과 상세 도구 사용법은 [원본 한국어 안내](https://github.com/jiwoochris/artex-global/blob/main/README.ko.md)를 참고하세요.

## 인증별 차이

| 항목 | OpenAI API 키 | ChatGPT 구독 |
| --- | --- | --- |
| 사용량·과금 | API 계정의 과금·한도 | 연결한 ChatGPT 계정·요금제의 한도 |
| `temperature` | 내부 요청값을 지원 모델에 전달. 모델별 지원 조건 적용 | 전송하지 않음. 동일 응답·무작위성 제어를 로컬에서 복원할 수 없음 |
| `max_output_tokens` | 서버 출력 상한 전달. 일반 텍스트의 상한 종료는 기존 이어쓰기 경로 사용 | 서버 출력 상한 미지원. 보조 요청에 간결한 출력 지시와 로컬 수신 제한 적용 |
| 추론 강도 | 선택 모델이 지원하는 effort | 계정에서 제공하는 모델이 지원하는 effort |
| 응답 전송 | SSE 스트리밍; 내부 `Complete`도 SSE 결과를 모아 반환 | 동일 |
| 도구·입력 | 텍스트·로컬 function·MCP 도구 | 동일. 현재 포크는 이미지·파일 직접 입력과 OpenAI 호스팅 도구를 구현하지 않음 |
| 추론 상태 | 완료 응답의 reasoning·`phase`·항목 순서 보존 | 동일한 어댑터·v1 저장 형식 사용 |
| 오류 시 인증 전환 | 기존 명시적 API 프로필 전환 정책 유지. opaque 상태가 있으면 다른 연결로 자동 재전송 차단 | 유료 API 키 경로로 자동 전환하지 않음 |

구독의 보조 요청 제한은 **도구가 없고 호출자가 추론 비활성화와 출력 토큰 수를 요청한 경우**에 적용합니다. 요청 시간은 90초, 수신 텍스트·추론 요약은 요청 크기에 따라 8–64KiB로 제한합니다. 이는 서버의 생성 토큰·사용량 상한이나 추론 완전 비활성화를 보장하지 않습니다. [OpenAI 미리보기 제한사항](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations)

계정·API 키·모델 변경이 관측되면 세션의 로컬 연결 기록을 기준으로 새 컨텍스트를 시작합니다. 변경 전 파일·작업 데이터는 남지만 모델의 이전 대화 기억은 이어지지 않습니다. 연결 기록이 없는 과거 v1 이력의 원래 계정은 추정하지 않습니다. API 풀에서 다른 연결로 전환한 응답은 표시용 내용·도구 이력만 유지하며 opaque 상태를 제거합니다.

HTTP 200이라도 SSE의 `response.failed`·`error` 이벤트로 모델 요청이 실패할 수 있습니다. 이 경우 오류 코드·요청 ID 등 진단 정보를 확인하세요. `Output too large` 파일 저장 안내만으로 컨텍스트 초과를 뜻하지는 않습니다. 원본 HTTP 본문·암호화 추론·자격증명은 두 인증 모두 디버그 녹화에 남기지 않습니다.

## 검증 범위

변경 경로는 모의 OpenAI SSE를 사용해 두 인증의 요청 형식, 추론 상태 왕복, 긴 도구 결과, 실패·미완료·취소, 실제 로컬 Shell 실행과 저장·재개를 검사합니다. DB 검사는 패키지별로 분리한 PostgreSQL 테스트 환경을 사용합니다.

1차 구현 검증 결과:

- 루트 Go 전체 테스트·`go vet` 및 패키지별 격리 DB 통합 검사 통과
- 변경 경로의 race 검사, 웹 테스트 24개·타입/정적 빌드·i18n·한자 누출·린트 검사 통과
- Linux 실행 파일 빌드 및 별도 포트·DB에서 관리자 로그인·설정 API·내장 웹 페이지 응답 확인

Norma 전체 검사에는 변경 전 코드에서도 재현되는 **Noa·noaadapter 테스트 실패 7건**이 남아 있습니다. 이번 범위에서 수정한 LLM·도구·세션·압축 검사는 통과했으며, Noa 알고리즘과 문맥 크기 조정은 후속 범위로 남깁니다.

이번 변경 후 실제 ChatGPT 계정·API 키로 하는 유료/구독 모델 호출과 장시간 실사용 시험은 별도 확인이 필요합니다. 정량 성능 향상이나 특정 모델의 접근 권한을 보장하지 않습니다. Daybreak 토글·모델 업그레이드·Noa 문맥 크기 조정·중국어 내부 프롬프트 번역은 이번 변경에 포함하지 않았습니다.

## 출처와 라이선스

원본 [Autumn-27/ARTEX](https://github.com/Autumn-27/ARTEX), 한국어 기반판 [jiwoochris/artex-global](https://github.com/jiwoochris/artex-global)의 [`fdfb7a1`](https://github.com/jiwoochris/artex-global/commit/fdfb7a17b7da5e2de716c0e98475ec77a41c64c6)에서 출발했습니다. 기능·운영 문서와 원저자 고지는 원본 저장소를 참고하세요.

루트 [GNU AGPL v3](LICENSE), 웹 코드의 [MIT 라이선스·저작권 고지](web/LICENSE), 제3자 코드의 개별 라이선스를 보존합니다. 원저자의 [사용 제한·면책 고지](https://github.com/jiwoochris/artex-global/blob/fdfb7a17b7da5e2de716c0e98475ec77a41c64c6/README.zh.md#许可与免责声明)를 확인하고 허가된 환경에서 사용하세요.
