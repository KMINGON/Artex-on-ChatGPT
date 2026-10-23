package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func TestTaskTemplateHTTPCRUD(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	s := New(context.Background(), m, t.TempDir(), t.TempDir(), t.TempDir())
	h := s.Handler()
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}

	do := func(method, path string, body any) (int, map[string]any) {
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(data))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	name := fmt.Sprintf("http-template-%d", time.Now().UnixNano())
	code, created := do(http.MethodPost, "/api/task-templates", map[string]any{
		"name": name, "description": "description", "goal": "goal",
		"task_name": "fixture task", "timeout_seconds": 3600, "plan_heartbeat_seconds": 900,
		"seed_first_intent": true, "coverage_enabled": false,
	})
	if code != http.StatusCreated {
		t.Fatalf("create status=%d body=%v", code, created)
	}
	id := int64(created["id"].(float64))
	t.Cleanup(func() { _, _ = m.pg.DeleteTaskTemplate(id) })
	for key, want := range map[string]any{"task_name": "fixture task", "timeout_seconds": float64(3600), "plan_heartbeat_seconds": float64(900), "seed_first_intent": true, "coverage_enabled": false} {
		if created[key] != want {
			t.Fatalf("template setting %s: got=%v want=%v", key, created[key], want)
		}
	}

	code, duplicate := do(http.MethodPost, "/api/task-templates", map[string]any{
		"name": "  " + name + "  ", "description": "other", "goal": "other",
	})
	if code != http.StatusConflict {
		t.Fatalf("duplicate status=%d body=%v", code, duplicate)
	}

	code, updated := do(http.MethodPatch, fmt.Sprintf("/api/task-templates/%d", id), map[string]any{"goal": "patched goal"})
	if code != http.StatusOK || updated["goal"] != "patched goal" || updated["description"] != "description" {
		t.Fatalf("patch status=%d body=%v", code, updated)
	}
	if updated["task_name"] != "fixture task" || updated["coverage_enabled"] != false {
		t.Fatal("partial template edit reset task settings")
	}
	code, updated = do(http.MethodPatch, fmt.Sprintf("/api/task-templates/%d", id), map[string]any{
		"task_name": "", "timeout_seconds": nil, "plan_heartbeat_seconds": nil, "seed_first_intent": nil, "coverage_enabled": nil,
	})
	if code != http.StatusOK || updated["task_name"] != "" || updated["timeout_seconds"] != nil || updated["plan_heartbeat_seconds"] != nil || updated["seed_first_intent"] != nil || updated["coverage_enabled"] != nil {
		t.Fatalf("explicit empty/null settings were lost: status=%d body=%v", code, updated)
	}
	for _, invalid := range []map[string]any{
		{"timeout_seconds": -1}, {"plan_heartbeat_seconds": -1},
		{"timeout_seconds": int64(2147483648)}, {"plan_heartbeat_seconds": int64(2147483648)},
		{"task_name": strings.Repeat("가", db.MaxTaskTemplateNameRunes+1)}, {"test_preset": "lab"},
	} {
		if status, _ := do(http.MethodPatch, fmt.Sprintf("/api/task-templates/%d", id), invalid); status != http.StatusBadRequest {
			t.Fatalf("invalid template edit accepted: %v status=%d", invalid, status)
		}
	}
	code, updated = do(http.MethodPatch, fmt.Sprintf("/api/task-templates/%d", id), map[string]any{
		"description": strings.Repeat("가", db.MaxTaskTemplateTextRunes+1),
	})
	if code != http.StatusOK || len([]rune(updated["description"].(string))) != db.MaxTaskTemplateTextRunes+1 {
		t.Fatalf("expanded description limit rejected: status=%d", code)
	}
	code, _ = do(http.MethodPatch, fmt.Sprintf("/api/task-templates/%d", id), map[string]any{
		"description": strings.Repeat("가", db.MaxTaskTemplateDescriptionRunes+1),
	})
	if code != http.StatusBadRequest {
		t.Fatalf("overlong description status=%d", code)
	}
	code, _ = do(http.MethodPatch, fmt.Sprintf("/api/task-templates/%d", id), map[string]any{
		"goal": strings.Repeat("界", db.MaxTaskTemplateTextRunes+1),
	})
	if code != http.StatusBadRequest {
		t.Fatalf("overlong patch status=%d, want %d", code, http.StatusBadRequest)
	}
	code, _ = do(http.MethodPost, "/api/task-templates", map[string]any{
		"name": "oversized", "description": strings.Repeat("x", maxTaskTemplateRequestBytes+1), "goal": "goal",
	})
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized create status=%d, want %d", code, http.StatusRequestEntityTooLarge)
	}
	code, listed := do(http.MethodGet, "/api/task-templates", nil)
	if code != http.StatusOK || listed["templates"] == nil {
		t.Fatalf("list status=%d body=%v", code, listed)
	}
	code, deleted := do(http.MethodDelete, fmt.Sprintf("/api/task-templates/%d", id), nil)
	if code != http.StatusOK || int64(deleted["deleted"].(float64)) != id {
		t.Fatalf("delete status=%d body=%v", code, deleted)
	}
}

func TestCreateTaskRejectsCachedPolicyChoice(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(`{"description":"fixture","goal":"inspect","test_preset":"lab"}`))
	response := httptest.NewRecorder()
	(&Server{}).createTask(response, req)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "error") {
		t.Fatalf("old policy choice was silently ignored: status=%d", response.Code)
	}
}

func TestConversationPatchReturnsPinState(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	s := New(context.Background(), m, t.TempDir(), t.TempDir(), t.TempDir())
	conversation, err := m.pg.CreateConversation("mainagent", "pin through http", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.pg.DeleteConversation(conversation.ID) })
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{"title": "  pinned title  ", "pinned": true})
	req := httptest.NewRequest(http.MethodPatch, fmt.Sprintf("/api/conversations/%d", conversation.ID), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", rec.Code, rec.Body.String())
	}
	var updated struct {
		Title    string  `json:"title"`
		Pinned   bool    `json:"pinned"`
		PinnedAt *string `json:"pinned_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Title != "pinned title" || !updated.Pinned || updated.PinnedAt == nil {
		t.Fatalf("unexpected conversation patch response: %+v", updated)
	}

	body, _ = json.Marshal(map[string]any{"title": strings.Repeat("会", maxConversationTitleRunes+1)})
	req = httptest.NewRequest(http.MethodPatch, fmt.Sprintf("/api/conversations/%d", conversation.ID), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("overlong conversation title status=%d body=%s", rec.Code, rec.Body.String())
	}

	body, _ = json.Marshal(map[string]any{"title": strings.Repeat("x", maxConversationRequestBytes+1)})
	req = httptest.NewRequest(http.MethodPatch, fmt.Sprintf("/api/conversations/%d", conversation.ID), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized conversation patch status=%d body=%s", rec.Code, rec.Body.String())
	}

	body, _ = json.Marshal(map[string]any{
		"agent_key": "mainagent", "title": strings.Repeat("会", maxConversationTitleRunes+1),
	})
	req = httptest.NewRequest(http.MethodPost, "/api/conversations", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("overlong conversation create status=%d body=%s", rec.Code, rec.Body.String())
	}

	body, _ = json.Marshal(map[string]any{
		"agent_key": "mainagent", "title": strings.Repeat("x", maxConversationRequestBytes+1),
	})
	req = httptest.NewRequest(http.MethodPost, "/api/conversations", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized conversation create status=%d body=%s", rec.Code, rec.Body.String())
	}
}
