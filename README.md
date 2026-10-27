<div align="center">
  <img src="web/public/logo.png" width="180" alt="ARTEX 로고" />
  <h1>Artex-on-ChatGPT</h1>
  <p><strong>ChatGPT 구독과 OpenAI API를 사용하는 한국어 ARTEX</strong></p>
</div>

## 프로젝트 소개

[artex-global `fdfb7a1`](https://github.com/jiwoochris/artex-global/commit/fdfb7a17b7da5e2de716c0e98475ec77a41c64c6)을 기반으로 한 OpenAI 전용 포크입니다. 원본의 **Goals → Planner → Worker → 결과 공유·후속 계획** 흐름을 사용하며, 실행 오류·정보 유실·연결 처리와 브라우저 실행 환경을 보완합니다.

API 키와 ChatGPT 구독이 같은 Responses 어댑터를 사용합니다. ChatGPT 연결에는 Codex 설치가 필요하지 않습니다. 앱의 관리자 로그인과 ChatGPT 계정 연결은 별개입니다.

## 원본 대비 변경점

| 영역 | 현재 차이 |
| --- | --- |
| 모델 연결 | OpenAI Responses로 통합, ChatGPT 구독 연결·토큰 갱신·계정별 모델 조회 추가 |
| 응답 처리 | 추론 상태·응답 단계·도구 항목 순서 보존, 완료·미완료·오류 구분 |
| 실행·저장 | 취소된 도구 실행 방지, 부분 저장·대화 재개·Todo·Worker 결과 유실 보완 |
| 그래프·도구 | 사실·자산·근거 연결의 원자적 저장, 취약점 등록 인자 처리와 최신 결과 조회 보완 |
| 브라우저 | Chromium·Playwright 실행 환경 통일, 같은 세션에서 수동 인증, 스크린샷의 실제 이미지 전달 |
| 설정·화면 | 한국어·영어, 작업 템플릿의 이름·고급값 저장, 그래프 페이지 조회 |
| 설치 | 현재 소스로 앱·웹을 빌드하는 Docker Compose와 PostgreSQL 구성 |

의도 분할·우선순위·재검증·사실 기록·목표 판단은 원본 지침의 한국어 번역을 사용합니다. 

[전체 변경점](docs/changes.md) · [브라우저·설정](docs/browser-and-presets.md) · [트래픽 증거](docs/finding-traffic-evidence.md)

## 빠른 시작

Git과 Docker Compose를 준비하세요. Go·Node.js·PostgreSQL은 이미지 빌드와 컨테이너에서 준비합니다.

```sh
git clone https://github.com/KMINGON/Artex-on-ChatGPT.git
cd Artex-on-ChatGPT
docker compose up -d
```

첫 실행은 이미지를 빌드합니다. `docker compose logs -f artex`로 진행 상태를 확인하고 [http://127.0.0.1:8787](http://127.0.0.1:8787)을 엽니다.

| 항목 | 기본값 |
| --- | --- |
| 관리자 계정 | `ARTEX` |
| 최초 관리자 비밀번호 | `artex1234` |
| 웹 포트 | `8787` |
| 기본 언어 | 한국어 |

처음 사용하는 DB에만 초기 비밀번호를 설정합니다. 기존 비밀번호는 유지되며 화면에서 변경할 수 있습니다. 웹은 기본적으로 모든 IPv4 주소에 열립니다. 이 PC에서만 사용하려면 `.env`에 `ARTEX_BIND_IP=127.0.0.1`을 지정하세요. 다른 선택값은 [.env.example](.env.example)을 참고하세요.

**시스템 → LLM**에서 ChatGPT 구독 계정 또는 OpenAI API 키를 연결하고, 모델 조회·선택·연결 확인 후 프로필을 저장합니다. 기존 기본 모델이 없으면 첫 사용 가능한 프로필을 기본으로 지정합니다. ChatGPT 로그인·재로그인은 Docker를 실행한 PC의 `http://127.0.0.1:8787`에서 진행합니다.

## 접속 주소

| 목적 | 주소 |
| --- | --- |
| 실행 PC에서 ARTEX 접속 | `http://127.0.0.1:8787` |
| 다른 PC에서 ARTEX 접속 | `http://실행PC의IP:8787` |
| ARTEX에서 같은 Docker 네트워크의 대상 접속 | `http://컨테이너명:내부포트` |
| ARTEX에서 Docker 호스트의 대상 접속 | `http://host.docker.internal:포트` |
| 내부망·외부 대상 | 대상의 실제 IP·도메인과 포트 |

ARTEX에 전달하는 `127.0.0.1`은 ARTEX 컨테이너 자신입니다. 예를 들어 Juice Shop을 같은 네트워크에 실행할 수 있습니다.

```sh
docker run -d --name juice-shop \
  --network artex-on-chatgpt_default \
  -p 127.0.0.1:3000:3000 \
  bkimminich/juice-shop:latest
```

PC 브라우저는 `http://127.0.0.1:3000`, ARTEX 작업의 대상 주소는 **`http://juice-shop:3000`**입니다. Compose 프로젝트 이름을 바꿨다면 네트워크 이름도 `<프로젝트명>_default`로 맞추세요.

호스트 서버는 컨테이너에서 도달할 수 있는 주소에서 수신해야 합니다. Linux의 `127.0.0.1` 전용 서버나 WSL 밖의 Windows 서버에는 별도의 수신 주소·호스트 IP 설정이 필요할 수 있습니다.

## 중지·업데이트·데이터

```sh
docker compose stop
docker compose up -d
```

업데이트할 때는 DB·앱 파일·인증 볼륨을 함께 백업한 뒤 소스와 이미지를 갱신합니다.

```sh
git pull --ff-only
docker compose up -d --build
```

| 볼륨 | 내용 |
| --- | --- |
| `pgdata` | 작업·프로필·설정 등 PostgreSQL 데이터 |
| `artex-data` | 작업 파일·도구 결과·대화 이력 |
| `artex-auth` | 관리자 서명 키·ChatGPT 인증·브라우저 프로필 |

같은 Compose 프로젝트는 볼륨을 재사용합니다. `docker compose down`은 볼륨을 보존하지만 **`docker compose down -v`는 세 볼륨의 데이터를 삭제**합니다. 호스트 설치, 환경변수, 기존 데이터 이관 조건은 [설치·운영 문서](docs/changes/operations.md)를 참고하세요.

## 출처와 라이선스

원본 [Autumn-27/ARTEX](https://github.com/Autumn-27/ARTEX)와 한국어 기반판 [jiwoochris/artex-global](https://github.com/jiwoochris/artex-global)의 출처·저작권 고지를 보존합니다. 루트는 [GNU AGPL v3](LICENSE), 웹 코드는 [MIT](web/LICENSE), 제3자 코드는 각 배포물의 조건을 따릅니다. 로컬 실행 엔진의 출처와 변경점은 [Norma 문서](third_party/norma/ARTEX_PATCHES.md)에 있습니다.

원저자의 [사용 제한·면책 고지](https://github.com/jiwoochris/artex-global/blob/fdfb7a17b7da5e2de716c0e98475ec77a41c64c6/README.zh.md#许可与免责声明)를 확인하고 허가된 환경에서 사용하세요.
