package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
)

func TestOverviewTextBudgetIsFairAndUTF8Safe(t *testing.T) {
	if got := relatedOverviewBudgetForSources(1); got != relatedOverviewMaxTextPerSource {
		t.Fatalf("single source budget=%d want=%d", got, relatedOverviewMaxTextPerSource)
	}
	perSource := relatedOverviewBudgetForSources(db.MaxTaskSourceCount)
	if perSource*db.MaxTaskSourceCount > relatedOverviewTotalTextRunes {
		t.Fatalf("aggregate budget exceeded: per_source=%d", perSource)
	}

	budget := overviewTextBudget{remaining: 5}
	got := budget.take(strings.Repeat("中", 10), 20)
	if !utf8.ValidString(got) {
		t.Fatalf("budget truncation produced invalid UTF-8: %q", got)
	}
	if utf8.RuneCountInString(got) != 5 || !budget.truncated || budget.remaining != 0 {
		t.Fatalf("unexpected truncation: got=%q budget=%+v", got, budget)
	}
	if tail := budget.take("more", 20); tail != "" {
		t.Fatalf("exhausted budget returned more text: %q", tail)
	}
}

// Count bounded prose, excluding the structural IDs/states and fixed guidance.
func overviewProseRunes(value any) int {
	switch value := value.(type) {
	case map[string]any:
		total := 0
		for key, child := range value {
			switch key {
			case "description", "goal", "text", "summary", "body", "result_summary", "source", "value", "note":
				if text, ok := child.(string); ok {
					total += utf8.RuneCountInString(text)
				}
			default:
				total += overviewProseRunes(child)
			}
		}
		return total
	case []map[string]any:
		total := 0
		for _, child := range value {
			total += overviewProseRunes(child)
		}
		return total
	case []any:
		total := 0
		for _, child := range value {
			total += overviewProseRunes(child)
		}
		return total
	}
	return 0
}

func TestGraphOverviewPreservesCurrentTextAndBoundsRelatedSources(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	long := strings.Repeat("원문🙂<&>\n", 2_000)
	var taskIDs, sourceIDs []int64
	defer func() {
		for i := len(taskIDs) - 1; i >= 0; i-- {
			_ = d.DeleteTask(taskIDs[i])
		}
	}()
	type original struct {
		id    int64
		field string
	}
	var originals []original
	for range db.MaxTaskSourceCount {
		source, err := d.CreateTask(long, long, nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		taskIDs = append(taskIDs, source.ID)
		sourceIDs = append(sourceIDs, source.ID)
		store := d.Exploration(source.ExplorationID)
		for range relatedOverviewMaxDigestsPerSource {
			id, err := store.AddDigest(map[string]any{"body": long}, nil)
			if err != nil {
				t.Fatal(err)
			}
			originals = append(originals, original{id, "body"})
		}
	}
	current, err := d.CreateTaskWithOptions(long, long, db.TaskCreateOptions{SourceTaskIDs: sourceIDs})
	if err != nil {
		t.Fatal(err)
	}
	taskIDs = append(taskIDs, current.ID)
	store := d.Exploration(current.ExplorationID)
	for _, kind := range []string{db.KindGoal, db.KindHint, db.KindIntent, db.KindFact, db.KindFinding} {
		field, state := "summary", "open"
		if kind == db.KindGoal || kind == db.KindHint {
			field = "text"
		}
		if kind == db.KindHint {
			state = "active"
		} else if kind == db.KindFact || kind == db.KindFinding {
			state = "confirmed"
		}
		for range 40 {
			id, err := store.AddNode(kind, map[string]any{field: long}, 0, state, "test", nil)
			if err != nil {
				t.Fatal(err)
			}
			originals = append(originals, original{id, field})
		}
	}
	digestID, err := store.AddDigest(map[string]any{"body": long}, nil)
	if err != nil {
		t.Fatal(err)
	}
	originals = append(originals, original{digestID, "body"})
	tools := NewToolSet(store, "planner")
	tools.SetTaskID(current.ID)
	overview := tools.graphOverviewData()
	root := overview["task"].(map[string]any)
	if root["description"] != long || root["goal"] != long || root["description_truncated"] != nil {
		t.Fatal("current task input was shortened before reaching the Planner")
	}
	for section, field := range map[string]string{
		"goals": "text", "hints": "text", "open_intents": "summary",
		"recent_facts": "summary", "finding_list": "summary", "cold_digests": "body",
	} {
		rows := overview[section].([]map[string]any)
		if len(rows) == 0 {
			t.Fatalf("missing %s", section)
		}
		for _, row := range rows {
			if row[field] != long || row[field+"_truncated"] != nil || row["id"] == nil {
				t.Fatalf("%s lost the full text or source id", section)
			}
		}
	}
	if overview["summary_truncated"] != nil {
		t.Fatal("current overview was silently truncated")
	}
	for _, source := range overview["related_tasks"].([]map[string]any) {
		if total := overviewProseRunes(source); total > relatedOverviewBudgetForSources(len(sourceIDs)) {
			t.Fatalf("related prose=%d exceeds per-source budget", total)
		}
		if source["summary_truncated"] != true {
			t.Fatal("source digest overflow not marked")
		}
	}
	raw, err := json.Marshal(overview)
	if err != nil || !utf8.Valid(raw) {
		t.Fatalf("invalid overview JSON: %v", err)
	}
	toolResult, err := tools.graphOverview().Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil || toolResult.Flatten() != string(raw) {
		t.Fatalf("tool output differs from shared overview: %v", err)
	}
	if !strings.HasSuffix(renderGraphOverview(overview), string(raw)) {
		t.Fatal("planner prompt did not use the same complete current overview")
	}
	originals = append(originals, original{root["id"].(int64), "goal"})
	for _, original := range originals {
		result := callReadJSON(t, tools.nodeDetail(), fmt.Sprintf(`{"id":%d}`, original.id)).(map[string]any)
		payload := result["payload"].(map[string]any)
		if payload[original.field] != long {
			t.Fatalf("node_detail(%d) lost original %s", original.id, original.field)
		}
	}
	desc, goal, err := store.Root()
	if err != nil || desc != long || goal != long {
		t.Fatal("overview changed persisted task description/goal")
	}
}
