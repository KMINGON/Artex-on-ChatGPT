# 원본 소스와 준비 범위

| 항목 | 값 |
| --- | --- |
| 포팅 저장소 (`origin`) | [KMINGON/Artex-on-ChatGPT](https://github.com/KMINGON/Artex-on-ChatGPT) |
| 가져온 저장소 (`upstream`) | [jiwoochris/artex-global](https://github.com/jiwoochris/artex-global) |
| 기준 브랜치 | `main` |
| 기준 커밋 | [`fdfb7a17b7da5e2de716c0e98475ec77a41c64c6`](https://github.com/jiwoochris/artex-global/commit/fdfb7a17b7da5e2de716c0e98475ec77a41c64c6) |
| 커밋 시각 | 2026-10-14 16:52:43 +09:00 |
| 가져온 날짜 | 2026-10-19 |
| 최초 프로젝트 | [Autumn-27/ARTEX](https://github.com/Autumn-27/ARTEX) |

## 이번 준비 작업

- 기준 커밋의 추적 파일 791개를 저장소 루트로 가져왔습니다.
- `.gitignore`와 `.dockerignore`에 기존 로컬 분석·연구 자료 제외 규칙을 추가했습니다.
- `README.md` 맨 앞에 포팅판의 준비 상태를 추가했습니다. 원본 본문과 한국어를 포함한 다른 언어의 README는 보존했습니다.
- 이 출처 기록을 추가하고, 기존 `.nvmrc` (`24.11.1`)는 유지했습니다.
- 준비 커밋은 위 기준 커밋을 직접 부모로 삼습니다. 이후 개발 커밋은 원본 이력에서 이어집니다.
- ARTEX·Norma 실행 코드, 의존성 버전, 인증, UI, 배포·업데이트 설정은 수정하지 않았습니다.

이번 단계에는 ChatGPT 로그인 구현, 의존성 설치, 앱 실행, 배포가 포함되지 않습니다.
현재 소스의 LLM 설정은 여전히 원본의 API 키 기반 방식입니다.

## 로컬에만 보관하는 자료

`reference/`는 Git 추적과 Docker 빌드 컨텍스트에서 제외하는 개인 문서함입니다.
기존 분석 문서·소스 스냅샷, 포팅 연구, 생성 HTML, 문서 생성 설정, 공유 가이드와 검수 캐시를
모두 이 폴더 아래에 보관합니다. 기존 분석 전용 커밋은 이 저장소의 개발 이력에 포함하지 않습니다.

원본 `docs/defense-*.md`와 `docs/finding-traffic-evidence-*.md`는 문서 링크와 테스트에서
사용하므로 계속 추적합니다. `skills/`의 Markdown 파일도 실행에 필요한 원본 리소스입니다.

## 라이선스와 출처

원본의 [LICENSE](LICENSE) (GNU AGPL version 3)를 그대로 보존합니다.
포함된 웹 코드의 [web/LICENSE](web/LICENSE)에는 MIT 원문과
`Copyright (c) 2024 Mohammed Arham Khan` 고지가 있으며, 이 파일도 그대로 보존합니다.
이 준비 작업으로 원본이나 제3자 코드의 라이선스를 변경하지 않습니다.

Norma는 `go.mod`와 `go.sum`에 고정된 `github.com/Autumn-27/norma v0.4.3`을 유지합니다.
확인한 v0.4.3 소스에는 LICENSE 파일이 없어, 후속 버전의 라이선스를 소급해 붙이지 않았습니다.
