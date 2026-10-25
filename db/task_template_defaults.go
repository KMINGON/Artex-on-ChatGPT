package db

import "errors"

const taskTemplateGraphHandoff = `

공통 탐색·인계 기준:
허용 범위에서 대상의 진입점·역할·상태와 적용 가능한 취약점 유형을 살피고, 확인한 사실에서 파생된 가설도 검토하세요. 실행하지 못한 항목은 근거·재개 조건을 남기고 성공으로 세지 마세요.
실제 관찰은 자산과 fact/finding에 연결하고, 다음 검증 방향은 근거 fact/finding의 ID와 함께 인계하세요. Planner는 그 ID를 add_intent.parent_ids에 연결하고 직렬 단계만 Todo에 남기세요. 공유 파일이 필요하면 system의 task_root/shared 아래에 저장하고 절대 경로를 인계하세요. 이미 그래프에 있는 결과를 매 의도마다 대장에 다시 쓰지 마세요. 자산 관찰 비율은 취약점 검증 완료율이 아닙니다.`

const taskTemplateCompletionReview = `

모든 실행 가능한 미검증 방향을 검토하고 목표의 상세 근거와 남은 단서를 대조한 뒤 완료를 판단하세요. prove_goal의 reason에는 목표별 근거를 남기고, 마지막 목표를 달성할 때 활성 의도·미처리 단서·미완료 Todo가 남아 있다면 completion_review에 남은 작업과 목표의 관계 및 종료 근거를 명시하세요. 광범위 탐색에서 실행 가능한 방향이 남으면 목표를 met로 만들지 마세요. 근거 있는 범위 제외·외부 조건 차단만 남으면 그 한계와 재개 조건을 결산할 수 있지만 해당 검증이 성공한 것은 아닙니다. 시간 제한·사용자 중단은 부분 결과로 구분하세요.`

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

핵심 지침:
허용 범위에서 발견 가능한 취약점을 넓고 깊게 조사하고 파생 가설과 취약점 간 연결까지 검증하세요. 첫 발견이나 보고서 초안으로 탐색을 끝내지 마세요. 개요의 설명이 생략되면 계획·실행·완료 판정 전에 node_detail(task.id)로 원문을 확인하세요.
Planner는 남은 방향을 add_intent로 배정하고, Worker는 할당된 의도 밖의 새 단서를 record_fact로 전달하세요. 선행 결과가 필요한 체인은 TodoWrite로 단계를 유지하세요.
일반 검증은 정상 조회·인증과 테스트 계정·임시 객체·테스트 파일의 생성·수정·삭제를 허용합니다. 실제 업무 자료·설정은 변경하지 말고 서버측 명령 실행·서비스 중단·대량 부하를 유발하는 검증은 수행하지 마세요. 이 허용 문구는 도구 가로채기 규칙을 해제하지 않습니다. 차단되면 원인과 필요한 조치를 기록하고 같은 요청을 반복하거나 차단을 우회하지 마세요.

탐색과 증거:
1. 기존 HTTP·파일·그래프 근거를 재사용해 실제 화면·JavaScript·API·역할·입력·상태 전이를 확인하세요. 동적 화면이나 사용자 인증을 직접 확인해야 할 때 제공된 브라우저를 사용하고 HTTP 관찰과 연결하세요. 확인하지 못한 동적 동작은 구분하세요. 새 진입점은 검증 범위에 추가하고 서로 다른 탐색 방향을 유지하세요.
2. 각 공격면에서 인증·세션·계정 복구, 객체·기능별 권한, 입력 검증·주입, 브라우저 스크립트, 파일·경로 처리, 요청 위조·교차 출처, 정보 노출·보안 설정, 업무 흐름 등 적용 가능한 유형을 검토하세요. 이 목록을 탐색 상한으로 삼지 마세요. 가설마다 정상 요청과 검증 요청의 응답·상태 변화를 비교하고, 적용 불가 판단에도 근거를 남기세요. 오류 메시지만으로 주입 성공이나 데이터 접근을 확정하지 마세요.
3. 확인한 사실에서 다른 진입점·역할·상태와 결합할 수 있는 가설을 도출하세요. Planner는 관련 fact/finding을 parent_ids로 연결해 후속 의도를 배정하고, Worker는 허용된 범위에서 해당 단계의 전제와 결과를 확인하세요. 연결이 입증되지 않은 단계는 성공한 체인으로 기록하지 마세요. 유망한 경로를 깊게 검증하면서 아직 다루지 않은 다른 공격면도 놓치지 마세요.
4. 새 자산은 insert_assets, 관찰·부정적 결과·후속 단서는 record_fact, 실제 재현된 취약점은 report_finding으로 기록하세요. 한 의도의 관련 관찰은 fact 하나의 detail에 모으고 실제 관찰과 추론을 구분하세요. 근본 원인이 같은 발견은 중복 등록하지 말고 영향받는 객체·역할·전제의 차이를 보존하세요. 트래픽이 수집된 경우 결론을 뒷받침하는 실제 기록을 연결하세요.
5. Planner는 기존 그래프와 TodoWrite로 결과와 남은 방향을 관리하세요. 공유 대장이 필요한 경우 새 결과가 모였을 때만 갱신하고 변경분·근거 ID를 인계하세요. 일반 관찰마다 별도 보고서 의도를 만들지 마세요. 최종 판정에는 관련 상세 근거를 확인하세요.

장애와 마무리:
검증 실패, 아직 시도하지 않음, 도구·인증·환경 차단, 허용 범위 밖을 구분하세요. 같은 오류는 원인과 대체 수단을 확인하고 새로운 전제나 방법이 없으면 재배정하지 마세요. 예를 들어 Python 의존성 오류는 이미 있는 HTTP 도구·curl·표준 라이브러리로 대체할 수 있는지 확인하세요. 차단 사유를 기록하는 동안 다른 실행 가능한 방향은 계속 검증하세요.
테스트 자료는 생성 시 출처와 식별자를 기록하고 필요한 후속 검증 뒤 직접 만든 항목만 정리하세요. 정리 완료·플랫폼 차단·사용자 조치 필요를 구분하고 삭제 실패를 복구 완료로 표현하지 마세요. 인증·도구·정책 변경이 필요한 잔여 항목에는 재개 조건을 남기세요. 모든 실행 가능한 방향이 소진되면 범위 한계와 잔여 상태를 정확히 결산하고, 같은 막힘을 해결된 것처럼 쓰거나 무한히 재확인하지 마세요.`,
			Goal: `지정 대상에서 일반 검증의 허용 범위 안에 있는 취약점과 파생·연결 가능성을 최대한 폭넓게 확인하고 다음 최종 결과를 확보하세요. 목표 분해는 검증 가능한 결과를 기준으로 하며 정찰·실행 단계나 임의의 목표 개수를 강제하지 않습니다.

1. 범위별 검증 결과: 실제로 식별한 화면·API·역할·업무 흐름과 적용 가능한 검증 유형을 대조한 대장. 각 항목에 관련 fact/finding ID, 관찰 결과, 아직 시도하지 않은 방향, 적용 불가 또는 차단의 근거와 재개 조건을 남깁니다.
2. 취약점과 체이닝 결과: 재현 조건·정상 대조·증거·관찰한 영향이 연결된 발견 목록과, 그 발견에서 파생된 가설·연결 단계의 검증 결과. 발견 수를 채우기 위해 미입증 가설을 확정하지 않습니다.
3. 최종 결산: 위 결과와 테스트 자료 정리 상태를 연결한 보고서. 실제 정리 완료와 실패·사용자 조치 필요를 구분하고 검증하지 못한 범위를 명시합니다.

완료 판정은 Planner가 수행합니다. 설명이 생략됐으면 node_detail(task.id)로 원문을 확인하고, 각 목표의 상세 fact/finding을 검증 대장과 대조한 뒤 prove_goal을 호출하세요. 보고서의 존재나 하나의 포괄적 요약만으로 모든 목표를 달성 처리하지 마세요. 새 결과가 모이면 대장을 갱신하고, 현재 조건에서 실행 가능한 미검증 항목·후속 의도·연결 단계가 남아 있는 동안 탐색 목표는 미달성입니다. 개요의 자산 커버리지 수치를 전체 취약점 검증률로 간주하지 마세요.
남은 항목이 근거가 확인된 범위 제외 또는 외부 조건 차단뿐이고 대체 가능한 검증 방향도 없다면 그 한계와 재개 조건을 포함해 허용 범위의 결과를 결산할 수 있습니다. 이는 차단된 검증의 성공이나 정리 완료를 뜻하지 않습니다. 시간 제한·사용자 중단으로 실행 가능한 검증을 남긴 경우에는 부분 결과로 보고하고 미달성 목표를 억지로 met로 바꾸지 마세요.`},
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
			Name: name, TaskName: &name, Description: entry.Description + taskTemplateGraphHandoff, Goal: entry.Goal + taskTemplateCompletionReview,
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
