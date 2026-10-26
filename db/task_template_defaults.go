package db

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
)

const forkTaskTemplatesRemovedFlag = "fork_task_templates_removed_v1"

// Fingerprints of unchanged automatic seeds shipped before the original-based branch.
// The template CRUD API and user-edited rows remain available.
var forkTaskTemplateBaselines = []struct {
	Name, Category string
	Hashes         []string
}{
	{Name: "실습 · 전체 검증", Category: "실습", Hashes: []string{"f4a5a7b2988d1cf35401ae5e1ce4106f02d891ab51a7540563ac880600789ab3", "0145329ea820887fe082cb31322e22b94c241ec13e713dc4c2ff229dc4c70131", "f3fe66e73ecb51ea0512e7447dba98352107a18005e46aab34a746821cf58e65", "e8a748b50fde08e079b573ed22669aae3a43795696b3b52becfc568825502d59", "cdac62cdc3e42d4a4c9da9025c72d20b1985a2872fd07cbf78ed6cdc866c723d"}},
	{Name: "운영 · 관찰", Category: "운영", Hashes: []string{"957986fe2a0c9d791f44cc39aa285199861c4921efe6d2e3fc7cb94c65137f09", "0e7afb1ffe52073574772d136e639e294e82728dd870e65d2392b462e6f64fb6", "c5d98a7e4f1661b5869a7fc63e5d2d6bbb2d3be9279fcca5a02b8ec1a620ceec", "98d0ea35b88ad1ced146657d66ee642a22ede765300902fed10bf7db14e50ce9", "93fa4e34f4d08d3918c43dde9fbc274bfefab6ee0de2376aa092cd0a9e8d30af"}},
	{Name: "운영 · 일반 검증", Category: "운영", Hashes: []string{"ba7925bd1cd0fae579aeffd4d4b4da48a8a24bd45f8038331966cec72fa53204", "cc09322a8fe7c4b716ebb3ba4405679d674a68d02356876a8129839291aed618", "db28e5f4464301573a6ac3d246c5c4e1edca6c620209e0ce8b55088d0468088c", "0f61f2a9128209f5dcc946acd2ac78ab8e7645915f22099d9382fb97748ba10d"}},
	{Name: "운영 · 확장 검증", Category: "운영", Hashes: []string{"4074c346dd4bdfb862ceb86b2f82084e10453605343ad06f279975906bfe0cc0", "d9168763c0598d5ff1d743afb578d12ad4f0ec6c8ab39defeb655a83bbd791b2", "8eee5ee7325dfb15da9284b4889529a133ed4587235010e8395c890b1576d4d5", "0b9ff79c9794ef917ae802a7ff4eb8f32c0528925137289e7cde781951dd80cf", "7617e83c93694a6cb8aed0802865ae5d99b1f5651d103dee48220ae6fa77ab6e"}},
}

// Remove only unedited automatic rows. Tasks store their own text, not template
// references; existing tasks, categories and archived manifests are left intact.
func (d *DB) removeUneditedForkTaskTemplates() error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var seeded string
	if err := tx.QueryRow(`SELECT value FROM settings WHERE key='default_task_templates_v1'`).Scan(&seeded); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if seeded != "done" {
		return nil
	}
	result, err := tx.Exec(`INSERT INTO settings(key,value) VALUES ($1,'done') ON CONFLICT(key) DO NOTHING`, forkTaskTemplatesRemovedFlag)
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
	for _, baseline := range forkTaskTemplateBaselines {
		var id int64
		var description, goal string
		err := tx.QueryRow(`SELECT t.id,t.description,t.goal FROM task_templates t JOIN task_categories c ON c.id=t.category_id
		WHERE t.name=$1 AND t.nkey=$1 AND c.name=$2 AND c.nkey=$2
		AND t.task_name=t.name AND t.timeout_seconds=0 AND t.plan_heartbeat_seconds=$3
		AND t.seed_first_intent=false AND t.coverage_enabled=true AND t.intercept_rules='[]'::jsonb
		AND t.test_preset='' AND t.test_preset_migrated_at IS NULL FOR UPDATE OF t,c`, baseline.Name, baseline.Category, MinPlanHeartbeatSeconds).Scan(&id, &description, &goal)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(description+"\x00"+goal)))
		for _, known := range baseline.Hashes {
			if hash == known {
				if _, err := tx.Exec(`DELETE FROM task_templates WHERE id=$1`, id); err != nil {
					return err
				}
				break
			}
		}
	}
	return tx.Commit()
}
