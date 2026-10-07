package selection

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kogen-go/internal/acceptance"
	acceptancecommand "kogen-go/internal/acceptance/command"
	"kogen-go/internal/build/repair"
	"kogen-go/internal/contract"
	"kogen-go/internal/gate"
	"kogen-go/internal/gitio"
	"kogen-go/internal/process"
	"kogen-go/internal/testkit"
)

func TestRankingUsesApprovedCompletionThenBlockersDiffAndRank(t *testing.T) {
	tests := []struct {
		name string
		a    CandidateSnapshot
		b    CandidateSnapshot
		want bool
	}{
		{
			name: "more approved items pass before fewer blockers",
			a:    CandidateSnapshot{PassedItems: 3, TotalItems: 3, AllApprovedItemsPass: true, BlockingCountKnown: true, BlockingCount: 8},
			b:    CandidateSnapshot{PassedItems: 2, TotalItems: 3, BlockingCountKnown: true, BlockingCount: 0},
			want: true,
		},
		{
			name: "same passing count proceeds to blockers",
			a:    CandidateSnapshot{PassedItems: 1, TotalItems: 2, BlockingCountKnown: true, BlockingCount: 0},
			b:    CandidateSnapshot{PassedItems: 1, TotalItems: 1, AllApprovedItemsPass: true, BlockingCountKnown: true, BlockingCount: 1},
			want: true,
		},
		{
			name: "known blocking observation before unknown",
			a:    CandidateSnapshot{BlockingCountKnown: true, BlockingCount: 1},
			b:    CandidateSnapshot{BlockingCountKnown: false},
			want: true,
		},
		{
			name: "fewer blockers",
			a:    CandidateSnapshot{BlockingCountKnown: true, BlockingCount: 1},
			b:    CandidateSnapshot{BlockingCountKnown: true, BlockingCount: 2},
			want: true,
		},
		{
			name: "smaller diff",
			a:    CandidateSnapshot{BlockingCountKnown: true, DiffLines: 4},
			b:    CandidateSnapshot{BlockingCountKnown: true, DiffLines: 5},
			want: true,
		},
		{
			name: "earlier rung rank",
			a:    CandidateSnapshot{BlockingCountKnown: true, DiffLines: 5, Rank: 1, AttemptOrder: 2},
			b:    CandidateSnapshot{BlockingCountKnown: true, DiffLines: 5, Rank: 2, AttemptOrder: 1},
			want: true,
		},
		{
			name: "earlier stable attempt within rung",
			a:    CandidateSnapshot{BlockingCountKnown: true, DiffLines: 5, Rank: 1, AttemptOrder: 1},
			b:    CandidateSnapshot{BlockingCountKnown: true, DiffLines: 5, Rank: 1, AttemptOrder: 2},
			want: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := better(test.a, test.b); got != test.want {
				t.Fatalf("better(a,b) = %t, want %t", got, test.want)
			}
			if got := better(test.b, test.a); got == test.want {
				t.Fatalf("reverse comparison unexpectedly returned %t", got)
			}
		})
	}
}

func TestSelectKeepsPerRungAndBestUnverifiedCapSnapshots(t *testing.T) {
	longDiff := []byte("diff --git a/large b/large\n--- a/large\n+++ b/large\n@@ -1 +1,2 @@\n-old\n+new\n+extra\n")
	shortDiff := []byte("diff --git a/small b/small\n+new\n")
	input := []Candidate{
		{Rung: "R2", Rank: 2, AttemptOrder: 2, Ref: "refs/kogen/candidates/0123456789abcdef0123456789abcdef/R2", Diff: shortDiff},
		{Rung: "R1", Rank: 1, AttemptOrder: 1, Ref: "refs/kogen/candidates/0123456789abcdef0123456789abcdef/R1", Diff: longDiff, SnapshotReason: repair.ReasonBudget},
	}

	report, err := Select(input)
	if err != nil {
		t.Fatal(err)
	}
	if report.Winner.Rung != "R2" || report.BestUnverified == nil || report.BestUnverified.Rung != "R2" {
		t.Fatalf("winner/best unverified = %+v / %+v, want R2", report.Winner, report.BestUnverified)
	}
	if report.Winner.DiffPath != "candidate-R2.diff" || report.BestDiffPath != BestDiffFilename || string(report.BestDiff) != string(shortDiff) {
		t.Fatalf("selected diff outputs = winner:%+v path:%q bytes:%q", report.Winner, report.BestDiffPath, report.BestDiff)
	}
	if len(report.Candidates) != 2 || report.Candidates[0].Rung != "R1" || report.Candidates[1].Rung != "R2" {
		t.Fatalf("artifact order = %+v, want attempt rank order", report.Candidates)
	}
	if report.Candidates[0].DiffPath != "candidate-R1.diff" || !report.Candidates[0].CapSnapshot || report.Candidates[0].SnapshotReason != string(repair.ReasonBudget) {
		t.Fatalf("cap candidate snapshot was lost: %+v", report.Candidates[0])
	}
	if report.Candidates[0].DiffLines != 3 || report.Candidates[1].DiffLines != 1 {
		t.Fatalf("diff line metrics = R1:%d R2:%d, want changed lines excluding headers", report.Candidates[0].DiffLines, report.Candidates[1].DiffLines)
	}
	if report.Demoted || report.AdvisoryItems == nil || len(report.AdvisoryItems) != 0 || report.AuditMode != AuditModeObservational {
		t.Fatalf("report did not pin observational policy fields: %+v", report)
	}

	// The returned snapshots own their bytes so later workspace cleanup or
	// reuse of the caller's buffer cannot change the selected output.
	shortDiff[0] = 'X'
	if report.BestDiff[0] != 'd' || report.Candidates[1].Diff[0] != 'd' {
		t.Fatal("selection retained an aliased diff buffer")
	}

	event, err := report.Event(123)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["event"] != "selection" || fields["winner_rung"] != "R2" || fields["demoted"] != false {
		t.Fatalf("selection event fields = %s", encoded)
	}
	if advisory, ok := fields["advisory_items"].([]any); !ok || len(advisory) != 0 {
		t.Fatalf("event advisory_items = %#v, want []", fields["advisory_items"])
	}
	if ranking, ok := fields["ranking"].([]any); !ok || len(ranking) != 2 {
		t.Fatalf("event ranking = %#v, want both candidate snapshots", fields["ranking"])
	}
}

func TestCandidateRefAndDiffNameValidateRungIdentity(t *testing.T) {
	ref, err := CandidateRef("0123456789abcdef0123456789abcdef", "R1-2")
	if err != nil || ref != "refs/kogen/candidates/0123456789abcdef0123456789abcdef/R1-2" {
		t.Fatalf("candidate ref = %q, %v", ref, err)
	}
	if _, err := CandidateRef("../../unsafe", "R1"); !errors.Is(err, ErrInvalidRunIdentity) {
		t.Fatalf("invalid run identity error = %v", err)
	}
	if _, err := DiffFilename("../escape"); !errors.Is(err, ErrInvalidCandidate) {
		t.Fatalf("invalid rung path error = %v", err)
	}
	if _, err := CandidateRef("0123456789abcdef0123456789abcdef", "R1.lock"); !errors.Is(err, ErrInvalidRunIdentity) {
		t.Fatalf("invalid Git ref component error = %v", err)
	}
}

func TestSelectRejectsDuplicateAttemptOrdersAndNoCandidates(t *testing.T) {
	if _, err := Select(nil); !errors.Is(err, ErrNoCandidates) {
		t.Fatalf("empty selection error = %v", err)
	}
	_, err := Select([]Candidate{
		{Rung: "R1", Rank: 1, AttemptOrder: 1},
		{Rung: "R2", Rank: 2, AttemptOrder: 1},
	})
	if !errors.Is(err, ErrInvalidCandidate) {
		t.Fatalf("duplicate attempt order error = %v", err)
	}
	if _, err := Select([]Candidate{
		{Rung: "R1", Rank: 1, AttemptOrder: 1},
		{Rung: "R1-2", Rank: 1, AttemptOrder: 2},
	}); err != nil {
		t.Fatalf("repeated rung attempts with distinct stable order were rejected: %v", err)
	}
}

func TestSelectUsesGateObservationsAndIgnoresAuditTiming(t *testing.T) {
	allPass := selectionGateReport(t, map[string]bool{"A1": true, "A2": true})
	partial := selectionGateReport(t, map[string]bool{"A1": true, "A2": false})
	if !allPass.IsVerified() || partial.IsVerified() {
		t.Fatalf("gate fixtures are not green/red: all=%t partial=%t", allPass.IsVerified(), partial.IsVerified())
	}

	candidates := []Candidate{
		{Rung: "R1", Rank: 1, AttemptOrder: 1, Ref: "refs/kogen/candidates/0123456789abcdef0123456789abcdef/R1", Diff: []byte("small\n"), Gate: partial},
		{Rung: "R2", Rank: 2, AttemptOrder: 2, Ref: "refs/kogen/candidates/0123456789abcdef0123456789abcdef/R2", Diff: []byte("larger\nextra\n"), Gate: allPass},
	}
	before, err := Select(candidates)
	if err != nil {
		t.Fatal(err)
	}
	if before.Winner.Rung != "R2" || !before.Winner.AllApprovedItemsPass || before.Winner.PassedItems != 2 || before.Winner.TotalItems != 2 {
		t.Fatalf("all-approved selection = %+v, want R2 with both items passed", before.Winner)
	}

	// The gate's public advice channel can change after selection input is
	// captured. Selection reads only immutable gate counts and checks.
	allPass.RecordAuditAdvice([]gate.AuditAdvice{{ID: "A2", Verdict: "over_strict", Reason: "too strict"}})
	partial.RecordAuditAdvice([]gate.AuditAdvice{{ID: "A2", Verdict: "contradicts", Reason: "not required"}})
	after, err := Select(candidates)
	if err != nil {
		t.Fatal(err)
	}
	if after.Winner != before.Winner || after.BestDiffPath != before.BestDiffPath || string(after.BestDiff) != string(before.BestDiff) {
		t.Fatalf("audit advice changed winner/output: before=%+v after=%+v", before.Winner, after.Winner)
	}
	if after.Demoted || len(after.AdvisoryItems) != 0 {
		t.Fatalf("audit advice changed fixed report fields: %+v", after)
	}
}

type selectionAcceptanceRunner struct{ itemPass map[string]bool }

func (r selectionAcceptanceRunner) Run(context.Context, gate.AcceptanceExecution) (acceptance.Result, error) {
	itemPass := make(map[string]bool, len(r.itemPass))
	for id, passed := range r.itemPass {
		itemPass[id] = passed
	}
	return acceptance.Result{
		Process:  contract.ProcessResult{ExitStatus: selectionIntPointer(0)},
		ItemPass: itemPass,
	}, nil
}

func selectionGateReport(t *testing.T, itemPass map[string]bool) *gate.GateReport {
	t.Helper()
	fixture := testkit.NewGitFixture(t)
	selectionWriteFile(t, filepath.Join(fixture.Checkout, ".kogen/acceptance/greet.sh"), []byte("approved acceptance source\n"), 0o644)
	selectionWriteFile(t, filepath.Join(fixture.Checkout, ".kogen/intents/greet/intent.md"), []byte("approved intent\n"), 0o644)
	selectionWriteFile(t, filepath.Join(fixture.Checkout, "lib/app.txt"), []byte("base\n"), 0o644)
	fixture.Run(t, "add", ".")
	fixture.Run(t, "commit", "--quiet", "--message", "selection gate fixture")
	fixture.Run(t, "push", "--quiet", "origin", "main")

	base := filepath.Join(fixture.Root, "base")
	candidate := filepath.Join(fixture.Root, "candidate")
	fixture.RunIn(t, fixture.Root, "clone", "--quiet", fixture.Origin, base)
	fixture.RunIn(t, fixture.Root, "clone", "--quiet", fixture.Origin, candidate)
	git := gitio.NewWorkspace(process.Supervisor{})
	environment := make(process.Environment)
	for _, entry := range fixture.Environment() {
		key, value, found := strings.Cut(entry, "=")
		if found {
			environment[key] = value
		}
	}
	policy := gitio.WorkspacePolicy(candidate, environment)
	commit := strings.TrimSpace(string(fixture.RunIn(t, candidate, "rev-parse", "HEAD")))
	metadata, err := gitio.LoadBaseMetadata(context.Background(), git, policy, contract.ObjectID(commit))
	if err != nil {
		t.Fatalf("load selection base metadata: %v", err)
	}
	trees := acceptance.CandidateTree{Git: git, Policy: policy, Base: metadata}
	baseTree, err := trees.Snapshot(context.Background(), base)
	if err != nil {
		t.Fatalf("snapshot selection base tree: %v", err)
	}
	runDir := filepath.Join(fixture.Root, "run")
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatalf("create selection run directory: %v", err)
	}
	approved := []byte("approved acceptance source\n")
	report, err := gate.Run(context.Background(), gate.Request{
		Processes:          process.Supervisor{},
		AcceptanceRunner:   selectionAcceptanceRunner{itemPass: itemPass},
		Trees:              trees,
		BaseWorkspace:      base,
		CandidateWorkspace: candidate,
		RunDir:             runDir,
		ExpectedBaseTree:   baseTree,
		ApprovalSHA256:     strings.Repeat("a", 64),
		Acceptance: gate.AcceptancePlan{
			Request: acceptancecommand.Request{
				Config: acceptancecommand.Config{
					Extension: ".sh", CandidateDir: "test/acceptance",
					Run: []string{"sh", "{path}"}, Timeout: time.Second,
				},
				Slug: "greet", ExpectedItems: []string{"A1", "A2"},
			},
			ApprovedBytes: approved,
			ChangeItems:   []string{"A1"},
		},
	})
	if err != nil {
		t.Fatalf("run selection gate fixture: %v", err)
	}
	return report
}

func selectionWriteFile(t *testing.T, filename string, contents []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatalf("create selection fixture parent: %v", err)
	}
	if err := os.WriteFile(filename, contents, mode); err != nil {
		t.Fatalf("write selection fixture file: %v", err)
	}
}

func selectionIntPointer(value int) *int { return &value }
