package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestTaskTemplateFinalFormCreatesOrdinaryTask(t *testing.T) {
	if os.Getenv("ARTEX_PG_DSN") == "" {
		t.Skip("requires an explicitly configured isolated ARTEX_PG_DSN")
	}
	t.Setenv(adminPasswordEnv, "")
	t.Setenv(setupTokenEnv, "template-flow-test-setup-token")
	dir := t.TempDir()
	m, err := NewManager(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := New(ctx, m, dir, dir, dir)
	s.engine.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) { return nil, nil })
	// Admission and seeding are synchronous; background jobs and model calls are
	// unnecessary when checking the form-to-storage contract.
	cancel()
	t.Cleanup(func() {
		s.CloseBrowsers()
		_ = m.Close()
	})
	hash, err := s.reloadPassHash()
	if err != nil {
		t.Fatal(err)
	}
	token, err := signJWT(s.signingKey(hash))
	if err != nil {
		t.Fatal(err)
	}
	handler := s.Handler()
	request := func(method, path string, body any) map[string]any {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(encoded))
		req.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("%s %s: status=%d body=%s", method, path, recorder.Code, recorder.Body.String())
		}
		var result map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	category, err := m.pg.CreateTaskCategory("template-flow-" + suffix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.pg.DeleteTaskCategory(category.ID) })
	template := request(http.MethodPost, "/api/task-templates", map[string]any{
		"name": "template-flow-" + suffix, "task_name": "저장된 작업 이름",
		"description": "저장된 작업 설명", "goal": "저장된 목표", "category_id": nil,
		"timeout_seconds": 3600, "plan_heartbeat_seconds": 1200,
		"seed_first_intent": false, "coverage_enabled": true,
		"intercept_rules": []map[string]any{{
			"enabled": true, "action": "block", "kind": "exact_domain",
			"pattern": "template.invalid", "note": "저장된 규칙",
		}},
	})
	templateID := int64(template["id"].(float64))
	t.Cleanup(func() { _, _ = m.pg.DeleteTaskTemplate(templateID) })

	// The UI copies a template into editable fields. Only these final values are
	// sent to the ordinary create endpoint; no template ID or policy is attached.
	finalForm := map[string]any{
		"name": template["task_name"], "category_id": template["category_id"],
		"description": template["description"], "goal": template["goal"],
		"timeout_seconds":        template["timeout_seconds"],
		"plan_heartbeat_seconds": template["plan_heartbeat_seconds"],
		"seed_first_intent":      template["seed_first_intent"],
		"coverage_enabled":       template["coverage_enabled"],
		"intercept_rules":        template["intercept_rules"],
	}
	const finalName = "수정한 작업 이름"
	const finalDescription = "fixture.invalid의 지정 자료를 확인하고 관찰 결과를 기록하세요."
	const finalGoal = "최종 폼의 목표와 범위를 그대로 사용하세요."
	finalForm["name"], finalForm["category_id"] = finalName, category.ID
	finalForm["description"], finalForm["goal"] = finalDescription, finalGoal
	finalForm["timeout_seconds"], finalForm["plan_heartbeat_seconds"] = 30, 601
	finalForm["seed_first_intent"], finalForm["coverage_enabled"] = true, false
	finalForm["intercept_rules"] = []map[string]any{{
		"enabled": true, "action": "allow", "kind": "exact_domain",
		"pattern": "fixture.invalid", "note": "최종 폼의 규칙",
	}}
	created := request(http.MethodPost, "/api/tasks", finalForm)
	id, ok := created["id"].(string)
	if !ok {
		t.Fatalf("task ID missing: %v", created)
	}
	t.Cleanup(func() { _, _ = m.DeleteTask(id, DeleteTaskOptions{}) })
	numericID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"name": finalName, "description": finalDescription, "goal": finalGoal,
		"category_id": float64(category.ID), "coverage_enabled": false,
	} {
		if created[key] != want {
			t.Errorf("task response %s=%v, want %v", key, created[key], want)
		}
	}
	for _, key := range []string{"test_preset", "test_policy"} {
		if _, exists := created[key]; exists {
			t.Errorf("task response retained separate policy field %s", key)
		}
	}
	task, err := m.pg.GetTask(numericID)
	if err != nil || task == nil {
		t.Fatalf("read created task: task=%+v err=%v", task, err)
	}
	if task.Name != finalName || task.Description != finalDescription || task.Goal != finalGoal || task.CategoryID == nil || *task.CategoryID != category.ID || task.TimeoutSeconds != 30 || task.PlanHeartbeatSeconds != 601 || !task.Queued || task.CoverageEnabled {
		t.Fatalf("final form differed from stored offline task: %+v", task)
	}
	if task.ExecutionProfile == nil || task.ExecutionProfile.Workers < 1 {
		t.Fatal("resource snapshot was lost with the removed policy layer")
	}
	var description, goal string
	if err := m.pg.QueryRow(`SELECT description,goal FROM explorations WHERE id=$1`, task.ExplorationID).Scan(&description, &goal); err != nil {
		t.Fatal(err)
	}
	if description != finalDescription || goal != finalGoal {
		t.Fatalf("exploration brief changed: description=%q goal=%q", description, goal)
	}
	store := m.pg.Exploration(task.ExplorationID)
	originID, err := store.OriginFactID()
	if err != nil {
		t.Fatal(err)
	}
	origin, err := store.GetNode(originID)
	if err != nil || origin == nil {
		t.Fatalf("origin fact missing: %v", err)
	}
	var brief struct{ Description, Goal, Summary string }
	if err := json.Unmarshal(origin.Payload, &brief); err != nil {
		t.Fatal(err)
	}
	if brief.Description != finalDescription || brief.Goal != finalGoal || !strings.Contains(brief.Summary, finalGoal) {
		t.Fatalf("graph input did not use the final brief: %+v", brief)
	}
	intents, err := store.ListByKind(db.KindIntent, 10)
	if err != nil || len(intents) != 1 {
		t.Fatalf("seed_first_intent=true was not applied: count=%d err=%v", len(intents), err)
	}
	if !strings.Contains(string(intents[0].Payload), finalGoal) || !strings.Contains(string(intents[0].Payload), finalDescription) {
		t.Fatalf("seed intent used an earlier template brief: %s", intents[0].Payload)
	}
	rules, err := m.pg.Assets().ListTaskInterceptRules(numericID)
	if err != nil || len(rules) != 1 {
		t.Fatalf("task rules not stored: count=%d err=%v", len(rules), err)
	}
	if rule := rules[0]; !rule.Enabled || rule.Action != "allow" || rule.Kind != "exact_domain" || rule.Pattern != "fixture.invalid" || rule.Note != "최종 폼의 규칙" {
		t.Fatalf("stored rule differs from final form: %+v", rule)
	}
	var noPreset, noPolicy, noMigration bool
	if err := m.pg.QueryRow(`SELECT test_preset='',test_policy='null'::jsonb,test_policy_migrated_at IS NULL FROM tasks WHERE id=$1`, numericID).Scan(&noPreset, &noPolicy, &noMigration); err != nil {
		t.Fatal(err)
	}
	if !noPreset || !noPolicy || !noMigration {
		t.Fatal("creating from template fields created a separate policy snapshot")
	}
	unchanged, err := m.pg.GetTaskTemplate(templateID)
	if err != nil || unchanged == nil {
		t.Fatalf("read source template: %v", err)
	}
	if unchanged.Description != "저장된 작업 설명" || unchanged.Goal != "저장된 목표" || unchanged.TaskName == nil || *unchanged.TaskName != "저장된 작업 이름" || unchanged.TimeoutSeconds == nil || *unchanged.TimeoutSeconds != 3600 || unchanged.CategoryID != nil {
		t.Fatalf("editing task fields mutated their source template: %+v", unchanged)
	}
}
