package integrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/gate"
	"kogen-go/internal/gitio"
	"kogen-go/internal/journal"
	"kogen-go/internal/landing/commit"
	"kogen-go/internal/landing/publish"
	"kogen-go/internal/process"
)

const defaultLandingAllowance = 10 * time.Minute

var runIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

var (
	ErrInvalidRequest   = errors.New("landing integration: invalid request")
	ErrRepairSpent      = errors.New("landing integration: landing repair allowance spent")
	ErrImpossibleRebase = errors.New("landing integration: rebase cannot be completed")
)

// RebaseKind describes the Git effect of applying one candidate to a refreshed
// target base.
type RebaseKind string

const (
	RebaseClean      RebaseKind = "clean"
	RebaseConflict   RebaseKind = "conflict"
	RebaseImpossible RebaseKind = "impossible"
)

// RebaseAttempt carries the observed Git result. Paths are the exact unmerged
// paths reported by Git and are passed to the winning rung's repair session.
type RebaseAttempt struct {
	Kind   RebaseKind
	Base   Base
	Paths  []string
	Detail string
}

// VerifyRequest asks the Build's existing gate wiring to run its full guard,
// checks, and approved acceptance set against the refreshed base. The caller
// must use the current immutable baseline for Base.Tree.
type VerifyRequest struct {
	Workspace string
	Base      Base
	Rebase    RebaseAttempt
}

// Verifier runs the ordinary production gate. A non-landable report is an
// observed red result; it is never replaced by a caller-provided pass flag.
type Verifier interface {
	Verify(context.Context, VerifyRequest) (*gate.GateReport, error)
}

// RepairRequest gives the repair driver exact conflict paths or the gate's
// feedback. The same Repairer value is called for every landing repair, so its
// winning-rung conversation remains live across conflicts and verification.
type RepairRequest struct {
	Workspace string
	Base      Base
	Feedback  string
	Paths     []string
	Deadline  time.Time
}

// Repairer performs one same-session model repair. It must honor ctx and the
// separate landing deadline; repair count is bounded by that allowance, not a
// fixed number of turns.
type Repairer interface {
	Repair(context.Context, RepairRequest) error
}

// CandidateFactory creates a signed, one-parent candidate from the workspace
// after the moved-base GateReport has a real landable receipt.
type CandidateFactory func(context.Context, Base, *gate.GateReport) (commit.Result, error)

// Publisher invokes the production landing publisher for one immutable
// candidate. A BaseMoved result returns control here for rebase and full
// verification before a new publication attempt.
type Publisher func(context.Context, commit.Result) (publish.Result, error)

// Request supplies the Build's private workspace and already-bound candidate
// alongside its existing gate, repair session, candidate builder and
// compare-and-swap publisher.
type Request struct {
	Processes       contract.ProcessRunner
	Environment     process.Environment
	Repository      string
	Workspace       string
	Branch          string
	RunID           string
	Candidate       commit.Result
	RunStore        *journal.RunStore
	Snapshot        *journal.RunSnapshot
	Clock           contract.Clock
	Allowance       time.Duration
	Verifier        Verifier
	Repairer        Repairer
	CreateCandidate CandidateFactory
	Publish         Publisher
	ReleaseClaim    func(context.Context) error
}

// OutcomeKind is the terminal landing result returned by Land.
type OutcomeKind string

const (
	OutcomeLanded OutcomeKind = "landed"
	OutcomeParked OutcomeKind = "parked"
)

// Result describes whether a candidate landed or was retained for later
// review. CandidateRefs lists immutable snapshots retained under the run's
// candidate namespace; no integration step removes or replaces them.
type Result struct {
	Kind          OutcomeKind
	Commit        contract.ObjectID
	Tree          contract.ObjectID
	Base          Base
	ParkedRef     string
	CandidateRefs []string
	Warnings      []string
	CleanupErrors []string
}

// Land retries publication through the supplied compare-and-swap publisher.
// A moved-base result refreshes the tip, rebases only the private workspace,
// runs the full gate, and uses the winning rung's same Repairer until the
// landing allowance ends. It never changes the target branch directly.
func Land(ctx context.Context, request Request) (Result, error) {
	if err := validateLandRequest(ctx, request); err != nil {
		return Result{}, err
	}
	repository, workspace, err := validatePaths(request.Repository, request.Workspace)
	if err != nil {
		return Result{}, err
	}
	request.Repository = repository
	request.Workspace = workspace
	git, policy, err := workspaceGit(request.Processes, workspace, request.Environment)
	if err != nil {
		return Result{}, err
	}
	if err := resetWorkspaceGitConfig(ctx, git, policy, workspace); err != nil {
		return Result{}, err
	}
	origin, originPolicy := originGit(request.Processes, repository, request.Environment)
	originRefs := gitio.NewRefPort(origin, originPolicy)
	clock := request.Clock
	if clock == nil {
		clock = wallClock{}
	}
	allowance := request.Allowance
	if allowance == 0 {
		allowance = defaultLandingAllowance
	}

	candidate := cloneCandidate(request.Candidate)
	candidateRefs := make([]string, 0, 4)
	sequence := 0
	for {
		candidateRef := candidateRefName(request.RunID, sequence)
		if err := preserveCandidate(ctx, git, policy, origin, originPolicy, candidateRef, candidate); err != nil {
			return Result{}, err
		}
		candidateRefs = appendUnique(candidateRefs, candidateRef)

		published, err := request.Publish(ctx, cloneCandidate(candidate))
		if err != nil {
			return Result{}, err
		}
		switch published.Kind {
		case publish.Landed:
			if published.Commit != candidate.Commit || published.Tree != candidate.Tree {
				return Result{}, fmt.Errorf("%w: publisher reported a different landed candidate", ErrInvalidRequest)
			}
			baseRef, err := branchRef(ctx, origin, originPolicy, request.Branch)
			if err != nil {
				return Result{}, err
			}
			observed, err := originRefs.ReadRef(ctx, baseRef)
			if err != nil || !observed.Exists || observed.Target != candidate.Commit {
				return Result{}, errors.New("landing integration: publisher reported landed before the target ref reached the candidate")
			}
			baseTree, err := originRefs.ResolveTree(ctx, string(candidate.Parent))
			if err != nil {
				return Result{}, fmt.Errorf("landing integration: resolve landed base tree: %w", err)
			}
			return Result{
				Kind: OutcomeLanded, Commit: published.Commit, Tree: published.Tree,
				Base: Base{Commit: candidate.Parent, Tree: baseTree}, CandidateRefs: candidateRefs,
				Warnings:      append([]string(nil), published.Warnings...),
				CleanupErrors: append([]string(nil), published.CleanupFailures...),
			}, nil
		case publish.BaseMoved:
			if published.Commit != candidate.Commit || published.Tree != candidate.Tree {
				return Result{}, fmt.Errorf("%w: publisher moved a different candidate", ErrInvalidRequest)
			}
		default:
			return Result{}, fmt.Errorf("%w: publisher returned unknown outcome %q", ErrInvalidRequest, published.Kind)
		}

		deadline := time.Now().Add(allowance)
		updated, base, park, err := integrateMoved(ctx, request, repository, workspace, git, policy, origin, originPolicy, candidate, deadline, clock, sequence+1)
		if err != nil {
			return Result{}, err
		}
		if park {
			parkedRef, err := parkCandidate(ctx, originRefs, request.RunID, candidate.Commit)
			if err != nil {
				return Result{}, err
			}
			if err := finishParked(request, clock); err != nil {
				return Result{}, err
			}
			if err := request.ReleaseClaim(ctx); err != nil {
				return Result{}, fmt.Errorf("landing integration: release parked Build claim: %w", err)
			}
			return Result{
				Kind: OutcomeParked, Commit: candidate.Commit, Tree: candidate.Tree, Base: base,
				ParkedRef: parkedRef, CandidateRefs: candidateRefs,
			}, nil
		}
		candidate = updated
		sequence++
	}
}

func validateLandRequest(ctx context.Context, request Request) error {
	if ctx == nil || request.Publish == nil || request.Verifier == nil || request.Repairer == nil || request.CreateCandidate == nil {
		return fmt.Errorf("%w: context, publisher, verifier, repairer, and candidate factory are required", ErrInvalidRequest)
	}
	if !runIDPattern.MatchString(request.RunID) {
		return fmt.Errorf("%w: run id must be 32 lowercase hexadecimal characters", ErrInvalidRequest)
	}
	if request.Environment == nil || request.Environment["PATH"] == "" {
		return fmt.Errorf("%w: captured controller Git environment with PATH is required", ErrInvalidRequest)
	}
	if request.Snapshot == nil || request.RunStore == nil || request.Snapshot.Status != "running" || request.Snapshot.RunID != request.RunID || request.Snapshot.TargetBranch != request.Branch {
		return fmt.Errorf("%w: a matching running Build journal and target branch are required", ErrInvalidRequest)
	}
	if err := request.Snapshot.Validate(); err != nil {
		return fmt.Errorf("%w: invalid running Build snapshot: %v", ErrInvalidRequest, err)
	}
	if request.ReleaseClaim == nil {
		return fmt.Errorf("%w: claim release callback is required for the parked outcome", ErrInvalidRequest)
	}
	if request.Allowance < 0 {
		return fmt.Errorf("%w: landing allowance must not be negative", ErrInvalidRequest)
	}
	if err := validateCandidateIDs(request.Candidate); err != nil {
		return fmt.Errorf("%w: initial candidate: %v", ErrInvalidRequest, err)
	}
	return nil
}

func integrateMoved(ctx context.Context, request Request, repository, workspace string, git contract.GitPort, policy contract.GitPolicy, origin contract.GitPort, originPolicy contract.GitPolicy, previous commit.Result, deadline time.Time, clock contract.Clock, sequence int) (commit.Result, Base, bool, error) {
	baseCommit, baseTree, err := fetchBase(ctx, git, policy, repository, request.Branch)
	if err != nil {
		return commit.Result{}, Base{}, false, err
	}
	base := Base{Commit: baseCommit, Tree: baseTree}
	refs := gitio.NewRefPort(git, policy)
	ancestor, err := refs.IsAncestor(ctx, previous.Parent, baseCommit)
	if err != nil {
		return commit.Result{}, Base{}, false, fmt.Errorf("landing integration: check moved-base ancestry: %w", err)
	}
	if !ancestor {
		if err := recordImpossibleRebase(request, clock, base, "target tip is not descended from the candidate base"); err != nil {
			return commit.Result{}, Base{}, false, err
		}
		return previous, base, true, nil
	}

	if _, err := execChecked(ctx, git, policy, []string{"reset", "--hard", string(previous.Commit)}, nil, "restore candidate before rebase"); err != nil {
		return commit.Result{}, Base{}, false, err
	}
	rebase := RebaseAttempt{Kind: RebaseClean, Base: base}
	if baseCommit != previous.Parent {
		rebasePolicy, err := rebaseIdentityPolicy(ctx, git, policy, origin, originPolicy)
		if err != nil {
			return commit.Result{}, Base{}, false, err
		}
		result, err := git.Exec(ctx, []string{"rebase", "--no-autostash", "--no-autosquash", "--no-update-refs", "--no-rerere-autoupdate", "--no-verify", "--onto", string(baseCommit), string(previous.Parent)}, nil, rebasePolicy)
		if err != nil {
			return commit.Result{}, Base{}, false, fmt.Errorf("landing integration: rebase candidate: %w", err)
		}
		if result.Process.ExitStatus == nil || result.Process.TimedOut || result.Process.Unavailable {
			return commit.Result{}, Base{}, false, errors.New("landing integration: rebase returned no usable exit status")
		}
		if *result.Process.ExitStatus != 0 {
			paths, listErr := unmergedPaths(ctx, git, policy)
			if listErr != nil {
				return commit.Result{}, Base{}, false, listErr
			}
			if len(paths) == 0 {
				if err := recordImpossibleRebase(request, clock, base, boundedDetail(result.StderrTail)); err != nil {
					return commit.Result{}, Base{}, false, err
				}
				return previous, base, true, nil
			}
			rebase = RebaseAttempt{Kind: RebaseConflict, Base: base, Paths: paths, Detail: boundedDetail(result.StderrTail)}
		}
	}
	if rebase.Kind == RebaseConflict {
		if err := repairConflicts(ctx, request, git, policy, origin, originPolicy, rebase, deadline, clock); err != nil {
			if errors.Is(err, ErrRepairSpent) || errors.Is(err, context.DeadlineExceeded) {
				return previous, base, true, nil
			}
			if errors.Is(err, ErrImpossibleRebase) {
				if recordErr := recordImpossibleRebase(request, clock, base, err.Error()); recordErr != nil {
					return commit.Result{}, Base{}, false, recordErr
				}
				return previous, base, true, nil
			}
			return commit.Result{}, Base{}, false, err
		}
	}

	for {
		report, err := request.Verifier.Verify(ctx, VerifyRequest{Workspace: workspace, Base: base, Rebase: rebase})
		if err != nil {
			return commit.Result{}, Base{}, false, fmt.Errorf("landing integration: verify moved-base candidate: %w", err)
		}
		if report == nil {
			return commit.Result{}, Base{}, false, errors.New("landing integration: verifier returned no gate report")
		}
		if err := verifyReportBase(report, base); err != nil {
			return commit.Result{}, Base{}, false, err
		}
		if err := recordVerification(request.RunStore, request.Snapshot, clock, base, report); err != nil {
			return commit.Result{}, Base{}, false, err
		}
		if report.IsLandable() {
			candidate, err := request.CreateCandidate(ctx, base, report)
			if err != nil {
				return commit.Result{}, Base{}, false, fmt.Errorf("landing integration: create moved-base candidate: %w", err)
			}
			if err := validateGateCandidate(ctx, git, policy, base, report, candidate); err != nil {
				return commit.Result{}, Base{}, false, err
			}
			if err := preserveCandidate(ctx, git, policy, origin, originPolicy, candidateRefName(request.RunID, sequence), candidate); err != nil {
				return commit.Result{}, Base{}, false, err
			}
			name := "rebase_green"
			if rebase.Kind == RebaseConflict || report.Verdict() != gate.VerdictGreen {
				name = "repaired"
			}
			if err := record(request.RunStore, request.Snapshot, clock, name, map[string]any{
				"base_commit": base.Commit,
				"base_tree":   base.Tree,
				"candidate":   candidate.Commit,
				"tree":        candidate.Tree,
			}); err != nil {
				return commit.Result{}, Base{}, false, err
			}
			return candidate, base, false, nil
		}
		if report.Verdict() == gate.VerdictGreen {
			return previous, base, true, nil
		}
		if time.Now().After(deadline) {
			return previous, base, true, nil
		}
		feedback := strings.TrimSpace(report.Feedback())
		if feedback == "" {
			feedback = "Moved-base verification is not landable. Repair the candidate, then rerun the full guard and gate."
		}
		repairCtx, cancel := context.WithDeadline(ctx, deadline)
		repairErr := request.Repairer.Repair(repairCtx, RepairRequest{
			Workspace: workspace, Base: base, Feedback: feedback,
			Deadline: deadline,
		})
		cancel()
		if err := record(request.RunStore, request.Snapshot, clock, "repair", map[string]any{
			"kind":      "moved_base_verification",
			"base_tree": base.Tree,
		}); err != nil {
			return commit.Result{}, Base{}, false, err
		}
		if errors.Is(repairErr, ErrRepairSpent) || errors.Is(repairErr, context.DeadlineExceeded) || time.Now().After(deadline) {
			return previous, base, true, nil
		}
		if repairErr != nil {
			return commit.Result{}, Base{}, false, fmt.Errorf("landing integration: same-session gate repair: %w", repairErr)
		}
		rebase.Kind = RebaseClean
		rebase.Paths = nil
		rebase.Detail = ""
	}
}

func repairConflicts(ctx context.Context, request Request, git contract.GitPort, policy contract.GitPolicy, origin contract.GitPort, originPolicy contract.GitPolicy, rebase RebaseAttempt, deadline time.Time, clock contract.Clock) error {
	for {
		if time.Now().After(deadline) {
			return ErrRepairSpent
		}
		feedback := conflictFeedback(rebase)
		repairCtx, cancel := context.WithDeadline(ctx, deadline)
		repairErr := request.Repairer.Repair(repairCtx, RepairRequest{
			Workspace: request.Workspace, Base: rebase.Base, Feedback: feedback,
			Paths: append([]string(nil), rebase.Paths...), Deadline: deadline,
		})
		cancel()
		if err := record(request.RunStore, request.Snapshot, clock, "repair", map[string]any{
			"kind": "moved_base_conflict", "paths": safePaths(rebase.Paths), "base_tree": rebase.Base.Tree,
		}); err != nil {
			return err
		}
		if errors.Is(repairErr, ErrRepairSpent) || errors.Is(repairErr, context.DeadlineExceeded) || time.Now().After(deadline) {
			return ErrRepairSpent
		}
		if repairErr != nil {
			return fmt.Errorf("landing integration: same-session conflict repair: %w", repairErr)
		}
		if _, err := execChecked(ctx, git, policy, []string{"add", "-A"}, nil, "stage resolved rebase paths"); err != nil {
			return err
		}
		policy = withEnvironment(policy, "GIT_EDITOR", "true")
		policy = withEnvironment(policy, "GIT_SEQUENCE_EDITOR", "true")
		policy, err := rebaseIdentityPolicy(ctx, git, policy, origin, originPolicy)
		if err != nil {
			return err
		}
		continued, err := git.Exec(ctx, []string{"rebase", "--continue"}, nil, policy)
		if err != nil {
			return fmt.Errorf("landing integration: continue resolved rebase: %w", err)
		}
		if continued.Process.ExitStatus == nil || continued.Process.TimedOut || continued.Process.Unavailable {
			return errors.New("landing integration: rebase continuation returned no usable exit status")
		}
		if *continued.Process.ExitStatus == 0 {
			return nil
		}
		paths, err := unmergedPaths(ctx, git, policy)
		if err != nil {
			return err
		}
		if len(paths) == 0 {
			return fmt.Errorf("%w: %s", ErrImpossibleRebase, gitFailure("continue resolved rebase", continued))
		}
		rebase.Paths = paths
		rebase.Detail = boundedDetail(continued.StderrTail)
	}
}

func recordImpossibleRebase(request Request, clock contract.Clock, base Base, detail string) error {
	return record(request.RunStore, request.Snapshot, clock, "rebase_impossible", map[string]any{
		"base_commit": base.Commit,
		"base_tree":   base.Tree,
		"detail":      boundedDetail([]byte(detail)),
	})
}

func rebaseIdentityPolicy(ctx context.Context, workspaceGit contract.GitPort, workspacePolicy contract.GitPolicy, origin contract.GitPort, originPolicy contract.GitPolicy) (contract.GitPolicy, error) {
	identity := make(map[string]string, 6)
	for _, pair := range []struct{ variable, prefix string }{
		{variable: "GIT_AUTHOR_IDENT", prefix: "GIT_AUTHOR"},
		{variable: "GIT_COMMITTER_IDENT", prefix: "GIT_COMMITTER"},
	} {
		result, err := origin.Exec(ctx, []string{"var", pair.variable}, nil, originPolicy)
		if err != nil {
			return contract.GitPolicy{}, fmt.Errorf("landing integration: resolve rebase identity: %w", err)
		}
		if err := requireSuccess("resolve rebase identity", result); err != nil {
			return contract.GitPolicy{}, err
		}
		values, err := parseGitIdentity(strings.TrimSpace(string(result.Stdout)))
		if err != nil {
			return contract.GitPolicy{}, err
		}
		for key, value := range values {
			identity[pair.prefix+"_"+key] = value
		}
	}
	for key, value := range identity {
		workspacePolicy = withEnvironment(workspacePolicy, key, value)
	}
	return workspacePolicy, nil
}

func parseGitIdentity(value string) (map[string]string, error) {
	open, close := strings.LastIndexByte(value, '<'), strings.LastIndexByte(value, '>')
	if open < 0 || close <= open {
		return nil, errors.New("landing integration: Git returned a malformed rebase identity")
	}
	fields := strings.Fields(value[close+1:])
	if len(fields) != 2 {
		return nil, errors.New("landing integration: Git returned a malformed rebase identity")
	}
	if _, err := strconv.ParseInt(fields[0], 10, 64); err != nil {
		return nil, errors.New("landing integration: Git returned a malformed rebase identity")
	}
	return map[string]string{
		"NAME":  strings.TrimSpace(value[:open]),
		"EMAIL": strings.TrimSpace(value[open+1 : close]),
		"DATE":  "@" + fields[0] + " " + fields[1],
	}, nil
}

func validateGateCandidate(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, base Base, report *gate.GateReport, candidate commit.Result) error {
	if err := validateCandidateIDs(candidate); err != nil {
		return fmt.Errorf("landing integration: candidate factory returned invalid object IDs: %w", err)
	}
	receipt, ok := report.Receipt()
	if !ok || !report.IsLandable() || receipt.BaseTree != string(base.Tree) || receipt.CandidateTree != string(candidate.VerifiedTree) {
		return errors.New("landing integration: candidate does not match the moved-base verification receipt")
	}
	if candidate.Parent != base.Commit {
		return errors.New("landing integration: moved-base candidate parent does not equal the refreshed base")
	}
	refs := gitio.NewRefPort(git, policy)
	resolved, err := refs.ResolveCommit(ctx, string(candidate.Commit))
	if err != nil {
		return fmt.Errorf("landing integration: candidate commit is unavailable in the private workspace: %w", err)
	}
	if resolved != candidate.Commit {
		return errors.New("landing integration: candidate commit did not resolve canonically in the private workspace")
	}
	tree, err := refs.ResolveTree(ctx, string(candidate.Commit))
	if err != nil {
		return fmt.Errorf("landing integration: resolve moved-base candidate tree: %w", err)
	}
	if tree != candidate.Tree {
		return errors.New("landing integration: moved-base candidate tree does not match its commit")
	}
	result, err := execChecked(ctx, git, policy, []string{"show", "-s", "--format=%P", string(candidate.Commit)}, nil, "verify moved-base candidate parent")
	if err != nil {
		return err
	}
	parents := strings.Fields(strings.TrimSpace(string(result.Stdout)))
	if len(parents) != 1 || contract.ObjectID(parents[0]) != base.Commit {
		return errors.New("landing integration: moved-base commit must have exactly the refreshed base as parent")
	}
	return nil
}

func verifyReportBase(report *gate.GateReport, base Base) error {
	receipt, ok := report.Receipt()
	if ok && receipt.BaseTree != string(base.Tree) {
		return errors.New("landing integration: moved-base gate receipt is bound to a different base tree")
	}
	return nil
}

func recordVerification(store *journal.RunStore, snapshot *journal.RunSnapshot, clock contract.Clock, base Base, report *gate.GateReport) error {
	counts := report.Counts()
	fields := map[string]any{
		"stage":       "landing",
		"base_commit": base.Commit,
		"base_tree":   base.Tree,
		"verdict":     report.Verdict(),
		"passed":      counts.Passed,
		"total":       counts.Total,
		"landable":    report.IsLandable(),
	}
	if receipt, ok := report.Receipt(); ok {
		fields["candidate_tree"] = receipt.CandidateTree
		fields["started_at"] = receipt.StartedAt
		fields["completed_at"] = receipt.CompletedAt
	}
	return record(store, snapshot, clock, "verification", fields)
}

func preserveCandidate(ctx context.Context, workspaceGit contract.GitPort, workspacePolicy contract.GitPolicy, origin contract.GitPort, originPolicy contract.GitPolicy, ref string, candidate commit.Result) error {
	if err := validateCandidateIDs(candidate); err != nil {
		return fmt.Errorf("landing integration: invalid candidate to preserve: %w", err)
	}
	refs := gitio.NewRefPort(workspaceGit, workspacePolicy)
	resolved, err := refs.ResolveCommit(ctx, string(candidate.Commit))
	if err != nil {
		return fmt.Errorf("landing integration: candidate commit is unavailable in the workspace: %w", err)
	}
	if resolved != candidate.Commit {
		return errors.New("landing integration: candidate commit did not resolve canonically in the workspace")
	}
	tree, err := refs.ResolveTree(ctx, string(candidate.Commit))
	if err != nil {
		return fmt.Errorf("landing integration: resolve preserved candidate tree: %w", err)
	}
	if tree != candidate.Tree {
		return errors.New("landing integration: candidate tree differs from its immutable commit")
	}
	show, err := execChecked(ctx, workspaceGit, workspacePolicy, []string{"show", "-s", "--format=%P", string(candidate.Commit)}, nil, "verify candidate parent before preservation")
	if err != nil {
		return err
	}
	parents := strings.Fields(strings.TrimSpace(string(show.Stdout)))
	if len(parents) != 1 || contract.ObjectID(parents[0]) != candidate.Parent {
		return errors.New("landing integration: candidate does not have its recorded sole parent")
	}
	originRefs := gitio.NewRefPort(origin, originPolicy)
	if existing, err := originRefs.ReadRef(ctx, ref); err != nil {
		return fmt.Errorf("landing integration: inspect preserved candidate ref: %w", err)
	} else if existing.Exists {
		if existing.Target == candidate.Commit {
			return nil
		}
		return fmt.Errorf("landing integration: refusing to replace preserved candidate %s", ref)
	}
	push := []string{
		"push", "--porcelain", "--no-recurse-submodules", "--force-with-lease=" + ref + ":",
		"--", originPolicy.WorkingDirectory, string(candidate.Commit) + ":" + ref,
	}
	result, err := workspaceGit.Exec(ctx, push, nil, workspacePolicy)
	if err != nil {
		return fmt.Errorf("landing integration: preserve candidate object: %w", err)
	}
	if err := requireSuccess("preserve candidate object", result); err != nil {
		// A competing create-only publisher may have installed the same object.
		observed, readErr := originRefs.ReadRef(ctx, ref)
		if readErr == nil && observed.Exists && observed.Target == candidate.Commit {
			return nil
		}
		return err
	}
	observed, err := originRefs.ReadRef(ctx, ref)
	if err != nil || !observed.Exists || observed.Target != candidate.Commit {
		return errors.New("landing integration: preserved candidate ref did not resolve to the candidate")
	}
	return nil
}

func parkCandidate(ctx context.Context, refs *gitio.RefPort, runID string, candidate contract.ObjectID) (string, error) {
	name := "refs/kogen/parked/" + runID
	result, err := refs.CompareAndSwap(ctx, contract.RefUpdate{Name: name, Next: candidate})
	if err == nil && result.Updated {
		return name, nil
	}
	if err != nil && !errors.Is(err, gitio.ErrRefConflict) {
		return "", fmt.Errorf("landing integration: preserve parked candidate: %w", err)
	}
	observed, readErr := refs.ReadRef(ctx, name)
	if readErr == nil && observed.Exists && observed.Target == candidate {
		return name, nil
	}
	if readErr != nil {
		return "", fmt.Errorf("landing integration: inspect parked candidate: %w", readErr)
	}
	return "", errors.New("landing integration: refusing to overwrite an existing parked candidate")
}

func finishParked(request Request, clock contract.Clock) error {
	request.Snapshot.Status = "parked"
	event := journal.NewRunEvent("finished", clock.Now().UnixMilli())
	if err := event.Set("status", "parked"); err != nil {
		return err
	}
	if err := event.Set("reason", "not_landable"); err != nil {
		return err
	}
	for _, key := range []string{"verdict", "rung", "advisory_items"} {
		if raw, ok := request.Snapshot.Fields[key]; ok && json.Valid(raw) {
			event.Fields[key] = append(json.RawMessage(nil), raw...)
		}
	}
	if err := request.RunStore.Record(event, *request.Snapshot); err != nil {
		return fmt.Errorf("landing integration: record parked outcome: %w", err)
	}
	return nil
}

func validateCandidateIDs(candidate commit.Result) error {
	for name, value := range map[string]contract.ObjectID{
		"commit": candidate.Commit, "parent": candidate.Parent,
		"verified tree": candidate.VerifiedTree, "tree": candidate.Tree,
	} {
		if err := gitio.ValidateObjectID(value); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func candidateRefName(runID string, sequence int) string {
	return fmt.Sprintf("refs/kogen/candidates/%s/landing-%06d", runID, sequence)
}

func cloneCandidate(candidate commit.Result) commit.Result {
	candidate.Message = append([]byte(nil), candidate.Message...)
	return candidate
}

func appendUnique(items []string, value string) []string {
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}

func unmergedPaths(ctx context.Context, git contract.GitPort, policy contract.GitPolicy) ([]string, error) {
	result, err := git.Exec(ctx, []string{"ls-files", "-u", "-z"}, nil, policy)
	if err != nil {
		return nil, fmt.Errorf("landing integration: inspect rebase conflicts: %w", err)
	}
	if err := requireSuccess("inspect rebase conflicts", result); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	for _, row := range bytesSplitNUL(result.Stdout) {
		separator := bytes.IndexByte(row, '\t')
		if separator < 0 || separator+1 >= len(row) {
			return nil, errors.New("landing integration: Git returned malformed unmerged path data")
		}
		seen[string(row[separator+1:])] = struct{}{}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func conflictFeedback(attempt RebaseAttempt) string {
	quoted := make([]string, len(attempt.Paths))
	for index, path := range attempt.Paths {
		quoted[index] = strconv.Quote(path)
	}
	return "The target base moved and Git could not rebase these paths: " + strings.Join(quoted, ", ") + ". Resolve the conflicts in this workspace, then continue with the same Build conversation."
}

func safePaths(paths []string) []string {
	out := make([]string, len(paths))
	for index, path := range paths {
		out[index] = strconv.Quote(path)
	}
	return out
}

func bytesSplitNUL(value []byte) [][]byte {
	rows := make([][]byte, 0)
	start := 0
	for index, byteValue := range value {
		if byteValue == 0 {
			if index > start {
				rows = append(rows, append([]byte(nil), value[start:index]...))
			}
			start = index + 1
		}
	}
	if start != len(value) {
		rows = append(rows, append([]byte(nil), value[start:]...))
	}
	return rows
}

func withEnvironment(policy contract.GitPolicy, key, value string) contract.GitPolicy {
	environment := make(map[string]string, len(policy.Environment)+1)
	for _, entry := range policy.Environment {
		name, setting, ok := strings.Cut(entry, "=")
		if ok {
			environment[name] = setting
		}
	}
	environment[key] = value
	keys := make([]string, 0, len(environment))
	for name := range environment {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	policy.Environment = make([]string, 0, len(keys))
	for _, name := range keys {
		policy.Environment = append(policy.Environment, name+"="+environment[name])
	}
	return policy
}

func gitFailure(operation string, result contract.GitResult) error {
	return fmt.Errorf("landing integration: Git %s failed: %s", operation, boundedDetail(result.StderrTail))
}

func boundedDetail(value []byte) string {
	detail := strings.TrimSpace(string(value))
	if detail == "" {
		return "no diagnostic output"
	}
	if len(detail) > 1024 {
		detail = detail[len(detail)-1024:]
	}
	return detail
}
