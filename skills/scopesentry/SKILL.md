---
name: scopesentry
description: ScopeSentry MCP로 보안 스캔 플랫폼(프로젝트, 작업, 템플릿, 자산, 노드)을 관리합니다. 사용자가 ScopeSentry, MCP, API Key, 스캔 작업, 자산 조회를 언급할 때 사용합니다.
---

# ScopeSentry MCP 사용 안내

**배포된 ScopeSentry 인스턴스**를 사용하는 사용자를 위한 안내입니다. Cursor 또는 다른 MCP 클라이언트로 플랫폼에 연결하며 로컬 소스 코드는 필요하지 않습니다.

## 1. 준비

### 1.1 서비스 접근 확인

- 기본 웹 화면: `http://<호스트>`
- MCP 엔드포인트: `http://<호스트>/mcp`(리버스 프록시 또는 프런트엔드 프록시가 있다면 실제 `/mcp` 주소 사용)

### 1.2 API Key 생성

1. 브라우저에서 ScopeSentry 웹 화면에 로그인합니다.
2. **API Key** 관리 페이지에서 키를 생성합니다(또는 관리자가 제공한 인터페이스로 생성).
3. 반환된 `ssk_...` 문자열을 저장합니다(**한 번만 표시됨**).

### 1.3 Cursor MCP 설정

Cursor → Settings → MCP → 서버 추가:

```json
{
  "mcpServers": {
    "scopesentry": {
      "url": "http://<호스트>:8082/mcp",
      "headers": {
        "X-API-Key": "ssk_발급받은키"
      }
    }
  }
}
```

`Authorization: Bearer ssk_발급받은키`도 사용할 수 있습니다.

설정 후 MCP를 다시 시작하거나 Cursor를 다시 로드하고, 도구 목록에 `list_projects`, `list_assets` 등이 나타나는지 확인합니다.

---

## 2. 도구 목록

아래 이름은 **ScopeSentry MCP 서버의 도구**입니다. `SearchExtraTools`로 현재 노출된 스키마를 확인하고 반환된 전체 이름(`mcp__<server>__<tool>`)으로 호출하세요. `<server>`는 실제 설정값을 따르며, ARTEX의 동명 내장 도구와 구분합니다.

| 도구 | 용도 |
|---|---|
| `list_projects` | 태그로 그룹화된 프로젝트 트리(프로젝트 ID 포함) |
| `list_projects_data` | 페이지별 프로젝트 목록, 이름 검색 가능 |
| `get_project` | 프로젝트 상세 정보 |
| `create_project` | 프로젝트 생성 |
| `list_tasks` | 스캔 작업 목록 |
| `get_task` | 작업 상세 정보 |
| `list_scan_templates` | 스캔 템플릿 목록 |
| `get_scan_template` | 템플릿 상세 정보 |
| `list_plugin_modules` | 스캔 파이프라인 모듈 이름 |
| `list_plugins` | 사용 가능한 플러그인(hash, 기본 인자 포함) |
| `create_scan_template` | 스캔 템플릿 생성 |
| `create_scan_task` | 스캔 작업 생성 |
| `list_assets` | 유형별 자산 조회(페이지별 목록) |
| `count_assets` | 자산 수 집계(`/api/assets/common/total`) |
| `get_asset_detail` | 자산 또는 취약점 상세 정보 |
| `add_asset_tag` | 자산에 태그 추가 |
| `list_nodes` | 스캔 노드 목록 |

각 도구의 인자는 MCP 도구 설명(schema)을 따릅니다. `list_assets` / `count_assets`는 같은 search, filter 문법을 사용합니다. 자산 조회 전에 `list_assets` description을 확인할 수 있습니다.

전체 항목 수는 `count_assets`(웹 페이지 나누기의 전체 개수 인터페이스)로 확인합니다. 개수를 세기 위해 `list_assets`의 모든 페이지를 반복 조회할 필요는 없습니다.

---

## 3. 주요 작업 흐름

### 3.1 프로젝트별 자산 조회

사용자 또는 대화에 **프로젝트 조건이 이미 있다면** `filter.project`로 범위를 좁혀 프로젝트 간 데이터 과다 조회로 인한 응답 지연을 피합니다. 명확한 프로젝트가 없으면 필터를 강제하지 않아도 됩니다.

1. `list_projects` 또는 `list_projects_data`로 대상 프로젝트의 **ObjectID**(`id` / `children[].value`)를 조회합니다.
2. `list_assets`에 `filter.project`를 전달합니다(**표시 이름이 아닌 ID 필수**).

```json
{
  "asset_type": "asset",
  "pageIndex": 1,
  "pageSize": 20,
  "search": "domain=^example.com",
  "filter": {
    "project": ["<프로젝트ObjectID>"]
  }
}
```

### 3.2 스캔 작업 생성

1. `list_nodes`로 온라인 노드 이름을 조회합니다.
2. `list_scan_templates` 또는 `create_scan_template`로 템플릿 **ObjectID**를 확보합니다.
3. `create_scan_task`: `name`, `node`는 필수이며 `template`에는 템플릿 ID를 입력합니다(템플릿 이름 사용 불가).

**대상 소스 `targetSource`(웹과 동일):**

| targetSource | 설명 | 필수 인자 |
|---|---|---|
| `general` | 대상 직접 입력 | `target` |
| `project` | 프로젝트에서 대상 읽기 | `project`(프로젝트 ObjectID 배열) |
| `asset` | 웹 자산 저장소에서 검색 | `search`; 선택: `project`, `filter`, `targetNumber` |
| `RootDomain` | 루트 도메인 저장소에서 검색 | `search`; 선택: `project`, `filter`, `targetNumber` |
| `subdomain` | 하위 도메인 저장소에서 검색 | `search`; 선택: `project`, `filter`, `targetNumber` |
| `UrlScan` | URL 스캔 결과에서 검색 | `search`; 선택: `project`, `filter`, `targetNumber` |
| `*Source`(예: `subdomainSource`) | 자산 페이지의 선택/검색 결과에서 생성 | `targetTp=search`이면 `search`, `targetTp=select`이면 `targetIds` |

**예시 — 루트 도메인 직접 스캔:**

```json
{
  "name": "example-하위도메인수집",
  "node": ["node-1"],
  "template": "<템플릿ObjectID>",
  "targetSource": "general",
  "target": "example.com\nfoo.com",
  "project": ["<프로젝트ObjectID>"]
}
```

**예시 — 하위 도메인 저장소에서 후속 스캔(이전 작업명으로 필터):**

```json
{
  "name": "example-포트와취약점",
  "node": ["node-1"],
  "template": "<후속모듈템플릿ObjectID>",
  "targetSource": "subdomain",
  "search": "task==\"example-하위도메인수집\"",
  "project": ["<프로젝트ObjectID>"]
}
```

### 3.3 루트 도메인 전체 정보 수집(두 단계 권장)

입력이 **루트 도메인**이고 **전체 정보 수집**이 필요하면 모든 파이프라인을 한 번에 실행하지 않고 두 번의 스캔으로 나누는 것을 권장합니다.

**이유:** 분산 작업은 **대상 하나**를 단위로 배분됩니다. 루트 도메인을 할당받은 노드는 그 안에서 찾은 하위 도메인의 후속 모듈도 계속 실행하므로 부하 불균형, 속도 저하, 오류가 생기기 쉽습니다.

**권장 절차:**

1. **1단계 — 하위 도메인 수집만 수행**
   - `targetSource`: `general`
   - `target`: 모든 루트 도메인(여러 줄)
   - 템플릿: `SubdomainScan`, `SubdomainSecurity`만 활성화(하위 도메인 스캔 + 하위 도메인 탈취 검사)
   - `get_task`로 작업 완료 대기

2. **2단계 — 후속 모듈**
   - `targetSource`: `subdomain`
   - `search`: `task=="<1단계작업명>"`(작업명 정확히 일치)
   - 선택적으로 `project`를 지정하여 범위 축소
   - 템플릿: 포트 스캔, 자산 식별, 취약점 스캔 등(SubdomainScan은 제외 가능)
   - 하위 도메인이 개별 대상으로 각 노드에 배분되어 병렬 처리 효율 향상

웹의 하위 도메인 자산 페이지에서 작업명으로 필터한 뒤 하위 도메인으로 작업 만들기를 선택해도 동일합니다.

```mermaid
flowchart LR
  A[루트 도메인 목록] --> B[1단계: general + SubdomainScan]
  B --> C[하위 도메인 저장]
  C --> D[2단계: subdomain + task==1단계작업명]
  D --> E[포트/자산/취약점 등 모듈]
```

### 3.4 스캔 템플릿 생성

1. `list_plugin_modules` → 모듈 이름 목록
2. `list_plugins`(`module`로 필터 가능) → 각 플러그인의 `hash`와 기본 `parameter`
3. `create_scan_template`: `modules`에 모듈 → 플러그인 hash 배열 지정

---

## 4. 자산 조회(`list_assets` / `count_assets`)

`count_assets`와 `list_assets`는 같은 `asset_type`, `search`, `filter`를 사용합니다. `count_assets`는 `{ "total": N }`을 반환하며 웹의 `/api/assets/common/total`에 대응합니다.

```json
{
  "asset_type": "subdomain",
  "search": "task==\"작업명\"",
  "filter": {"project": ["<프로젝트ObjectID>"]}
}
```

**성능 권장 사항(`list_assets` / `count_assets` 공통):** 프로젝트 조건이 있으면 `filter.project`로 먼저 범위를 좁힙니다. `search`에서 인덱스가 있는 필드에는 가능하면 `==` 완전 일치나 `^` 접두사 매칭을 사용합니다([4.3](#43-search-검색-표현식) 참조). 넓은 범위의 `=` 부분 검색으로 응답이 느려지지 않도록 합니다. 프로젝트 정보가 없을 때는 프로젝트 필터를 강제하지 않습니다.

`filter.project`를 지원하는 유형은 [4.4](#44-filter-정확한-필터) 표를 참조합니다.

### 4.1 자산 유형 `asset_type`

`asset`, `RootDomain`, `subdomain`, `app`, `mp`, `UrlScan`, `SensitiveResult`, `DirScanResult`, `crawler`, `vulnerability`, `PageMonitoring`, `IPAsset`, `SubdomainTakerResult`

별칭 예시: `web`→asset, `vuln`→vulnerability, `ip`→IPAsset, `url`→UrlScan

### 4.2 인자 설명

| 인자 | 설명 |
|---|---|
| `pageIndex` / `pageSize` | 페이지 나누기, 기본값 1 / 20 |
| `search` | 검색 표현식(다음 절 참조) |
| `filter` | 정확한 필터 JSON(다음 절 참조) |
| `sort` | UrlScan, DirScanResult만 `length` 기준 정렬 지원 |
| `sid` | SensitiveResult 전용: 민감 정보 규칙 이름 |

`search`와 `filter`는 **동시에 사용 가능**합니다.

### 4.3 search 검색 표현식

사용자 정의 DSL이며 **SQL이 아닙니다**.

| 연산자 | 의미 | 인덱스 | 예시 |
|---|---|---|---|
| `=` | 부분 매칭(regex) | 사용 안 함 | `domain=example` |
| `==` | 정확한 매칭(완전 일치) | **사용함** | `port==443` |
| `!=` | 제외 | — | `port!="80"` |
| `&&` | AND | — | `domain==example.com && port==443` |
| `||` | OR | — | `title=admin || body=login` |

**인덱스와 연산자:** `domain`, `ip`, `port`, `title` 등의 필드에는 인덱스가 있지만, **`==` 완전 일치** 또는 **`^`로 시작하는 접두사 매칭**(예: `domain=^example.com`)에만 사용됩니다. **`=`는 regex 부분 매칭으로 변환되어 인덱스를 사용할 수 없으므로**, 데이터가 많으면 느려질 수 있습니다.

**모든 유형에서 공통으로 사용하는 search 필드:** `tag`, `task`(작업명), `rootDomain`.

**project는 search 안에 넣을 수 없습니다**(무효하거나 `&&`와 조합할 때 오류). 프로젝트 필터는 `filter.project`를 사용합니다.

**유형별 주요 search 필드:**

| asset_type | 필드 |
|---|---|
| asset | domain, ip, port, service, app, title, statuscode, icon, banner, type, body, header |
| RootDomain | domain, icp, company |
| subdomain | domain, ip, type, value |
| app | name, icp, company, category, description, url, apk |
| mp | name, icp, company, category, description, url |
| UrlScan | url, input, source, resultId, type |
| SensitiveResult | url, sname, body, info, md5 |
| DirScanResult | url, statuscode, redirect, length |
| vulnerability | url, vulname, matched, request, response, level |
| crawler | url, method, body, resultId |
| PageMonitoring | url, hash, diff, response |
| IPAsset | ip, domain, port, service, webServer, app |
| SubdomainTakerResult | domain, value, type, response |

**search 예시:**

- `domain==www.example.com && port==443`(완전 일치, 인덱스 사용)
- `domain=^example.com`(접두사 매칭, 인덱스 사용)
- `ip==192.168.1.1`
- `task=="작업명"`
- `level==high`(vulnerability)
- `statuscode==200`(DirScanResult)

부분 포함 검색이 필요할 때만 `=`를 사용합니다. 예: `title=admin`(인덱스를 사용하지 않으므로 프로젝트 등의 조건으로 범위를 좁히는 것이 좋음).

### 4.4 filter 정확한 필터

JSON 객체이며 같은 key의 여러 값은 **OR**, 서로 다른 key는 **AND**입니다.

**프로젝트 조건이 있으면 `project` 우선 사용:** 사용자나 대화에서 프로젝트가 정해졌고 asset_type이 `project`를 지원하면 범위를 줄이기 위해 지정합니다. 프로젝트 정보가 없으면 강제하지 않습니다.

| filter key | 의미 | 값 설명 |
|---|---|---|
| `project` | 소속 프로젝트 | **ObjectID**, `list_projects` / `list_projects_data`로 조회 |
| `task` | 원본 작업 | **작업명**, `list_tasks`의 `name` 사용 |
| `port` | 포트 | 예: `"443"` |
| `service` | 서비스/프로토콜 | 예: `"https"` |
| `app` | 애플리케이션 식별 정보 | 예: `"Nginx"` |
| `icon` | 아이콘 hash | |
| `statuscode` | HTTP 상태 코드 | 주로 asset에서 사용 |
| `status` | 상태 | UrlScan/DirScan HTTP 코드, 취약점/민감 정보 처리 상태 |
| `level` | 취약점 등급 | critical / high / medium / low / info |
| `type` | 유형 | 예: 하위 도메인 레코드 유형 A, CNAME |
| `color` | 민감 정보 규칙 색상 | SensitiveResult |
| `sname` | 민감 정보 규칙 이름 | SensitiveResult |
| `tags` | 태그 | |

**유형별로 사용할 수 있는 filter key:**

| asset_type | filter key |
|---|---|
| asset | project, port, service, app, icon, statuscode, type, task, tags |
| RootDomain | project, tags |
| subdomain | project, type, task, tags |
| app / mp | project, tags |
| UrlScan | status, tags |
| DirScanResult | status, tags |
| SensitiveResult | status, color, sname, tags |
| crawler | project, task, tags |
| vulnerability | project, level, status, task, tags |
| PageMonitoring / SubdomainTakerResult | tags |
| IPAsset | project, port, service, app |

**filter 예시:**

```json
{"project": ["<프로젝트ObjectID>"], "port": ["443"]}
```

**조합 검색 예시:**

```json
{
  "asset_type": "asset",
  "search": "domain=^baidu && port==443",
  "filter": {"project": ["<프로젝트ObjectID>"]},
  "pageIndex": 1,
  "pageSize": 10
}
```

**주의 사항:**

- 프로젝트 조건이 있고 지원되는 경우 `filter.project`를 우선 지정. 프로젝트 정보가 없다면 강제하지 않음
- `filter.project`에 프로젝트 표시 이름을 넣지 않음
- 알려진 값은 `==`, 접두사는 `^` 사용. 큰 테이블에 `=` 부분 매칭을 남용하지 않음
- UrlScan의 HTTP 상태에는 `filter.status` 사용. DirScanResult는 search에 `statuscode==200` 사용 가능
- SensitiveResult의 규칙 이름: `search`의 `sname=규칙명` 또는 `filter.sname` 사용

### 4.5 sort 정렬

**UrlScan**, **DirScanResult**만 지원합니다.

```json
{"length": "ascending"}
```

다른 유형은 `sort`를 무시하고 기본 시간순으로 정렬합니다.

---

## 5. 스캔 템플릿 모듈 이름

`TargetHandler`, `SubdomainScan`, `SubdomainSecurity`, `PortScanPreparation`, `PortScan`, `PortFingerprint`, `AssetMapping`, `AssetHandle`, `URLScan`, `WebCrawler`, `URLSecurity`, `DirScan`, `VulnerabilityScan`, `PassiveScan`

---

## 6. 문제 해결

| 현상 | 조치 |
|---|---|
| MCP에 도구가 없음 | URL, API Key, ScopeSentry 실행 여부 확인 |
| 401 / 403 | API Key 재생성 또는 교체 |
| 자산을 찾을 수 없음 | `filter.project`가 ObjectID인지 확인. search에 project를 넣지 않음 |
| 템플릿/작업 생성 실패 | `template`은 템플릿 ObjectID, `node`는 온라인 노드 이름이어야 함 |
| 조회가 느리거나 멈춤 | 프로젝트가 있으면 `filter.project` 추가. 인덱스 필드의 search에 `==` 또는 `^` 접두사 사용, `=` 사용 최소화. `pageSize` 축소 |

---
