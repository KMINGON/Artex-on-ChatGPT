---
name: web-cli
description: 포트·서비스는 Nmap, 웹 후보 비교는 ffuf·SecLists, 확보한 요청의 SQL 삽입 조사는 sqlmap을 사용합니다. Bash 예시와 결과 해석을 제공합니다.
---

# 웹·서비스 CLI 활용

CLI는 현재 에이전트의 `Bash`에서 실행합니다. 도구 목록의 `shell` 항목은 설치 환경을 알려주는 설명입니다. ARTEX Docker 이미지에 포함된 도구도 다른 설치 환경에서는 없을 수 있으므로 명령을 찾을 수 없으면 설치·경로 문제로 구분합니다.

| 도구 | 현재 입력과 목적 | 기본 확인 |
|---|---|---|
| `nmap` | 호스트/IP를 알고 포트·서비스·버전이 필요할 때 | `nmap --help` |
| `ffuf` | 기준 URL/HTTP 요청이 있고 경로·가상 호스트·인자 후보의 응답을 비교할 때 | `ffuf -h` |
| `sqlmap` | 재현 가능한 HTTP 요청과 조사할 인자를 확보했을 때 | `sqlmap -hh` |
| SecLists | 목적에 맞는 경로·DNS·인자 사전이 필요할 때 | `/usr/share/seclists`의 설치된 파일 확인 |

ScopeSentry에 이미 수집한 결과는 `scopesentry` 스킬로 조회할 수 있습니다. 브라우저의 로그인·JavaScript 동작이 필요하면 현재 제공된 브라우저 도구를 사용하고, 확보한 HTTP 요청을 CLI 조사에 연결합니다.

설치된 사전은 `Discovery/Web-Content/`의 `common.txt`, `raft-medium-directories.txt`, `burp-parameter-names.txt`, `common-api-endpoints-mazen160.txt`와 `Discovery/DNS/subdomains-top1million-5000.txt`입니다. 대상과 가설에 맞는 목록을 고릅니다.

## 빠른 사용 예시

예시의 대상·인자는 실제 작업 값으로 바꿉니다. 현재 작업 디렉터리 또는 Worker에게 지정된 `intent_dir`에서 실행합니다.

```bash
nmap -sV -oA ./service-scan '<대상>'
ffuf -u 'https://<대상>/FUZZ' -w /usr/share/seclists/Discovery/Web-Content/common.txt -of json -o ./paths.json
sqlmap -r ./request.txt -p '<확인할 인자>' --batch --output-dir=./sqlmap-output
```

Nmap은 `.xml`의 포트·서비스·버전과 호스트 상태를, ffuf는 JSON의 후보 URL·상태·길이와 기준 응답을, sqlmap은 출력 디렉터리의 로그·세션과 실제 요청 조건을 읽습니다. 종료 코드나 HTTP 200만으로 취약점 성공을 판단하지 않습니다. 필터링된 결과가 비어 있는 경우와 요청 자체가 실패한 경우도 구분합니다.

인증 요청 재사용, ffuf 필터 조정, 프록시 옵션이 필요하면 `Read`로 `${SKILL_DIR}/references/requests-and-results.md`를 읽습니다. [요청·필터·결과 예시](references/requests-and-results.md)

Bash의 기존 프록시 환경과 도구 자체의 프록시 지원은 구분합니다. 프록시가 필요한 도구에는 도움말에 맞는 옵션을 사용하고 트래픽 기록 여부를 실제 기록으로 확인합니다. 모든 CLI 트래픽이 자동으로 ARTEX에 수집되는 것은 아닙니다.

후속 에이전트에는 대상·실행 조건, 관찰 내용, 실패/부분 결과, 원본 결과의 **절대 경로**를 전달합니다. 예: `443/tcp에서 HTTPS 확인; ffuf 후보 3개 중 2개는 로그인 리다이렉트; 원문 /<현재 작업 경로>/paths.json`. 작업 내 기록 도구가 있으면 새 자산은 `insert_assets`, 관찰·판단은 `record_fact`, 재현한 취약점은 `report_finding`의 기존 계약을 따릅니다. 독립 대화에는 사용할 수 있는 기록 도구와 파일 경로를 사용합니다.

사용법: [Nmap](https://nmap.org/book/man.html), [ffuf](https://github.com/ffuf/ffuf), [sqlmap](https://github.com/sqlmapproject/sqlmap/wiki/Usage), [SecLists](https://github.com/danielmiessler/SecLists).
