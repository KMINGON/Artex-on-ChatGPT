package server

import "testing"

// Keep the shipped user-facing labels and errors in Korean.
func TestReporterSeedLabelsLocalized(t *testing.T) {
	assertKoreanError(t, "reporter_name", reporterAgentName)
	assertKoreanError(t, "reporter_description", reporterAgentDescription)
}
