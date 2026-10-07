package selection

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"kogen-go/internal/build/repair"
	"kogen-go/internal/gate"
	"kogen-go/internal/journal"
)

const (
	AuditModeObservational = "observational"
	BestDiffFilename       = "candidate.diff"
)

var (
	ErrNoCandidates       = errors.New("build selection: no candidates")
	ErrInvalidCandidate   = errors.New("build selection: candidate is incomplete or inconsistent")
	ErrInvalidRunIdentity = errors.New("build selection: run identity or rung is invalid")
	rungNamePattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	runIDPattern          = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// Candidate is one completed rung observation. Diff contains the exact
// captured unified diff bytes for that rung. Gate is the real verification
// report; a nil report represents an unverified snapshot with no measured
// gate count. SnapshotReason is copied from the rung controller so cap-time
// output remains available to the report.
//
// Rank is the one-based recipe rung index. AttemptOrder is the stable order
// assigned when an attempt starts; it is not derived from completion or audit
// timing and breaks ties between repeated attempts.
type Candidate struct {
	Rung           string
	Rank           int
	AttemptOrder   int
	Ref            string
	Diff           []byte
	Gate           *gate.GateReport
	SnapshotReason repair.Reason
}

// CandidateSnapshot is a detached record of the candidate and its observed
// gate state. Diff is kept for the caller to publish as a per-rung artifact
// and is omitted from JSON reports; DiffPath is the corresponding run-relative
// artifact name. DiffLines counts added plus removed implementation lines.
type CandidateSnapshot struct {
	Rung                 string       `json:"rung"`
	Rank                 int          `json:"rank"`
	AttemptOrder         int          `json:"attempt_order"`
	Ref                  string       `json:"ref,omitempty"`
	DiffPath             string       `json:"diff_path"`
	Diff                 []byte       `json:"-"`
	DiffLines            int          `json:"diff_lines"`
	PassedItems          int          `json:"passed_items"`
	TotalItems           int          `json:"total_items"`
	AllApprovedItemsPass bool         `json:"all_approved_items_pass"`
	BlockingCount        int          `json:"blocking_count"`
	BlockingCountKnown   bool         `json:"blocking_count_known"`
	Verified             bool         `json:"verified"`
	Verdict              gate.Verdict `json:"verdict"`
	CapSnapshot          bool         `json:"cap_snapshot"`
	SnapshotReason       string       `json:"snapshot_reason,omitempty"`
}

// CandidateSummary is the compact public report view for a candidate.
type CandidateSummary struct {
	Rung                 string       `json:"rung"`
	Rank                 int          `json:"rank"`
	AttemptOrder         int          `json:"attempt_order"`
	Ref                  string       `json:"ref,omitempty"`
	DiffPath             string       `json:"diff_path"`
	DiffLines            int          `json:"diff_lines"`
	PassedItems          int          `json:"passed_items"`
	TotalItems           int          `json:"total_items"`
	AllApprovedItemsPass bool         `json:"all_approved_items_pass"`
	BlockingCount        int          `json:"blocking_count"`
	BlockingCountKnown   bool         `json:"blocking_count_known"`
	Verified             bool         `json:"verified"`
	Verdict              gate.Verdict `json:"verdict"`
	CapSnapshot          bool         `json:"cap_snapshot"`
	SnapshotReason       string       `json:"snapshot_reason,omitempty"`
}

// Report contains every rung snapshot, the deterministic ranking, the
// overall winner, and the best unverified output for failed/stopped Builds.
// The audit fields are fixed policy values and are never populated from model
// advice.
type Report struct {
	Candidates     []CandidateSnapshot `json:"candidates"`
	Ranking        []CandidateSummary  `json:"ranking"`
	Winner         CandidateSummary    `json:"winner"`
	BestUnverified *CandidateSummary   `json:"best_unverified,omitempty"`
	BestDiff       []byte              `json:"-"`
	BestDiffPath   string              `json:"best_diff_path"`
	AuditMode      string              `json:"audit_mode"`
	Demoted        bool                `json:"demoted"`
	AdvisoryItems  []string            `json:"advisory_items"`
}

// Select snapshots all supplied rung outputs and orders them by: most approved
// acceptance items passing, fewer blocking observations, fewer added/removed
// implementation lines, then earlier rung and stable attempt order. Audit
// advice is not an input and cannot change the winner, score, candidate
// references, or captured diff bytes.
func Select(candidates []Candidate) (Report, error) {
	if len(candidates) == 0 {
		return Report{}, ErrNoCandidates
	}

	snapshots := make([]CandidateSnapshot, 0, len(candidates))
	seenOrders := make(map[int]struct{}, len(candidates))
	seenRungs := make(map[string]struct{}, len(candidates))
	totalItems := -1
	for _, candidate := range candidates {
		snapshot, err := snapshotCandidate(candidate)
		if err != nil {
			return Report{}, err
		}
		if _, exists := seenOrders[candidate.AttemptOrder]; exists {
			return Report{}, fmt.Errorf("%w: duplicate attempt order %d", ErrInvalidCandidate, candidate.AttemptOrder)
		}
		if _, exists := seenRungs[candidate.Rung]; exists {
			return Report{}, fmt.Errorf("%w: duplicate rung %q", ErrInvalidCandidate, candidate.Rung)
		}
		seenOrders[candidate.AttemptOrder] = struct{}{}
		seenRungs[candidate.Rung] = struct{}{}
		if candidate.Gate != nil {
			counts := candidate.Gate.Counts()
			if counts.Total < 1 || counts.Passed < 0 || counts.Passed > counts.Total {
				return Report{}, fmt.Errorf("%w: invalid acceptance counts for %q", ErrInvalidCandidate, candidate.Rung)
			}
			if totalItems >= 0 && totalItems != counts.Total {
				return Report{}, fmt.Errorf("%w: approved item totals differ across candidates", ErrInvalidCandidate)
			}
			totalItems = counts.Total
		}
		snapshots = append(snapshots, snapshot)
	}

	// Preserve artifact order by rung and stable attempt order, independent of
	// completion order. Ranking below is a separate score order.
	artifacts := cloneSnapshots(snapshots)
	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].Rank != artifacts[j].Rank {
			return artifacts[i].Rank < artifacts[j].Rank
		}
		return artifacts[i].AttemptOrder < artifacts[j].AttemptOrder
	})

	ranked := cloneSnapshots(snapshots)
	sort.Slice(ranked, func(i, j int) bool { return better(ranked[i], ranked[j]) })
	if len(ranked) == 0 {
		return Report{}, ErrNoCandidates
	}

	winner := summarize(ranked[0])
	report := Report{
		Candidates:    artifacts,
		Ranking:       make([]CandidateSummary, len(ranked)),
		Winner:        winner,
		BestDiff:      bytes.Clone(ranked[0].Diff),
		BestDiffPath:  BestDiffFilename,
		AuditMode:     AuditModeObservational,
		Demoted:       false,
		AdvisoryItems: []string{},
	}
	for i, candidate := range ranked {
		report.Ranking[i] = summarize(candidate)
		if !candidate.Verified && report.BestUnverified == nil {
			best := summarize(candidate)
			report.BestUnverified = &best
		}
	}
	return report, nil
}

// CandidateRef returns the create-only per-rung candidate ref name for one
// Build identity. The caller still publishes the ref through its supervised
// Git port and compare-and-swap policy.
func CandidateRef(runID, rung string) (string, error) {
	if !runIDPattern.MatchString(runID) || !validRungName(rung) {
		return "", ErrInvalidRunIdentity
	}
	return "refs/kogen/candidates/" + runID + "/" + rung, nil
}

// DiffFilename returns the fixed per-rung artifact name used for a captured
// candidate diff.
func DiffFilename(rung string) (string, error) {
	if !validRungName(rung) {
		return "", fmt.Errorf("%w: invalid rung %q", ErrInvalidCandidate, rung)
	}
	return "candidate-" + rung + ".diff", nil
}

// Event builds the journal selection event consumed by Build/status reporting.
// It records the full ordered ranking and fixed observational audit fields.
func (r Report) Event(ts int64) (journal.RunEvent, error) {
	if r.Winner.Rung == "" || len(r.Ranking) == 0 {
		return journal.RunEvent{}, ErrNoCandidates
	}
	if r.Demoted || r.AdvisoryItems == nil || len(r.AdvisoryItems) != 0 || r.AuditMode != AuditModeObservational {
		return journal.RunEvent{}, fmt.Errorf("%w: selection report is not observational", ErrInvalidCandidate)
	}
	event := journal.NewRunEvent("selection", ts)
	fields := []struct {
		key   string
		value any
	}{
		{key: "winner_rung", value: r.Winner.Rung},
		{key: "winner_ref", value: r.Winner.Ref},
		{key: "ranking", value: r.Ranking},
		{key: "best_candidate", value: map[string]any{
			"rung": r.Winner.Rung, "ref": r.Winner.Ref,
			"diff_path": r.BestDiffPath, "verdict": r.Winner.Verdict,
		}},
		{key: "mode", value: r.AuditMode},
		{key: "demoted", value: false},
		{key: "advisory_items", value: []string{}},
	}
	if r.BestUnverified != nil {
		fields = append(fields, struct {
			key   string
			value any
		}{key: "best_unverified", value: *r.BestUnverified})
	}
	for _, field := range fields {
		if err := event.Set(field.key, field.value); err != nil {
			return journal.RunEvent{}, err
		}
	}
	return event, nil
}

func snapshotCandidate(candidate Candidate) (CandidateSnapshot, error) {
	if !validRungName(candidate.Rung) || candidate.Rank < 1 || candidate.AttemptOrder < 1 {
		return CandidateSnapshot{}, fmt.Errorf("%w: rung, positive rank, and positive attempt order are required", ErrInvalidCandidate)
	}
	if candidate.Ref != "" && !validCandidateRef(candidate.Ref) {
		return CandidateSnapshot{}, fmt.Errorf("%w: candidate ref is malformed", ErrInvalidCandidate)
	}
	filename, err := DiffFilename(candidate.Rung)
	if err != nil {
		return CandidateSnapshot{}, err
	}
	snapshot := CandidateSnapshot{
		Rung:           candidate.Rung,
		Rank:           candidate.Rank,
		AttemptOrder:   candidate.AttemptOrder,
		Ref:            candidate.Ref,
		DiffPath:       filename,
		Diff:           bytes.Clone(candidate.Diff),
		DiffLines:      changedLineCount(candidate.Diff),
		Verdict:        gate.VerdictUnverified,
		CapSnapshot:    isCapReason(candidate.SnapshotReason),
		SnapshotReason: string(candidate.SnapshotReason),
	}
	if candidate.Gate == nil {
		return snapshot, nil
	}

	counts := candidate.Gate.Counts()
	snapshot.PassedItems = counts.Passed
	snapshot.TotalItems = counts.Total
	snapshot.AllApprovedItemsPass = counts.Total > 0 && counts.Passed == counts.Total
	snapshot.Verified = candidate.Gate.IsVerified()
	snapshot.Verdict = candidate.Gate.Verdict()
	snapshot.BlockingCount = repair.RedCount(candidate.Gate)
	snapshot.BlockingCountKnown = true
	// RedCount intentionally counts repair-progress observations. A gate can
	// still be unverified for a blocking condition outside that progress count
	// (for example, an unstable tree); retain at least one blocker in the score.
	if !snapshot.Verified && snapshot.BlockingCount == 0 {
		snapshot.BlockingCount = 1
	}
	return snapshot, nil
}

func validRungName(rung string) bool {
	if !rungNamePattern.MatchString(rung) || strings.Contains(rung, "..") || strings.HasSuffix(rung, ".") {
		return false
	}
	return !strings.HasSuffix(strings.ToLower(rung), ".lock")
}

func validCandidateRef(ref string) bool {
	const prefix = "refs/kogen/candidates/"
	rest := strings.TrimPrefix(ref, prefix)
	if rest == ref {
		return false
	}
	parts := strings.Split(rest, "/")
	return len(parts) == 2 && runIDPattern.MatchString(parts[0]) && validRungName(parts[1])
}

func better(a, b CandidateSnapshot) bool {
	if a.PassedItems != b.PassedItems {
		return a.PassedItems > b.PassedItems
	}
	if a.BlockingCountKnown != b.BlockingCountKnown {
		return a.BlockingCountKnown
	}
	if a.BlockingCount != b.BlockingCount {
		return a.BlockingCount < b.BlockingCount
	}
	if a.DiffLines != b.DiffLines {
		return a.DiffLines < b.DiffLines
	}
	if a.Rank != b.Rank {
		return a.Rank < b.Rank
	}
	return a.AttemptOrder < b.AttemptOrder
}

func changedLineCount(diff []byte) int {
	count := 0
	for _, line := range bytes.Split(diff, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		if (line[0] == '+' && !bytes.HasPrefix(line, []byte("+++"))) ||
			(line[0] == '-' && !bytes.HasPrefix(line, []byte("---"))) {
			count++
		}
	}
	return count
}

func isCapReason(reason repair.Reason) bool {
	switch reason {
	case repair.ReasonRepairCap, repair.ReasonTurnCap, repair.ReasonWallCap,
		repair.ReasonBudget, repair.ReasonProtectedRestoreLimit:
		return true
	default:
		return false
	}
}

func summarize(candidate CandidateSnapshot) CandidateSummary {
	return CandidateSummary{
		Rung: candidate.Rung, Rank: candidate.Rank, AttemptOrder: candidate.AttemptOrder, Ref: candidate.Ref,
		DiffPath: candidate.DiffPath, DiffLines: candidate.DiffLines,
		PassedItems: candidate.PassedItems, TotalItems: candidate.TotalItems,
		AllApprovedItemsPass: candidate.AllApprovedItemsPass,
		BlockingCount:        candidate.BlockingCount, BlockingCountKnown: candidate.BlockingCountKnown,
		Verified: candidate.Verified, Verdict: candidate.Verdict,
		CapSnapshot: candidate.CapSnapshot, SnapshotReason: candidate.SnapshotReason,
	}
}

func cloneSnapshots(candidates []CandidateSnapshot) []CandidateSnapshot {
	cloned := make([]CandidateSnapshot, len(candidates))
	for i, candidate := range candidates {
		cloned[i] = candidate
		cloned[i].Diff = bytes.Clone(candidate.Diff)
	}
	return cloned
}
