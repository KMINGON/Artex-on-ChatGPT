package server

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/mcp"
	actool "github.com/Autumn-27/norma/tool"
)

func observedBrowserSession(t *testing.T) *browserSession {
	t.Helper()
	s := fakeBrowserSession(t)
	script := `const readline=require('node:readline');process.stdout.write('{"ready":true}\n');readline.createInterface({input:process.stdin}).on('line',line=>{const r=JSON.parse(line);const items=r.after>=1?[]:[{sequence:1,url:'https://fixture.test/api?token=fixture-private',method:'GET',status:200}];process.stdout.write(JSON.stringify({id:r.id,result:{epoch:'fixture-epoch',items,cursor:1,has_more:false,dropped:0}})+'\n')}).on('close',()=>process.exit(0));`
	if err := os.WriteFile(filepath.Join(os.Getenv("ARTEX_BROWSER_SCRIPTS"), "browser-session.cjs"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	return s
}

type browserObservationProvider struct {
	step func(llm.CompletionRequest) []llm.StreamEvent
}

func (p *browserObservationProvider) Stream(_ context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		for _, event := range p.step(req) {
			if !yield(event, nil) {
				return
			}
		}
	}
}

func (p *browserObservationProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return accumulateStreamForTest(ctx, p.Stream, req)
}

func TestBrowserObservationWorkerToGraphAndDerivedIntent(t *testing.T) {
	if os.Getenv("ARTEX_PG_DSN") == "" {
		t.Skip("requires an explicitly configured isolated ARTEX_PG_DSN")
	}
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	task, err := m.CreateTask("browser graph flow", "preserve observed route", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	var taskID int64
	if _, err := fmt.Sscan(task.ID, &taskID); err != nil {
		t.Fatal(err)
	}
	if err := m.assets.AddAutoScope(taskID, "subdomain", "fixture.test", "", ""); err != nil {
		t.Fatal(err)
	}
	intentID, err := task.Store.AddIntent(map[string]any{"summary": "inspect browser fixture"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Store.SetIntentState(intentID, "running"); err != nil {
		t.Fatal(err)
	}
	intent, err := task.Store.GetNode(intentID)
	if err != nil {
		t.Fatal(err)
	}
	session := observedBrowserSession(t)
	oldAugment, oldResolve := agent.ToolAugment, agent.ToolResolve
	t.Cleanup(func() { agent.ToolAugment, agent.ToolResolve = oldAugment, oldResolve })
	agent.ToolResolve = nil
	agent.ToolAugment = func(_ context.Context, role string) ([]actool.CoreTool, agent.DeferredInfo, func()) {
		if role != "worker" {
			return nil, agent.DeferredInfo{}, func() {}
		}
		base := actool.Build(actool.Spec{Name: "fixture_browser", Schema: map[string]any{"type": "object", "properties": map[string]any{}}, Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			return actool.Text("browser fixture completed"), nil
		}})
		return []actool.CoreTool{&managedBrowserTool{CoreTool: base, session: session}}, agent.DeferredInfo{}, func() {}
	}
	calls := 0
	var assetID int64
	provider := &browserObservationProvider{step: func(req llm.CompletionRequest) []llm.StreamEvent {
		calls++
		name, input := "fixture_browser", `{}`
		if calls == 2 {
			messages, _ := json.Marshal(req.Messages)
			if !strings.Contains(string(messages), "browser_observations") || strings.Contains(string(messages), "fixture-private") {
				t.Fatal("model did not receive redacted browser registration summary")
			}
			if err := m.pg.QueryRow(`SELECT a.id FROM assets a JOIN task_asset_links l ON l.asset_id=a.id WHERE l.task_id=$1 AND a.type='endpoint' AND a.url='https://fixture.test/api'`, taskID).Scan(&assetID); err != nil {
				t.Fatal(err)
			}
			name = "record_fact"
			input = fmt.Sprintf(`{"summary":"browser route verified","detail":"다른 fixture 분기를 확인해야 합니다","asset_ids":[%d]}`, assetID)
		} else if calls > 2 {
			return []llm.StreamEvent{{Type: llm.SETextDelta, Text: "fixture handoff completed"}, {Type: llm.SEMessageDelta, StopReason: "end_turn"}}
		}
		return []llm.StreamEvent{{Type: llm.SEToolUseStart, ToolID: fmt.Sprintf("fixture-%d", calls), ToolName: name}, {Type: llm.SEToolInputJSON, Text: input}, {Type: llm.SEMessageDelta, StopReason: "tool_use"}}
	}}
	worker := agent.NewWorker(provider, "fixture", m.dir, nil, 0, 4)
	if !NewEngine(m).runIntent(t.Context(), task, "fixture", worker, intent, "", "") {
		t.Fatal("worker not run")
	}
	var factID int64
	if err := m.pg.QueryRow(`SELECT id FROM exploration_nodes WHERE exploration_id=$1 AND payload->>'summary'='browser route verified'`, task.ExpID).Scan(&factID); err != nil {
		t.Fatal(err)
	}
	plannerTools := agent.NewToolSet(task.Store, "planner")
	plannerTools.SetTaskID(taskID)
	plannerTools.SetAssetStore(m.assets, m.assets.Companies())
	addIntent := workflowTool(t, plannerTools.PlannerTools(), "add_intent")
	workflowCall(t, t.Context(), addIntent, map[string]any{"summary": "follow observed route", "asset_ids": []int64{assetID}, "parent_ids": []int64{factID}}, false)
	var anchors, derived int
	if err := m.pg.QueryRow(`SELECT count(*) FROM exploration_anchors WHERE asset_id=$1`, assetID).Scan(&anchors); err != nil {
		t.Fatal(err)
	}
	if err := m.pg.QueryRow(`SELECT count(*) FROM exploration_edges WHERE exploration_id=$1 AND src_id=$2 AND rel='derived_from'`, task.ExpID, factID).Scan(&derived); err != nil {
		t.Fatal(err)
	}
	if anchors < 4 || derived != 1 {
		t.Fatalf("handoff graph disconnected: anchors=%d derived=%d", anchors, derived)
	}
	var leaked int
	if err := m.pg.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND payload::text LIKE '%fixture-private%'`, task.ExpID).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("query credential persisted: count=%d err=%v", leaked, err)
	}
}

func TestBrowserObservationCursorRetriesOnlyFailedPersistence(t *testing.T) {
	s := observedBrowserSession(t)
	var calls, observed int
	var sourceID string
	ctx := agent.WithHTTPObserver(t.Context(), func(_ context.Context, items []agent.HTTPObservation) agent.HTTPObservationResult {
		observed++
		if len(items) != 1 || !strings.Contains(items[0].URL, "token=fixture-private") {
			t.Fatal("private callback lost URL-dependent gate input")
		}
		if sourceID == "" {
			sourceID = items[0].SourceID
		} else if items[0].SourceID != sourceID {
			t.Fatal("retry changed observation source identity")
		}
		if observed == 1 {
			return agent.HTTPObservationResult{Failed: 1}
		}
		return agent.HTTPObservationResult{Registered: 1, AssetIDs: []int64{71}, FactID: 91}
	})
	base := actool.Build(actool.Spec{Name: "fixture", Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
		calls++
		return actool.Text("browser original output"), nil
	}})
	tool := &managedBrowserTool{CoreTool: base, session: s}
	for attempt := range 3 {
		result, err := tool.Call(ctx, json.RawMessage(`{}`), nil)
		if err != nil || result.IsError || !strings.Contains(result.Flatten(), "browser original output") {
			t.Fatalf("browser result lost: %v, %s", err, result.Flatten())
		}
		if strings.Contains(result.Flatten(), "fixture-private") || strings.Contains(result.Flatten(), "fixture.test") {
			t.Fatal("private observation URL leaked to tool output")
		}
		if attempt == 0 && (s.observationCursor != 0 || !strings.Contains(result.Flatten(), `"failed":1`)) {
			t.Fatal("failed DB batch was acknowledged")
		}
		if attempt == 1 && (s.observationCursor != 1 || !strings.Contains(result.Flatten(), `"fact_id":91`)) {
			t.Fatal("successful DB batch not acknowledged")
		}
		if attempt == 2 && strings.Contains(result.Flatten(), "browser_observations") {
			t.Fatal("acknowledged batch was emitted twice")
		}
	}
	if calls != 3 || observed != 2 {
		t.Fatalf("browser action replayed or observation lost: browser=%d callback=%d", calls, observed)
	}
}

func TestCanceledBrowserCallDoesNotRegisterLateObservations(t *testing.T) {
	s := observedBrowserSession(t)
	notices := 0
	ctx := agent.WithHTTPObserver(t.Context(), func(context.Context, []agent.HTTPObservation) agent.HTTPObservationResult {
		notices++
		return agent.HTTPObservationResult{Registered: 1}
	})
	ctx, cancel := context.WithCancel(ctx)
	base := actool.Build(actool.Spec{Name: "fixture", Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
		cancel() // Simulate a late tool return after the task has been stopped.
		return actool.Text("late browser output"), nil
	}})
	tool := &managedBrowserTool{CoreTool: base, session: s}
	if _, err := tool.Call(ctx, json.RawMessage(`{}`), nil); err != nil {
		t.Fatal(err)
	}
	if notices != 0 || s.observationCursor != 0 {
		t.Fatal("canceled call committed or acknowledged late observations")
	}
}

func TestBrowserCloseReportsUnsavedObservationsOnce(t *testing.T) {
	for _, mode := range []string{"storage_failure", "read_failure", "limited", "dropped", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			s := observedBrowserSession(t)
			path := filepath.Join(os.Getenv("ARTEX_BROWSER_SCRIPTS"), "browser-session.cjs")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "dropped" {
				raw = []byte(strings.ReplaceAll(string(raw), "dropped:0", "dropped:3"))
			}
			if mode == "read_failure" {
				raw = []byte(strings.ReplaceAll(string(raw), "result:{epoch:", "error:{epoch:"))
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := s.ensure(t.Context()); err != nil {
				t.Fatal(err)
			}
			var notices []agent.HTTPObservationSummary
			var writes int
			ctx := agent.WithHTTPObserver(t.Context(), func(context.Context, []agent.HTTPObservation) agent.HTTPObservationResult {
				writes++
				if mode == "storage_failure" {
					return agent.HTTPObservationResult{Failed: 1}
				}
				if mode == "limited" {
					return agent.HTTPObservationResult{Skipped: 1, Limited: true}
				}
				return agent.HTTPObservationResult{Registered: 1, AssetIDs: []int64{71}, FactID: 91}
			})
			ctx = agent.WithHTTPObservationFlushReporter(ctx, func(summary agent.HTTPObservationSummary) { notices = append(notices, summary) })
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			client := &managedBrowserClient{client: new(mcp.Client), session: s, ctx: ctx}
			_ = client.Close()
			_ = client.Close()
			if mode == "cancelled" {
				if writes != 0 || len(notices) != 0 {
					t.Fatal("cancelled cleanup registered/reported late observations")
				}
				return
			}
			if len(notices) != 1 {
				t.Fatalf("flush notifications=%d, want one", len(notices))
			}
			summary := notices[0]
			if mode == "storage_failure" && (summary.Failed != 1 || !summary.HasMore || s.observationCursor != 0) {
				t.Fatalf("lost failed flush: %+v", summary)
			}
			if mode == "read_failure" && (!summary.ReadFailed || writes != 0) {
				t.Fatalf("lost read failure: %+v", summary)
			}
			if mode == "limited" && !summary.Limited {
				t.Fatalf("lost limit: %+v", summary)
			}
			if mode == "dropped" && summary.Dropped != 3 {
				t.Fatalf("lost drops: %+v", summary)
			}
			encoded, _ := json.Marshal(summary)
			if strings.Contains(string(encoded), "fixture-private") || strings.Contains(string(encoded), "fixture.test") {
				t.Fatal("flush exposed private URL")
			}
		})
	}
}

func TestWorkerCleanupEmitsBrowserObservationLoss(t *testing.T) {
	m := stateGraphManager(t)
	task, err := m.CreateTask("browser cleanup loss", "report unsaved observations", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	intentID, err := task.Store.AddIntent(map[string]any{"summary": "finish fixture"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := task.Store.GetNode(intentID)
	if err != nil {
		t.Fatal(err)
	}
	oldAugment, oldResolve := agent.ToolAugment, agent.ToolResolve
	t.Cleanup(func() { agent.ToolAugment, agent.ToolResolve = oldAugment, oldResolve })
	agent.ToolResolve = nil
	cleanupCalls := 0
	agent.ToolAugment = func(ctx context.Context, _ string) ([]actool.CoreTool, agent.DeferredInfo, func()) {
		return nil, agent.DeferredInfo{}, func() {
			cleanupCalls++
			agent.ReportHTTPObservationFlush(ctx, agent.HTTPObservationSummary{HTTPObservationResult: agent.HTTPObservationResult{Failed: 2, Limited: true}, Dropped: 3, ReadFailed: true, HasMore: true})
		}
	}
	provider := &browserObservationProvider{step: func(llm.CompletionRequest) []llm.StreamEvent {
		return []llm.StreamEvent{{Type: llm.SETextDelta, Text: "fixture completed"}, {Type: llm.SEMessageDelta, StopReason: "end_turn"}}
	}}
	worker := agent.NewWorker(provider, "fixture", m.dir, nil, 0, 2)
	var activities []db.Activity
	_, _, err = worker.Execute(t.Context(), "fixture", 0, nil, task.Store, intent, nil, func(activity db.Activity) { activities = append(activities, activity) }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cleanupCalls != 1 {
		t.Fatalf("worker cleanup ran %d times", cleanupCalls)
	}
	notices := 0
	for _, activity := range activities {
		if activity.Kind != "observation" {
			continue
		}
		notices++
		if activity.NodeID == nil || *activity.NodeID != intentID || activity.Worker != "fixture" || !strings.Contains(activity.Summary, `"failed":2`) || !strings.Contains(activity.Summary, `"read_failed":true`) || !strings.Contains(activity.Summary, `"dropped":3`) {
			t.Fatalf("incomplete cleanup activity: %+v", activity)
		}
	}
	if notices != 1 {
		t.Fatalf("worker cleanup loss notices=%d, want one", notices)
	}
}
