package edge

import (
	"context"
	"errors"
	"sort"
	"sync"
)

// ParallelCandidate describes an already executed parallel rung. Cross-checks
// are scheduled only for candidates whose approved acceptance and own edge
// suite both passed.
type ParallelCandidate struct {
	ID               string
	Order            int
	Workspace        string
	AcceptancePassed bool
	OwnEdge          Outcome
}

// CrossCheckResult records one peer suite running against one candidate.
// Results are returned in target-rung then suite-owner-rung order, regardless
// of completion order.
type CrossCheckResult struct {
	CandidateID  string
	SuiteOwnerID string
	Status       Status
	Failure      Failure
	Observation  *Observation
}

// CandidateDecision keeps standard acceptance, own generated tests, and peer
// generated tests as separate gates. Landable is true only when every
// required gate passed.
type CandidateDecision struct {
	ID                string
	AcceptancePassed  bool
	OwnEdgePassed     bool
	CrossChecksPassed bool
	Landable          bool
}

// CrossCheckReport contains a stable directed matrix for green parallel
// candidates. Required is false when fewer than two edge-enabled candidates
// are green, in which case the spec does not require peer checks.
type CrossCheckReport struct {
	Required   bool
	Checks     []CrossCheckResult
	Candidates []CandidateDecision
}

type crossJob struct {
	targetIndex int
	ownerIndex  int
}

// CrossCheck runs each eligible rung's generated tests against every other
// eligible parallel rung. The fixed recipe caps concurrent executions and
// total candidates; any missing or failed required peer check blocks that
// candidate without changing its approved acceptance result.
func CrossCheck(ctx context.Context, runner Runner, candidates []ParallelCandidate) (CrossCheckReport, error) {
	if ctx == nil || runner == nil {
		return CrossCheckReport{}, errors.New("edge: context and runner are required")
	}
	recipe := SharedRecipe()
	if len(candidates) > recipe.MaxParallelCandidates {
		return CrossCheckReport{}, errors.New("edge: parallel candidate count exceeds the shared recipe limit")
	}
	ordered := append([]ParallelCandidate(nil), candidates...)
	seenIDs := make(map[string]struct{}, len(ordered))
	var edgeEnabled *bool
	for index := range ordered {
		if !candidateIDPattern.MatchString(ordered[index].ID) || ordered[index].Order < 1 {
			return CrossCheckReport{}, errors.New("edge: parallel candidate identity is invalid")
		}
		if _, exists := seenIDs[ordered[index].ID]; exists {
			return CrossCheckReport{}, errors.New("edge: parallel candidate identities must be unique")
		}
		seenIDs[ordered[index].ID] = struct{}{}
		if edgeEnabled == nil {
			configured := ordered[index].OwnEdge.Required
			edgeEnabled = &configured
		} else if *edgeEnabled != ordered[index].OwnEdge.Required {
			return CrossCheckReport{}, errors.New("edge: parallel candidates must share the same edge-test setting")
		}
		ordered[index].OwnEdge = cloneOutcome(ordered[index].OwnEdge)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Order < ordered[j].Order })
	for index := 1; index < len(ordered); index++ {
		if ordered[index-1].Order == ordered[index].Order {
			return CrossCheckReport{}, errors.New("edge: parallel candidate order must be unique")
		}
	}
	for index := range ordered {
		if ordered[index].AcceptancePassed && ordered[index].OwnEdge.Required && ordered[index].OwnEdge.Passed() {
			if !validCandidate(ordered[index].ID, ordered[index].Workspace) || !validateSuite(ordered[index].OwnEdge.Suite, recipe) {
				return CrossCheckReport{}, errors.New("edge: green candidate is missing a valid workspace or generated suite")
			}
		}
	}

	report := CrossCheckReport{Candidates: make([]CandidateDecision, len(ordered))}
	eligible := make([]int, 0, len(ordered))
	for index, candidate := range ordered {
		ownPassed := candidate.OwnEdge.Passed()
		report.Candidates[index] = CandidateDecision{
			ID: candidate.ID, AcceptancePassed: candidate.AcceptancePassed,
			OwnEdgePassed: ownPassed, CrossChecksPassed: true,
			Landable: candidate.AcceptancePassed && ownPassed,
		}
		if candidate.AcceptancePassed && candidate.OwnEdge.Required && candidate.OwnEdge.Passed() {
			eligible = append(eligible, index)
		}
	}
	if len(eligible) < 2 {
		return report, nil
	}
	report.Required = true

	jobs := make([]crossJob, 0, len(eligible)*(len(eligible)-1))
	for _, targetIndex := range eligible {
		for _, ownerIndex := range eligible {
			if targetIndex != ownerIndex {
				jobs = append(jobs, crossJob{targetIndex: targetIndex, ownerIndex: ownerIndex})
			}
		}
	}
	report.Checks = make([]CrossCheckResult, len(jobs))
	jobIndexes := make(chan int, len(jobs))
	workers := recipe.MaxConcurrentChecks
	if workers > len(jobs) {
		workers = len(jobs)
	}
	var group sync.WaitGroup
	group.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer group.Done()
			for jobIndex := range jobIndexes {
				report.Checks[jobIndex] = executeCrossCheck(ctx, runner, recipe, ordered, jobs[jobIndex])
			}
		}()
	}
	for index := range jobs {
		jobIndexes <- index
	}
	close(jobIndexes)
	group.Wait()

	decisionIndex := make(map[string]int, len(report.Candidates))
	for index, candidate := range ordered {
		decisionIndex[candidate.ID] = index
	}
	for _, check := range report.Checks {
		if check.Status != StatusPassed {
			index := decisionIndex[check.CandidateID]
			report.Candidates[index].CrossChecksPassed = false
			report.Candidates[index].Landable = false
		}
	}
	return report, nil
}

func executeCrossCheck(ctx context.Context, runner Runner, recipe Recipe, candidates []ParallelCandidate, job crossJob) CrossCheckResult {
	target := candidates[job.targetIndex]
	owner := candidates[job.ownerIndex]
	result := CrossCheckResult{CandidateID: target.ID, SuiteOwnerID: owner.ID, Status: StatusStopped}
	if err := ctx.Err(); err != nil {
		result.Failure = FailureCancelled
		return result
	}
	observation, failure, stopped := runSuite(ctx, runner, RunRequest{
		CandidateID:  target.ID,
		SuiteOwnerID: owner.ID,
		Workspace:    target.Workspace,
		Suite:        cloneSuite(owner.OwnEdge.Suite),
		Recipe:       recipe,
	})
	if failure != "" {
		result.Failure = failure
		if stopped {
			result.Status = StatusStopped
		} else {
			result.Status = StatusFailed
		}
		result.Observation = observation
		return result
	}
	result.Status = StatusPassed
	result.Observation = observation
	return result
}

func cloneOutcome(value Outcome) Outcome {
	value.Suite = cloneSuite(value.Suite)
	if value.Observation != nil {
		observation := cloneObservation(*value.Observation)
		value.Observation = &observation
	}
	return value
}
