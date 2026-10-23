package db

import "errors"

const defaultTaskTemplatesFlag = "default_task_templates_v1"

type defaultTaskTemplate struct {
	Category string
	Input    TaskTemplateInput
}

func defaultTaskTemplateSeeds() []defaultTaskTemplate {
	entries := []struct{ Name, Category, Description, Goal string }{
		{Name: "실습 · 전체 검증", Category: "실습",
			Description: `[테스트 대상과 계정/자료를 입력하세요]
테스트 대상: [주소·포트·허용 범위]
계정·자료: [제공 계정과 참고 자료]

검증 방식:
1. 작업 목표와 지정 실습 대상을 확인하고 정상 요청·계정·응답을 기준선으로 확보하세요. HTTP, 브라우저와 제공 자료로 진입점·인증 흐름·데이터 흐름을 파악하고 근거가 있는 가설부터 검증하세요.
2. 폐기 가능한 지정 대상에서는 입력 검증, 인증·권한 우회, 파일 처리, 서버측 실행과 업무 흐름을 적극적으로 재현하세요. 필요한 데이터 생성·변경·삭제와 설정 변경을 수행할 수 있으며 대상 복구는 생략합니다. 후속 검증을 막을 변경은 필요한 증거를 먼저 수집한 뒤 수행하세요.
3. 각 가설은 정상 요청과 비교하고 실제 응답·상태 변화로 판단하세요. 유효한 발견은 재현 가능한 최소 요청, 사용 계정·권한, 전제 조건, 관찰한 영향과 증거를 남기세요. 도구의 탐지 문구만으로 성공을 확정하지 마세요.
4. 중복 검증을 줄이고 확보한 세션·요청·발견을 다음 검증에 재사용하세요. 목표에 필요한 연결 검증은 지정 대상 안에서 이어가며 실패한 가설과 미검증 항목도 구분해 기록하세요.
5. 완료 시 확인한 문제·재현 절차·증거, 변경하거나 삭제한 대상 상태, 남은 검증 항목을 보고하세요. 복구하지 않은 상태를 명시하고 ARTEX 호스트와 다른 프로젝트의 자료는 정리 대상으로 삼지 마세요.`,
			Goal: "지정한 실습 대상에서 재현 가능한 취약점과 영향을 확인하고 재현 절차·증거·변경된 상태 및 남은 검증 항목을 정리한 최종 보고서를 작성하세요."},
		{Name: "운영 · 관찰", Category: "운영",
			Description: `[테스트 대상과 계정/자료를 입력하세요]
테스트 대상: [주소·포트·허용 범위]
계정·자료: [제공 계정과 참고 자료]

검증 방식:
1. 지정 대상·제공 계정·자료와 관찰 목표를 확인하세요. 정상 페이지와 조회 응답, 문서·소스·로그를 읽어 진입점·인증 흐름·데이터 노출을 분석하세요. 제공 계정의 정상 로그인과 필요한 세션 유지는 허용됩니다.
2. 정상 이용 중 발생하는 읽기 요청을 중심으로 응답·헤더·권한별 화면을 비교하세요. 브라우저 이동이나 페이지 스크립트 실행 전에 상태 변경 가능성을 살피고, 조회처럼 보여도 업무 데이터를 바꾸는 동작은 실행하지 마세요.
3. 입력 변조·쓰기·업로드·서버 실행이나 부하를 통한 재현이 필요한 가설은 근거를 수집한 뒤 미검증으로 남기세요. 실행하지 않은 공격이 성공했다고 결론짓지 마세요.
4. 관찰된 문제에는 URL·요청 조건·계정 권한·관련 응답 또는 코드·로그를 연결해 기록하세요. 같은 자료를 반복 수집하기보다 확인된 결과를 재사용하고 민감한 내용은 입증에 필요한 부분만 보고하세요.
5. 완료 시 직접 관찰해 확인한 사실과 추가 능동 검증이 필요한 가설을 구분하고, 확인하지 못한 범위와 이유를 정리하세요. 이 모드에서는 정리를 위한 업무 데이터 변경도 수행하지 않습니다.`,
			Goal: "지정 운영 대상에서 정상 조회와 제공 자료로 확인한 문제를 증거와 함께 정리하고, 능동 검증이 필요한 미확인 가설을 구분한 최종 보고서를 작성하세요."},
		{Name: "운영 · 일반 검증", Category: "운영",
			Description: `[테스트 대상과 계정/자료를 입력하세요]
테스트 대상: [주소·포트·허용 범위]
계정·자료: [제공 계정과 참고 자료]

검증 방식:
1. 지정 대상의 정상 응답과 제공 계정·권한을 파악하고 테스트 계정·임시 데이터·실제 업무 자료를 구분하세요. 필요한 최소 테스트 자료를 준비하고 생성한 항목을 기록하세요.
2. 입력 검증, 인증·권한 비교, 세션 처리와 업무 흐름을 능동적으로 테스트하세요. 테스트 계정·임시 객체·테스트 파일의 생성·조회·수정·삭제를 활용해 실제 영향을 확인하되 실제 업무 자료와 설정은 변경하지 마세요.
3. 가설마다 정상 요청과 검증 요청을 비교하고 필요한 최소 요청으로 재현하세요. 쓰기 요청을 재시도하기 전에는 이전 요청이 반영됐는지 확인해 중복 생성을 줄이세요. 서버측 명령 실행이나 업무 설정 변경이 필요한 검증은 이 모드에서 진행하지 마세요.
4. 확인한 문제에는 재현 요청, 전제 조건·권한, 응답과 임시 자료의 상태 변화를 증거로 연결하세요. 가설·도구 경고·재현 성공을 구분하고 효과가 없는 반복과 대량 요청을 피하세요.
5. 검증 후 자신이 만든 테스트 자료를 정리하고 정리 결과를 확인하세요. 완료 보고에는 발견·증거·재현 절차와 정리 완료 여부, 남은 임시 자료 및 추가 검증이 필요한 항목을 적으세요.`,
			Goal: "지정 운영 대상에서 서비스 영향이 낮은 능동 검증으로 취약점을 확인하고 재현 절차·증거와 테스트 자료 정리 결과를 포함한 최종 보고서를 작성하세요."},
		{Name: "운영 · 확장 검증", Category: "운영",
			Description: `[테스트 대상과 계정/자료를 입력하세요]
테스트 대상: [주소·포트·허용 범위]
계정·자료: [제공 계정과 참고 자료]

검증 방식:
1. 지정 대상·허용 범위와 정상 상태를 확인하고 진입점·인증·권한·데이터 흐름에서 검증 가설을 세우세요. 변경이 필요한 항목은 이전 값과 복구 방법을 먼저 확보하세요.
2. 승인 범위에서 권한 상승, 파일 업로드, 서버측 실행, 되돌릴 수 있는 설정·데이터 변경까지 검증하세요. 필요한 영향만 입증하며 지속 접근 수단이나 불필요한 변경을 남기지 마세요. 이전 상태를 확보하거나 되돌릴 수 없는 변경은 실행하지 마세요.
3. 변경은 영향을 구분할 수 있는 단위로 수행하고 요청·명령·권한·변경 전후 상태를 기록하세요. 정상 동작과 비교해 영향이 실제로 발생했는지 확인하고 관련 증거를 보존하세요.
4. 재현 뒤에는 변경한 데이터·설정·파일·권한을 이전 상태로 복구하고 정상 응답을 다시 확인하세요. 복구 실패나 예상하지 못한 서비스 영향이 생기면 추가 변경을 중지하고 현재 상태와 필요한 복구 작업을 명확히 남기세요.
5. 완료 시 발견·영향·재현 절차·증거와 항목별 복구 결과를 함께 보고하세요. 미검증 가설과 잔여 변경을 따로 적고 복구를 확인하지 못한 항목을 완료로 표시하지 마세요.`,
			Goal: "지정 운영 대상에서 승인 범위의 가역적 검증으로 취약점과 영향을 확인하고, 재현 절차·증거·항목별 복구 결과 및 잔여 변경을 정리한 최종 보고서를 작성하세요."},
	}
	out := make([]defaultTaskTemplate, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name
		timeout, heartbeat, seed, coverage := 0, MinPlanHeartbeatSeconds, false, true
		out = append(out, defaultTaskTemplate{Category: entry.Category, Input: TaskTemplateInput{
			Name: name, TaskName: &name, Description: entry.Description, Goal: entry.Goal,
			TimeoutSeconds: &timeout, PlanHeartbeatSeconds: &heartbeat, SeedFirstIntent: &seed, CoverageEnabled: &coverage,
		}})
	}
	return out
}

// A transaction-wide marker makes these ordinary editable rows a one-time seed.
// Reopening the application never restores a template the user deleted.
func (d *DB) seedDefaultTaskTemplates() error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`INSERT INTO settings(key,value) VALUES ($1,'done') ON CONFLICT(key) DO NOTHING`, defaultTaskTemplatesFlag)
	if err != nil {
		return err
	}
	added, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if added == 0 {
		return tx.Commit()
	}
	for _, seed := range defaultTaskTemplateSeeds() {
		name, key, err := normalizeTaskCategoryName(seed.Category)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO task_categories(name,nkey) VALUES ($1,$2) ON CONFLICT(nkey) DO NOTHING`, name, key); err != nil {
			return err
		}
		var category int64
		if err := tx.QueryRow(`SELECT id FROM task_categories WHERE nkey=$1`, key).Scan(&category); err != nil {
			return err
		}
		seed.Input.CategoryID = &category
		if _, err := insertTaskTemplate(tx, seed.Input); err != nil && !errors.Is(err, ErrTaskTemplateNameConflict) {
			return err
		}
	}
	return tx.Commit()
}
