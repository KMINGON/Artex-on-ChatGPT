# 원본 대비 변경점과 OpenAI 호환성

개인 PC에서 **ChatGPT 구독 또는 OpenAI API 키로 ARTEX를 사용하기 위한 포크**입니다. 비교 기준은 한국어판 [artex-global의 `fdfb7a1`](https://github.com/jiwoochris/artex-global/commit/fdfb7a17b7da5e2de716c0e98475ec77a41c64c6)이며, 설치는 [README](../README.md#빠른-시작)를 참고하세요.

원본의 작업·Planner·Worker, 로컬 도구·MCP·Skills, 자산 그래프와 탐색 그래프를 유지하며 OpenAI 호출·상태 보존과 에이전트 간 결과 인계를 보완합니다. **원본 개선**은 비교 기준에서 이어진 동작의 보완, **포크 수정**은 이 프로젝트에서 추가·변경한 기능의 수정을 뜻합니다. 버그와 결함은 같은 범주로 관리합니다.

<a id="핵심-요약"></a>

| 분류 | 주요 내용 |
| --- | --- |
| [기능 추가](changes/features.md) | ChatGPT 구독, 브라우저 인증·이미지, 작업·자원 프리셋, 그래프 근거 인계 |
| [동작·사용성 변경](changes/behavior.md) | 모델 조회·연결·저장 흐름, 한국어·영어, 자산 관찰 범위·현재 실행 설정 표시 |
| [결함 수정](changes/fixes.md) | 두 그래프의 원자적 기록, Planner 상태 보존, 공유 파일·결과 조회, 압축·저장·취소 |
| [OpenAI 호환성](changes/openai.md) | Daybreak, 모델별 옵션, API·구독 차이, 실제 응답 메타데이터 |
| [설치·운영·정리](changes/operations.md) | Compose·네트워크·볼륨·기존 데이터, 로컬 Norma, Claude·Noa·문서·스크립트 정리 |
| [검증 기록](changes/validation.md) | 변경별 자동 검사, 격리 DB, 실제 계정·브라우저 관측과 남은 범위 |

최근 핵심 연결 수정에서는 **관찰 → 자산·사실 기록 → 근거를 연결한 후속 의도 → 목표 증명**이 이어지는 경로를 보완했습니다. 저장·연결 실패를 성공으로 표시하지 않고, 진행 중인 의도와 미완료 Todo가 남은 마지막 목표의 완료 근거를 기록합니다. 자세한 동작은 [그래프·에이전트 인계 수정](changes/fixes.md#그래프에이전트-인계)을 참고하세요.

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
