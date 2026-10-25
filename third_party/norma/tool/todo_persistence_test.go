package tool

import (
	"context"
	"errors"
	"testing"
)

func TestPersistentTodoWriteCommitsOnlyAfterSave(t *testing.T) {
	initial := []Todo{{Content: "existing plan", Status: TodoPending}}
	wantErr := errors.New("storage unavailable")
	var saved []Todo
	store := NewPersistentTodoStore(initial, func(ctx context.Context, items []Todo) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if wantErr != nil {
			return wantErr
		}
		saved = append([]Todo(nil), items...)
		return nil
	})
	initial[0].Content = "caller mutation"
	input := []byte(`{"todos":[{"content":"new plan","status":"in_progress"}]}`)
	result, err := store.Tool().Call(t.Context(), input, nil)
	if err != nil || !result.IsError || store.List()[0].Content != "existing plan" {
		t.Fatalf("failed write changed plan or acknowledged success: %v, %+v, %+v", err, result, store.List())
	}
	wantErr = nil
	result, err = store.Tool().Call(t.Context(), input, nil)
	if err != nil || result.IsError || len(saved) != 1 || store.List()[0] != saved[0] {
		t.Fatalf("successful save mismatch: %v, %+v, %+v", err, result, store.List())
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err = store.Tool().Call(ctx, []byte(`{"todos":[]}`), nil)
	if err != nil || !result.IsError || len(store.List()) != 1 {
		t.Fatalf("canceled save changed plan: %v, %+v, %+v", err, result, store.List())
	}
	read := store.List()
	read[0].Content = "reader mutation"
	if store.List()[0].Content != "new plan" {
		t.Fatal("List exposed mutable plan storage")
	}
}
