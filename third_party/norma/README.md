# Norma — ARTEX에서 사용하는 로컬 사본

[Norma](https://github.com/Autumn-27/norma)의 에이전트 실행 엔진을 ARTEX에서 사용하기 위한 사본입니다. 원본 라이선스와 저작권 고지를 유지합니다.

이 포크에서는 LLM 연결을 루트의 [`chatgptllm`](../../chatgptllm/) 패키지로 통합했습니다. Norma 내부의 Anthropic·Chat Completions·기존 Responses 어댑터와 해당 어댑터에 의존하는 독립 CLI·예제는 제거했습니다. 호스트가 `llm.Provider`를 주입하며, OpenAI API 키와 ChatGPT 구독 모두 같은 도구 실행 루프를 사용합니다.

| 구성 | 역할 |
| --- | --- |
| `llm` | 메시지·완료·스트리밍 인터페이스, 상태 누적, 속도 제한 |
| `agentcore`, `harness` | 세션·도구 호출·취소·하위 에이전트 실행 |
| `tool`, `permission`, `hook` | 파일·Shell·검색 도구와 실행 정책 |
| `mcp`, `skill` | 로컬 MCP 및 Skills 연결 |
| `compaction`, `noaadapter`, `transcript` | 대화 압축·이력 저장·재개 |
| `coordinator`, `subagent`, `memory`, `plan` | 기존 다중 에이전트·메모리·계획 기능 |

Responses 원형 상태를 위한 `Message.ProviderData`와 완료 메시지 전달을 유지합니다. 메시지 내용을 다시 쓰는 압축·투영은 해당 상태를 제거합니다. 요약은 정상 종료한 경우에만 적용합니다. DeepSeek 전용 검색은 제거했으며 DDGS·Brave·Tavily는 유지합니다.

루트 어댑터의 인증·상태 왕복 검사는 프로젝트 루트에서 `go test ./chatgptllm ./agent`로, 엔진 검사는 이 폴더에서 `go test ./...`로 실행합니다. 실제 사용·설치는 [프로젝트 README](../../README.md)를 참고하세요.
