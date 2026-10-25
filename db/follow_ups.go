package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// FollowUp is retained to read archived fact payloads from the former queue.
type FollowUp struct {
	ID          int     `json:"id"`
	Summary     string  `json:"summary"`
	State       string  `json:"state"`
	Revision    int64   `json:"revision,omitempty"`
	IntentID    int64   `json:"intent_id,omitempty"`
	Reason      string  `json:"reason,omitempty"`
	EvidenceIDs []int64 `json:"evidence_ids,omitempty"`
}

type FollowUpRef struct {
	FactID int64 `json:"fact_id"`
	LeadID int   `json:"lead_id"`
}

type FollowUpEntry struct {
	FactID int64 `json:"fact_id"`
	FollowUp
	IntentState    string  `json:"intent_state,omitempty"`
	NeedsReview    bool    `json:"needs_review"`
	ResultID       int64   `json:"result_id,omitempty"`
	ResultSummary  string  `json:"result_summary,omitempty"`
	OutcomeNodeIDs []int64 `json:"outcome_node_ids"`
	FindingNodeIDs []int64 `json:"finding_node_ids"`
	OutcomeCount   int     `json:"outcome_count"`
}

type FollowUpUpdate struct {
	FollowUpRef
	State            string  `json:"state"`
	Reason           string  `json:"reason"`
	EvidenceIDs      []int64 `json:"evidence_ids,omitempty"`
	IntentID         int64   `json:"intent_id,omitempty"`
	ExpectedRevision *int64  `json:"expected_revision,omitempty"`
}

func NewFollowUps(summaries []string) ([]FollowUp, error) {
	if len(summaries) > 20 {
		return nil, fmt.Errorf("사실 하나의 후속 단서는 최대 20개입니다")
	}
	seen := map[string]bool{}
	var out []FollowUp
	for _, summary := range summaries {
		summary = strings.TrimSpace(summary)
		if summary == "" || utf8.RuneCountInString(summary) > 1000 {
			return nil, fmt.Errorf("후속 단서는 1~1000자로 입력하세요")
		}
		if seen[summary] {
			continue
		}
		seen[summary] = true
		out = append(out, FollowUp{ID: len(out) + 1, Summary: summary, State: "open", Revision: 1})
	}
	return out, nil
}

func FollowUpsFromPayload(raw json.RawMessage) []FollowUp {
	var payload struct {
		FollowUps []FollowUp `json:"follow_ups"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return nil
	}
	return payload.FollowUps
}

const followUpRows = `SELECT n.id, lead.value FROM exploration_nodes n
CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(n.payload->'follow_ups')='array' THEN n.payload->'follow_ups' ELSE '[]'::jsonb END) lead(value)
WHERE n.exploration_id=$1 AND n.kind='fact' AND n.state<>'dismissed'`

// ListFollowUps reads this task's legacy entries. Source tasks remain read-only.
func (s *ExplorationStore) ListFollowUps(includeSettled bool, limit, offset int) ([]FollowUpEntry, int, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	query := followUpRows
	if !includeSettled {
		query += ` AND COALESCE(lead.value->>'state','open') IN ('open','assigned')`
	}
	var total int
	if err := s.db.QueryRow(`SELECT count(*) FROM (`+query+`) entries`, s.expID).Scan(&total); err != nil {
		return nil, 0, err
	}
	// Review ended assignments first, including missing/deleted intents. This
	// stays derived read state: a normal Worker exit never resolves a lead.
	rows, err := s.db.Query(`SELECT leads.id,leads.value,COALESCE(i.state,''),
	  COALESCE(leads.value->>'state','open')='assigned' AND (i.id IS NULL OR i.state NOT IN ('open','running','paused')) AS needs_review,
	  COALESCE(result.id,0),LEFT(COALESCE(result.summary,''),400),
	  COALESCE(outcomes.ids,'[]'::jsonb),COALESCE(outcomes.findings,'[]'::jsonb),COALESCE(outcomes.total,0)
	  FROM (`+query+`) leads
	  LEFT JOIN exploration_nodes i ON i.id::text=leads.value->>'intent_id' AND i.exploration_id=$1 AND i.kind='intent'
	  LEFT JOIN LATERAL (
	    SELECT a.id,a.summary FROM activity a WHERE a.exploration_id=$1 AND a.node_id=i.id AND a.kind IN ('result','text')
	    ORDER BY CASE WHEN a.kind='result' THEN 0 ELSE 1 END,a.id DESC LIMIT 1
	  ) result ON true
	  LEFT JOIN LATERAL (
	    SELECT jsonb_agg(n.id ORDER BY n.id) AS ids,max(n.total) AS total,
	      jsonb_agg(n.id ORDER BY n.id) FILTER(WHERE n.kind='finding') AS findings
	    FROM (SELECT n.id,n.kind,count(*) OVER() AS total FROM exploration_edges e JOIN exploration_nodes n ON n.id=e.dst_id AND n.exploration_id=e.exploration_id
	    WHERE e.exploration_id=$1 AND e.src_id=i.id AND e.rel='yields' AND n.kind IN ('fact','finding') AND n.state<>'dismissed'
	    ORDER BY n.id DESC LIMIT 20) n
	  ) outcomes ON true
	  ORDER BY needs_review DESC,leads.id,(leads.value->>'id')::integer LIMIT $2 OFFSET $3`, s.expID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []FollowUpEntry{}
	for rows.Next() {
		var entry FollowUpEntry
		var raw, outcomes, findings []byte
		if err := rows.Scan(&entry.FactID, &raw, &entry.IntentState, &entry.NeedsReview, &entry.ResultID, &entry.ResultSummary, &outcomes, &findings, &entry.OutcomeCount); err != nil {
			return nil, 0, err
		}
		if err := json.Unmarshal(raw, &entry.FollowUp); err != nil {
			return nil, 0, fmt.Errorf("후속 단서 읽기: %w", err)
		}
		if err := json.Unmarshal(outcomes, &entry.OutcomeNodeIDs); err != nil {
			return nil, 0, err
		}
		if err := json.Unmarshal(findings, &entry.FindingNodeIDs); err != nil {
			return nil, 0, err
		}
		out = append(out, entry)
	}
	return out, total, rows.Err()
}

// UpdateFollowUps remains as a compatibility boundary for old callers. Legacy
// payloads are readable, while new planning uses fact/finding provenance and Todo.
func (s *ExplorationStore) UpdateFollowUps(_ context.Context, _ []FollowUpUpdate) error {
	return fmt.Errorf("후속 단서 상태 도구는 종료되었습니다. 기존 fact를 읽고 새 의도는 parent_ids로 연결하세요")
}
