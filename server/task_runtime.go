package server

import "github.com/Autumn-27/artex/agent"

// TaskRuntimeDTO describes configuration at read time, not historical execution
// or provider-confirmed values. Browser availability does not assert Chromium health.
type TaskRuntimeDTO struct {
	Workers              int                          `json:"workers"`
	WorkerSource         string                       `json:"worker_source"`
	BrowserLimit         int                          `json:"browser_limit"`
	BrowserEnabled       *bool                        `json:"browser_enabled"`
	BrowserWorkerVisible *bool                        `json:"browser_worker_visible"`
	TrafficCapture       bool                         `json:"traffic_capture"`
	Models               map[string]taskLLMResolution `json:"models"`
	WorkerProgress       []agent.WorkerProgress       `json:"worker_progress"`
}

func (s *Server) taskRuntime(t *Task) *TaskRuntimeDTO {
	if s.m == nil || s.m.pg == nil {
		return nil
	}
	runtime := &TaskRuntimeDTO{
		Workers: s.m.Workers(), WorkerSource: "global", BrowserLimit: s.m.browserLimit(),
		TrafficCapture: s.m.TrafficEnabled(), Models: map[string]taskLLMResolution{},
	}
	if t.ExecutionProfile != nil && t.ExecutionProfile.Workers > 0 {
		runtime.Workers = t.ExecutionProfile.Workers
		runtime.WorkerSource = "task_snapshot"
	}
	if s.engine != nil {
		runtime.WorkerProgress = s.engine.WorkerProgress(t.ID)
	}
	var enabled, visible bool
	err := s.m.pg.QueryRow(`SELECT
EXISTS(SELECT 1 FROM mcp_servers WHERE name=$1 AND enabled),
EXISTS(SELECT 1 FROM mcp_servers m JOIN agent_visibility v ON v.resource_id=m.id
 JOIN agents a ON a.id=v.agent_id
 WHERE m.name=$1 AND m.enabled AND v.resource_kind='mcp' AND v.enabled AND a.key='worker')`, browserMCPName).Scan(&enabled, &visible)
	if err == nil {
		runtime.BrowserEnabled, runtime.BrowserWorkerVisible = &enabled, &visible
	}
	for _, role := range []string{"mainagent", "planner", "worker"} {
		if model, err := s.resolveTaskRoleLLM(t, role); err == nil {
			runtime.Models[role] = model
		}
	}
	return runtime
}
