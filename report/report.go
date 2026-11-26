// Package report renders a deliverable Markdown report from the exploration
// graph: confirmed findings (with severity/evidence) (docs §15 P5).
package report

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

// Input bundles what the report needs.
type Input struct {
	Title       string
	Goal        string
	GeneratedAt time.Time
	AssetCounts map[string]int
	Findings    []*db.Node
	// Meta maps a finding node id to its findings row: id, triage state and
	// evidence state. A node without a row is rendered without those lines.
	Meta map[int64]db.FindingMeta
}

type findingView struct {
	VulnClass string
	Name      string
	Severity  string
	Summary   string
	PoC       string
	Meta      *db.FindingMeta
}

// dismissedLabels names the triage states that take a finding out of the
// confirmed count, in the order the summary lists them.
var dismissedLabels = []struct{ status, label string }{
	{db.FindingFalsePositive, "오탐"}, {db.FindingDuplicate, "중복"}, {db.FindingIgnored, "무시"},
}

// findingRef links the finding id to its detail page, where the bound traffic
// and the detailed report are one step away.
func findingRef(id int64) string {
	return fmt.Sprintf("[%d](/function/findings/detail?id=%d)", id, id)
}

func parseFinding(n *db.Node) findingView {
	var p struct {
		VulnClass string `json:"vulnclass"`
		Name      string `json:"name"`
		Severity  string `json:"severity"`
		Summary   string `json:"summary"`
		Evidence  struct {
			PoC string `json:"poc"`
		} `json:"evidence"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return findingView{VulnClass: p.VulnClass, Name: p.Name, Severity: p.Severity, Summary: p.Summary, PoC: p.Evidence.PoC}
}

var sevRank = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "": 4}

// Markdown renders the report.
func Markdown(in Input) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 침투 테스트 보고서: %s\n\n", nz(in.Title, "이름 없는 작업"))
	fmt.Fprintf(&b, "- **작업 목표**: %s\n", nz(in.Goal, "(지정되지 않음)"))
	fmt.Fprintf(&b, "- **생성 시각**: %s\n\n", in.GeneratedAt.Format("2006-01-02 15:04:05"))

	// A finding the analyst dismissed is listed apart from the confirmed ones.
	var confirmed, dismissed []findingView
	for _, n := range in.Findings {
		f := parseFinding(n)
		if m, ok := in.Meta[n.ID]; ok {
			f.Meta = &m
		}
		if f.Meta != nil && db.FindingStatusDismissed(f.Meta.Status) {
			dismissed = append(dismissed, f)
		} else {
			confirmed = append(confirmed, f)
		}
	}
	bySeverity := func(fs []findingView) {
		sort.SliceStable(fs, func(i, j int) bool { return sevRank[fs[i].Severity] < sevRank[fs[j].Severity] })
	}
	bySeverity(confirmed)
	bySeverity(dismissed)
	title := func(f findingView) string {
		return fmt.Sprintf("[%s] %s", strings.ToUpper(nz(f.Severity, "info")), nz(f.Name, nz(f.VulnClass, "미분류")))
	}

	// summary
	fmt.Fprintf(&b, "## 요약\n\n")
	fmt.Fprintf(&b, "- 확인된 취약점: **%d**건\n", len(confirmed))
	if len(dismissed) > 0 {
		fmt.Fprintf(&b, "- 분류로 제외한 발견: %d건 (", len(dismissed))
		for i, d := range dismissedLabels {
			n := 0
			for _, f := range dismissed {
				if f.Meta.Status == d.status {
					n++
				}
			}
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%s %d", d.label, n)
		}
		b.WriteString(")\n")
	}
	fmt.Fprintf(&b, "- 자산: ")
	var types []string
	for t := range in.AssetCounts {
		types = append(types, t)
	}
	sort.Strings(types)
	if len(types) == 0 {
		b.WriteString("없음")
	}
	for i, t := range types {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s %d", t, in.AssetCounts[t])
	}
	b.WriteString("\n\n")

	// findings
	fmt.Fprintf(&b, "## 취약점\n\n")
	if len(confirmed) == 0 {
		b.WriteString("_이번에 확인된 취약점이 없습니다._\n\n")
	}
	for i, f := range confirmed {
		fmt.Fprintf(&b, "### %d. %s\n\n", i+1, title(f))
		if m := f.Meta; m != nil {
			fmt.Fprintf(&b, "- **취약점 ID**: %s\n- **상태**: %s\n", findingRef(m.ID), nz(m.Status, db.FindingPending))
			fmt.Fprintf(&b, "- **연결된 트래픽 증거**: %d건(증명·검증 %d건), 증거 버전 %d\n", m.TrafficCount, m.ProofCount, m.EvidenceVersion)
			if m.ReportStale() {
				b.WriteString("- 증거가 변경되어 상세 보고서를 갱신해야 합니다.\n")
			}
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s\n\n", nz(f.Summary, ""))
		if f.PoC != "" {
			fmt.Fprintf(&b, "**PoC / 증거:**\n\n```\n%s\n```\n\n", f.PoC)
		}
	}
	if len(dismissed) > 0 {
		fmt.Fprintf(&b, "## 분류로 제외한 발견\n\n")
		for _, f := range dismissed {
			fmt.Fprintf(&b, "- %s — 상태 %s, 취약점 ID %s\n", title(f), f.Meta.Status, findingRef(f.Meta.ID))
		}
		b.WriteString("\n")
	}

	return b.String()
}

func nz(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}
