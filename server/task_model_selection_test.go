package server

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

func TestMissingModelWaitsWithoutStartingEvenWhenConcurrencyDisabled(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	defer restoreConcurrencySetting(t, m)()
	if err := m.SetConcurrency(false, defaultConcurrencyLimit); err != nil {
		t.Fatal(err)
	}
	task, err := m.CreateTask("missing model", "wait for a model", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	available := false
	s := newAdmissionTestServer(m, func(*Task) bool { return available })
	queued, err := s.admitTask(task, "bootstrap")
	if err != nil || !queued || !task.Queued || s.engine.Started(task.ID) {
		t.Fatalf("missing model started: queued=%v err=%v", queued, err)
	}
	s.reconcileConcurrency()
	if !task.Queued || s.engine.Started(task.ID) {
		t.Fatal("disabled limit dequeued an unavailable task")
	}
	activities, _, err := task.Store.ActivityList(nil, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, activity := range activities {
		if activity.Summary == queuedNoLLMSummary {
			found = true
		}
		if activity.Kind == "round" || activity.Kind == "llm" {
			t.Fatalf("model activity before configuration: %+v", activity)
		}
	}
	if !found {
		t.Fatal("missing Korean configuration status")
	}
	// Recovery must preserve the original bootstrap mode and require no restart.
	available = true
	s.reconcileConcurrency()
	if task.Queued || s.engine.IsPaused(task.ID) {
		t.Fatal("recovered task stayed blocked")
	}
}

func TestMissingTaskProviderIsKoreanAndNeverRetried(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("no provider", "no calls", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	s := &Server{m: m}
	router := &taskLLMRuntime{s: s, taskID: task.ID, agentKey: "worker"}
	_, err = collectTaskLLMStream(router.Stream(context.Background(), llm.CompletionRequest{}))
	if err == nil || !strings.Contains(err.Error(), "사용 가능한 LLM 설정이 없습니다") {
		t.Fatalf("unhelpful configuration error: %v", err)
	}
	if isRetryableStreamError(err) || retryableWorkerModelError(harness.ReasonModelError, err) || isQuotaExhaustedError(err) {
		t.Fatalf("configuration error can retry/rotate: %v", err)
	}
}

func TestGoalsWithoutProviderKeepsOriginalGoalWithoutModelActivity(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("goals fallback", "https://fixture.invalid:3000/ 대상의 로그인 동작 확인", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	s := &Server{m: m, engine: NewEngine(m)}
	var activities []db.Activity
	goals := s.createGoals(context.Background(), task, func(a db.Activity) { activities = append(activities, a) })
	if len(goals) != 1 || goals[0].Text != task.Goal || len(activities) != 0 {
		t.Fatalf("fallback caused model run or changed goal: goals=%+v activities=%+v", goals, activities)
	}
}

func TestSpawnTaskInheritsConversationPinWithExplicitPrecedence(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	defer restoreConcurrencySetting(t, m)()
	if err := m.SetConcurrency(false, defaultConcurrencyLimit); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, name := range []string{"conversation", "parent", "explicit", "worker"} {
		id, err := m.pg.SaveProfile(&db.LLMProfile{Name: name, Model: "model-" + name, APIKey: "fixture"})
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	var tasks []*Task
	defer func() {
		_ = m.pg.SetAgentLLMProfile("worker", nil)
		for _, task := range tasks {
			_, _ = m.DeleteTask(task.ID, DeleteTaskOptions{})
		}
		for _, id := range ids {
			_ = m.pg.DeleteProfile(id)
		}
	}()
	parentID := ids["parent"]
	parent, err := m.CreateTask("pinned parent", "parent", &parentID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	tasks = append(tasks, parent)
	unbound, err := m.CreateTask("unbound parent", "parent", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	tasks = append(tasks, unbound)
	// Cancelled server context and unavailable fake resolver prevent model calls.
	s := newAdmissionTestServer(m, func(*Task) bool { return false })
	s.provByProfile = map[int64]*provEntry{}
	ctx := context.WithValue(context.Background(), conversationProfileKey{}, ids["conversation"])
	for _, tc := range []struct {
		name, parent    string
		explicit, want  int64
		executingParent bool
	}{
		{"conversation alone", "", 0, ids["conversation"], false},
		{"parent wins", parent.ID, 0, ids["parent"], false},
		{"unbound parent inherits conversation", unbound.ID, 0, ids["conversation"], false},
		{"explicit wins", parent.ID, ids["explicit"], ids["explicit"], false},
		{"executing parent wins when omitted", "", 0, ids["parent"], true},
		{"executing parent wins over other parent", unbound.ID, 0, ids["parent"], true},
		{"explicit still wins for executing parent", "", ids["explicit"], ids["explicit"], true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"description": tc.name, "goal": "test inheritance", "parent_ref": tc.parent}
			if tc.explicit > 0 {
				args["llm_profile_id"] = tc.explicit
			}
			raw, _ := json.Marshal(args)
			callCtx := ctx
			if tc.executingParent {
				id, _ := strconv.ParseInt(parent.ID, 10, 64)
				callCtx = agent.WithRunInfo(callCtx, agent.RunInfo{TaskID: id})
			}
			result, err := s.toolSpawnTask().Call(callCtx, raw, nil)
			if err != nil || result.IsError || len(result.Content) == 0 {
				t.Fatalf("spawn failed: %+v %v", result, err)
			}
			id := strings.TrimPrefix(result.Content[0].Text, "task created: ")
			task, ok := m.Task(id)
			if !ok {
				t.Fatalf("created task missing: %s", id)
			}
			tasks = append(tasks, task)
			state := task.llmStateSnapshot()
			if state.ProfileID == nil || *state.ProfileID != tc.want || state.ActiveID == nil || *state.ActiveID != tc.want {
				t.Fatalf("wrong inherited selection: %+v want=%d", state, tc.want)
			}
			stored, err := m.pg.GetTask(mustTaskID(t, id))
			if err != nil || stored.LLMProfileID == nil || *stored.LLMProfileID != tc.want {
				t.Fatalf("pin not persisted: %+v %v", stored, err)
			}
			if tc.executingParent && (task.ParentRef != parent.ID || stored.ParentRef != parent.ID) {
				t.Fatalf("parent was not inherited with its model: runtime=%s stored=%s", task.ParentRef, stored.ParentRef)
			}
			workerID := ids["worker"]
			if err := m.pg.SetAgentLLMProfile("worker", &workerID); err != nil {
				t.Fatal(err)
			}
			selected, err := s.resolveTaskRoleLLM(task, "worker")
			if err != nil || selected.ProfileID == nil || *selected.ProfileID != workerID {
				t.Fatalf("task inheritance overrode worker role: %+v %v", selected, err)
			}
			_ = m.pg.SetAgentLLMProfile("worker", nil)
		})
	}
	// An ordinary task tool context with no chat pin keeps global fallback.
	result, err := s.toolSpawnTask().Call(context.Background(), json.RawMessage(`{"goal":"global fallback"}`), nil)
	if err != nil || result.IsError {
		t.Fatalf("global spawn: %+v %v", result, err)
	}
	task, _ := m.Task(strings.TrimPrefix(result.Content[0].Text, "task created: "))
	tasks = append(tasks, task)
	if task.llmStateSnapshot().ProfileID != nil {
		t.Fatal("ordinary spawn unexpectedly acquired a pin")
	}
}

func TestSpawnTaskModelHintUpgradePreservesEditedSchema(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	s := &Server{m: m}
	previous, err := m.pg.GetTool("spawn_task")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if previous == nil {
			_, _ = m.pg.Exec(`DELETE FROM tools WHERE key='spawn_task'`)
		} else {
			bindings, _ := json.Marshal(previous.Agents)
			_ = m.pg.UpdateTool(previous.Key, previous.Description, previous.Schema, bindings, previous.Enabled)
		}
	}()
	tool := s.toolSpawnTask()
	schema, _ := json.Marshal(tool.InputSchema())
	if err := m.pg.SeedTool(tool.Name(), tool.Description(), schema, json.RawMessage(`[]`)); err != nil {
		t.Fatal(err)
	}
	for _, desc := range []string{
		"선택: 하위 작업 planner/worker의 LLM 설정 id(list_llm_profiles 참조). 생략하면 상위 작업을 상속하고, 없으면 전역 활성 설정을 사용합니다",
		"사용자 지정 모델 상속 설명",
	} {
		current := tool.InputSchema()
		property := current["properties"].(map[string]any)["llm_profile_id"].(map[string]any)
		property["description"], property["default"] = desc, 123
		raw, _ := json.Marshal(current)
		if err := m.pg.UpdateTool("spawn_task", "custom description", raw, json.RawMessage(`["worker"]`), false); err != nil {
			t.Fatal(err)
		}
		if err := s.refreshSpawnTaskModelHint(); err != nil {
			t.Fatal(err)
		}
		got, err := m.pg.GetTool("spawn_task")
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(got.Schema, &result); err != nil {
			t.Fatal(err)
		}
		updated := result["properties"].(map[string]any)["llm_profile_id"].(map[string]any)
		want := desc
		if desc != "사용자 지정 모델 상속 설명" {
			want = spawnTaskProfileDescription
		}
		if updated["description"] != want || updated["default"] != float64(123) || got.Enabled || got.Description != "custom description" || len(got.Agents) != 1 || got.Agents[0] != "worker" {
			t.Fatalf("upgrade changed customized fields: %+v %+v", got, updated)
		}
	}
}
