package db

import (
	"encoding/json"
	"testing"
)

func TestShellInterceptDefaultsMigrationPreservesUserRules(t *testing.T) {
	d, dsn := templateFixtureDB(t)
	var count int
	if err := d.QueryRow(`SELECT count(*) FROM intercept_rules WHERE match_target='shell_command'`).Scan(&count); err != nil || count != 10 {
		t.Fatalf("new shell defaults=%d err=%v", count, err)
	}
	// Recreate a pre-upgrade database, including its old CHECK constraint.
	if _, err := d.Exec(`UPDATE intercept_rules SET match_target='tool_input' WHERE match_target='shell_command';
DELETE FROM settings WHERE key='intercept_shell_command_defaults_v1';
ALTER TABLE intercept_rules DROP CONSTRAINT intercept_rules_match_target_check;
ALTER TABLE intercept_rules ADD CONSTRAINT intercept_rules_match_target_check CHECK (match_target IN ('tool_name','tool_input'))`); err != nil {
		t.Fatal(err)
	}
	var original InterceptRule
	rules, err := d.ListInterceptRules()
	if err != nil {
		t.Fatal(err)
	}
	originalIDs := make(map[int64]bool, len(rules))
	for _, r := range rules {
		originalIDs[r.ID] = true
		if r.Name == "[내장] 종료 / 재시작" {
			original = r
		}
	}
	if original.ID == 0 {
		t.Fatal("missing shutdown fixture")
	}
	for _, edit := range []struct{ field, value string }{
		{"name", "사용자 이름"}, {"pattern", "custom-halt"}, {"message", "사용자 설명"},
		{"priority", "99"}, {"enabled", "false"}, {"action", "ask"},
		{"match_type", "string"}, {"timeout_enabled", "true"},
		{"timeout_seconds", "90"}, {"timeout_action", "allow"},
	} {
		r, err := d.CreateInterceptRule(original.Name, original.MatchTarget, original.MatchType, original.Pattern, original.Action, original.Message, original.Priority, original.Enabled, original.TimeoutEnabled, original.TimeoutSeconds, original.TimeoutAction)
		if err != nil {
			t.Fatal(err)
		}
		// field is one of the test-owned column literals above.
		if _, err := d.Exec(`UPDATE intercept_rules SET `+edit.field+`=$1 WHERE id=$2`, edit.value, r.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Exec(`INSERT INTO intercept_pending(rule_id,tool_name,tool_input,status,reason)
VALUES ($1,'Write','{"content":"print(''halt'')"}','denied','과거 판정 보존')`, original.ID); err != nil {
		t.Fatal(err)
	}
	before, err := d.ListInterceptRules()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	after, err := upgraded.ListInterceptRules()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]InterceptRule{}
	for _, r := range after {
		byID[r.ID] = r
	}
	for _, r := range before {
		got := byID[r.ID]
		wantTarget := r.MatchTarget
		if originalIDs[r.ID] {
			for _, shipped := range append(defaultInterceptRules(), defaultInterceptRulesV2()...) {
				if shipped.target == "shell_command" && r.Name == shipped.name {
					wantTarget = "shell_command"
				}
			}
		}
		if got.MatchTarget != wantTarget {
			t.Errorf("rule %d (%s) target=%s, want %s", r.ID, r.Name, got.MatchTarget, wantTarget)
		}
		r.MatchTarget, got.MatchTarget = "", ""
		r.UpdatedAt, got.UpdatedAt = r.CreatedAt, r.CreatedAt
		beforeJSON, _ := json.Marshal(r)
		afterJSON, _ := json.Marshal(got)
		if string(beforeJSON) != string(afterJSON) {
			t.Errorf("rule %d changed beyond target: %s / %s", r.ID, beforeJSON, afterJSON)
		}
	}
	if err := upgraded.QueryRow(`SELECT count(*) FROM intercept_pending WHERE rule_id=$1 AND reason='과거 판정 보존' AND tool_input->>'content'='print(''halt'')'`, original.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("historical decision changed: count=%d err=%v", count, err)
	}
	// Explicit rollback and deletion after this migration are user's choices.
	if _, err := upgraded.Exec(`UPDATE intercept_rules SET match_target='tool_input' WHERE id=$1`, original.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := upgraded.Exec(`DELETE FROM intercept_rules WHERE name='[내장] 디스크 포맷 mkfs'`); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.seedBuiltins(); err != nil {
		t.Fatal(err)
	}
	var target string
	if err := upgraded.QueryRow(`SELECT match_target FROM intercept_rules WHERE id=$1`, original.ID).Scan(&target); err != nil || target != "tool_input" {
		t.Fatalf("explicit rollback lost: target=%s err=%v", target, err)
	}
	if err := upgraded.QueryRow(`SELECT count(*) FROM intercept_rules WHERE name='[내장] 디스크 포맷 mkfs'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted rule restored: count=%d err=%v", count, err)
	}
}

func TestShellInterceptDefaultsMigrationFollowsLegacyLocalization(t *testing.T) {
	d, _ := templateFixtureDB(t)
	old := legacyRuleLabels[5]
	if _, err := d.Exec(`UPDATE intercept_rules SET name=$1,message=$2,match_target='tool_input' WHERE name=$3`, old.oldName, old.oldMessage, old.name); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`DELETE FROM settings WHERE key=$1`, shellInterceptDefaultsFlag); err != nil {
		t.Fatal(err)
	}
	if err := d.seedBuiltins(); err != nil {
		t.Fatal(err)
	}
	var target, message string
	if err := d.QueryRow(`SELECT match_target,message FROM intercept_rules WHERE name=$1`, old.name).Scan(&target, &message); err != nil {
		t.Fatal(err)
	}
	if target != "shell_command" || message != old.message {
		t.Fatalf("legacy rule not migrated after localization: target=%s message=%s", target, message)
	}
}

func TestShellInterceptDefaultsMigrationIsAtomic(t *testing.T) {
	d, _ := templateFixtureDB(t)
	if _, err := d.Exec(`UPDATE intercept_rules SET match_target='tool_input' WHERE match_target='shell_command';
DELETE FROM settings WHERE key='intercept_shell_command_defaults_v1';
CREATE FUNCTION reject_shell_migration_fixture() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.name='[내장] 파괴적 시스템 명령' AND NEW.match_target='shell_command' THEN
    RAISE EXCEPTION 'fixture migration failure';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER reject_shell_migration_fixture BEFORE UPDATE ON intercept_rules
FOR EACH ROW EXECUTE FUNCTION reject_shell_migration_fixture()`); err != nil {
		t.Fatal(err)
	}
	if err := d.migrateShellInterceptDefaults(); err == nil {
		t.Fatal("injected migration failure was ignored")
	}
	var count int
	if err := d.QueryRow(`SELECT count(*) FROM intercept_rules WHERE match_target='shell_command'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial rule migration survived rollback: count=%d err=%v", count, err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM settings WHERE key=$1`, shellInterceptDefaultsFlag).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed migration marked complete: count=%d err=%v", count, err)
	}
	if _, err := d.Exec(`DROP TRIGGER reject_shell_migration_fixture ON intercept_rules;
DROP FUNCTION reject_shell_migration_fixture()`); err != nil {
		t.Fatal(err)
	}
	if err := d.migrateShellInterceptDefaults(); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM intercept_rules WHERE match_target='shell_command'`).Scan(&count); err != nil || count != 10 {
		t.Fatalf("retry did not finish migration: count=%d err=%v", count, err)
	}
}
