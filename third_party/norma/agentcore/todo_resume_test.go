package agentcore

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

func resumeTodoCall(id, content string) llm.Message {
	input, _ := json.Marshal(map[string]any{"todos": []tool.Todo{{Content: content, Status: tool.TodoInProgress}}})
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: id, Name: "TodoWrite", Input: input}}}
}

func TestResumeRestoresTodoBeforeFirstRequestAfterCompaction(t *testing.T) {
	store := transcript.NewStore(t.TempDir())
	initial := tool.NewTodoStore()
	calls := 0
	session := NewSession(Options{
		SessionID: "todo-resume", Transcript: store, Todos: initial,
		Tools: []tool.CoreTool{}, DisableBackgroundTasks: true, PermissionMode: permission.ModeBypass,
		Provider: fixtureProvider(func(req llm.CompletionRequest) llm.Message {
			calls++
			if calls == 1 {
				return resumeTodoCall("original-write", "remaining authorization comparison")
			}
			return textProvider("paused with work left")(req)
		}),
	})
	drainPrompt(t, session, "investigate")
	if len(initial.List()) != 1 {
		t.Fatal("fixture did not execute TodoWrite")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	records, err := store.ResumeRecords(session.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	w := store.ResumeWriter(session.SessionID(), transcript.LastUUID(records))
	w.RecordCheckpoint([]llm.Message{llm.BoundaryMessage(llm.BoundaryMeta{}), llm.UserText("compressed context without the structured plan"), llm.UserText("recent tail")})
	// A failed later write and an interrupted write must not replace the last
	// acknowledged list. The retained checkpoint hides the successful raw call.
	w.RecordMessage(resumeTodoCall("failed", "incorrect failed replacement"), llm.Usage{})
	w.RecordMessage(llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText("failed", "cancelled", true)}}, llm.Usage{})
	w.RecordMessage(resumeTodoCall("interrupted", "incorrect interrupted replacement"), llm.Usage{})
	if err := w.Err(); err != nil {
		t.Fatal(err)
	}
	resumedTodos := tool.NewTodoStore()
	requests := 0
	resumed := NewSession(Options{
		SessionID: "todo-resume", Transcript: store, Todos: resumedTodos,
		Tools: []tool.CoreTool{}, DisableBackgroundTasks: true, PermissionMode: permission.ModeBypass,
		Provider: fixtureProvider(func(req llm.CompletionRequest) llm.Message {
			requests++
			seen := false
			for _, message := range req.Messages {
				if llm.IsTodoReminder(message) && strings.Contains(message.Text(), "remaining authorization comparison") {
					seen = true
				}
				for _, use := range message.ToolUses() {
					if use.ID == "original-write" {
						t.Fatal("fixture did not exercise a successful write hidden by compaction")
					}
				}
			}
			if !seen {
				t.Fatal("first resumed provider request did not receive the restored unfinished plan")
			}
			return textProvider("continuing the remaining comparison")(req)
		}),
	})
	defer resumed.Close()
	if err := resumed.Resume(session.SessionID()); err != nil {
		t.Fatal(err)
	}
	if got := resumedTodos.List(); len(got) != 1 || got[0].Content != "remaining authorization comparison" {
		t.Fatalf("raw acknowledged plan not restored: %+v", got)
	}
	drainPrompt(t, resumed, "continue")
	if requests != 1 {
		t.Fatalf("requests=%d", requests)
	}
	// The recovery reminder is an ordinary durable birth, so another cold start
	// sees it without creating or executing an additional TodoWrite.
	records, err = store.ResumeRecords(session.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	reminders := 0
	for _, record := range records {
		if record.Message != nil && llm.IsTodoReminder(*record.Message) {
			reminders++
		}
	}
	if reminders != 1 {
		t.Fatalf("restored plan reminder was not persisted exactly once: %d", reminders)
	}
}

func TestResumeFailureDoesNotChangeInMemoryTodoPlan(t *testing.T) {
	store := transcript.NewStore(t.TempDir())
	if err := os.WriteFile(store.MainPath("corrupt"), []byte("{broken\n"), 0600); err != nil {
		t.Fatal(err)
	}
	todos := tool.NewTodoStore()
	if result, err := todos.Tool().Call(t.Context(), []byte(`{"todos":[{"content":"existing","status":"pending"}]}`), nil); err != nil || result.IsError {
		t.Fatalf("seed: %+v %v", result, err)
	}
	session := NewSession(Options{SessionID: "corrupt", Transcript: store, Todos: todos, Tools: []tool.CoreTool{}, DisableBackgroundTasks: true})
	if err := session.Resume("corrupt"); err == nil {
		t.Fatal("corrupt transcript accepted")
	}
	if got := todos.List(); len(got) != 1 || got[0].Content != "existing" {
		t.Fatalf("failed resume changed current todos: %+v", got)
	}
}
