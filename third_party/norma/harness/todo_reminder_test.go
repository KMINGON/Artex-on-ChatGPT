package harness

import (
	"context"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/tool"
)

func asst(text string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(text)}}
}

func todoWriteAsst() llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
		{Type: llm.BlockToolUse, ID: "t1", Name: "TodoWrite", Input: []byte(`{}`)},
	}}
}

func TestTodoReminderTurnCounts(t *testing.T) {
	msgs := []llm.Message{
		llm.UserText("go"),
		todoWriteAsst(), // the write turn — not counted
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText("t1", "ok", false)}},
		asst("thinking about it"), // +1 since write
		llm.UserText("more"),
		asst("still working"), // +1 since write
	}
	sinceWrite, sinceReminder := todoReminderTurnCounts(msgs)
	if sinceWrite != 2 {
		t.Fatalf("sinceWrite=%d, want 2", sinceWrite)
	}
	// No reminder in history → counts every assistant turn (3: two text + the write turn).
	if sinceReminder != 3 {
		t.Fatalf("sinceReminder=%d, want 3", sinceReminder)
	}

	// Insert a reminder; sinceReminder should count only assistant turns after it.
	withReminder := append([]llm.Message{}, msgs...)
	withReminder = append(withReminder, llm.TodoReminderMessage("1. [pending] x"), asst("after reminder"))
	_, sinceReminder = todoReminderTurnCounts(withReminder)
	if sinceReminder != 1 {
		t.Fatalf("sinceReminder after a reminder=%d, want 1", sinceReminder)
	}
}

func TestMaybeInjectTodoReminder(t *testing.T) {
	store := tool.NewTodoStore()

	// Disabled / empty list → never injects.
	l := &loop{in: QueryInput{}}
	l.maybeInjectTodoReminder()
	if len(l.messages) != 0 {
		t.Fatal("no injection when Todos is nil")
	}
	l = &loop{in: QueryInput{Todos: store}}
	l.maybeInjectTodoReminder()
	if len(l.messages) != 0 {
		t.Fatal("no injection for an empty todo list")
	}

	// Seed a list; 10 assistant turns with no TodoWrite → injects.
	store.Tool().Call(context.Background(), []byte(`{"todos":[{"content":"build","status":"in_progress"}]}`), &tool.ToolContext{})
	l = &loop{in: QueryInput{Todos: store}}
	for i := 0; i < 10; i++ {
		l.messages = append(l.messages, asst("work"))
	}
	l.maybeInjectTodoReminder()
	if len(l.messages) != 11 || !llm.IsTodoReminder(l.messages[10]) {
		t.Fatalf("expected a todo reminder appended, got %d msgs", len(l.messages))
	}
	if !strings.Contains(l.messages[10].Text(), "build") {
		t.Fatalf("reminder should carry the current list: %q", l.messages[10].Text())
	}

	// Immediately calling again → sinceReminder=0 < gap → no second injection.
	l.maybeInjectTodoReminder()
	if len(l.messages) != 11 {
		t.Fatal("should not inject a second reminder within the throttle gap")
	}
}

func TestMaybeInjectTodoReminderSkipsAfterRecentWrite(t *testing.T) {
	store := tool.NewTodoStore()
	store.Tool().Call(context.Background(), []byte(`{"todos":[{"content":"x","status":"pending"}]}`), &tool.ToolContext{})

	l := &loop{in: QueryInput{Todos: store}}
	// A recent successful TodoWrite already exposes the current list.
	write := todoWriteAsst()
	write.Content[0].Input = []byte(`{"todos":[{"content":"x","status":"pending"}]}`)
	l.messages = []llm.Message{write, {Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText("t1", "ok", false)}}, asst("a"), asst("b")}
	l.maybeInjectTodoReminder()
	if len(l.messages) != 4 {
		t.Fatalf("should not inject shortly after a TodoWrite, got %d msgs", len(l.messages))
	}
}

func TestRestoredTodoReminderRequiresSuccessfulVisibleWrite(t *testing.T) {
	input := []byte(`{"todos":[{"content":"unfinished comparison","status":"pending"}]}`)
	for _, state := range []string{"hidden", "failed", "interrupted", "empty", "wrapper", "already_reminded"} {
		t.Run(state, func(t *testing.T) {
			store := tool.NewTodoStore()
			store.Tool().Call(t.Context(), input, nil)
			write := todoWriteAsst()
			write.Content[0].Input = input
			result := llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText("t1", "ok", false)}}
			messages := []llm.Message{llm.BoundaryMessage(llm.BoundaryMeta{}), llm.UserText("summary without plan")}
			wantInjection := true
			switch state {
			case "failed":
				result.Content[0].IsError = true
				messages = append(messages, write, result)
			case "interrupted":
				messages = append(messages, write)
			case "empty":
				store.Tool().Call(t.Context(), []byte(`{"todos":[]}`), nil)
				wantInjection = false
			case "wrapper":
				write.Content[0].Name = tool.ExecuteExtraToolName
				write.Content[0].Input = []byte(`{"tool_name":"TodoWrite","parameters":` + string(input) + `}`)
				messages = append(messages, write, result)
				wantInjection = false
			case "already_reminded":
				messages = append(messages, llm.TodoReminderMessage(tool.RenderTodoReminder(store.List())))
				wantInjection = false
			}
			before := len(messages)
			l := &loop{in: QueryInput{Todos: store}, messages: messages}
			l.maybeInjectTodoReminder()
			if added := len(l.messages) > before; added != wantInjection {
				t.Fatalf("reminder injected=%v want=%v", added, wantInjection)
			}
			if wantInjection && !strings.Contains(l.messages[len(l.messages)-1].Text(), "unfinished comparison") {
				t.Fatal("restored reminder did not contain the active plan")
			}
			length := len(l.messages)
			l.maybeInjectTodoReminder()
			if len(l.messages) != length {
				t.Fatal("a visible restored plan was injected repeatedly without the normal interval")
			}
		})
	}
}
