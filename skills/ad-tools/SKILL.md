---
name: ad-tools
description: GOAD·Dracarys 등 AD 조사에서 다중 호스트는 NetExec, 개별 프로토콜은 Impacket, 인증서 서비스는 Certipy를 선택하고 Bash로 사용합니다.
---

# Active Directory 도구 활용

현재 에이전트의 `Bash`에서 설치된 CLI를 사용합니다. Docker 이미지에는 도구마다 별도 Python 환경을 사용하므로 시스템 Python에 패키지를 다시 설치하거나 서로의 의존성을 섞지 않습니다.

| 도구 | 역할 | 도움말 |
|---|---|---|
| NetExec `nxc` (`netexec` 별칭) | SMB·LDAP 등 여러 호스트의 서비스·인증·설정 비교 | `nxc --help`, `nxc smb --help`, `nxc ldap --help` |
| Impacket `impacket-<스크립트명>` | 필요한 AD 프로토콜을 개별 스크립트로 조사·재현 | `impacket-GetADUsers -h`, `impacket-smbclient -h` |
| Certipy `certipy` | AD CS 인증 기관·템플릿·인증서 관련 조건 조사 | `certipy -h`, `certipy find -h` |
| `nmap` | 도메인 컨트롤러와 관련 서비스의 연결 상태 확인 | `nmap --help` |

Impacket 원본 스크립트는 `/opt/artex-tools/impacket/bin/`에 있습니다. 별칭에는 `.py`를 붙이지 않습니다. Python 환경은 `/opt/artex-tools/netexec`, `/opt/artex-tools/impacket`, `/opt/artex-tools/certipy`로 분리되어 있습니다.

## 입력과 선택

작업에서 제공된 도메인·DC·DNS·대상 주소와 사용할 계정 또는 인증 수단을 가져옵니다. GOAD·Dracarys라는 환경 이름만으로 도메인명·계정·토폴로지를 가정하지 않습니다. 도메인 DNS 이름, 계정의 도메인, 실제 연결할 DC IP를 각각 유지합니다.

| 확인하려는 정보 | 선택 예 |
| --- | --- |
| 여러 호스트의 SMB 정보·공유 접근성 | `nxc smb` |
| 도메인 사용자 등 LDAP 객체 | `nxc ldap` 또는 `impacket-GetADUsers` |
| 특정 SMB/RPC/Kerberos 동작 | 목적에 맞는 `impacket-<스크립트명>` |
| AD CS 기관·템플릿·관련 주체 권한 | `certipy find` |

## 빠른 사용 예시

아래 IP·도메인은 예시이며 실제 작업 값으로 바꿉니다. 인증이 필요한 명령의 계정·티켓 입력과 파일 출력 예시는 `Read`로 `${SKILL_DIR}/references/commands-and-results.md`를 읽습니다. [AD 명령·결과 예시](references/commands-and-results.md)

```bash
nmap -sV -p 53,88,135,389,445,636,3268,3269 -oA ./ad-services 192.0.2.10
nxc smb 192.0.2.10
impacket-GetADUsers -h
certipy find -h
```

서비스·배너 조회와 인증된 객체 조회는 별개의 결과입니다. 예시의 포트 목록은 대표 AD 서비스이며 필요한 포트·조사 범위는 현재 관찰에 맞춥니다. 인증 오류는 DNS·도메인 식별·시계 차이·접근성·인증 방식의 영향을 나누어 해석합니다.

AD CS는 템플릿의 설정뿐 아니라 해당 주체의 권한과 실제 인증 경로를 함께 해석합니다. 도구가 표시한 취약 후보와 실제로 확인된 동작을 구분합니다. 선택한 스크립트가 수행하는 조회·실행·변경 작업은 현재 작업 목표에 맞춰 사용합니다.

출력은 현재 작업 디렉터리 또는 지정된 `intent_dir`에 저장합니다. 반환된 객체 식별자와 이름을 함께 유지합니다. 예: `DC IP·도메인, 사용한 주체, 조회된 공유/템플릿, 관찰된 권한, 실패한 조회, /<작업 경로>/ad-cs_Certipy.json`. 계정·키·인증서 파일은 보관 위치로 참조하고 보고서에 자격 증명 원문을 반복하지 않습니다. 작업에서는 현재 제공된 자산·사실·발견 기록 도구를, 독립 대화에서는 사용할 수 있는 기록 도구와 파일 경로를 사용합니다.

사용법: [NetExec](https://www.netexec.wiki/), [Impacket](https://github.com/fortra/impacket), [Certipy](https://github.com/ly4k/Certipy/wiki).
