package server

import (
	"testing"

	"github.com/Autumn-27/artex/chatgptllm"
	"github.com/Autumn-27/artex/db"
)

func TestWorkerProgressTracksAndClosesActiveRun(t *testing.T) {
	e := &Engine{}
	p := e.beginWorkerProgress("task-1", "worker-1", 42)
	p.stream(chatgptllm.StreamProgress{CallID: 1, Phase: "waiting"})
	if got := e.WorkerProgress("task-1"); len(got) != 1 || got[0].Phase != "model_wait" {
		t.Fatalf("model wait: %+v", got)
	}
	p.stream(chatgptllm.StreamProgress{CallID: 1, Phase: "receiving"})
	p.activity(db.Activity{Kind: "tool_use", ToolUseID: "call-1", Tool: "browser_snapshot"})
	if got := e.WorkerProgress("task-1"); len(got) != 1 || got[0].Phase != "tool" || got[0].PendingTools != 1 {
		t.Fatalf("tool run: %+v", got)
	}
	p.activity(db.Activity{Kind: "tool_result", ToolUseID: "call-1"})
	p.stream(chatgptllm.StreamProgress{CallID: 1, Phase: "finished"})
	e.endWorkerProgress(p)
	p.stream(chatgptllm.StreamProgress{CallID: 2, Phase: "waiting"})
	if got := e.WorkerProgress("task-1"); len(got) != 0 {
		t.Fatalf("late progress restored closed run: %+v", got)
	}
}
