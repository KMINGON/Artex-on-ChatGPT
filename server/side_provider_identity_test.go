package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
)

func TestSideSnapshotRejectsChangedLoginModeOrAccount(t *testing.T) {
	base := agent.Config{AuthMode: "chatgpt_plan", ChatGPTAccountID: "saved-account-one", Model: "model"}
	snapshotModel := sideModel(base, 0, "fixture")
	server := &Server{llmCfg: base, llmOn: true, llmDirect: &sideHTTPProvider{}}
	if _, err := server.sideProvider(snapshotModel); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*agent.Config)
	}{
		{"different account", func(c *agent.Config) { c.ChatGPTAccountID = "saved-account-two" }},
		{"different login mode", func(c *agent.Config) { c.AuthMode = "api_key" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			server.llmCfg = base
			test.change(&server.llmCfg)
			if _, err := server.sideProvider(snapshotModel); err == nil {
				t.Fatal("stale snapshot accepted across account/authentication boundary")
			}
		})
	}
	public, err := json.Marshal(snapshotModel)
	if err != nil || strings.Contains(string(public), base.ChatGPTAccountID) {
		t.Fatal("public side-model metadata exposed internal account binding")
	}
}

func TestSideSnapshotRejectsChangedAPIKey(t *testing.T) {
	base := agent.Config{AuthMode: "api_key", APIKey: "old-fixture-key", Model: "model"}
	snapshot := sideModel(base, 0, "fixture")
	server := &Server{llmCfg: base, llmOn: true, llmDirect: &sideHTTPProvider{}}
	server.llmCfg.APIKey = "new-fixture-key"
	if _, err := server.sideProvider(snapshot); err == nil {
		t.Fatal("stale API key snapshot accepted")
	}
	raw, _ := json.Marshal(snapshot)
	if strings.Contains(string(raw), "fixture-key") {
		t.Fatal("API key exposed")
	}
}
