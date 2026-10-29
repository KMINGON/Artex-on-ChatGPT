package db

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
)

const taskCompletionDefaultsFlag = "task_completion_defaults_v1"

// Exact description and goal shipped in 35a93b2.
const taskCompletionTemplateBaseline = "c655861322a345166c2b8dec0e5d96202330e0c5677730b2fc70854d38280846"

func (d *DB) refreshTaskCompletionDefaults() error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`INSERT INTO settings(key,value) VALUES ($1,'done') ON CONFLICT(key) DO NOTHING`, taskCompletionDefaultsFlag)
	if err != nil {
		return err
	}
	added, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if added == 0 {
		return nil
	}
	for _, target := range defaultTaskTemplates {
		if target.Name != "실습 · 전체 검증" {
			continue
		}
		var id int64
		var description, goal string
		// The seed marker and automatic rows share a transaction timestamp. A
		// later user-created copy or edited-and-restored row is not that seed.
		err := tx.QueryRow(`SELECT t.id,t.description,t.goal FROM task_templates t
		JOIN task_categories c ON c.id=t.category_id
		JOIN settings s ON s.key=$3 AND s.value='done'
		WHERE t.name=$1 AND t.nkey=$1 AND c.name=$2 AND c.nkey=$2
		AND t.created_at=s.updated_at AND t.updated_at=t.created_at
		AND t.task_name IS NULL AND t.timeout_seconds IS NULL AND t.plan_heartbeat_seconds IS NULL
		AND t.seed_first_intent IS NULL AND t.coverage_enabled IS NULL AND t.intercept_rules='[]'::jsonb
		AND t.test_preset='' AND t.test_preset_migrated_at IS NULL
		FOR UPDATE OF t,c`, target.Name, target.Category, defaultTaskTemplatesSeededFlag).Scan(&id, &description, &goal)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if fmt.Sprintf("%x", sha256.Sum256([]byte(description+"\x00"+goal))) == taskCompletionTemplateBaseline && goal != target.Goal {
			if _, err := tx.Exec(`UPDATE task_templates SET goal=$2 WHERE id=$1`, id, target.Goal); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
