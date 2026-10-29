package server

import (
	"database/sql"
	"errors"
)

const completionDefaultsFlag = "planner_completion_defaults_v1"

// Exact 35a93b2 Planner body; other roles and tool settings are not part of this update.
const completionPlannerBaseline = "8b44e077f0ded0d92bd4ee2669a57dc32f263b67fee4c797b70d4ad152fde7fd"

func (s *Server) refreshCompletionDefaults() error {
	tx, err := s.m.pg.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(7337741011)`); err != nil {
		return err
	}
	var done string
	if err := tx.QueryRow(`SELECT value FROM settings WHERE key=$1`, completionDefaultsFlag).Scan(&done); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if done == "true" {
		return nil
	}
	if err := refreshStoredPrompts(tx, map[string]string{"planner": completionPlannerBaseline}, "목표 달성과 탐색 종료를 구분하는 기본 지침 적용"); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO settings(key,value) VALUES($1,'true') ON CONFLICT(key) DO UPDATE SET value='true'`, completionDefaultsFlag); err != nil {
		return err
	}
	return tx.Commit()
}
