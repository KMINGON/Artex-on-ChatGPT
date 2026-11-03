# Metasploit MCP 상세 설정

기본 실행과 ARTEX 입력값은 [MCP 서버 실행·연결](../mcp-servers.md)을 참고하세요. 이 문서는 네트워크·인증·운영 설정을 직접 조정할 때 사용합니다. 명령은 저장소 루트에서 실행합니다. 독립 Compose와 `up` 명령은 `v0.2.1`부터 제공합니다. MCP용 소스를 별도 디렉터리에 두어도 기존 ARTEX를 재생성할 필요는 없습니다.

## 구성과 초기 설정

[독립 Compose](../../docker/metasploit/compose.yml)는 `artex-metasploit` 프로젝트에 Metasploit Framework와 전용 PostgreSQL을 실행합니다. Metasploit은 자체 네트워크와 ARTEX 네트워크에 함께 연결되고, DB는 자체 네트워크에서만 접근합니다. MCP의 `3000` 포트는 호스트에 공개하지 않습니다.

일반 기동은 다음 한 명령으로 준비할 수 있습니다. 새 환경은 실행 중인 ARTEX의 네트워크를 찾고, 기존 환경은 저장한 설정을 사용합니다.

```sh
python3 scripts/prepare-mcp.py metasploit up
```

기동 전에 설정을 직접 바꾸려면 `init`으로 파일만 준비한 뒤 편집하고 실행합니다. 아래 네트워크 이름은 개발 실습 환경의 예시이며, 실제 이름은 [ARTEX 네트워크 선택](#artex-네트워크-선택)에서 확인합니다.

```sh
python3 scripts/prepare-mcp.py metasploit init --network artex-dev-lab_default
```

dev 초기 설정은 `MSF_MCP_DANGEROUS_ACTIONS=true`를 유지합니다. 처음 연결을 조회 기능으로만 확인하려면 기동 전에 `.local/mcp/metasploit/.env`의 해당 항목을 `MSF_MCP_DANGEROUS_ACTIONS='false'`로 바꿉니다. 설정의 의미와 변경 적용법은 아래 실행 권한 절에서 설명합니다.

```sh
docker compose -p artex-metasploit \
  --env-file "$PWD/.local/mcp/metasploit/.env" \
  -f docker/metasploit/compose.yml up -d
docker compose -p artex-metasploit \
  --env-file "$PWD/.local/mcp/metasploit/.env" \
  -f docker/metasploit/compose.yml ps
```

초기화는 `.local/mcp/metasploit/.env`에 임의 MCP 토큰과 DB 비밀번호를 생성합니다. 기존 파일이 있으면 값을 보존하며 다시 초기화하지 않습니다. 파일을 직접 준비하려면 [환경 예시](../../docker/metasploit/.env.example)를 참고합니다.

| 변수 | 의미·초기값 |
| --- | --- |
| `ARTEX_NETWORK` | ARTEX가 실행 중인 외부 Docker 네트워크 이름 |
| `MSF_MCP_AUTH_TOKEN` | ARTEX가 MCP에 인증할 Bearer 토큰. 초기화 시 생성 |
| `MSF_POSTGRES_PASSWORD` | 전용 PostgreSQL 비밀번호. 초기화 시 생성 |
| `MSF_MCP_DANGEROUS_ACTIONS` | dev의 새 준비 파일·`.env.example`·Compose 기본값은 `true`. 조회로 제한하려면 `false` 명시 |

이미지 digest는 Compose에 고정합니다. 현재 구성은 `linux/amd64`이며 ARM에서는 Docker의 amd64 에뮬레이션이 필요합니다. 첫 실행은 이미지 다운로드와 Framework·DB 초기화 때문에 시간이 걸릴 수 있습니다. `metasploit`과 `metasploit-db`가 정상 상태인지 확인한 후 연결합니다. 컨테이너 healthcheck는 MCP 포트 준비 상태이며 인증·도구 호출 검증은 [ARTEX에서 연결](#artex에서-연결)에서 합니다.

## ARTEX 네트워크 선택

새 `up`은 실행 중인 Compose의 `artex` 서비스가 하나이고 연결할 사용자 네트워크도 하나일 때 자동 선택합니다. ARTEX가 여러 개이거나 직접 만든 컨테이너라면 실제 이름을 확인해 지정합니다.

```sh
docker ps --format 'table {{.Names}}\t{{.Ports}}'
docker inspect <ARTEX_컨테이너_이름> \
  --format '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}}{{"\n"}}{{end}}'
python3 scripts/prepare-mcp.py metasploit up --network artex-dev-lab_default
```

`<ARTEX_컨테이너_이름>`과 예시 네트워크를 실제 값으로 바꿉니다. 기본 Compose는 보통 `artex-on-chatgpt_default`, 개발 스크립트는 `artex-dev-lab_default`, `-p my-artex`로 실행한 환경은 `my-artex_default`입니다. 조회 결과를 기준으로 선택하세요.

이미 환경 파일이 있으면 그 파일의 `ARTEX_NETWORK`를 사용합니다. 네트워크를 바꿀 때는 파일 값을 수정한 뒤 다시 `up`합니다. 다른 `--network` 값으로 저장 설정을 덮어쓰지 않습니다. 네트워크가 없으면 ARTEX를 먼저 실행해야 하며, MCP 프로젝트는 외부 네트워크를 생성하거나 삭제하지 않습니다.

두 MCP는 서로 없이도 실행할 수 있습니다. 같은 ARTEX 네트워크에는 같은 별칭의 서버를 중복 연결하지 않습니다. 기본 ARTEX의 기동·종료 명령이 독립 MCP를 함께 기동·종료하지 않습니다. 직접 Compose를 실행할 때는 생성·조회·갱신·종료에 같은 프로젝트 이름을 사용합니다. `.env`의 `COMPOSE_PROJECT_NAME`을 바꿨다면 아래 명령의 `-p` 값도 맞추세요.

## ARTEX에서 연결

`.local/mcp/metasploit/.env`를 비공개 편집기에서 열어 `MSF_MCP_AUTH_TOKEN` 값을 확인합니다. ARTEX의 **시스템 → MCP → Metasploit → 설정**에 다음을 입력합니다.

| 필드 | 입력 |
| --- | --- |
| 이름 | `Metasploit` |
| 전송 방식 | `http (원격)` |
| 원격 URL | `http://metasploit:3000/` |
| 요청 헤더 | `Authorization=Bearer <MSF_MCP_AUTH_TOKEN의 실제 값>` |
| TLS 인증서 검증 건너뛰기 | 해제 |

토큰의 `.env` 따옴표는 복사하지 않습니다. `Bearer` 뒤 공백 하나를 유지하고 헤더를 JSON으로 입력하지 않습니다. 기본 항목을 편집해 저장해도 사용 토글은 OFF를 유지합니다.

1. **도구 목록 → 새로 고침**을 눌러 목록을 가져옵니다.
2. 사용 토글을 ON으로 바꿉니다.
3. `metasploit` 스킬이 원하는 에이전트에 연결돼 있는지 확인합니다. 기본 대상은 침투 테스트·실행 에이전트입니다.
4. 새 대화에서 “Metasploit 스킬을 읽고 모듈 검색과 모듈 설명 조회만 확인해 달라”고 요청해 사용 경로를 확인할 수 있습니다. 모듈 검색 결과는 실제 대상의 취약점 확인 결과가 아닙니다.

Metasploit MCP 도구 목록 조회나 모듈 정보 읽기에는 LLM 계정 설정과 별개로 이 서버 인증이 필요합니다.

Compose의 ARTEX 네트워크 별칭 `metasploit`은 연결 계약에 포함됩니다. 동봉한 [HTTP 어댑터](../../docker/metasploit/mcp-server.rb)는 원본의 Host·Origin 검사를 유지하며 `metasploit:3000`을 추가로 허용합니다. URL을 임의의 컨테이너 IP·별칭으로 바꾸면 Host 검증에서 거절될 수 있으므로 안내된 주소를 사용합니다.

## MCP 토글·에이전트·스킬

MCP의 **사용** 토글을 끄면 새 에이전트 실행에서 직접 연결과 스킬 경로 모두 제외합니다. 수동 **도구 목록 새로 고침**은 OFF 상태에서도 연결을 확인할 수 있습니다. 기존 사용자가 저장한 활성 상태·편집·삭제는 초기화로 바꾸지 않습니다. 기본 항목을 삭제한 뒤 UI에서 새로 추가하면 ON으로 등록되므로 바로 사용하지 않을 때는 OFF로 바꾸세요.

MCP의 에이전트 체크박스는 직접 노출 경로이고, 스킬의 에이전트 연결은 스킬을 통한 사용 경로입니다. 기본 `metasploit` 스킬이 연결된 침투 테스트(`pentest`)·실행(`worker`)에는 MCP 체크박스를 중복 선택할 필요가 없습니다. 서버 이름 `Metasploit`은 스킬의 연결 이름과 대소문자까지 일치해야 합니다.

두 경로를 모두 선택해도 스킬이 연결돼 있으면 해당 도구는 스킬을 읽은 후 공개됩니다. 활성 서버의 네트워크 연결·`tools/list`는 도구 구성을 만들 때 수행하며 스킬 호출까지 미루지 않습니다. 에이전트는 `SearchExtraTools`로 입력 스키마를 읽고 `ExecuteExtraTool`로 호출합니다.

토글 변경은 Docker를 중지하거나 이미 실행 중인 에이전트의 도구·원격 작업을 즉시 회수하지 않습니다. 새 대화 호출이나 새 Worker 실행에서 반영 상태를 확인합니다.

## `false`·`true` 실행 권한

이 변수는 **Metasploit 서버가 허용하는 호출 범위**입니다. ARTEX의 MCP 사용 토글이나 에이전트 체크박스와는 별개입니다.

| 값 | 서버 측 동작 |
| --- | --- |
| `false` | 모듈 검색·정보, 기존 결과·호스트·서비스·세션 목록 등의 조회 사용. `msf_module_check`, `msf_module_execute`, `msf_session_write`, `msf_session_stop` 호출 거부 |
| `true` | 위 조회에 더해 모듈 검사·실행과 세션 입력·종료 호출 허용. 대상 연결, 모듈 필수 옵션과 기능 지원 여부는 별도 조건 |

**현재 dev의 새 준비 파일·환경 예시·Compose는 실습 기본값인 `true`를 사용합니다.** 변수를 생략하거나 빈 값으로 둬도 Compose는 `true`를 사용합니다. 기존 `.env`에 명시한 값은 초기화 스크립트가 덮어쓰지 않으므로 이전 `false`도 보존됩니다. 조회만 사용할 때는 파일에 `false`를 명시합니다. 이 값과 별개로 ARTEX의 기본 MCP 사용 토글은 OFF입니다. 도구가 목록에 보인다는 사실만으로 실행 권한이 허용됐다고 판단하지 않습니다.

실습 환경에서 실행 기능을 사용할 때는 해당 `.env`의 한 줄을 `MSF_MCP_DANGEROUS_ACTIONS='true'`로 변경합니다. 다시 조회 중심으로 사용할 때는 `'false'`로 바꿉니다. 파일 변경 후 컨테이너에 적용하려면 다음처럼 `up -d`를 사용합니다. 단순 `restart`는 바뀐 환경변수를 반영하지 않습니다.

```sh
docker compose -p artex-metasploit \
  --env-file "$PWD/.local/mcp/metasploit/.env" \
  -f docker/metasploit/compose.yml up -d metasploit
docker compose -p artex-metasploit \
  --env-file "$PWD/.local/mcp/metasploit/.env" \
  -f docker/metasploit/compose.yml exec -T metasploit \
  ruby -e 'puts ENV.fetch("MSF_MCP_DANGEROUS_ACTIONS")'
```

마지막 명령은 실제 컨테이너의 해당 boolean만 출력합니다. `prepare-mcp.py up`은 저장된 환경 파일의 값을 사용하며 호스트의 관련 환경변수를 전달하지 않습니다. 위처럼 Compose를 직접 실행할 때는 셸의 `export` 값이 환경 파일보다 우선하므로 해제하거나 의도한 값으로 맞춥니다. 이 Compose는 별도의 `--enable-dangerous-actions` 플래그를 넘기지 않습니다.

ARTEX에서 토글을 끄거나 작업을 취소해도 이미 실행된 Metasploit 작업·세션이 자동 종료되지는 않습니다. 반대로 서버를 재생성하면 연결이 끊기고 메모리상의 실행 상태·세션은 유지되지 않을 수 있습니다. 실행 중인 항목이 있으면 상태를 확인한 뒤 설정을 바꿉니다.

## 중지·재시작·삭제

```sh
docker compose -p artex-metasploit \
  --env-file "$PWD/.local/mcp/metasploit/.env" \
  -f docker/metasploit/compose.yml stop
docker compose -p artex-metasploit \
  --env-file "$PWD/.local/mcp/metasploit/.env" \
  -f docker/metasploit/compose.yml up -d
```

일반 종료에는 같은 명령의 `stop` 대신 `down`을 사용해 컨테이너와 자체 네트워크를 제거할 수 있습니다. DB·Framework 볼륨과 ARTEX 외부 네트워크는 남습니다. **`down -v`는 전용 DB와 Framework 파일을 영구 삭제**하므로 초기화할 때만 사용합니다. `.local/mcp/metasploit/.env`는 Docker가 삭제하지 않습니다.

백업 대상은 `metasploit-pgdata`, `metasploit-data` 볼륨과 `.env`입니다. 실제 볼륨명에는 Compose 프로젝트명이 붙습니다. 기존 DB가 있는 상태에서 `.env`의 DB 비밀번호만 바꾸면 DB 계정은 바뀌지 않으므로 연결에 실패할 수 있습니다. 토큰을 바꾸면 서버 컨테이너 갱신과 ARTEX 요청 헤더 변경을 함께 합니다.

독립 MCP를 ARTEX 네트워크에 연결했다면 ARTEX 전체 정리 전에 이 MCP 프로젝트를 먼저 `down`합니다. `v0.2.0` 통합 구성의 프로젝트·볼륨·`.env.pentest`는 자동 이관되지 않습니다. 기존 데이터가 있으면 DB·볼륨·비공개 설정을 백업한 뒤 이관하며 새 초기화 스크립트로 기존 DB 비밀번호를 교체하지 않습니다.

## 연결 문제 확인

| 증상 | 확인할 항목 |
| --- | --- |
| external network를 찾지 못함 | ARTEX 기동 여부, 실제 네트워크 이름과 `ARTEX_NETWORK` |
| 주소 해석·연결 실패 | ARTEX와 `metasploit`의 공유 네트워크, 컨테이너 health 상태, 내부 포트 `3000` |
| 401·403 또는 Host 오류 | `Authorization=Bearer ...`, 실제 토큰, URL의 `metasploit:3000` |
| 도구 목록은 있지만 에이전트가 사용하지 못함 | MCP 사용 토글, 스킬 연결, 설정 반영 후 새 실행인지 여부 |
| 검사·실행·세션 변경 거부 | 컨테이너의 `MSF_MCP_DANGEROUS_ACTIONS` 값. 도구 목록만으로 판단하지 않음 |
| DB 조회 실패 | `metasploit-db` 상태, 저장 DB와 일치하는 비밀번호, 초기 default workspace 생성 여부 |

설정 전체를 출력하는 `docker compose config`·`docker inspect` 결과에는 비밀번호·토큰이 포함될 수 있습니다. 문제 공유에는 상태·오류만 추려 사용합니다.
