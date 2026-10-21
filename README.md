# Artex-on-ChatGPT

**개인 ChatGPT 구독 또는 OpenAI API 키로 사용하는 한국어 ARTEX 포크입니다.**

한국어 기반판 [artex-global](https://github.com/jiwoochris/artex-global)에 OpenAI의 공식 [Sign in with ChatGPT](https://developers.openai.com/siwc/token-sharing-open-source/sign-in) 연결을 추가하고, 모델 통신을 **OpenAI Responses**로 통합했습니다. 기존 Norma 엔진의 작업·도구·MCP·대화 압축을 유지합니다. ChatGPT 연결에는 Codex 설치나 API 키가 필요하지 않습니다.

> 기본 실행 방법은 **개인 PC의 Docker Compose**입니다. Linux 또는 WSL에서 사용하며, Docker Desktop을 쓰는 경우 먼저 실행해 두세요. ARTEX 관리자 로그인과 ChatGPT 계정 연결은 별개입니다.

[빠른 시작](#빠른-시작) · [중지·업데이트·데이터](#중지업데이트데이터) · [ChatGPT 연결](#chatgpt-구독으로-연결) · [API 키 연결](#openai-api-키로-연결) · [상세 변경점·인증 차이](docs/changes.md) · [원본 사용법](https://github.com/jiwoochris/artex-global/blob/main/README.ko.md)

## 원본 대비 주요 변경점

| 변경 | 핵심 내용 |
| --- | --- |
| **간편 설치** | Compose가 앱과 PostgreSQL을 준비. 기본 관리자 계정으로 바로 로그인 |
| **ChatGPT 개인 계정** | 공식 구독 로그인 추가. OpenAI API 키도 함께 지원 |
| **OpenAI 전용 연결** | Claude와 관련 설정 제거, 두 인증을 Responses로 통합 |
| **컨텍스트 보존** | OpenAI API에도 reasoning·`phase` 보존 적용. 도구 결과 압축과 저장·재개 오류 수정 |
| **실행 안정성** | 취소 뒤 대기 도구 차단, 부분 출력을 미완료로 표시, 실패 사용량과 구독 한도 처리 개선 |
| **설정·자동화** | 프로필 적용 경합 수정, 트리거 대기열 저장, API Daybreak 선택 및 구독 모델 선택 보완 |

**[원본과의 비교, 상세 개선점, Daybreak 및 인증별 제약 →](docs/changes.md)**

기존 API에도 일반 대화 압축·Noa가 있었습니다. 이번 개선은 누락된 응답 상태와 실행 결함을 보완하며, 모델 자체의 성능 향상을 보장하지 않습니다. Noa 기능은 유지합니다.

## 빠른 시작

준비물은 **Git과 Docker Compose**입니다. Go·Node.js·PostgreSQL은 이미지 빌드와 컨테이너에서 준비하므로 호스트에 따로 설치하지 않아도 됩니다.

### 1. 소스 받고 실행

```sh
git clone --branch master https://github.com/KMINGON/Artex-on-ChatGPT.git
cd Artex-on-ChatGPT
docker compose up -d
```

첫 실행에는 현재 소스로 웹 화면과 앱 이미지를 빌드하므로 시간이 걸립니다. PostgreSQL이 준비되면 앱이 시작합니다. 진행 상태는 `docker compose logs -f artex`로 확인할 수 있습니다.

### 2. ARTEX 로그인

[http://127.0.0.1:8787](http://127.0.0.1:8787)을 열고 다음 값으로 로그인합니다.

| 항목 | 기본값 |
| --- | --- |
| 관리자 계정 | `ARTEX` |
| 관리자 비밀번호 | `artex1234` |

처음 사용하는 DB에는 관리자 비밀번호가 자동으로 설정되어 설정 토큰을 복사할 필요가 없습니다. **기존 DB의 관리자 비밀번호는 유지**됩니다. 기본 비밀번호는 로컬 실행용이며, 외부에 공개하기 전에는 변경해야 합니다. 기본 웹 포트는 이 PC의 `127.0.0.1`에만 열리고 DB 포트는 호스트에 공개하지 않습니다.

### 3. 사용할 모델 연결

**시스템 → LLM**에서 [ChatGPT 구독 계정](#chatgpt-구독으로-연결) 또는 [OpenAI API 키](#openai-api-키로-연결)를 연결하고 모델 프로필을 저장·활성화합니다. 기존 연결·프로필은 재실행해도 보존됩니다.

<details>
<summary>포트·초기 비밀번호 등을 바꾸려면 (.env 선택 사항)</summary>

기본값을 쓰면 `.env`는 필요하지 않습니다. 처음 실행하기 전에 [.env.example](.env.example)을 참고해 `.env`에 바꿀 값만 넣을 수 있습니다. 기존 `.env`가 있다면 덮어쓰지 말고 수정하세요.

| 변수 | 기본값 | 용도 |
| --- | --- | --- |
| `ARTEX_PORT` | `8787` | 호스트 웹 포트 |
| `ARTEX_ADMIN_PASSWORD` | `artex1234` | 관리자 비밀번호가 없는 DB의 최초 비밀번호 |
| `POSTGRES_USER` | `artex` | 새 DB의 사용자 |
| `POSTGRES_PASSWORD` | `artex-local-db` | 새 DB의 비밀번호 |
| `POSTGRES_DB` | `artex` | 새 DB 이름 |
| `ARTEX_LANG` | `ko` | 기본 출력 언어 |

예를 들어 `ARTEX_PORT=9000`이면 `http://127.0.0.1:9000`으로 접속합니다. ChatGPT 로그인 콜백도 같은 웹 포트를 사용합니다.

`ARTEX_ADMIN_PASSWORD`를 바꿔도 이미 설정된 관리자 비밀번호는 초기화되지 않습니다. 기존 비밀번호는 ARTEX 화면에서 변경하세요. 기존 PostgreSQL 볼륨에도 새 환경 변수로 계정·비밀번호가 다시 설정되지 않으므로 기존 DB와 맞는 값을 유지해야 합니다.

</details>

## 중지·업데이트·데이터

중지와 재실행:

```sh
docker compose stop
docker compose up -d
```

소스를 업데이트한 뒤에는 이미지를 다시 빌드합니다. 업데이트 전에는 DB와 앱·인증 볼륨을 백업하세요.

```sh
git pull --ff-only
docker compose up -d --build
```

| Compose 볼륨 | 내용 |
| --- | --- |
| `pgdata` | PostgreSQL의 프로필·작업·대화 데이터 |
| `artex-data` | 앱의 파일·도구 결과·대화 이력 (`/app/data`) |
| `artex-auth` | 관리자 세션 서명 키와 ChatGPT 인증 저장소 (`/app/auth`) |

같은 폴더·Compose 프로젝트 이름으로 실행하면 기존 볼륨을 재사용합니다. `docker compose down`도 볼륨은 보존하지만, **`docker compose down -v`는 DB·앱·인증 볼륨을 삭제**하므로 데이터를 지울 때만 사용하세요.

기존 호스트 설치의 DB·`data/`·ChatGPT 인증은 자동으로 이관하지 않습니다. 기존 Compose의 `pgdata`는 같은 프로젝트 이름으로 유지합니다. 앱의 Bash·로컬 도구는 컨테이너 안에서 실행됩니다.

<details>
<summary>Docker 앱 대신 기존 호스트 설치를 계속 사용하려면</summary>

호스트 설치에는 **Go 1.26.3 이상, Node.js 24.15 이상·npm, rsync, PostgreSQL 16**이 필요합니다. 소스 폴더에서 다음을 진행하세요. 이 경로는 기존 설정 토큰 방식으로 관리자 비밀번호를 만듭니다.

**PostgreSQL 준비**

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

**빌드하고 실행**

빌드 스크립트가 웹 의존성 설치·정적 빌드 후 화면을 실행 파일에 포함합니다.

```sh
ARTEX_OUTPUT=./artex ARTEX_BUILD_VERSION=openai-local ./build.sh --no-compress

export ARTEX_PG_DSN='postgres://artex:CHANGE_ME_LOCAL_PASSWORD@127.0.0.1:5433/artex?sslmode=disable'
ARTEX_LANG=ko ./start.sh
```

[http://127.0.0.1:8787](http://127.0.0.1:8787)을 열고 서버 콘솔의 `[auth]` 설정 토큰으로 **ARTEX 관리자 비밀번호**를 만듭니다. 환경 변수 대신 [config.example.json](config.example.json)을 `config.json`으로 복사해 DB 정보를 설정할 수도 있습니다. 둘 다 있으면 `ARTEX_PG_DSN`이 우선합니다.

종료는 `Ctrl+C`, DB 정지는 `docker stop artex-chatgpt-postgres`입니다. 데이터는 프로젝트 `data/`와 PostgreSQL 볼륨에 남습니다. 포트를 바꾸려면 `ARTEX_LANG=ko ./start.sh -addr 127.0.0.1:9000`처럼 실행합니다.

</details>

## ChatGPT 구독으로 연결

1. **시스템 → LLM → ChatGPT 계정 연결**에서 **ChatGPT로 계속**을 누릅니다. 팝업이 차단되면 **로그인 화면 열기**를 사용합니다.
2. OpenAI 화면에서 계정을 선택하고 요금제 사용에 동의합니다. ARTEX에서 **연결됨**을 확인합니다.
3. 모델 프로필을 추가하고 **모델 연결 방식 → ChatGPT 구독 계정**을 선택합니다. 계정을 고르고 목록을 새로고침해 **계정에 제공되는 모델**을 선택합니다.
4. 이름을 입력해 저장하고 **활성으로 설정 → 연결 테스트**를 실행합니다.

연결 테스트는 도구 없는 실제 모델 요청 한 번입니다. **30초 제한·재시도 없음**이 적용되며 구독 사용량을 소비합니다. 모델·effort 기본값을 이 포크가 임의로 최신 모델이나 높은 강도로 바꾸지는 않습니다.

추론용 access token과 refresh token은 브라우저나 PostgreSQL에 저장하지 않습니다. Compose에서는 `artex-auth` 볼륨의 `/app/auth/artex-on-chatgpt/`에, 호스트 설치에서는 기본적으로 `~/.config/artex-on-chatgpt/`에 저장하며 `XDG_CONFIG_HOME` 설정을 따릅니다. 인증 폴더는 `0700`, 자격증명 파일은 `0600` 권한입니다.

브라우저는 Docker를 실행한 **같은 PC**에서 열어 주세요. Compose의 로그인 콜백은 앱과 같은 웹 포트의 `http://127.0.0.1:8787/auth/callback`을 사용하며, 포트를 바꾸면 그 값도 함께 바뀝니다. 이 Docker 경로의 실제 ChatGPT 계정 로그인은 아직 실검증하지 않았습니다.

인증 만료 시 **다시 연결**, 사용 중단 시 **연결 해제**를 누릅니다. 한도에 도달하면 Usage 설정을 확인한 후 **한도 재확인**을 실행합니다. 재확인은 실제 요청 1회로 사용량이 발생할 수 있습니다. 원격 세션 폐기 실패가 안내되면 ChatGPT 설정에서도 앱 접근을 해제하세요. [OpenAI 계정·세션 안내](https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions)

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

## Daybreak와 인증별 차이

API 프로필은 **모델 + API 액세스 프로그램(`standard`/`daybreak_blue`/`daybreak_red`)**을 따로 선택합니다. 서버 기본값은 필드 생략이며 Daybreak를 끈다는 뜻이 아닙니다. 조직·프로젝트의 승인이 필요합니다.

구독은 **연결 계정의 모델 목록**을 기준으로 선택합니다. 목록에 제공된 Daybreak ID는 그대로 사용하지만, 구독 요청의 명시적 프로그램 토글은 공개 SIWC 계약에서 확인되지 않아 전송하지 않습니다. [선택 방법과 확인 범위](docs/changes.md#daybreak-선택)

구독은 `temperature`·`max_output_tokens`를 전송하지 않으며 로컬 수신 제한이 서버의 생성 비용 상한을 대체하지 않습니다. [API·구독 차이표](docs/changes.md#api와-구독의-차이) · [업데이트·백업](docs/changes.md#업데이트데이터-보존) · [테스트와 남은 한계](docs/changes.md#검증-범위)

## 출처와 라이선스

원본 [Autumn-27/ARTEX](https://github.com/Autumn-27/ARTEX), 한국어 기반판 [jiwoochris/artex-global](https://github.com/jiwoochris/artex-global)의 [`fdfb7a1`](https://github.com/jiwoochris/artex-global/commit/fdfb7a17b7da5e2de716c0e98475ec77a41c64c6)에서 출발했습니다. 기능·운영 문서와 원저자 고지는 원본 저장소를 참고하세요.

루트 [GNU AGPL v3](LICENSE), 웹 코드의 [MIT 라이선스·저작권 고지](web/LICENSE), 제3자 코드의 개별 라이선스를 보존합니다. 원저자의 [사용 제한·면책 고지](https://github.com/jiwoochris/artex-global/blob/fdfb7a17b7da5e2de716c0e98475ec77a41c64c6/README.zh.md#许可与免责声明)를 확인하고 허가된 환경에서 사용하세요.
