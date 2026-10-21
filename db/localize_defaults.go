package db

import "fmt"

// Only exact shipped text is replaced; rule matching, user edits and history stay intact.
func (d *DB) localizeBuiltinDefaults() error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range legacyRuleLabels {
		if _, err := tx.Exec(`UPDATE intercept_rules SET
    name=CASE WHEN name=$1 THEN $2 ELSE name END,
    message=CASE WHEN message=$3 THEN $4 ELSE message END
    WHERE name=$1 OR message=$3`, r.oldName, r.name, r.oldMessage, r.message); err != nil {
			return fmt.Errorf("localize intercept rule: %w", err)
		}
	}
	for _, r := range legacyAssetRuleNotes {
		if _, err := tx.Exec(`UPDATE asset_intercept_rules SET note=$2 WHERE builtin AND note=$1`, r[0], r[1]); err != nil {
			return fmt.Errorf("localize asset rule: %w", err)
		}
	}
	for _, a := range legacyAgentLabels {
		if _, err := tx.Exec(`UPDATE agents SET
    name=CASE WHEN name=$2 THEN $3 ELSE name END,
    description=CASE WHEN description=$4 THEN $5 ELSE description END
    WHERE builtin AND key=$1 AND ((name=$2 AND $2<>$3) OR (description=$4 AND $4<>$5))`, a.key, a.oldName, a.name, a.oldDescription, a.description); err != nil {
			return fmt.Errorf("localize agent: %w", err)
		}
	}
	for _, v := range legacyPromptVarLabels {
		if _, err := tx.Exec(`UPDATE agent_prompt_vars v SET
    description=CASE WHEN v.description=$3 THEN $4 ELSE v.description END,
    example=CASE WHEN v.example=$5 THEN $6 ELSE v.example END
    FROM agents a WHERE v.agent_id=a.id AND a.builtin AND a.key=$1 AND v.var_name=$2
    AND ((v.description=$3 AND $3<>$4) OR (v.example=$5 AND $5<>$6))`, v.agent, v.name, v.oldDescription, v.description, v.oldExample, v.example); err != nil {
			return fmt.Errorf("localize prompt variable: %w", err)
		}
	}
	return tx.Commit()
}

var legacyRuleLabels = []struct{ oldName, name, oldMessage, message string }{
	{"[内置] 递归强制删除 rm -rf", "[내장] 재귀 강제 삭제 rm -rf", "禁止执行递归强制删除（rm -rf / rm --recursive），可能永久损坏系统或靶机环境", "재귀 강제 삭제(rm -rf / rm --recursive)는 시스템 또는 대상 환경을 영구적으로 손상시킬 수 있어 금지됩니다"},
	{"[内置] 删除系统关键目录", "[내장] 시스템 핵심 디렉터리 삭제", "禁止删除系统关键路径", "시스템 핵심 경로 삭제는 금지됩니다"},
	{"[内置] 磁盘格式化 mkfs", "[내장] 디스크 포맷 mkfs", "禁止格式化磁盘（mkfs）", "디스크 포맷(mkfs)은 금지됩니다"},
	{"[内置] 覆写磁盘设备 dd", "[내장] 디스크 장치 덮어쓰기 dd", "禁止使用 dd 覆写磁盘设备", "dd로 디스크 장치를 덮어쓰는 작업은 금지됩니다"},
	{"[内置] Fork 炸弹", "[내장] Fork 폭탄", "禁止执行 Fork 炸弹", "Fork 폭탄 실행은 금지됩니다"},
	{"[内置] 关机 / 重启", "[내장] 종료 / 재시작", "禁止执行关机或重启命令", "종료 또는 재시작 명령은 금지됩니다"},
	{"[内置] 杀死全部进程", "[내장] 모든 프로세스 강제 종료", "禁止 kill -9 -1 或 killall -9（杀死所有进程）", "kill -9 -1 또는 killall -9(모든 프로세스 강제 종료)는 금지됩니다"},
	{"[内置] 磁盘擦除 shred / wipe", "[내장] 디스크 삭제 shred / wipe", "禁止对磁盘设备执行 shred/wipe 擦除", "디스크 장치에 대한 shred/wipe 삭제는 금지됩니다"},
	{"[内置] 清空防火墙规则", "[내장] 방화벽 규칙 초기화", "禁止清空防火墙规则（iptables -F / nft flush）", "방화벽 규칙 초기화(iptables -F / nft flush)는 금지됩니다"},
	{"[内置] SQL DROP DATABASE / TABLE / SCHEMA", "[내장] SQL DROP DATABASE / TABLE / SCHEMA", "禁止执行 DROP 操作，可能不可逆地销毁数据库对象", "DROP 작업은 데이터베이스 객체를 복구할 수 없게 삭제할 수 있어 금지됩니다"},
	{"[内置] SQL TRUNCATE", "[내장] SQL TRUNCATE", "禁止执行 TRUNCATE，可能清空数据表所有数据", "TRUNCATE는 테이블의 모든 데이터를 지울 수 있어 금지됩니다"},
	{"[内置] MongoDB drop / dropDatabase", "[내장] MongoDB drop / dropDatabase", "禁止执行 MongoDB drop 操作", "MongoDB drop 작업은 금지됩니다"},
	{"[内置] Redis FLUSHALL / FLUSHDB", "[내장] Redis FLUSHALL / FLUSHDB", "禁止执行 Redis FLUSHALL / FLUSHDB，可能清空全部缓存数据", "Redis FLUSHALL / FLUSHDB는 모든 캐시 데이터를 지울 수 있어 금지됩니다"},
	{"[内置] curl / wget 发送 DELETE 请求", "[내장] curl / wget DELETE 요청", "禁止通过 curl/wget 发送 HTTP DELETE 请求，可能删除目标系统数据", "curl/wget의 HTTP DELETE 요청은 대상 시스템 데이터를 삭제할 수 있어 금지됩니다"},
	{"[内置] Python HTTP 客户端 DELETE（requests/httpx/aiohttp）", "[내장] Python HTTP 클라이언트 DELETE(requests/httpx/aiohttp)", "禁止使用 Python HTTP 客户端发送 DELETE 请求", "Python HTTP 클라이언트의 DELETE 요청은 금지됩니다"},
	{"[内置] 脚本中声明 HTTP DELETE 方法（JS/通用）", "[내장] 스크립트의 HTTP DELETE 메서드(JS/일반)", "禁止在脚本中声明并发送 HTTP DELETE 请求", "스크립트에서 HTTP DELETE 요청을 선언하고 전송하는 작업은 금지됩니다"},
	{"[内置] 批量清空 / 清除接口路径", "[내장] 일괄 비우기 / 삭제 API 경로", "禁止调用批量清空或销毁类接口（/clear /wipe /flush /purge 等）", "일괄 비우기 또는 삭제 API(/clear /wipe /flush /purge 등) 호출은 금지됩니다"},
	{"[内置] 破坏性系统命令", "[내장] 파괴적 시스템 명령", "破坏性命令被拒绝（rm -rf / / mkfs / dd / fork bomb / 关机重启 / 覆写磁盘设备）", "파괴적 명령을 거부했습니다(rm -rf / / mkfs / dd / fork 폭탄 / 종료·재시작 / 디스크 장치 덮어쓰기)"},
	{"[内置] 数据外泄管道", "[내장] 데이터 유출 파이프", "疑似数据外泄管道被拒绝（命令输出经 curl/wget/nc 外传）", "데이터 유출이 의심되는 파이프를 거부했습니다(명령 출력을 curl/wget/nc로 외부 전송)"},
	{"[内置] 删除类接口路径", "[내장] 삭제 API 경로", "禁止调用删除类接口（/delete /remove /unlink /erase 等），不论使用哪种 HTTP 方法——多数应用的删除接口用 GET/POST 就能触发，同样会真实删除目标数据", "HTTP 메서드와 관계없이 삭제 API(/delete /remove /unlink /erase 등) 호출은 금지됩니다. 많은 앱에서 GET/POST만으로도 삭제 API가 실행되어 대상 데이터가 실제로 삭제됩니다"},
}

var legacyAssetRuleNotes = [][2]string{
	{"[内置] 政府网站 (.gov)", "[내장] 정부 사이트 (.gov)"},
	{"[内置] 政府网站 (.gov.cn)", "[내장] 정부 사이트 (.gov.cn)"},
	{"[内置] 教育网站 (.edu)", "[내장] 교육 사이트 (.edu)"},
	{"[内置] 教育网站 (.edu.cn)", "[내장] 교육 사이트 (.edu.cn)"},
}

var legacyAgentLabels = []struct{ key, oldName, name, oldDescription, description string }{
	{"goals", "目标拆解", "목표 분해", "把渗透任务目标拆解成若干独立、可验证的子目标。", "침투 테스트 목표를 독립적으로 검증할 수 있는 하위 목표로 나눕니다."},
	{"planner", "规划", "계획", "读取态势、判定目标，只在确有未覆盖的新方向时补充探索意图（每任务一个规划循环）。", "현황을 읽고 목표를 판단하며, 아직 다루지 않은 새 방향이 있을 때만 탐색 의도를 추가합니다(작업별 계획 루프 1개)."},
	{"mainagent", "主", "메인", "人机接口：观察进展，把人的意图落成 hint 或高优先级意图。", "사용자와의 접점: 진행 상황을 관찰하고 사용자의 의도를 hint 또는 우선순위가 높은 의도로 반영합니다."},
	{"worker", "执行", "실행", "领取一条意图执行，把发现的事实/漏洞写回知识图谱后停止。", "의도 하나를 맡아 실행하고 발견한 사실/취약점을 지식 그래프에 기록한 뒤 종료합니다."},
	{"auto", "Auto", "Auto", "平台操作助手：用工具管理任务(建/看/暂停/给提示)与资产，并可创建/修改 skill、自定义工具、MCP。", "플랫폼 운영 도우미: 도구로 작업(생성/조회/일시 정지/힌트 제공)과 자산을 관리하고 스킬, 사용자 정의 도구, MCP를 생성하거나 수정합니다."},
	{"pentest", "渗透测试", "침투 테스트", "独立渗透 agent：一人从侦察→找攻击面→深入利用→验证→收尾走完整条链，自己规划、自己执行、自己对抗式验证。", "독립 침투 테스트 에이전트: 정찰→공격 표면 탐색→심층 악용→검증→마무리의 전체 과정을 스스로 계획하고 실행하며 반증을 통해 검증합니다."},
}

var legacyPromptVarLabels = []struct{ agent, name, oldDescription, description, oldExample, example string }{
	{"goals", "EngagementDescription", "任务描述（测试对象/背景）", "작업 설명(테스트 대상/배경)", "测试 example.com 站点", "example.com 사이트 테스트"},
	{"planner", "Goal", "任务总目标", "작업의 전체 목표", "拿下 example.com 的管理员权限", "example.com의 관리자 권한 획득"},
	{"planner", "AssetSummary", "资产计数/类型分布摘要(可选)", "자산 수/유형 분포 요약(선택)", "domain:3 ip:5 site:2", "domain:3 ip:5 site:2"},
	{"mainagent", "Goal", "当前任务目标", "현재 작업 목표", "拿下 example.com 的管理员权限", "example.com의 관리자 권한 획득"},
	{"mainagent", "AssetSummary", "开局态势摘要(可选)", "초기 현황 요약(선택)", "domain:3 ip:5", "domain:3 ip:5"},
	{"mainagent", "FindingsSummary", "已确认漏洞摘要(可选)", "확인된 취약점 요약(선택)", "high:1 medium:2", "high:1 medium:2"},
	{"worker", "ProxyAddr", "记录代理地址(驱动 if 双文案)", "기록 프록시 주소(if 조건별 안내에 사용)", "127.0.0.1:8080", "127.0.0.1:8080"},
	{"worker", "WorkerName", "worker 自我标识(可选)", "Worker 식별자(선택)", "worker-1", "worker-1"},
}
