package db

import (
	"database/sql"
	"errors"
	"fmt"
)

const shellInterceptDefaultsFlag = "intercept_shell_command_defaults_v1"

// Migrate only unchanged shipped system-command rules. Custom input rules keep
// their full-JSON semantics, and a later user rollback survives subsequent starts.
func (d *DB) migrateShellInterceptDefaults() error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(7337741014)`); err != nil {
		return err
	}
	var done string
	if err := tx.QueryRow(`SELECT value FROM settings WHERE key=$1`, shellInterceptDefaultsFlag).Scan(&done); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if done == "done" {
		return nil
	}
	for _, r := range append(defaultInterceptRules(), defaultInterceptRulesV2()...) {
		if r.target != "shell_command" {
			continue
		}
		if _, err := tx.Exec(`UPDATE intercept_rules SET match_target='shell_command'
WHERE name=$1 AND pattern=$2 AND message=$3 AND priority=$4 AND enabled=$5
  AND match_target='tool_input' AND match_type=$6 AND action=$7
  AND timeout_enabled=false AND timeout_seconds=60 AND timeout_action='deny'`,
			r.name, r.pattern, r.message, r.priority, r.enabled, r.typ, r.action); err != nil {
			return fmt.Errorf("migrate shell intercept rule %q: %w", r.name, err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO settings(key,value) VALUES ($1,'done')
ON CONFLICT (key) DO UPDATE SET value='done'`, shellInterceptDefaultsFlag); err != nil {
		return err
	}
	return tx.Commit()
}
