package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
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

func TestCurrentOverviewBoundsAllProseWithoutDroppingGraphStructure(t *testing.T) {
	long := strings.Repeat("한글🙂<&>\n", 2_000)
	root := map[string]any{"id": int64(10), "description": long, "goal": long}
	out := map[string]any{"task": root, "frontier_open": 800}
	for _, key := range []string{"goals", "hints", "open_intents", "running_intents", "finding_list", "recent_facts", "recent_done_intents", "cold_digests"} {
		field := "summary"
		if key == "goals" || key == "hints" {
			field = "text"
		} else if key == "cold_digests" {
			field = "body"
		}
		rows := make([]map[string]any, 300)
		for i := range rows {
			rows[i] = map[string]any{
				"id": i + 20, "state": "running", field: long,
				"parents": []int64{1, 2}, "yields": []int64{3}, "asset_ids": []any{float64(9)},
			}
			if key == "hints" {
				rows[i]["traffic_refs"] = []any{map[string]any{"traffic_id": "request-id", "role": "proof", "note": long}}
			}
		}
		out[key] = rows
	}
	limitCurrentOverviewText(out)
	if total := overviewProseRunes(out); total > currentOverviewTotalTextRunes {
		t.Fatalf("current prose=%d exceeds %d", total, currentOverviewTotalTextRunes)
	}
	if root["goal_truncated"] != true || root["description_truncated"] != true || out["summary_truncated"] != true || out["frontier_open"] != 800 {
		t.Fatal("root truncation flags or frontier count changed")
	}
	if !strings.Contains(out["summary_note"].(string), "판단 전에") {
		t.Fatal("missing instruction to retrieve truncated goals/hints before acting")
	}
	for _, key := range []string{"goals", "hints", "open_intents", "running_intents", "finding_list", "recent_facts", "recent_done_intents", "cold_digests"} {
		rows := out[key].([]map[string]any)
		if len(rows) != 300 {
			t.Fatalf("%s lost graph rows", key)
		}
		for i, row := range rows {
			if row["id"] != i+20 || row["state"] != "running" || !reflect.DeepEqual(row["parents"], []int64{1, 2}) || !reflect.DeepEqual(row["yields"], []int64{3}) || !reflect.DeepEqual(row["asset_ids"], []any{float64(9)}) {
				t.Fatalf("%s lost graph structure", key)
			}
			for _, value := range row {
				if text, ok := value.(string); ok && !utf8.ValidString(text) {
					t.Fatalf("%s invalid UTF-8", key)
				}
			}
			if key == "hints" {
				ref := row["traffic_refs"].([]any)[0].(map[string]any)
				if ref["traffic_id"] != "request-id" || ref["role"] != "proof" || ref["note_truncated"] != true {
					t.Fatal("traffic provenance or truncation flag lost")
				}
			}
		}
	}
}

func TestGraphOverviewBudgetIncludesSourcesAndPreservesOriginals(t *testing.T) {
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
	if total := overviewProseRunes(overview); total > currentOverviewTotalTextRunes+relatedOverviewTotalTextRunes {
		t.Fatalf("combined current and related prose=%d exceeds budget", total)
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
		t.Fatal("planner prompt did not use the same bounded overview")
	}
	root := overview["task"].(map[string]any)
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
