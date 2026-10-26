package agent

// cold-digest §6: graph_overview folding + the restore tools.
//
//	coldDigestsRecent — builds the folded cold region for graph_overview:
//	  cold_digests (flat {id, body, member_count}), newest-member first, capped.
//	expand_digest(id)  — level-1 restore: a digest's member compact list.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// digestMemberEntry builds the compact per-member view expand_digest returns —
// same shape as recent_facts / recent_done_intents (§6.1 middle level). store is
// the digest's OWNING store (the current task, or a read-only source task §2).
func (t *ToolSet) digestMemberEntry(store *db.ExplorationStore, id int64) map[string]any {
	n, _ := store.GetNode(id)
	if n == nil {
		return map[string]any{"id": id, "missing": true}
	}
	m := compactNode(n)
	m["state"] = n.State
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if c, ok := p["confidence"].(string); ok && c != "" {
			m["confidence"] = c
		}
	}
	return m
}

// coldDigestsRecent returns a store's active digests as flat bodies for
// graph_overview, ordered by the recency of their freshest member (max member id ≈
// latest cooled node — a digest near the live frontier is likelier relevant), and
// capped at `cap`. Overflow digest ids are returned separately (moreIDs) so they
// stay reachable via expand_digest even when not shown inline — cold_digests is the
// only exit for folded cold nodes. Shared by the current task overview and the
// read-only related-task overview (§2 cross-task reuse).
func coldDigestsRecent(store *db.ExplorationStore, cap int) (shown []map[string]any, moreIDs []int64) {
	ads, err := store.ActiveDigests()
	if err != nil || len(ads) == 0 {
		return nil, nil
	}
	graph, nodes, versions, snapshotErr := loadCompressionSnapshot(store)
	type dg struct {
		id        int64
		entry     map[string]any
		freshness int64 // max member id (ids are monotonic ≈ creation time)
	}
	items := make([]dg, 0, len(ads))
	for _, d := range ads {
		var p struct {
			Body string `json:"body"`
		}
		_ = json.Unmarshal(d.Payload, &p)
		ms, memberErr := store.DigestMembers(d.ID) // sorted asc → last = freshest
		current := snapshotErr == nil && memberErr == nil && digestMatchesSource(d, ms, graph, nodes, versions)
		entry := map[string]any{"id": d.ID, "body": p.Body, "member_count": len(ms), "input_complete": digestInputComplete(d), "source_current": current}
		if !current {
			entry["source_note"] = staleDigestNote
		}
		var fresh int64
		if len(ms) > 0 {
			fresh = ms[len(ms)-1]
		}
		items = append(items, dg{
			id:        d.ID,
			entry:     entry,
			freshness: fresh,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].freshness > items[j].freshness })
	for i, it := range items {
		if i < cap {
			shown = append(shown, it.entry)
		} else {
			moreIDs = append(moreIDs, it.id)
		}
	}
	return shown, moreIDs
}

// hiddenMembersFor returns a predicate telling whether a member is hidden (folded
// into an active digest AND still cold) in the given store — so a source task's
// overview folds exactly the way that task folds itself (§2 cross-task: "当前任务
// 什么展示逻辑，关联任务就什么逻辑"). A revived (now hot) covered member is NOT
// hidden (§6 render-time revival check). Returns a never-hidden predicate when the
// store has no digests.
func hiddenMembersFor(store *db.ExplorationStore) func(int64) bool {
	covered, hot, err := completeCoverageSnapshot(store)
	if err != nil || len(covered) == 0 {
		return func(int64) bool { return false }
	}
	return func(id int64) bool { _, c := covered[id]; return c && !hot[id] }
}

func digestInputComplete(n *db.Node) bool {
	var payload struct {
		Complete bool `json:"input_complete"`
	}
	return n != nil && json.Unmarshal(n.Payload, &payload) == nil && payload.Complete
}

const staleDigestNote = "과거 요약입니다. input_complete는 생성 당시 원문 입력 여부이며 현재 일치 여부는 source_current입니다. 최신 결론·미완료 검증은 함께 보이는 원문 또는 expand_digest/node_detail로 확인하고 원문을 우선하세요."

func digestMatchesSource(digest *db.Node, members []int64, graph *coldGraph, nodes map[int64]*db.Node, versions map[int64]int) bool {
	if !digestInputComplete(digest) {
		return false
	}
	var payload struct {
		Signature string  `json:"signature"`
		Anchors   []int64 `json:"anchor_ids"`
	}
	if json.Unmarshal(digest.Payload, &payload) != nil {
		return false
	}
	block := block{Members: members, Anchors: payload.Anchors}
	return payload.Signature == blockSignature(block, versions) && digestSourceSignature(digest) == compressionSourceSignature(graph, block, nodes)
}

// Legacy digests remain readable, but cannot hide originals whose full content
// never reached the summary model. A changed source also becomes visible again.
func completeCoveredMembers(store *db.ExplorationStore) (map[int64]int64, error) {
	covered, _, err := completeCoverageSnapshot(store)
	return covered, err
}

func completeCoverageSnapshot(store *db.ExplorationStore) (map[int64]int64, map[int64]bool, error) {
	digests, err := store.ActiveDigests()
	if err != nil {
		return nil, nil, err
	}
	covered := map[int64]int64{}
	if len(digests) == 0 {
		return covered, nil, nil
	}
	graph, nodes, versions, err := loadCompressionSnapshot(store)
	if err != nil {
		return nil, nil, err
	}
	for _, digest := range digests {
		if !digestInputComplete(digest) {
			continue
		}
		members, err := store.DigestMembers(digest.ID)
		if err != nil {
			return nil, nil, err
		}
		if !digestMatchesSource(digest, members, graph, nodes, versions) {
			continue
		}
		for _, id := range members {
			covered[id] = digest.ID
		}
	}
	return covered, graph.hotSet(), nil
}

// resolveDigest finds a digest node by id in the current task, else in a direct
// source task (read-only, §2). Returns the node, its owning store, and the source
// task id (0 = current task).
func (t *ToolSet) resolveDigest(id int64) (*db.Node, *db.ExplorationStore, int64) {
	if n, _ := t.ts.GetNode(id); n != nil && n.Kind == db.KindDigest {
		return n, t.ts, 0
	}
	srcs, _ := t.ts.DirectSourceStores()
	for _, s := range srcs {
		if n, _ := s.Store.GetNode(id); n != nil && n.Kind == db.KindDigest {
			return n, s.Store, s.Task.TaskID
		}
	}
	return nil, nil, 0
}

// expandDigest returns a digest's covered members as a compact list (§6.1). It is
// a distinct tool from node_detail because it returns a LIST of members, not one
// node's full detail.
func (t *ToolSet) expandDigest() actool.CoreTool {
	return t.writeExpTool("expand_digest",
		"cold digest를 펼쳐 구성원의 간결한 목록(id/summary/state/confidence)을 반환합니다. 개요의 recent_facts/recent_done_intents와 같은 형태이며, 전체 상세/증거는 node_detail(member_id)로 조회하세요.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "integer", "description": "digest 노드 id(개요의 cold_digests에서 조회)"},
			},
			"required": []any{"id"},
		},
		func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
			var in struct {
				ID int64 `json:"id"`
			}
			_ = json.Unmarshal(raw, &in)
			n, store, srcTaskID := t.resolveDigest(in.ID)
			if n == nil {
				return jsonResult(map[string]any{"error": fmt.Sprintf("#%d는 digest 노드가 아닙니다(이 작업 또는 직접 연결된 작업에서 찾지 못함)", in.ID)})
			}
			var p struct {
				Body string `json:"body"`
			}
			_ = json.Unmarshal(n.Payload, &p)
			members, membersErr := store.DigestMembers(in.ID)
			graph, nodes, versions, snapshotErr := loadCompressionSnapshot(store)
			current := membersErr == nil && snapshotErr == nil && digestMatchesSource(n, members, graph, nodes, versions)
			list := make([]map[string]any, 0, len(members))
			for _, m := range members {
				entry := t.digestMemberEntry(store, m)
				if srcTaskID > 0 { // 关联任务的成员：只读，带继承标记（§2）
					entry["inherited"] = true
					entry["source_task_id"] = srcTaskID
				}
				list = append(list, entry)
			}
			out := map[string]any{
				"id":             in.ID,
				"state":          n.State, // active / superseded
				"body":           p.Body,
				"members":        list,
				"input_complete": digestInputComplete(n),
				"source_current": current,
			}
			if !current {
				out["source_note"] = staleDigestNote
			}
			if srcTaskID > 0 {
				out["inherited"] = true
				out["source_task_id"] = srcTaskID
			}
			return jsonResult(out)
		})
}
