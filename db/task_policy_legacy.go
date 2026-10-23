package db

// These are the released v1 constraints, kept only to migrate template IDs.
// They are not a selectable runtime policy or a current prompt catalog.
type legacyTaskConstraint struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}
type legacyTaskPolicy struct {
	Name         string                 `json:"name"`
	Instructions string                 `json:"instructions"`
	Constraints  []legacyTaskConstraint `json:"constraints"`
}

func legacyV1TaskPolicy(id string) *legacyTaskPolicy {
	switch id {
	case "lab":
		return &legacyTaskPolicy{Name: "실습 · 전체 검증", Constraints: []legacyTaskConstraint{{Kind: "allow", Text: "지정된 폐기 가능한 실습 대상에서는 인증·권한·서버측 실행·설정·데이터 변경/삭제 등 필요한 검증을 수행할 수 있으며 대상의 원상 복구는 필요하지 않습니다."}, {Kind: "deny", Text: "이 허용은 지정한 실습 대상에만 적용됩니다. ARTEX 호스트·다른 프로젝트·지정하지 않은 자산으로 대상을 넓히지 마세요."}}}
	case "observe":
		return &legacyTaskPolicy{Name: "운영 · 관찰", Constraints: []legacyTaskConstraint{{Kind: "allow", Text: "승인된 대상에서 페이지·문서·정상 조회 응답·제공된 소스와 로그를 관찰하고 분석하세요. 명시적으로 제공된 계정의 정상 로그인은 가능하며 인증 세션 생성과 접속 로그는 업무 데이터 변경과 구분합니다."}, {Kind: "deny", Text: "업무 데이터 생성/수정/삭제, 상태 변경을 목적으로 한 입력 전송, 파일 업로드, 코드 실행·설정 변경 및 부하/잠금 유발 검증을 하지 마세요. GET이나 페이지 스크립트라도 상태 변경이 예상되면 실행하지 마세요."}}}
	case "standard":
		return &legacyTaskPolicy{Name: "운영 · 일반 검증", Constraints: []legacyTaskConstraint{{Kind: "allow", Text: "승인 범위에서 능동 입력 검증, 인증/권한 비교, 테스트 계정·임시 데이터·테스트 파일의 생성/수정/삭제를 허용합니다. 정상 서비스 영향이 낮은 재현을 진행하고 만든 테스트 자료는 정리하세요."}, {Kind: "deny", Text: "실제 업무 자료 훼손·업무 설정 변경·서비스 중단·대량 부하·사용자 계정 잠금을 유발하지 마세요. 임시 자료와 실제 자료를 구분할 수 없으면 해당 변경을 실행하지 마세요."}}}
	case "extended":
		return &legacyTaskPolicy{Name: "운영 · 확장 검증", Constraints: []legacyTaskConstraint{{Kind: "allow", Text: "승인된 범위에서 권한 상승, 서버측 명령 실행, 업로드, 되돌릴 수 있는 설정/데이터 변경을 포함한 재현을 수행하세요. 변경 전 상태를 확인하고 변경 내용을 기록한 뒤 복구하세요. 시스템을 훼손하지 않는 증거만 확보하세요."}, {Kind: "deny", Text: "서비스 중단, 핵심 데이터의 회복 불가능한 손상, 무제한 자원 소모 및 승인 범위 밖의 작업을 하지 마세요."}}}
	}
	return nil
}
