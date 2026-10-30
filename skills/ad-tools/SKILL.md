---
name: ad-tools
description: GOAD·Dracarys 등 Active Directory 실습과 진단에서 NetExec·Impacket·Certipy로 도메인 서비스·인증·AD CS 정보를 조사할 때 사용합니다.
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

## 조사 흐름

1. 작업에서 제공된 도메인·DC·DNS·대상 주소와 사용할 계정 또는 인증 수단을 확인합니다. GOAD·Dracarys라는 환경 이름만으로 도메인명·계정·토폴로지를 가정하지 않습니다.
2. 서비스 목록과 현재 가설에 따라 NetExec의 프로토콜, Impacket의 개별 스크립트 또는 Certipy를 고릅니다. 설치된 버전의 도움말에서 입력 형식을 확인합니다.
3. 인증 오류는 DNS·도메인 식별·시계 차이·대상 접근성·인증 방식의 영향을 나누어 해석합니다. 연결 실패를 계정 실패나 취약점 부재로 바로 판정하지 않습니다.
4. 응답에서 확인한 호스트·주체·그룹·권한·템플릿 관계를 현재 작업에 연결하고 다음에 확인할 경로를 선택합니다. 반환된 객체 식별자와 이름을 함께 유지합니다.

AD CS는 템플릿의 설정뿐 아니라 해당 주체의 권한과 실제 인증 경로를 함께 해석합니다. 도구가 표시한 취약 후보와 실제로 확인된 동작을 구분합니다. 선택한 스크립트가 수행하는 조회·실행·변경 작업은 현재 작업 목표에 맞춰 사용합니다.

출력은 현재 작업 디렉터리 또는 지정된 `intent_dir`에 저장합니다. 계정·키·인증서 파일은 후속 작업에 필요한 보관 위치로 참조하고 보고서에 자격 증명 원문을 반복하지 않습니다. 후속 에이전트에는 대상, 인증 방식, 관찰된 관계, 결과 파일의 절대 경로를 전달합니다.

사용법: [NetExec](https://www.netexec.wiki/), [Impacket](https://github.com/fortra/impacket), [Certipy](https://github.com/ly4k/Certipy/wiki).
