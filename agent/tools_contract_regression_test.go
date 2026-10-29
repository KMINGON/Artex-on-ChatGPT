package agent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

func TestBatchToolSchemasExposeFieldsWithoutRejectingPartialInput(t *testing.T) {
	tools := NewToolSet(nil, "fixture")
	for _, tc := range []struct {
		tool   actool.CoreTool
		batch  string
		fields []string
	}{
		{tools.setGoals(), "goals", []string{"text", "vulnclass"}},
		{tools.setConstraints(), "constraints", []string{"text", "type"}},
		{tools.addHint(), "hints", []string{"text", "asset_ids", "traffic_refs"}},
	} {
		t.Run(tc.tool.Name(), func(t *testing.T) {
			schema := tc.tool.InputSchema()
			props := schema["properties"].(map[string]any)
			items := props[tc.batch].(map[string]any)["items"].(map[string]any)
			itemProps := items["properties"].(map[string]any)
			for _, field := range tc.fields {
				if itemProps[field] == nil {
					t.Errorf("batch item field %s is hidden", field)
				}
			}
			// Missing item text must reach the handler's per-item errors instead of
			// making the SDK discard the valid items in the same call.
			raw := json.RawMessage(fmt.Sprintf(`{"%s":[{"text":"first"},{},{"text":"last"}]}`, tc.batch))
			if err := actool.ValidateInput(schema, raw); err != nil {
				t.Fatalf("schema rejected partial-success input: %v", err)
			}
		})
	}
}

func TestBatchToolContractsKeepOrderedPartialSuccess(t *testing.T) {
	_, _, tools, _, _ := graphToolFixture(t)
	for _, tc := range []struct {
		tool  actool.CoreTool
		batch string
	}{
		{tools.setGoals(), "goals"},
		{tools.setConstraints(), "constraints"},
		{tools.addHint(), "hints"},
	} {
		t.Run(tc.tool.Name(), func(t *testing.T) {
			raw := json.RawMessage(fmt.Sprintf(`{"%s":[{"text":"first"},{},{"text":"last"}]}`, tc.batch))
			if err := actool.ValidateInput(tc.tool.InputSchema(), raw); err != nil {
				t.Fatal(err)
			}
			result, err := tc.tool.Call(t.Context(), raw, nil)
			if err != nil || result.IsError {
				t.Fatalf("partial batch failed as a whole: %v %s", err, result.Flatten())
			}
			var got struct {
				IDs    []int64           `json:"ids"`
				Errors map[string]string `json:"errors"`
			}
			if err := json.Unmarshal([]byte(result.Flatten()), &got); err != nil {
				t.Fatal(err)
			}
			if len(got.IDs) != 3 || got.IDs[0] == 0 || got.IDs[1] != 0 || got.IDs[2] == 0 || len(got.Errors) != 1 || got.Errors["1"] == "" {
				t.Fatalf("lost ordered partial-success result: %+v", got)
			}
			for i, id := range got.IDs {
				if id == 0 {
					continue
				}
				var text string
				if tc.batch == "constraints" {
					rows, err := tools.ts.ListConstraints()
					if err != nil {
						t.Fatal(err)
					}
					for _, row := range rows {
						if row.ID == id {
							text = row.Text
						}
					}
				} else {
					node, err := tools.ts.GetNode(id)
					if err != nil || node == nil {
						t.Fatalf("successful item %d was not stored: %v", i, err)
					}
					var payload struct{ Text string }
					if err := json.Unmarshal(node.Payload, &payload); err != nil {
						t.Fatal(err)
					}
					text = payload.Text
				}
				want := "first"
				if i == 2 {
					want = "last"
				}
				if text != want {
					t.Fatalf("item %d stored %q, want %q", i, text, want)
				}
			}
		})
	}
}

func TestConstraintBatchKeepsLegacyMalformedFieldHandling(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		wantTexts []string
		badIndex  int
	}{
		{"text type", `{"constraints":[{"text":"first"},{"text":7},{"text":"last"}]}`, []string{"first", "last"}, 1},
		{"kind type", `{"constraints":[{"text":"first"},{"text":"middle","type":7},{"text":"last"}]}`, []string{"first", "middle", "last"}, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, tools, _, _ := graphToolFixture(t)
			tool := tools.setConstraints()
			raw := json.RawMessage(tc.body)
			// Call the unchanged handler directly to pin its legacy per-item result.
			result, err := tool.Call(t.Context(), raw, nil)
			if err != nil || result.IsError {
				t.Fatalf("handler rejected the whole batch: %v %s", err, result.Flatten())
			}
			var got struct {
				IDs    []int64           `json:"ids"`
				Errors map[string]string `json:"errors"`
			}
			if err := json.Unmarshal([]byte(result.Flatten()), &got); err != nil {
				t.Fatal(err)
			}
			if len(got.IDs) != 3 {
				t.Fatalf("lost item positions: %+v", got)
			}
			for i, id := range got.IDs {
				if (id == 0) != (i == tc.badIndex) || (got.Errors[fmt.Sprint(i)] != "") != (i == tc.badIndex) {
					t.Fatalf("changed item %d handling: %+v", i, got)
				}
			}
			rows, err := tools.ts.ListConstraints()
			if err != nil {
				t.Fatal(err)
			}
			var texts []string
			for _, row := range rows {
				texts = append(texts, row.Text)
				if row.Kind != "deny" {
					t.Fatalf("changed the handler's default kind: %q", row.Kind)
				}
			}
			if !reflect.DeepEqual(texts, tc.wantTexts) {
				t.Fatalf("stored=%v, want %v", texts, tc.wantTexts)
			}
			if err := actool.ValidateInput(tool.InputSchema(), raw); err != nil {
				t.Fatalf("schema prevented the handler's legacy per-item handling: %v", err)
			}
			for _, single := range []string{`{"text":7}`, `{"text":"valid","type":7}`} {
				if err := actool.ValidateInput(tool.InputSchema(), json.RawMessage(single)); err == nil {
					t.Fatalf("single-item field type was weakened: %s", single)
				}
			}
		})
	}
}

func TestWorkerTraceReturnedIDsMatchActualDetails(t *testing.T) {
	_, _, tools, assigned, other := graphToolFixture(t)
	appendStep := func(intent int64, text string) int64 {
		t.Helper()
		id, err := tools.ts.AppendActivity(db.Activity{NodeID: &intent, Worker: "fixture", Kind: "tool_result", Summary: text, Detail: text})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := appendStep(assigned, "first detail")
	foreign := appendStep(other, "other intent detail")
	second := appendStep(assigned, "second detail")
	third := appendStep(assigned, "third detail")
	deferred := appendStep(assigned, "deferred detail")
	const missingID int64 = 1 << 62

	read := func(requested []int64) (got struct {
		Steps []struct {
			StepID int64 `json:"step_id"`
		} `json:"steps"`
		Returned []int64 `json:"returned_step_ids"`
		Omitted  []int64 `json:"omitted_step_ids"`
		Notice   string  `json:"notice"`
	}) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"intent_id": assigned, "step_ids": requested, "q": "does not match any step"})
		result, err := tools.getWorkerTrace().Call(t.Context(), raw, nil)
		if err != nil || result.IsError {
			t.Fatalf("trace call failed: %v %s", err, result.Flatten())
		}
		if err := json.Unmarshal([]byte(result.Flatten()), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	got := read([]int64{first, foreign, missingID, second, third, deferred})
	var actual []int64
	for _, step := range got.Steps {
		actual = append(actual, step.StepID)
	}
	want := []int64{first, second, third}
	if !reflect.DeepEqual(got.Returned, want) || !reflect.DeepEqual(got.Returned, actual) {
		t.Fatalf("returned=%v actual=%v, want %v", got.Returned, actual, want)
	}
	if !reflect.DeepEqual(got.Omitted, []int64{deferred}) || !strings.Contains(got.Notice, "이번에는 3개") {
		t.Fatalf("incorrect deferred IDs or actual count: omitted=%v notice=%q", got.Omitted, got.Notice)
	}
	empty := read([]int64{foreign, missingID})
	if len(empty.Steps) != 0 || len(empty.Returned) != 0 || empty.Notice != "" {
		t.Fatalf("claimed unread details were returned: %+v", empty)
	}
}
