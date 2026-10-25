package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/harness"
)

func TestWorkerStopsBeforeModelWhenTargetScopeFails(t *testing.T) {
	d, task, tools, _, _ := graphToolFixture(t)
	asset, err := d.Assets().UpsertEndpoint(db.UpsertEndpointReq{URL: "https://worker-scope.invalid/target", Method: "GET", TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	intentID, err := tools.ts.AddIntentWithParents(t.Context(), map[string]any{"summary": "target scope fixture", "asset_ids": []int64{asset}}, 5, []int64{asset}, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := tools.ts.GetNode(intentID)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("reject_worker_scope_%d", task.ID)
	if _, err := d.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.task_id=%d THEN RAISE EXCEPTION 'fixture scope failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER %s BEFORE INSERT ON task_scope FOR EACH ROW EXECUTE FUNCTION %s()`, name, task.ID, name, name)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(fmt.Sprintf(`DROP TRIGGER %s ON task_scope; DROP FUNCTION %s()`, name, name)) })
	// No provider is installed: the storage failure must return before any model
	// call, rather than proceed with an incomplete target context.
	worker := NewWorker(nil, "fixture", t.TempDir(), nil, 0, 1)
	reason, _, err := worker.Execute(t.Context(), "worker-fixture", task.ID, d.Assets(), tools.ts, intent, nil, nil, nil, nil)
	if reason != harness.ReasonStorageError || err == nil || !strings.Contains(err.Error(), "시작 자산 준비 실패") {
		t.Fatalf("scope failure hidden: reason=%s err=%v", reason, err)
	}
}
