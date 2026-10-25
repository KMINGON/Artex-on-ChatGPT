package server

import (
	"sort"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/chatgptllm"
	"github.com/Autumn-27/artex/db"
)

type workerProgressState struct {
	mu     sync.Mutex
	taskID string
	value  agent.WorkerProgress
	closed bool
	models map[uint64]string
	tools  map[string]string
}

func (e *Engine) beginWorkerProgress(taskID, worker string, intentID int64) *workerProgressState {
	now := time.Now().Unix()
	p := &workerProgressState{taskID: taskID, value: agent.WorkerProgress{IntentID: intentID, Worker: worker, Phase: "preparing", StartedUnix: now, PhaseSinceUnix: now, LastProgressUnix: now}, models: map[uint64]string{}, tools: map[string]string{}}
	e.workerProgress.Store(intentID, p)
	return p
}

func (e *Engine) endWorkerProgress(p *workerProgressState) {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	// A late old run must not erase a newer run for the same intent.
	e.workerProgress.CompareAndDelete(p.value.IntentID, p)
}

func (p *workerProgressState) setPhase(phase string) {
	if p.value.Phase != phase {
		p.value.Phase = phase
		p.value.PhaseSinceUnix = time.Now().Unix()
	}
}

func (p *workerProgressState) recalculate() {
	p.value.PendingTools = len(p.tools)
	p.value.Tool = ""
	if len(p.tools) > 0 {
		p.setPhase("tool")
		// Stable selection for parallel calls; count still exposes every pending tool.
		for _, name := range p.tools {
			if p.value.Tool == "" || name < p.value.Tool {
				p.value.Tool = name
			}
		}
		return
	}
	phase := "processing"
	var latest uint64
	for id, state := range p.models {
		if id > latest {
			latest = id
			phase = "model_" + state
		}
	}
	p.setPhase(phase)
}

func (p *workerProgressState) stream(event chatgptllm.StreamProgress) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	switch event.Phase {
	case "waiting":
		p.models[event.CallID] = "wait"
	case "receiving":
		if _, ok := p.models[event.CallID]; !ok {
			return
		}
		p.models[event.CallID] = "stream"
	case "finished":
		if _, ok := p.models[event.CallID]; !ok {
			return
		}
		delete(p.models, event.CallID)
	default:
		return
	}
	p.value.LastProgressUnix = time.Now().Unix()
	p.recalculate()
}

func (p *workerProgressState) activity(a db.Activity) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	switch a.Kind {
	case "tool_use":
		p.tools[a.ToolUseID] = a.Tool
	case "tool_result":
		delete(p.tools, a.ToolUseID)
	case "result":
		p.value.LastProgressUnix = time.Now().Unix()
		p.setPhase("settling")
		return
	}
	p.value.LastProgressUnix = time.Now().Unix()
	p.recalculate()
}

func (e *Engine) WorkerProgress(taskID string) []agent.WorkerProgress {
	items := make([]agent.WorkerProgress, 0)
	e.workerProgress.Range(func(_, value any) bool {
		p := value.(*workerProgressState)
		p.mu.Lock()
		if !p.closed && p.taskID == taskID {
			snapshot := p.value
			snapshot.IdleSeconds = max(time.Now().Unix()-snapshot.LastProgressUnix, 0)
			items = append(items, snapshot)
		}
		p.mu.Unlock()
		return true
	})
	sort.Slice(items, func(i, j int) bool { return items[i].IntentID < items[j].IntentID })
	return items
}
