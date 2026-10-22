package chatgptllm

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func TestCatalogPreservesAccountProgramsAndReasoning(t *testing.T) {
	raw := `{"models":[
		{"slug":"gpt-6-sol","display_name":"GPT-6-Sol","visibility":"list","context_window":272000,"available_access_programs":{"cyber":["standard","daybreak_blue"]},"supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"ultra"}],"default_reasoning_level":"medium"},
		{"slug":"gpt-6.1-sol","visibility":"list","available_access_programs":{"cyber":["standard"]},"supported_reasoning_levels":[{"effort":"low"},{"effort":"ultra"}],"default_reasoning_level":"low"},
		{"slug":"gpt-daybreak-blue-latest","visibility":"list","available_access_programs":{"cyber":["daybreak_blue"]}},
		{"slug":"hidden","visibility":"hide","available_access_programs":{"cyber":["standard"]}}
	]}`
	client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Errorf("unexpected catalog request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(raw))
	})
	models, err := Models(t.Context(), auth, client)
	if err != nil || len(models) != 3 {
		t.Fatalf("catalog: %+v %v", models, err)
	}
	for _, model := range models {
		if model.Capabilities.Source != "account_catalog" || !model.Capabilities.AccessProgramsKnown || model.Capabilities.CheckedAt == "" {
			t.Fatalf("account evidence lost: %+v", model)
		}
	}
	sol := models[0]
	if sol.ContextWindow != 272000 || sol.Name != "GPT-6-Sol" || !sol.Capabilities.ReasoningEffortsKnown || sol.Capabilities.DefaultReasoningEffort != "medium" || !reflect.DeepEqual(sol.Capabilities.ReasoningEfforts, []string{"low", "medium", "ultra"}) || sol.Capabilities.DaybreakAvailable == nil || !*sol.Capabilities.DaybreakAvailable {
		t.Fatalf("Sol metadata lost or substituted with API docs: %+v", sol)
	}
	if models[1].Capabilities.DaybreakAvailable == nil || *models[1].Capabilities.DaybreakAvailable || models[1].Capabilities.DefaultReasoningEffort != "low" {
		t.Fatalf("account Standard-only model marked Daybreak: %+v", models[1])
	}
	if !reflect.DeepEqual(models[2].Capabilities.AccessProgramsCyber, []string{"daybreak_blue"}) || models[2].Capabilities.ReasoningEffortsKnown {
		t.Fatalf("Blue-only catalog gained invented options: %+v", models[2])
	}
}

func TestCatalogMissingNullAndEmptyCapabilitiesRemainDistinct(t *testing.T) {
	for _, metadata := range []string{"", `,"available_access_programs":null,"supported_reasoning_levels":null`, `,"available_access_programs":{}`} {
		models, err := DecodeModels(strings.NewReader(`{"models":[{"slug":"gpt-6-sol","visibility":"list"`+metadata+`}]}`), "chatgpt_plan")
		if err != nil {
			t.Fatal(err)
		}
		c := models[0].Capabilities
		if c.Known || c.AccessProgramsKnown || c.ReasoningEffortsKnown || c.DaybreakAvailable != nil || c.Source != "unknown" {
			t.Fatalf("missing metadata interpreted as support: %+v", c)
		}
		encoded, _ := json.Marshal(c)
		if !strings.Contains(string(encoded), `"daybreak_available":null`) {
			t.Fatalf("unknown changed into false: %s", encoded)
		}
	}
	models, err := DecodeModels(strings.NewReader(`{"models":[{"slug":"empty","visibility":"list","available_access_programs":{"cyber":[]},"supported_reasoning_levels":[]}]}`), "chatgpt_plan")
	if err != nil {
		t.Fatal(err)
	}
	c := models[0].Capabilities
	if !c.Known || !c.AccessProgramsKnown || !c.ReasoningEffortsKnown || c.DaybreakAvailable == nil || *c.DaybreakAvailable {
		t.Fatalf("explicit empty capability list lost: %+v", c)
	}
}

func TestAPIModelCatalogUsesMetadataBeforeDocumentedCompatibility(t *testing.T) {
	models, err := DecodeModels(strings.NewReader(`{"data":[
		{"id":"gpt-6-sol"},
		{"id":"future-model"},
		{"id":"gpt-6-luna"},
		{"id":"gpt-6-astra","display_name":"Account Astra","context_window":400000,"available_access_programs":{"cyber":["standard"]},"supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"}],"default_reasoning_level":"low"},
		{"id":"gpt-6-sol","supported_reasoning_levels":[{"effort":"medium"}]}
	]}`), "api_key")
	if err != nil || len(models) != 5 {
		t.Fatalf("catalog: %+v %v", models, err)
	}
	if models[0].Capabilities.Source != "documentation" || !models[0].Capabilities.AccessProgramsKnown {
		t.Fatalf("documented compatibility lost: %+v", models[0])
	}
	if models[1].Capabilities.Source != "unknown" || models[1].Capabilities.DaybreakAvailable != nil {
		t.Fatalf("unknown API model gained Daybreak entitlement: %+v", models[1])
	}
	if !models[2].Capabilities.ReasoningEffortsKnown || models[2].Capabilities.AccessProgramsKnown || models[2].Capabilities.DaybreakAvailable != nil {
		t.Fatalf("Luna API capabilities overclaimed: %+v", models[2])
	}
	c := models[3].Capabilities
	if c.Source != "account_catalog" || c.DaybreakAvailable == nil || *c.DaybreakAvailable || c.DefaultReasoningEffort != "low" || models[3].ContextWindow != 400000 || models[3].Name != "Account Astra" {
		t.Fatalf("account catalog overwritten by general docs: %+v", models[3])
	}
	if models[4].Capabilities.Source != "account_catalog" || models[4].Capabilities.AccessProgramsKnown || models[4].Capabilities.DaybreakAvailable != nil {
		t.Fatalf("documentation mislabeled as account access: %+v", models[4])
	}
	for _, mode := range []string{"api_key", "chatgpt_plan"} {
		if _, err := DecodeModels(strings.NewReader(`{"object":"list"}`), mode); err == nil {
			t.Fatalf("missing model list accepted in %s", mode)
		}
	}
}

func TestSubscriptionAutomaticAndStandardProgramsReachResponses(t *testing.T) {
	for _, cyber := range []string{"", "standard"} {
		t.Run(cyber, func(t *testing.T) {
			client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
				var request map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				if cyber == "" {
					if _, present := request["access_programs"]; present {
						t.Fatal("automatic subscription selection must omit access_programs")
					}
				} else if string(request["access_programs"]) != `{"cyber":"standard"}` {
					t.Fatalf("explicit Standard lost: %s", request["access_programs"])
				}
				w.Header().Set("Content-Type", "text/event-stream")
				completed(w, textItem("OK"))
			})
			p, err := New(Config{Model: "gpt-6-sol", Authorize: auth, HTTPClient: client, AccessProgramCyber: cyber, ReasoningEffort: "ultra", MaxRetries: -1})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, _, err = p.Complete(t.Context(), llm.CompletionRequest{}); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, cyber := range []string{"daybreak_blue", "daybreak_red", "invented"} {
		if err := ValidateOptions("gpt-6-sol", "chatgpt_plan", "", cyber); err == nil {
			t.Fatalf("unverified subscription program accepted: %s", cyber)
		}
	}
	if err := ValidateOptions("future-model", "api_key", "", "invented"); err == nil {
		t.Fatal("unknown model bypassed program enum validation")
	}
}

func TestDocumentedCapabilitiesDoNotGuessNewModelVariants(t *testing.T) {
	for _, model := range []string{"gpt-6-sol-future-variant", "gpt-6-astra-special", "gpt-5.6-cyber-latest"} {
		c := Capabilities(model, "api_key")
		if c.Known || c.Source != "unknown" || c.DaybreakAvailable != nil {
			t.Fatalf("undocumented model variant inherited capabilities: %s %+v", model, c)
		}
	}
	if c := Capabilities("gpt-6-sol-2026-10-21", "api_key"); c.Source != "documentation" || !c.ReasoningEffortsKnown || !c.AccessProgramsKnown {
		t.Fatal("dated model snapshot lost family documentation")
	}
}
