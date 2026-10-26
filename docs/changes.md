# 원본 대비 변경점과 OpenAI 호환성

개인 PC에서 **ChatGPT 구독 또는 OpenAI API 키로 ARTEX를 사용하기 위한 포크**입니다. 비교 기준은 한국어판 [artex-global의 `fdfb7a1`](https://github.com/jiwoochris/artex-global/commit/fdfb7a17b7da5e2de716c0e98475ec77a41c64c6)이며, 설치는 [README](../README.md#빠른-시작)를 참고하세요.

원본의 작업·Planner·Worker, 로컬 도구·MCP·Skills, 자산 그래프와 탐색 그래프를 유지하며 OpenAI 호출·상태 보존과 에이전트 간 결과 인계를 보완합니다. 아래 문서는 **원본 대비 최신 버전의 최종 차이와 현재 동작**을 설명합니다. 원본 기능의 결함 수정과 이 포크에서 추가한 기능을 구분합니다.

<a id="핵심-요약"></a>

| 분류 | 주요 내용 |
| --- | --- |
| [기능 추가](changes/features.md) | ChatGPT 구독, 브라우저 인증·이미지, 작업·자원 프리셋, 그래프 근거 인계 |
| [동작·사용성 변경](changes/behavior.md) | 모델 조회·연결·저장 흐름, 한국어·영어, 자산 관찰 범위·현재 실행 설정 표시 |
| [결함 수정](changes/fixes.md) | 원자적 그래프 기록, 탐색·미완료 인계, 최신 실행 결과, 압축·저장·재개, 조기 완료·명령 오탐 방지 |
| [OpenAI 호환성](changes/openai.md) | Daybreak, 모델별 옵션, API·구독 차이, 실제 응답 메타데이터 |
| [설치·운영·정리](changes/operations.md) | Compose·네트워크·볼륨·기존 데이터, 로컬 Norma, Claude·Noa·문서·스크립트 정리 |
| [검증 범위](changes/validation.md) | 자동 검사 방법, 격리 DB와 실제 계정·브라우저의 확인 범위, 미검증 한계 |

**Planner는 전체 범위를 관리하고 Worker는 독립 검증 질문 하나를 깊게 수행**합니다. 같은 질문의 준비·정상 대조·재현·영향 확인을 이어가면서 대기 중인 독립 방향도 착수하도록 안내합니다. 결과 전문과 원래 미완료 질문·근거 ID·재개 조건을 후속 계획에 전달하며 다른 질문의 결과로 대체하지 않습니다. 재검증 횟수나 사실 개수만으로 탐색을 닫지 않으며 대상의 오류 응답과 실제 도구 차단을 구분합니다. 명시된 사용자 범위·도구 차단·취소·시간 제한은 유지합니다. [의도 배정·미완료 인계](changes/fixes.md#의도-배정과-미완료-인계)에서 지침과 한계를 설명합니다.

**관찰 → 자산·사실 기록 → 후속 의도 → 목표 증명**의 저장과 조회를 연결합니다. 기록 실패를 성공으로 처리하지 않고, 열린·실행 중·일시정지 의도가 남은 마지막 목표 증명을 거절합니다. 완료 직전에도 새 근거와 계획 변경을 다시 확인합니다. [그래프·에이전트 인계](changes/fixes.md#그래프에이전트-인계)를 참고하세요.

사용법: [브라우저·이미지·실행 프리셋](browser-and-presets.md) · [트래픽 증거](finding-traffic-evidence.md) · [Daybreak 선택](changes/openai.md#daybreak-선택) · [업데이트·데이터 보존](changes/operations.md#업데이트데이터-보존)

<details>
<summary>이전 단일 문서의 항목별 바로가기</summary>

- <a id="기능-추가"></a>[기능 추가](changes/features.md#기능-추가)
- <a id="로그인모델-연결"></a>[로그인·모델 연결](changes/features.md#로그인모델-연결)
- <a id="브라우저이미지프리셋"></a>[브라우저·이미지·프리셋](changes/features.md#브라우저이미지프리셋)
- <a id="동작사용성-변경"></a>[동작·사용성 변경](changes/behavior.md#동작사용성-변경)
- <a id="모델-설정-화면"></a>[모델 설정 화면](changes/behavior.md#모델-설정-화면)
- <a id="한국어화"></a>[한국어화](changes/behavior.md#한국어화)
- <a id="버그호환성-수정"></a>[버그·호환성 수정](changes/fixes.md#버그호환성-수정)
- <a id="브라우저이미지프록시"></a>[브라우저·이미지·프록시](changes/fixes.md#브라우저이미지프록시)
- <a id="작업-템플릿설정"></a>[작업 템플릿·설정](changes/fixes.md#작업-템플릿설정)
- <a id="openai프로필-연결"></a>[OpenAI·프로필 연결](changes/openai.md#openai프로필-연결)
- <a id="기억실행-안정성"></a>[기억·실행 안정성](changes/fixes.md#기억실행-안정성)
- <a id="설치운영-개선"></a>[설치·운영 개선](changes/operations.md#설치운영-개선)
- <a id="제거정리"></a>[제거·정리](changes/operations.md#제거정리)
- <a id="noa원본-잔재-정리"></a>[Noa·원본 잔재 정리](changes/operations.md#noa원본-잔재-정리)
- <a id="api구독-상세-동작"></a>[API·구독 상세 동작](changes/openai.md#api구독-상세-동작)
- <a id="daybreak-선택"></a>[Daybreak 선택](changes/openai.md#daybreak-선택)
- <a id="openai-api-키"></a>[OpenAI API 키](changes/openai.md#openai-api-키)
- <a id="chatgpt-구독"></a>[ChatGPT 구독](changes/openai.md#chatgpt-구독)
- <a id="api와-구독의-차이"></a>[API와 구독의 차이](changes/openai.md#api와-구독의-차이)
- <a id="업데이트데이터-보존"></a>[업데이트·데이터 보존](changes/operations.md#업데이트데이터-보존)
- <a id="검증-범위"></a>[검증 범위](changes/validation.md#검증-범위)
- <a id="작업-템플릿-통합-검증"></a>[작업 템플릿 통합 검증](changes/validation.md#작업-템플릿-통합-검증)
- <a id="브라우저이미지프리셋-검증"></a>[브라우저·이미지·프리셋 검증](changes/validation.md#브라우저이미지프리셋-검증)

</details>
