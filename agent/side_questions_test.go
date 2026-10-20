package agent

import (
	"context"
	"encoding/json"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/sidequestion"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

type sideAgentProvider struct {
	calls int
	path  string
}

func (p *sideAgentProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(y func(llm.StreamEvent, error) bool) {
		msg, stop, _, err := p.Complete(ctx, req)
		if err != nil {
			y(llm.StreamEvent{}, err)
			return
		}
		for _, b := range msg.Content {
			if b.Type == llm.BlockToolUse {
				if !y(llm.StreamEvent{Type: llm.SEToolUseStart, ToolID: b.ID, ToolName: b.Name}, nil) {
					return
				}
				if !y(llm.StreamEvent{Type: llm.SEToolInputJSON, Text: string(b.Input)}, nil) {
					return
				}
			} else if !y(llm.StreamEvent{Type: llm.SETextDelta, Text: b.Text}, nil) {
				return
			}
		}
		if !y(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: stop}, nil) {
			return
		}
		y(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
	}
}
func (p *sideAgentProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.calls++
	if p.calls == 1 {
		input, _ := json.Marshal(map[string]string{"file_path": p.path})
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "fixture-read", Name: "Read", Input: input}}}, "tool_use", llm.Usage{}, nil
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("main finished")}}, "end_turn", llm.Usage{}, nil
}

func TestSideActualChatCheckpointToolResultAndTranscriptIsolation(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{true: "stream", false: "atomic"}[streaming], func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "asset.txt")
			if err := os.WriteFile(file, []byte("controlled-homepage-result"), 0600); err != nil {
				t.Fatal(err)
			}
			p := &sideAgentProvider{path: file}
			bound := sidequestion.Bind(p, sidequestion.Model{Model: "fixture", Streaming: streaming})
			store := transcript.NewStore(filepath.Join(dir, "transcripts"))
			chat := NewChatAgent(bound, "fixture", dir, store, 100000)
			chat.SetNonStreaming(func() bool { return !streaming })
			var snapshots []sidequestion.Snapshot
			var activities []db.Activity
			ctx := sidequestion.WithPublisher(t.Context(), func(s sidequestion.Snapshot) { snapshots = append(snapshots, s) })
			if _, err := chat.Chat(ctx, "mainagent", "conv-987654", "Read the local fixture", 5, time.Minute, false, func(a db.Activity) { activities = append(activities, a) }); err != nil {
				t.Fatal(err)
			}
			if len(snapshots) < 3 {
				t.Fatalf("actual Chat missed capture hooks: %d", len(snapshots))
			}
			last := snapshots[len(snapshots)-1]
			raw, _ := json.Marshal(last)
			if last.Parent.ConversationID != 987654 || !strings.Contains(string(raw), "controlled-homepage-result") || !strings.Contains(string(raw), "main finished") {
				t.Fatalf("missing real tool result/final reply: %s", raw)
			}
			before, err := os.ReadFile(store.MainPath("conv-987654"))
			if err != nil {
				t.Fatal(err)
			}
			count := len(activities)
			tools := 0
			for _, a := range activities {
				if a.Kind == "tool_use" {
					tools++
				}
			}
			if tools != 1 {
				t.Fatalf("main fixture tool executions: %d", tools)
			}
			req, err := sidequestion.BuildRequest(last, nil, "side-only-question")
			if err != nil {
				t.Fatal(err)
			}
			// Reset the fake to request another Read; the side executor cannot run it.
			p.calls = 0
			p.path = filepath.Join(dir, "nonexistent")
			answer, err := (sidequestion.SideQuestionService{Provider: bound}).Answer(t.Context(), req, streaming, nil)
			if err != nil || !answer.ToolUse || p.calls != 1 || !strings.Contains(answer.Text, "도구 작업을 실행할 수 없습니다") {
				t.Fatalf("tool denial %+v %v", answer, err)
			}
			after, err := os.ReadFile(store.MainPath("conv-987654"))
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) || len(activities) != count {
				t.Fatal("side question modified main transcript/activity")
			}
			if len(snapshots) == 0 || snapshots[len(snapshots)-1].Version != last.Version {
				t.Fatal("side request replaced main checkpoint")
			}
		})
	}
}

func TestChatConnectionRotationKeepsConversationSnapshots(t *testing.T) {
	dir := t.TempDir()
	store := transcript.NewStore(filepath.Join(dir, "transcripts"))
	provider := sidequestion.Bind(&sideAgentProvider{calls: 1}, sidequestion.Model{Model: "fixture", Streaming: true})
	chat := NewChatAgent(provider, "fixture", dir, store, 100000)
	cfg := Config{AuthMode: "api_key", APIKey: "first-fixture-key", Model: "fixture"}
	chat.SetConnectionIdentity(func() string { return cfg.ConnectionIdentity() })
	var snapshots []sidequestion.Snapshot
	ctx := sidequestion.WithPublisher(t.Context(), func(s sidequestion.Snapshot) { snapshots = append(snapshots, s) })
	run := func(message string) sidequestion.Snapshot {
		t.Helper()
		snapshots = nil
		if _, err := chat.Chat(ctx, "mainagent", "conv-42", message, 2, time.Minute, false, nil); err != nil {
			t.Fatal(err)
		}
		if len(snapshots) == 0 || snapshots[len(snapshots)-1].Parent.ConversationID != 42 {
			t.Fatal("connection rotation lost the logical conversation snapshot")
		}
		return snapshots[len(snapshots)-1]
	}
	run("first-connection-message")
	before, err := os.ReadFile(store.MainPath("conv-42"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.APIKey = "second-fixture-key"
	rotated := run("second-connection-message")
	raw, _ := json.Marshal(rotated.Request)
	if strings.Contains(string(raw), "first-connection-message") {
		t.Fatal("rotated connection replayed the old transcript")
	}
	resumed := run("third-message")
	raw, _ = json.Marshal(resumed.Request)
	if !strings.Contains(string(raw), "second-connection-message") {
		t.Fatal("new connection did not resume its own transcript")
	}
	after, err := os.ReadFile(store.MainPath("conv-42"))
	if err != nil || string(after) != string(before) {
		t.Fatal("connection rotation modified the previous transcript")
	}
}
