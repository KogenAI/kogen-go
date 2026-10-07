package selection

import (
	"context"
	"fmt"
	"testing"

	buildaudit "kogen-go/internal/build/audit"
	"kogen-go/internal/contract"
	"kogen-go/internal/gate"
	"kogen-go/internal/provider/session"
)

type observationalAuditTransport struct{ reply string }

func (t observationalAuditTransport) Audit(context.Context, buildaudit.Prompt) (string, error) {
	return t.reply, nil
}

func TestAuditTimingAndParallelCompletionOrderCannotChangeRequiredA2OrWinner(t *testing.T) {
	redBefore := selectionGateReport(t, map[string]bool{"A1": true, "A2": false})
	redAfter := selectionGateReport(t, map[string]bool{"A1": true, "A2": false})
	green := selectionGateReport(t, map[string]bool{"A1": true, "A2": true})
	if redBefore.IsVerified() || redBefore.IsLandable() || redAfter.IsVerified() || redAfter.IsLandable() {
		t.Fatal("red A2 fixture unexpectedly passed verification or was landable")
	}
	if !green.IsVerified() || !green.IsLandable() {
		t.Fatal("fully passing repair fixture must be verified and landable")
	}

	// Audit before selection: an over_strict opinion about A2 remains advice;
	// its recorded verification still has one of two approved items passing.
	beforeAudit := runObservationalAudit(t, redBefore, buildaudit.VerdictOverStrict)
	assertA2RemainsRequired(t, redBefore, beforeAudit, buildaudit.VerdictOverStrict)
	before, err := Select([]Candidate{
		observationalCandidate("R1", 1, 1, redBefore, "small red change\n"),
		observationalCandidate("R2", 2, 2, green, "larger passing change\n+extra\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertD1Selection(t, before)

	// Audit after an initial selection: the unchanged gate report must produce
	// the same counts and ranking. Reversing input order models either parallel
	// rung finishing first; stable rank, not completion order, breaks the tie.
	initial, err := Select([]Candidate{
		observationalCandidate("R1", 1, 1, redAfter, "small red change\n"),
		observationalCandidate("R2", 2, 2, green, "larger passing change\n+extra\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertD1Selection(t, initial)
	afterAudit := runObservationalAudit(t, redAfter, buildaudit.VerdictContradicts)
	assertA2RemainsRequired(t, redAfter, afterAudit, buildaudit.VerdictContradicts)
	after, err := Select([]Candidate{
		observationalCandidate("R2", 2, 2, green, "larger passing change\n+extra\n"),
		observationalCandidate("R1", 1, 1, redAfter, "small red change\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertD1Selection(t, after)
}

func runObservationalAudit(t *testing.T, report *gate.GateReport, verdict buildaudit.Verdict) buildaudit.Result {
	t.Helper()
	role := contract.RoleSettings{Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high"}
	manifest := contract.RoleManifest{Effective: map[contract.RoleName]contract.RoleSettings{"auditor": role}}
	transport := observationalAuditTransport{reply: fmt.Sprintf(`{"items":[{"id":"A2","verdict":"%s","reason":"test opinion"}]}`, verdict)}
	auditor, err := buildaudit.New(manifest, transport)
	if err != nil {
		t.Fatal(err)
	}
	const runID, cacheKey, rung = "run_observational", "cache_observational", "R1"
	identity := contract.ConversationIdentity{
		RunID: runID, CacheKey: cacheKey,
		ThreadID:  session.DeriveThreadID(runID, buildaudit.ConversationStage, buildaudit.ConversationAttempt, rung, buildaudit.ConversationEpoch),
		SessionID: cacheKey, Role: "auditor", Stage: buildaudit.ConversationStage,
		Attempt: buildaudit.ConversationAttempt, Rung: rung, Epoch: buildaudit.ConversationEpoch,
		Provider: role.Provider, Model: role.Model, Effort: role.Effort,
	}
	conversation, err := session.New(identity)
	if err != nil {
		t.Fatal(err)
	}
	result, err := auditor.BeforeRepair(context.Background(), buildaudit.Input{
		Rung: rung, AcceptanceOnlyRed: true, FailedItemIDs: []string{"A2"},
		Request: []byte("Preserve the widget behavior."), AcceptanceTestSource: []byte("test A2: approved behavior\n"),
		FailureOutput: []byte("A2 failed"), CandidateDiff: []byte("candidate diff"), Session: conversation,
	}, report)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Audited {
		t.Fatal("eligible red acceptance gate was not audited")
	}
	return result
}

func assertA2RemainsRequired(t *testing.T, report *gate.GateReport, result buildaudit.Result, verdict buildaudit.Verdict) {
	t.Helper()
	if len(result.Receipt.Items) != 1 || result.Receipt.Items[0].ID != "A2" || result.Receipt.Items[0].Verdict != verdict {
		t.Fatalf("auditor returned the wrong A2 observation: %+v", result.Receipt.Items)
	}
	if counts := report.Counts(); counts != (gate.AcceptanceCounts{Passed: 1, Total: 2}) {
		t.Fatalf("audit changed approved pass counts: %+v", counts)
	}
	if report.IsVerified() || report.IsLandable() || report.Verdict() != gate.VerdictUnverified {
		t.Fatalf("audit voted failed A2 into eligibility: verified=%t landable=%t verdict=%s", report.IsVerified(), report.IsLandable(), report.Verdict())
	}
	if advice := report.AuditAdvice(); len(advice) != 1 || advice[0].ID != "A2" || advice[0].Verdict != string(verdict) {
		t.Fatalf("gate did not retain display-only A2 advice: %+v", advice)
	}
}

func observationalCandidate(rung string, rank, order int, report *gate.GateReport, diff string) Candidate {
	return Candidate{Rung: rung, Rank: rank, AttemptOrder: order, Gate: report, Diff: []byte(diff)}
}

func assertD1Selection(t *testing.T, report Report) {
	t.Helper()
	if report.Winner.Rung != "R2" || report.Winner.PassedItems != 2 || report.Winner.TotalItems != 2 || !report.Winner.Verified {
		t.Fatalf("audit/order changed winner eligibility or count: %+v", report.Winner)
	}
	if len(report.Ranking) != 2 || report.Ranking[0].Rung != "R2" || report.Ranking[1].Rung != "R1" {
		t.Fatalf("audit/order changed ranking: %+v", report.Ranking)
	}
	red := report.Ranking[1]
	if red.PassedItems != 1 || red.TotalItems != 2 || red.Verified || red.Rank != 1 {
		t.Fatalf("red candidate no longer reflects required A2: %+v", red)
	}
	if len(report.Candidates) != 2 || report.Candidates[0].Rung != "R1" || report.Candidates[1].Rung != "R2" {
		t.Fatalf("parallel completion order changed artifact rank order: %+v", report.Candidates)
	}
	if report.Demoted || report.AdvisoryItems == nil || len(report.AdvisoryItems) != 0 || report.AuditMode != AuditModeObservational {
		t.Fatalf("selection report changed fixed observational fields: %+v", report)
	}
}
