package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func TestTriggerWithoutReadyConnectionStaysPending(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("no test database")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	key := fmt.Sprintf("trigger_pending_%d", time.Now().UnixNano())
	profile, err := pg.SaveProfile(&db.LLMProfile{Name: key, AuthMode: "api_key", Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Exec(`DELETE FROM llm_profiles WHERE id=$1`, profile)
	if _, err := pg.Exec(`INSERT INTO agents(key,name,role,builtin,enabled,llm_profile_id) VALUES($1,$1,'worker',false,true,$2)`, key, profile); err != nil {
		t.Fatal(err)
	}
	defer pg.Exec(`DELETE FROM agents WHERE key=$1`, key)
	tr, err := pg.CreateTrigger(&db.AgentTrigger{AgentKey: key, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer pg.DeleteTrigger(tr.ID)
	if err := pg.CommitTriggerBatch(db.TriggerBatch{Deliveries: []db.TriggerDelivery{{TriggerID: tr.ID, EventKey: "fixture", AgentKey: key, Message: "pending"}}}); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := pg.QueryRow(`SELECT id FROM trigger_deliveries WHERE trigger_id=$1`, tr.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	s := &Server{ctx: context.Background(), m: &Manager{pg: pg, dir: t.TempDir()}}
	s.runTriggeredRun(triggeredRun{agentKey: key, deliveryIDs: []int64{id}, message: "pending"})
	var state string
	var hasConversation bool
	if err := pg.QueryRow(`SELECT state,conversation_id IS NOT NULL FROM trigger_deliveries WHERE id=$1`, id).Scan(&state, &hasConversation); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || hasConversation {
		t.Fatalf("unstarted work consumed: state=%s conversation=%v", state, hasConversation)
	}
}
