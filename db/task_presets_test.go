package db

import (
	"encoding/json"
	"github.com/Autumn-27/artex/policy"
	"testing"
)

func TestTaskPresetRejectsUnknownAndMismatchedSnapshots(t *testing.T) {
	if _, _, _, err := taskPresetSnapshot(TaskCreateOptions{TestPreset: "unknown"}); err == nil {
		t.Fatal("unknown preset accepted")
	}
	p, _ := policy.Resolve("lab")
	if _, _, _, err := taskPresetSnapshot(TaskCreateOptions{TestPreset: "observe", TestPolicy: p}); err == nil {
		t.Fatal("mismatched snapshot accepted")
	}
	if _, _, _, err := taskPresetSnapshot(TaskCreateOptions{ExecutionProfile: &ExecutionProfile{Workers: 0, BrowserLimit: 1}}); err == nil {
		t.Fatal("invalid resource count accepted")
	}
}
func TestTaskPresetPersistsWithTaskAndTemplate(t *testing.T) {
	dsn, _, err := DSN()
	if err != nil {
		t.Skip("isolated PostgreSQL not configured")
	}
	d, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	profile := &ExecutionProfile{ID: "medium", Version: 1, Workers: 5, BrowserLimit: 4}
	created, err := d.CreateTaskWithOptions("preset fixture", "read the fixture", TaskCreateOptions{TestPreset: "observe", ExecutionProfile: profile})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM tasks WHERE id=$1`, created.ID)
		_, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, created.ExplorationID)
	})
	fetched, err := d.GetTask(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fetched.TestPolicy == nil || fetched.TestPreset != "observe" || fetched.TestPolicy.Version != 1 || fetched.ExecutionProfile.Workers != 5 {
		t.Fatalf("snapshot lost: %+v", fetched)
	}
	original, _ := json.Marshal(fetched.TestPolicy)
	prior, present, _ := d.GetSetting("workers")
	t.Cleanup(func() {
		if present {
			_ = d.SetSetting("workers", prior)
		} else {
			_, _ = d.Exec(`DELETE FROM settings WHERE key='workers'`)
		}
	})
	_, _ = d.Exec(`INSERT INTO settings(key,value) VALUES ('workers','10') ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`)
	read, err := d.Exploration(created.ExplorationID).TestPolicy()
	if err != nil {
		t.Fatal(err)
	}
	actual, _ := json.Marshal(read)
	if string(original) != string(actual) {
		t.Fatal("saved policy changed")
	}
	fetched, err = d.GetTask(created.ID)
	if err != nil || fetched.ExecutionProfile.Workers != 5 {
		t.Fatal("global setting rewrote task snapshot")
	}
	template, err := d.CreateTaskTemplate(TaskTemplateInput{Name: "preset-fixture-" + t.Name(), Description: "fixture", Goal: "fixture", TestPreset: "standard"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.DeleteTaskTemplate(template.ID) })
	if template.TestPreset != "standard" {
		t.Fatal("template lost preset")
	}
	v := "extended"
	template, err = d.PatchTaskTemplate(template.ID, TaskTemplatePatch{TestPreset: &v})
	if err != nil || template.TestPreset != v {
		t.Fatalf("patch lost preset: %v", err)
	}
	v = ""
	template, err = d.PatchTaskTemplate(template.ID, TaskTemplatePatch{TestPreset: &v})
	if err != nil || template.TestPreset != "" {
		t.Fatal("cannot clear template preset")
	}
}
