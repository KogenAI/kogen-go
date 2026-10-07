// Package publish durably publishes a verified landing commit to a project
// base ref. It records the candidate before creating an incoming ref, advances
// the base with compare-and-swap, then updates clean checked-out worktrees.
package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/journal"
	"kogen-go/internal/landing/commit"
	"kogen-go/internal/process"
)

var (
	ErrInvalidRequest = errors.New("landing publish: invalid request")
	ErrNotFastForward = errors.New("landing publish: candidate parent is not the expected base")
	ErrTreeMismatch   = errors.New("landing publish: candidate tree differs from the expected tree")
	ErrIncomingExists = errors.New("landing publish: incoming ref already exists")
)

const maxTransientRetries = 3

// Request contains the durable run record and immutable candidate prepared by
// landing/commit. Repository is the origin checkout or bare repository whose
// refs are being advanced. Processes and Environment must be captured from the
// controller before project child environment construction.
type Request struct {
	Processes   contract.ProcessRunner
	Environment process.Environment
	Repository  string
	Store       *journal.RunStore
	Snapshot    *journal.RunSnapshot
	Candidate   commit.Result
	Clock       contract.Clock
	Observer    Observer
}

// Point identifies a durable publication boundary for diagnostics and crash
// fixtures. An observer error models interruption at that boundary; the
// publisher does not roll back effects that already occurred.
type Point uint8

const (
	RecordDurable Point = iota + 1
	IncomingPublished
	BeforeBaseCAS
	BaseCASSucceeded
)

// Observer runs after the named publication boundary. Production callers
// normally leave it nil.
type Observer interface {
	Reached(Point, commit.Result) error
}

// ObserverFunc adapts a function to Observer.
type ObserverFunc func(Point, commit.Result) error

func (f ObserverFunc) Reached(point Point, candidate commit.Result) error {
	return f(point, candidate)
}

type OutcomeKind string

const (
	Landed    OutcomeKind = "landed"
	BaseMoved OutcomeKind = "base_moved"
)

// Result describes the base publication. BaseMoved means the transient retry
// budget ended after the base ref changed; the moved-base integration owner
// must rebase, re-verify, and call Publish again with a new commit.
type Result struct {
	Kind            OutcomeKind
	Commit          contract.ObjectID
	Tree            contract.ObjectID
	Warnings        []string
	CleanupFailures []string
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Sleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type publisher struct {
	request Request
	git     contract.GitPort
	policy  contract.GitPolicy
	refs    *gitio.RefPort
	clock   contract.Clock
	baseRef string
	inRef   string
}

// Publish validates the candidate commit, records its landing identity, then
// creates refs/kogen/incoming/<run_id> and CAS-updates refs/heads/<branch>.
// Only lock-file and ref-CAS conflicts are retried. Once the base CAS succeeds,
// worktree and incoming-ref cleanup problems are reported but cannot turn the
// Build back into a failure.
func Publish(ctx context.Context, request Request) (Result, error) {
	p, err := newPublisher(request)
	if err != nil {
		return Result{}, err
	}
	if err := p.validate(ctx); err != nil {
		return Result{}, err
	}
	if err := p.recordPrepared(); err != nil {
		return Result{}, err
	}
	if err := p.reached(RecordDurable); err != nil {
		return Result{}, err
	}

	retries := 0
	for {
		locked, err := p.baseLocked(ctx)
		if err != nil {
			return Result{}, err
		}
		if locked {
			decision, err := p.retryOrMove(ctx, retries, "lock")
			if err != nil {
				return Result{}, err
			}
			if decision == baseMoved {
				return p.movedResult(), nil
			}
			retries++
			continue
		}

		if err := p.createIncoming(ctx); err != nil {
			return Result{}, err
		}
		if err := p.reached(IncomingPublished); err != nil {
			return Result{}, err
		}
		checkedOut, err := p.checkedOutBases(ctx)
		if err != nil {
			if cleanupErr := p.deleteIncoming(ctx); cleanupErr != nil {
				return Result{}, errors.Join(err, fmt.Errorf("landing publish: clear incoming ref after checkout inspection failure: %w", cleanupErr))
			}
			return Result{}, err
		}
		if err := p.reached(BeforeBaseCAS); err != nil {
			return Result{}, err
		}

		updated, casErr := p.casBase(ctx)
		if casErr == nil && updated {
			if err := p.reached(BaseCASSucceeded); err != nil {
				return Result{}, err
			}
			return p.finishLanded(ctx, checkedOut)
		}
		observed, readErr := p.refs.ReadRef(ctx, p.baseRef)
		if readErr != nil {
			return Result{}, errors.Join(
				fmt.Errorf("landing publish: inspect base after uncertain compare-and-swap: %w", readErr),
				casErr,
			)
		}
		if observed.Exists && observed.Target == p.request.Candidate.Commit {
			if err := p.reached(BaseCASSucceeded); err != nil {
				return Result{}, err
			}
			return p.finishLanded(ctx, checkedOut)
		}
		if !observed.Exists || observed.Target != p.request.Candidate.Parent {
			// A competing writer moved the branch between CAS and the read.
			casErr = gitio.ErrRefConflict
		}

		transient, reason := p.isTransientCASFailure(ctx, casErr)
		if !transient {
			cleanupErr := p.deleteIncoming(ctx)
			if casErr != nil {
				return Result{}, errors.Join(fmt.Errorf("landing publish: update base ref: %w", casErr), cleanupErr)
			}
			return Result{}, errors.Join(errors.New("landing publish: base compare-and-swap returned no update"), cleanupErr)
		}
		if err := p.deleteIncoming(ctx); err != nil {
			return Result{}, fmt.Errorf("landing publish: clear incoming ref after lost base CAS: %w", err)
		}
		decision, err := p.retryOrMove(ctx, retries, reason)
		if err != nil {
			return Result{}, err
		}
		if decision == baseMoved {
			return p.movedResult(), nil
		}
		retries++
	}
}

type retryDecision uint8

const (
	retryAgain retryDecision = iota + 1
	baseMoved
)

func newPublisher(request Request) (*publisher, error) {
	if request.Repository == "" || !filepath.IsAbs(request.Repository) || filepath.Clean(request.Repository) != request.Repository || strings.ContainsRune(request.Repository, '\x00') {
		return nil, fmt.Errorf("%w: repository must be a clean absolute path", ErrInvalidRequest)
	}
	if request.Store == nil || request.Snapshot == nil {
		return nil, fmt.Errorf("%w: durable run store and snapshot are required", ErrInvalidRequest)
	}
	if request.Snapshot.Status != "running" || request.Snapshot.RunID == "" || request.Snapshot.TargetBranch == "" {
		return nil, fmt.Errorf("%w: snapshot must identify a running Build and target branch", ErrInvalidRequest)
	}
	baseRef, err := targetRef(request.Snapshot.TargetBranch)
	if err != nil {
		return nil, err
	}
	if len(request.Snapshot.RunID) != 32 || !isLowerHex(request.Snapshot.RunID) {
		return nil, fmt.Errorf("%w: run id must be 32 lowercase hexadecimal characters", ErrInvalidRequest)
	}
	if request.Environment == nil || request.Environment["PATH"] == "" {
		return nil, fmt.Errorf("%w: captured controller Git environment with PATH is required", ErrInvalidRequest)
	}
	if request.Processes == nil {
		request.Processes = process.Supervisor{}
	}
	if request.Clock == nil {
		request.Clock = realClock{}
	}
	policy := gitio.OriginPolicy(request.Repository, request.Environment)
	git := gitio.NewOrigin(request.Processes)
	return &publisher{
		request: request,
		git:     git,
		policy:  policy,
		refs:    gitio.NewRefPort(git, policy),
		clock:   request.Clock,
		baseRef: baseRef,
		inRef:   "refs/kogen/incoming/" + request.Snapshot.RunID,
	}, nil
}

func (p *publisher) validate(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrInvalidRequest)
	}
	if err := p.request.Snapshot.Validate(); err != nil {
		return fmt.Errorf("%w: invalid run snapshot: %v", ErrInvalidRequest, err)
	}
	base, err := p.refs.ReadRef(ctx, p.baseRef)
	if err != nil {
		return fmt.Errorf("landing publish: validate target branch ref: %w", err)
	}
	if !base.Exists {
		return fmt.Errorf("%w: target branch ref %q does not exist", ErrInvalidRequest, p.baseRef)
	}
	candidate := p.request.Candidate
	for label, id := range map[string]contract.ObjectID{
		"candidate commit": candidate.Commit,
		"candidate parent": candidate.Parent,
		"candidate tree":   candidate.Tree,
		"verified tree":    candidate.VerifiedTree,
	} {
		if err := gitio.ValidateObjectID(id); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrInvalidRequest, label, err)
		}
	}
	resolved, err := p.refs.ResolveCommit(ctx, string(candidate.Commit))
	if err != nil {
		return fmt.Errorf("landing publish: resolve candidate commit: %w", err)
	}
	if resolved != candidate.Commit {
		return fmt.Errorf("%w: candidate commit did not resolve canonically", ErrInvalidRequest)
	}
	parent, err := p.refs.ResolveCommit(ctx, string(candidate.Parent))
	if err != nil {
		return fmt.Errorf("landing publish: resolve expected parent: %w", err)
	}
	if parent != candidate.Parent {
		return fmt.Errorf("%w: expected parent did not resolve canonically", ErrInvalidRequest)
	}
	tree, err := p.refs.ResolveTree(ctx, string(candidate.Commit))
	if err != nil {
		return fmt.Errorf("landing publish: resolve candidate tree: %w", err)
	}
	if tree != candidate.Tree {
		return fmt.Errorf("%w: expected %s, found %s", ErrTreeMismatch, candidate.Tree, tree)
	}
	show, err := p.run(ctx, p.policy, []string{"show", "-s", "--format=%P", string(candidate.Commit)}, nil)
	if err != nil {
		return fmt.Errorf("landing publish: inspect candidate parent: %w", err)
	}
	parents := strings.Fields(strings.TrimSpace(string(show)))
	if len(parents) != 1 || contract.ObjectID(parents[0]) != candidate.Parent {
		return fmt.Errorf("%w: candidate must have sole parent %s", ErrNotFastForward, candidate.Parent)
	}
	return nil
}

func (p *publisher) recordPrepared() error {
	landing := &journal.LandingRecord{
		ApprovalCommit:  p.request.Snapshot.ApprovalCommit,
		RunID:           p.request.Snapshot.RunID,
		ExpectedParent:  string(p.request.Candidate.Parent),
		FinalTree:       string(p.request.Candidate.Tree),
		CandidateCommit: string(p.request.Candidate.Commit),
	}
	p.request.Snapshot.Landing = landing
	event := journal.NewRunEvent("landing_prepared", p.clock.Now().UnixMilli())
	if err := event.Set("landing", landing); err != nil {
		return err
	}
	if err := p.request.Store.Record(event, *p.request.Snapshot); err != nil {
		return fmt.Errorf("landing publish: persist landing record: %w", err)
	}
	return nil
}

func (p *publisher) baseLocked(ctx context.Context) (bool, error) {
	result, err := p.git.Exec(ctx, []string{"rev-parse", "--git-path", p.baseRef + ".lock"}, nil, p.policy)
	if err != nil {
		return false, fmt.Errorf("landing publish: resolve base lock path: %w", err)
	}
	if result.Process.ExitStatus == nil || *result.Process.ExitStatus != 0 {
		return false, gitCommandError("resolve base lock path", result)
	}
	lock := strings.TrimSuffix(string(result.Stdout), "\n")
	if lock == "" || strings.ContainsRune(lock, '\x00') {
		return false, errors.New("landing publish: Git returned an invalid base lock path")
	}
	if !filepath.IsAbs(lock) {
		lock = filepath.Join(p.request.Repository, lock)
	}
	_, err = os.Lstat(lock)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("landing publish: inspect base lock: %w", err)
	}
	return true, nil
}

func (p *publisher) createIncoming(ctx context.Context) error {
	result, err := p.refs.CompareAndSwap(ctx, contract.RefUpdate{
		Name: p.inRef, Next: p.request.Candidate.Commit,
	})
	if err != nil {
		if errors.Is(err, gitio.ErrRefConflict) {
			return fmt.Errorf("%w: %s", ErrIncomingExists, p.inRef)
		}
		return fmt.Errorf("landing publish: create incoming ref: %w", err)
	}
	if !result.Updated {
		return fmt.Errorf("%w: %s", ErrIncomingExists, p.inRef)
	}
	return nil
}

func (p *publisher) casBase(ctx context.Context) (bool, error) {
	result, err := p.refs.CompareAndSwap(ctx, contract.RefUpdate{
		Name: p.baseRef, Expected: p.request.Candidate.Parent, Next: p.request.Candidate.Commit,
	})
	if err != nil {
		if errors.Is(err, gitio.ErrRefConflict) {
			return false, err
		}
		return false, err
	}
	return result.Updated, nil
}

func (p *publisher) isTransientCASFailure(ctx context.Context, casErr error) (bool, string) {
	if errors.Is(casErr, gitio.ErrRefConflict) {
		return true, "cas"
	}
	if casErr == nil {
		return true, "cas"
	}
	locked, err := p.baseLocked(ctx)
	if err == nil && locked {
		return true, "lock"
	}
	return false, ""
}

func (p *publisher) retryOrMove(ctx context.Context, retries int, reason string) (retryDecision, error) {
	if retries >= maxTransientRetries {
		if err := p.recordRetry("rebase_required", 0); err != nil {
			return 0, err
		}
		return baseMoved, nil
	}
	delay := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}[retries]
	if err := p.recordRetry(reason, delay.Milliseconds()); err != nil {
		return 0, err
	}
	if err := p.clock.Sleep(ctx, delay); err != nil {
		return 0, fmt.Errorf("landing publish: wait before retry: %w", err)
	}
	return retryAgain, nil
}

func (p *publisher) recordRetry(reason string, delayMS int64) error {
	event := journal.NewRunEvent("landing_retry", p.clock.Now().UnixMilli())
	if err := event.Set("reason", reason); err != nil {
		return err
	}
	if err := event.Set("delay_ms", delayMS); err != nil {
		return err
	}
	if err := p.request.Store.Record(event, *p.request.Snapshot); err != nil {
		return fmt.Errorf("landing publish: persist retry event: %w", err)
	}
	return nil
}

func (p *publisher) deleteIncoming(ctx context.Context) error {
	result, err := p.refs.CompareAndSwap(ctx, contract.RefUpdate{
		Name: p.inRef, Expected: p.request.Candidate.Commit,
	})
	if err != nil {
		return err
	}
	if !result.Updated {
		return errors.New("incoming ref changed before cleanup")
	}
	return nil
}

func (p *publisher) finishLanded(ctx context.Context, checkedOut []checkedOutWorktree) (Result, error) {
	result := Result{
		Kind:   Landed,
		Commit: p.request.Candidate.Commit,
		Tree:   p.request.Candidate.Tree,
	}
	if err := p.recordEvent("base_cas", map[string]any{"candidate_commit": p.request.Candidate.Commit}); err != nil {
		result.CleanupFailures = append(result.CleanupFailures, err.Error())
	}
	for _, update := range checkedOut {
		if !update.dirty && p.updateCheckedOut(ctx, update.path) == nil {
			continue
		}
		warning := checkoutWarning(p.request.Candidate.Commit, p.request.Snapshot.TargetBranch, update.path)
		result.Warnings = append(result.Warnings, warning)
		if recordErr := p.recordEvent("landing_warning", map[string]any{"path": update.path, "detail": warning}); recordErr != nil {
			result.CleanupFailures = append(result.CleanupFailures, recordErr.Error())
		}
	}
	if err := p.deleteIncoming(ctx); err != nil {
		p.recordCleanupFailure(&result, "delete incoming ref", err)
	}
	p.request.Snapshot.Status = "landed"
	finished := journal.NewRunEvent("finished", p.clock.Now().UnixMilli())
	_ = finished.Set("status", "landed")
	_ = finished.Set("reason", "")
	for _, key := range []string{"verdict", "rung", "advisory_items"} {
		if raw, ok := p.request.Snapshot.Fields[key]; ok && json.Valid(raw) {
			finished.Fields[key] = append(json.RawMessage(nil), raw...)
		}
	}
	if err := p.request.Store.Record(finished, *p.request.Snapshot); err != nil {
		result.CleanupFailures = append(result.CleanupFailures, fmt.Errorf("landing publish: record landed outcome: %w", err).Error())
	}
	return result, nil
}

type checkedOutWorktree struct {
	path  string
	dirty bool
}

func (p *publisher) checkedOutBases(ctx context.Context) ([]checkedOutWorktree, error) {
	output, err := p.run(ctx, p.policy, []string{"worktree", "list", "--porcelain", "-z"}, nil)
	if err != nil {
		return nil, fmt.Errorf("landing publish: list checked-out worktrees: %w", err)
	}
	rows, err := parseWorktrees(output)
	if err != nil {
		return nil, err
	}
	updates := make([]checkedOutWorktree, 0, len(rows))
	for _, row := range rows {
		if row.branch != p.baseRef || row.path == "" {
			continue
		}
		policy := p.policy
		policy.WorkingDirectory = row.path
		status, err := p.run(ctx, policy, []string{"status", "--porcelain=v1", "--untracked-files=all"}, nil)
		if err != nil {
			return nil, fmt.Errorf("landing publish: inspect checked-out base %q: %w", row.path, err)
		}
		updates = append(updates, checkedOutWorktree{path: row.path, dirty: len(status) != 0})
	}
	return updates, nil
}

type worktreeRow struct {
	path   string
	branch string
}

func parseWorktrees(output []byte) ([]worktreeRow, error) {
	fields := strings.Split(string(output), "\x00")
	rows := make([]worktreeRow, 0)
	var current worktreeRow
	seenPath := false
	flush := func() error {
		if !seenPath {
			if current.branch != "" {
				return errors.New("landing publish: malformed worktree listing")
			}
			return nil
		}
		rows = append(rows, current)
		current = worktreeRow{}
		seenPath = false
		return nil
	}
	for _, field := range fields {
		if field == "" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		key, value, ok := strings.Cut(field, " ")
		if !ok {
			key, value = field, ""
		}
		switch key {
		case "worktree":
			if seenPath {
				if err := flush(); err != nil {
					return nil, err
				}
			}
			current.path = value
			seenPath = true
		case "branch":
			current.branch = value
		case "HEAD", "detached", "bare", "locked", "prunable":
		default:
			return nil, fmt.Errorf("landing publish: unsupported worktree field %q", key)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return rows, nil
}

func (p *publisher) updateCheckedOut(ctx context.Context, directory string) error {
	policy := p.policy
	policy.WorkingDirectory = directory
	branch, err := p.run(ctx, policy, []string{"symbolic-ref", "-q", "HEAD"}, nil)
	if err != nil || strings.TrimSpace(string(branch)) != p.baseRef {
		return errors.New("checked-out branch changed after landing inspection")
	}
	clean, err := p.cleanAgainst(ctx, policy, p.request.Candidate.Parent)
	if err != nil {
		return err
	}
	if !clean {
		return errors.New("checked-out base became dirty after landing inspection")
	}
	if _, err := p.run(ctx, policy, []string{"update-ref", "--no-deref", "HEAD", string(p.request.Candidate.Parent)}, nil); err != nil {
		return err
	}
	if _, err := p.run(ctx, policy, []string{"reset", "--keep", string(p.request.Candidate.Commit)}, nil); err != nil {
		_, _ = p.run(context.Background(), policy, []string{"symbolic-ref", "HEAD", p.baseRef}, nil)
		return err
	}
	if _, err := p.run(ctx, policy, []string{"symbolic-ref", "HEAD", p.baseRef}, nil); err != nil {
		return err
	}
	return nil
}

func (p *publisher) cleanAgainst(ctx context.Context, policy contract.GitPolicy, expected contract.ObjectID) (bool, error) {
	diff, err := p.git.Exec(ctx, []string{"diff", "--quiet", "--no-ext-diff", string(expected), "--"}, nil, policy)
	if err != nil {
		return false, err
	}
	if diff.Process.ExitStatus == nil {
		return false, errors.New("landing publish: git diff returned no exit status")
	}
	if *diff.Process.ExitStatus == 1 {
		return false, nil
	}
	if *diff.Process.ExitStatus != 0 {
		return false, gitCommandError("diff checked-out base", diff)
	}
	untracked, err := p.run(ctx, policy, []string{"ls-files", "--others", "--exclude-standard", "-z"}, nil)
	if err != nil {
		return false, err
	}
	return len(untracked) == 0, nil
}

func (p *publisher) movedResult() Result {
	return Result{Kind: BaseMoved, Commit: p.request.Candidate.Commit, Tree: p.request.Candidate.Tree}
}

func (p *publisher) reached(point Point) error {
	if p.request.Observer == nil {
		return nil
	}
	if err := p.request.Observer.Reached(point, p.request.Candidate); err != nil {
		return fmt.Errorf("landing publish: interrupted at boundary %d: %w", point, err)
	}
	return nil
}

func (p *publisher) recordEvent(name string, fields map[string]any) error {
	event := journal.NewRunEvent(name, p.clock.Now().UnixMilli())
	for key, value := range fields {
		if err := event.Set(key, value); err != nil {
			return err
		}
	}
	if err := p.request.Store.Record(event, *p.request.Snapshot); err != nil {
		return fmt.Errorf("landing publish: persist %s event: %w", name, err)
	}
	return nil
}

func (p *publisher) recordCleanupFailure(result *Result, operation string, cause error) {
	detail := fmt.Sprintf("landing publish: %s: %v", operation, cause)
	result.CleanupFailures = append(result.CleanupFailures, detail)
	if err := p.recordEvent("cleanup_failure", map[string]any{"operation": operation, "detail": cause.Error()}); err != nil {
		result.CleanupFailures = append(result.CleanupFailures, err.Error())
	}
}

func (p *publisher) run(ctx context.Context, policy contract.GitPolicy, args []string, stdin []byte) ([]byte, error) {
	result, err := p.git.Exec(ctx, args, stdin, policy)
	if err != nil {
		return nil, err
	}
	if result.Process.ExitStatus == nil || *result.Process.ExitStatus != 0 {
		return nil, gitCommandError(strings.Join(args[:min(len(args), 2)], " "), result)
	}
	return result.Stdout, nil
}

func gitCommandError(operation string, result contract.GitResult) error {
	detail := strings.TrimSpace(string(result.StderrTail))
	if detail == "" {
		detail = "no diagnostic output"
	}
	if len(detail) > 1024 {
		detail = detail[len(detail)-1024:]
	}
	status := "unknown"
	if result.Process.ExitStatus != nil {
		status = fmt.Sprint(*result.Process.ExitStatus)
	}
	return fmt.Errorf("git %s failed (status %s): %s", operation, status, detail)
}

func targetRef(branch string) (string, error) {
	branch = strings.TrimPrefix(branch, "refs/heads/")
	if branch == "" || strings.ContainsAny(branch, "\x00\n\r") {
		return "", fmt.Errorf("%w: target branch is invalid", ErrInvalidRequest)
	}
	return "refs/heads/" + branch, nil
}

func checkoutWarning(commit contract.ObjectID, branch, directory string) string {
	shortBranch := strings.TrimPrefix(branch, "refs/heads/")
	return fmt.Sprintf("land: warning: landed %s on %s; your checkout at %s has local changes and was not updated; run `git reset --keep %s`, or merge it yourself", commit, shortBranch, directory, commit)
}

func isLowerHex(value string) bool {
	for _, char := range value {
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}
