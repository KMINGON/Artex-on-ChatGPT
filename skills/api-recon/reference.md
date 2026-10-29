# api-recon — 참고 안내서

grep 예시, `config.json` 템플릿, 문제 해결 안내입니다. 모든 grep은 `js/` 디렉터리에서 실행합니다. bundle이 한 줄이면 먼저 `js-beautify` 또는 `sed 's/}/}\n/g'`를 사용할 수 있지만, 보통 주변 코드 범위를 포함한 원문 grep으로 충분합니다.

## 스크립트 안내

`scripts/`의 모든 파일은 **참고 템플릿**입니다. 실행 전에 대상 사이트에 맞게 수정해야 합니다. 주요 수정 항목:

| 스크립트 | 일반적인 수정 항목 |
|---|---|
| `harvest_static.py` | endpoint 정규식, webpack/Vite manifest 분석, 마이크로 프런트엔드 publicPath, 재시도/동시 실행 |
| `runtime_harvest.js` | neutralize 필드명과 성공 값, stub 매칭 규칙과 body 구조, routes 소스, WS 기록, `waitUntil`/`routeTimeout`/`proxy` |
| `preload.js` | `loginPathRe`, L1 stubs, `neutralize.fields`, `apiPattern`, L3 활성화 여부, `recordDetail`, `observe.*`, `neutralizeVueRouter` |
| `spider_mpa.py` | 파괴적 링크의 `--exclude`, cookie, depth/max, 동일 도메인 필터 |
| `extract_route_map.py` | `routeMap` / `routeLink` 정규식, KEY 이름 패턴 |
| `build_perm_tree.py` | `userRouteAuth` 분석, `ROOTS`/`PREFIX_PARENT` 계층 추정, stub 바깥쪽 필드명 |
| `config.json` | 위의 모든 사이트별 인자를 모으는 설정 |

수정한 파일은 작업 디렉터리(예: `recon/`)에 두고, 보고서에는 참고 스크립트와 달라진 내용을 구체적으로 기록하는 것을 권장합니다.

---

## A. 인증의 세 가지 조건 역추적

### A1. 렌더링 조건 — 로그인 여부를 어떻게 판단하는가?

```bash
grep -rhoaE '.{0,40}(isLogin|isAuthenticated|loggedIn|hasLogin|requireAuth)\b.{0,80}' js | head
grep -rhoaE 'function (getUser|getToken|getAuth)[0-9]?\([^)]*\)\{.{0,200}' js | head
grep -rhoaE '(localStorage|sessionStorage)\.getItem\("[^"]+"\)' js | sort -u
grep -rhoaE '(Cookies?|cookie)\.(get|load)\("[^"]+"\)' js | sort -u
grep -rhoaE '\batob\(|JSON\.parse\(|jwt|decode' js | head
```

`isLogin = f(getUser())` → `getUser = decode(storage.read(KEY))` 흐름을 찾아 **저장 키**, **저장소**(Cookie 또는 localStorage), **인코딩**을 확인합니다.

| 인코딩 | config에서 모의 값 구성 |
|---|---|
| 평문 문자열 / `"1"` / token | `"value": "anything-truthy"` |
| `JSON.parse(x)` | `"value": "json:{\"id\":1,\"username\":\"admin\"}"` |
| `JSON.parse(atob(x))` | `"value": "b64json:{\"id\":1,\"username\":\"admin\"}"` |
| JWT | 서명 없는/`alg:none` JWT 또는 bundle 안의 키로 서명 |
| 암호화(SM2/AES/RSA) | 하드코딩된 키 확인. 렌더링 조건에 디코딩 가능한 blob만 필요하면 모의 값 생성 가능. 그 외에는 정적 분석으로 보완 |

→ `cookies` / `localStorage`에 기록합니다.

### A2. 인터셉터 조건 — 무엇이 /login 이동을 유발하는가?

```bash
grep -rhoaE '.{0,60}(interceptors\.response|axios|request\.use).{0,120}' js | head
grep -rhoaE '.{0,40}(response_code|errcode|errno|\bcode\b|\bret\b|\bstatus\b)\s*[=!]==?\s*[\-0-9]{1,4}.{0,60}' js | head -20
grep -rhoaE '.{0,40}(未登录|请重新登录|登录已过期|unauthorized|登录失效|授权|token.{0,10}invalid).{0,40}' js | head
grep -rhoaE '.{0,30}(location\.href|router\.(push|replace)|navigate)\([^)]*login[^)]*\)' js | head
```

**필드명**, **성공 값**(보통 `0` 또는 `200`), **이동을 유발하는 실패 값**을 확인합니다. junk session으로 검증:

```bash
curl -sk -X POST -H 'Cookie: <fakekey>=junk' https://target/api/<protected> -d '{}' -H 'Content-Type: application/json'
```

→ `neutralize.fields` + `neutralize.success`에 기록합니다.

### A3. 콘텐츠 조건 — 메뉴/권한은 어디서 가져오는가?

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl|resource|nav)[^"]*"' js | sort -u
grep -rhoaE '.{0,30}(menus|permissions|menuList|routeList|authList|role_permissions)\b.{0,120}' js | head
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|hasPermission|checkAuth' js | head
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head
```

**두 계층의 데이터**(일반적인 기업용 관리 화면):

| API | 일반적인 payload | 사용하는 곳 |
|---|---|---|
| `.../role_permissions` | `{ permissions: string[], role_type }` | 프런트엔드 라우트 가드, 버튼 수준 ACL |
| `.../permissions/all` | `tree[{ code, position, children }]` | 사이드바 메뉴 렌더링 |
| bundle 안의 `userRouteAuth` | `{ CODE: { url, name? } }` | code → 프런트엔드 라우트 |
| bundle 안의 `routeMap` | `{ KEY: { name, link } }` | 별칭 해석(webpack `o.DASHBOARD`) |

사용하는 쪽의 코드를 읽어 `getResultTree(tree, permissions)`의 필터 방식과 `v-if` / `hasAuth(code)`가 확인하는 필드를 파악합니다.

**수동 모의 값 구성**(소규모 사이트): 접근을 허용하는 payload 구성 → `stubs`.

**전체 권한 트리 복원**(대형 사이트에서 사이드바/하위 모듈이 여전히 비어 있는 경우): **I절** 참조.

---

## B. config.json 템플릿

```json
{
  "baseUrl": "https://target/",
  "runtimeMode": "both",
  "chromium": "",
  "ignoreHTTPSErrors": false,

  "cookies": [
    { "name": "auth", "value": "b64json:{\"id\":1,\"username\":\"admin\",\"role\":\"admin\",\"func\":{},\"permissions\":[\"*\"]}" }
  ],
  "localStorage": { "token": "faketoken", "isLogin": "1" },

  "neutralize": {
    "fields": ["response_code", "code", "errno", "ret", "status"],
    "success": 0,
    "flags": { "success": true, "message": "ok" }
  },
  "forward": true,
  "loginUrlPattern": "/login",
  "apiPattern": "/api/|/rest/|/graphql",

  "mockTier": "L1+L2",
  "recordDetail": true,
  "observe": {
    "storageReads": false,
    "cookieReads": false,
    "xhrHeaders": true
  },
  "neutralizeVueRouter": true,
  "stubs": [
    {
      "match": "permissions/all|/menu|role_permissions",
      "body": {
        "response_code": 0, "code": 0,
        "data": {
          "permissions": ["*"],
          "menus": [
            { "name": "dashboard", "path": "/dashboard", "show": true, "children": [] },
            { "name": "alert", "path": "/alert", "show": true, "children": [] }
          ]
        }
      }
    }
  ],

  "explore": {
    "clickTabs": true,
    "clickTables": true,
    "pushStateFallback": true,
    "maxMenuItems": 50
  },

  "routes": ["/dashboard", "/alert", "/asset", "/device", "/report", "/config", "/system"],
  "waitMs": 1500, "perRouteMs": 900, "headless": true,
  "waitUntil": "domcontentloaded",
  "routeTimeout": 12000,
  "proxy": "",

  "captureResponses": true, "recordWs": true, "respMax": 600
}
```

필드 설명:
- `runtimeMode`: `depth`(Playwright), `coverage`(browser MCP), `both`
- `cookies[].value` 접두사: `b64json:` → base64(JSON), `json:` → 원시 JSON, 접두사 없음 → 리터럴
- `forward: true`는 stub으로 처리하지 않은 API 요청을 전달한 뒤 응답 본문의 업무 코드 필드를 보정합니다. depth의 `false`는 `apiPattern`에 맞는 요청을 stub 처리하며, HTML/JS와 패턴 밖 요청은 전송될 수 있습니다. coverage의 preload에서는 mock에 일치하는 fetch/XHR에만 `forward: false`가 적용됩니다. 네트워크 격리 설정은 아닙니다.
- `mockTier`: coverage 모드 preload에서 활성화할 계층. 예: `L1+L2`, `L1+L2+L3`
- `routes`는 `routes.txt`의 프런트엔드 라우트에서 가져옴. 모의 메뉴 생성 후 harness가 `<a href>`를 자동 추가
- `captureResponses` / `recordWs`는 depth 모드에서만 유효
- `waitUntil`: 대형 SPA는 상시 통신으로 대기가 끝나지 않을 수 있으므로 `domcontentloaded`를 사용합니다. 이전 Puppeteer 명칭인 `networkidle2`/`networkidle0`도 호환 별칭으로 받지만 Playwright의 `networkidle`로 변환됩니다.
- `routeTimeout`: 각 프런트엔드 라우트의 `page.goto` 제한 시간(ms)
- `proxy`: Playwright launch의 `proxy` 설정으로 전달합니다. 생략하면 `HTTP_PROXY` / `HTTPS_PROXY`를 사용합니다.

### B1. 이중 stub 템플릿(role_permissions + permissions/all)

```json
"stubs": [
  {
    "match": "role_permissions",
    "body": {
      "response_code": 0,
      "data": {
        "permissions": ["MONITOR", "MONITOR_ALERT", "THREAT", "ASSETS_RISK"],
        "role_type": "SUPER_ADMIN"
      }
    }
  },
  {
    "match": "permissions/all",
    "body": {
      "response_code": 0,
      "data": [
        {
          "code": "MONITOR",
          "position": 1,
          "children": [
            { "code": "MONITOR_ALERT", "position": 1, "children": [] }
          ]
        }
      ]
    }
  }
]
```

바깥쪽 필드명(`response_code` / `code` / `data`)은 A2 인터셉터 조건과 일치해야 합니다. `permissions`는 tree의 모든 leaf code를 포함해야 합니다.

---

## C. coverage 모드: preload 설정

`scripts/preload.js` 상단의 `CONFIG` 객체를 편집하거나 CDP로 주입하기 전에 바꿉니다.

```javascript
const CONFIG = {
  loginPathRe: /\/(login|signin)(\/|$|\?)/i,
  mockTier: 'L1+L2',
  forward: true,
  recordDetail: true,
  extractUrlsFromResponse: true,
  neutralizeVueRouter: true,
  observe: { storageReads: false, cookieReads: false, xhrHeaders: true },
  neutralize: { fields: ['response_code', 'code'], success: 0 },
  stubs: [ /* config.json의 stubs와 동일 */ ],
  apiPattern: /\/(api|apis|v\d+|dev|internal|graphql)\//i,
};
```

검증: `window.__API_RECON_PRELOAD__ === true`이고 pathname이 안정적으로 유지되어야 합니다.

기록 결과 내보내기:

```javascript
JSON.stringify({
  apis: [...window.__API_RECON_LOG__],
  detail: window.__API_RECON_DETAIL__,
  routes: [...(window.__API_RECON_ROUTES__ || [])],
  observe: window.__API_RECON_OBSERVE__,
}, null, 2)
```

---

## D. preload / runtime Hook 기능

preload(coverage)와 runtime_harvest(depth)에 내장된 브라우저 Hook 기능 및 적용 범위:

| Hook 기능 | API 발견에 도움이 되는 점 | 적용 범위 |
|---|---|---|
| Hook fetch / XHR.open | 요청 URL/메서드 기록 | ✅ `recordDetail` + `__API_RECON_LOG__` |
| Hook XHR.setRequestHeader | Authorization 등의 헤더 발견 | ✅ `observe.xhrHeaders` |
| localStorage/cookie 읽기 Hook | 세션 키 이름 확인 | ⚠️ 선택 사항 `observe.storageReads/cookieReads` |
| Vue 라우트 조회 | frontendRoutes 보완 | ✅ `__API_RECON_ROUTES__`(로드된 라우트) |
| Vue 라우트 가드 무력화 / 로그인 이동 차단 | 모듈을 렌더링하여 API 실행 | ✅ `neutralizeVueRouter` + 기본 페이지 이동 무력화 |
| React 라우트 조회 | 프런트엔드 라우트 보완 | ⚠️ 정적 분석 + 클릭. 전용 Hook 없음 |
| 페이지 이동 차단(로그인 path) | 페이지를 유지하며 분석 | ⚠️ 업무 탐색을 막지 않도록 로그인 path만 차단 |
| 암호화 라이브러리 Hook(CryptoJS/SM 등) | 암호화 인자 → 평문 API body | ❌ 암호화 함수 입력을 수동 Hook해야 함. 결론은 config에 기록 |
| 디버깅 방지 우회 | 없으면 runtime에서 API 기록 불가 | ❌ 수동 처리 필요. 정적 분석은 계속 가능 |

---

## E. Endpoint 추출 정규식(정적 결과가 적을 때)

`harvest_static.py`의 `extract_endpoints` 범위를 넓히거나 수동 실행:

```bash
grep -rhoaE '"/[a-z][A-Za-z0-9_/\-]{3,}"' js | sort -u
grep -rhoaE '/api/[a-zA-Z0-9_./-]+' js | sort -u
```

---

## F. 문제 해결

| 현상 | 원인 → 조치 |
|---|---|
| 정적 API가 매우 적음 | endpoint 형식 불일치 → 정규식 범위 확대(E절) |
| chunk 수 ≪ manifest | CSS 전용 또는 배포되지 않은 chunk. 404는 재시도됨 |
| runtime에서 여전히 로그인 페이지 표시 | 렌더링 조건 오류 → A1 재확인: 키 이름, 저장소, 인코딩, domain |
| 화면 구조는 진입했지만 모듈이 비어 있음 | 콘텐츠 조건 → 모의 메뉴 구성(A3). `routes` path가 잘못되었을 수 있음 |
| 라우트마다 bootstrap/locale만 발생 | 권한 코드 부족 → I절 권한 트리 복원. `role_permissions` + `permissions/all` 이중 stub 확인 |
| 사이드바 항목은 있으나 하위 페이지가 비어 있음 | tree의 중간 노드 누락 또는 code가 `userRouteAuth`와 불일치 |
| 모든 API가 로그인으로 이동 | 인터셉터 조건 → `neutralize` 확인. 중첩 필드는 순회 로직 확장 필요 |
| WS 프레임이 0 | 사용자 상호작용 후에만 subscribe될 수 있음. `perRouteMs` 증가 |
| 응답 본문이 비어 있음 | `forward: true`일 때만 실제 응답 존재 |
| Chromium 없음 | ARTEX Docker의 내장 런타임 사용. 로컬은 `npm ci` + `npx playwright install chromium`; 필요 시 `config.chromium` / `CHROMIUM` 설정 |
| Mock이 많은데도 로그인으로 돌아감 | Hook이 늦거나 `location.href` setter 누락 → document-start + preload |
| 목록이 모두 비어 있음 | L3의 빈 배열은 정상. 탭/설정/상세 정보를 계속 클릭 |
| Redux action을 경로로 오인 | get/set/change/clear/toggle/upload를 포함한 내부 path 필터링 |
| Vue가 여전히 로그인으로 이동 | preload가 document-start가 아님 → 주입 시점 변경. 또는 `neutralizeVueRouter: false`일 때 가드를 수동 제거 |
| 응답의 URL이 log에 없음 | `extractUrlsFromResponse` 활성화 또는 `__API_RECON_DETAIL__`에서 수동 추출 |
| Authorization 헤더 이름을 모름 | `observe.xhrHeaders` 활성화 또는 DevTools에서 요청 헤더 확인 |
| runtime이 매우 느리거나 시간 초과 | `waitUntil: domcontentloaded`로 변경. `routeTimeout` 감소. `networkidle` 대기를 피하고 요소/앱 상태로 준비 여부 확인 |
| 프록시 연결 실패 | `proxy` / 환경 변수 확인. Playwright launch에 적용한 proxy와 curl의 프록시 주소·포트 일치 여부 확인 |

---

## G. 보호가 강화된 대상

서버가 단계별로 세션을 검증하면(모의 생성 불가한 서명 cookie, 서버 렌더링되어 stub 처리 불가한 메뉴) runtime이 화면 구조에서 막힐 수 있습니다. 예상되는 동작:

- **정적 분석만으로 endpoint 열거 가능** — 모듈 path가 코드 안에 있음
- 이 스킬의 자격 증명 없는 조사로 진행할 수 없으면 정적 후보와 한계를 기록합니다. 승인된 실제 세션 검증은 일반 브라우저 흐름에서 별도로 수행하세요. 이 harness는 `forward: true`여도 수신 응답을 보정하며, `neutralize`를 생략해도 기본 보정값이 적용됩니다. mock·보정 응답을 실제 서버 검증 결과로 사용하지 마세요.

---

## H. 작업별 확인 목록

1. 승인 범위 확인
2. `scripts/harvest_static.py` **읽기** → 대상에 맞게 수정 → 실행 → `api_static.txt`, `routes.txt` 검토
3. **Phase 1b**: path 기준점 주변 코드 확장 + 바인딩 계층 → `param_candidates.json`(J절)
4. A1/A2/A3 역추적 → 사이트별 `config.json` 작성
5. `runtime_harvest.js` / `preload.js`를 **읽고 수정한 뒤** 실행
6. `runtimeMode=depth`: ARTEX Docker에서는 포함된 Playwright/Chromium으로 수정한 OUTDIR 스크립트를 실행합니다. Playwright가 없는 로컬 환경에서는 OUTDIR에 복사한 `package.json`/`package-lock.json`으로 `npm ci` 후 `npx playwright install chromium`을 실행합니다.
7. `runtimeMode=coverage/both`: document-start에 수정한 preload 주입 → browser MCP 동적 열거 + **UI 조작별 요청 파라미터 수집·비교표**
8. 모듈이 렌더링되지 않음 → **I절 권한 트리 복원** → stubs 수정 → 재실행
9. 여러 인자 표본 diff + 오류 역추적 → `params_merged.json`
10. 병합 → `site_map.json` + `api_merged.txt`. 확인한 범위, 누락된 부분, 스크립트 수정 사항을 정확히 명시

---

## I. 권한 트리 복원(Phase 4 심화)

단순한 `menus: [{ path, show: true }]` 모의 구성이 효과가 없고 하위 모듈이 여전히 mount되지 않을 때 사용합니다.

### I1. auth 모듈 찾기

```bash
grep -l 'userRouteAuth' js/*.js
grep -l 'routeMap\|routeLink' js/*.js
grep -rhoaE 'getResultTree|role_permissions|permissions/all' js | head
```

**권한 API path**, **응답 필드명**, **사용하는 chunk 파일명**을 기록합니다.

### I2. routeMap 추출

```bash
python3 recon/extract_route_map.py recon/js recon/
# recon/route_map.json 생성
```

`[!] no routeMap pattern found`이면 `extract_route_map.py`의 정규식 범위를 넓히거나 수동 grep:

```bash
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head -20
```

### I3. 권한 트리 + stub 구성

```bash
python3 recon/build_perm_tree.py recon/js recon/ --config recon/config.json
```

스크립트 동작:
1. `userRouteAuth={MONITOR:{url:...},...}` 분석(webpack 별칭 `He=o.DASHBOARD` 포함)
2. `route_map.json`으로 alias → 실제 path 해석
3. code 접두사로 parent 추정(`MONITOR_ALERT` → `MONITOR`)
4. `permissions_tree.json`, `permissions_all_stub.json`, `role_permissions_stub.json` 출력
5. `--config` 지정 시 `config.json`의 `stubs`와 확장된 `routes`를 자동 기록

**대상에 맞게 수정**(스크립트 상단):
- `DEFAULT_ROOTS`: 최상위 모듈 code 목록
- `DEFAULT_PREFIX_PARENT`: `PREFIX_` → parent 매핑
- `DEFAULT_EXTRA_PARENT`: 접두사 관계가 없는 orphan 노드

### I4. stub 일관성 확인

```bash
# permissions 수는 userRouteAuth 항목 수와 비슷해야 함
wc -l recon/perm_codes_all.txt
# routes는 route_map의 모든 link를 포함해야 함
python3 -c "import json; m=json.load(open('recon/route_map.json')); r=set(json.load(open('recon/config.json'))['routes']); print('missing', [v['link'] for v in m.values() if v['link'] not in r])"
```

### I5. runtime 재실행 및 비교

```bash
node recon/runtime_harvest.js recon/config.json
# 모의 구성 전후 runtime_api.json 항목 수 비교. /attack, /asset 등에 모듈 API가 나타나는지 확인
```

| 모의 구성 전 | 모의 구성 후(성공) |
|---|---|
| 모든 라우트에서 동일한 bootstrap 3–5개 | 라우트별로 서로 다른 모듈 API 실행 |
| `/api/locale/language`만 존재 | `/api/web/...` 모듈 endpoint 등장 |
| `routes.txt`에 한 자릿수 라우트 | route_map에서 가져온 `routes`가 80–110개 이상 |

### I6. 여전히 실패하는 경우

- **coverage 모드**: 사이드바 + 탭 클릭. 상호작용 이후에 권한 조건 요청이 발생할 수 있음
- **stub 필드**: bundle의 응답 사용 코드나 이미 확보한 승인된 응답 자료와 stub의 중첩 구조를 비교합니다. 실제 세션 검증이 필요하면 이 스킬의 mock 조사와 구분하여 일반 브라우저 흐름에서 수행하세요.
- **추가 가드**: `hasPermission|checkRole|func.` 등의 버튼 수준 검사를 grep하고 `role_permissions.permissions` 확장
- **정적 분석으로 보완**: 모듈 API path는 `api_static.txt`에 남아 있음. runtime은 METHOD/body를 보완. 인자는 `param_candidates.json` + 기록된 표본 유지

---

## J. 인자 역추적(Phase 1b / 5b / 5c)

**조사 방법이며 범용 스크립트가 아닙니다.** path는 정규식으로, 인자는 기준점 주변 코드 확장 + UI 바인딩 추적 + 여러 표본 diff + 오류 역추적으로 찾습니다.

### J1. 기준점 주변 코드 확장 — path에서 요청 구성 객체 찾기

```bash
# Phase 1에서 확인한 path를 기준점으로 사용
grep -n '"/api/user/list"' js/*.js
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' js | head
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' js | head
grep -rhoaE '(get|post|put|delete|patch)\([^,]+,\s*\{' js | head
```

### J2. 래퍼 계층과 전송 형식

```bash
# axios / 공통 request
grep -rhoaE '(axios|request)\.(get|post|put|delete|patch)\(' js | head
grep -rhoaE 'interceptors\.(request|response)' js | head

# GraphQL
grep -rhoaE '(query|mutation)\s+\w+|gql`|graphql\(' js | head
grep -rhoaE '\$[a-zA-Z_]+\s*:\s*(Int|String|Boolean|\[)' js | head

# FormData / multipart
grep -rhoaE 'FormData|\.append\(' js | head

# 경로 인자
grep -rhoaE 'path:\s*"/[^"]*:[^"]+"' js | head
grep -rhoaE 'useParams|route\.params|\$route\.params' js | head
```

### J3. 검증 조건 — 필수 / 형식 / 열거 값

```bash
grep -rhoaE '(required|message|pattern|enum|validator)\s*:' js | head
grep -rhoaE 'yup\.|zod\.|async-validator|Form\.Item|a-form-item|el-form-item' js | head
grep -rhoaE 'rules\s*:\s*\[|name:\s*["\'][a-zA-Z_]+["\']' js | head
grep -rhoaE 'label.*value|options\s*:\s*\[' js | head
```

### J4. 바인딩 계층 — 폼 → API

```bash
grep -rhoaE 'onFinish|handleSubmit|getFieldsValue|validateFields' js | head
grep -rhoaE '(pick|omit|transform|dayjs|moment)\(' js | head
```

runtime에서 보완: DevTools → Network → 요청 → **Initiator**(call stack)에서 `fetch`/`send`의 호출자를 따라 요청 구성 함수를 찾습니다.

### J5. 암호화 인자

```bash
grep -rhoaE 'encrypt|decrypt|sign|CryptoJS|sm2|sm3|sm4|RSA|AES' js | head
```

**암호문으로 필드를 추측하지 마세요.** 암호화 함수의 **입력 인자**를 Hook하고 암호화 전에 평문 payload를 기록합니다. 결론은 `config.json` / `param_candidates.json`에 기록합니다.

### J6. UI 조작별 요청 파라미터 수집·비교표(Phase 3 필수)

각 모듈에서 작업별로 한 번씩 기록하고 요청 body/query를 diff합니다.

| 작업 | 확인할 내용 |
|---|---|
| 목록 첫 화면 | 페이지 나누기 기본값 |
| 검색 | keyword, filters |
| 고급 필터 | optional 필드 |
| 새로 만들기/편집 | 전체 entity |
| 일괄 작업/내보내기 | `ids[]`, `exportType` |
| 정렬/페이지 이동 | `sortField`, `order` |

`param_samples.json` 생성: `[{ "path", "method", "action": "search", "body", "query", "headers" }]`

### J7. 신뢰도 규칙

| 신뢰도 | 조건 |
|---|---|
| **높음** | 정적 호출 위치와 runtime 표본 2개 이상이 일치 |
| **중간** | 정적 정보만 있거나 runtime 표본이 1개뿐 |
| **낮음** | 응답/오류에서 추론했으며 재검증하지 않음 |
| **아직 유발하지 못함(미관측)** | 정적으로 알려진 필드이나 UI/권한 조건에 도달하지 못함 |

### J8. 상황별 빠른 구성

| 상황 | 순서 |
|---|---|
| REST 목록 페이지 | J1 요청 구성 객체 → J6 네 번의 diff → J3 rules |
| 새로 만들기/편집 폼 | J3 Form name → J4 submit 흐름 → runtime 제출 + 의도적으로 비워 400 확인 |
| GraphQL | J2 variables 선언 → runtime에서 operation별 variables 기록 |
| 암호화 body | J5 입력 인자 Hook → 암호화 전 필드가 실제 params |

### J9. api-recon 단계와 대응

| api-recon | 인자 조사 |
|---|---|
| Phase 1 정적 분석 | J1 기준점 주변 코드 확장 |
| Phase 2 A2 인터셉터 | 전역 주입 필드(tenantId, sign) |
| Phase 3 runtime | J6 수집·비교표 + `param_samples.json` |
| Phase 4 권한 트리 | 모듈별 폼이 다르므로 충분한 권한 조건이 있어야 모든 필드 실행 가능 |
| Phase 5 병합 | `params_merged.json` + 신뢰도. 단일 표본으로 필수 여부를 확정하지 않음 |

### J10. 문제 해결

| 현상 | 조치 |
|---|---|
| 정적 필드명이 runtime에 나타나지 않음 | “아직 유발하지 못함(미관측)”으로 표시. 권한 트리 보완 / 고급 필터 클릭 / 연동 select의 각 option 확인 |
| 같은 path에서 body 구조가 다름 | 정상. `action`별로 분리 기록하고 schema를 강제로 합치지 않음 |
| stub 응답은 가짜지만 params를 확인하고 싶음 | **outbound 요청**의 body/headers 확인. stub 응답에서 역추론하지 않음 |
| 400 오류가 nested field를 지목 | 바깥쪽 래퍼 `data`/`bizData`/`variables` 확인 |
| GraphQL에서 operation 이름만 보임 | `variables` JSON을 펼치고 정적 코드에서 `$var: Type` 검색 |

---
