package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/norma/llm"
)

func TestSideSnapshotRejectsChangedLoginModeOrAccount(t *testing.T) {
	base := agent.Config{AuthMode: "chatgpt_plan", ChatGPTAccountID: "saved-account-one", Format: llm.FormatOpenAIResponses, Model: "model", Stream: true}
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
