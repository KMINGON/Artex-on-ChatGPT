package db

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestShutdownWordingUpgradePreservesRuleAndRollback(t *testing.T) {
	d, _ := templateFixtureDB(t)
	if _, err := d.Exec(`DELETE FROM settings WHERE key=$1`, shutdownRuleWordingFlag); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE intercept_rules SET name=$1,message=$2,enabled=false,priority=37,action='ask',timeout_enabled=true,timeout_seconds=91
WHERE name=$3`, "[내장] 종료 / 재시작", "종료 또는 재시작 명령은 금지됩니다", "[내장] 시스템 종료 / 재부팅"); err != nil {
		t.Fatal(err)
	}
	before, err := d.ListInterceptRules()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.localizeBuiltinDefaults(); err != nil {
		t.Fatal(err)
	}
	after, err := d.ListInterceptRules()
	if err != nil || len(after) != len(before) {
		t.Fatalf("rule count changed: %v", err)
	}
	byID := make(map[int64]InterceptRule)
	for _, r := range after {
		byID[r.ID] = r
	}
	var updatedID int64
	for _, r := range before {
		got := byID[r.ID]
		if r.Name == "[내장] 종료 / 재시작" {
			updatedID = r.ID
			r.Name = "[내장] 시스템 종료 / 재부팅"
			r.Message = "시스템 전원을 끄거나 재부팅하는 명령은 금지됩니다"
			if got.UpdatedAt.Before(r.UpdatedAt) {
				t.Fatal("updated timestamp moved backwards")
			}
			r.UpdatedAt = got.UpdatedAt // The DB trigger timestamps the wording update.
		}
		if !reflect.DeepEqual(r, got) {
			t.Fatalf("rule changed beyond wording: before=%+v after=%+v", r, got)
		}
	}
	if updatedID == 0 {
		t.Fatal("missing shutdown rule")
	}
	if _, err := d.Exec(`UPDATE intercept_rules SET name=$1,message=$2 WHERE id=$3`, "[내장] 종료 / 재시작", "종료 또는 재시작 명령은 금지됩니다", updatedID); err != nil {
		t.Fatal(err)
	}
	if err := d.localizeBuiltinDefaults(); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := d.QueryRow(`SELECT name FROM intercept_rules WHERE id=$1`, updatedID).Scan(&name); err != nil || name != "[내장] 종료 / 재시작" {
		t.Fatalf("explicit rollback overwritten: %s, %v", name, err)
	}
}

func TestLocalizeDefaultsPreservesUserChanges(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires isolated ARTEX_PG_DSN")
	}
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := fmt.Sprintf("ko_defaults_%d", time.Now().UnixNano())
	if _, err := base.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer base.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	d, err := Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var fresh int
	if err := d.QueryRow(`SELECT count(*) FROM intercept_rules WHERE name LIKE '[내장]%'`).Scan(&fresh); err != nil || fresh != 20 {
		t.Fatalf("fresh rules=%d err=%v", fresh, err)
	}
	if _, err := d.Exec(`DELETE FROM intercept_rules`); err != nil {
		t.Fatal(err)
	}
	old := legacyRuleLabels[0]
	untouched, err := d.CreateInterceptRule(old.oldName, "tool_input", "regex", "custom-pattern", "ask", old.oldMessage, 7, false, true, 90, "allow")
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := d.CreateInterceptRule("내가 정한 이름", "tool_input", "string", "fixture", "deny", old.oldMessage, 8, true, false, 60, "deny")
	if err != nil {
		t.Fatal(err)
	}
	edited, err := d.CreateInterceptRule(old.oldName, "tool_input", "string", "fixture", "allow", "내가 정한 설명", 9, true, false, 60, "deny")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE agents SET name='사용자 이름', description=$1 WHERE key='planner'`, legacyAgentLabels[1].oldDescription); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE agent_prompt_vars SET description='사용자 변수 설명', example=$1 WHERE var_name='Goal'`, "拿下 example.com 的管理员权限"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE asset_intercept_rules SET note=$1, enabled=false WHERE pattern='.gov'`, legacyAssetRuleNotes[0][0]); err != nil {
		t.Fatal(err)
	}
	if err := d.seedBuiltins(); err != nil {
		t.Fatal(err)
	}
	if err := d.seedBuiltins(); err != nil {
		t.Fatal(err)
	}
	rules, err := d.ListInterceptRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 3 {
		t.Fatalf("deleted defaults resurrected: %d", len(rules))
	}
	byID := map[int64]InterceptRule{}
	for _, rule := range rules {
		byID[rule.ID] = rule
	}
	got := byID[untouched.ID]
	if got.Name != old.name || got.Message != old.message || got.Pattern != "custom-pattern" || got.Action != "ask" || got.Priority != 7 || got.Enabled || !got.TimeoutEnabled || got.TimeoutSeconds != 90 || got.TimeoutAction != "allow" {
		t.Fatalf("translation changed rule behavior: %+v", got)
	}
	if got := byID[renamed.ID]; got.Name != "내가 정한 이름" || got.Message != old.message {
		t.Fatalf("renamed rule overwritten: %+v", got)
	}
	if got := byID[edited.ID]; got.Name != old.name || got.Message != "내가 정한 설명" {
		t.Fatalf("custom explanation overwritten: %+v", got)
	}
	var name, desc, example string
	if err := d.QueryRow(`SELECT name,description FROM agents WHERE key='planner'`).Scan(&name, &desc); err != nil {
		t.Fatal(err)
	}
	if name != "사용자 이름" || desc != legacyAgentLabels[1].description {
		t.Fatalf("agent metadata changed incorrectly: %s / %s", name, desc)
	}
	if err := d.QueryRow(`SELECT v.description,v.example FROM agent_prompt_vars v JOIN agents a ON a.id=v.agent_id WHERE a.key='planner' AND v.var_name='Goal'`).Scan(&desc, &example); err != nil {
		t.Fatal(err)
	}
	if desc != "사용자 변수 설명" || example != "example.com의 관리자 권한 획득" {
		t.Fatalf("variable metadata changed incorrectly: %s / %s", desc, example)
	}
	var enabled bool
	if err := d.QueryRow(`SELECT note,enabled FROM asset_intercept_rules WHERE pattern='.gov'`).Scan(&desc, &enabled); err != nil {
		t.Fatal(err)
	}
	if desc != legacyAssetRuleNotes[0][1] || enabled {
		t.Fatalf("asset rule changed incorrectly: %s enabled=%v", desc, enabled)
	}
}
