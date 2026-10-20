package agent

import (
	"context"
	"iter"
	"os"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

type graphSummaryProvider struct {
	stop  string
	calls int
}

func (p *graphSummaryProvider) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.calls++
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("partial graph summary")}}, p.stop, llm.Usage{}, nil
}
func (graphSummaryProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	panic("compress must use Complete")
}

func TestGraphSummaryRequiresNormalCompletion(t *testing.T) {
	for _, stop := range []string{"", "max_tokens", "tool_use", "end_turn"} {
		c := NewCompactor(&graphSummaryProvider{stop: stop}, "fixture")
		body, err := c.compress(t.Context(), &coldGraph{}, block{}, map[int64]*db.Node{})
		if stop == "end_turn" {
			if err != nil || body == "" {
				t.Fatalf("valid graph summary rejected: %v", err)
			}
		} else if err == nil || body != "" {
			t.Fatalf("partial graph summary accepted: stop=%q body=%q err=%v", stop, body, err)
		}
	}
}

func TestGraphMajorFailurePreservesActiveDigest(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires an explicitly configured isolated ARTEX_PG_DSN")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	expID, err := d.CreateExploration("compaction completion fixture", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec("DELETE FROM explorations WHERE id=$1", expID) })
	store := d.Exploration(expID)
	first, err := store.AddNode(db.KindFact, map[string]any{"summary": "first fact"}, 0, "confirmed", "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AddNode(db.KindFact, map[string]any{"summary": "second fact"}, 0, "confirmed", "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Link(first, db.RelYields, second); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec("UPDATE explorations SET round_no=100 WHERE id=$1", expID); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyStampOps([]db.StampOp{{ID: first, Set: true, Round: 1}, {ID: second, Set: true, Round: 1}}); err != nil {
		t.Fatal(err)
	}
	digestID, err := store.AddDigest(map[string]any{"body": "previous valid summary", "signature": "stale fixture", "generation": 1}, []int64{first, second})
	if err != nil {
		t.Fatal(err)
	}
	provider := &graphSummaryProvider{stop: "max_tokens"}
	c := NewCompactor(provider, "fixture")
	c.major(t.Context(), store)
	if provider.calls != 1 {
		t.Fatalf("graph summary was not attempted: calls=%d", provider.calls)
	}
	active, err := store.ActiveDigests()
	if err != nil || len(active) != 1 || active[0].ID != digestID {
		t.Fatalf("failed summary replaced previous digest: active=%+v err=%v", active, err)
	}
	members, err := store.DigestMembers(digestID)
	if err != nil || len(members) != 2 {
		t.Fatalf("failed summary removed coverage: members=%v err=%v", members, err)
	}
}
