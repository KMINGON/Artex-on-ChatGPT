package db

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestTaskQuotaFailoverCannotChangeBillingMode(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires an explicitly configured isolated ARTEX_PG_DSN")
	}
	d, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	var ids []int64
	for i, mode := range []string{"api_key", "api_key", "chatgpt_plan"} {
		profile := &LLMProfile{Name: fmt.Sprintf("billing-fixture-%d-%d", time.Now().UnixNano(), i), Format: "openai-responses", Model: "fixture", AuthMode: mode, APIKey: "fixture"}
		if mode == "chatgpt_plan" {
			profile.APIKey, profile.ChatGPTAccountID = "", "fixture-account"
		}
		id, err := d.SaveProfile(profile)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		t.Cleanup(func() { _ = d.DeleteProfile(id) })
	}
	task, err := d.CreateTaskWithOptions("billing fixture", "fixture", TaskCreateOptions{LLMProfileIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	transition, err := d.MarkTaskLLMProfileQuotaExhaustedAtRevision(task.ID, ids[0], task.LLMChainRevision, "insufficient_quota")
	if err != nil || transition.NextProfileID == nil || *transition.NextProfileID != ids[1] {
		t.Fatalf("API-to-API failover changed: %+v %v", transition, err)
	}
	current, err := d.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	revision := current.LLMChainRevision
	_, err = d.MarkTaskLLMProfileQuotaExhaustedAtRevision(task.ID, ids[1], revision, "insufficient_quota")
	if !errors.Is(err, ErrTaskLLMBillingTransition) {
		t.Fatalf("mixed billing transition accepted: %v", err)
	}
	current, err = d.GetTask(task.ID)
	if err != nil || current.ActiveLLMProfileID == nil || *current.ActiveLLMProfileID != ids[1] || current.LLMChainRevision != revision {
		t.Fatalf("blocked billing transition changed the cursor: %+v %v", current, err)
	}
	chain, err := d.TaskLLMProfiles(task.ID)
	if err != nil || len(chain) != 3 || chain[1].Status != "ready" || chain[2].Status != "ready" {
		t.Fatalf("blocked transition changed chain status: %+v %v", chain, err)
	}
}
