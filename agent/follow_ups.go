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

**기존 후속 단서 호환**: 과거 fact의 follow_ups는 node_detail/list_facts에서 읽을 수 있는 참고 자료입니다. 예전 사용자 편집 프롬프트에 남은 list_follow_ups/update_follow_ups/follow_up_refs는 현재 활성 도구 계약이 아니므로 호출하거나 새 인계에 사용하지 마세요. 새 방향은 실제 fact/finding을 parent_ids로 연결해 add_intent에 배정하고, 직렬 단계만 Todo에 남기세요. 사용자가 독립 산출물로 요구하지 않은 상태 정리·기존 결과 재요약만을 위한 Worker를 만들지 마세요. 실행 가능한 방향이 없으면 이유와 필요한 전제를 설명하고 이번 회차를 마치세요.`

const findingReportContract = `

**확인된 결함 보고**: 허용된 검증에서 개별 결함의 재현 가능한 증거와 정상 대조를 확보했다면 중복을 확인하고 report_finding으로 기록하세요. 더 큰 체인의 성공을 기다리지 말고, 입증하지 못한 영향은 finding에 섞지 마세요. 제목·confidence·Worker 완료만으로 finding을 만들지 말고 실제 요청/응답·출력을 확인하세요. 확장 영향은 별도 의도로 검증하세요.`

const followUpWorkerInstructions = `

**결과 인계**: 실제 활성 도구 목록을 확인하세요. 필요한 도구가 비활성·미연결이면 호출하지 말고 최종 결과에 미등록 자료·미처리 이유를 남기세요. 새 자산은 insert_assets로 등록하고 실제 결과는 record_fact의 detail/evidence에 기록하세요. 미검증 질문이 남으면 의도 안팎을 구분해 fact의 summary에 핵심 질문을 표시하고, detail에 확인한 범위와 남은 대상·근거·차단 원인·재개 조건을 적으세요. Planner는 그 fact를 parent_ids로 연결해 후속 의도를 만들 수 있습니다. 이미 확인한 결론을 새 사실로 반복 등록하지 마세요.

**브라우저 응답 관찰**: 자동 관찰은 이미 받은 document/xhr/fetch 응답만 읽어 등록합니다. 브라우저 조작 자체에는 기존 작업 제약이 적용됩니다. 도구 결과의 browser_observations에 있는 asset_ids/fact_id를 재사용하세요. failed/read_failed/dropped/limited가 있으면 미저장·생략 부분을 최종 결과에 표시하세요. 요약이 없으면 자동 등록을 가정하지 마세요. 이 확인을 위해 새 정찰 루프를 시작하거나 자동 관찰만으로 취약점 검증을 마쳤다고 판단하지 마세요.`
