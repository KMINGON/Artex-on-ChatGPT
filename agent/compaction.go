package agent

// cold-digest §4/§5/§7: the Compactor ties the pure algorithms (coldgraph.go)
// to the store (db/digest.go) and the LLM. It runs in two modes:
//
//	maintain — cheap, synchronous, once per planner round: bump round_no,
//	           recompute hot/cold, stamp/clear cold_since_round (§2.3). This is
//	           the bookkeeping the planner does anyway; it never calls the LLM.
//	minor/major — background, off the planner hot path (§7): group cold nodes
//	           and compress each ≥2 block into a digest via the LLM. minor folds
//	           only the not-yet-covered cold set (tiered append); major re-derives
//	           the whole grouping from source and merges fragments (§5.1/§5.2),
//	           reusing bodies whose signature is unchanged (§5.3).
//
// Concurrency: one compaction per task at a time (mutex), ≥cooldown between runs,
// and a pre-save source recheck rejects any block changed during summarization.
// Render-time source checks also invalidate summaries after subsequent writes.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

// Compactor performs background cold-node compaction for many explorations.
type Compactor struct {
	prov     llm.Provider
	model    string
	params   coldParams
	n, m     int           // minor / major thresholds (§7 N=20, M=8)
	cooldown time.Duration // min gap between compactions per task (§7 60s)
	maxDur   time.Duration // hard cap on one background compaction

	mu      sync.Mutex
	running map[int64]bool
	lastRun map[int64]time.Time
}

// NewCompactor builds a compactor. prov/model are used for the §4 body LLM call
// (same model the agent runs on, per §4). A nil Compactor is a safe no-op.
func NewCompactor(prov llm.Provider, model string) *Compactor {
	return &Compactor{
		prov:     prov,
		model:    model,
		params:   defaultColdParams(),
		n:        20,
		m:        8,
		cooldown: 60 * time.Second,
		maxDur:   5 * time.Minute,
		running:  map[int64]bool{},
		lastRun:  map[int64]time.Time{},
	}
}

// OnPlannerRound is the single entry the planner calls each wake-up. It bumps the
// round, maintains the cold stamps synchronously, then (if a threshold is hit and
// no compaction is running / cooling down) launches a background compaction that
// outlives this planner round.
func (c *Compactor) OnPlannerRound(ctx context.Context, ts *db.ExplorationStore) {
	if c == nil || c.prov == nil || ts == nil {
		return
	}
	round, uncompressed, activeDigests, err := c.maintain(ts)
	if err != nil {
		log.Printf("[compaction] maintain exp=%d: %v", ts.ID(), err)
		return
	}
	needMinor := uncompressed >= c.n
	needMajor := activeDigests >= c.m
	if !needMinor && !needMajor {
		return
	}
	if !c.tryStart(ts.ID()) {
		return // already running, or within cooldown —派生态最终一致，下轮再压
	}
	go func() {
		defer c.finish(ts.ID())
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.maxDur)
		defer cancel()
		// 压缩是裸 provider 调用（compress 里直接 prov.Complete），不经过 agentcore
		// 的会话循环，所以 ctx 上没有 session id；按 session-id 头做提示缓存/粘性
		// 路由的网关（opencode zen 缺 x-opencode-session 直接 400）就收不到该头。
		// 这里补一个按探索稳定的 id：同一探索的所有压缩请求共享它，既能带上头，
		// 也让 llmrec 能把这次调用的 token 归因回该探索（此前记不到）。
		bg = transcript.WithSessionID(bg, fmt.Sprintf("exp%d-compactor", ts.ID()))
		if needMajor {
			c.major(bg, ts)
		} else {
			c.minor(bg, ts)
		}
	}()
	_ = round
}

// maintain bumps round_no, recomputes hot/cold over the whole graph, and applies
// the cold_since_round stamp/clear ops (§2.3). Returns the new round plus the
// counts that drive the trigger: how many eligible-cold nodes are not yet covered
// (minor) and how many active digests exist (major).
func (c *Compactor) maintain(ts *db.ExplorationStore) (round int64, uncompressed, activeDigests int, err error) {
	round, err = ts.BumpRound()
	if err != nil {
		return
	}
	g, _, err := loadColdGraph(ts)
	if err != nil {
		return
	}
	stamps, err := ts.ColdStamps()
	if err != nil {
		return
	}
	hot := g.hotSet()
	structCold := g.structuralCold(hot)
	ops := computeStampOps(structCold, stamps, round)
	if err = ts.ApplyStampOps(toDBStampOps(ops)); err != nil {
		return
	}
	applyStampsInPlace(stamps, ops)
	elig := g.eligibleCold(structCold, stamps, round, c.params)
	covered, err := completeCoveredMembers(ts)
	if err != nil {
		return
	}
	for id := range elig {
		if _, ok := covered[id]; !ok {
			uncompressed++
		}
	}
	ad, err := ts.ActiveDigests()
	if err != nil {
		return
	}
	activeDigests = len(ad)
	return
}

// minor folds the not-yet-covered eligible-cold set into new digest segments
// (tiered append, §5). Existing digests are untouched.
func (c *Compactor) minor(ctx context.Context, ts *db.ExplorationStore) {
	round, err := ts.RoundNo()
	if err != nil {
		return
	}
	g, nodeByID, cvers, err := loadCompressionSnapshot(ts)
	if err != nil {
		return
	}
	stamps, err := ts.ColdStamps()
	if err != nil {
		return
	}
	covered, err := completeCoveredMembers(ts)
	if err != nil {
		return
	}
	hot := g.hotSet()
	elig := g.eligibleCold(g.structuralCold(hot), stamps, round, c.params)
	uncompressed := map[int64]bool{}
	for id := range elig {
		if _, ok := covered[id]; !ok {
			uncompressed[id] = true
		}
	}
	blocks := completeCompressionBlocks(g, g.group(uncompressed, c.params), nodeByID, c.params.K)
	if len(blocks) == 0 {
		return // this batch has no ≥2 connected/shared-parent block — nothing to fold (§7)
	}
	for _, b := range blocks {
		c.foldBlock(ctx, ts, g, b, nodeByID, cvers, c.generationFor(b, nil))
	}
	// A minor may have pushed the segment count over M → merge in the same run.
	if ad, e := ts.ActiveDigests(); e == nil && len(ad) >= c.m {
		c.major(ctx, ts)
	}
}

// major re-derives the whole grouping from source over ALL eligible-cold nodes
// (§5.1 回源重压), then reconciles against the active digests by signature:
// unchanged blocks keep their digest (no LLM), stale digests are superseded, and
// new/changed blocks are compressed afresh. This is where tiered fragments of one
// direction merge and where "later became connected" blocks unify (§5.2).
func (c *Compactor) major(ctx context.Context, ts *db.ExplorationStore) {
	round, err := ts.RoundNo()
	if err != nil {
		return
	}
	g, nodeByID, cvers, err := loadCompressionSnapshot(ts)
	if err != nil {
		return
	}
	stamps, err := ts.ColdStamps()
	if err != nil {
		return
	}
	active, err := ts.ActiveDigests()
	if err != nil {
		return
	}
	hot := g.hotSet()
	elig := g.eligibleCold(g.structuralCold(hot), stamps, round, c.params)
	blocks := completeCompressionBlocks(g, g.group(elig, c.params), nodeByID, c.params.K)

	bySig := map[string]*db.Node{}
	for _, d := range active {
		if digestInputComplete(d) {
			sig, _ := digestSigGen(d)
			bySig[sig] = d
		}
	}
	desired := map[string]bool{}
	var toCreate []block
	for _, b := range blocks {
		sig := blockSignature(b, cvers)
		desired[sig] = true
		if existing := bySig[sig]; existing != nil && digestSourceSignature(existing) == compressionSourceSignature(g, b, nodeByID) {
			continue // unchanged → reuse the existing digest, skip LLM (§5.3)
		}
		toCreate = append(toCreate, b)
	}
	// Generate all replacements before retiring any digest. A failed or truncated
	// summary must leave the currently usable graph unchanged.
	bodies := make([]string, len(toCreate))
	for i, b := range toCreate {
		body, err := c.compress(ctx, g, b, nodeByID)
		if err != nil {
			log.Printf("[compaction] compress exp=%d block=%v: %v", ts.ID(), b.Members, err)
			return
		}
		if body == "" {
			return // an oversized complete input stays visible, never partially folded
		}
		bodies[i] = body
	}
	var stale []int64
	for _, d := range active {
		sig, _ := digestSigGen(d)
		valid := desired[sig] && digestInputComplete(d)
		if valid {
			members, err := ts.DigestMembers(d.ID)
			if err != nil {
				return
			}
			valid = digestMatchesSource(d, members, g, nodeByID, cvers)
		}
		if !valid {
			stale = append(stale, d.ID)
		}
	}
	for i, b := range toCreate {
		if !c.saveBlock(ts, g, b, nodeByID, cvers, c.generationFor(b, active), bodies[i]) {
			return
		}
	}
	if err := ts.SupersedeDigests(stale); err != nil {
		log.Printf("[compaction] supersede exp=%d: %v", ts.ID(), err)
	}
}

// foldBlock compresses one block and writes it only while its entire source is
// unchanged. Dropping changed members would leave their stale claims in the body.
func (c *Compactor) foldBlock(ctx context.Context, ts *db.ExplorationStore, g *coldGraph, b block, nodeByID map[int64]*db.Node, cvers map[int64]int, generation int) {
	body, err := c.compress(ctx, g, b, nodeByID)
	if err != nil {
		log.Printf("[compaction] compress exp=%d block=%v: %v", ts.ID(), b.Members, err)
		return
	}
	if body != "" {
		c.saveBlock(ts, g, b, nodeByID, cvers, generation, body)
	}
}

func (c *Compactor) saveBlock(ts *db.ExplorationStore, original *coldGraph, b block, nodeByID map[int64]*db.Node, cvers map[int64]int, generation int, body string) bool {
	fresh, freshNodes, freshVersions, err := loadCompressionSnapshot(ts)
	if err != nil {
		return false
	}
	if blockSignature(b, cvers) != blockSignature(b, freshVersions) || compressionSourceSignature(original, b, nodeByID) != compressionSourceSignature(fresh, b, freshNodes) {
		return false
	}
	freshHot := fresh.hotSet()
	for _, mID := range b.Members {
		if freshHot[mID] {
			return false
		}
	}
	if len(b.Members) < c.params.K {
		return false
	}
	payload := digestPayload(body, b, nodeByID, generation, blockSignature(b, cvers))
	payload["source_signature"] = compressionSourceSignature(original, b, nodeByID)
	if _, err := ts.AddDigest(payload, b.Members); err != nil {
		log.Printf("[compaction] add digest exp=%d: %v", ts.ID(), err)
		return false
	}
	return true
}

// generationFor computes a digest's重摘代次 (§1): 1 for a fresh fold; for a major
// merge, max(generation) over the active digests that overlap this block's
// members, +1.
func (c *Compactor) generationFor(b block, active []*db.Node) int {
	if len(active) == 0 {
		return 1
	}
	memberSet := make(map[int64]bool, len(b.Members))
	for _, m := range b.Members {
		memberSet[m] = true
	}
	best := 0
	for _, d := range active {
		_, gen := digestSigGen(d)
		for _, m := range digestMemberIDs(d) {
			if memberSet[m] {
				if gen > best {
					best = gen
				}
				break
			}
		}
	}
	return best + 1
}

// tryStart acquires the per-task compaction lock, honoring the cooldown.
func (c *Compactor) tryStart(expID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running[expID] {
		return false
	}
	if t, ok := c.lastRun[expID]; ok && time.Since(t) < c.cooldown {
		return false
	}
	c.running[expID] = true
	return true
}

func (c *Compactor) finish(expID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.running[expID] = false
	c.lastRun[expID] = time.Now()
}

// --- helpers: db ↔ coldgraph ---

// loadColdGraph reads the exploration's nodes + edges and builds the cold-graph
// view plus an id→node index (for summaries/payload during compression).
func loadColdGraph(ts *db.ExplorationStore) (*coldGraph, map[int64]*db.Node, error) {
	g, nodes, _, err := loadCompressionSnapshot(ts)
	return g, nodes, err
}

func loadCompressionSnapshot(ts *db.ExplorationStore) (*coldGraph, map[int64]*db.Node, map[int64]int, error) {
	nodes, edges, versions, err := ts.CompactionSnapshot()
	if err != nil {
		return nil, nil, nil, err
	}
	cgNodes := make([]cgNode, 0, len(nodes))
	byID := make(map[int64]*db.Node, len(nodes))
	for _, n := range nodes {
		cgNodes = append(cgNodes, cgNode{ID: n.ID, Kind: n.Kind, State: n.State,
			KeepHot: n.State == "origin"})
		byID[n.ID] = n
	}
	cgEdges := make([]cgEdge, 0, len(edges))
	for _, e := range edges {
		cgEdges = append(cgEdges, cgEdge{From: e.From, Rel: e.Rel, To: e.To})
	}
	return newColdGraph(cgNodes, cgEdges), byID, versions, nil
}

func compressionSourceSignature(g *coldGraph, b block, nodes map[int64]*db.Node) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(buildCompressionInput(g, b, nodes))))
}

func digestSourceSignature(n *db.Node) string {
	var payload struct {
		Signature string `json:"source_signature"`
	}
	_ = json.Unmarshal(n.Payload, &payload)
	return payload.Signature
}

func toDBStampOps(ops []stampOp) []db.StampOp {
	out := make([]db.StampOp, len(ops))
	for i, o := range ops {
		out[i] = db.StampOp{ID: o.ID, Set: o.Set, Round: o.Round}
	}
	return out
}

// applyStampsInPlace folds the just-applied ops into the in-memory stamp map so
// eligibility can be computed immediately without a re-read.
func applyStampsInPlace(stamps map[int64]*int64, ops []stampOp) {
	for _, o := range ops {
		if o.Set {
			r := o.Round
			stamps[o.ID] = &r
		} else {
			stamps[o.ID] = nil
		}
	}
}

// --- helpers: digest payload ---

// digestPayload builds the digest node payload (cold-digest §1): the body, the
// member ids split by kind (restore cache; source of truth is the covers edges),
// the anchor ids, the generation, and the change-detection signature.
func digestPayload(body string, b block, nodeByID map[int64]*db.Node, generation int, signature string) map[string]any {
	var facts, intents []int64
	for _, m := range b.Members {
		if n := nodeByID[m]; n != nil && n.Kind == db.KindIntent {
			intents = append(intents, m)
		} else {
			facts = append(facts, m)
		}
	}
	return map[string]any{
		"body":           body,
		"member_ids":     map[string]any{"facts": facts, "intents": intents},
		"anchor_ids":     b.Anchors,
		"generation":     generation,
		"signature":      signature,
		"input_complete": true,
	}
}

func digestSigGen(n *db.Node) (string, int) {
	var p struct {
		Signature  string `json:"signature"`
		Generation int    `json:"generation"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return p.Signature, p.Generation
}

func digestMemberIDs(n *db.Node) []int64 {
	var p struct {
		MemberIDs struct {
			Facts   []int64 `json:"facts"`
			Intents []int64 `json:"intents"`
		} `json:"member_ids"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return append(append([]int64{}, p.MemberIDs.Facts...), p.MemberIDs.Intents...)
}

// --- helpers: compression input + LLM (§4) ---

func nodeSummary(n *db.Node) string {
	if n == nil {
		return ""
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok {
			return s
		}
		if t, ok := p["text"].(string); ok {
			return t
		}
	}
	return ""
}

func nodeConfidence(n *db.Node) string {
	if n == nil {
		return ""
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if c, ok := p["confidence"].(string); ok {
			return c
		}
	}
	return ""
}

// buildCompressionInput renders the connected sub-graph for the §4 prompt:
// member nodes (summary + id + kind + state + confidence), the internal blood
// edges among members, and — for a §3.1 shared-parent group — the anchor parents
// as context ("共同父 #p"), which are NOT members.
func buildCompressionInput(g *coldGraph, b block, nodeByID map[int64]*db.Node) string {
	memberSet := make(map[int64]bool, len(b.Members))
	for _, m := range b.Members {
		memberSet[m] = true
	}
	var sb strings.Builder
	sb.WriteString("【구성 노드(압축 대상)】: \n")
	for _, m := range b.Members {
		n := nodeByID[m]
		kind := "fact"
		if n != nil && n.Kind == db.KindIntent {
			kind = "intent"
		}
		state := ""
		if n != nil {
			state = n.State
		}
		line := fmt.Sprintf("- #%d [%s/%s] %s", m, kind, state, nodeSummary(n))
		if conf := nodeConfidence(n); conf != "" {
			line += fmt.Sprintf(" (confidence=%s)", conf)
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
		if n != nil {
			if len(n.Anchors) > 0 {
				fmt.Fprintf(&sb, "  asset_ids: %v (자산 그래프 ID; 탐색 노드 ID와 구분)\n", n.Anchors)
			}
			var payload map[string]json.RawMessage
			if json.Unmarshal(n.Payload, &payload) == nil {
				fields := make([]string, 0, len(payload))
				for field := range payload {
					if field != "summary" && field != "confidence" {
						fields = append(fields, field)
					}
				}
				sort.Strings(fields)
				for _, field := range fields {
					raw := payload[field]
					if len(raw) == 0 || string(raw) == "null" {
						continue
					}
					var value string
					if json.Unmarshal(raw, &value) != nil {
						value = string(raw)
					}
					if strings.TrimSpace(value) != "" {
						fmt.Fprintf(&sb, "  %s: %s\n", field, value)
					}
				}
			}
		}
	}
	// internal edges among members
	var edgeLines []string
	for _, m := range b.Members {
		for _, to := range g.children[m] {
			if memberSet[to] {
				edgeLines = append(edgeLines, fmt.Sprintf("- #%d 생성/파생→ #%d", m, to))
			} else {
				edgeLines = append(edgeLines, fmt.Sprintf("- #%d 생성/파생→ #%d (이 묶음 밖의 후속 노드)", m, to))
			}
		}
		for _, from := range g.parents[m] {
			if !memberSet[from] {
				edgeLines = append(edgeLines, fmt.Sprintf("- #%d (이 묶음 밖의 근거 노드) 생성/파생→ #%d", from, m))
			}
		}
	}
	if len(edgeLines) > 0 {
		sb.WriteString("\n【구성 노드의 계보 관계(부모→자식; 묶음 밖 원문은 해당 id로 조회)】: \n")
		sort.Strings(edgeLines)
		sb.WriteString(strings.Join(edgeLines, "\n"))
		sb.WriteByte('\n')
	}
	if len(b.Anchors) > 0 {
		sb.WriteString("\n【공통 부모 / 컨텍스트 기준점(압축 대상이 아니며, 어느 의도에서 이 결과를 얻었는지 이해하기 위한 정보)】: \n")
		for _, a := range b.Anchors {
			n := nodeByID[a]
			state := ""
			if n != nil {
				state = n.State
			}
			fmt.Fprintf(&sb, "- #%d [%s] %s\n", a, state, nodeSummary(n))
		}
	}
	return sb.String()
}

// Keep the existing compression input budget, but partition at whole-node
// boundaries instead of discarding the middle of detail/evidence. A node larger
// than the budget remains available as an original and never reaches the model
// as an incomplete summary source.
const compressionInputRunes = 24_000

func completeCompressionBlocks(g *coldGraph, blocks []block, nodes map[int64]*db.Node, minMembers int) []block {
	var out []block
	for _, source := range blocks {
		current := block{Anchors: source.Anchors}
		flush := func() {
			if len(current.Members) >= minMembers {
				out = append(out, current)
			}
			current = block{Anchors: source.Anchors}
		}
		for _, id := range source.Members {
			single := block{Members: []int64{id}, Anchors: source.Anchors}
			if len([]rune(buildCompressionInput(g, single, nodes))) > compressionInputRunes {
				continue
			}
			candidate := block{Members: append(append([]int64(nil), current.Members...), id), Anchors: source.Anchors}
			if len([]rune(buildCompressionInput(g, candidate, nodes))) > compressionInputRunes {
				flush()
				current = single
			} else {
				current = candidate
			}
		}
		flush()
	}
	return out
}

// compress runs the §4 body LLM call on one block. Uses the same model the agent
// runs on; thinking disabled (a pure summarization step).
func (c *Compactor) compress(ctx context.Context, g *coldGraph, b block, nodeByID map[int64]*db.Node) (string, error) {
	input := buildCompressionInput(g, b, nodeByID)
	if len([]rune(input)) > compressionInputRunes {
		return "", nil
	}
	req := llm.CompletionRequest{
		System:   []string{compressionSystemPrompt},
		Messages: []llm.Message{llm.UserText(input)},
		Thinking: "disabled",
	}
	msg, stopReason, _, err := c.prov.Complete(ctx, req)
	if err != nil {
		return "", err
	}
	if stopReason != "end_turn" {
		return "", fmt.Errorf("graph summary did not complete normally (stop_reason=%q)", stopReason)
	}
	for _, block := range msg.Content {
		if block.Type == llm.BlockToolUse {
			return "", fmt.Errorf("graph summary unexpectedly requested a tool")
		}
	}
	body := strings.TrimSpace(msg.Text())
	if body == "" {
		return "", fmt.Errorf("empty body from model")
	}
	return body, nil
}

// compressionSystemPrompt is the §4 body prompt.
const compressionSystemPrompt = `서로 연관된 탐색 노드 묶음을 압축하여 종합 결론(body)을 작성합니다. 계획자가 "이 영역에서 무엇을 확인했는지" 빠르게 파악할 수 있어야 합니다.

입력은 연결된 부분 그래프입니다:
- 노드: 의도 또는 사실의 summary와 id, 유형(intent/fact), state, confidence 및 detail/evidence/과거 follow_ups의 원문이 포함됩니다. 확인한 결론과 아직 검증하지 않은 질문·전제를 구분하여 보존하세요.
- 관계: 노드 간 계보 관계(A가 B에서 파생 / A가 B를 생성)로 연결 방식을 보여 줍니다.
- 노드 간 직접 계보 관계가 없지만 같은 상위 의도에 속한다면(해당 의도를 "공통 부모 #p"로 별도 제공), "이 의도(#p)가 무엇을 알아냈는가"를 기준으로 종합합니다. 공통 부모는 컨텍스트 기준점이며 압축 대상 구성원이 아닙니다.

이를 바탕으로 body를 작성하세요:
1. 나열하지 말고 종합하세요. 관계를 따라 인과를 연결하여(어떤 사실이 어떤 의도를 낳았고, 어떤 의도가 어떤 결론을 냈는지) "이 탐색 영역에서 얻은 결론"을 설명하세요. 각 summary를 그대로 옮기지 마세요.
2. 차이를 보존하세요. 서로 다른 결론은 각각 명확히 설명하고 모호한 한 문장으로 합치지 마세요.
3. 증거의 강도를 보존하세요. confidence가 있는 결론은 observed / inferred를 표시하세요. inferred인 부정적이거나 불확실한 결론은 추론이며 재검증할 수 있음을 밝히고 확정된 결론처럼 쓰지 마세요.
4. id를 포함하세요. 각 결론 뒤에 출처 노드 id(예: "…(#12,#28)")를 표시하여 계획자가 원본 노드를 찾아볼 수 있게 하세요.
5. 입력에 있는 내용만 직접 서술하세요. 추측하거나 입력에 없는 판단을 추가하지 마세요.
   과거 follow_ups가 있으면 출처와 내용을 보존하되 별도 상태 큐로 해석하지 마세요. intent가 done이라는 이유로 detail에 남은 질문까지 검증됐다고 해석하지 마세요.
6. 중복을 합쳐 전체 입력보다 짧게 쓰되, 요약 길이를 줄이려고 검증 근거·제약·후속 단서·출처 ID를 없애지 마세요. 서로 다른 결론은 구분해서 보존하세요.

body 본문만 출력하세요.`
