---
name: web-cli
description: Nmap으로 서비스 정보를 확인하거나 ffuf·sqlmap과 SecLists로 웹 경로·인자·SQL 삽입 후보를 조사할 때 사용합니다.
---

# 웹·서비스 CLI 활용

CLI는 현재 에이전트의 `Bash`에서 실행합니다. 도구 목록의 `shell` 항목은 설치 환경을 알려주는 설명입니다. ARTEX Docker 이미지에 포함된 도구도 다른 설치 환경에서는 없을 수 있으므로 명령을 찾을 수 없으면 설치·경로 문제로 구분합니다.

| 도구 | 사용할 때 | 기본 확인 |
|---|---|---|
| `nmap` | 포트·서비스·버전과 스크립트 기반 정보를 확인할 때 | `nmap --help` |
| `ffuf` | 웹 경로·가상 호스트·인자 후보를 비교할 때 | `ffuf -h` |
| `sqlmap` | 실제 HTTP 요청의 SQL 삽입 후보를 조사할 때 | `sqlmap -hh` |
| SecLists | 목적에 맞는 경로·DNS·인자 사전이 필요할 때 | `/usr/share/seclists`의 설치된 파일 확인 |

설치된 사전은 `Discovery/Web-Content/`의 `common.txt`, `raft-medium-directories.txt`, `burp-parameter-names.txt`, `common-api-endpoints-mazen160.txt`와 `Discovery/DNS/subdomains-top1million-5000.txt`입니다. 대상과 가설에 맞는 목록을 고릅니다.

## 작업에 연결하기

1. 현재 작업 정보, ScopeSentry 결과, 브라우저 또는 API 조사 결과에서 대상과 확인할 가설을 가져옵니다. 이전 결과가 있다면 그 조건과 시점을 함께 봅니다.
2. 실제 요청의 URL·메서드·인자·인증 문맥에 맞게 도구를 고릅니다. 필요한 옵션은 설치된 버전의 도움말로 확인합니다.
3. 현재 작업 디렉터리에 원본 결과를 저장합니다. Worker에게 `intent_dir`가 지정되어 있으면 그 위치를 사용합니다. Nmap은 `-oA`/`-oX`, ffuf는 `-of json -o`, sqlmap은 `--output-dir`로 산출물을 남길 수 있습니다.
4. 응답 내용과 기준 요청을 비교해 후보를 해석하고 다음 탐색에 연결합니다. 일괄 실행의 종료 코드나 HTTP 200만으로 취약점 성공을 판단하지 않습니다.

예시 명령의 대상과 경로는 실제 작업 값으로 바꿉니다.

```bash
nmap -sV -oA ./service-scan '<대상>'
ffuf -u 'https://<대상>/FUZZ' -w /usr/share/seclists/Discovery/Web-Content/common.txt -of json -o ./paths.json
sqlmap -r ./request.txt -p '<확인할 인자>' --batch --output-dir=./sqlmap-output
```

ffuf는 같은 응답을 반환하는 경로, 로그인 리다이렉트, 가상 호스트에 따른 차이를 보고 필터를 조정합니다. sqlmap에는 재현할 요청을 전달하고 요청의 만료된 쿠키·CSRF 토큰·프록시 조건을 확인합니다. 옵션으로 바꾸는 테스트 강도와 실행 범위는 현재 작업 지침을 따릅니다.

Bash의 기존 프록시 환경과 도구 자체의 프록시 지원은 구분합니다. 프록시가 필요한 도구에는 도움말에 맞는 옵션을 사용하고 트래픽 기록 여부를 실제 기록으로 확인합니다. 모든 CLI 트래픽이 자동으로 ARTEX에 수집되는 것은 아닙니다.

후속 에이전트에는 실행 조건, 관찰 내용, 원본 결과의 **절대 경로**를 전달합니다. 작업 내 기록 도구가 있다면 기존 자산·사실·발견 흐름을 사용합니다.

사용법: [Nmap](https://nmap.org/book/man.html), [ffuf](https://github.com/ffuf/ffuf), [sqlmap](https://github.com/sqlmapproject/sqlmap/wiki/Usage), [SecLists](https://github.com/danielmiessler/SecLists).
