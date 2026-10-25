package db

import (
	"context"
	"testing"
)

func TestIntentAssetScopePreparationAtomicAndGuarded(t *testing.T) {
	d, _ := templateFixtureDB(t)
	task, store := graphMutationTask(t, d)
	as := d.Assets()
	var ids []int64
	for _, host := range []string{"a.scope.invalid", "b.scope.invalid"} {
		id, err := as.UpsertEndpoint(UpsertEndpointReq{URL: "https://" + host + "/target", Method: "GET", TaskID: task.ID})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	intent, err := store.AddIntentWithParents(t.Context(), map[string]any{"summary": "prepare targets"}, 5, ids, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE assets SET last_seen=now()+interval '1 minute' WHERE id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`CREATE FUNCTION reject_second_scope_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.domain='b.scope.invalid' THEN RAISE EXCEPTION 'fixture second scope failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_second_scope_fixture BEFORE INSERT ON task_scope FOR EACH ROW EXECUTE FUNCTION reject_second_scope_fixture()`); err != nil {
		t.Fatal(err)
	}
	prepare := func(ctx context.Context, enabled bool) ([]*Asset, error) {
		return as.PrepareIntentAssets(ctx, task.ID, store.ID(), intent, ids, enabled)
	}
	if assets, err := prepare(t.Context(), true); err == nil || len(assets) != 0 {
		t.Fatalf("partial scope accepted: %+v %v", assets, err)
	}
	if n := graphCount(t, d, `SELECT count(*) FROM task_scope WHERE task_id=$1`, task.ID); n != 0 {
		t.Fatalf("first scope escaped rollback: %d", n)
	}
	if assets, err := prepare(t.Context(), false); err != nil || len(assets) != 2 {
		t.Fatalf("coverage-off targets unavailable: %+v %v", assets, err)
	}
	if _, err := d.Exec(`DROP TRIGGER reject_second_scope_fixture ON task_scope; DROP FUNCTION reject_second_scope_fixture()`); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := prepare(canceled, true); err == nil {
		t.Fatal("canceled preparation accepted")
	}
	other, otherStore := graphMutationTask(t, d)
	if _, err := as.PrepareIntentAssets(t.Context(), other.ID, otherStore.ID(), intent, ids, true); err == nil {
		t.Fatal("foreign intent accepted")
	}
	for range 2 {
		if assets, err := prepare(t.Context(), true); err != nil || len(assets) != 2 {
			t.Fatalf("prepared targets %+v %v", assets, err)
		}
	}
	if n := graphCount(t, d, `SELECT count(*) FROM task_scope WHERE task_id=$1`, task.ID); n != 2 {
		t.Fatalf("retry scope count=%d", n)
	}
	if n := graphCount(t, d, `SELECT count(*) FROM task_asset_links WHERE task_id=$1 AND source<>'system'`, task.ID); n != 0 {
		t.Fatal("preparation overwrote original asset provenance")
	}
	if err := d.SetPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := d.QueueTaskArchive(task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := prepare(t.Context(), true); err == nil {
		t.Fatal("queued archive accepted late scope preparation")
	}
}
