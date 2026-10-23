package db

import (
	"encoding/json"
	"testing"
)

func TestExecutionProfileSnapshotKeepsTaskResources(t *testing.T) {
	profile := &ExecutionProfile{ID: "medium", Version: 1, Workers: 5, BrowserLimit: 4}
	raw, err := executionProfileSnapshot(profile)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ExecutionProfile
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != *profile {
		t.Fatalf("resource snapshot changed: %+v", decoded)
	}
	if _, err := executionProfileSnapshot(&ExecutionProfile{Workers: 0, BrowserLimit: 1}); err == nil {
		t.Fatal("invalid resource snapshot accepted")
	}
	if raw, err := executionProfileSnapshot(nil); err != nil || string(raw) != "null" {
		t.Fatalf("nil snapshot: %s %v", raw, err)
	}
}
