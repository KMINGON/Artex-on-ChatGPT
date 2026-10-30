---
name: metasploit
description: Metasploit 모듈을 검색하고 대상 조건에 맞는 검사·실행 결과와 세션 상태를 MCP로 확인할 때 사용합니다.
mcps: Metasploit
---

# Metasploit MCP 활용

ARTEX의 `Metasploit` MCP가 구성·활성화되어 있어야 합니다. 스킬에 공개된 도구를 `SearchExtraTools`로 검색하고, 반환된 전체 이름과 입력 스키마로 `ExecuteExtraTool`을 호출합니다. 목록이 없으면 현재 세션에서 사용할 수 없는 상태로 보고합니다.

## 모듈 선택과 결과 확인

1. 확인한 서비스·제품·버전·CVE를 `msf_search_modules`로 검색합니다.
2. `msf_module_info`에서 모듈의 대상, 옵션, 참조를 읽고 현재 대상과 맞춥니다. 모듈이 검색된 사실만으로 취약하다고 판단하지 않습니다.
3. 작업 목적에 따라 `msf_module_check` 또는 `msf_module_execute`를 사용합니다. 현재 서버가 허용한 기능과 실제 입력 스키마를 따릅니다.
4. 반환된 `uuid`로 `msf_module_results`를 조회합니다. `job_id`·`uuid` 반환은 접수 결과이며 완료나 성공 증거가 아닙니다. 오류·실행 중·완료 상태와 실제 결과를 구분합니다.
5. 세션이 관련되면 `msf_session_list`에서 대상과 연결된 세션을 확인하고, 필요한 읽기·입력·종료 도구를 사용합니다. 세션 ID만으로 현재 작업 소유라고 가정하지 않습니다.

원본 서버는 검사·실행·세션 입력·종료 기능에 별도 실행 설정을 사용합니다. 해당 호출이 거부되면 설정 상태를 설명하고 성공한 것으로 기록하지 않습니다. 데이터베이스 조회 도구에는 Metasploit의 DB 연결도 필요합니다.

ARTEX 중지·취소와 원격 Metasploit 작업·세션의 종료는 별개입니다. 재개할 때는 저장된 UUID와 현재 서버 상태를 대조하고, 기록된 ID가 더 이상 유효하지 않으면 이를 명시합니다. 관찰 결과와 실행 식별자를 기존 작업 기록 또는 대화에 전달합니다.

서버 계약: [공식 MCP 사용법](https://github.com/rapid7/metasploit-framework/blob/master/docs/metasploit-framework.wiki/How-to-use-Metasploit-MCP-Server.md).
