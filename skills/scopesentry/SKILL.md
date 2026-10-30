---
name: scopesentry
description: ScopeSentry의 기존 자산·취약점·스캔 결과를 조회하거나 노드와 템플릿을 선택해 후속 스캔을 실행할 때 사용합니다.
mcps: ScopeSentry
---

# ScopeSentry 연결과 활용

ARTEX에 등록된 `ScopeSentry` MCP를 사용합니다. 서비스 주소와 인증이 설정되고 MCP가 활성화되어 있어야 합니다. 이 스킬을 읽으면 연결된 서버의 도구 이름이 공개됩니다. 도구 목록이 없으면 현재 세션에서 사용할 수 없는 상태로 보고하고, 이미 제공된 결과나 다른 사용 가능한 도구로 작업을 이어갑니다.

## ARTEX에서 호출하기

1. `SearchExtraTools`로 필요한 ScopeSentry 도구의 설명과 입력 스키마를 확인합니다.
2. 반환된 전체 이름(`mcp__<server>__<tool>`)과 스키마에 맞는 `params`로 `ExecuteExtraTool`을 호출합니다. 전체 이름은 검색 결과에서 가져옵니다.
3. ScopeSentry의 `list_tasks`와 ARTEX의 작업 조회 도구를 구분합니다. 두 시스템의 프로젝트·작업·자산 ID는 서로 다른 값입니다.

## 기존 결과 조회

| 필요한 정보 | 조회 흐름 |
|---|---|
| 프로젝트와 대상 | `list_projects` 또는 `list_projects_data` → `get_project` |
| 스캔 이력과 상태 | `list_tasks` → `get_task` |
| 서비스·URL·취약점 | `list_assets` → `get_asset_detail` |
| 조건에 맞는 전체 개수 | `count_assets` |

프로젝트가 정해져 있으면 `filter.project`에 **프로젝트 ObjectID 배열**을 넣습니다. 작업 결과를 찾을 때 `filter.task`에는 **작업 이름 배열**을 넣습니다. 프로젝트 ID, 작업 ID, 작업 이름을 혼용하지 않습니다.

```json
{
  "asset_type": "asset",
  "pageIndex": 1,
  "pageSize": 20,
  "filter": {
    "project": ["<프로젝트 ObjectID>"],
    "task": ["<ScopeSentry 작업 이름>"]
  }
}
```

`asset_type`은 서비스 `asset`, 하위 도메인 `subdomain`, URL `UrlScan`, 취약점 `vulnerability` 등 실제 스키마의 값을 사용합니다. `search`는 SQL이 아닌 전용 검색식입니다. `field=value`는 부분 검색, `field=="값"`은 정확한 검색이며 `&&`, `||`를 지원합니다. `project`는 `search`에 넣지 않고 `filter.project`로 전달합니다. 자산 유형마다 지원 필터가 다르므로 해당 도구 설명을 따릅니다. 필요한 후속 페이지를 조회하고 일부 페이지만 읽었다면 전체 결과로 표현하지 않습니다.

## 새 스캔과 후속 탐색

1. 현재 목표와 기존 결과를 바탕으로 추가 수집할 대상을 정합니다.
2. `list_nodes`에서 실행할 노드를, `list_scan_templates`와 `get_scan_template`에서 적합한 템플릿을 확인합니다.
3. `create_scan_task`에 노드 이름, 템플릿 **ObjectID**, 대상 소스를 전달합니다. 직접 입력은 `targetSource: general`과 `target`, 기존 프로젝트는 `targetSource: project`와 `project`를 사용합니다. 다른 소스의 필수 인자는 현재 스키마를 확인합니다.
4. 반환된 작업 식별자로 `get_task`를 조회하고 관련 자산·취약점 결과를 읽습니다. 생성 응답은 실행 완료를 뜻하지 않습니다. 대기 중에는 다른 독립적인 조사를 진행할 수 있습니다.
5. 템플릿 조정이 필요하면 `list_plugin_modules` → `list_plugins`로 플러그인의 **hash**와 인자를 확인한 뒤 `create_scan_template`을 사용합니다.

반환 결과에서 근거가 되는 대상, URL·포트, 스캔 작업 식별자, 요청·응답을 읽고 필요한 검증에 연결합니다. ARTEX 작업 안에서는 현재 제공된 자산·사실·발견 기록 도구로 정보를 전달하고, 독립 대화에서는 결과와 파일 경로를 대화에 남깁니다. ScopeSentry 등록만으로 ARTEX 그래프가 자동 동기화되지는 않습니다. 스캔 취소나 상세 진행 조회는 실제 서버가 제공한 도구가 있을 때만 호출합니다.

서버 계약 확인: [ScopeSentry MCP 구현](https://github.com/Autumn-27/ScopeSentry/tree/main/internal/mcp).
