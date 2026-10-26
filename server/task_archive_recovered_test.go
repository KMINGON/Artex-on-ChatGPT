package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

func TestTaskArchiveRecoveredTranscriptKeepsResumedTodo(t *testing.T) {
	dataDir := t.TempDir()
	store := transcript.NewStore(filepath.Join(dataDir, "transcripts"))
	sessionID := "exp73-worker-1"
	writer := store.NewWriter(sessionID, "")
	writer.RecordMessage(llm.UserText("preserve unfinished verification"), llm.Usage{})
	if err := writer.Err(); err != nil {
		t.Fatal(err)
	}
	appendInterruptedCheckpoint := func(path string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.WriteString("{\"type\":\"checkpoint_begin\",\"checkpoint\":{\"version\":1,\"id\":\"interrupted\",\"count\":2,\"digest\":\"incomplete\"}}\n{\"type\":")
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("checkpoint fixture: %v %v", err, closeErr)
		}
	}
	writePlanAfterRecovery := func(content string) {
		t.Helper()
		calls := 0
		s := agentcore.NewSession(agentcore.Options{
			SessionID: sessionID, Transcript: store, Todos: tool.NewTodoStore(),
			Tools: []tool.CoreTool{}, DisableBackgroundTasks: true, NonStreaming: true, PermissionMode: permission.ModeBypass,
			Provider: retestProvider{complete: func(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
				calls++
				if calls == 1 {
					input, _ := json.Marshal(map[string]any{"todos": []tool.Todo{{Content: content, Status: tool.TodoPending}}})
					return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "todo-" + content, Name: "TodoWrite", Input: input}}}, "tool_use", llm.Usage{}, nil
				}
				return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("paused")}}, "end_turn", llm.Usage{}, nil
			}},
		})
		if err := s.Resume(sessionID); err != nil {
			t.Fatal(err)
		}
		for _, err := range s.Prompt(t.Context(), "continue") {
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Close(); err != nil || calls != 2 {
			t.Fatalf("write resumed plan: calls=%d err=%v", calls, err)
		}
	}
	basePath := store.MainPath(sessionID)
	appendInterruptedCheckpoint(basePath)
	writePlanAfterRecovery("earlier unfinished question")
	appendInterruptedCheckpoint(basePath + ".recovered")
	const latestPlan = "latest unresolved authorization comparison"
	writePlanAfterRecovery(latestPlan)
	records, err := store.ResumeRecords(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	writer = store.ResumeWriter(sessionID, transcript.LastUUID(records))
	writer.RecordCheckpoint([]llm.Message{llm.BoundaryMessage(llm.BoundaryMeta{}), llm.UserText("compressed view without structured plan")})
	if err := writer.Err(); err != nil {
		t.Fatal(err)
	}
	sidechainPath := filepath.Join(dataDir, "transcripts", sessionID, "side-session.jsonl.recovered")
	if err := os.MkdirAll(filepath.Dir(sidechainPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sidechainPath, []byte("existing sidechain evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	untouched := []string{"exp74-other.jsonl.recovered", "exp73-notes.txt", "exp73-worker-1.jsonl.recovered.backup", "exp73-worker-1.jsonl.recovery"}
	for _, name := range untouched {
		if err := os.WriteFile(filepath.Join(dataDir, "transcripts", name), []byte("unrelated"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	linkName := "exp73-link.jsonl.recovered"
	if err := os.Symlink(basePath, filepath.Join(dataDir, "transcripts", linkName)); err == nil {
		untouched = append(untouched, linkName)
	}
	stage, err := stageTaskArchiveFiles(dataDir, 1, "42", 73)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", ".recovered", ".recovered.recovered"} {
		if _, err := os.Lstat(basePath + suffix); !os.IsNotExist(err) {
			t.Fatalf("authoritative transcript generation %q was not archived: %v", suffix, err)
		}
	}
	if _, err := os.Lstat(sidechainPath); !os.IsNotExist(err) {
		t.Fatalf("existing sidechain directory was not staged: %v", err)
	}
	for _, name := range untouched {
		if _, err := os.Lstat(filepath.Join(dataDir, "transcripts", name)); err != nil {
			t.Fatalf("unrelated file or symlink was staged: %s: %v", name, err)
		}
	}
	archivePath := taskArchivePath(dataDir, 1, "42")
	_, _, checksum, err := writeTaskArchivePackage(archivePath, stage.payload, &pgdb.TaskArchiveSnapshot{FormatVersion: pgdb.TaskArchiveFormatVersion, TaskID: 42, ExplorationID: 73})
	if err != nil {
		t.Fatal(err)
	}
	if err := stage.commit(); err != nil {
		t.Fatal(err)
	}
	extracted := filepath.Join(dataDir, "restore")
	if err := extractTaskArchivePackage(archivePath, checksum, extracted); err != nil {
		t.Fatal(err)
	}
	installed, err := installTaskArchiveFiles(dataDir, extracted, "42", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := installed.commit(); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(sidechainPath); err != nil || string(body) != "existing sidechain evidence" {
		t.Fatalf("restored sidechain evidence: %q %v", body, err)
	}
	todos := tool.NewTodoStore()
	requests := 0
	resumed := agentcore.NewSession(agentcore.Options{
		SessionID: sessionID, Transcript: transcript.NewStore(filepath.Join(dataDir, "transcripts")), Todos: todos,
		Tools: []tool.CoreTool{}, DisableBackgroundTasks: true, NonStreaming: true,
		Provider: retestProvider{complete: func(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
			requests++
			seen := false
			for _, message := range req.Messages {
				seen = seen || llm.IsTodoReminder(message) && strings.Contains(message.Text(), latestPlan)
			}
			if !seen {
				t.Fatal("restored provider request lost the latest recovered Todo")
			}
			return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("continuing")}}, "end_turn", llm.Usage{}, nil
		}},
	})
	if err := resumed.Resume(sessionID); err != nil {
		t.Fatal(err)
	}
	if got := todos.List(); len(got) != 1 || got[0].Content != latestPlan {
		t.Fatalf("archive restored a stale plan: %+v", got)
	}
	for _, err := range resumed.Prompt(t.Context(), "continue restored task") {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := resumed.Close(); err != nil || requests != 1 {
		t.Fatalf("restored request: calls=%d err=%v", requests, err)
	}
}
