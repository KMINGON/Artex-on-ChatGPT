package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// HTTPObservation comes from an actual browser/HTTP response, not page text.
// Bodies, headers, credentials and query values are deliberately not retained.
type HTTPObservation struct {
	URL      string `json:"url"`
	Method   string `json:"method"`
	Status   int    `json:"status"`
	SourceID string `json:"source_id"`
	AssetID  int64  `json:"asset_id,omitempty"`
	State    string `json:"state,omitempty"`
}

type HTTPObservationResult struct {
	Registered int     `json:"registered"`
	Skipped    int     `json:"skipped"`
	Failed     int     `json:"failed"`
	AssetIDs   []int64 `json:"asset_ids,omitempty"`
	FactID     int64   `json:"fact_id,omitempty"`
	Limited    bool    `json:"limited,omitempty"`
}

func cleanHTTPObservation(in HTTPObservation) (HTTPObservation, error) {
	u, err := url.Parse(in.URL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || len(in.URL) > 8192 {
		return HTTPObservation{}, errors.New("관측 URL 형식이 올바르지 않습니다")
	}
	u.User, u.RawQuery, u.Fragment, u.RawFragment = nil, "", "", ""
	u.ForceQuery = false
	if u.Path == "" {
		u.Path = "/"
	}
	u.Host = strings.ToLower(u.Host)
	in.URL = u.String()
	in.Method = strings.ToUpper(strings.TrimSpace(in.Method))
	if len(in.Method) < 1 || len(in.Method) > 20 || in.Status < 100 || in.Status > 599 {
		return HTTPObservation{}, errors.New("실제 응답의 HTTP 메서드·상태가 필요합니다")
	}
	for _, c := range in.Method {
		if c < 'A' || c > 'Z' {
			return HTTPObservation{}, errors.New("관측 HTTP 메서드 형식이 올바르지 않습니다")
		}
	}
	if len(in.SourceID) > 128 || !strings.HasPrefix(in.SourceID, "browser:") {
		in.SourceID = ""
	}
	// The transport owns source IDs; strip free-form identifiers before storage.
	for _, c := range in.SourceID {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_:.", c)) {
			in.SourceID = ""
			break
		}
	}
	in.AssetID, in.State = 0, ""
	return in, nil
}

func observationHostInScope(host string, scopes []TaskScope) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, scope := range scopes {
		domain := strings.ToLower(strings.TrimSuffix(scope.Domain, "."))
		switch scope.Kind {
		case "root_domain":
			if domain != "" && (host == domain || strings.HasSuffix(host, "."+domain)) {
				return true
			}
		case "subdomain":
			if domain != "" && host == domain {
				return true
			}
		case "ip", "cidr":
			_, network, err := net.ParseCIDR(scope.Net)
			if err == nil && network.Contains(net.ParseIP(host)) {
				return true
			}
		}
	}
	return false
}

// RecordHTTPObservations atomically keeps one observation fact per intent and
// registers in-scope endpoints through the existing normalizer. Observations
// never create task_scope rows or label a request as a tested vulnerability.
func (s *AssetStore) RecordHTTPObservations(ctx context.Context, taskID, explorationID, intentID int64, observations []HTTPObservation) (HTTPObservationResult, error) {
	var result HTTPObservationResult
	if taskID <= 0 || explorationID <= 0 || intentID <= 0 {
		return HTTPObservationResult{}, errors.New("관측을 연결할 작업·의도가 필요합니다")
	}
	if len(observations) > 100 {
		return HTTPObservationResult{}, errors.New("관측은 한 번에 최대 100개입니다")
	}
	scopes, err := s.ListTaskScopeWithSources(taskID)
	if err != nil {
		return HTTPObservationResult{}, err
	}
	blocks, err := s.ListAssetInterceptRules()
	if err != nil {
		return HTTPObservationResult{}, err
	}
	taskBlocks, allows, err := s.TaskInterceptRulesSplit(taskID)
	if err != nil {
		return HTTPObservationResult{}, err
	}
	blocks = append(blocks, taskBlocks...)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return HTTPObservationResult{}, err
	}
	defer tx.Rollback()
	if err := lockCompanyScopeMutation(tx); err != nil {
		return HTTPObservationResult{}, err
	}
	if err := LockTaskEvidenceTx(tx, taskID); err != nil {
		return HTTPObservationResult{}, err
	}
	var valid bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks t JOIN exploration_nodes n ON n.exploration_id=t.exploration_id WHERE t.id=$1 AND t.exploration_id=$2 AND n.id=$3 AND n.kind='intent')`, taskID, explorationID, intentID).Scan(&valid); err != nil {
		return HTTPObservationResult{}, err
	}
	if !valid {
		return HTTPObservationResult{}, errors.New("관측의 작업과 의도가 일치하지 않습니다")
	}
	var factID int64
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT n.id,n.payload FROM exploration_nodes n JOIN exploration_edges e ON e.dst_id=n.id AND e.rel='yields' WHERE n.exploration_id=$1 AND n.kind='fact' AND e.src_id=$2 AND n.payload->>'observation_source'='http_response' ORDER BY n.id LIMIT 1 FOR NO KEY UPDATE OF n`, explorationID, intentID).Scan(&factID, &raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return HTTPObservationResult{}, err
	}
	payload := map[string]any{"observation_source": "http_response", "confidence": "observed"}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &payload); err != nil {
			return HTTPObservationResult{}, err
		}
	}
	var saved struct {
		Observations []HTTPObservation `json:"observations"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &saved); err != nil {
			return HTTPObservationResult{}, err
		}
	}
	seen := map[string]int{}
	for i, observation := range saved.Observations {
		seen[observation.Method+" "+observation.URL] = i
	}
	store := &AssetStore{db: s.db, company: s.company, tx: tx}
	changed := false
	for _, observation := range observations {
		rawURL := observation.URL // match original rules without storing credentials/query values
		observation, err = cleanHTTPObservation(observation)
		if err != nil {
			result.Skipped++
			continue
		}
		key := observation.Method + " " + observation.URL
		previous, exists := seen[key]
		if exists && saved.Observations[previous].State == "registered" {
			result.Skipped++
			continue
		}
		if !exists && len(saved.Observations) >= 1000 {
			result.Skipped++
			result.Limited = true
			continue
		}
		u, _ := url.Parse(observation.URL)
		host := u.Hostname()
		allowed := observationHostInScope(host, scopes)
		// Company/ICP scopes and explicitly anchored targets can establish an
		// already-known host without treating a linked external URL as permission.
		if !allowed {
			if err := tx.QueryRowContext(ctx, `WITH `+contextCoverageCTE+` SELECT EXISTS(SELECT 1 FROM target JOIN assets a ON a.id=target.id WHERE lower(a.domain)=lower($2) OR a.ip=$2)`, taskID, host).Scan(&allowed); err != nil {
				return HTTPObservationResult{}, err
			}
		}
		var domains, ips []string
		if net.ParseIP(host) == nil {
			domains = []string{host}
		} else {
			ips = []string{host}
		}
		gate := EvaluateAssetGate(blocks, allows, domains, ips, []string{rawURL, observation.URL})
		observation.State = "outside_scope"
		if !gate.Allowed {
			observation.State = "blocked"
		}
		if exists && !(allowed && gate.Allowed) && saved.Observations[previous].State == observation.State {
			result.Skipped++
			continue
		}
		if allowed && gate.Allowed {
			id, err := store.UpsertEndpoint(UpsertEndpointReq{URL: observation.URL, Method: observation.Method, TaskID: taskID})
			if err != nil {
				return HTTPObservationResult{}, err
			}
			observation.AssetID, observation.State = id, "registered"
			result.AssetIDs = append(result.AssetIDs, id)
			result.Registered++
		} else {
			result.Skipped++
		}
		if exists {
			saved.Observations[previous] = observation
		} else {
			seen[key] = len(saved.Observations)
			saved.Observations = append(saved.Observations, observation)
		}
		changed = true
	}
	if changed {
		registered := 0
		for _, observation := range saved.Observations {
			if observation.AssetID > 0 {
				registered++
			}
		}
		payload["summary"] = fmt.Sprintf("HTTP 응답 관찰 %d개: 범위 내 endpoint %d개 등록, 나머지는 범위/규칙 확인 필요. 요청 관찰이며 취약점 검증 완료를 뜻하지 않습니다.", len(saved.Observations), registered)
		payload["observations"] = saved.Observations
		if factID == 0 {
			factID, err = addGraphNodeTx(ctx, tx, explorationID, KindFact, payload, 5, "confirmed", "http-observer", nil)
			if err != nil {
				return HTTPObservationResult{}, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id) VALUES($1,$2,'yields',$3)`, explorationID, intentID, factID); err != nil {
				return HTTPObservationResult{}, err
			}
		} else {
			encoded, err := json.Marshal(payload)
			if err != nil {
				return HTTPObservationResult{}, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE exploration_nodes SET payload=$1,content_version=content_version+1,cold_since_round=NULL WHERE id=$2`, string(encoded), factID); err != nil {
				return HTTPObservationResult{}, err
			}
		}
		for _, id := range result.AssetIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO exploration_anchors(node_id,asset_id) VALUES($1,$3),($2,$3) ON CONFLICT DO NOTHING`, intentID, factID, id); err != nil {
				return HTTPObservationResult{}, err
			}
			if err := store.SetTaskAssetSource(taskID, id, "agent", "실제 HTTP 응답 관찰", &factID); err != nil {
				return HTTPObservationResult{}, err
			}
		}
	}
	result.FactID = factID
	if err := tx.Commit(); err != nil {
		return HTTPObservationResult{}, err
	}
	return result, nil
}
