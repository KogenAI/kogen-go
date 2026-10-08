package parallel

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"kogen-go/internal/build/ladder"
	"kogen-go/internal/build/recipe"
	"kogen-go/internal/build/repair"
	selection "kogen-go/internal/build/select"
	"kogen-go/internal/contract"
	"kogen-go/internal/provider/session"
	"kogen-go/internal/workspace"
)

var runIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type Status string

const (
	StatusNotApplicable Status = "not_applicable"
	StatusWinner        Status = "winner"
	StatusContinue      Status = "continue"
	StatusStopped       Status = "stopped"
)

// WorkspaceCleaner removes a rung workspace after its candidate has been
// durably preserved. Implementations must stop and join any workspace writers
// before returning.
type WorkspaceCleaner interface {
	Remove(context.Context, workspace.Workspace) error
}

// RungExecutionRequest couples one ladder attempt to its own persistent model
// conversation. The same Conversation pointer must be used for every request
// and repair in this rung; the other parallel rung receives a different one.
type RungExecutionRequest struct {
	Attempt ladder.RungRequest
	Session *session.Conversation
}

// RungExecutor executes a model-backed rung. Run must honor cancellation by
// stopping and joining rung-owned writers before it returns.
type RungExecutor interface {
	Run(context.Context, RungExecutionRequest) (ladder.RungResult, error)
}

// StartObserver records the stable pre-execution event sequence. Implementors
// record parallel_started, followed by rung_started for R1 then R2.
type StartObserver interface {
	ParallelStarted(context.Context, string, int) error
	RungStarted(context.Context, ladder.RungRequest) error
}

type Dependencies struct {
	Workspaces ladder.WorkspaceFactory
	Rungs      RungExecutor
	Snapshots  ladder.Snapshotter
	Cleaner    WorkspaceCleaner
	Observer   StartObserver
}

type Controller struct{ deps Dependencies }

func New(deps Dependencies) (*Controller, error) {
	if deps.Workspaces == nil || deps.Rungs == nil || deps.Snapshots == nil || deps.Cleaner == nil || deps.Observer == nil {
		return nil, errors.New("parallel: workspace, rung, snapshot, cleanup, and observation ports are required")
	}
	return &Controller{deps: deps}, nil
}

type Request struct {
	RunID         string
	CacheKey      string
	Source        string
	WorkspacesDir string
	BaseCommit    contract.ObjectID
	Build         recipe.Build
	Plan          *recipe.Plan
	BudgetLeft    func() bool
}

// AttemptResult is one completed initial rung. Results are returned in R1,
// R2 order even when R2 finishes first. Session is the persistent session
// supplied to that rung's executor.
type AttemptResult struct {
	Request RungExecutionRequest
	Result  ladder.RungResult
	Err     error
}

type Outcome struct {
	Status           Status
	Applicable       bool
	Reason           string
	Results          []AttemptResult
	Candidates       []selection.Candidate
	Winner           *AttemptResult
	Best             *AttemptResult
	FailureSummaries []ladder.FailureSummary
}

// Run executes R1/R2 only when the validated plan is hard and the resolved
// recipe/max_rungs schedule admits both initial rungs. A non-applicable
// outcome has no side effects; the caller can use the sequential controller.
// Completed observations, snapshot calls, and cleanup calls use R1/R2 order.
func (c *Controller) Run(ctx context.Context, request Request) (Outcome, error) {
	outcome := Outcome{Status: StatusNotApplicable}
	if c == nil {
		return stopped(outcome, "invalid_request", errors.New("parallel controller is required"))
	}
	if err := validateRequest(ctx, request); err != nil {
		return stopped(outcome, "invalid_request", err)
	}
	if request.Plan == nil || request.Plan.Text == "" || (request.Plan.Difficulty != recipe.Easy && request.Plan.Difficulty != recipe.Hard) {
		return stopped(outcome, "plan_unavailable", errors.New("a validated Build plan is required"))
	}
	if err := validateBuild(request.Build); err != nil {
		return stopped(outcome, "recipe_invalid", err)
	}
	if request.Plan.Difficulty != recipe.Hard {
		return outcome, nil
	}

	schedule := request.Build.Recipe.EntrySchedule(true, request.Build.Settings.MaxRungs)
	if !schedule.Parallel || len(schedule.Rungs) != 2 {
		return outcome, nil
	}
	outcome.Applicable = true
	if !request.BudgetLeft() {
		outcome.Applicable = false
		return outcome, nil
	}

	first, firstAvailable, err := request.Build.NextAttempt(0, 1, true)
	if err != nil {
		return stopped(outcome, "recipe_attempt_invalid", err)
	}
	second, secondAvailable, err := request.Build.NextAttempt(1, 1, true)
	if err != nil {
		return stopped(outcome, "recipe_attempt_invalid", err)
	}
	if !firstAvailable || !secondAvailable {
		outcome.Applicable = false
		return outcome, nil
	}
	attempts := []recipe.RungAttempt{first, second}

	workers, err := c.createWorkers(ctx, request, attempts)
	if err != nil {
		return stopped(outcome, "workspace_create_failed", err)
	}
	if err := c.deps.Observer.ParallelStarted(ctx, request.RunID, len(workers)); err != nil {
		cleanupErr := c.cleanupCreated(ctx, workers)
		return stopped(outcome, "journal_failed", errors.Join(err, cleanupErr))
	}
	for _, worker := range workers {
		if err := c.deps.Observer.RungStarted(ctx, worker.Attempt); err != nil {
			cleanupErr := c.cleanupCreated(ctx, workers)
			return stopped(outcome, "journal_failed", errors.Join(err, cleanupErr))
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	completed := make(chan AttemptResult, len(workers))
	for _, worker := range workers {
		worker := worker
		go func() {
			result, runErr := c.deps.Rungs.Run(runCtx, cloneExecutionRequest(worker))
			completed <- AttemptResult{Request: worker, Result: result, Err: runErr}
		}()
	}

	results := make([]AttemptResult, len(workers))
	seen := make([]bool, len(workers))
	remaining := len(workers)
	cancelReason := ""
	for remaining > 0 {
		result := <-completed
		index := result.Request.Attempt.AttemptOrder - 1
		if index < 0 || index >= len(results) || seen[index] {
			cancel()
			return stopped(outcome, "parallel_result_invalid", errors.New("rung executor returned an invalid attempt order"))
		}
		seen[index] = true
		remaining--
		results[index] = result
		if normalized := normalizeResult(ctx, result, cancelReason); normalized.Result.Status == ladder.RungLandable {
			results[index] = normalized
			if cancelReason == "" {
				cancelReason = "parallel_winner"
				cancel()
			}
		} else {
			results[index] = normalized
			if normalized.Result.Status == ladder.RungStopped && cancelReason == "" {
				cancelReason = "parallel_peer_stopped"
				cancel()
			}
		}
	}

	outcome.Results = cloneAttemptResults(results)
	if err := ctx.Err(); err != nil {
		for i := range outcome.Results {
			outcome.Results[i] = asStopped(outcome.Results[i], "cancelled", err)
		}
		outcome.Reason = "cancelled"
		if prepareErr := c.prepareCandidates(outcome.Results, request.RunID); prepareErr != nil {
			return stoppedWithResults(outcome, "candidate_identity_failed", prepareErr)
		}
		if preserveErr := c.preserve(ctx, outcome.Results, request.RunID); preserveErr != nil {
			return stoppedWithResults(outcome, "snapshot_preservation_failed", preserveErr)
		}
		outcome.Candidates = candidates(outcome.Results)
		return stoppedWithResults(outcome, "cancelled", err)
	}
	if err := c.prepareCandidates(outcome.Results, request.RunID); err != nil {
		return stoppedWithResults(outcome, "candidate_identity_failed", err)
	}

	if winner, err := chooseLandable(outcome.Results); err != nil {
		if preserveErr := c.preserve(ctx, outcome.Results, request.RunID); preserveErr != nil {
			return stoppedWithResults(outcome, "snapshot_preservation_failed", errors.Join(err, preserveErr))
		}
		outcome.Candidates = candidates(outcome.Results)
		return stoppedWithResults(outcome, "candidate_selection_failed", err)
	} else if winner != nil {
		outcome.Status = StatusWinner
		outcome.Winner = winner
	} else if bothFailed(outcome.Results) {
		best, err := chooseBest(outcome.Results)
		if err != nil {
			if preserveErr := c.preserve(ctx, outcome.Results, request.RunID); preserveErr != nil {
				return stoppedWithResults(outcome, "snapshot_preservation_failed", errors.Join(err, preserveErr))
			}
			outcome.Candidates = candidates(outcome.Results)
			return stoppedWithResults(outcome, "candidate_selection_failed", err)
		}
		outcome.Status = StatusContinue
		outcome.Best = best
		outcome.FailureSummaries = failureSummaries(outcome.Results)
		if best != nil {
			outcome.Reason = string(best.Result.Reason)
		}
	} else {
		outcome.Status = StatusStopped
		outcome.Reason = stopReason(outcome.Results)
	}

	outcome.Candidates = candidates(outcome.Results)
	if err := c.preserve(ctx, outcome.Results, request.RunID); err != nil {
		return stoppedWithResults(outcome, "snapshot_preservation_failed", err)
	}
	if err := ctx.Err(); err != nil {
		outcome.Winner, outcome.Best = nil, nil
		return stoppedWithResults(outcome, "cancelled", err)
	}

	keepWorkspace := ""
	if outcome.Status == StatusWinner && outcome.Winner != nil {
		keepWorkspace = outcome.Winner.Request.Attempt.Workspace.Path
	}
	if err := c.cleanupPreserved(ctx, outcome.Results, keepWorkspace); err != nil {
		return stoppedWithResults(outcome, "workspace_cleanup_failed", err)
	}
	if err := ctx.Err(); err != nil {
		outcome.Winner, outcome.Best = nil, nil
		return stoppedWithResults(outcome, "cancelled", err)
	}
	if outcome.Status == StatusStopped {
		return outcome, &ladder.StopError{Reason: outcome.Reason}
	}
	return outcome, nil
}

func (c *Controller) createWorkers(ctx context.Context, request Request, attempts []recipe.RungAttempt) ([]RungExecutionRequest, error) {
	workers := make([]RungExecutionRequest, 0, len(attempts))
	seenPaths := make(map[string]struct{}, len(attempts))
	for index, attempt := range attempts {
		attemptOrder := index + 1
		rung := fmt.Sprintf("R%d", attemptOrder)
		work, err := c.deps.Workspaces.Create(ctx, workspace.CloneRequest{
			Source: request.Source, WorkspacesDir: request.WorkspacesDir,
			RunID: request.RunID, Rung: rung, BaseCommit: request.BaseCommit,
		})
		if err != nil {
			cleanupErr := c.cleanupCreated(ctx, workers)
			return nil, errors.Join(fmt.Errorf("create %s workspace: %w", rung, err), cleanupErr)
		}
		if err := validateWorkspace(work, request, rung, seenPaths); err != nil {
			cleanupErr := c.cleanupCreated(ctx, workers)
			return nil, errors.Join(err, cleanupErr)
		}
		plan, err := planForAttempt(request.Plan, attempt)
		if err != nil {
			cleanupErr := c.cleanupCreated(ctx, append(workers, RungExecutionRequest{Attempt: ladder.RungRequest{Workspace: work}}))
			return nil, errors.Join(err, cleanupErr)
		}
		identity, err := session.Bind(session.Binding{
			RunID: request.RunID, CacheKey: request.CacheKey,
			Role: contract.RoleName("builder"), Provider: attempt.Rung.Model.Provider,
			Model: attempt.Rung.Model.Model, Effort: attempt.Rung.Model.Effort,
			Stage: "build", Attempt: attempt.Name, Rung: rung, Epoch: "initial",
		})
		if err != nil {
			cleanupErr := c.cleanupCreated(ctx, append(workers, RungExecutionRequest{Attempt: ladder.RungRequest{Workspace: work}}))
			return nil, errors.Join(fmt.Errorf("create %s session identity: %w", rung, err), cleanupErr)
		}
		// The Build's persisted opaque affinity is the protocol session-id for
		// every stage; the rung-specific thread id remains distinct.
		identity.SessionID = request.CacheKey
		conversation, err := session.New(identity)
		if err != nil {
			cleanupErr := c.cleanupCreated(ctx, append(workers, RungExecutionRequest{Attempt: ladder.RungRequest{Workspace: work}}))
			return nil, errors.Join(fmt.Errorf("create %s session: %w", rung, err), cleanupErr)
		}
		workers = append(workers, RungExecutionRequest{Session: conversation, Attempt: ladder.RungRequest{
			RunID: request.RunID, AttemptOrder: attemptOrder, Attempt: attempt,
			Workspace: work, Build: cloneBuild(request.Build), Plan: plan,
		}})
	}
	if len(workers) != 2 || workers[0].Session == workers[1].Session ||
		workers[0].Session.ProtocolSession().Identity.ThreadID == workers[1].Session.ProtocolSession().Identity.ThreadID {
		cleanupErr := c.cleanupCreated(ctx, workers)
		return nil, errors.Join(errors.New("parallel: rung sessions must have distinct objects and thread identities"), cleanupErr)
	}
	return workers, nil
}

func (c *Controller) cleanupCreated(ctx context.Context, workers []RungExecutionRequest) error {
	cleanupCtx := context.WithoutCancel(ctx)
	var errs []error
	seen := make(map[string]struct{}, len(workers))
	for _, item := range workers {
		work := item.Attempt.Workspace
		if work.Path == "" {
			continue
		}
		if _, exists := seen[work.Path]; exists {
			continue
		}
		seen[work.Path] = struct{}{}
		if err := c.deps.Cleaner.Remove(cleanupCtx, work); err != nil {
			errs = append(errs, fmt.Errorf("remove incomplete %s workspace: %w", work.Rung, err))
		}
	}
	return errors.Join(errs...)
}

func (c *Controller) cleanupPreserved(ctx context.Context, results []AttemptResult, keepPath string) error {
	cleanupCtx := context.WithoutCancel(ctx)
	var errs []error
	for _, result := range results {
		work := result.Request.Attempt.Workspace
		if work.Path == keepPath {
			continue
		}
		if err := c.deps.Cleaner.Remove(cleanupCtx, work); err != nil {
			errs = append(errs, fmt.Errorf("remove preserved %s workspace: %w", work.Rung, err))
		}
	}
	return errors.Join(errs...)
}

func (c *Controller) prepareCandidates(results []AttemptResult, runID string) error {
	for i := range results {
		item := &results[i]
		rung := fmt.Sprintf("R%d", item.Request.Attempt.AttemptOrder)
		ref, err := selection.CandidateRef(runID, rung)
		if err != nil {
			return fmt.Errorf("candidate identity %s: %w", rung, err)
		}
		candidate := cloneCandidate(item.Result.Candidate)
		candidate.Rung = rung
		candidate.Rank = item.Request.Attempt.Attempt.Rung.Index
		candidate.AttemptOrder = item.Request.Attempt.AttemptOrder
		candidate.Ref = ref
		candidate.SnapshotReason = snapshotReason(item.Result)
		item.Result.Candidate = candidate
	}
	return nil
}

func (c *Controller) preserve(ctx context.Context, results []AttemptResult, runID string) error {
	var errs []error
	preserveCtx := context.WithoutCancel(ctx)
	for i := range results {
		item := &results[i]
		candidate := cloneCandidate(item.Result.Candidate)
		rung := candidate.Rung
		if err := c.deps.Snapshots.Preserve(preserveCtx, ladder.SnapshotRequest{
			RunID: runID, AttemptOrder: item.Request.Attempt.AttemptOrder,
			Attempt: item.Request.Attempt.Attempt, Workspace: item.Request.Attempt.Workspace,
			CandidateRef: candidate.Ref, Candidate: candidate, Result: cloneRungResult(item.Result),
		}); err != nil {
			errs = append(errs, fmt.Errorf("preserve %s candidate: %w", rung, err))
		}
	}
	return errors.Join(errs...)
}

func candidates(results []AttemptResult) []selection.Candidate {
	items := make([]selection.Candidate, len(results))
	for i, item := range results {
		items[i] = cloneCandidate(item.Result.Candidate)
	}
	return items
}

func validateRequest(ctx context.Context, request Request) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if !runIDPattern.MatchString(request.RunID) || request.CacheKey == "" {
		return errors.New("run identity must be 32 lowercase hexadecimal characters and cache identity is required")
	}
	if request.Source == "" || !filepath.IsAbs(request.Source) || filepath.Clean(request.Source) != request.Source {
		return errors.New("workspace source must be a clean absolute path")
	}
	if request.WorkspacesDir == "" || !filepath.IsAbs(request.WorkspacesDir) || filepath.Clean(request.WorkspacesDir) != request.WorkspacesDir {
		return errors.New("workspace directory must be a clean absolute path")
	}
	if request.BaseCommit == "" {
		return errors.New("base commit is required")
	}
	if request.BudgetLeft == nil {
		return errors.New("Build budget observer is required")
	}
	return nil
}

func validateBuild(build recipe.Build) error {
	if len(build.Recipe.Rungs) == 0 {
		return errors.New("recipe has no rungs")
	}
	if build.Settings.MaxRungs < 1 || build.Settings.MaxRungs > recipe.DefaultMaxRungs {
		return fmt.Errorf("build.ladder.max_rungs must be an integer from 1 to %d", recipe.DefaultMaxRungs)
	}
	return nil
}

func validateWorkspace(work workspace.Workspace, request Request, rung string, seen map[string]struct{}) error {
	if work.Path == "" || !filepath.IsAbs(work.Path) || filepath.Clean(work.Path) != work.Path {
		return errors.New("workspace factory returned a non-canonical absolute path")
	}
	if work.RunID != request.RunID || work.Rung != rung || work.BaseCommit != request.BaseCommit {
		return errors.New("workspace factory did not return the requested run, rung, and immutable base")
	}
	if _, exists := seen[work.Path]; exists {
		return errors.New("workspace factory reused the other parallel rung workspace")
	}
	seen[work.Path] = struct{}{}
	return nil
}

func planForAttempt(plan *recipe.Plan, attempt recipe.RungAttempt) (*recipe.Plan, error) {
	if attempt.Rung.Input != recipe.PlanInput {
		return nil, nil
	}
	if plan == nil || plan.Text == "" || (plan.Difficulty != recipe.Easy && plan.Difficulty != recipe.Hard) {
		return nil, errors.New("recipe rung requires the validated Build plan")
	}
	copy := *plan
	return &copy, nil
}

func normalizeResult(ctx context.Context, item AttemptResult, cancelReason string) AttemptResult {
	if item.Err != nil {
		if errors.Is(item.Err, context.Canceled) && cancelReason == "parallel_winner" {
			return asStopped(item, "parallel_cancelled", nil)
		}
		if errors.Is(item.Err, context.Canceled) && cancelReason == "parallel_peer_stopped" {
			return asStopped(item, "parallel_peer_stopped", nil)
		}
		if ctx.Err() != nil && errors.Is(item.Err, ctx.Err()) {
			return asStopped(item, "cancelled", item.Err)
		}
		return asStopped(item, "rung_execution_failed", item.Err)
	}
	if err := validateResult(item.Result); err != nil {
		return asStopped(item, "rung_result_invalid", err)
	}
	return item
}

func asStopped(item AttemptResult, reason string, err error) AttemptResult {
	item.Result.Status = ladder.RungStopped
	item.Result.StopReason = reason
	item.Result.Reason = ""
	item.Err = err
	return item
}

func validateResult(result ladder.RungResult) error {
	switch result.Status {
	case ladder.RungLandable:
		if result.Candidate.Gate == nil || !result.Candidate.Gate.IsLandable() {
			return errors.New("landable rung result has no landable verification report")
		}
	case ladder.RungFailed:
		if result.Reason == "" || result.Reason == repair.ReasonGreen {
			return errors.New("failed rung result has no end reason")
		}
		if result.Candidate.Gate != nil && result.Candidate.Gate.IsLandable() {
			return errors.New("failed rung result contains a landable verification report")
		}
	case ladder.RungStopped:
		if result.StopReason == "" {
			return errors.New("stopped rung result has no stop reason")
		}
	default:
		return fmt.Errorf("unknown rung result status %q", result.Status)
	}
	return nil
}

func chooseLandable(results []AttemptResult) (*AttemptResult, error) {
	landable := make([]selection.Candidate, 0, len(results))
	for _, item := range results {
		if item.Result.Status == ladder.RungLandable {
			landable = append(landable, cloneCandidate(item.Result.Candidate))
		}
	}
	if len(landable) == 0 {
		return nil, nil
	}
	report, err := selection.Select(landable)
	if err != nil {
		return nil, err
	}
	for i := range results {
		if results[i].Result.Candidate.Rung == report.Winner.Rung {
			copy := cloneAttemptResult(results[i])
			return &copy, nil
		}
	}
	return nil, errors.New("selector returned a rung without an observation")
}

func chooseBest(results []AttemptResult) (*AttemptResult, error) {
	candidates := make([]selection.Candidate, 0, len(results))
	for _, item := range results {
		candidates = append(candidates, cloneCandidate(item.Result.Candidate))
	}
	report, err := selection.Select(candidates)
	if err != nil {
		return nil, err
	}
	for i := range results {
		if results[i].Result.Candidate.Rung == report.Winner.Rung {
			copy := cloneAttemptResult(results[i])
			return &copy, nil
		}
	}
	return nil, errors.New("selector returned a rung without an observation")
}

func bothFailed(results []AttemptResult) bool {
	return len(results) == 2 && results[0].Result.Status == ladder.RungFailed && results[1].Result.Status == ladder.RungFailed
}

func failureSummaries(results []AttemptResult) []ladder.FailureSummary {
	summaries := make([]ladder.FailureSummary, 0, len(results))
	for _, item := range results {
		attempt := item.Request.Attempt
		summaries = append(summaries, ladder.FailureSummary{
			RungLabel: fmt.Sprintf("R%d", attempt.AttemptOrder), Attempt: attempt.Attempt.Name,
			Model:     fmt.Sprintf("%s/%s", attempt.Attempt.Rung.Model.Model, attempt.Attempt.Rung.Model.Effort),
			EndReason: string(item.Result.Reason), Lines: compactFailureLines(item.Result.FailureLines, item.Result.Candidate.Diff),
		})
	}
	return summaries
}

func compactFailureLines(source []string, diff []byte) []string {
	diffContents := make(map[string]struct{})
	for _, line := range strings.Split(string(diff), "\n") {
		line = strings.TrimSuffix(line, "\r")
		diffContents[line] = struct{}{}
		if len(line) > 0 && (line[0] == '+' || line[0] == '-') {
			diffContents[line[1:]] = struct{}{}
		}
	}
	lines := make([]string, 0, len(source))
	for _, entry := range source {
		for _, line := range strings.Split(entry, "\n") {
			line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
			if line == "" || isDiffLine(line) {
				continue
			}
			if _, isPatchContent := diffContents[line]; isPatchContent {
				continue
			}
			lines = append(lines, truncateRunes(line, 180))
		}
	}
	if len(lines) > 5 {
		lines = append([]string(nil), lines[len(lines)-5:]...)
	}
	return lines
}

func isDiffLine(line string) bool {
	return (len(line) > 0 && (line[0] == '+' || line[0] == '-')) ||
		strings.HasPrefix(line, "diff --git ") || strings.HasPrefix(line, "index ") ||
		strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") ||
		strings.HasPrefix(line, "@@") || strings.HasPrefix(line, `\ No newline at end of file`)
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

func stopReason(results []AttemptResult) string {
	for _, item := range results {
		if item.Result.Status == ladder.RungStopped && item.Result.StopReason != "parallel_cancelled" && item.Result.StopReason != "parallel_peer_stopped" {
			return item.Result.StopReason
		}
	}
	return "parallel_rung_stopped"
}

func snapshotReason(result ladder.RungResult) repair.Reason {
	switch result.Status {
	case ladder.RungLandable:
		return repair.ReasonGreen
	case ladder.RungFailed:
		return result.Reason
	default:
		return repair.Reason(result.StopReason)
	}
}

func stopped(outcome Outcome, reason string, err error) (Outcome, error) {
	outcome.Status, outcome.Reason = StatusStopped, reason
	return outcome, &ladder.StopError{Reason: reason, Cause: err}
}

func stoppedWithResults(outcome Outcome, reason string, err error) (Outcome, error) {
	outcome.Status, outcome.Reason = StatusStopped, reason
	return outcome, &ladder.StopError{Reason: reason, Cause: err}
}

func cloneBuild(build recipe.Build) recipe.Build {
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

func cloneExecutionRequest(request RungExecutionRequest) RungExecutionRequest {
	request.Attempt.Build = cloneBuild(request.Attempt.Build)
	if request.Attempt.Plan != nil {
		plan := *request.Attempt.Plan
		request.Attempt.Plan = &plan
	}
	request.Attempt.PriorFailures = cloneFailureSummaries(request.Attempt.PriorFailures)
	return request
}

func cloneAttemptResult(result AttemptResult) AttemptResult {
	result.Request = cloneExecutionRequest(result.Request)
	result.Result = cloneRungResult(result.Result)
	return result
}

func cloneAttemptResults(results []AttemptResult) []AttemptResult {
	cloned := make([]AttemptResult, len(results))
	for i, result := range results {
		cloned[i] = cloneAttemptResult(result)
	}
	return cloned
}

func cloneRungResult(result ladder.RungResult) ladder.RungResult {
	result.FailureLines = append([]string(nil), result.FailureLines...)
	result.Candidate = cloneCandidate(result.Candidate)
	return result
}

func cloneCandidate(candidate selection.Candidate) selection.Candidate {
	candidate.Diff = append([]byte(nil), candidate.Diff...)
	return candidate
}

func cloneFailureSummaries(summaries []ladder.FailureSummary) []ladder.FailureSummary {
	cloned := make([]ladder.FailureSummary, len(summaries))
	for i, summary := range summaries {
		cloned[i] = summary
		cloned[i].Lines = append([]string(nil), summary.Lines...)
	}
	return cloned
}
