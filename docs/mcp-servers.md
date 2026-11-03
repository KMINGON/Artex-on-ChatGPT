# MCP 서버 실행·연결

[README](../README.md)대로 ARTEX를 먼저 실행한 뒤, 필요한 MCP 서버만 아래 명령으로 준비합니다. Docker Compose와 Python 3가 필요하며 명령은 저장소 루트에서 실행합니다.

독립 MCP 실행은 `v0.2.1` 이상 소스에서 지원합니다. 이미 실행 중인 ARTEX에도 재생성 없이 연결할 수 있습니다.

## Metasploit

```sh
python3 scripts/prepare-mcp.py metasploit up
```

네트워크 확인, 토큰·DB 비밀번호 생성과 서버 기동을 처리합니다. ARTEX의 **시스템 → MCP → Metasploit → 설정**에 다음을 입력합니다.

| 필드 | 입력 |
| --- | --- |
| 전송 방식 | `http (원격)` |
| 원격 URL | `http://metasploit:3000/` |
| 요청 헤더 | `Authorization=Bearer <토큰>` |

`<토큰>`은 `.local/mcp/metasploit/.env`의 `MSF_MCP_AUTH_TOKEN` 값입니다. 파일의 따옴표는 제외합니다. dev의 실행 허용 기본값은 `true`이며, 조회용 `false`로 바꾸는 방법은 [Metasploit 상세 설정](mcp/metasploit.md#falsetrue-실행-권한)에 있습니다.

## ScopeSentry

```sh
python3 scripts/prepare-mcp.py scopesentry up
```

네트워크 확인, 서버·Scanner·DB 기동과 API 키 발급·인증 확인까지 처리합니다. ARTEX의 **시스템 → MCP → ScopeSentry → 설정**에 다음을 입력합니다.

| 필드 | 입력 |
| --- | --- |
| 전송 방식 | `http (원격)` |
| 원격 URL | `http://scopesentry:8082/mcp` |
| 요청 헤더 | `X-API-Key=<API 키>` |

`<API 키>`는 `.local/mcp/scopesentry/.env`의 `ARTEX_SCOPESENTRY_API_KEY` 값입니다. 따옴표나 `Bearer`를 붙이지 않습니다. 웹은 `http://127.0.0.1:18083`이며 로그인·Scanner 대상 네트워크는 [ScopeSentry 상세 설정](mcp/scopesentry.md)에 있습니다.

## ARTEX에서 사용 시작

1. 위 설정을 저장하고 **도구 목록 → 새로 고침**으로 연결을 확인합니다. 요청 헤더는 JSON이 아닌 `KEY=VALUE` 형식입니다.
2. 사용할 MCP의 **사용** 토글을 ON으로 바꿉니다. 기본값은 OFF이며 `up`이 ARTEX 설정을 자동 변경하지 않습니다.
3. 기본 `metasploit`·`scopesentry` 스킬은 침투 테스트·실행 에이전트에 연결돼 있으므로, 새 대화나 Worker 실행에서 사용할 수 있습니다. 기존에 해제했다면 해당 스킬 연결을 다시 선택합니다.

재실행도 같은 `up` 명령을 사용합니다. 기존 설정·인증값은 유지하며 비밀값 자체는 터미널에 출력하지 않습니다. 첫 실행의 이미지 다운로드와 ScopeSentry Scanner 자체 검사는 시간이 걸릴 수 있습니다.

ARTEX가 여러 개이거나 네트워크 자동 선택이 되지 않으면 `--network <실제 네트워크 이름>`을 지정합니다. 네트워크 확인, 포트·실행 권한 변경, 중지·삭제와 문제 해결은 서버별 상세 가이드를 참고하세요.

- [Metasploit 상세 설정](mcp/metasploit.md)
- [ScopeSentry 상세 설정](mcp/scopesentry.md)
