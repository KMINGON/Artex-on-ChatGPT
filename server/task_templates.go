package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
)

const maxTaskTemplateRequestBytes = 512 << 10

// 작업 템플릿 API 가 사용자에게 돌려주는 응답 문구. 식별자, JSON 필드명, 요청 본문
// 키는 원문을 유지하고 사람이 읽는 메시지만 한국어로 둔다.
const (
	errTaskTemplateRequestTooLarge = "요청 본문이 너무 큽니다"
	errTaskTemplateNameConflict    = "이미 존재하는 템플릿 이름입니다"
	errTaskTemplateRuleInvalid     = "가로채기/허용 규칙이 올바르지 않습니다: "
	errTaskTemplateNoFields        = "변경할 작업 템플릿 설정을 하나 이상 제공해야 합니다"
	errTaskTemplateFieldTooLongFmt = "%s 항목은 최대 %d자까지 입력할 수 있습니다"
	errLegacyTaskPreset            = "이전 작업 프리셋 방식은 더 이상 사용하지 않습니다. 화면을 새로고침한 뒤 작업 템플릿을 선택해 주세요"
	errTemplateTimeoutRange        = "작업 시간 제한은 0~2147483647초 사이여야 합니다"
	errTemplateHeartbeatRange      = "계획 주기는 0~2147483647초 사이여야 합니다"
)

// taskTemplateRequest uses pointers so PATCH can distinguish omitted fields from
// explicit empty values. Empty values are still rejected by the DB validator.
// category_id / intercept_rules presence is detected via the raw key set (see
// decodeTaskTemplateRequest) so a PATCH can clear category to null.
type taskTemplateRequest struct {
	// Reject cached clients instead of silently dropping their old policy choice.
	LegacyTestPreset     string                 `json:"test_preset,omitempty"`
	TaskName             *string                `json:"task_name"`
	TimeoutSeconds       *int                   `json:"timeout_seconds"`
	PlanHeartbeatSeconds *int                   `json:"plan_heartbeat_seconds"`
	SeedFirstIntent      *bool                  `json:"seed_first_intent"`
	CoverageEnabled      *bool                  `json:"coverage_enabled"`
	Name                 *string                `json:"name"`
	Description          *string                `json:"description"`
	Goal                 *string                `json:"goal"`
	CategoryID           *int64                 `json:"category_id"`
	InterceptRules       []taskInterceptRuleReq `json:"intercept_rules"`
}

// decodeTaskTemplateRequest decodes the body into req and returns the set of
// top-level keys present in the JSON (for PATCH presence detection).
func decodeTaskTemplateRequest(w http.ResponseWriter, r *http.Request, req *taskTemplateRequest) (map[string]struct{}, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxTaskTemplateRequestBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, errTaskTemplateRequestTooLarge)
		} else {
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return nil, false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	if err := json.Unmarshal(body, req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	present := make(map[string]struct{}, len(raw))
	for k := range raw {
		present[k] = struct{}{}
	}
	return present, true
}

func validateTaskTemplateRequest(req taskTemplateRequest) error {
	if req.LegacyTestPreset != "" {
		return errors.New(errLegacyTaskPreset)
	}
	if req.TimeoutSeconds != nil && (*req.TimeoutSeconds < 0 || *req.TimeoutSeconds > db.MaxTaskTemplateSeconds) {
		return errors.New(errTemplateTimeoutRange)
	}
	if req.PlanHeartbeatSeconds != nil && (*req.PlanHeartbeatSeconds < 0 || *req.PlanHeartbeatSeconds > db.MaxTaskTemplateSeconds) {
		return errors.New(errTemplateHeartbeatRange)
	}
	checks := []struct {
		name  string
		value *string
		limit int
	}{
		{name: "name", value: req.Name, limit: db.MaxTaskTemplateNameRunes},
		{name: "task_name", value: req.TaskName, limit: db.MaxTaskTemplateNameRunes},
		{name: "description", value: req.Description, limit: db.MaxTaskTemplateDescriptionRunes},
		{name: "goal", value: req.Goal, limit: db.MaxTaskTemplateTextRunes},
	}
	for _, check := range checks {
		if check.value == nil {
			continue
		}
		value := strings.TrimSpace(*check.value)
		if check.name == "name" {
			value = strings.Join(strings.Fields(value), " ")
		}
		if utf8.RuneCountInString(value) > check.limit {
			return fmt.Errorf(trMsg(errTaskTemplateFieldTooLongFmt), check.name, check.limit)
		}
	}
	return nil
}

func writeTaskTemplateErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrTaskTemplateInvalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, db.ErrTaskTemplateNameConflict):
		writeErr(w, http.StatusConflict, errTaskTemplateNameConflict)
	case errors.Is(err, db.ErrTaskTemplateNotFound):
		writeErr(w, http.StatusNotFound, "task template not found")
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) pgListTaskTemplates(w http.ResponseWriter, _ *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	templates, err := pg.ListTaskTemplates()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": templates})
}

func (s *Server) pgCreateTaskTemplate(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req taskTemplateRequest
	if _, ok := decodeTaskTemplateRequest(w, r, &req); !ok {
		return
	}
	if err := validateTaskTemplateRequest(req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rules, err := buildTaskInterceptRules(req.InterceptRules)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errTaskTemplateRuleInvalid+err.Error())
		return
	}
	template, err := pg.CreateTaskTemplate(db.TaskTemplateInput{
		TaskName:             req.TaskName,
		TimeoutSeconds:       req.TimeoutSeconds,
		PlanHeartbeatSeconds: req.PlanHeartbeatSeconds,
		SeedFirstIntent:      req.SeedFirstIntent,
		CoverageEnabled:      req.CoverageEnabled,
		Name:                 stringValue(req.Name),
		Description:          stringValue(req.Description),
		Goal:                 stringValue(req.Goal),
		CategoryID:           req.CategoryID,
		InterceptRules:       rules,
	})
	if err != nil {
		writeTaskTemplateErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, template)
}

func (s *Server) pgUpdateTaskTemplate(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad task template id")
		return
	}
	var req taskTemplateRequest
	present, ok := decodeTaskTemplateRequest(w, r, &req)
	if !ok {
		return
	}
	if err := validateTaskTemplateRequest(req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_, catPresent := present["category_id"]
	_, rulesPresent := present["intercept_rules"]
	_, taskNamePresent := present["task_name"]
	_, timeoutPresent := present["timeout_seconds"]
	_, heartbeatPresent := present["plan_heartbeat_seconds"]
	_, seedPresent := present["seed_first_intent"]
	_, coveragePresent := present["coverage_enabled"]
	if req.Name == nil && req.Description == nil && req.Goal == nil && !catPresent && !rulesPresent && !taskNamePresent && !timeoutPresent && !heartbeatPresent && !seedPresent && !coveragePresent {
		writeErr(w, http.StatusBadRequest, errTaskTemplateNoFields)
		return
	}
	patch := db.TaskTemplatePatch{
		Name: req.Name, Description: req.Description, Goal: req.Goal,
		TaskName: req.TaskName, SetTaskName: taskNamePresent,
		TimeoutSeconds: req.TimeoutSeconds, SetTimeoutSeconds: timeoutPresent,
		PlanHeartbeatSeconds: req.PlanHeartbeatSeconds, SetPlanHeartbeatSeconds: heartbeatPresent,
		SeedFirstIntent: req.SeedFirstIntent, SetSeedFirstIntent: seedPresent,
		CoverageEnabled: req.CoverageEnabled, SetCoverageEnabled: coveragePresent,
	}
	if catPresent {
		patch.SetCategoryID = true
		patch.CategoryID = req.CategoryID
	}
	if rulesPresent {
		rules, err := buildTaskInterceptRules(req.InterceptRules)
		if err != nil {
			writeErr(w, http.StatusBadRequest, errTaskTemplateRuleInvalid+err.Error())
			return
		}
		patch.SetInterceptRules = true
		patch.InterceptRules = rules
	}
	template, err := pg.PatchTaskTemplate(id, patch)
	if err != nil {
		writeTaskTemplateErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, template)
}

func (s *Server) pgDeleteTaskTemplate(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad task template id")
		return
	}
	deleted, err := pg.DeleteTaskTemplate(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !deleted {
		writeErr(w, http.StatusNotFound, "task template not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
