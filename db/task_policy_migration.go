package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

func legacyPolicyText(p *legacyTaskPolicy) string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("【이전 작업 설정에서 가져온 지침")
	if p.Name != "" {
		b.WriteString(": " + p.Name)
	}
	b.WriteString("】\n")
	if p.Instructions != "" {
		b.WriteString(p.Instructions + "\n")
	}
	for _, c := range p.Constraints {
		label := c.Kind
		if c.Kind == "allow" {
			label = "허용"
		} else if c.Kind == "deny" {
			label = "금지"
		}
		b.WriteString(label + ": " + c.Text + "\n")
	}
	return strings.TrimSpace(b.String())
}

func appendLegacyPolicy(description, block string) string {
	if block == "" || strings.Contains(description, block) {
		return description
	}
	if strings.TrimSpace(description) == "" {
		return block
	}
	return description + "\n\n" + block
}

// Migrate complete task rows at startup. Archived stubs contain no original
// description/graph; their verified manifest is migrated during restoration.
func (d *DB) migrateLegacyTaskPolicies() error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := migrateLegacyTaskPoliciesTx(tx, 0); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT id,description,test_preset FROM task_templates WHERE test_preset<>'' AND test_preset_migrated_at IS NULL FOR UPDATE`)
	if err != nil {
		return err
	}
	type template struct {
		id                  int64
		description, preset string
	}
	var templates []template
	for rows.Next() {
		var row template
		if err := rows.Scan(&row.id, &row.description, &row.preset); err != nil {
			rows.Close()
			return err
		}
		templates = append(templates, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, row := range templates {
		old := legacyV1TaskPolicy(row.preset)
		if old == nil {
			old = &legacyTaskPolicy{Name: row.preset}
		}
		if _, err := tx.Exec(`UPDATE task_templates SET description=$2,test_preset_migrated_at=now() WHERE id=$1`, row.id, appendLegacyPolicy(row.description, legacyPolicyText(old))); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func migrateLegacyTaskPoliciesTx(tx *sql.Tx, taskID int64) error {
	rows, err := tx.Query(`SELECT t.id,t.exploration_id,t.description,COALESCE(e.description,''),t.test_preset,t.test_policy
 FROM tasks t JOIN explorations e ON e.id=t.exploration_id
 WHERE ($1::bigint=0 OR t.id=$1) AND t.archived_at IS NULL AND t.test_policy_migrated_at IS NULL
 AND (t.test_policy<>'null'::jsonb OR t.test_preset<>'') FOR UPDATE OF t,e`, taskID)
	if err != nil {
		return err
	}
	type task struct {
		id, expID                                   int64
		description, explorationDescription, preset string
		raw                                         []byte
	}
	var tasks []task
	for rows.Next() {
		var row task
		if err := rows.Scan(&row.id, &row.expID, &row.description, &row.explorationDescription, &row.preset, &row.raw); err != nil {
			rows.Close()
			return err
		}
		tasks = append(tasks, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, row := range tasks {
		var old *legacyTaskPolicy
		if err := json.Unmarshal(row.raw, &old); err != nil {
			return fmt.Errorf("작업 %d의 이전 설정을 읽지 못했습니다: %w", row.id, err)
		}
		if old == nil {
			old = legacyV1TaskPolicy(row.preset)
		}
		if old == nil {
			old = &legacyTaskPolicy{Name: row.preset}
		}
		block := legacyPolicyText(old)
		description := appendLegacyPolicy(row.description, block)
		explorationDescription := appendLegacyPolicy(row.explorationDescription, block)
		if _, err := tx.Exec(`UPDATE tasks SET description=$2,test_policy_migrated_at=now() WHERE id=$1`, row.id, description); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE explorations SET description=$2 WHERE id=$1`, row.expID, explorationDescription); err != nil {
			return err
		}
		// The original graph fact carries the task brief into already-running graphs.
		// Preserve its other fields and custom summary text; do not regenerate goals.
		if _, err := tx.Exec(`UPDATE exploration_nodes SET payload=payload || jsonb_build_object(
   'description',$2::text,'summary',CASE WHEN position($3::text IN COALESCE(payload->>'summary',''))>0 THEN payload->>'summary' ELSE COALESCE(payload->>'summary','') || E'\n\n' || $3::text END)
   WHERE exploration_id=$1 AND kind='fact' AND state='origin'`, row.expID, explorationDescription, block); err != nil {
			return err
		}
		for _, c := range old.Constraints {
			if strings.TrimSpace(c.Text) == "" {
				continue
			}
			if c.Kind != "allow" && c.Kind != "deny" {
				return fmt.Errorf("작업 %d의 이전 제약 종류가 올바르지 않습니다", row.id)
			}
			if _, err := tx.Exec(`INSERT INTO task_constraints(exploration_id,kind,text,origin)
    SELECT $1,$2,$3,'system' WHERE NOT EXISTS(SELECT 1 FROM task_constraints WHERE exploration_id=$1 AND kind=$2 AND text=$3)`, row.expID, c.Kind, c.Text); err != nil {
				return err
			}
		}
	}
	return nil
}
