package chatgptllm

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/Autumn-27/norma/llm"
)

// ResponseMetadata reports only observed non-secret terminal response fields.
// AccessProgramsPresent distinguishes an explicit null (standard/default) from
// a server that omitted the field entirely. It is not an eligibility promise.
type ResponseMetadata struct {
	Model                 string `json:"model,omitempty"`
	AccessProgramCyber    string `json:"access_program_cyber,omitempty"`
	AccessProgramsPresent bool   `json:"access_programs_present"`
	UsageReceived         bool   `json:"usage_received"`
}
type responseObserverKey struct{}

// WithResponseObserver adds a synchronous observer. It is called once per
// terminal HTTP attempt, including a failed attempt before a bounded retry.
func WithResponseObserver(ctx context.Context, observer func(ResponseMetadata)) context.Context {
	previous, _ := ctx.Value(responseObserverKey{}).(func(ResponseMetadata))
	return context.WithValue(ctx, responseObserverKey{}, func(m ResponseMetadata) {
		if previous != nil {
			previous(m)
		}
		if observer != nil {
			observer(m)
		}
	})
}
func observeResponse(ctx context.Context, metadata ResponseMetadata) {
	if observer, ok := ctx.Value(responseObserverKey{}).(func(ResponseMetadata)); ok {
		observer(metadata)
	}
}
func (r response) metadata() ResponseMetadata {
	m := ResponseMetadata{Model: safeToken(r.Model), AccessProgramsPresent: len(r.AccessPrograms) > 0, UsageReceived: r.Usage != nil}
	var access struct {
		Cyber string `json:"cyber"`
	}
	if json.Unmarshal(r.AccessPrograms, &access) == nil {
		m.AccessProgramCyber = safeToken(access.Cyber)
	}
	// The Responses contract uses explicit null for implicit Standard; an absent
	// field or empty object does not establish the program used by the server.
	if bytes.Equal(bytes.TrimSpace(r.AccessPrograms), []byte("null")) {
		m.AccessProgramCyber = "standard"
	}
	return m
}
func (r response) usage() llm.Usage {
	if r.Usage == nil {
		return llm.Usage{}
	}
	return llm.Usage{InputTokens: r.Usage.Input, OutputTokens: r.Usage.Output, CacheReadTokens: r.Usage.Details.Cached}
}
