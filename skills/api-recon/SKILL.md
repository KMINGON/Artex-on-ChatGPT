---
name: api-recon
description: 웹사이트의 API 인터페이스를 수집할 때 사용하는 스킬입니다.
---

# API Recon(프런트엔드 인터페이스 조사)

**승인된 범위**에서 다음을 최대한 빠짐없이 찾습니다. **백엔드 API**(경로, 메서드, 인자, 응답 본문), **프런트엔드 경로**, **UI 기능 실행 지점**(탭, 팝업, 테이블 작업 등).

---

## 범위와 금지 사항(에이전트 필독)

이 스킬은 **API / 인자 조사만 수행**합니다. 취약점 탐색이나 침투·악용 단계가 아닙니다. 아래 mock 제한은 이 조사 스킬에만 적용합니다. 일반 브라우저 도구로 사용자가 제공한 테스트 계정에 로그인하거나 같은 인증 세션에서 검증하는 흐름을 금지하지 않습니다. mock으로 나타난 화면·성공 응답을 실제 서버의 인증 우회나 취약점 성공 증거로 보고하지 마세요.

### 작업 범위

| 범위 | 허용 | 금지 |
|---|---|---|
| **목표** | path, method, 인자, 경로, UI 실행 지점 열거 | SQLi/XSS/권한 우회/무차별 대입/fuzz 취약점 탐색, 패킷 변조 공격, 파괴적 작업 |
| **인증** | Hook + stub/mock으로 **클라이언트** 로그인 조건 우회 | 사용자에게 계정·비밀번호를 요구하거나 추측하기, 실제 로그인 폼 제출 시도 |
| **런타임** | 자격 증명 없이 인터페이스를 hook하고 mock 응답으로 SPA의 로그인 후 화면 구조를 렌더링 | 실제 백엔드 세션이 있어야 진행 가능한 흐름 |

### 자격 증명 없는 동적 분석(Phase 3 기본)

1. `preload.js` / `runtime_harvest.js`로 로그인·권한·메뉴 등 bootstrap 인터페이스를 **가로채고 stub 처리**합니다.
2. 업무 조회 인터페이스에 **올바른 구조, 성공 상태 코드, 비어 있어도 되는 데이터**를 갖춘 mock body를 반환합니다.
3. 백엔드가 없거나 401 응답이 와도 프런트엔드에서 로그인 후 화면을 렌더링하여 더 많은 XHR/fetch/WebSocket을 실행하게 합니다.
4. **빈 데이터, 빈 테이블, 자리 표시 UI는 예상된 결과**입니다. 이를 이유로 실제 로그인이나 취약점 테스트로 전환하지 마세요.

mock으로 프런트엔드 경로와 컴포넌트를 로드하고 **outbound 요청만 기록**합니다. 백엔드의 반환 내용보다 프런트엔드가 **추가로 어떤 인터페이스를 호출하는지** 확인하는 것이 목적입니다.

### 절차상 금지 사항

| 금지 | 대체 방법 |
|---|---|
| Phase 1 완료 전에 주 entry `index-*.js`를 grep/curl/Read하여 API path 추출 | `OUTDIR/harvest_static.py` 실행 |
| harvest 대신 `extract_apis.py` 등의 스크립트를 직접 작성 | `OUTDIR/harvest_static.py`를 수정한 뒤 다시 실행 |
| 동일한 grep/명령이 2회 이상 실패했는데도 반복 | 전략 변경: tool_logs 확인, harvest 수정, reference 확인 |
| 점검 A/B를 건너뛰고 `scripts/` 원본 직접 실행 | OUTDIR에 복사하고 대상에 맞게 수정 |
| 실제 사용자 이름/비밀번호, OTP, OAuth 등을 이용한 인증 | stub/mock 사용(위 내용 참조) |
| 실제 데이터를 얻겠다는 이유로 stub을 생략하고 권한 우회/주입 테스트 | outbound만 기록하며 조사 범위를 유지 |
| 삭제, 민감 데이터 내보내기, 일괄 쓰기 등 되돌릴 수 없는 작업 | coverage에서 클릭할 때도 동일하게 금지 |
| runtime + 동적 열거를 완료하지 않고 모든 페이지와 인터페이스를 확보했다고 주장 | 완료 기준 확인 또는 한계 명시 |
| 인자 실행 행렬 + diff를 완료하지 않고 모든 인자를 파악했다고 주장 | Phase 3b 행렬 + Phase 5 diff 수행 |
| 단일 runtime 표본으로 필수/선택 여부 추론 | 여러 표본 diff 또는 검증 규칙/오류로 역추적 |

---

## 두 가지 분석 계층과 실행 모드

| 계층 | 산출물 | 한계 |
|---|---|---|
| **정적**(JS bundle) | 전체 endpoint 경로, 경로 초안, 요청 구성 위치의 필드 후보 | HTTP 메서드 없음. 인자는 Phase 1b 필요. 런타임에 조합되는 URL 누락 |
| **런타임**(실행 중 세션) | 메서드 + body + 응답 + 동적 URL + WS/SSE. 여러 표본 diff로 인자 보완 | 페이지가 실제로 렌더링되어야 요청 발생. 단일 표본으로 필수/선택 여부 확정 불가 |

| 실행 모드 | 엔진 | 용도 |
|---|---|---|
| **depth** | `runtime_harvest.js`(Playwright) | API 목록, METHOD/params/응답 본문, WS/SSE, 재현 가능한 일괄 실행 |
| **coverage** | browser + `preload.js` | 탭/팝업/테이블 클릭으로 기능 실행 범위 확대 |
| **both** | depth 이후 coverage | 가장 넓은 범위, 가장 긴 소요 시간 |

**인자 조사 방법**(범용 스크립트 없음): path는 harvest/정규식으로, 인자는 **기준점 주변 코드 확장 + UI 바인딩 추적 + 여러 표본 diff + 오류 역추적**으로 찾습니다(grep 예시는 [reference.md](reference.md)의 J절 참조).

---

## 완료 기준

다음을 모두 충족해야 조사를 완료했다고 할 수 있습니다.

- [ ] **정적**: Phase 1 harvest가 `api_static.txt`, `routes.txt`, `js/` 생성
- [ ] **런타임**: depth 또는 coverage 중 하나 이상 수행. coverage/both는 **Hook 적용 + 동적 열거 반복** 필요
- [ ] **화면 진입**: 업무 path 접근 시 `/login`이 아님(hash 경로 주의)
- [ ] **인자**: coverage/both에서 인자 실행 행렬 + `param_samples.json` 완료. Phase 5에서 `params_merged.json` 병합
- [ ] **깊이**(모듈 페이지가 비어 있다면): Phase 4 권한 트리 복원 후 재실행하여 **모듈 수준 API** 확인(locale/bootstrap만으로는 부족)
- [ ] **산출물**: Phase 5 산출물 구비(Phase 5 표 참조). `insert_assets`로 서비스와 엔드포인트 자산 저장

---

## 스크립트와 사전 점검

`scripts/`는 참고 템플릿입니다. **원본을 그대로 실행한 결과를 최종 결과로 사용하지 마세요.**

**규칙**: 먼저 읽기 → 대상에 맞게 수정 → `OUTDIR`(예: `recon/`)에 저장 → `CHANGES.md` 기록. 맞지 않으면 구조만 참고하여 조사 방법에 따라 다시 작성합니다.

| 점검 | 시점 | 참고 스크립트 → OUTDIR 복사본 | 일반적인 수정 항목 |
|---|---|---|---|
| **A(정적)** | Phase 0 이후, harvest/spider **첫 실행** 전 | `harvest_static.py` / `spider_mpa.py` | **대부분의 사이트는 기본 regex로 실행 가능**. manifest/형식이 맞지 않을 때만 endpoint 정규식, webpack/Vite `publicPath`, MPA exclude/cookie 수정 |
| **B(런타임)** | Phase 2 이후, depth/coverage 실행 전 | `runtime_harvest.js` / `preload.js` + `config.json` | Cookie/localStorage 키, neutralize 성공 값, stubs, login 정규식, api 접두사, hash/history |

**SPA 필수 순서**(순서 변경 불가. 먼저 탐색하고 스크립트를 실행하는 일반 관행보다 Phase 번호가 우선):

| 단계 | 필수 | 금지 |
|---|---|---|
| Phase 0 완료 후 | 다음 Bash = `python3 OUTDIR/harvest_static.py <URL> OUTDIR` | 주 entry `index-*.js`에 curl/grep/Read 실행(보통 >500KB) |
| 점검 A | 스크립트 복사 → 필요한 부분만 수정 → **즉시 실행** | API를 수동 추출한 뒤 harvest 여부 결정 |
| Phase 1 완료 전 | `wc -l`로 산출물 확인. 404 발생 시 harvest 수정 후 재시도 | extract 스크립트 직접 작성, 다운로드하지 않은 URL에 반복 grep |
| Phase 1b부터 | `OUTDIR/js/*.js`에만 grep | 주 bundle로 harvest 대체 |

- ✅ `harvest_static.py` 복사 → (선택) regex 수정 → **즉시 실행**
- ❌ 주 bundle curl → 여러 번 grep → 임시 extract 작성 → 마지막에 harvest
- **MPA**: Phase 0 이후 다음 Bash = `python3 OUTDIR/spider_mpa.py ...`

---

## 도구와 출력 제약

| 제약 | 설명 |
|---|---|
| 큰 파일 | >100KB `index-*.js`를 Read/grep으로 컨텍스트에 넣는 행위 **금지**. OUTDIR 스크립트로 일괄 처리 |
| grep 출력 | 반드시 `\| head -20` 또는 `-m 5` 사용. 대화에는 path 요약만 남기고 bundle 조각을 붙여넣지 않음 |
| 검증 | `wc -l`, `ls \| wc -l` 사용. 전체 디렉터리를 Read하지 않음 |
| regex 사전 탐색 | 선택, 최대 1회, ≤50KB의 작은 chunk 또는 HTML에 한함. 정식 정적 분석은 harvest 기준 |
| reference | 예시/템플릿/문제 해결은 [reference.md](reference.md) 참조. 전문을 반복 삽입하지 않음 |

---

## 실행 순서

```
Phase 0 분류 + OUTDIR
  → 점검 A → Phase 1 harvest(★ 즉시 실행 ★)
  → Phase 1b 인자 역추적
  → Phase 2 인증의 세 가지 조건 → config.json
  → 점검 B → Phase 3 런타임 + 인자 행렬
  → Phase 4 권한 트리(필요한 경우) → Phase 3 재실행
  → Phase 5 보고서 병합 + insert_assets로 발견한 모든 서비스·엔드포인트 API 자산 일괄 저장. 발견한 자산을 누락해서는 안 됨
```

순서대로 확인합니다. **이전 항목을 완료하기 전에는 다음 Phase로 넘어가지 마세요.**

1. [ ] **Phase 0**: SPA/MPA 사전 확인. `OUTDIR` 생성 → [Phase 0](#phase-0--분류)
2. [ ] **점검 A + Phase 1**: 스크립트 복사 → **즉시** harvest → `wc -l` 검증 → [Phase 1](#phase-1--정적-분석)
3. [ ] **Phase 1b**: 기준점 주변 코드 확장 + 바인딩 계층 → `param_candidates.json` → [Phase 1b](#phase-1b--인자-역추적)
4. [ ] **Phase 2**: 인증의 세 가지 조건 → `config.json` → [Phase 2](#phase-2--인증의-세-가지-조건)
5. [ ] **점검 B**: runtime 스크립트 수정 → [Phase 3](#phase-3--런타임)
6. [ ] **Phase 3**: depth / coverage / both. 화면 진입 확인. 인자 실행 행렬 → `param_samples.json`
7. [ ] **Phase 4**(필요 시): 권한 트리 → stubs 수정 → Phase 3 재실행 → [Phase 4](#phase-4--권한-트리-복원)
8. [ ] **Phase 5**: 산출물 병합 + 보고서 + `insert_assets` → [Phase 5](#phase-5--병합과-보고서)

---

## Phase 0 — 분류

진입 HTML을 가져오고 **`OUTDIR`를 생성**합니다(스킬 안의 `scripts/`는 수정하지 않음).

- **SPA**: 빈 화면 구조 + `<div id=app>` + chunk → Phase 1–5
- **MPA**: SSR + `<form>`, endpoint bundle 없음 → 점검 A 이후:

```bash
python3 recon/spider_mpa.py <BASE_URL> <OUTDIR> [--cookie "session=..."] [--max 300] [--depth 5] [--exclude "logout|delete|destroy"]
```

`forms.txt`, `links.txt`, `api_inline.txt`를 생성합니다. SPA에서 forms ≈ 0이면 Phase 1로 전환합니다.

---

## Phase 1 — 정적 분석

[스크립트와 사전 점검](#스크립트와-사전-점검) 및 [도구와 출력 제약](#도구와-출력-제약)을 준수합니다.

```bash
python3 recon/harvest_static.py <BASE_URL> <OUTDIR>
```

harvest: HTML script 분석 → webpack/Vite manifest → 모든 lazy chunk 다운로드 → `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt` 생성.

```bash
wc -l OUTDIR/api_static.txt OUTDIR/routes.txt
ls OUTDIR/js | wc -l
```

- chunk 수와 manifest 비교: 404이면 harvest를 수정하고 재시도. chunk마다 수동 curl하지 않음
- `api_static.txt` 항목이 너무 적으면 OUTDIR의 endpoint 정규식 범위를 넓힌 뒤 재실행(reference 참조)

### Phase 1b — 인자 역추적

path는 Phase 1에서 가져오며 인자 필드는 별도로 조사합니다. grep 규칙은 [도구와 출력 제약](#도구와-출력-제약) 참조.

**완료 기준**: 주요 인터페이스의 필드명, 전송 위치, 추정 타입, 필수 여부, 표본 값, 신뢰도를 설명할 수 있어야 합니다.

#### 1b.0 — 전송 형식

| 형식 | 인자 위치 | 정적 분석 시 우선 확인 |
|---|---|---|
| REST JSON | body + query | path 기준점 옆 `(params\|data\|body)\s*:\s*\{` |
| GraphQL | `variables` | gql 템플릿, `$page: Int` |
| 일반 form | urlencoded | `<form>`, `FormData` |
| 파일 업로드 | multipart | `FormData.append` |
| 경로 인자 | `/user/:id` | 경로 테이블 + `useParams` / `$route.params` |
| 암호화/서명 | `sign`/`data` 내부 | 암호화 함수의 입력 인자 Hook(reference D절) |

산출물: 각 인터페이스에 `transport: query|json|form|graphql|encrypted` 표시.

#### 1b.1 — 기준점 주변 코드 확장

알려진 path를 기준으로 주변 코드를 확장하여 요청 구성 객체를 찾습니다.

```bash
grep -n '"/api/user/list"' OUTDIR/js/*.js | head -20
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' OUTDIR/js/*.js | head -20
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' OUTDIR/js/*.js | head -20
```

| 래퍼 계층 | 인자 단서 |
|---|---|
| axios 인스턴스 | `data` / `params` |
| 공통 request | 인터셉터가 주입하는 전역 필드 |
| OpenAPI 클라이언트 | 생성된 method 시그니처 |
| React Query / SWR | hook의 두 번째 인자 |
| Vue composable | composable 입력 인자 |

남아 있는 타입 정보: `yup`/`zod`/rules, `Form.Item name=`, 내장 Swagger.

→ `param_candidates.json`: `{ path, fields[], source: "static-callsite", confidence }`

#### 1b.2 — 바인딩 계층

```
Form field → onFinish/handleSubmit → transform → API payload
```

| 바인딩 소스 | 조사 방법 |
|---|---|
| 폼 submit | submit → transform → API 추적 |
| 테이블 검색 | `getFieldsValue()` → `params` |
| 경로 | `:id` / `?tab=` |
| 인터셉터 | 전역 `tenantId`, 페이지 나누기, sign |
| 열거형 select | `options` → API 열거 값 |

DevTools call stack에서 `fetch`/`XHR.send`의 호출자를 따라 요청 구성 함수를 찾습니다.

#### 1b.3 — 요청 구성의 세 가지 질문(Phase 2 인증 조건과 별개)

| 질문 | 확인할 내용 |
|---|---|
| **구성** | payload를 build하는 위치, transform 흔적 |
| **검증** | required, pattern, enum |
| **전송** | path / query / body / multipart / 헤더 |

인터셉터 조건(Phase 2)을 확인하면서 전역 주입 필드(Authorization, `X-Tenant-Id`, sign)도 읽습니다.

#### 1b.4 — Phase 3과 연결

후보 필드는 정적/바인딩 계층에서 찾습니다. **필수/선택/조건부 의존성**은 Phase 3 인자 행렬 + diff + Phase 5 오류 역추적으로 확인해야 합니다.

---

## Phase 2 — 인증의 세 가지 조건

`OUTDIR/js/`에서 grep(`head` 포함)한 뒤 `config.json`에 기록합니다(예시는 reference 참조).

| 조건 | 질문 | 키워드 |
|---|---|---|
| **렌더링 조건** | 로그인 여부를 어떻게 판단하는가? | `isLogin`, `getToken`, Cookie/localStorage |
| **인터셉터 조건** | 무엇이 `/login` 이동을 유발하는가? | `response_code`, `errno`, axios interceptor |
| **콘텐츠 조건** | 메뉴/권한은 어디서 가져오는가? | `menu`, `permission`, `role`, `acl`, `routes` |

localStorage 키 이름을 자격 증명으로 간주하지 마세요. chunk/요청 흐름에서 확인해야 합니다.

**완료 후 점검 B**: 결론을 `config.json`에 기록하고 `OUTDIR/runtime_harvest.js` / `preload.js`를 수정합니다.

### Phase 2b — API 관찰(선택)

OUTDIR의 `preload.js`로 세션 키 이름, Authorization, 중첩 API URL을 확인합니다.

| 설정 | 산출물 |
|---|---|
| `recordDetail: true` | `__API_RECON_DETAIL__` |
| `observe.xhrHeaders: true` | headers 관찰 |
| `extractUrlsFromResponse: true` | 응답 안의 하위 API |
| `observe.storageReads/cookieReads: true` | config에 반영 |
| `neutralizeVueRouter: true` | `__API_RECON_ROUTES__` |

coverage의 각 회차에서 `__API_RECON_LOG__`, `__API_RECON_DETAIL__`, `__API_RECON_ROUTES__`, `__API_RECON_OBSERVE__`를 내보냅니다.

---

## Phase 3 — 런타임

점검 B를 완료해야 합니다. [범위와 금지 사항](#범위와-금지-사항에이전트-필독) 및 자격 증명 없는 mock 전략을 준수합니다.

`config.json`에서 `"runtimeMode": "depth" | "coverage" | "both"`를 설정합니다(템플릿은 reference 참조).

### Hook과 stub(depth + coverage 공통)

| 계층 | 범위 | 목적 |
|---|---|---|
| L1 정확한 매칭 | auth/권한/bootstrap stub | 첫 화면 인증 조건 통과 |
| L2 실패 응답 보정 | 모든 JSON 응답 | 로그인되지 않음 코드 → 성공 |
| L3 보완 | L1에 매칭되지 않은 `/api` 등 | 빈 성공 본문으로 UI 렌더링 |

- **depth**: fake auth + `forward`로 업무 코드 수정 + `stubs`. `routes` 순회(hash/history). `runtime_api.json` 생성
- **coverage**: **document-start**에 `preload.js` 주입(CDP `addScriptToEvaluateOnNewDocument` 또는 Userscript)

검증: `window.__API_RECON_PRELOAD__`가 존재하고 업무 path가 `/login`으로 돌아가지 않아야 합니다.

```bash
cd recon
# ARTEX Docker는 Playwright와 Chromium이 포함되어 있어 설치 없이 실행합니다.
node runtime_harvest.js config.json
```

로컬 실행 환경에 Playwright가 없다면 복사한 `package.json`/`package-lock.json`으로 `npm ci` 후 `npx playwright install chromium`을 한 번 실행합니다. Chromium 경로는 자동 탐색하며 필요할 때만 `config.chromium`/`CHROMIUM`으로 지정합니다. 기록 프록시 CA는 ARTEX 런타임의 브라우저별 신뢰 저장소를 재사용합니다. 대상 자체 서명 인증서를 허용해야 하는 실습에서만 `ignoreHTTPSErrors: true`를 명시하세요.

`runtime_api.json`의 `evidenceKind`와 각 요청의 `responseSource`는 실제 수신 응답을 보정했는지, stub인지 구분합니다. `forwardError`가 있으면 해당 응답은 서버 검증 결과가 아닙니다. WebSocket 프레임은 CDP로 수집하고 SSE는 끝없는 본문을 기다리지 않고 경로를 기록합니다.

### 3b — coverage 동적 열거(필수)

1. 주 탐색 메뉴/사이드바 — 모든 항목을 클릭하고 네트워크 응답을 1–3초 대기
2. Tab — `role=tab`, `.ant-tabs-tab`
3. 테이블 — 첫 행의 보기/편집/상세 정보
4. 도구 모음 — 내보내기, 필터, 새로 만들기(**되돌릴 수 없는 삭제 금지**)
5. 모듈에 진입할 때마다 API/경로 병합
6. SPA — `routes.txt`에서 아직 확인하지 않은 path에 제한적으로 `pushState` 사용(MPA에서는 금지)

**인자 실행 행렬**(필수): 각 모듈에서 작업 유형별로 한 번씩 기록하고 **여러 표본을 diff**합니다.

| 작업 | 보통 추가되는 인자 |
|---|---|
| 목록 첫 화면 | 페이지 나누기 + 기본 필터 |
| 검색 클릭 | keyword, filter |
| 고급 필터 | 추가 optional 필드 |
| 새로 만들기/편집 | 전체 entity |
| 일괄 작업/내보내기/정렬 | `ids[]`, `exportType`, `sortField` |

**stub을 사용해도 outbound body/headers는 실제 요청입니다.** 요청을 기준으로 판단합니다. 기록 → `scan_raw.json`, `param_samples.json`, `api_detail.json`.

- **Vue**: `neutralizeVueRouter: true` + document-start preload
- **React**: `routes.txt` + 사이드바 클릭 + `pushState`
- **both**: 먼저 3a depth, 이후 3b coverage

---

## Phase 4 — 권한 트리 복원

**실행 조건**: 모듈 페이지가 비어 있거나 모든 경로에서 bootstrap(locale 등)만 발생하면 콘텐츠 조건을 통과하지 못한 것입니다.

| 현상 | 의미 |
|---|---|
| 화면 구조 진입 성공 | 렌더링 조건 + 인터셉터 조건 통과 |
| 사이드바 항목 누락/클릭 후 빈 화면 | stub 구조 또는 권한 코드 부족 |
| 경로별 API가 동일하고 매우 적음 | `v-if permission` 조건 미통과 |
| `routes.txt`가 bundle보다 훨씬 적음 | auth 모듈에서 보완 필요 |

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl)[^"]*"' OUTDIR/js/*.js | sort -u | head -30
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|menuList|authList' OUTDIR/js/*.js | head -20
```

일반적인 흐름: `role_permissions`(flat codes) + `permissions/all`(tree) → `getResultTree` → `userRouteAuth[CODE].url`.

```bash
python3 recon/extract_route_map.py recon/js recon/
python3 recon/build_perm_tree.py recon/js recon/ --config recon/config.json
```

중간 산출물: `route_map.json`, `userRouteAuth.json`, `permissions_tree.json`, `*_stub.json`, `perm_codes_all.txt`.

stub 확인: 바깥쪽 `response_code`가 인터셉터 조건과 일치해야 하며 flat codes와 tree가 대응해야 합니다. `routes`가 `route_map`의 모든 link를 포함해야 합니다.

`config.json`을 갱신한 뒤 **Phase 3을 재실행**합니다. 대형 SPA는 `waitUntil`, `routeTimeout`, `perRouteMs` 조정 가능(reference A3/I절 참조).

---

## Phase 5 — 병합과 보고서

### 산출물 표

| 파일 | 단계 | 내용 |
|---|---|---|
| `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt` | 1 | 정적 bundle과 path |
| `param_candidates.json` | 1b | 정적 인자 필드 후보 |
| `config.json` | 2 | 세 가지 조건 + runtime 설정 |
| `runtime_api.json` | 3a | depth 상세 기록(WS/SSE 포함) |
| `param_samples.json`, `scan_raw.json`, `api_detail.json` | 3b | 여러 표본, 클릭 로그, detail |
| `route_map.json` 등 | 4 | 권한 트리 중간 파일(실행한 경우) |
| `params_merged.json` | 5 | 병합된 인자 필드 + 신뢰도 |
| `api_merged.txt` | 5 | `METHOD /path [params] [static\|runtime\|both]` |
| `site_map.json` | 5 | 경로, API, params, 기능 지점, 한계 |
| **insert_assets** | 5 | 모든 서비스·엔드포인트 자산을 자산 저장소에 기록 |

### 5b — 인자 병합

`param_samples.json`을 diff합니다. **범용 병합 스크립트는 없습니다.** 신뢰도 규칙은 reference J7 참조(높음/중간/낮음/실행 대기).

### 5c — 오류 역추적

승인된 범위에서 불완전한 요청의 400 응답을 읽을 수 있습니다(**인자 조사이며 취약점 테스트가 아님**). `field 'x' is required`, 열거 값 오류 등이 대상입니다. `data` 래퍼, `variables`, 암호화 전 `bizData`에 주의합니다.

보고서에 runtimeMode, 정적/런타임 API 수, 인자 신뢰도, 미확인 모듈, 참고 스크립트 대비 `CHANGES.md` 요약을 명시해야 합니다.

권장 `site_map.json` 구조:

```json
{
  "site": "https://example.com",
  "runtimeMode": "both",
  "appType": "vue-spa",
  "routeGuardStrategy": ["nav-neutralize", "L1-auth", "L2-patch", "forward"],
  "apisFromStatic": [],
  "apisFromRuntime": [],
  "apis": [],
  "params": [{ "method": "POST", "path": "/api/user/list", "transport": "json", "fields": [] }],
  "frontendRoutes": [],
  "routesVerifiedByClick": [],
  "featuresTriggered": [],
  "limitations": ""
}
```

추가 필드와 grep 예시는 [reference.md](reference.md) 참조.

---

## 공통 안내

- **프레임워크 독립적**: webpack/Vite/Angular의 lazy load 방법은 동일
- **전송**: REST/JSON, GraphQL, WebSocket, SSE. gRPC-web은 범위 밖
- **SSR**: 클라이언트 fetch는 기록 가능. RSC/Server Actions는 완전한 열거 불가
- **사각지대**: JSVMP, WASM, HMAC/mTLS의 강한 검증 → 정적 분석 + 한계 명시
- **인자 사각지대**: 조건부 연동, hidden params, WASM의 요청 구성 → 실행 대기/도달 불가로 표시
- **정적 분석의 보완 역할**: runtime이 차단되어도 정적으로 endpoint 열거 가능

---

## 추가 자료

- grep 예시, `config.json` 템플릿, 문제 해결, Hook, J절 인자 역추적, site_map 템플릿: **[reference.md](reference.md)**
- 참고 스크립트 경로는 [스크립트와 사전 점검](#스크립트와-사전-점검) 표 참조
