package witness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"kogen-go/internal/contract"
	"kogen-go/internal/gate"
)

func TestDecodeAdjudicationsKeepsOnlyUsableExpectedRows(t *testing.T) {
	rows := DecodeAdjudications([]byte(`{"items":[{"id":"A1","verdict":"TEST-WRONG","citation":"assertion A1","reason":"The fixture contradicts the Request."},{"id":"A2","verdict":"WITNESS-WRONG","citation":"line 4","reason":"The implementation is incomplete."},{"id":"A3","verdict":"UNDECIDED","citation":"output","reason":"Evidence is insufficient."},{"id":"unknown","verdict":"TEST-WRONG","citation":"x","reason":"ignored"}]}`), []string{"A1", "A2", "A3", "A4"})
	if len(rows) != 4 {
		t.Fatalf("got %d judgments, want 4: %+v", len(rows), rows)
	}
	if rows[0].Verdict != VerdictTestWrong || rows[0].Citation != "assertion A1" || rows[1].Verdict != VerdictWitnessWrong {
		t.Fatalf("recognized judgments = %+v", rows[:2])
	}
	if rows[2].Verdict != VerdictUndecided || rows[3].Verdict != VerdictUndecided || rows[3].Reason == "" {
		t.Fatalf("open or missing rows were not preserved as undecided: %+v", rows[2:])
	}
}

func TestDecodeAdjudicationsMakesMalformedAndDuplicateRowsUndecided(t *testing.T) {
	for _, input := range []string{
		`not-json`,
		`{"items":[{"id":"A1","verdict":"TEST-WRONG","citation":"c","reason":"r"},{"id":"A1","verdict":"WITNESS-WRONG","citation":"c","reason":"r"}]}`,
		`{"items":[{"id":"A1","verdict":"maybe","citation":"c","reason":"r"}]}`,
		`{"items":[{"id":"A1","verdict":"TEST-WRONG","reason":"r"}]}`,
	} {
		rows := DecodeAdjudications([]byte(input), []string{"A1"})
		if len(rows) != 1 || rows[0].Verdict != VerdictUndecided {
			t.Fatalf("input %q decoded as %+v", input, rows)
		}
	}
}

func TestRepairPlanPreservesThreeDistinctRedAdjudications(t *testing.T) {
	repairs, warnings := repairPlan([]Judgment{
		{ID: "A1", Verdict: VerdictTestWrong, Citation: "test line 3", Reason: "test claim is false"},
		{ID: "A2", Verdict: VerdictWitnessWrong, Citation: "source line 8", Reason: "implementation misses the requirement"},
		{ID: "A3", Verdict: VerdictUndecided, Citation: "output", Reason: "insufficient evidence"},
	})
	if len(repairs) != 2 || repairs[0].ID != "A1" || repairs[0].Scope != RepairTest || repairs[1].ID != "A2" || repairs[1].Scope != RepairWitness {
		t.Fatalf("repair plan = %+v", repairs)
	}
	if len(warnings) != 1 || warnings[0].Code != "feasibility_concern" || warnings[0].ItemIDs[0] != "A3" {
		t.Fatalf("undecided warnings = %+v", warnings)
	}
}

func TestAdjudicatorPromptUsesMarkerAndNoToolContract(t *testing.T) {
	prompt := AdjudicatorInstructions()
	for _, part := range []string{"You are Kogen's acceptance test auditor.", "TEST-WRONG|WITNESS-WRONG|UNDECIDED", `"citation"`, `"reason"`} {
		if !contains(prompt, part) {
			t.Fatalf("adjudicator instructions omit %q: %s", part, prompt)
		}
	}
}

func TestBudgetReservesAggregateOutputAcrossConcurrentRungs(t *testing.T) {
	budget := NewBudget(100)
	left, err := budget.ReserveOutput(60)
	if err != nil {
		t.Fatal(err)
	}
	right, err := budget.ReserveOutput(40)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := budget.ReserveOutput(1); !errors.Is(err, ErrOutputBudgetExceeded) {
		t.Fatalf("over-budget concurrent reservation error = %v", err)
	}
	usedLeft, usedRight := int64(55), int64(39)
	if err := left.Complete(&usedLeft); err != nil {
		t.Fatal(err)
	}
	if err := right.Complete(&usedRight); err != nil {
		t.Fatal(err)
	}
	if got := budget.RemainingOutputTokens(); got != 6 {
		t.Fatalf("remaining budget = %d, want 6", got)
	}
	if err := budget.Finish(); err != nil {
		t.Fatal(err)
	}
}

func TestBudgetUnknownUsageStopsFurtherDispatch(t *testing.T) {
	budget := NewBudget(100)
	reservation, err := budget.ReserveOutput(100)
	if err != nil {
		t.Fatal(err)
	}
	if err := reservation.Complete(nil); !errors.Is(err, ErrOutputUsageUnknown) {
		t.Fatalf("unknown usage error = %v", err)
	}
	if got := budget.RemainingOutputTokens(); got != 0 {
		t.Fatalf("unknown usage left %d dispatchable tokens", got)
	}
	if _, err := budget.ReserveOutput(1); !errors.Is(err, ErrOutputUsageUnknown) {
		t.Fatalf("later dispatch error = %v", err)
	}
}

type bindingReaderStub struct {
	ref    string
	exists bool
	diff   []byte
	err    error
	refs   int
	reads  int
}

func (r *bindingReaderStub) WitnessRefTarget(_ context.Context, _ string) (string, bool, error) {
	r.refs++
	return r.ref, r.exists, r.err
}

func (r *bindingReaderStub) WitnessDiff(_ context.Context, _, _ string) ([]byte, error) {
	r.reads++
	return append([]byte(nil), r.diff...), r.err
}

func TestValidateApprovalBindsRefBaseAndExactDiff(t *testing.T) {
	diff := []byte("diff --git a/main.go b/main.go\n")
	digest := sha256.Sum256(diff)
	proof := &Proof{
		Verdict: VerdictProven, Commit: stringsOf('a', 40),
		DiffSHA256: hex.EncodeToString(digest[:]), BaseSHA: stringsOf('b', 40),
	}
	reader := &bindingReaderStub{ref: proof.Commit, exists: true, diff: diff}
	if err := ValidateApproval(context.Background(), ModeWitness, "alpha", proof.BaseSHA, proof, reader); err != nil {
		t.Fatal(err)
	}
	if reader.refs != 1 || reader.reads != 1 {
		t.Fatalf("binding reads = ref:%d diff:%d", reader.refs, reader.reads)
	}
	reader.diff = []byte("different bytes")
	err := ValidateApproval(context.Background(), ModeWitness, "alpha", proof.BaseSHA, proof, reader)
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Class != "intent" || failure.Reason != "unproven" || failure.Exit != 1 {
		t.Fatalf("mismatched proof failure = %#v", err)
	}
}

func TestValidateApprovalRefusesMissingOrUnprovenWitness(t *testing.T) {
	reader := &bindingReaderStub{}
	for _, proof := range []*Proof{
		nil,
		{Verdict: "UNPROVEN", Commit: stringsOf('a', 40), DiffSHA256: stringsOf('b', 64), BaseSHA: stringsOf('c', 40)},
	} {
		err := ValidateApproval(context.Background(), ModeWitness, "alpha", stringsOf('c', 40), proof, reader)
		var failure *contract.Failure
		if !errors.As(err, &failure) || failure.Reason != "unproven" {
			t.Fatalf("proof %v failure = %#v", proof, err)
		}
	}
	if reader.refs != 0 || reader.reads != 0 {
		t.Fatalf("invalid proofs reached Git: ref:%d diff:%d", reader.refs, reader.reads)
	}
}

type reverifyStub struct {
	applyErr error
	apply    int
	verify   int
	close    int
}

func (e *reverifyStub) Apply(context.Context, Proof, string) (BuildWorkspace, error) {
	e.apply++
	return "throwaway", e.applyErr
}

func (e *reverifyStub) Verify(context.Context, BuildWorkspace) (*gate.GateReport, error) {
	e.verify++
	return nil, nil
}

func (e *reverifyStub) Close(context.Context, BuildWorkspace) error {
	e.close++
	return nil
}

func TestReverifyConflictFallsThroughBeforeAnyVerifierCall(t *testing.T) {
	effects := &reverifyStub{applyErr: ErrApplyConflict}
	proof := &Proof{Verdict: VerdictProven, Commit: stringsOf('a', 40), DiffSHA256: stringsOf('b', 64), BaseSHA: stringsOf('c', 40)}
	result, err := Reverify(context.Background(), ReverifyRequest{
		Mode: ModeWitness, TargetBase: stringsOf('d', 40), TargetBaseTree: stringsOf('e', 40), Proof: proof,
	}, effects)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != ContinueLadder || effects.apply != 1 || effects.verify != 0 || effects.close != 1 {
		t.Fatalf("reverify result=%+v apply/verify/close=%d/%d/%d", result, effects.apply, effects.verify, effects.close)
	}
}

func TestRunIsStrictlyOptIn(t *testing.T) {
	var effects Effects
	result, err := Run(context.Background(), Request{Mode: ModeNone}, effects)
	if err != nil || result.Feasibility != FeasibilityNotChecked || result.Proof != nil {
		t.Fatalf("opt-out result=%+v err=%v", result, err)
	}
}

func stringsOf(value byte, count int) string {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return string(result)
}

func contains(text, part string) bool {
	return strings.Contains(text, part)
}
