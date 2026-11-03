# ScopeSentry MCP 상세 설정

기본 실행과 ARTEX 입력값은 [MCP 서버 실행·연결](../mcp-servers.md)을 참고하세요. 이 문서는 네트워크·인증·운영 설정을 직접 조정할 때 사용합니다. 명령은 저장소 루트에서 실행합니다. 독립 Compose와 `up` 명령은 `v0.2.1`부터 제공합니다. MCP용 소스를 별도 디렉터리에 두어도 기존 ARTEX를 재생성할 필요는 없습니다.

## 구성과 초기 설정

[독립 Compose](../../docker/scopesentry/compose.yml)는 `artex-scopesentry` 프로젝트에 다음 네 서비스를 실행합니다.

| 서비스 | 역할·연결 |
| --- | --- |
| `scopesentry` | Web·API·MCP 서버. 자체 네트워크와 ARTEX 네트워크에 연결 |
| `scopesentry-scanner` | 스캔 작업을 처리하는 노드. 기본은 자체 네트워크에만 연결 |
| `scopesentry-mongodb` | ScopeSentry 데이터 저장. 자체 네트워크 전용 |
| `scopesentry-redis` | 작업 큐·실행 정보. 자체 네트워크 전용 |

ScopeSentry `1.9.4`와 Scanner `1.9.1`의 공식 이미지를 digest로 고정합니다. 현재 구성은 `linux/amd64`이며 ARM에서는 에뮬레이션 검증이 별도로 필요합니다. MongoDB·Redis는 호스트 포트를 열지 않고, 웹은 기본 `127.0.0.1:18083`에 엽니다.

일반 기동은 다음 한 명령으로 준비할 수 있습니다. 새 환경은 실행 중인 ARTEX의 네트워크를 찾고, 기존 환경은 저장한 설정을 사용합니다.

```sh
python3 scripts/prepare-mcp.py scopesentry up
```

`up`에는 서버 준비 대기와 API 키 발급·검증도 포함됩니다. Scanner의 개별 플러그인 자체 검사 완료를 보장하는 명령은 아닙니다.

기동 전에 설정을 직접 바꾸려면 `init`으로 파일만 준비한 뒤 편집하고 실행합니다. 아래 네트워크 이름은 개발 실습 환경의 예시이며, 실제 이름은 [ARTEX 네트워크 선택](#artex-네트워크-선택)에서 확인합니다.

```sh
python3 scripts/prepare-mcp.py scopesentry init --network artex-dev-lab_default
docker compose -p artex-scopesentry \
  --env-file "$PWD/.local/mcp/scopesentry/.env" \
  -f docker/scopesentry/compose.yml up -d
docker compose -p artex-scopesentry \
  --env-file "$PWD/.local/mcp/scopesentry/.env" \
  -f docker/scopesentry/compose.yml ps
```

준비 스크립트는 `.local/mcp/scopesentry/.env`와 `.local/mcp/scopesentry/state/`를 만듭니다. 초기 상태 파일은 서버가 기록할 설정·비밀번호 파일과 플러그인 키입니다. 기존 `.env`와 상태가 있으면 보존하며, 누락된 기존 상태를 임의로 새 비밀번호로 교체하지 않습니다.

| 변수·파일 | 의미·초기값 |
| --- | --- |
| `ARTEX_NETWORK` | ARTEX가 실행 중인 외부 Docker 네트워크 |
| `SCOPESENTRY_STATE_DIR` | `server-config.yaml`, `server-plugin-key`, `server-password`를 보관할 절대 경로 |
| `SCOPESENTRY_BIND_IP` / `SCOPESENTRY_PORT` | 웹 수신 주소·호스트 포트. 기본 `127.0.0.1` / `18083` |
| `SCOPESENTRY_NODE_NAME` | Scanner 노드 이름. 기본 `artex-scopesentry-node` |
| `SCOPESENTRY_MONGO_USER` | DB 사용자. 기본 `scopesentry` |
| `SCOPESENTRY_MONGO_PASSWORD` / `SCOPESENTRY_REDIS_PASSWORD` | 초기화 시 생성하는 임의 비밀번호 |
| `ARTEX_SCOPESENTRY_API_KEY` | `up` 또는 `connect`에서 발급·저장하는 MCP 인증 키 |

직접 설정할 때는 [환경 예시](../../docker/scopesentry/.env.example)를 참고합니다. 환경 파일만 복사해서는 상태 파일이 준비되지 않으므로 초기화 스크립트를 먼저 사용하세요. 웹 포트를 바꿔도 ARTEX의 MCP 주소는 내부 포트 `8082`를 사용합니다.

## 웹 로그인과 API 키 발급

서버가 정상 기동하면 [http://127.0.0.1:18083](http://127.0.0.1:18083)을 엽니다. 초기 계정은 `ScopeSentry`이고 초기 비밀번호는 `.local/mcp/scopesentry/state/server-password`에 기록됩니다. 이 파일을 비공개 편집기에서 확인합니다. 서버 로그에 초기 계정 정보가 포함될 수 있으므로 로그 전체를 공유하지 않습니다.

```sh
python3 scripts/prepare-mcp.py scopesentry connect
```

`up`이 이 단계를 자동으로 수행합니다. 수동 기동했거나 인증만 재확인할 때는 위 `connect`를 사용합니다. 로컬 ScopeSentry에 초기 계정으로 로그인해 공식 API 키를 발급하고 `.env`의 `ARTEX_SCOPESENTRY_API_KEY`에 저장한 뒤 MCP 인증을 확인합니다. 이미 저장된 키가 있으면 재사용합니다. **ARTEX 설정을 자동 변경하거나 스캔을 생성하지 않습니다.** 최초 기동 직후 비밀번호 파일이 비어 있으면 서버 준비가 끝난 뒤 다시 실행합니다.

ScopeSentry 계정 비밀번호를 변경한 설치에서는 웹 UI의 API Key 관리에서 키를 발급하고 `.env`의 `ARTEX_SCOPESENTRY_API_KEY`에 저장합니다. 기존 비밀번호를 초기화할 필요는 없습니다. 다시 `connect`를 실행하면 저장한 키로 검증합니다. 기본 경로 외의 환경 파일을 사용한다면 `up`·`init`·`connect`·Compose에 동일한 `--env-file`을 지정합니다. `prepare-mcp.py up`은 저장된 환경 파일의 값을 사용하며 호스트의 관련 환경변수를 전달하지 않습니다. Compose를 직접 실행할 때는 셸의 `export` 값이 환경 파일보다 우선합니다.

웹을 다른 PC에 열려면 `.env`의 `SCOPESENTRY_BIND_IP`를 실제 수신할 주소로 바꾸고 `up -d`를 다시 실행합니다. 초기화 스크립트의 `connect`도 저장된 호스트 주소·포트로 접속합니다. ARTEX와 같은 Docker 호스트에서만 관리한다면 기본 loopback 설정으로 충분합니다.

## ARTEX 네트워크 선택

새 `up`은 실행 중인 Compose의 `artex` 서비스가 하나이고 연결할 사용자 네트워크도 하나일 때 자동 선택합니다. ARTEX가 여러 개이거나 직접 만든 컨테이너라면 실제 이름을 확인해 지정합니다.

```sh
docker ps --format 'table {{.Names}}\t{{.Ports}}'
docker inspect <ARTEX_컨테이너_이름> \
  --format '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}}{{"\n"}}{{end}}'
python3 scripts/prepare-mcp.py scopesentry up --network artex-dev-lab_default
```

`<ARTEX_컨테이너_이름>`과 예시 네트워크를 실제 값으로 바꿉니다. 기본 Compose는 보통 `artex-on-chatgpt_default`, 개발 스크립트는 `artex-dev-lab_default`, `-p my-artex`로 실행한 환경은 `my-artex_default`입니다. 조회 결과를 기준으로 선택하세요.

이미 환경 파일이 있으면 그 파일의 `ARTEX_NETWORK`를 사용합니다. 네트워크를 바꿀 때는 파일 값을 수정한 뒤 다시 `up`합니다. 다른 `--network` 값으로 저장 설정을 덮어쓰지 않습니다. 네트워크가 없으면 ARTEX를 먼저 실행해야 하며, MCP 프로젝트는 외부 네트워크를 생성하거나 삭제하지 않습니다.

두 MCP는 서로 없이도 실행할 수 있습니다. 같은 ARTEX 네트워크에는 같은 별칭의 서버를 중복 연결하지 않습니다. 기본 ARTEX의 기동·종료 명령이 독립 MCP를 함께 기동·종료하지 않습니다. 직접 Compose를 실행할 때는 생성·조회·갱신·종료에 같은 프로젝트 이름을 사용합니다. `.env`의 `COMPOSE_PROJECT_NAME`을 바꿨다면 아래 명령의 `-p` 값도 맞추세요.

## ARTEX에서 연결

`.local/mcp/scopesentry/.env`의 `ARTEX_SCOPESENTRY_API_KEY` 값을 확인한 뒤 ARTEX의 **시스템 → MCP → ScopeSentry → 설정**에 입력합니다.

| 필드 | 입력 |
| --- | --- |
| 이름 | `ScopeSentry` |
| 전송 방식 | `http (원격)` |
| 원격 URL | `http://scopesentry:8082/mcp` |
| 요청 헤더 | `X-API-Key=<ARTEX_SCOPESENTRY_API_KEY의 실제 값>` |
| TLS 인증서 검증 건너뛰기 | 해제 |

키의 `.env` 따옴표는 복사하지 않고 `Bearer` 접두사도 붙이지 않습니다. 요청 헤더는 JSON이 아닌 위 한 줄을 입력합니다. 기본 항목을 편집해 저장하면 사용 토글은 OFF를 유지합니다.

1. **도구 목록 → 새로 고침**으로 ARTEX에서 MCP에 도달하는지 확인합니다.
2. 사용할 때 MCP 사용 토글을 ON으로 바꿉니다.
3. `scopesentry` 스킬이 원하는 에이전트에 연결돼 있는지 확인합니다. 기본 대상은 침투 테스트·실행 에이전트입니다.
4. 새 대화에서 “ScopeSentry 스킬을 읽고 기존 프로젝트·작업·노드 목록만 조회해 달라”고 요청해 읽기 경로를 확인할 수 있습니다. 데이터가 없으면 빈 결과가 정상입니다.

`connect`는 호스트에서의 인증 검증이고, ARTEX의 도구 새로 고침은 컨테이너 사이 연결 검증입니다. 둘을 구분합니다. 서버는 조회 외에 스캔·템플릿 생성 도구도 제공하므로 토글 ON을 조회 전용 권한 설정으로 해석하지 않습니다. 결과가 ARTEX 그래프에 자동 등록되는 것은 아니며, 기존 조회·동기화 기능과 에이전트의 기록 도구를 통해 활용합니다.

## MCP 토글·에이전트·스킬

MCP의 **사용** 토글을 끄면 새 에이전트 실행에서 직접 연결과 스킬 경로 모두 제외합니다. 수동 **도구 목록 새로 고침**은 OFF 상태에서도 연결을 확인할 수 있습니다. 기존 사용자가 저장한 활성 상태·편집·삭제는 초기화로 바꾸지 않습니다. 기본 항목을 삭제한 뒤 UI에서 새로 추가하면 ON으로 등록되므로 바로 사용하지 않을 때는 OFF로 바꾸세요.

MCP의 에이전트 체크박스는 직접 노출 경로이고, 스킬의 에이전트 연결은 스킬을 통한 사용 경로입니다. 기본 `scopesentry` 스킬이 연결된 침투 테스트(`pentest`)·실행(`worker`)에는 MCP 체크박스를 중복 선택할 필요가 없습니다. 서버 이름 `ScopeSentry`는 스킬의 연결 이름과 대소문자까지 일치해야 합니다.

두 경로를 모두 선택해도 스킬이 연결돼 있으면 해당 도구는 스킬을 읽은 후 공개됩니다. 활성 서버의 네트워크 연결·`tools/list`는 도구 구성을 만들 때 수행하며 스킬 호출까지 미루지 않습니다. 에이전트는 `SearchExtraTools`로 입력 스키마를 읽고 `ExecuteExtraTool`로 호출합니다.

토글 변경은 Docker를 중지하거나 이미 실행 중인 에이전트의 도구·원격 작업을 즉시 회수하지 않습니다. 새 대화 호출이나 새 Worker 실행에서 반영 상태를 확인합니다.

## Scanner 준비 상태와 대상 네트워크

ScopeSentry 웹의 노드 목록에서 `artex-scopesentry-node`가 온라인인지 확인합니다. 컨테이너가 실행 중이거나 MCP 도구 목록이 조회돼도 모든 Scanner 플러그인의 준비가 끝났다는 뜻은 아닙니다. Scanner는 처음 실행할 때 플러그인 설치·자체 검사를 하며 외부 DNS·네트워크를 사용할 수 있습니다. 원본 ksubdomain 시작 검사는 최대 5분을 기다릴 수 있고 raw packet 기능은 Docker 호스트와 네트워크 조건에 따라 실패할 수 있습니다.

기본 Scanner는 ScopeSentry 프로젝트 네트워크에서 접근 가능한 대상으로 작업합니다. **ARTEX와 ScopeSentry MCP 서버가 같은 네트워크에 있어도 Scanner가 `juiceshop` 같은 ARTEX 네트워크의 컨테이너 이름을 사용할 수 있는 것은 아닙니다.** 실제 스캔 노드의 대상 접근 경로를 별도로 맞춰야 합니다.

개발 Juice Shop처럼 ARTEX 네트워크에 있는 대상을 사용하려면 `.local/mcp/scopesentry/scanner-network.yml`에 다음을 저장합니다.

```yaml
services:
  scopesentry-scanner:
    networks:
      default:
      artex:
```

기본 Compose가 선언한 외부 `artex` 네트워크를 Scanner에도 연결하는 추가 설정입니다. 다음처럼 두 파일을 함께 지정합니다.

```sh
docker compose -p artex-scopesentry \
  --env-file "$PWD/.local/mcp/scopesentry/.env" \
  -f docker/scopesentry/compose.yml \
  -f "$PWD/.local/mcp/scopesentry/scanner-network.yml" up -d
```

이후 갱신·재시작에도 같은 두 파일을 사용합니다. `prepare-mcp.py scopesentry up`은 기본 Compose만 사용하므로, 이 추가 설정을 사용하는 환경에서는 위의 두 파일 지정 명령으로 계속 기동합니다. 이때 대상 주소는 호스트 공개 포트가 아닌 `http://juiceshop:3000` 같은 컨테이너 내부 주소입니다. 다른 전용 실습 네트워크의 대상이라면 해당 네트워크를 external로 선언하고 Scanner에 연결하는 방식으로 맞춥니다. 임시 `docker network connect`만 사용하면 Compose 재생성 시 연결이 유지되지 않습니다.

## 중지·재시작·삭제

추가 네트워크 파일을 사용한다면 아래 명령에도 같은 `-f`를 추가합니다.

```sh
docker compose -p artex-scopesentry \
  --env-file "$PWD/.local/mcp/scopesentry/.env" \
  -f docker/scopesentry/compose.yml stop
docker compose -p artex-scopesentry \
  --env-file "$PWD/.local/mcp/scopesentry/.env" \
  -f docker/scopesentry/compose.yml up -d
```

`down`은 컨테이너와 자체 네트워크를 제거하고 볼륨·ARTEX 외부 네트워크를 보존합니다. **`down -v`는 MongoDB·Redis·서버 결과·Scanner 설정 등 해당 프로젝트의 11개 볼륨을 삭제**합니다. `.local/mcp/scopesentry/`의 환경·상태 파일은 Docker가 지우지 않습니다. 초기화하려면 DB 볼륨과 이 파일들의 관계를 확인한 뒤 함께 처리해야 합니다.

백업에는 `.env`, `state/`와 프로젝트 볼륨을 모두 포함합니다. 기존 DB나 Scanner 설정 볼륨이 있는 상태에서 DB 비밀번호·노드 이름을 환경 파일만으로 바꾸면 저장 설정과 불일치할 수 있습니다. 해당 DB 계정과 Scanner 설정도 함께 갱신해야 합니다. API 키를 교체했다면 `.env`와 ARTEX의 `X-API-Key`를 모두 맞춥니다.

독립 MCP를 ARTEX 네트워크에 연결했다면 ARTEX 전체 정리 전에 이 MCP 프로젝트를 먼저 `down`합니다. `v0.2.0` 통합 구성의 프로젝트·볼륨·`.env.pentest`는 자동 이관되지 않습니다. 기존 데이터가 있으면 DB·볼륨·비공개 설정을 백업한 뒤 이관하며 새 초기화 스크립트로 기존 DB 비밀번호를 교체하지 않습니다.

## 연결 문제 확인

| 증상 | 확인할 항목 |
| --- | --- |
| external network를 찾지 못함 | ARTEX 기동 여부, 실제 네트워크 이름과 `ARTEX_NETWORK` |
| bind mount·상태 파일 오류 | `SCOPESENTRY_STATE_DIR`의 절대 경로와 초기 상태 파일. 기존 데이터를 임의 재생성하지 않음 |
| 웹은 열리지만 ARTEX에서 연결 실패 | 서버의 공유 네트워크, URL의 내부 포트 `8082`와 `/mcp` 경로 |
| 401·403 | 요청 헤더 `X-API-Key`, 키 폐기·교체 여부, 따옴표·Bearer 접두사 혼입 |
| connect의 초기 계정 로그인 실패 | 비밀번호 변경 여부. 웹에서 발급한 API 키를 저장해 재검증 |
| MCP 조회는 되지만 노드가 없음 | Scanner 초기 플러그인 설치·자체 검사, 노드 등록 상태 |
| Juice Shop 등 컨테이너 이름을 찾지 못함 | MCP 서버와 별도로 Scanner의 대상 네트워크 연결 여부 |
| ksubdomain 검사 실패 | raw packet·인터페이스·DNS 접근 조건. 다른 플러그인까지 전부 실패했다는 뜻은 아님 |

현재 고정 이미지의 격리 기동 검사에서는 `ksubdomain`의 약 5분 타임아웃 이후 노드가 온라인으로 등록됐으며, `trufflehog` 초기화에서 `/apps/dictionaries: is a directory` 경고도 확인했습니다. 서버·노드 연결 확인과 전체 플러그인 정상 여부를 구분하고, 해당 플러그인을 사용할 때는 실제 저장된 입력 경로와 실행 결과를 확인하세요. 원인 수정이나 전체 스캔 검증은 이 구성 확인에 포함하지 않았습니다.

MCP 연결 확인과 Scanner별 실제 작업 검증은 별개입니다. 특정 플러그인 실패나 결과 없음만으로 대상에 문제가 없다고 결론 내리지 않습니다.
