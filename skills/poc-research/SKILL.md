---
name: poc-research
description: 기존 웹 검색·본문 조회로 취약점과 PoC 자료를 찾고 Python·PHP·Node.js·Java 런타임에서 적용 조건·의존성·재현 결과를 비교할 때 사용합니다.
---

# 취약점 자료 검색과 PoC 재현

실제 도구 이름은 `web_search`입니다. 전역·에이전트 설정에서 활성화되어 있으면 검색에 사용합니다. 없으면 제공된 URL이나 아래 공식 자료를 `WebFetch`로 읽고 필요한 검색 설정을 확인합니다. 소스 파일은 `git` 또는 다운로드 도구로 가져와 `Read`와 `Bash`로 확인합니다. 연결된 도구 목록에 없는 별도 검색 MCP를 가정하지 않습니다.

## 사용할 도구와 입력

| 확보한 정보·목적 | 사용할 도구 |
| --- | --- |
| 제품·버전·CVE로 공지나 공개 코드 찾기 | `web_search`: `query`, 선택 `limit` |
| 공지·README 웹 페이지의 본문 읽기 | `WebFetch`: `url` |
| 실제 코드·정적 JavaScript 파일 읽기 | `Bash`로 git/다운로드 → 로컬 파일을 `Read` |
| Framework에 대응 모듈이 있는지 확인 | `metasploit` 스킬의 모듈 검색·정보 조회 |
| 확보한 PoC의 의존성 준비·실습·비교 | 아래 런타임을 현재 작업의 `Bash`에서 사용 |

`web_search` 입력 예시는 `{"query":"<제품명> <버전> security advisory proof of concept", "limit":5}`입니다. 결과는 제목·URL·요약이며 본문이 아닙니다. 선택한 문서 URL을 `WebFetch`의 `{"url":"<문서 URL>"}`로 읽습니다. 검색 실패·요청 제한과 정상 빈 결과를 구분하고, 검색 결과가 없다는 사실만으로 해당 취약점이나 PoC가 없다고 판단하지 않습니다. 큰 소스나 JS 파일은 WebFetch 대신 로컬 파일로 받아 필요한 부분을 읽습니다.

## 자료 선택

1. 관찰된 제품명·구성요소·버전·입력 지점을 기준으로 검색합니다. 제품 보안 공지, 공식 저장소의 advisory·패치, 공개 PoC 순으로 연결해 원인과 적용 조건을 확인합니다.
2. 보안 공지의 영향 버전·필수 설정·인증 조건과 실제 대상을 비교합니다. 런타임이 설치되어 있다는 사실과 특정 취약점을 재현할 수 있다는 사실은 구분합니다.
3. PoC의 소스·의존성·실행 인자를 읽고 대상 주소, 외부 통신, 생성·변경 파일을 파악합니다. 현재 작업에 맞는 요청과 실행 경로를 사용합니다.

일반적인 테스트 방법은 [OWASP WSTG](https://owasp.org/projects/web-security-testing-guide)와 [PortSwigger Web Security Academy](https://portswigger.net/web-security/all-topics)를 참조합니다. 최신 제품별 취약점의 적용 여부는 해당 제품의 공지와 패치에서 확인합니다.

## 로컬 실행 환경

| 계열 | 사용 가능한 명령 | 먼저 맞출 조건 |
|---|---|---|
| Python | `python3`, `python3 -m venv` | Python 버전·패키지 요구사항; PoC별 가상 환경 |
| PHP | `php` | PHP 버전·확장 모듈·CLI와 서버 실행의 차이 |
| Node.js | `node`, `npm` | Node 버전·모듈 형식·패키지와 잠금 파일 |
| Java | `java`, `javac`, `mvn` | JDK 버전·클래스 파일 버전·빌드 의존성 |

필요한 언어의 `--version`/`-version`과 도움말을 확인합니다. Docker 외 환경에서는 해당 명령이 설치되어 있지 않을 수 있습니다. PoC의 의존성은 작업 디렉터리에 준비하고 ARTEX 및 기본 도구의 전역 환경을 변경하지 않습니다.

확보한 저장소의 설명·코드를 읽은 뒤 필요한 명령을 고릅니다. 아래는 버전·실행 인자·의존성을 확인하는 예시이며 하나의 필수 실행 순서가 아닙니다.

```bash
python3 --version
python3 -m venv ./poc-venv
./poc-venv/bin/python -m pip install -r ./requirements.txt
php --version
php -m
node --version
npm --version
java -version
javac -version
mvn -version
```

Python 예시는 현재 디렉터리에 해당 PoC의 `requirements.txt`가 있는 경우입니다. Node는 해당 프로젝트의 잠금 파일과 scripts를 읽고 설치·실행 명령을 고르며, Java는 `pom.xml`과 JDK/컴파일 대상 버전을 확인합니다. PHP CLI에서 동작한 결과가 대상 웹서버의 SAPI·확장·설정과 같다고 가정하지 않습니다. 런타임 설치만으로 모든 제품별 PoC 의존성이 갖춰진 것은 아닙니다.

## 결과와 비교 자료

실행 결과는 현재 작업 디렉터리 또는 지정된 `intent_dir`에 저장합니다. 사용한 출처 URL·태그/커밋, 런타임 버전, 실제 요청·응답, 결과 파일 경로를 기존 작업 기록에 함께 남기면 후속 에이전트가 조건을 맞춰 비교할 수 있습니다. 대상 응답, 권한·상태 변화 등 가설에 맞는 근거를 해석하고, 실행 오류·적용 조건 불일치·재현 성공을 구분합니다.

인계 예: `출처 URL·commit / 대상 제품·버전과 인증 조건 / 실행 명령·런타임 / 관찰한 요청·응답 / 실패 또는 재현 결과 / 원문 절대 경로`. 패치·회귀 테스트·탐지 템플릿·공격 PoC는 목적이 다르므로 자료 유형을 구분합니다.

자료의 예시 결과나 PoC의 성공 문구만으로 현재 대상에서 확인된 취약점이라고 보고하지 않습니다. 독립 대화에서는 기록 도구의 존재를 가정하지 말고 관찰 내용과 파일의 절대 경로를 대화에 전달합니다.
