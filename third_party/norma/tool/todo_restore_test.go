package tool

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func todoCall(id, name, input string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(input)}}}
}

func todoResult(id string, failed bool) llm.Message {
	return llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText(id, "result", failed)}}
}

func TestRestoreTodosOnlyFromAcknowledgedWrites(t *testing.T) {
	good := `{"todos":[{"content":"verify the remaining comparison","status":"in_progress"}]}`
	base := []llm.Message{todoCall("good", "TodoWrite", good), todoResult("good", false)}
	cases := map[string][]llm.Message{
		"cancelled_without_result": {todoCall("unfinished", "TodoWrite", `{"todos":[]}`)},
		"error_result":             {todoCall("failed", "TodoWrite", `{"todos":[]}`), todoResult("failed", true)},
		"result_without_call":      {todoResult("unknown", false)},
		"missing_required_todos":   {todoCall("invalid", "TodoWrite", `{}`), todoResult("invalid", false)},
		"malformed_items":          {todoCall("invalid", "TodoWrite", `{"todos":[{"content":3}]}`), todoResult("invalid", false)},
		"conflicting_aliases":      {todoCall("invalid", ExecuteExtraToolName, `{"tool_name":"TodoWrite","params":{"todos":[]},"parameters":{"todos":[{"content":"other","status":"pending"}]}}`), todoResult("invalid", false)},
		"wrong_tool":               {todoCall("other", "Read", good), todoResult("other", false)},
		"ambiguous_duplicate_id":   {todoCall("duplicate", "TodoWrite", good), todoCall("duplicate", "TodoWrite", `{"todos":[]}`), todoResult("duplicate", false)},
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			store := NewTodoStore()
			store.RestoreFromHistory(append(append([]llm.Message(nil), base...), extra...))
			if got := store.List(); len(got) != 1 || got[0].Content != "verify the remaining comparison" {
				t.Fatalf("unacknowledged or invalid call changed the plan: %+v", got)
			}
		})
	}
}

func TestRestoreTodosSupportsWrapperAliasesAndEmptyPlan(t *testing.T) {
	for _, key := range []string{"params", "parameters"} {
		t.Run(key, func(t *testing.T) {
			store := NewTodoStore()
			input := `{"tool_name":"TodoWrite","` + key + `":{"todos":[{"content":"unfinished","status":"pending"}]}}`
			messages := []llm.Message{todoCall("wrapper", ExecuteExtraToolName, input), todoResult("wrapper", false)}
			store.RestoreFromHistory(messages)
			if got := store.List(); len(got) != 1 || got[0].Content != "unfinished" {
				t.Fatalf("wrapper write was not restored: %+v", got)
			}
			messages = append(messages, todoCall("clear", "TodoWrite", `{"todos":[]}`), todoResult("clear", false))
			store.RestoreFromHistory(messages)
			if len(store.List()) != 0 {
				t.Fatal("successful empty plan did not clear the restored list")
			}
		})
	}
}

func TestRestoreTodosCheckpointDuplicatesDoNotRollBackLatestPlan(t *testing.T) {
	first := todoCall("shared", "TodoWrite", `{"todos":[{"content":"first","status":"pending"}]}`)
	latest := todoCall("shared", "TodoWrite", `{"todos":[{"content":"latest","status":"in_progress"}]}`)
	messages := []llm.Message{first, todoResult("shared", false), latest, todoResult("shared", false), llm.BoundaryMessage(llm.BoundaryMeta{}), llm.UserText("summary"), first, todoResult("shared", false), todoResult("shared", false)}
	before, _ := json.Marshal(messages)
	store := NewTodoStore()
	for range 2 {
		store.RestoreFromHistory(messages)
		if got := store.List(); len(got) != 1 || got[0].Content != "latest" {
			t.Fatalf("checkpoint duplicate rolled back the newer successful plan: %+v", got)
		}
	}
	after, _ := json.Marshal(messages)
	if string(before) != string(after) {
		t.Fatal("restoration mutated the transcript")
	}
	store.RestoreFromHistory(nil)
	if len(store.List()) != 0 {
		t.Fatal("resuming a fresh transcript retained another session's plan")
	}
}

func TestRestoreTodosPreservesHostOwnedPlan(t *testing.T) {
	want := []Todo{{Content: "host edited plan", Status: TodoPending}}
	saves := 0
	for _, save := range []func(context.Context, []Todo) error{nil, func(context.Context, []Todo) error { saves++; return nil }} {
		store := NewPersistentTodoStore(want, save)
		store.RestoreFromHistory([]llm.Message{todoCall("past", "TodoWrite", `{"todos":[]}`), todoResult("past", false)})
		if !reflect.DeepEqual(store.List(), want) || saves != 0 {
			t.Fatalf("transcript overrode host state or replayed save: %+v saves=%d", store.List(), saves)
		}
	}
}
