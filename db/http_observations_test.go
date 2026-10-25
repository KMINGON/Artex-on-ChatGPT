package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func httpObservationFixture(t *testing.T) (*DB, *Task, *ExplorationStore, int64) {
	t.Helper()
	d, _ := templateFixtureDB(t)
	task, s := graphMutationTask(t, d)
	intent, err := s.AddIntentWithParents(context.Background(), map[string]any{"summary": "observe browser"}, 5, nil, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Assets().AddAutoScope(task.ID, "subdomain", "app.observation.invalid", "", ""); err != nil {
		t.Fatal(err)
	}
	return d, task, s, intent
}
func observedPayload(t *testing.T, s *ExplorationStore, id int64) []HTTPObservation {
	t.Helper()
	node, err := s.GetNode(id)
	if err != nil || node == nil {
		t.Fatal(err)
	}
	var payload struct {
		Observations []HTTPObservation `json:"observations"`
	}
	if err := json.Unmarshal(node.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Observations
}

func TestHTTPObservationSanitizationAndValidation(t *testing.T) {
	cleaned, err := cleanHTTPObservation(HTTPObservation{URL: "https://user:password@APP.Observation.Invalid/path?token=SECRET#fragment", Method: " get ", Status: 200, SourceID: "browser:fixture:1:2", AssetID: 999, State: "registered"})
	if err != nil || cleaned.URL != "https://app.observation.invalid/path" || cleaned.Method != "GET" || cleaned.AssetID != 0 || cleaned.State != "" || cleaned.SourceID != "browser:fixture:1:2" {
		t.Fatalf("sanitize %+v %v", cleaned, err)
	}
	for _, source := range []string{"secret-token", "browser:session:1:2?token=SECRET", strings.Repeat("x", 129)} {
		cleaned, err := cleanHTTPObservation(HTTPObservation{URL: "https://app.observation.invalid", Method: "GET", Status: 200, SourceID: source})
		if err != nil || cleaned.SourceID != "" {
			t.Fatalf("unsafe source retained: %+v %v", cleaned, err)
		}
	}
	for _, ob := range []HTTPObservation{{URL: "file:///tmp/x", Method: "GET", Status: 200}, {URL: "https://x.invalid", Method: "GET", Status: 0}, {URL: "https://x.invalid", Method: "G ET", Status: 200}, {URL: "https://x.invalid", Method: "GET", Status: 600}} {
		if _, err := cleanHTTPObservation(ob); err == nil {
			t.Fatalf("invalid observation accepted: %+v", ob)
		}
	}
}

func TestHTTPObservationsRegisterSingletonAndPreserveScope(t *testing.T) {
	d, task, s, intent := httpObservationFixture(t)
	as := d.Assets()
	ctx := context.Background()
	existing, err := as.UpsertEndpoint(UpsertEndpointReq{URL: "http://app.observation.invalid/path", Method: "GET", TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	scopes, _ := as.ListTaskScope(task.ID)
	result, err := as.RecordHTTPObservations(ctx, task.ID, s.ID(), intent, []HTTPObservation{
		{URL: "http://user:secret@APP.Observation.Invalid/path?token=SECRET", Method: "get", Status: 200, SourceID: "browser:fixture:1:1"},
		{URL: "http://app.observation.invalid/path?token=SECOND", Method: "GET", Status: 200},
		{URL: "http://app.observation.invalid/path", Method: "POST", Status: 201},
		{URL: "https://outside.observation.invalid/auth?token=EXTERNAL", Method: "GET", Status: 200},
		{URL: "http://app.observation.invalid/no-response", Method: "GET", Status: 0},
	})
	if err != nil || result.Registered != 2 || result.Skipped != 3 || result.FactID == 0 || len(result.AssetIDs) != 2 || result.AssetIDs[0] != existing {
		t.Fatalf("observation result %+v %v", result, err)
	}
	payload := observedPayload(t, s, result.FactID)
	if len(payload) != 3 || payload[2].State != "outside_scope" || payload[2].AssetID != 0 {
		t.Fatalf("external candidate %+v", payload)
	}
	node, _ := s.GetNode(result.FactID)
	for _, secret := range []string{"SECRET", "SECOND", "EXTERNAL", "secret@", "token="} {
		if strings.Contains(string(node.Payload), secret) {
			t.Fatalf("secret persisted %q", secret)
		}
	}
	after, _ := as.ListTaskScope(task.ID)
	if len(after) != len(scopes) {
		t.Fatalf("observer expanded scope %+v -> %+v", scopes, after)
	}
	if n := graphCount(t, d, `SELECT count(*) FROM assets WHERE domain='outside.observation.invalid'`); n != 0 {
		t.Fatal("outside host registered")
	}
	for _, id := range result.AssetIDs {
		if n := graphCount(t, d, `SELECT count(*) FROM exploration_anchors WHERE asset_id=$1 AND node_id IN ($2,$3)`, id, intent, result.FactID); n != 2 {
			t.Fatalf("missing observation anchors %d", n)
		}
		if n := graphCount(t, d, `SELECT count(*) FROM task_asset_links WHERE task_id=$1 AND asset_id=$2 AND source_node_id=$3 AND source='agent'`, task.ID, id, result.FactID); n != 1 {
			t.Fatal("missing source")
		}
	}
	retry, err := as.RecordHTTPObservations(ctx, task.ID, s.ID(), intent, []HTTPObservation{{URL: "http://app.observation.invalid/new", Method: "GET", Status: 200}})
	if err != nil || retry.FactID != result.FactID || retry.Registered != 1 {
		t.Fatalf("new fact per flush %+v %v", retry, err)
	}
	if len(observedPayload(t, s, result.FactID)) != 4 {
		t.Fatal("lost earlier observations")
	}
	if ids, err := s.OutcomeIDs(intent); err != nil || len(ids) != 1 || ids[0] != result.FactID {
		t.Fatalf("singleton yields %v %v", ids, err)
	}
}

func TestHTTPObservationsRulesAndCandidatePromotion(t *testing.T) {
	d, task, s, intent := httpObservationFixture(t)
	as := d.Assets()
	ctx := context.Background()
	rawURL := "http://app.observation.invalid/blocked?private=SECRET"
	rule, err := d.CreateAssetInterceptRule("exact_url", rawURL, "test raw rule", true)
	if err != nil {
		t.Fatal(err)
	}
	taskRule, err := as.CreateTaskInterceptRule(task.ID, "block", "exact_url", "http://app.observation.invalid/task-blocked", "task rule", true)
	if err != nil {
		t.Fatal(err)
	}
	observations := []HTTPObservation{{URL: rawURL, Method: "GET", Status: 200}, {URL: "http://app.observation.invalid/task-blocked", Method: "GET", Status: 200}, {URL: "http://new.observation.invalid/new", Method: "GET", Status: 200}}
	result, err := as.RecordHTTPObservations(ctx, task.ID, s.ID(), intent, observations)
	if err != nil || result.Registered != 0 || result.Skipped != 3 {
		t.Fatalf("rules bypassed %+v %v", result, err)
	}
	list := observedPayload(t, s, result.FactID)
	if len(list) != 3 || list[0].State != "blocked" || list[1].State != "blocked" || list[2].State != "outside_scope" {
		t.Fatalf("candidate states %+v", list)
	}
	// The same observed URL may become eligible after an operator changes scope/rules.
	if err := d.ToggleAssetInterceptRule(rule.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := as.ToggleTaskInterceptRule(task.ID, taskRule.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := as.AddAutoScope(task.ID, "subdomain", "new.observation.invalid", "", ""); err != nil {
		t.Fatal(err)
	}
	promoted, err := as.RecordHTTPObservations(ctx, task.ID, s.ID(), intent, observations)
	if err != nil || promoted.Registered != 3 || promoted.FactID != result.FactID {
		t.Fatalf("candidate never promoted %+v %v", promoted, err)
	}
	list = observedPayload(t, s, result.FactID)
	if len(list) != 3 {
		t.Fatal("promotion duplicated candidate")
	}
	for _, item := range list {
		if item.State != "registered" || item.AssetID == 0 {
			t.Fatalf("promotion failed %+v", item)
		}
	}
}

func TestHTTPObservationsAtomicFailureOwnershipCancellation(t *testing.T) {
	d, task, s, intent := httpObservationFixture(t)
	as := d.Assets()
	ctx := context.Background()
	before := graphCount(t, d, `SELECT count(*) FROM assets`)
	if _, err := d.Exec(`CREATE FUNCTION reject_observation_source() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.source='agent' THEN RAISE EXCEPTION 'fixture source failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_observation_source BEFORE INSERT OR UPDATE ON task_asset_links FOR EACH ROW EXECUTE FUNCTION reject_observation_source()`); err != nil {
		t.Fatal(err)
	}
	obs := []HTTPObservation{{URL: "http://app.observation.invalid/rollback", Method: "GET", Status: 200}}
	failed, err := as.RecordHTTPObservations(ctx, task.ID, s.ID(), intent, obs)
	if err == nil || failed.Registered != 0 || failed.FactID != 0 || len(failed.AssetIDs) != 0 {
		t.Fatalf("partial success %+v %v", failed, err)
	}
	if count := graphCount(t, d, `SELECT count(*) FROM assets`); count != before {
		t.Fatal("asset side effects survived rollback")
	}
	if count := graphCount(t, d, `SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND payload->>'observation_source'='http_response'`, s.ID()); count != 0 {
		t.Fatal("observation fact survived rollback")
	}
	if _, err := d.Exec(`DROP TRIGGER reject_observation_source ON task_asset_links; DROP FUNCTION reject_observation_source()`); err != nil {
		t.Fatal(err)
	}
	_, otherStore := graphMutationTask(t, d)
	foreign, err := otherStore.AddIntent(map[string]any{"summary": "foreign"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := as.RecordHTTPObservations(ctx, task.ID, s.ID(), foreign, obs); err == nil {
		t.Fatal("accepted foreign intent")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := as.RecordHTTPObservations(cancelled, task.ID, s.ID(), intent, obs); err == nil {
		t.Fatal("cancelled observation saved")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := as.RecordHTTPObservations(ctx, task.ID, s.ID(), intent, obs)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if count := graphCount(t, d, `SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND payload->>'observation_source'='http_response'`, s.ID()); count != 1 {
		t.Fatalf("concurrent observation duplicate facts %d", count)
	}
}

func TestHTTPObservationsBoundedBufferAndArchiveAppend(t *testing.T) {
	d, task, s, intent := httpObservationFixture(t)
	as := d.Assets()
	ctx := context.Background()
	if _, err := as.RecordHTTPObservations(ctx, task.ID, s.ID(), intent, make([]HTTPObservation, 101)); err == nil {
		t.Fatal("oversized batch accepted")
	}
	entries := make([]HTTPObservation, 1000)
	for i := range entries {
		entries[i] = HTTPObservation{URL: fmt.Sprintf("http://outside.invalid/%d", i), Method: "GET", Status: 200, State: "outside_scope"}
	}
	fact, err := s.AddFactWithIntent(ctx, map[string]any{"summary": "bounded fixture", "observation_source": "http_response", "observations": entries}, nil, "http-observer", intent)
	if err != nil {
		t.Fatal(err)
	}
	result, err := as.RecordHTTPObservations(ctx, task.ID, s.ID(), intent, []HTTPObservation{{URL: "http://app.observation.invalid/new", Method: "GET", Status: 200}})
	if err != nil || !result.Limited || result.Registered != 0 || result.FactID != fact || len(observedPayload(t, s, fact)) != 1000 {
		t.Fatalf("limit lost %+v %v", result, err)
	}
	// Existing candidates can be promoted even when the observation buffer is full.
	if err := as.AddAutoScope(task.ID, "subdomain", "outside.invalid", "", ""); err != nil {
		t.Fatal(err)
	}
	promoted, err := as.RecordHTTPObservations(ctx, task.ID, s.ID(), intent, entries[:1])
	if err != nil || promoted.Registered != 1 || len(observedPayload(t, s, fact)) != 1000 {
		t.Fatalf("full candidate promotion %+v %v", promoted, err)
	}
	if err := d.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	if err := d.SetPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}
	archive, err := d.QueueTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := as.RecordHTTPObservations(ctx, task.ID, s.ID(), intent, entries[1:2]); err == nil {
		t.Fatal("late archive observation saved")
	}
	if _, err := d.ClaimTaskArchiveJob(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := d.SnapshotTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CompleteTaskArchive(archive.ID, snapshot, "/tmp/observation-fixture.tar.zst", "fixture", 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := d.QueueTaskArchiveRestore(archive.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClaimTaskArchiveJob(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RestoreTaskArchive(archive.ID, snapshot, 0); err != nil {
		t.Fatal(err)
	}
	if err := d.CompleteTaskArchiveRestore(archive.ID); err != nil {
		t.Fatal(err)
	}
	continued, err := as.RecordHTTPObservations(ctx, task.ID, s.ID(), intent, entries[1:2])
	if err != nil || continued.FactID != fact || continued.Registered != 1 || len(observedPayload(t, s, fact)) != 1000 {
		t.Fatalf("archive continuation %+v %v", continued, err)
	}
}
