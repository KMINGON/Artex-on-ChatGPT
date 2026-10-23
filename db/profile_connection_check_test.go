package db

import (
	"testing"
	"time"
)

func TestProfileConnectionCheckRoundTripAndClear(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	p := &LLMProfile{
		Name: "connection-check", Model: "requested-model", APIKey: "fixture-key",
		ConnectionCheck: &LLMConnectionCheck{
			Model: "resolved-model", AccessProgramCyber: "daybreak_blue",
			CheckedAt: time.Date(2026, 10, 23, 1, 2, 3, 0, time.UTC),
		},
	}
	p.ID, err = d.SaveProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, p.ID) }()
	assertCheck := func(got *LLMProfile) {
		t.Helper()
		if got == nil || got.ConnectionCheck == nil || *got.ConnectionCheck != *p.ConnectionCheck {
			t.Fatalf("connection check lost: %+v", got)
		}
		if got.Model != "requested-model" || got.AccessProgramCyber != "" {
			t.Fatal("display metadata changed the requested model/program")
		}
	}
	got, err := d.ProfileByID(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertCheck(got)
	profiles, err := d.ListProfiles()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, got := range profiles {
		if got.ID == p.ID {
			assertCheck(got)
			found = true
		}
	}
	if !found {
		t.Fatal("saved profile missing from list")
	}
	p.ConnectionCheck.AccessProgramCyber = "standard"
	if _, err := d.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	got, err = d.ProfileByID(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertCheck(got)
	p.ConnectionCheck = nil
	if _, err := d.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	got, err = d.ProfileByID(p.ID)
	if err != nil || got.ConnectionCheck != nil {
		t.Fatalf("omitted connection check was not cleared: %+v, %v", got, err)
	}
}
