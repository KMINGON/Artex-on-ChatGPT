<div align="center">
  <img src="web/public/logo.png" width="180" alt="ARTEX 로고" />
  <h1>Artex-on-ChatGPT</h1>
  <p><strong>개인 ChatGPT 계정으로 시작하는 한국어 ARTEX</strong></p>
  <p>ChatGPT 구독 · OpenAI API · Docker Compose</p>
</div>

<p align="center">
  <a href="#빠른-시작">빠른 시작</a> ·
  <a href="#원본-대비-변경점">원본 대비 변경점</a> ·
  <a href="docs/changes.md">상세 변경 문서</a>
</p>

---

## 프로젝트 소개

원본 ARTEX는 Claude, GPT 등 다양한 LLM 프로바이더를 지원하는 에이전트 플랫폼입니다. 다만 OpenAI API 경로에서는 응답 단계(`phase`)와 추론 상태가 대화 이력에서 누락되고, 도구 호출 전후의 컨텍스트 압축·저장·재개 과정에도 보완이 필요한 부분이 있었습니다.

이 프로젝트는 **OpenAI 단일 프로바이더와 Responses 호출 규약으로 통합**하여, 원본의 다중 에이전트·도구·MCP·컨텍스트 관리가 OpenAI에서도 일관되게 이어지도록 정비했습니다. API 키와 개인 ChatGPT 구독이 같은 실행 엔진을 사용하며, 응답 상태 보존부터 도구 실행·대화 재개까지 함께 다룹니다.

여기에 공식 [Sign in with ChatGPT](https://developers.openai.com/siwc/token-sharing-open-source/sign-in) 연결, 기본 프롬프트·규칙·오류 안내의 한국어화, **명령 한 번으로 시작하는 Docker Compose 환경**을 추가했습니다. ChatGPT 연결에는 Codex 설치나 API 키가 필요하지 않습니다.

> **개인 PC에서 바로 시작하세요.** Linux·WSL에서 Docker Compose로 실행합니다. Docker Desktop을 사용한다면 먼저 실행해 두세요. ARTEX 관리자 로그인과 ChatGPT 계정 연결은 별도 절차입니다.

## 원본 대비 변경점

| 영역 | 원본 (`fdfb7a1`) | 이 프로젝트 |
| --- | --- | --- |
| **모델 연결** | Claude·OpenAI 등 공급자별 호출 경로 | OpenAI Responses로 통합하고 Claude·타사 공급자 설정 제거 |
| **개인 계정 로그인** | API 키 중심 | **ChatGPT 구독 로그인** 추가, OpenAI API 키도 지원 |
| **대화·추론 상태** | OpenAI 응답의 reasoning 원형·`phase` 누락 | 응답 단계·추론 상태·항목 순서를 도구 왕복과 저장·재개 과정에서 보존 |
| **컨텍스트 관리** | 압축 경계의 도구 결과 누락, 초과 시 일부 원문 제외 | 도구 호출·결과를 함께 요약하고 실패 시 원문 유지, 현재 입력·도구 정의까지 반영해 압축 |
| **에이전트·그래프 연결** | 결과·자산·부모 연결의 부분 저장, 메모리에만 남는 계획 상태 | 결과 귀속과 연결을 함께 저장하고 공유 파일·최신 실행 결과·계획 재개·완료 근거를 연결 |
| **탐색과 후속 검증** | 결과 인계 800자 제한, 부정 방향 재검증·사실 기록 횟수 지침 | 결과 전문과 미완료 질문을 인계하고 같은 질문의 대조·재현·영향 확인을 이어가며, 진행 중 의도가 남은 조기 완료 방지 |
| **실행·저장 안정성** | 취소 후 대기 도구 실행, 부분 저장·출력 처리와 차단 집계의 빈틈 | 취소 전파, 완성된 체크포인트 검증, 미완료 출력·실패 사용량 구분, 셸 입력에 한정한 명령 검사와 실제 Guard 차단 집계 |
| **설정·자동화** | 프로필 변경 경합, 실행 대기 상태 유실 가능성 | 첫 사용 가능 모델의 기본 지정, 대화 모델의 작업 전달, 프로필 적용 동기화·트리거 대기열 저장 |
| **모델 선택** | 공급자별 모델 이름 조회·입력 중심 | API 키·구독 계정으로 조회 후 선택, 계정·모델별 프로그램과 추론 옵션 반영, 실제 연결 결과 표시 |
| **브라우저·이미지** | 브라우저 실행 준비·인증 처리와 이미지 결과 전달의 빈틈 | Playwright·Chromium 버전과 실행 경로 통일, 같은 브라우저 세션의 수동 로그인·재개, 스크린샷을 시각 입력으로 전달 |
| **작업·자원 설정** | 설명·목표·분류·규칙을 저장하는 작업 템플릿, 개별 도구·병렬량 설정 | 실습·운영 기본 템플릿 4종과 작업 이름·고급값 저장, 시스템 설정 상단의 Low / Medium / High 버튼 |
| **한국어·영어** | 화면 외 프롬프트·규칙·실행 안내에 중국어 잔존 | 기본 지침·오류·스킬 한국어화, 모델·브라우저·프리셋 화면 영어 지원, 두 언어만 유지 |
| **설치·운영** | 호스트 빌드·DB 준비·초기 설정 | **Docker Compose로 앱·DB·기본 관리자 계정 준비**, 내부망 웹 접속·호스트 서버 연결 |
| **프로젝트 구성** | 실험적 Noa와 원본 탐지 자료·중복 운영 경로 | 기본 자동 압축으로 통일하고 사용하지 않는 문서·스크립트·템플릿 정리 |

작업·계획·도구·MCP·Skills·탐색 그래프 요약은 유지합니다. 기존 설정을 갱신할 때는 확인된 기본값만 바꾸고 사용자가 수정한 프롬프트·규칙·템플릿은 보존합니다.

**[상세 변경점과 인증별 동작 차이 →](docs/changes.md)** · [브라우저·실행 프리셋](docs/browser-and-presets.md) · [트래픽 증거 사용법](docs/finding-traffic-evidence.md)

## 빠른 시작

준비물은 **Git과 Docker Compose**입니다. Go·Node.js·PostgreSQL은 이미지 빌드와 컨테이너에서 준비하므로 호스트에 따로 설치하지 않아도 됩니다.

### 1. 소스 받고 실행

현재 기능은 `dev` 브랜치에서 제공합니다.

```sh
git clone --branch dev https://github.com/KMINGON/Artex-on-ChatGPT.git
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

처음 사용하는 DB에는 관리자 비밀번호가 자동으로 설정되어 설정 토큰을 복사할 필요가 없습니다. **기존 DB의 관리자 비밀번호는 유지**됩니다. 웹은 기본적으로 호스트의 모든 IPv4 주소에 열리므로 다른 PC에서는 `http://실행PC의IP:8787`로 접속합니다. DB와 트래픽 기록 프록시는 호스트에 공개하지 않습니다. 기본 관리자 비밀번호는 ARTEX 화면에서 변경할 수 있습니다.

### 3. 사용할 모델 연결

**시스템 → LLM**에서 ChatGPT 구독 계정 또는 OpenAI API 키를 연결하고 모델 프로필을 저장합니다. 기본 모델이 없는 첫 설정에서는 사용 가능한 프로필을 자동으로 활성화합니다. 기존 기본 모델을 바꾸려면 해당 프로필의 활성화 버튼을 사용합니다. 기존 연결·프로필은 재실행해도 보존됩니다.

### 접속 주소

| 목적 | 주소 |
| --- | --- |
| 실행 PC에서 ARTEX 열기 | `http://127.0.0.1:8787` |
| 다른 PC에서 ARTEX 열기 | `http://실행PC의IP:8787` |
| ARTEX에서 같은 Docker 네트워크의 서버 접근 | `http://컨테이너명:내부포트` |
| ARTEX에서 Docker 호스트의 서버 접근 | `http://host.docker.internal:포트` |
| ARTEX에서 내부망·외부 웹 접근 | 대상 서버의 실제 IP·도메인과 포트 |

에이전트에 전달하는 `127.0.0.1`은 **ARTEX 컨테이너 자신**입니다. 예를 들어 ARTEX 실행 후 Juice Shop을 같은 Docker 엔진에서 다음과 같이 실행합니다.

```sh
docker run -d --name juice-shop \
  --network artex-on-chatgpt_default \
  -p 127.0.0.1:3000:3000 \
  bkimminich/juice-shop:latest
```

PC 브라우저에서는 `http://127.0.0.1:3000`, ARTEX에는 **`http://juice-shop:3000`**을 전달합니다. Compose 프로젝트 이름을 바꿨다면 네트워크 이름도 `<프로젝트명>_default`로 맞춥니다. 이미 만든 컨테이너는 `docker network connect artex-on-chatgpt_default 컨테이너명`으로 연결할 수 있습니다.

호스트 서버는 Docker에서 도달할 수 있는 주소에서 수신해야 합니다. Linux에서 `127.0.0.1`에만 열린 서버는 위 호스트 이름만 추가해도 접근되지 않습니다. WSL 내부 Docker의 호스트는 WSL이며, Windows 서버는 Windows 호스트 IP로 접근합니다. 다른 PC·망에서 접속하려면 해당 호스트의 방화벽·라우팅도 연결을 허용해야 합니다.

ChatGPT 계정 연결·재로그인은 실행 PC의 **`http://127.0.0.1:8787`**에서 진행합니다. 연결된 모델로 작업하는 화면은 내부망 주소에서도 사용할 수 있습니다.

<details>
<summary>포트·초기 비밀번호 등을 바꾸려면 (.env 선택 사항)</summary>

기본값을 쓰면 `.env`는 필요하지 않습니다. 처음 실행하기 전에 [.env.example](.env.example)을 참고해 `.env`에 바꿀 값만 넣을 수 있습니다. 기존 `.env`가 있다면 덮어쓰지 말고 수정하세요.

| 변수 | 기본값 | 용도 |
| --- | --- | --- |
| `ARTEX_BIND_IP` | `0.0.0.0` | 웹 공개 주소. 이 PC 전용은 `127.0.0.1` |
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
| `artex-auth` | 관리자 세션 서명 키, ChatGPT 인증, 작업별 브라우저 프로필 (`/app/auth`) |

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

## 출처와 라이선스

원본 [Autumn-27/ARTEX](https://github.com/Autumn-27/ARTEX), 한국어 기반판 [jiwoochris/artex-global](https://github.com/jiwoochris/artex-global)의 [`fdfb7a1`](https://github.com/jiwoochris/artex-global/commit/fdfb7a17b7da5e2de716c0e98475ec77a41c64c6)에서 출발했습니다. 원본 링크는 출처 기록이며, 현재 설치·운영 안내는 이 저장소의 문서를 기준으로 합니다.

루트 [GNU AGPL v3](LICENSE), 웹 코드의 [MIT 라이선스·저작권 고지](web/LICENSE), 제3자 코드의 개별 라이선스를 보존합니다. 원저자의 [사용 제한·면책 고지](https://github.com/jiwoochris/artex-global/blob/fdfb7a17b7da5e2de716c0e98475ec77a41c64c6/README.zh.md#许可与免责声明)를 확인하고 허가된 환경에서 사용하세요.
