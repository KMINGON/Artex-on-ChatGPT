package chatgptllm

import (
	"encoding/json"
	"io"
	"time"
)

type catalogModel struct {
	ID                       string `json:"id"`
	Slug                     string `json:"slug"`
	DisplayName              string `json:"display_name"`
	Visibility               string `json:"visibility"`
	ContextWindow            int    `json:"context_window"`
	DefaultReasoningLevel    string `json:"default_reasoning_level"`
	SupportedReasoningLevels []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
	AvailableAccessPrograms struct {
		Cyber []string `json:"cyber"`
	} `json:"available_access_programs"`
}

// DecodeModels preserves the account's advertised options. Missing capability
// fields remain unknown; only an API catalog without metadata uses model docs.
func DecodeModels(body io.Reader, mode string) ([]Model, error) {
	var catalog struct {
		Models []catalogModel `json:"models"`
		Data   []catalogModel `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(body, 4<<20)).Decode(&catalog); err != nil {
		return nil, failure("invalid_catalog", "OpenAI 모델 목록을 읽을 수 없습니다", err)
	}
	entries := catalog.Models
	if mode == "api_key" {
		entries = catalog.Data
	}
	if entries == nil {
		return nil, failure("invalid_catalog", "OpenAI 모델 목록이 응답에 없습니다", nil)
	}
	out := make([]Model, 0, len(entries))
	for _, entry := range entries {
		id := entry.ID
		if mode != "api_key" {
			if entry.Visibility != "list" {
				continue
			}
			id = entry.Slug
		}
		if id == "" {
			continue
		}
		name := entry.DisplayName
		if name == "" {
			name = id
		}
		c := Capabilities(id, mode)
		if entry.SupportedReasoningLevels != nil || entry.AvailableAccessPrograms.Cyber != nil || entry.DefaultReasoningLevel != "" {
			// Do not mix documentation and account evidence under one source label.
			c = Capabilities(id, "chatgpt_plan")
			c.Source, c.SourceURL = "account_catalog", modelsURL
			c.CheckedAt = time.Now().UTC().Format(time.DateOnly)
			c.DefaultReasoningEffort = entry.DefaultReasoningLevel
			if entry.SupportedReasoningLevels != nil {
				c.ReasoningEffortsKnown = true
				for _, level := range entry.SupportedReasoningLevels {
					if level.Effort != "" && !contains(c.ReasoningEfforts, level.Effort) {
						c.ReasoningEfforts = append(c.ReasoningEfforts, level.Effort)
					}
				}
			}
			if entry.AvailableAccessPrograms.Cyber != nil {
				c.AccessProgramsKnown = true
				c.AccessProgramsCyber = entry.AvailableAccessPrograms.Cyber
				available := contains(c.AccessProgramsCyber, "daybreak_blue") || contains(c.AccessProgramsCyber, "daybreak_red")
				c.DaybreakAvailable = &available
			}
			c.Known = c.ReasoningEffortsKnown || c.AccessProgramsKnown
		}
		out = append(out, Model{ID: id, Name: name, ContextWindow: entry.ContextWindow, Capabilities: c})
	}
	return out, nil
}
