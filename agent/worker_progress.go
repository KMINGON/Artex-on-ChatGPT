package agent

// WorkerProgress is current in-memory execution state, not a graph completion
// claim. No output or arguments are included. An absent run is no longer active.
type WorkerProgress struct {
	IntentID         int64  `json:"intent_id"`
	Worker           string `json:"worker"`
	Phase            string `json:"phase"`
	StartedUnix      int64  `json:"started_unix"`
	PhaseSinceUnix   int64  `json:"phase_since_unix"`
	LastProgressUnix int64  `json:"last_progress_unix"`
	IdleSeconds      int64  `json:"idle_seconds"`
	Tool             string `json:"tool,omitempty"`
	PendingTools     int    `json:"pending_tools"`
}
