package ladder

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"kogen-go/internal/build/recipe"
	"kogen-go/internal/build/repair"
	"kogen-go/internal/build/select"
	"kogen-go/internal/contract"
	"kogen-go/internal/workspace"
)

var runIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type Status string

const (
	StatusReady   Status = "ready"
	StatusFailed  Status = "failed"
	StatusStopped Status = "stopped"
)

type RungStatus string

const (
	RungLandable RungStatus = "landable"
	RungFailed   RungStatus = "failed"
	RungStopped  RungStatus = "stopped"
)

// StopError marks a controller or environment failure that ends the Build
// without judging the Intent. Rung failures are data and may escalate instead.
type StopError struct {
	Reason string
	Cause  error
}

func (e *StopError) Error() string {
	if e == nil {
		return "ladder stopped"
	}
	if e.Cause != nil {
		return "ladder stopped: " + e.Reason + ": " + e.Cause.Error()
	}
	return "ladder stopped: " + e.Reason
}

func (e *StopError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type WorkspaceFactory interface {
	Create(context.Context, workspace.CloneRequest) (workspace.Workspace, error)
}

// RungExecutor runs one attempt in the supplied detached workspace. A new
// workspace is created for every call, including repeats.
type RungExecutor interface {
	Run(context.Context, RungRequest) (RungResult, error)
}

// Snapshotter must durably preserve the exact candidate bytes/ref before it
// returns. It is called for failed, stopped, and landable attempts alike.
type Snapshotter interface {
	Preserve(context.Context, SnapshotRequest) error
}

type Dependencies struct {
	Workspaces WorkspaceFactory
	Rungs      RungExecutor
	Snapshots  Snapshotter
}

type Controller struct {
	deps Dependencies
}

func New(deps Dependencies) (*Controller, error) {
	if deps.Workspaces == nil || deps.Rungs == nil || deps.Snapshots == nil {
		return nil, errors.New("ladder: workspace, rung, and snapshot ports are required")
	}
	return &Controller{deps: deps}, nil
}

type Request struct {
	RunID         string
	Source        string
	WorkspacesDir string
	BaseCommit    contract.ObjectID
	Build         recipe.Build
	Plan          *recipe.Plan
	BudgetLeft    func() bool
}

type AttemptRequest struct {
	RunID            string
	AttemptOrder     int
	Attempt          recipe.RungAttempt
	Workspace        workspace.Workspace
	Build            recipe.Build
	Plan             *recipe.Plan
	EscalationReason string
	PriorFailures    []FailureSummary
}

type RungRequest = AttemptRequest

type RungResult struct {
	Status       RungStatus
	Reason       repair.Reason
	StopReason   string
	FailureLines []string
	Candidate    selection.Candidate
}

type SnapshotRequest struct {
	RunID        string
	AttemptOrder int
	Attempt      recipe.RungAttempt
	Workspace    workspace.Workspace
	CandidateRef string
	Candidate    selection.Candidate
	Result       RungResult
}

type FailureSummary struct {
	RungLabel string
	Attempt   string
	Model     string
	EndReason string
	Lines     []string
}

type Outcome struct {
	Status            Status
	Reason            string
	LastAttemptReason string
	Build             recipe.Build
	Candidates        []selection.Candidate
	Workspaces        []workspace.Workspace
	Summaries         []FailureSummary
	Candidate         *selection.Candidate
}

func (c *Controller) Run(ctx context.Context, request Request) (Outcome, error) {
	build := cloneBuild(request.Build)
	outcome := Outcome{Status: StatusStopped, Build: cloneBuild(build)}
	if ctx == nil {
		return outcome, stop("invalid_request", errors.New("context is required"))
	}
	if err := validateRequest(request); err != nil {
		return outcome, stop("invalid_request", err)
	}
	if err := validateBuild(build); err != nil {
		return outcome, stop("recipe_invalid", err)
	}
	if err := ctx.Err(); err != nil {
		return outcome, stop("cancelled", err)
	}

	completed := 0
	prior := make([]FailureSummary, 0)
	seenWorkspaces := make(map[string]struct{})
	escalationReason := ""
	lastAttemptReason := ""
	for {
		if err := ctx.Err(); err != nil {
			outcome.Status, outcome.Reason, outcome.LastAttemptReason = StatusStopped, "cancelled", lastAttemptReason
			return finishOutcome(outcome), stop("cancelled", err)
		}

		attempt, available, err := build.NextAttempt(completed, repeatNumber(build, completed), request.BudgetLeft())
		if err != nil {
			outcome.Status, outcome.Reason, outcome.LastAttemptReason = StatusStopped, "recipe_attempt_invalid", lastAttemptReason
			return finishOutcome(outcome), stop("recipe_attempt_invalid", err)
		}
		if !available {
			if completed == 0 {
				outcome.Status, outcome.Reason = StatusFailed, "budget"
			} else {
				outcome.Status, outcome.Reason = StatusFailed, "best_candidate"
			}
			outcome.LastAttemptReason = lastAttemptReason
			return finishOutcome(outcome), nil
		}
		plan, err := planForAttempt(request.Plan, attempt)
		if err != nil {
			outcome.Status, outcome.Reason, outcome.LastAttemptReason = StatusStopped, "plan_unavailable", lastAttemptReason
			return finishOutcome(outcome), stop("plan_unavailable", err)
		}

		attemptOrder := completed + 1
		workspaceName := fmt.Sprintf("R%d", attemptOrder)
		work, err := c.deps.Workspaces.Create(ctx, workspace.CloneRequest{
			Source: request.Source, WorkspacesDir: request.WorkspacesDir,
			RunID: request.RunID, Rung: workspaceName, BaseCommit: request.BaseCommit,
		})
		if err != nil {
			outcome.Status, outcome.Reason, outcome.LastAttemptReason = StatusStopped, "workspace_create_failed", lastAttemptReason
			return finishOutcome(outcome), stop("workspace_create_failed", err)
		}
		outcome.Workspaces = append(outcome.Workspaces, work)
		if err := validateWorkspace(work, request, workspaceName, seenWorkspaces); err != nil {
			outcome.Status, outcome.Reason, outcome.LastAttemptReason = StatusStopped, "workspace_invalid", lastAttemptReason
			return finishOutcome(outcome), stop("workspace_invalid", err)
		}

		rungRequest := AttemptRequest{
			RunID: request.RunID, AttemptOrder: attemptOrder, Attempt: attempt,
			Workspace: work, Build: cloneBuild(build), Plan: plan,
			EscalationReason: escalationReason, PriorFailures: cloneSummaries(prior),
		}
		result, runErr := c.deps.Rungs.Run(ctx, rungRequest)
		if runErr != nil {
			result.Status = RungStopped
			result.StopReason = "rung_execution_failed"
		}
		if validationErr := validateResult(result); validationErr != nil {
			result.Status = RungStopped
			result.StopReason = "rung_result_invalid"
			if runErr == nil {
				runErr = validationErr
			} else {
				runErr = errors.Join(runErr, validationErr)
			}
		}
		preserveCtx := ctx
		if ctx.Err() != nil || errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			preserveCtx = context.WithoutCancel(ctx)
		}
		candidate, preserveErr := c.preserve(preserveCtx, request.RunID, attemptOrder, attempt, work, result)
		if preserveErr != nil {
			outcome.Status, outcome.Reason, outcome.LastAttemptReason = StatusStopped, "snapshot_preservation_failed", lastAttemptReason
			return finishOutcome(outcome), stop("snapshot_preservation_failed", errors.Join(runErr, preserveErr))
		}
		outcome.Candidates = append(outcome.Candidates, candidate)

		if runErr != nil {
			outcome.Status, outcome.Reason, outcome.LastAttemptReason = StatusStopped, result.StopReason, lastAttemptReason
			return finishOutcome(outcome), stop(result.StopReason, runErr)
		}
		switch result.Status {
		case RungLandable:
			selected := cloneCandidate(candidate)
			outcome.Candidate = &selected
			outcome.Status, outcome.Reason = StatusReady, string(repair.ReasonGreen)
			return finishOutcome(outcome), nil
		case RungStopped:
			outcome.Status, outcome.Reason, outcome.LastAttemptReason = StatusStopped, result.StopReason, lastAttemptReason
			return finishOutcome(outcome), stop(result.StopReason, nil)
		case RungFailed:
			lastAttemptReason = string(result.Reason)
			summary := summarizeFailure(attemptOrder, attempt, result)
			prior = append(prior, summary)
			outcome.Summaries = append(outcome.Summaries, cloneSummary(summary))
			escalationReason = string(result.Reason)
			completed++
		}
	}
}

func (c *Controller) preserve(ctx context.Context, runID string, order int, attempt recipe.RungAttempt, work workspace.Workspace, result RungResult) (selection.Candidate, error) {
	rungID := fmt.Sprintf("R%d", order)
	ref, err := selection.CandidateRef(runID, rungID)
	if err != nil {
		return selection.Candidate{}, fmt.Errorf("candidate identity: %w", err)
	}
	candidate := cloneCandidate(result.Candidate)
	candidate.Rung = rungID
	candidate.Rank = attempt.Rung.Index
	candidate.AttemptOrder = order
	candidate.Ref = ref
	if result.Status == RungLandable {
		candidate.SnapshotReason = repair.ReasonGreen
	} else if result.Status == RungFailed {
		candidate.SnapshotReason = result.Reason
	} else {
		candidate.SnapshotReason = repair.Reason(result.StopReason)
	}
	if err := c.deps.Snapshots.Preserve(ctx, SnapshotRequest{
		RunID: runID, AttemptOrder: order, Attempt: attempt, Workspace: work,
		CandidateRef: ref, Candidate: cloneCandidate(candidate), Result: cloneResult(result),
	}); err != nil {
		return selection.Candidate{}, err
	}
	return candidate, nil
}

func validateRequest(request Request) error {
	if !runIDPattern.MatchString(request.RunID) {
		return errors.New("run id must be 32 lowercase hexadecimal characters")
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

func validateWorkspace(work workspace.Workspace, request Request, name string, seen map[string]struct{}) error {
	if work.Path == "" || !filepath.IsAbs(work.Path) || filepath.Clean(work.Path) != work.Path {
		return errors.New("workspace factory returned a non-canonical absolute path")
	}
	if work.RunID != request.RunID || work.Rung != name || work.BaseCommit != request.BaseCommit {
		return errors.New("workspace factory did not return the requested run, attempt, and immutable base")
	}
	if _, exists := seen[work.Path]; exists {
		return errors.New("workspace factory reused a prior attempt workspace")
	}
	seen[work.Path] = struct{}{}
	return nil
}

func validateResult(result RungResult) error {
	switch result.Status {
	case RungLandable:
		if result.Candidate.Gate == nil || !result.Candidate.Gate.IsLandable() {
			return errors.New("landable rung result has no landable verification report")
		}
	case RungFailed:
		if result.Reason == "" || result.Reason == repair.ReasonGreen {
			return errors.New("failed rung result has no end reason")
		}
		if result.Candidate.Gate != nil && result.Candidate.Gate.IsLandable() {
			return errors.New("failed rung result contains a landable verification report")
		}
	case RungStopped:
		if result.StopReason == "" {
			return errors.New("stopped rung result has no stop reason")
		}
	default:
		return fmt.Errorf("unknown rung result status %q", result.Status)
	}
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

func summarizeFailure(order int, attempt recipe.RungAttempt, result RungResult) FailureSummary {
	return FailureSummary{
		RungLabel: fmt.Sprintf("R%d", order), Attempt: attempt.Name,
		Model:     attempt.Rung.Model.Model + "/" + attempt.Rung.Model.Effort,
		EndReason: string(result.Reason),
		Lines:     compactFailureLines(result.FailureLines, result.Candidate.Diff),
	}
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
		strings.HasPrefix(line, "@@") || strings.HasPrefix(line, "\\ No newline at end of file")
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

func repeatNumber(build recipe.Build, completed int) int {
	count := build.Settings.MaxRungs
	if count > len(build.Recipe.Rungs) {
		count = len(build.Recipe.Rungs)
	}
	if count >= 4 && build.Settings.ExperimentalR4 != nil && !*build.Settings.ExperimentalR4 {
		count = 3
	}
	if completed < count || build.Settings.RepeatFrom == nil || *build.Settings.RepeatFrom >= count {
		return 1
	}
	cycleLength := count - *build.Settings.RepeatFrom
	if cycleLength < 1 {
		return 1
	}
	return 2 + (completed-count)/cycleLength
}

func stop(reason string, cause error) error {
	return &StopError{Reason: reason, Cause: cause}
}

func finishOutcome(outcome Outcome) Outcome {
	outcome.Build = cloneBuild(outcome.Build)
	outcome.Candidates = cloneCandidates(outcome.Candidates)
	outcome.Workspaces = append([]workspace.Workspace(nil), outcome.Workspaces...)
	outcome.Summaries = cloneSummaries(outcome.Summaries)
	if outcome.Candidate != nil {
		candidate := cloneCandidate(*outcome.Candidate)
		outcome.Candidate = &candidate
	}
	return outcome
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

func cloneCandidate(candidate selection.Candidate) selection.Candidate {
	candidate.Diff = append([]byte(nil), candidate.Diff...)
	return candidate
}

func cloneResult(result RungResult) RungResult {
	result.FailureLines = append([]string(nil), result.FailureLines...)
	result.Candidate = cloneCandidate(result.Candidate)
	return result
}

func cloneCandidates(candidates []selection.Candidate) []selection.Candidate {
	cloned := make([]selection.Candidate, len(candidates))
	for i, candidate := range candidates {
		cloned[i] = cloneCandidate(candidate)
	}
	return cloned
}

func cloneSummary(summary FailureSummary) FailureSummary {
	summary.Lines = append([]string(nil), summary.Lines...)
	return summary
}

func cloneSummaries(summaries []FailureSummary) []FailureSummary {
	cloned := make([]FailureSummary, len(summaries))
	for i, summary := range summaries {
		cloned[i] = cloneSummary(summary)
	}
	return cloned
}
