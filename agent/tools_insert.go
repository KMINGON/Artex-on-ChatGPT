package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// assetInterceptCandidates 提取一条待插入资产输入项的 域名/IP/URL 候选串，用于资产拦截匹配。
// URL 的 host 会拆出归类，使「只带 URL」的服务/端点资产也能被 域名/IP 规则命中。
func assetInterceptCandidates(item assetInputItem) (domains, ips, urls []string) {
	add := func(dst *[]string, s string) {
		if s = strings.TrimSpace(s); s != "" {
			*dst = append(*dst, s)
		}
	}
	add(&domains, item.Domain)
	for _, d := range item.BoundDomains {
		add(&domains, d)
	}
	add(&ips, item.IP)
	add(&ips, item.ServiceIP)
	add(&urls, item.URL)
	if item.URL != "" {
		if u, err := url.Parse(item.URL); err == nil {
			if h := u.Hostname(); h != "" {
				if net.ParseIP(h) != nil {
					add(&ips, h)
				} else {
					add(&domains, h)
				}
			}
		}
	}
	return domains, ips, urls
}

// assetInputLabel 返回一条待插入资产的简短标识，用于拦截说明消息。
func assetInputLabel(item assetInputItem) string {
	typ := strings.TrimSpace(item.Type)
	var target string
	switch {
	case strings.TrimSpace(item.Domain) != "":
		target = strings.TrimSpace(item.Domain)
	case strings.TrimSpace(item.URL) != "":
		target = strings.TrimSpace(item.URL)
	case strings.TrimSpace(item.IP) != "":
		target = strings.TrimSpace(item.IP)
	case strings.TrimSpace(item.ServiceIP) != "":
		target = strings.TrimSpace(item.ServiceIP)
	default:
		target = "(알 수 없음)"
	}
	if typ != "" {
		return fmt.Sprintf("[%s] %s", typ, target)
	}
	return target
}

// =====================================================================
// Unified asset insertion tools
// =====================================================================

// SetAssetStore wires the asset store and company store onto this ToolSet
// so the insert_assets, add_company_scope, and list_assets tools are active.
func (t *ToolSet) SetAssetStore(as *db.AssetStore, cs *db.CompanyStore) {
	t.as = as
	t.cs = cs
}

// assetInputItem is one element of the insert_assets "assets" array.
type assetInputItem struct {
	Type string `json:"type"` // root_domain|ip|subdomain|app|service|endpoint

	// ---- root_domain / subdomain ----
	Domain      string   `json:"domain"`
	ICP         string   `json:"icp"`
	RecordType  string   `json:"record_type"`
	RecordValue []string `json:"record_value"`

	// ---- ip ----
	IP           string           `json:"ip"`
	BoundDomains []string         `json:"bound_domains"`
	OpenPorts    []db.PortService `json:"open_ports"`

	// ---- app ----
	AppName     string `json:"app_name"`
	BundleID    string `json:"bundle_id"`
	Category    string `json:"category"`
	Description string `json:"description"`
	AppICP      string `json:"app_icp"`
	CompanyID   *int64 `json:"company_id"` // explicit company link (app only; others auto-attribute via scope)

	// ---- service (http) ----
	URL           string           `json:"url"`
	Technologies  []string         `json:"technologies"`
	StatusCode    *int             `json:"status_code"`
	ContentLength *int64           `json:"content_length"`
	PageTitle     string           `json:"page_title"`
	FaviconMMH3   string           `json:"favicon_mmh3"`
	Auth          []map[string]any `json:"auth"`
	ServiceName   string           `json:"service_name"`
	ServiceIP     string           `json:"service_ip"` // optional enrichment IP

	// ---- service (other) ----
	Port  int    `json:"port"`
	Proto string `json:"proto"`

	// ---- endpoint ----
	Method string           `json:"method"`
	Params []map[string]any `json:"params"`
}

// insertAssets is the unified insert_assets agent tool.
func (t *ToolSet) insertAssets() actool.CoreTool {
	return writeTool(
		"insert_assets",
		"새로 발견한 자산을 일괄 등록하며 여러 유형을 함께 제출할 수 있습니다(type 열거값 참조).\n"+
			"유형별 필수 필드: root_domain→domain; ip→ip(호스트명이 아닌 IPv4/IPv6); subdomain→domain; app→app_name; service(HTTP)→url; service(비HTTP)→service_name+port(ip/domain 중 하나 이상); endpoint→url+method. 나머지 필드는 각각의 설명을 참조하세요.\n"+
			"auth/technologies/params는 추가 병합(append)하며 기존 값을 덮어쓰지 않습니다.\n"+
			"반환값: {results:[{index,id,type}], errors:[{index,error}]}",
		obj(map[string]any{
			// task_id 不暴露给模型：worker 归属哪个 task 由程序经 SetTaskID 权威赋值(见 handler)。
			"assets": map[string]any{
				"type":        "array",
				"description": "자산 배열. 각 요소는 자산 레코드 하나에 대응",
				"items": obj(map[string]any{
					"type": map[string]any{
						"type":        "string",
						"enum":        []string{"root_domain", "ip", "subdomain", "app", "service", "endpoint"},
						"description": "자산 유형",
					},
					// root_domain / subdomain
					"domain":      str("루트 도메인 또는 하위 도메인(root_domain/subdomain 필수)"),
					"icp":         str("ICP 등록 번호(선택 사항)"),
					"record_type": str("DNS 레코드 유형: A/AAAA/CNAME/MX 등(subdomain 선택 사항)"),
					"record_value": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "DNS 레코드 값 목록(subdomain 선택 사항, 예: [\"1.2.3.4\",\"2.3.4.5\"])",
					},
					// ip
					"ip": str("IP 주소. 호스트명이 아닌 IPv4/IPv6 주소여야 합니다(호스트명은 type=subdomain의 domain 필드 사용). ip 유형은 필수이며 service/endpoint는 IP 연결용으로 지정할 수 있습니다"),
					"bound_domains": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "이 IP에 연결된 도메인 목록(ip 유형 선택 사항)",
					},
					"open_ports": map[string]any{
						"type":        "array",
						"description": "열린 포트 목록(ip 유형 선택 사항)",
						"items": obj(map[string]any{
							"port":    intp("포트 번호"),
							"service": str("서비스 이름(예: http/ssh/mysql, 선택 사항)"),
						}, "port"),
					},
					// app
					"app_name":    str("애플리케이션 이름(app 유형 필수)"),
					"bundle_id":   str("Bundle ID(app 유형 선택 사항)"),
					"category":    str("애플리케이션 분류(선택 사항)"),
					"description": str("애플리케이션 설명(선택 사항)"),
					"app_icp":     str("애플리케이션 ICP 등록(선택 사항)"),
					"company_id":  intp("소속 회사 id(app 유형 선택 사항. app은 scope로 자동 귀속할 수 없으므로 명시적으로 지정해야 합니다. id는 add_company_scope가 반환)"),
					// service (http)
					"url":         str("프로토콜과 포트를 포함한 전체 URL(HTTP 서비스 필수, service_type은 http로 자동 설정)"),
					"status_code": intp("HTTP 응답 상태 코드(예: 200/301/403/404, 선택 사항)"),
					"content_length": map[string]any{
						"type":        "integer",
						"description": "HTTP 응답 본문 바이트 수(선택 사항)",
					},
					"page_title":   str("페이지 <title> 내용(선택 사항)"),
					"favicon_mmh3": str("favicon MMH3 해시(선택 사항)"),
					"technologies": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "특징/기술 스택 목록(예: [\"Nginx\",\"Vue\",\"Bootstrap\"], 선택 사항)",
					},
					"auth": map[string]any{
						"type":        "array",
						"description": "발견한 인증정보 목록. 각 항목은 type/username/password 등 포함(선택 사항, 추가하며 덮어쓰지 않음)",
						"items":       map[string]any{"type": "object"},
					},
					// service (other，非 HTTP)
					"service_name": str("서비스 이름(예: ssh/mysql/redis, HTTP가 아닌 service 필수)"),
					"port":         intp("포트 번호(HTTP가 아닌 service 필수)"),
					// endpoint
					"method": str("HTTP 메서드: GET/POST/PUT/PATCH/DELETE 등(endpoint 필수)"),
					"params": map[string]any{
						"type":        "array",
						"description": "요청 인자 목록. 각 항목은 location(query/body/header/path)/name/value/type 포함(선택 사항, 추가하며 덮어쓰지 않음)",
						"items":       map[string]any{"type": "object"},
					},
				}, "type"),
			},
		}, "assets"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("insert_assets 비활성: AssetStore가 초기화되지 않았습니다"), nil
			}
			var a struct {
				Assets []assetInputItem `json:"assets"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("invalid input: " + err.Error()), nil
			}
			// task_id 由程序权威赋值(worker: SetTaskID)，不接受模型传入——避免模型漏传/错传
			// 导致资产未归任务或归错任务。无任务上下文的调用方(auto/pentest/chat)其 t.taskID=0。
			taskID := t.taskID

			type result struct {
				Index int    `json:"index"`
				ID    int64  `json:"id"`
				Type  string `json:"type"`
			}
			type errEntry struct {
				Index int    `json:"index"`
				Error string `json:"error"`
			}

			var results []result
			var errs []errEntry

			// Read the existing rules once; a storage error must not bypass them.
			blockRules, err := t.as.ListAssetInterceptRules()
			if err != nil {
				return actool.Errorf("자산 규칙 조회 실패: " + err.Error()), nil
			}
			var allowRules []db.AssetInterceptRule
			if t.taskID > 0 {
				tb, ta, err := t.as.TaskInterceptRulesSplit(t.taskID)
				if err != nil {
					return actool.Errorf("작업 자산 규칙 조회 실패: " + err.Error()), nil
				}
				blockRules = append(blockRules, tb...)
				allowRules = ta
			}

			for i, item := range a.Assets {
				// 资产闸门：先拦截后允许，被拒的资产禁止插入（跳过 Upsert 及后续副作用）。
				domains, ips, urls := assetInterceptCandidates(item)
				if d := db.EvaluateAssetGate(blockRules, allowRules, domains, ips, urls); !d.Allowed {
					errs = append(errs, errEntry{
						Index: i,
						Error: fmt.Sprintf("자산 %s %s, 삽입이 금지되었습니다", assetInputLabel(item), d.Reason),
					})
					continue
				}

				typ := strings.TrimSpace(item.Type)
				svcIP := item.ServiceIP
				if svcIP == "" {
					svcIP = item.IP
				}
				var explorationID int64
				if t.ts != nil {
					explorationID = t.ts.ID()
				}
				id, err := t.as.RegisterObservedAsset(ctx, db.AssetObservationContext{
					TaskID: taskID, ExplorationID: explorationID, OwnerNodeID: t.ownerNode,
					Type: typ, Domain: item.Domain, URL: item.URL, IP: svcIP,
				}, func(as *db.AssetStore) (int64, error) {

					switch typ {
					case "root_domain":
						return as.UpsertRootDomain(db.UpsertRootDomainReq{
							Domain: item.Domain,
							ICP:    item.ICP,
							TaskID: taskID,
						})

					case "ip":
						return as.UpsertIP(db.UpsertIPReq{
							IP:           item.IP,
							BoundDomains: item.BoundDomains,
							OpenPorts:    item.OpenPorts,
							TaskID:       taskID,
						})

					case "subdomain":
						return as.UpsertSubdomain(db.UpsertSubdomainReq{
							Domain:      item.Domain,
							RecordType:  item.RecordType,
							RecordValue: item.RecordValue,
							ICP:         item.ICP,
							TaskID:      taskID,
						})

					case "app":
						return as.UpsertApp(db.UpsertAppReq{
							Name:        item.AppName,
							BundleID:    item.BundleID,
							Category:    item.Category,
							Description: item.Description,
							ICP:         item.AppICP,
							CompanyID:   item.CompanyID,
							TaskID:      taskID,
						})

					case "service":
						// distinguish HTTP vs other by presence of url
						if item.URL != "" {
							return as.UpsertHTTPService(db.UpsertHTTPServiceReq{
								URL:           item.URL,
								Technologies:  item.Technologies,
								StatusCode:    item.StatusCode,
								ContentLength: item.ContentLength,
								PageTitle:     item.PageTitle,
								FaviconMMH3:   item.FaviconMMH3,
								Auth:          item.Auth,
								IP:            svcIP,
								TaskID:        taskID,
							})
						} else {
							return as.UpsertOtherService(db.UpsertOtherServiceReq{
								Domain:      item.Domain,
								IP:          item.IP,
								Port:        item.Port,
								ServiceName: item.ServiceName,
								Auth:        item.Auth,
								TaskID:      taskID,
							})
						}

					case "endpoint":
						return as.UpsertEndpoint(db.UpsertEndpointReq{
							URL:    item.URL,
							Method: item.Method,
							Params: item.Params,
							IP:     svcIP,
							TaskID: taskID,
						})

					default:
						return 0, fmt.Errorf("알 수 없는 자산 유형: %s", typ)
					}

				})

				if err != nil {
					errs = append(errs, errEntry{Index: i, Error: err.Error()})
					continue
				}
				results = append(results, result{Index: i, ID: id, Type: typ})
				t.writes.Assets++
			}

			return jsonResult(map[string]any{
				"results": results,
				"errors":  errs,
			})
		},
	)
}

// addCompanyScope writes to company_scope table and triggers asset attribution.
func (t *ToolSet) addCompanyScope() actool.CoreTool {
	return writeTool(
		"add_company_scope",
		"도메인/IP/CIDR/ICP 등록/회사 키워드를 회사의 【자산 범위】에 추가합니다. 도메인·네트워크·ICP에 일치하는 자산은 자동 귀속하고 키워드는 에이전트에 범위 힌트로만 제공합니다.\n"+
			"회사명은 고유합니다. company가 없으면 생성하고 이미 있으면 재사용하며 범위만 병합합니다.\n"+
			"scope는 한 줄에 하나이며 시스템이 자동 판별합니다: 루트 도메인 / URL / 단일 IP / CIDR 대역 / ICP 등록 / 회사 키워드.\n"+
			"귀속 판단의 근거와 확인한 출처(whois/인증서/ASN 등)를 reason에 반드시 작성하세요.\n"+
			"보호 규칙: TLD만 지정하거나 지나치게 넓은 대역은 거부합니다(IPv4 접두사 /16-/32, IPv6 접두사 /32-/128 필요). 유효하지 않은 줄은 건너뛰고 errors로 반환합니다.",
		obj(map[string]any{
			"company": str("회사명(없으면 생성, 있으면 재사용. 이름은 고유)"),
			"scope":   str("자산 범위, 한 줄에 하나: 도메인 / URL / IP / CIDR / ICP 등록 / 회사 키워드"),
			"reason":  str("귀속 근거(증거/출처), 반드시 작성"),
			"logo":    str("회사 아이콘 URL(선택 사항, 회사 신규 생성 시에만 적용)"),
		}, "company", "scope"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.cs == nil {
				return actool.Errorf("add_company_scope 비활성: CompanyStore가 초기화되지 않았습니다"), nil
			}
			var a struct {
				Company string `json:"company"`
				Scope   string `json:"scope"`
				Reason  string `json:"reason"`
				Logo    string `json:"logo"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if strings.TrimSpace(a.Company) == "" {
				return actool.Errorf("company는 비워 둘 수 없습니다"), nil
			}
			companyID, _, err := t.cs.UpsertCompany(a.Company, a.Logo)
			if err != nil {
				return actool.Errorf("회사 생성/조회 실패: " + err.Error()), nil
			}
			lines := splitLines(a.Scope)
			added, skipped, invalid, errMsgs := t.cs.AddScope(companyID, lines, a.Reason)
			out := map[string]any{
				"company_id": companyID,
				"added":      added,
				"skipped":    skipped,
				"invalid":    invalid,
			}
			if len(errMsgs) > 0 {
				out["errors"] = errMsgs
			}
			return jsonResult(out)
		},
	)
}

// addTaskScope lets the plan agent add test scope to THE CURRENT TASK — the coverage
// denominator and the task's authorization edge. Worker discoveries are auto-scoped
// (precise host) by insertAssets; this tool is for DELIBERATELY WIDENING: pull a whole
// root domain or whole company into scope, or add a specific subdomain / ip.
func (t *ToolSet) addTaskScope() actool.CoreTool {
	return writeTool(
		"add_task_scope",
		"【이 작업】에 테스트 범위를 추가합니다. 작업의 허가 경계이자 자산 테스트 커버리지의 분모입니다.\n"+
			"지원 kind: company(회사 전체 자산) / root_domain(모든 하위 도메인을 포함한 루트 도메인 전체) / subdomain(정확히 일치하는 단일 하위 도메인) / ip / cidr / icp / keyword.\n"+
			"insert_assets로 명시 등록한 자산은 해당 유형에 따라 작업 범위에 자동 추가됩니다. 단순한 요청·브라우저 방문·파일 기록만으로 새 자산이 등록되는 것은 아닙니다. 이 도구는 전체 루트 도메인/회사를 포함하거나 특정 하위 도메인/IP를 추가하여 【명시적으로 확장】할 때 사용합니다.\n"+
			"value: company는 회사명 또는 id(기존 회사 필수), root_domain/subdomain은 도메인, ip/cidr는 IP 또는 대역, icp/keyword는 등록 번호 또는 회사 키워드를 전달하세요.\n"+
			"범위에 포함하는 사유와 확인한 근거를 reason에 반드시 작성하세요. 여러 항목은 entries 배열을 사용하세요.",
		obj(map[string]any{
			"entries": map[string]any{"type": "array", "description": "일괄: [{kind, value}]. kind∈company/root_domain/subdomain/ip/cidr/icp/keyword.", "items": map[string]any{"type": "object"}},
			"kind":    str("[단일] company / root_domain / subdomain / ip / cidr / icp / keyword"),
			"value":   str("[단일] 회사명 또는 id / 도메인 / IP / CIDR / ICP / 키워드"),
			"reason":  str("추가 근거(감사용), 반드시 작성"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("add_task_scope 비활성: AssetStore가 초기화되지 않았습니다"), nil
			}
			if t.taskID <= 0 {
				return actool.Errorf("add_task_scope에는 작업 컨텍스트가 필요합니다(현재 task 없음)"), nil
			}
			type scopeEntry struct {
				Kind  string `json:"kind"`
				Value string `json:"value"`
			}
			var a struct {
				Entries    []scopeEntry `json:"entries"`
				scopeEntry              // 单条模式
				Reason     string       `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			items := a.Entries
			if len(items) == 0 {
				items = []scopeEntry{a.scopeEntry}
			}
			var added []map[string]any
			errs := map[string]string{}
			for i, e := range items {
				ts, err := t.as.AddAgentScope(t.taskID, strings.TrimSpace(e.Kind), e.Value, a.Reason, "agent")
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				added = append(added, map[string]any{"kind": ts.Kind, "domain": ts.Domain, "net": ts.Net, "value": ts.Value, "company_id": ts.CompanyID})
			}
			out := map[string]any{"added": added}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		},
	)
}

// listUntestedAssets lets the plan agent pull the current + directly inherited
// scope's not-yet-tested assets on demand (filter by type, paginated).
func (t *ToolSet) listUntestedAssets() actool.CoreTool {
	return readTool(
		"list_untested_assets",
		"【이 작업과 직접 연결된 작업】의 등록 자산 중 유효한 fact/finding의 자산 연결(anchor)이 아직 없는 항목을 조회합니다. 실제 요청 이력이나 기능별 검증 완료 여부와 같지 않습니다(연결된 범위는 읽기 전용이며 추가 테스트 여부를 직접 판단할 참고 자료이지 결정을 대신하지 않음).\n"+
			"자산 유형 필터 선택 가능: root_domain/subdomain/service/app/endpoint/ip.\n"+
			"페이지 조회: page는 1부터, page_size는 기본 10입니다. 반환값 {assets:[{id,type,label}], total, page, page_size}. 작업 컨텍스트에서만 사용 가능합니다.",
		obj(map[string]any{
			"type":      str("자산 유형 필터(선택 사항): root_domain/subdomain/service/app/endpoint/ip"),
			"page":      intp("페이지 번호, 1부터 시작(기본 1)"),
			"page_size": intp("페이지당 개수(기본 10)"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("list_untested_assets 비활성: AssetStore가 초기화되지 않았습니다"), nil
			}
			if t.taskID <= 0 || t.ts == nil {
				return actool.Errorf("list_untested_assets에는 작업 컨텍스트가 필요합니다"), nil
			}
			var a struct {
				Type     string `json:"type"`
				Page     int    `json:"page"`
				PageSize int    `json:"page_size"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Page <= 0 {
				a.Page = 1
			}
			if a.PageSize <= 0 {
				a.PageSize = 10
			}
			offset := (a.Page - 1) * a.PageSize
			assets, total, err := t.as.ListUntestedAssetsWithSources(t.taskID, strings.TrimSpace(a.Type), a.PageSize, offset)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(map[string]any{
				"assets": assets, "total": total, "page": a.Page, "page_size": a.PageSize,
			})
		},
	)
}

// listAssets lets an agent query the asset table.
func (t *ToolSet) listAssets() actool.CoreTool {
	return readTool(
		"list_assets",
		"자산 저장소 조회: DSL 표현식 검색 또는 id/ids 직접 조회, 페이지 조회 지원. 작업 안에서는 【이 작업과 직접 연결된 작업】의 테스트 범위 내 자산만 반환하며, 작업 컨텍스트가 없는 플랫폼 에이전트에서는 전역 자산 저장소를 조회합니다.\n"+
			"DSL: field=value 부분 일치(ILIKE) | field==value 정확히 일치 | field!=value 제외 | 숫자 필드는 > >= < <= 지원 | 필드 없는 단어=전체 텍스트 부분 일치. AND/OR 결합(AND 우선), 괄호 그룹 지원. 자산 유형은 DSL 대신 별도 type 인자를 사용하세요.\n"+
			"id/ids를 전달하지 않으면 dsl이 비어 있으면 안 됩니다(조건 없는 전체 조회 금지).\n"+
			"조회 우선순위는 양수 id, 비어 있지 않은 ids, dsl 순입니다. 직접 id/ids 조회에서는 dsl/type/limit/offset을 적용하지 않습니다.\n"+
			"사용 가능 필드: domain(루트/하위/서비스 도메인), root_domain, ip, url, page_title, icp, service_name, app_name, method(예: GET/POST), service_type(http|other), record_type(예: A/CNAME), technology(배열, =부분 일치 ==정확히 일치), port/status_code/company_id(정수).\n"+
			"예: status_code>=400 AND technology=shiro ; (port==80 OR port==443) AND technology=nginx",
		obj(map[string]any{
			"dsl":    str(`DSL 조회 표현식(문법/필드는 도구 설명 참조). id/ids가 없으면 필수입니다.`),
			"type":   str("DSL 검색의 자산 유형 필터: root_domain|ip|subdomain|app|service|endpoint. type만으로는 조회할 수 없고 dsl이 필요하며, id/ids 직접 조회에는 적용하지 않습니다."),
			"id":     intp("단일 자산 id 직접 조회(선택 사항). 양수 id가 있으면 ids/dsl/type보다 우선합니다."),
			"ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "여러 자산 id 직접 조회(선택 사항). 양수 id가 없으면 dsl/type보다 우선합니다."},
			"limit":  intp("DSL 검색의 반환 상한, 기본 10(선택 사항)"),
			"offset": intp("DSL 검색의 페이지 오프셋, 기본 0(선택 사항)"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("list_assets 비활성: AssetStore가 초기화되지 않았습니다"), nil
			}
			var a struct {
				DSL    string  `json:"dsl"`
				Type   string  `json:"type"`
				ID     int64   `json:"id"`
				IDs    []int64 `json:"ids"`
				Limit  int     `json:"limit"`
				Offset int     `json:"offset"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Limit <= 0 {
				a.Limit = 10
			}

			var assets []*db.Asset
			var err error
			switch {
			case a.ID > 0:
				assets, err = t.as.GetByIDsInScope(t.taskID, []int64{a.ID})
			case len(a.IDs) > 0:
				assets, err = t.as.GetByIDsInScope(t.taskID, a.IDs)
			case a.DSL != "":
				assets, err = t.as.QueryDSLInScope(a.DSL, a.Type, t.taskID, a.Limit, a.Offset)
			default:
				return actool.Errorf("id/ids를 전달하지 않으면 dsl은 필수입니다. 조건 없이 전체 자산을 조회할 수 없으므로 조회 조건을 제공하세요"), nil
			}
			if err != nil {
				return actool.Errorf("DSL 오류: " + err.Error()), nil
			}
			return jsonResult(map[string]any{
				"count":  len(assets),
				"assets": assets,
			})
		},
	)
}

// listCompanies lets an agent enumerate companies (企业) with their scope + asset count.
func (t *ToolSet) listCompanies() actool.CoreTool {
	return readTool(
		"list_companies",
		"자산 저장소의 【기업/회사】와 자산 범위(scope), 귀속 자산 수를 나열합니다. 회사 목록 확인과 "+
			"company_id 조회에 사용합니다(insert_assets의 app 연결, list_assets의 company_id 필터 등에 사용)."+
			"선택 인자 search로 회사명 부분 일치 필터(대소문자 무시)를 적용하며 비워 두면 전체 반환합니다.",
		obj(map[string]any{
			"search": str("회사명 부분 일치 필터(선택 사항, 대소문자 무시). 비워 두면 전체 반환"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.cs == nil {
				return actool.Errorf("list_companies 비활성: CompanyStore가 초기화되지 않았습니다"), nil
			}
			var a struct {
				Search string `json:"search"`
			}
			_ = json.Unmarshal(in, &a)
			cos, err := t.cs.ListCompanies()
			if err != nil {
				return actool.Errorf("회사 조회 실패: " + err.Error()), nil
			}
			q := strings.ToLower(strings.TrimSpace(a.Search))
			type companyOut struct {
				ID         int64    `json:"id"`
				Name       string   `json:"name"`
				AssetCount int      `json:"asset_count"`
				Scope      []string `json:"scope"`
			}
			out := make([]companyOut, 0, len(cos))
			for _, c := range cos {
				if q != "" && !strings.Contains(strings.ToLower(c.Name), q) {
					continue
				}
				scope := make([]string, 0, len(c.Scope))
				for _, r := range c.Scope {
					scope = append(scope, r.Raw)
				}
				out = append(out, companyOut{ID: c.ID, Name: c.Name, AssetCount: c.AssetCount, Scope: scope})
			}
			return jsonResult(map[string]any{"count": len(out), "companies": out})
		},
	)
}

// splitLines splits a multi-line string into non-empty trimmed lines.
func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// WorkerTools returns the tool set for a work agent.
func (t *ToolSet) WorkerTools() []actool.CoreTool {
	return []actool.CoreTool{
		// list_findings 保留：报漏洞前先查本任务已确认漏洞，避免重复上报同一漏洞。
		t.listFindings(),
		t.addFinding(), t.recordFact(),
		// asset management (handlers guard nil store internally)。
		// add_company_scope 不给 worker：定义企业资产范围属规划/主控/Auto 的职责，worker 只执行探索。
		t.insertAssets(), t.listAssets(),
		// 跨 work 回看：worker 也可复用其他 work 的观察，避免重复劳动。
		// search_all_worker_traces：不必先知道 intent_id，按关键字全局捞命中步骤；
		// get_worker_trace：锁定某条 work 后列步骤/就地搜/取完整内容。
		t.searchAllWorkerTraces(), t.getWorkerTrace(),
		// node_detail：worker 拿到 intent_id/节点 id 后可查该节点完整详情（配合上面的回看）。
		t.nodeDetail(),
		// 以下工具仍【不给】worker，只留给 planner/main（读上下文、跨 work 复盘是规划职责，
		// worker 只做单条意图的执行与写回）：list_facts / list_companies / list_worker_traces。
	}
}

// MainAgentTools returns the human-interface tool set.
func (t *ToolSet) MainAgentTools() []actool.CoreTool {
	return []actool.CoreTool{
		t.graphOverview(), t.listFindings(), t.listFacts(), t.nodeDetail(),
		t.expandDigest(), // cold-digest §6.1
		t.getWorkerOutput(), t.getWorkerTrace(), t.searchAllWorkerTraces(), t.addHint(), t.addIntent(),
		// steer_work：人可对某条正在运行的意图(work)实时注入纠偏指令（不打断、不丢进展）。
		t.steerWorkTool(),
		// set_goals：人可在运行时给本任务补一个新的最终目标（规划者据此重判是否达成）。
		t.setGoals(),
		// set_constraints：人可在运行时给本任务补/改操作约束（allow/deny），约束 planner/worker 的探索边界。
		t.setConstraints(),
		// asset management (handlers guard nil store internally)
		t.insertAssets(), t.addCompanyScope(), t.listAssets(),
		t.addFinding(), t.recordFact(),
		t.addTaskScope(),
		// list_untested_assets：按需查本任务范围内未测资产(类型+分页)，自行决定补测。
		t.listUntestedAssets(),
	}
}

// AllDomainTools returns the union of all domain tools across all agent types,
// deduped by name (mainagent order wins). Used by the server to build a registry
// for injecting domain tools into agents (Auto, custom) that don't own a per-task
// ToolSet. The caller provides real stores; tools are callable at taskID=0 scope.
func (t *ToolSet) AllDomainTools() []actool.CoreTool {
	seen := map[string]bool{}
	var out []actool.CoreTool
	all := append(append(t.MainAgentTools(), t.PlannerTools()...), t.WorkerTools()...)
	for _, tool := range all {
		if !seen[tool.Name()] {
			seen[tool.Name()] = true
			out = append(out, tool)
		}
	}
	return out
}
