package llmrec

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/chatgptllm"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
	"github.com/google/uuid"
)

type usageTransport func(*http.Request) (*http.Response, error)

func (f usageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUsageObservationDistinguishesAbsentZeroAndUnknown(t *testing.T) {
	var o usageObservation
	if o.received(nil) != nil {
		t.Fatal("legacy usage should remain unknown")
	}
	o.observe(chatgptllm.ResponseMetadata{})
	if got := o.received(nil); got == nil || *got {
		t.Fatal("missing response usage must be false")
	}
	o.observe(chatgptllm.ResponseMetadata{UsageReceived: true})
	o.observe(chatgptllm.ResponseMetadata{})
	if got := o.received(nil); got == nil || !*got {
		t.Fatal("later missing usage erased earlier known zero")
	}
	var failure usageObservation
	if got := failure.received(fmt.Errorf("wrapped: %w", &chatgptllm.Error{})); got == nil || *got {
		t.Fatal("pre-response error should report missing usage")
	}
}

func TestOpenAIUsageLedgerFailedAndRetryResponses(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires isolated ARTEX_PG_DSN")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if err = pg.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	if err = pg.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	legacy := "usage-legacy-" + uuid.NewString()
	if _, err = pg.Exec(`INSERT INTO llm_usage(profile_name,status) VALUES ($1,'error')`, legacy); err != nil {
		t.Fatal(err)
	}
	defer pg.Exec(`DELETE FROM llm_usage WHERE profile_name=$1`, legacy)
	if err = pg.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	var old *bool
	if err = pg.QueryRow(`SELECT usage_received FROM llm_usage WHERE profile_name=$1`, legacy).Scan(&old); err != nil || old != nil {
		t.Fatalf("legacy changed: %v %v", old, err)
	}
	for _, stream := range []bool{false, true} {
		for _, kind := range []string{"missing", "zero", "known", "retry"} {
			t.Run(fmt.Sprintf("stream%v/%s", stream, kind), func(t *testing.T) {
				profile := "usage-" + uuid.NewString()
				defer pg.Exec(`DELETE FROM llm_usage WHERE profile_name=$1`, profile)
				defer pg.Exec(`DELETE FROM llm_records WHERE profile_name=$1`, profile)
				calls := 0
				client := &http.Client{Transport: usageTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					response := map[string]any{"status": "failed", "model": "resolved-test-model", "access_programs": nil, "error": map[string]string{"code": "unknown_failure", "message": "raw-secret-should-not-log"}}
					if kind == "known" || kind == "retry" {
						response["usage"] = map[string]int{"input_tokens": 7, "output_tokens": 3}
					}
					if kind == "zero" {
						response["usage"] = map[string]int{"input_tokens": 0, "output_tokens": 0}
					}
					if kind == "retry" && calls == 1 {
						response["error"] = map[string]string{"code": "server_error"}
					}
					b, _ := json.Marshal(map[string]any{"type": "response.failed", "response": response})
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(b) + "\n\n"))}, nil
				})}
				p, err := chatgptllm.New(chatgptllm.Config{Model: "mock-model", APIKey: "mock-key", HTTPClient: client, MaxRetries: 1, RetryInterval: time.Millisecond})
				if err != nil {
					t.Fatal(err)
				}
				r := Wrap(p, pg, "mock-model", profile, "", "", nil)
				req := llm.CompletionRequest{Messages: []llm.Message{llm.UserText("test")}}
				if stream {
					for _, e := range r.Stream(context.Background(), req) {
						if e != nil {
							err = e
						}
					}
				} else {
					_, _, _, err = r.Complete(context.Background(), req)
				}
				if err == nil {
					t.Fatal("failed response accepted")
				}
				var rows, input, output int
				var received bool
				var status string
				if e := pg.QueryRow(`SELECT input_tokens,output_tokens,usage_received,status FROM llm_usage WHERE profile_name=$1`, profile).Scan(&input, &output, &received, &status); e != nil {
					t.Fatal(e)
				}
				if e := pg.QueryRow(`SELECT COUNT(*) FROM llm_usage WHERE profile_name=$1`, profile).Scan(&rows); e != nil {
					t.Fatal(e)
				}
				wantInput, wantOutput := 0, 0
				if kind == "known" || kind == "retry" {
					wantInput, wantOutput = 7*calls, 3*calls
				}
				if rows != 1 || input != wantInput || output != wantOutput || received != (kind != "missing") || status != "error" {
					t.Fatalf("rows=%d usage=%d/%d received=%v status=%s", rows, input, output, received, status)
				}
				if (kind == "retry" && calls != 2) || (kind != "retry" && calls != 1) {
					t.Fatalf("attempts=%d", calls)
				}
				var body, rawRequest, rawResponse string
				if e := pg.QueryRow(`SELECT response_body,COALESCE(raw_request,''),COALESCE(raw_response,'') FROM llm_records WHERE profile_name=$1`, profile).Scan(&body, &rawRequest, &rawResponse); e != nil {
					t.Fatal(e)
				}
				if rawRequest != "" || rawResponse != "" || strings.Contains(body, "raw-secret") {
					t.Fatal("raw failure response exposed")
				}
				var recorded struct {
					Metadata chatgptllm.ResponseMetadata `json:"response_metadata"`
					Error    map[string]any              `json:"llm_error"`
				}
				if e := json.Unmarshal([]byte(body), &recorded); e != nil {
					t.Fatal(e)
				}
				if recorded.Metadata.Model != "resolved-test-model" || recorded.Metadata.UsageReceived != received || recorded.Error["usage_received"] != received || recorded.Error["attempts"] != float64(calls) {
					t.Fatalf("recorded safe metadata: %+v", recorded)
				}
			})
		}
	}
}
