package edge

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestCrossCheckRunsDirectedMatrixInRungOrderWithBoundedParallelism(t *testing.T) {
	var mu sync.Mutex
	calls := make([]string, 0, 6)
	active, maxActive := 0, 0
	runner := runnerFunc(func(_ context.Context, request RunRequest) (Observation, error) {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		calls = append(calls, request.CandidateID+"<-"+request.SuiteOwnerID)
		mu.Unlock()
		// Finish later rung checks first to show report order is not completion order.
		if request.CandidateID == "R1" {
			time.Sleep(15 * time.Millisecond)
		}
		mu.Lock()
		active--
		mu.Unlock()
		return passObservation(), nil
	})
	input := []ParallelCandidate{
		parallelCandidate(t, "R3", 3, true, "suite-3"),
		parallelCandidate(t, "R1", 1, true, "suite-1"),
		parallelCandidate(t, "R2", 2, true, "suite-2"),
	}
	report, err := CrossCheck(context.Background(), runner, input)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Required || len(report.Checks) != 6 || len(report.Candidates) != 3 {
		t.Fatalf("cross-check matrix is incomplete: %+v", report)
	}
	wantPairs := []string{"R1<-R2", "R1<-R3", "R2<-R1", "R2<-R3", "R3<-R1", "R3<-R2"}
	gotPairs := make([]string, len(report.Checks))
	for index, result := range report.Checks {
		gotPairs[index] = result.CandidateID + "<-" + result.SuiteOwnerID
		if result.Status != StatusPassed || result.Failure != "" {
			t.Fatalf("cross-check failed: %+v", result)
		}
	}
	if fmt.Sprint(gotPairs) != fmt.Sprint(wantPairs) {
		t.Fatalf("cross-check output order = %v, want %v", gotPairs, wantPairs)
	}
	if maxActive > SharedRecipe().MaxConcurrentChecks {
		t.Fatalf("cross-check concurrency exceeded recipe: max=%d recipe=%d", maxActive, SharedRecipe().MaxConcurrentChecks)
	}
	for _, decision := range report.Candidates {
		if !decision.AcceptancePassed || !decision.OwnEdgePassed || !decision.CrossChecksPassed || !decision.Landable {
			t.Fatalf("green candidate did not remain landable: %+v", decision)
		}
	}
}

func TestCrossCheckFailureBlocksOnlyCandidateBeingChecked(t *testing.T) {
	runner := runnerFunc(func(_ context.Context, request RunRequest) (Observation, error) {
		row := passObservation()
		if request.CandidateID == "R1" && request.SuiteOwnerID == "R2" {
			row.ExitStatus = intPointer(1)
			row.TotalTests, row.PassedTests, row.FailedTests = 1, 0, 1
		}
		return row, nil
	})
	report, err := CrossCheck(context.Background(), runner, []ParallelCandidate{
		parallelCandidate(t, "R1", 1, true, "suite-1"),
		parallelCandidate(t, "R2", 2, true, "suite-2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Candidates) != 2 || report.Candidates[0].Landable || report.Candidates[0].CrossChecksPassed || !report.Candidates[1].Landable {
		t.Fatalf("failed peer test did not block only its target: %+v", report.Candidates)
	}
	if report.Checks[0].CandidateID != "R1" || report.Checks[0].Failure != FailureTestsFailed || report.Checks[1].CandidateID != "R2" || report.Checks[1].Status != StatusPassed {
		t.Fatalf("cross-check outcomes were attributed incorrectly: %+v", report.Checks)
	}
}

func TestCrossCheckExcludesRedRungsAndNeverPromotesAcceptance(t *testing.T) {
	var calls int
	runner := runnerFunc(func(context.Context, RunRequest) (Observation, error) {
		calls++
		return passObservation(), nil
	})
	report, err := CrossCheck(context.Background(), runner, []ParallelCandidate{
		parallelCandidate(t, "R1", 1, true, "suite-1"),
		parallelCandidate(t, "R2", 2, true, "suite-2"),
		parallelCandidate(t, "R3", 3, false, "suite-3"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(report.Checks) != 2 || report.Candidates[2].Landable {
		t.Fatalf("red rung was cross-checked or promoted: calls=%d report=%+v", calls, report)
	}
	if !report.Candidates[0].Landable || !report.Candidates[1].Landable {
		t.Fatalf("passing candidates did not clear strict gates: %+v", report.Candidates)
	}
	if report.Candidates[2].AcceptancePassed || !report.Candidates[2].OwnEdgePassed {
		t.Fatalf("failed approved acceptance was overwritten by edge result: %+v", report.Candidates[2])
	}

	// A candidate with green edge tests but red approved acceptance remains red.
	single, err := CrossCheck(context.Background(), runner, []ParallelCandidate{
		parallelCandidate(t, "R4", 4, false, "suite-4"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if single.Required || single.Candidates[0].Landable || !single.Candidates[0].OwnEdgePassed {
		t.Fatalf("edge green promoted a standard-acceptance failure: %+v", single)
	}
}

func TestCrossCheckDoesNotRequirePeerRunsForOneGreenCandidate(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	report, err := CrossCheck(context.Background(), runnerFunc(func(context.Context, RunRequest) (Observation, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return passObservation(), nil
	}), []ParallelCandidate{
		parallelCandidate(t, "R1", 1, true, "suite-1"),
		parallelCandidate(t, "R2", 2, false, "suite-2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Required || calls != 0 || !report.Candidates[0].Landable || report.Candidates[1].Landable {
		t.Fatalf("peer checks ran without two green edge candidates: calls=%d report=%+v", calls, report)
	}
}

func TestCrossCheckRejectsDuplicateAndUnboundedCandidateSets(t *testing.T) {
	runner := runnerFunc(func(context.Context, RunRequest) (Observation, error) { return passObservation(), nil })
	for _, candidates := range [][]ParallelCandidate{
		{parallelCandidate(t, "R1", 1, true, "suite-1"), parallelCandidate(t, "R1", 2, true, "suite-2")},
		{
			parallelCandidate(t, "R1", 1, true, "suite-1"),
			parallelCandidate(t, "R2", 2, true, "suite-2"),
			parallelCandidate(t, "R3", 3, true, "suite-3"),
			parallelCandidate(t, "R4", 4, true, "suite-4"),
			parallelCandidate(t, "R5", 5, true, "suite-5"),
		},
	} {
		if _, err := CrossCheck(context.Background(), runner, candidates); err == nil {
			t.Fatalf("invalid candidate set was accepted: %+v", candidates)
		}
	}
}

func TestCrossCheckCancellationProducesBlockingRows(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := CrossCheck(ctx, runnerFunc(func(context.Context, RunRequest) (Observation, error) {
		t.Fatal("cancelled cross-check invoked runner")
		return Observation{}, nil
	}), []ParallelCandidate{
		parallelCandidate(t, "R1", 1, true, "suite-1"),
		parallelCandidate(t, "R2", 2, true, "suite-2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Required || len(report.Checks) != 2 {
		t.Fatalf("cancelled required checks disappeared: %+v", report)
	}
	for _, check := range report.Checks {
		if check.Status != StatusStopped || check.Failure != FailureCancelled {
			t.Fatalf("cancelled check was not a blocking result: %+v", check)
		}
	}
	for _, decision := range report.Candidates {
		if decision.Landable || decision.CrossChecksPassed {
			t.Fatalf("cancelled cross-check remained landable: %+v", decision)
		}
	}
}

func parallelCandidate(t *testing.T, id string, order int, acceptancePassed bool, source string) ParallelCandidate {
	t.Helper()
	suite, err := newSuite([]byte(source), SharedRecipe())
	if err != nil {
		t.Fatal(err)
	}
	return ParallelCandidate{
		ID: id, Order: order, Workspace: "/work/" + id, AcceptancePassed: acceptancePassed,
		OwnEdge: Outcome{Required: true, Status: StatusPassed, Suite: suite, Observation: observationPointer(passObservation())},
	}
}

func observationPointer(value Observation) *Observation { return &value }
