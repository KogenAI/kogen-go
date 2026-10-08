package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"

	"kogen-go/internal/build/ladder"
	"kogen-go/internal/build/parallel"
	"kogen-go/internal/build/recipe"
	selection "kogen-go/internal/build/select"
	"kogen-go/internal/contract"
	"kogen-go/internal/project"
	"kogen-go/internal/provider/session"
	"kogen-go/internal/workspace"
)

var ladderRunID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ladderWiringDependencies are the production ports for composing the
// sequential ladder, hard-plan parallel entry, candidate snapshots, and
// observational selection. The queue route supplies its rung executor from
// the Build integration that owns approval, run state, journal, and landing.
type ladderWiringDependencies struct {
	Workspaces ladder.WorkspaceFactory
	Rungs      parallel.RungExecutor
	Snapshots  ladder.Snapshotter
	Cleaner    parallel.WorkspaceCleaner
	Observer   parallel.StartObserver
}

// ladderWiring is the app-level coordinator for recipe-resolved Build rungs.
// It deliberately keeps landing outside this type: the selected verified
// candidate receives the distinct recipe landing allowance in its caller.
type ladderWiring struct {
	build recipe.Build
	deps  ladderWiringDependencies
}

type ladderWiringRequest struct {
	RunID         string
	CacheKey      string
	Source        string
	WorkspacesDir string
	BaseCommit    contract.ObjectID
	Plan          *recipe.Plan
	BudgetLeft    func() bool
}

type ladderWiringOutcome struct {
	Status       ladder.Status
	Reason       string
	Build        recipe.Build
	Candidates   []selection.Candidate
	Selected     *selection.Candidate
	Selection    selection.Report
	HasSelection bool
	Workspaces   []workspace.Workspace
	Summaries    []ladder.FailureSummary
	Parallel     *parallel.Outcome
}

// newLadderWiring uses the already resolved project/machine/default role
// manifest through recipe.Load. No local model default is introduced here.
func newLadderWiring(resolved *project.Resolution, deps ladderWiringDependencies) (*ladderWiring, error) {
	if resolved == nil {
		return nil, errors.New("ladder wiring requires a resolved project")
	}
	build, err := recipe.Load(resolved)
	if err != nil {
		return nil, fmt.Errorf("resolve Build recipe and roles: %w", err)
	}
	if deps.Workspaces == nil || deps.Rungs == nil || deps.Snapshots == nil {
		return nil, errors.New("ladder wiring requires workspace, rung, and snapshot ports")
	}
	if build.Recipe.Kind == recipe.Ladder || build.Recipe.Kind == recipe.Staged {
		if deps.Cleaner == nil || deps.Observer == nil {
			return nil, errors.New("ladder wiring requires parallel cleanup and observation ports")
		}
	}
	return &ladderWiring{build: build, deps: deps}, nil
}

// Run executes the configured ladder. Hard plans use the parallel controller
// only when the resolved recipe admits R1/R2; a dual-red result continues from
// the recipe's later rungs with stable global attempt numbering.
func (w *ladderWiring) Run(ctx context.Context, request ladderWiringRequest) (ladderWiringOutcome, error) {
	if w == nil {
		return ladderWiringOutcome{Status: ladder.StatusStopped}, errors.New("ladder wiring is required")
	}
	result := ladderWiringOutcome{Status: ladder.StatusStopped, Build: cloneRecipeBuild(w.build)}
	if ctx == nil || !ladderRunID.MatchString(request.RunID) || request.CacheKey == "" ||
		!cleanAbsolute(request.Source) || !cleanAbsolute(request.WorkspacesDir) || request.BaseCommit == "" || request.BudgetLeft == nil {
		return result, errors.New("ladder wiring request is incomplete")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	if request.Plan != nil && request.Plan.Difficulty == recipe.Hard {
		parallelController, err := parallel.New(parallel.Dependencies{
			Workspaces: w.deps.Workspaces,
			Rungs:      w.deps.Rungs,
			Snapshots:  w.deps.Snapshots,
			Cleaner:    w.deps.Cleaner,
			Observer:   w.deps.Observer,
		})
		if err != nil {
			return result, err
		}
		parallelOutcome, runErr := parallelController.Run(ctx, parallel.Request{
			RunID: request.RunID, CacheKey: request.CacheKey,
			Source: request.Source, WorkspacesDir: request.WorkspacesDir,
			BaseCommit: request.BaseCommit, Build: cloneRecipeBuild(w.build),
			Plan: cloneRecipePlan(request.Plan), BudgetLeft: request.BudgetLeft,
		})
		result.Parallel = &parallelOutcome
		result.Candidates = append(result.Candidates, cloneSelectionCandidates(parallelOutcome.Candidates)...)
		switch parallelOutcome.Status {
		case parallel.StatusWinner:
			result.Status = ladder.StatusReady
			result.Workspaces = append(result.Workspaces, workspacesFromParallel(parallelOutcome)...)
			return w.finish(result, runErr)
		case parallel.StatusStopped:
			result.Status, result.Reason = ladder.StatusStopped, parallelOutcome.Reason
			result.Workspaces = append(result.Workspaces, workspacesFromParallel(parallelOutcome)...)
			return w.finish(result, runErr)
		case parallel.StatusContinue:
			result.Summaries = append(result.Summaries, cloneFailureSummaries(parallelOutcome.FailureSummaries)...)
			result.Workspaces = append(result.Workspaces, workspacesFromParallel(parallelOutcome)...)
			continued, continueErr := w.runAfterParallel(ctx, request, parallelOutcome)
			result.Candidates = append(result.Candidates, cloneSelectionCandidates(continued.Candidates)...)
			result.Workspaces = append(result.Workspaces, continued.Workspaces...)
			result.Summaries = append(result.Summaries, continued.Summaries...)
			result.Status, result.Reason = continued.Status, continued.Reason
			return w.finish(result, errors.Join(runErr, continueErr))
		case parallel.StatusNotApplicable:
			// A hard plan with a one-rung cap still enters the sequential path.
		}
	}

	sequentialController, err := ladder.New(ladder.Dependencies{
		Workspaces: w.deps.Workspaces,
		Rungs: sequentialRungAdapter{
			Rungs: w.deps.Rungs, RunID: request.RunID, CacheKey: request.CacheKey,
		},
		Snapshots: w.deps.Snapshots,
	})
	if err != nil {
		return result, err
	}
	sequential, runErr := sequentialController.Run(ctx, ladder.Request{
		RunID: request.RunID, Source: request.Source, WorkspacesDir: request.WorkspacesDir,
		BaseCommit: request.BaseCommit, Build: cloneRecipeBuild(w.build),
		Plan: cloneRecipePlan(request.Plan), BudgetLeft: request.BudgetLeft,
	})
	result.Status, result.Reason = sequential.Status, sequential.Reason
	result.Candidates = append(result.Candidates, cloneSelectionCandidates(sequential.Candidates)...)
	result.Workspaces = append(result.Workspaces, sequential.Workspaces...)
	result.Summaries = append(result.Summaries, cloneFailureSummaries(sequential.Summaries)...)
	return w.finish(result, runErr)
}

func (w *ladderWiring) runAfterParallel(ctx context.Context, request ladderWiringRequest, first parallel.Outcome) (ladderWiringOutcome, error) {
	result := ladderWiringOutcome{Status: ladder.StatusFailed, Reason: first.Reason, Build: cloneRecipeBuild(w.build)}
	available, unresolvedR4 := remainingLadderRungs(w.build)
	if len(available) <= 2 {
		if unresolvedR4 && len(available) == 1 && request.BudgetLeft() {
			result.Status, result.Reason = ladder.StatusStopped, "recipe_attempt_invalid"
			return result, &ladder.StopError{Reason: result.Reason, Cause: errors.New("build.ladder.experimental_r4 does not resolve whether the experimental raw-request rung is enabled")}
		}
		return result, nil
	}

	// Keep the original rung models and ranks while giving the sequential
	// controller a suffix whose local R1 maps to the already-started R3.
	suffix := cloneRecipeBuild(w.build)
	suffix.Recipe.Rungs = append([]recipe.Rung(nil), available[2:]...)
	suffix.Settings.MaxRungs = len(suffix.Recipe.Rungs)
	if unresolvedR4 {
		suffix.Settings.RepeatFrom = nil
	} else {
		suffix.Settings.RepeatFrom = intPointer(0)
	}
	suffix.Settings.ExperimentalR4 = nil
	globalOffset := 2
	workspaces := offsetWorkspaceFactory{Base: w.deps.Workspaces, Offset: globalOffset}
	snapshots := offsetSnapshotter{Base: w.deps.Snapshots, Offset: globalOffset, RunID: request.RunID}
	rungs := sequentialRungAdapter{
		Rungs: w.deps.Rungs, RunID: request.RunID, CacheKey: request.CacheKey,
		Offset: globalOffset, Prior: cloneFailureSummaries(first.FailureSummaries),
	}
	controller, err := ladder.New(ladder.Dependencies{Workspaces: workspaces, Rungs: rungs, Snapshots: snapshots})
	if err != nil {
		return result, err
	}
	sequential, runErr := controller.Run(ctx, ladder.Request{
		RunID: request.RunID, Source: request.Source, WorkspacesDir: request.WorkspacesDir,
		BaseCommit: request.BaseCommit, Build: suffix, Plan: cloneRecipePlan(request.Plan),
		BudgetLeft: request.BudgetLeft,
	})
	result.Status, result.Reason = sequential.Status, sequential.Reason
	result.Candidates = remapCandidates(sequential.Candidates, globalOffset, request.RunID)
	result.Workspaces = remapWorkspaces(sequential.Workspaces, globalOffset)
	result.Summaries = remapFailureSummaries(sequential.Summaries, globalOffset)
	if runErr == nil && unresolvedR4 && len(available) == 3 && request.BudgetLeft() && sequential.Status == ladder.StatusFailed {
		result.Status, result.Reason = ladder.StatusStopped, "recipe_attempt_invalid"
		runErr = &ladder.StopError{Reason: result.Reason, Cause: errors.New("build.ladder.experimental_r4 does not resolve whether the experimental raw-request rung is enabled")}
	}
	if first.Status != parallel.StatusContinue {
		return result, errors.New("parallel continuation requires a dual-red result")
	}
	return result, runErr
}

func (w *ladderWiring) finish(result ladderWiringOutcome, runErr error) (ladderWiringOutcome, error) {
	if len(result.Candidates) == 0 {
		return result, runErr
	}
	report, err := selection.Select(cloneSelectionCandidates(result.Candidates))
	if err != nil {
		return result, errors.Join(runErr, fmt.Errorf("select Build candidate snapshots: %w", err))
	}
	result.Selection, result.HasSelection = report, true
	for index := range result.Candidates {
		candidate := result.Candidates[index]
		if candidate.Rung == report.Winner.Rung && candidate.Ref == report.Winner.Ref {
			selected := cloneSelectionCandidates([]selection.Candidate{candidate})[0]
			result.Selected = &selected
			break
		}
	}
	if result.Selected == nil {
		return result, errors.Join(runErr, errors.New("selection winner does not match a preserved candidate"))
	}
	if result.Selected.Gate != nil && result.Selected.Gate.IsLandable() && result.Status != ladder.StatusStopped {
		result.Status, result.Reason = ladder.StatusReady, "green"
	} else if result.Status == ladder.StatusReady {
		return result, errors.Join(runErr, errors.New("ladder reported ready without a selected landable candidate"))
	}
	return result, runErr
}

// remainingLadderRungs applies the frozen max_rungs and experimental-R4
// policy before the post-parallel suffix is handed to the sequential machine.
func remainingLadderRungs(build recipe.Build) ([]recipe.Rung, bool) {
	limit := build.Settings.MaxRungs
	if limit > len(build.Recipe.Rungs) {
		limit = len(build.Recipe.Rungs)
	}
	if limit < 0 {
		limit = 0
	}
	unresolvedR4 := false
	if limit >= 4 {
		switch {
		case build.Settings.ExperimentalR4 == nil:
			limit, unresolvedR4 = 3, true
		case !*build.Settings.ExperimentalR4:
			limit = 3
		}
	}
	if limit > len(build.Recipe.Rungs) {
		limit = len(build.Recipe.Rungs)
	}
	return append([]recipe.Rung(nil), build.Recipe.Rungs[:limit]...), unresolvedR4
}

type sequentialRungAdapter struct {
	Rungs    parallel.RungExecutor
	RunID    string
	CacheKey string
	Offset   int
	Prior    []ladder.FailureSummary
}

func (adapter sequentialRungAdapter) Run(ctx context.Context, request ladder.RungRequest) (ladder.RungResult, error) {
	order := request.AttemptOrder + adapter.Offset
	rungName := fmt.Sprintf("R%d", order)
	identity, err := session.Bind(session.Binding{
		RunID: adapter.RunID, CacheKey: adapter.CacheKey, Role: contract.RoleName("builder"),
		Provider: request.Attempt.Rung.Model.Provider, Model: request.Attempt.Rung.Model.Model,
		Effort: request.Attempt.Rung.Model.Effort, Stage: "build", Attempt: request.Attempt.Name,
		Rung: rungName, Epoch: "initial",
	})
	if err != nil {
		return ladder.RungResult{Status: ladder.RungStopped, StopReason: "session_identity_invalid"}, err
	}
	identity.SessionID = adapter.CacheKey
	conversation, err := session.New(identity)
	if err != nil {
		return ladder.RungResult{Status: ladder.RungStopped, StopReason: "session_invalid"}, err
	}
	request.RunID, request.AttemptOrder = adapter.RunID, order
	request.Workspace.Rung = rungName
	prior := cloneFailureSummaries(adapter.Prior)
	for _, summary := range request.PriorFailures {
		if adapter.Offset != 0 {
			summary.RungLabel = rungAfter(summary.RungLabel, adapter.Offset)
		}
		prior = append(prior, summary)
	}
	request.PriorFailures = prior
	return adapter.Rungs.Run(ctx, parallel.RungExecutionRequest{Attempt: request, Session: conversation})
}

type offsetWorkspaceFactory struct {
	Base   ladder.WorkspaceFactory
	Offset int
}

func (factory offsetWorkspaceFactory) Create(ctx context.Context, request workspace.CloneRequest) (workspace.Workspace, error) {
	localName := request.Rung
	globalName := rungAfter(request.Rung, factory.Offset)
	request.Rung = globalName
	work, err := factory.Base.Create(ctx, request)
	if err != nil {
		return work, err
	}
	work.Rung = localName
	return work, nil
}

type offsetSnapshotter struct {
	Base   ladder.Snapshotter
	Offset int
	RunID  string
}

func (snapshotter offsetSnapshotter) Preserve(ctx context.Context, request ladder.SnapshotRequest) error {
	request.AttemptOrder += snapshotter.Offset
	request.Workspace.Rung = rungAfter(request.Workspace.Rung, snapshotter.Offset)
	request.Candidate.Rung = rungAfter(request.Candidate.Rung, snapshotter.Offset)
	request.Candidate.AttemptOrder += snapshotter.Offset
	ref, err := selection.CandidateRef(snapshotter.RunID, request.Candidate.Rung)
	if err != nil {
		return err
	}
	request.Candidate.Ref, request.CandidateRef = ref, ref
	request.Result.Candidate = cloneSelectionCandidates([]selection.Candidate{request.Result.Candidate})[0]
	request.Result.Candidate.Rung = request.Candidate.Rung
	request.Result.Candidate.AttemptOrder = request.Candidate.AttemptOrder
	request.Result.Candidate.Ref = ref
	return snapshotter.Base.Preserve(ctx, request)
}

func cloneRecipeBuild(build recipe.Build) recipe.Build {
	build.Recipe.Rungs = append([]recipe.Rung(nil), build.Recipe.Rungs...)
	if build.Settings.ExperimentalR4 != nil {
		value := *build.Settings.ExperimentalR4
		build.Settings.ExperimentalR4 = &value
	}
	if build.Settings.RepeatFrom != nil {
		value := *build.Settings.RepeatFrom
		build.Settings.RepeatFrom = &value
	}
	return build
}

func cloneRecipePlan(plan *recipe.Plan) *recipe.Plan {
	if plan == nil {
		return nil
	}
	copy := *plan
	return &copy
}

func cloneSelectionCandidates(items []selection.Candidate) []selection.Candidate {
	result := make([]selection.Candidate, len(items))
	for index, item := range items {
		item.Diff = append([]byte(nil), item.Diff...)
		result[index] = item
	}
	return result
}

func cloneFailureSummaries(items []ladder.FailureSummary) []ladder.FailureSummary {
	result := make([]ladder.FailureSummary, len(items))
	for index, item := range items {
		item.Lines = append([]string(nil), item.Lines...)
		result[index] = item
	}
	return result
}

func workspacesFromParallel(outcome parallel.Outcome) []workspace.Workspace {
	result := make([]workspace.Workspace, 0, len(outcome.Results))
	for _, attempt := range outcome.Results {
		if attempt.Request.Attempt.Workspace.Path != "" {
			result = append(result, attempt.Request.Attempt.Workspace)
		}
	}
	return result
}

func remapCandidates(items []selection.Candidate, offset int, runID string) []selection.Candidate {
	result := cloneSelectionCandidates(items)
	for index := range result {
		result[index].AttemptOrder += offset
		result[index].Rung = rungAfter(result[index].Rung, offset)
		result[index].Ref, _ = selection.CandidateRef(runID, result[index].Rung)
	}
	return result
}

func remapWorkspaces(items []workspace.Workspace, offset int) []workspace.Workspace {
	result := append([]workspace.Workspace(nil), items...)
	for index := range result {
		result[index].Rung = rungAfter(result[index].Rung, offset)
	}
	return result
}

func remapFailureSummaries(items []ladder.FailureSummary, offset int) []ladder.FailureSummary {
	result := cloneFailureSummaries(items)
	for index := range result {
		result[index].RungLabel = rungAfter(result[index].RungLabel, offset)
	}
	return result
}

func rungAfter(name string, offset int) string {
	var order int
	if _, err := fmt.Sscanf(name, "R%d", &order); err != nil || order < 1 {
		return name
	}
	return fmt.Sprintf("R%d", order+offset)
}

func intPointer(value int) *int { return &value }

func cleanAbsolute(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

var _ ladder.RungExecutor = sequentialRungAdapter{}
var _ ladder.WorkspaceFactory = offsetWorkspaceFactory{}
var _ ladder.Snapshotter = offsetSnapshotter{}
