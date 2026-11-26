# Anthropic·로컬 모델 연결

[변경점 전체 목록](../changes.md) · 비교 기준: ARTEX/global `fdfb7a1`

원본은 환경 변수와 프로필에서 Anthropic Messages, OpenAI Chat Completions, OpenAI Responses 형식과 임의 주소를 골랐습니다. 현재는 **시스템 → LLM**의 연결 방식으로 아래 네 가지를 제공합니다. 환경 변수로는 OpenAI API 키만 설정합니다.

| 연결 방식 | 요청 형식 | 주소 | 키 |
| --- | --- | --- | --- |
| OpenAI API 키 | Responses | 공식 주소 고정 | 필수 |
| ChatGPT 구독 | Responses | 공식 주소 고정 | 계정 로그인 |
| Anthropic API | Messages (`/v1/messages`, `anthropic-version: 2026-10-21`) | 비우면 `https://api.anthropic.com`. 호환 게이트웨이 주소 입력 가능 | 필수 |
| OpenAI 호환 Local LLM | Chat Completions (`<Base URL>/chat/completions`) | 필수. 예: Ollama `http://127.0.0.1:11434/v1` | 선택 |

OpenAI 두 방식의 처리는 [OpenAI 처리](openai.md)에 있습니다.

## 설정과 키

- 모델 ID를 직접 입력하고 연결 확인에 성공한 뒤 저장합니다. 모델 목록 조회는 OpenAI 연결에만 있습니다.
- Base URL에 `/v1/messages`나 `/chat/completions`까지 붙여 넣으면 기준 주소로 바꿉니다. 사용자 정보·쿼리·조각이 들어간 주소는 거부합니다.
- 키는 같은 프로필에 저장한 주소로만 보냅니다. 연결 방식이나 주소를 바꾸면 저장된 키를 재사용하지 않습니다. Anthropic은 새 키를 요구하고, Local LLM은 저장된 키를 지웁니다. 연결 확인·모델 목록·단일 설정 저장도 다른 연결의 키를 빌려 쓰지 않습니다.
- 추론 강도는 Anthropic이 `low`~`max`(`output_config.effort`), Local LLM이 OpenAI 이름의 `reasoning_effort`입니다. 서버가 지원할 때만 지정하세요. OpenAI 액세스 프로그램(Daybreak)은 적용하지 않습니다.
- 두 연결은 순환(장애 전환) 체인에 들어가지 않습니다. 이 연결이 활성일 때 다른 공급자로 자동 전환하지 않습니다.
- Docker로 실행한 ARTEX에서 호스트의 로컬 서버는 `http://host.docker.internal:<포트>/v1`로 연결합니다.

## 요청과 응답 처리

| 항목 | 동작 |
| --- | --- |
| 샘플링 | `temperature` 등 샘플링 값을 보내지 않습니다 |
| 사고 블록 | Anthropic의 사고 내용은 실행 기록에 표시하고 다음 요청에는 다시 보내지 않습니다. 대화 압축·재개로 이력이 바뀌어도 요청이 거부되지 않습니다 |
| 이미지 | Anthropic은 스크린샷·이미지를 그대로 전달합니다. Local LLM에는 이미지 대신 설명 문구를 보냅니다 |
| 도구 호출 | 두 형식 모두 도구 호출과 결과를 주고받습니다. Local LLM은 서버가 OpenAI `tools` 형식을 지원해야 합니다 |
| 완료 판정 | Anthropic은 `message_stop`, Local LLM은 `finish_reason` 또는 `[DONE]`으로 판단합니다. HTTP 200 뒤 완료 신호 없이 끊긴 스트림은 오류입니다 |
| 오류 표시 | 공급자 오류 문구를 상태 코드와 함께 실행 기록에 표시합니다. 스트림 안의 오류 객체도 같은 방식으로 표시합니다 |
| 거부 | Anthropic의 `refusal`은 분류와 설명을 붙여 오류로 표시하고 같은 요청을 다시 보내거나 다른 공급자로 넘기지 않습니다 |
| 재시도 | 응답 시작 전 408·429·5xx와 연결 오류만 프로필의 연결 재시도 설정에 따라 다시 요청합니다 |

## 이전 프로필

OpenAI 전용 버전 이전에 저장한 Anthropic·사용자 지정 주소 프로필은 그대로 실행하지 않습니다. 목록에서 설정 갱신이 필요하다고 표시하며, 연결 방식과 주소·키를 다시 선택해 저장하면 같은 프로필 ID로 바뀝니다.

## 검증 범위

모의 Messages·Chat Completions 서버로 연결 확인, 프로필 저장, 작업 입력 전달, 도구 호출 왕복, 스트림 완료, 일시정지 취소, 401·스트림 오류·끊긴 스트림 표시를 확인했습니다. 실제 Anthropic 계정과 실제 로컬 모델 서버로는 확인하지 않았습니다. 모델별 도구 호출 품질과 지원 옵션은 연결한 모델과 서버에 따라 다릅니다. [검증과 한계](validation.md)

관련 구현: [`third_party/norma/llm/anthropic.go`](../../third_party/norma/llm/anthropic.go), [`third_party/norma/llm/openai.go`](../../third_party/norma/llm/openai.go), [`agent/provider.go`](../../agent/provider.go), [`db/config.go`](../../db/config.go).
