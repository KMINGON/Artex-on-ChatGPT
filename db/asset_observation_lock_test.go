package db

import (
	"testing"
	"time"
)

func TestTaskCompanyCreationLocksBeforeSourceReferences(t *testing.T) {
	d, _ := templateFixtureDB(t)
	source, _ := graphMutationTask(t, d)
	var companyID int64
	if err := d.QueryRow(`INSERT INTO companies(name,nkey) VALUES('observation lock fixture','observation-lock-fixture') RETURNING id`).Scan(&companyID); err != nil {
		t.Fatal(err)
	}
	blocker, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if err := lockCompanyScopeMutation(blocker); err != nil {
		t.Fatal(err)
	}
	created := make(chan error, 1)
	go func() {
		_, err := d.CreateTaskWithOptions("source and company child", "", TaskCreateOptions{SourceTaskIDs: []int64{source.ID}, CompanyIDs: []int64{companyID}})
		created <- err
	}()
	defer func() {
		_ = blocker.Rollback()
		if err := <-created; err != nil {
			t.Errorf("task creation failed after releasing company lock: %v", err)
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		if err := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("task creation did not reach the company lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// An observation owns the company lock before locking its task. A creator
	// waiting for that company lock must not hold the source task's FK KEY SHARE,
	// otherwise these real paths acquire the same locks in opposite orders.
	probe, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Rollback()
	var id int64
	if err := probe.QueryRow(`SELECT id FROM tasks WHERE id=$1 FOR UPDATE NOWAIT`, source.ID).Scan(&id); err != nil {
		t.Fatalf("creator already locked its source before company lock: %v", err)
	}
}
