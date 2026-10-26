package db

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
)

const forkTaskTemplatesRemovedFlag = "fork_task_templates_removed_v1"

// Reuse the original seed marker so defaults deleted in earlier releases stay deleted.
const defaultTaskTemplatesSeededFlag = "default_task_templates_v1"

var defaultTaskTemplates = []struct {
	Name, Category, Description, Goal string
}{
	{
		Name: "실습 · 전체 검증", Category: "실습",
		Description: `대상: [실습 URL·허용 범위]
계정·자료: [제공 계정과 참고 자료]
지정된 실습 환경의 기능과 공격 표면을 폭넓게 살펴보세요.
발견한 단서는 취약점의 재현과 영향 확인까지 이어가세요.
실습 대상 안에서는 데이터 변경·업로드·명령 실행을 포함해 검증할 수 있습니다.
재현 근거와 실제 영향, 아직 확인하지 못한 사항을 구분해 남기세요.`,
		Goal: "실습 대상의 취약점을 최대한 찾아 재현 가능한 증거와 실제 영향을 정리하세요.",
	},
	{
		Name: "운영 · 관찰", Category: "운영",
		Description: `대상: [운영 URL·허용 범위]
계정·자료: [제공 계정과 참고 자료]
일반 조회로 노출 정보와 보안 설정을 확인하세요.
제공 계정에서 보이는 화면·API·권한 경계를 살펴보세요.
데이터 변경, 공격 입력, 대량 요청, 서버 명령 실행은 하지 마세요.
직접 확인한 문제와 추가 검증이 필요한 의심 사항을 구분하세요.`,
		Goal: "운영 데이터를 변경하지 않는 관찰 범위에서 보안 문제와 추가 검증 대상을 정리하세요.",
	},
	{
		Name: "운영 · 일반 검증", Category: "운영",
		Description: `대상: [운영 URL·허용 범위]
계정·자료: [제공 계정과 참고 자료]
기능·API·인증·권한 경계를 살펴보고 의심 지점을 실제로 검증하세요.
상태 변경이 필요하면 제공된 시험 계정과 시험 데이터를 사용하세요.
서비스 중단, 실제 사용자 데이터 훼손, 되돌릴 수 없는 변경은 피하세요.
재현 근거와 영향을 남기고, 만든 시험 데이터의 정리 여부를 기록하세요.`,
		Goal: "허용된 일반 검증 범위에서 취약점을 찾아 재현 근거와 영향을 정리하세요.",
	},
	{
		Name: "운영 · 확장 검증", Category: "운영",
		Description: `대상: [운영 URL·허용 범위]
계정·자료: [제공 계정과 참고 자료]
기능과 공격 경로를 폭넓게 살펴보고 발견한 단서를 끝까지 검증하세요.
허용 범위 안에서 권한 상승·업로드·서버 명령 실행과 취약점 연계를 검증하세요.
서비스 중단과 되돌릴 수 없는 변경은 피하고, 변경한 시험 자원을 정리하세요.
재현 근거·확인한 영향·정리 여부와 미확인 사항을 구분해 남기세요.`,
		Goal: "허용된 확장 검증 범위에서 취약점과 연결 가능한 공격 경로의 실제 영향을 확인하세요.",
	},
}

// Seed editable task inputs once. Execution options stay unset so applying a
// template preserves the options already selected in the task form.
func (d *DB) seedDefaultTaskTemplates() error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`INSERT INTO settings(key,value) VALUES ($1,'done') ON CONFLICT(key) DO NOTHING`, defaultTaskTemplatesSeededFlag)
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
	for _, entry := range defaultTaskTemplates {
		if _, err := tx.Exec(`INSERT INTO task_categories(name,nkey) VALUES($1,$2) ON CONFLICT(nkey) DO NOTHING`, entry.Category, taskTemplateNKey(entry.Category)); err != nil {
			return err
		}
		var categoryID int64
		if err := tx.QueryRow(`SELECT id FROM task_categories WHERE nkey=$1`, taskTemplateNKey(entry.Category)).Scan(&categoryID); err != nil {
			return err
		}
		if _, err := insertTaskTemplate(tx, TaskTemplateInput{
			Name: entry.Name, Description: entry.Description, Goal: entry.Goal, CategoryID: &categoryID,
		}); err != nil && !errors.Is(err, ErrTaskTemplateNameConflict) {
			return err
		}
	}
	return tx.Commit()
}

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
