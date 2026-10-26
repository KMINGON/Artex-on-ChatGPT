package agent

// Batch items expose the same contract as the single-item form. Previously the
// empty object schema hid optional provenance fields from deferred-tool lookup.
func withBatchSchema(schema map[string]any, batch string, required ...string) map[string]any {
	props := schema["properties"].(map[string]any)
	fields := make(map[string]any, len(props)-1)
	for name, field := range props {
		if name != batch {
			fields[name] = field
		}
	}
	props[batch].(map[string]any)["items"] = obj(fields, required...)
	return schema
}

const followUpPlannerInstructions = `

**기존 후속 단서 호환**: 과거 fact의 follow_ups는 node_detail/list_facts에서 읽을 수 있는 참고 자료입니다. 예전 사용자 편집 프롬프트에 남은 list_follow_ups/update_follow_ups/follow_up_refs는 현재 활성 도구 계약이 아니므로 호출하거나 새 인계에 사용하지 마세요. 새 방향은 실제 fact/finding을 parent_ids로 연결해 add_intent에 배정하고, 배정하지 못한 질문과 필요한 선행 단계를 근거 ID와 함께 Todo에 남기세요. 사용자가 독립 산출물로 요구하지 않은 상태 정리·기존 결과 재요약만을 위한 Worker를 만들지 마세요. 전제 부족을 발견하면 허용 범위에서 이를 확보하거나 다른 방법으로 검증할 작업을 먼저 찾으세요. 모든 남은 질문이 실제 외부 차단으로 대기해야 할 때에는 미달성 상태와 재개 조건을 명시하세요.`

const findingReportContract = `

**확인된 결함 보고**: 허용된 검증에서 개별 결함의 재현 가능한 증거와 정상 대조를 확보했다면 중복을 확인하고 report_finding으로 기록하세요. 더 큰 체인의 성공을 기다리지 말고, 입증하지 못한 영향은 finding에 섞지 마세요. 제목·confidence·Worker 완료만으로 finding을 만들지 말고 실제 요청/응답·출력을 확인하세요. finding 등록은 의도의 종료 조건이 아닙니다. 같은 질문의 정상 대조와 실제 영향은 현재 Worker가 이어서 검증하고, 별도 완료 기준을 가진 독립 질문만 근거와 함께 Planner에게 인계하세요.`

const followUpWorkerInstructions = `

**결과 인계**: 실제 활성 도구 목록을 확인하세요. 필요한 도구가 비활성·미연결이면 호출하지 말고 최종 결과에 미등록 자료·미처리 이유를 남기세요. 새 자산은 insert_assets로 등록하고 실제 결과는 record_fact의 detail/evidence에 기록하세요. 미검증 질문이 남으면 의도 안팎을 구분해 fact의 summary에 핵심 질문을 표시하고, detail에 확인한 범위와 남은 대상·근거·차단 원인·재개 조건을 적으세요. Planner는 그 fact를 parent_ids로 연결해 후속 의도를 만들 수 있습니다. 같은 결론이라도 새 재현·대조 결과나 검증 범위가 추가되면 그 차이를 기록하여 인계하세요.

**브라우저 응답 관찰**: 자동 관찰은 이미 받은 document/xhr/fetch 응답만 읽어 등록합니다. 브라우저 조작 자체에는 기존 작업 제약이 적용됩니다. 도구 결과의 browser_observations에 있는 asset_ids/fact_id를 재사용하세요. failed/read_failed/dropped/limited가 있으면 미저장·생략 부분을 최종 결과에 표시하세요. 요약이 없으면 자동 등록을 가정하지 마세요. 검증에 필요한 증거가 누락되거나 잘렸다면 트래픽·파일 조회나 필요한 재요청으로 보완하세요. 단순 자동 등록 여부 확인만을 위해 별도 정찰을 반복할 필요는 없으며, 자동 관찰만으로 취약점 검증을 마쳤다고 판단하지 마세요.`
