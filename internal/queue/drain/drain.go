//go:build darwin || linux

package drain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"kogen-go/internal/build/single"
	"kogen-go/internal/contract"
	"kogen-go/internal/journal"
	"kogen-go/internal/project"
	"kogen-go/internal/queue/lock"
	"kogen-go/internal/queue/schedule"
	"kogen-go/internal/safefs"
)

// RecoveryPort reconciles prior Builds before a queue snapshot is read.
type RecoveryPort interface {
	Recover(context.Context, *project.Resolution) error
}

// RecoveryFunc adapts a function to RecoveryPort.
type RecoveryFunc func(context.Context, *project.Resolution) error

// Recover implements RecoveryPort.
func (f RecoveryFunc) Recover(ctx context.Context, resolved *project.Resolution) error {
	return f(ctx, resolved)
}

// SnapshotPort returns the status-derived candidates. It should include
// blocked approvals with Blocked set so they remain visible to the scheduler
// without being selected.
type SnapshotPort interface {
	Load(context.Context, *project.Resolution) ([]schedule.QueueApproval, error)
}

// SnapshotFunc adapts a function to SnapshotPort.
type SnapshotFunc func(context.Context, *project.Resolution) ([]schedule.QueueApproval, error)

// Load implements SnapshotPort.
func (f SnapshotFunc) Load(ctx context.Context, resolved *project.Resolution) ([]schedule.QueueApproval, error) {
	return f(ctx, resolved)
}

// BuildPort runs one approved Intent. The implementation must use ctx for all
// child work so queue signals can stop process groups before returning.
type BuildPort interface {
	Run(context.Context, *project.Resolution, string) (single.Outcome, error)
}

// SingleBuilder adapts the one-rung controller to BuildPort.
type SingleBuilder struct {
	Controller *single.Controller
}

// Run executes one approved slug with the configured single-rung controller.
func (b SingleBuilder) Run(ctx context.Context, resolved *project.Resolution, slug string) (single.Outcome, error) {
	if b.Controller == nil {
		return single.Outcome{}, failure("controller", "build_unavailable", 70, errors.New("single Build controller is unavailable"))
	}
	return b.Controller.Run(ctx, single.Request{Project: resolved, Slug: slug})
}

// Dependencies are the production effects used by a drain. Recovery, status
// derivation, and Build execution remain owned by their respective packages.
type Dependencies struct {
	Recovery RecoveryPort
	Snapshot SnapshotPort
	Build    BuildPort
	Output   io.Writer
	// Signals is an optional signal channel for embedding and deterministic
	// lifecycle control. When nil, Start listens for SIGINT and SIGTERM.
	Signals <-chan os.Signal
}

// Controller owns public queue start/stop/detach orchestration.
type Controller struct {
	deps Dependencies
}

// StartOptions controls the public queue start operation.
type StartOptions struct {
	Detach     bool
	Executable string
	Args       []string
}

// Result is the final drain count and process exit. Output lines are written
// as they happen through Dependencies.Output.
type Result struct {
	ExitCode int
	Built    uint64
	Landed   uint64
	Queue    []string
	Final    string
	PID      int
	LogPath  string
	// InterruptionError is set when the signal exit was honored but its run
	// journal could not be marked for recovery as interrupted.
	InterruptionError error
}

// NewController validates all non-optional drain effects.
func NewController(deps Dependencies) (*Controller, error) {
	if deps.Recovery == nil || deps.Snapshot == nil || deps.Build == nil {
		return nil, errors.New("queue drain requires recovery, snapshot, and Build ports")
	}
	if deps.Output == nil {
		deps.Output = os.Stdout
	}
	return &Controller{deps: deps}, nil
}

// Start begins a foreground drain or relaunches the same public command in a
// detached session. The queue lock is held for the whole foreground drain.
func (c *Controller) Start(ctx context.Context, resolved *project.Resolution, options StartOptions) (Result, error) {
	if c == nil || ctx == nil || resolved == nil {
		return Result{}, failure("controller", "request_invalid", 70, errors.New("queue start requires a controller, context, and resolved project"))
	}
	if err := ensureStateRoot(resolved.StateRoot); err != nil {
		return Result{}, failure("environment", "queue_lock_failed", 3, err)
	}
	if options.Detach {
		args := append([]string(nil), options.Args...)
		if args == nil {
			args = []string{"queue", "start", "--project", resolved.Checkout, "--origin", resolved.Origin, "--base", resolved.Base}
		}
		launched, err := lock.Detach(lock.DetachSpec{
			StateRoot: resolved.StateRoot, Executable: options.Executable, Args: args, Dir: resolved.Checkout,
		})
		if err != nil {
			return Result{}, detachFailure(err)
		}
		if launched.AlreadyRunning {
			if err := c.writeLine(fmt.Sprintf("queue: already running (pid %d)\n", launched.PID)); err != nil {
				return Result{}, failure("controller", "output_failed", 70, err)
			}
			return Result{PID: launched.PID}, nil
		}
		if err := c.writeLine(fmt.Sprintf("queue: started in the background (pid %d)\nlog: %s\n", launched.PID, launched.LogPath)); err != nil {
			return Result{}, failure("controller", "output_failed", 70, err)
		}
		return Result{PID: launched.PID, LogPath: launched.LogPath}, nil
	}
	return c.drain(ctx, resolved)
}

// Stop asks a live owner to stop after its current Build. A dead stale owner
// is removed by the lock protocol and is reported as not running.
func (c *Controller) Stop(ctx context.Context, resolved *project.Resolution) (Result, error) {
	if c == nil || ctx == nil || resolved == nil {
		return Result{}, failure("controller", "request_invalid", 70, errors.New("queue stop requires a controller, context, and resolved project"))
	}
	if err := ensureStateRoot(resolved.StateRoot); err != nil {
		return Result{}, failure("environment", "queue_lock_failed", 3, err)
	}
	requested, err := lock.RequestStop(resolved.StateRoot)
	if err != nil {
		return Result{}, lockFailure(err)
	}
	if requested.Requested {
		line := fmt.Sprintf("queue: stopping after the current Build (pid %d)\n", requested.PID)
		if err := c.writeLine(line); err != nil {
			return Result{}, failure("controller", "output_failed", 70, err)
		}
		return Result{PID: requested.PID}, nil
	}
	if err := c.writeLine("queue: not running\n"); err != nil {
		return Result{}, failure("controller", "output_failed", 70, err)
	}
	return Result{}, nil
}

func (c *Controller) drain(parent context.Context, resolved *project.Resolution) (result Result, retErr error) {
	started, err := lock.Acquire(resolved.StateRoot)
	if err != nil {
		return Result{}, lockFailure(err)
	}
	if started.Owner == nil {
		if err := c.writeLine(fmt.Sprintf("queue: already running (pid %d)\n", started.PID)); err != nil {
			return Result{}, failure("controller", "output_failed", 70, err)
		}
		return Result{PID: started.PID}, nil
	}
	owner := started.Owner
	defer func() {
		if err := owner.Release(); err != nil && retErr == nil {
			retErr = failure("environment", "queue_lock_unavailable", 3, err)
		}
	}()

	runCtx, cancel := context.WithCancel(parent)
	defer cancel()
	signalChannel, stopSignals := c.signalChannel()
	defer stopSignals()
	var signalExit atomic.Int32
	watchDone := make(chan struct{})
	watchStopped := make(chan struct{})
	go func() {
		defer close(watchStopped)
		select {
		case received, ok := <-signalChannel:
			if ok && received != nil {
				signalExit.Store(int32(exitForSignal(received)))
				cancel()
			}
		case <-parent.Done():
		case <-watchDone:
		}
	}()
	defer func() {
		close(watchDone)
		<-watchStopped
	}()

	if err := c.deps.Recovery.Recover(runCtx, resolved); err != nil {
		if code := int(signalExit.Load()); code != 0 {
			return Result{ExitCode: code}, nil
		}
		return Result{}, failure("controller", "recovery_failed", 70, err)
	}
	approvals, err := c.deps.Snapshot.Load(runCtx, resolved)
	if err != nil {
		if code := int(signalExit.Load()); code != 0 {
			return Result{ExitCode: code}, nil
		}
		return Result{}, failure("controller", "queue_snapshot_failed", 70, err)
	}
	if code := int(signalExit.Load()); code != 0 {
		return Result{ExitCode: code}, nil
	}

	scheduler := schedule.New(resolved.Base)
	for _, approval := range approvals {
		observation := scheduler.Apply(schedule.QueueEvent{Kind: schedule.EventEnqueue, Approval: approval})
		if observation.Last != "ok" {
			return Result{}, failure("controller", "queue_snapshot_invalid", 70, fmt.Errorf("queue snapshot was rejected: %s", observation.Last))
		}
	}
	observation := scheduler.Apply(schedule.QueueEvent{Kind: schedule.EventStart})
	var stopped *stopReason

	for observation.Current != "" {
		if code := int(signalExit.Load()); code != 0 {
			return Result{ExitCode: code, Built: observation.Built, Landed: observation.Landed}, nil
		}
		stopRequested, stopErr := owner.StopRequested()
		if stopErr != nil {
			return Result{Built: observation.Built, Landed: observation.Landed}, lockFailure(stopErr)
		}
		if stopRequested {
			current := observation.Current
			remaining := make([]string, 0, len(observation.Queue)+1)
			remaining = append(remaining, current)
			remaining = append(remaining, observation.Queue...)
			stopped := observation
			stopped.Line = "stopped_on_request"
			stopped.Exit = normalExit(stopped.Built, stopped.Landed)
			final := finalLine(stopped, nil)
			if err := c.writeLine(final); err != nil {
				return Result{Built: observation.Built, Landed: observation.Landed, Queue: remaining}, failure("controller", "output_failed", 70, err)
			}
			return Result{ExitCode: stopped.Exit, Built: stopped.Built, Landed: stopped.Landed, Queue: remaining, Final: final}, nil
		}

		current := observation.Current
		if observation.Phase == "skipping" {
			if err := c.writeLine(fmt.Sprintf("skipped %s: environment/approval_branch_mismatch\n", current)); err != nil {
				return Result{Built: observation.Built, Landed: observation.Landed}, failure("controller", "output_failed", 70, err)
			}
			observation = scheduler.Apply(schedule.QueueEvent{Kind: schedule.EventOutcome, Outcome: schedule.OutcomeSkipped})
			continue
		}
		if err := c.writeLine(fmt.Sprintf("building %s\n", current)); err != nil {
			return Result{Built: observation.Built, Landed: observation.Landed}, failure("controller", "output_failed", 70, err)
		}
		built, buildErr := c.deps.Build.Run(runCtx, resolved, current)
		if code := int(signalExit.Load()); code != 0 {
			var interruptErr error
			if built.RunID != "" {
				interruptErr = recordInterrupted(resolved.StateRoot, built.RunID, signalReason(code))
			}
			return Result{ExitCode: code, Built: observation.Built, Landed: observation.Landed, InterruptionError: interruptErr}, nil
		}
		if buildErr != nil {
			class, reason := errorIdentity(buildErr)
			if err := c.writeLine(stoppedLine(current, class, reason, built.RunID)); err != nil {
				return Result{Built: observation.Built, Landed: observation.Landed}, failure("controller", "output_failed", 70, err)
			}
			stopped = &stopReason{slug: current, class: class}
			observation = scheduler.Apply(schedule.QueueEvent{Kind: schedule.EventOutcome, Outcome: stoppedOutcome(class)})
			break
		}

		outcome, line, terminalStop, mapErr := drainOutcome(current, built)
		if mapErr != nil {
			class, reason := "controller", "build_outcome_invalid"
			if err := c.writeLine(stoppedLine(current, class, reason, built.RunID)); err != nil {
				return Result{Built: observation.Built, Landed: observation.Landed}, failure("controller", "output_failed", 70, err)
			}
			stopped = &stopReason{slug: current, class: class}
			observation = scheduler.Apply(schedule.QueueEvent{Kind: schedule.EventOutcome, Outcome: stoppedOutcome(class)})
			break
		}
		if line != "" {
			if err := c.writeLine(line); err != nil {
				return Result{Built: observation.Built, Landed: observation.Landed}, failure("controller", "output_failed", 70, err)
			}
		}

		if terminalStop {
			latest, refreshErr := c.deps.Snapshot.Load(runCtx, resolved)
			if refreshErr != nil {
				if code := int(signalExit.Load()); code != 0 {
					return Result{ExitCode: code, Built: observation.Built, Landed: observation.Landed}, nil
				}
				return Result{Built: observation.Built, Landed: observation.Landed}, failure("controller", "queue_snapshot_failed", 70, refreshErr)
			}
			scheduler.Apply(schedule.QueueEvent{Kind: schedule.EventRefresh, Approvals: latest})
			if _, ok := findApproval(latest, current); !ok {
				if prior, known := findApproval(approvals, current); known {
					scheduler.Apply(schedule.QueueEvent{Kind: schedule.EventEnqueue, Approval: prior})
				}
			}
			class := "environment"
			if built.Failure != nil {
				class, _ = errorIdentity(built.Failure)
			}
			stopped = &stopReason{slug: current, class: class}
			observation = scheduler.Apply(schedule.QueueEvent{Kind: schedule.EventOutcome, Outcome: outcome})
			break
		}

		latest, refreshErr := c.deps.Snapshot.Load(runCtx, resolved)
		if refreshErr != nil {
			if code := int(signalExit.Load()); code != 0 {
				return Result{ExitCode: code, Built: observation.Built, Landed: observation.Landed}, nil
			}
			return Result{Built: observation.Built, Landed: observation.Landed}, failure("controller", "queue_snapshot_failed", 70, refreshErr)
		}
		observation = scheduler.Apply(schedule.QueueEvent{Kind: schedule.EventRefresh, Approvals: latest})
		approvals = latest
		stopRequested, stopErr = owner.StopRequested()
		if stopErr != nil {
			return Result{Built: observation.Built, Landed: observation.Landed}, lockFailure(stopErr)
		}
		if stopRequested {
			scheduler.Apply(schedule.QueueEvent{Kind: schedule.EventHalt})
		}
		observation = scheduler.Apply(schedule.QueueEvent{Kind: schedule.EventOutcome, Outcome: outcome})
	}

	if code := int(signalExit.Load()); code != 0 {
		return Result{ExitCode: code, Built: observation.Built, Landed: observation.Landed}, nil
	}
	final := finalLine(observation, stopped)
	if err := c.writeLine(final); err != nil {
		return Result{Built: observation.Built, Landed: observation.Landed}, failure("controller", "output_failed", 70, err)
	}
	return Result{ExitCode: observation.Exit, Built: observation.Built, Landed: observation.Landed, Queue: append([]string(nil), observation.Queue...), Final: final}, nil
}

func (c *Controller) signalChannel() (<-chan os.Signal, func()) {
	if c.deps.Signals != nil {
		return c.deps.Signals, func() {}
	}
	channel := make(chan os.Signal, 1)
	signal.Notify(channel, os.Interrupt, syscall.SIGTERM)
	return channel, func() { signal.Stop(channel) }
}

func (c *Controller) writeLine(line string) error {
	written, err := io.WriteString(c.deps.Output, line)
	if err != nil {
		return err
	}
	if written != len(line) {
		return io.ErrShortWrite
	}
	if flusher, ok := c.deps.Output.(interface{ Flush() error }); ok {
		return flusher.Flush()
	}
	return nil
}

func ensureStateRoot(stateRoot string) error {
	if !filepath.IsAbs(stateRoot) || filepath.Clean(stateRoot) != stateRoot || stateRoot == string(filepath.Separator) {
		return errors.New("queue state root must be a clean absolute path")
	}
	root, err := safefs.OpenRoot(string(filepath.Separator))
	if err != nil {
		return err
	}
	defer root.Close()
	name := strings.TrimPrefix(stateRoot, string(filepath.Separator))
	if err := root.MkdirAll(name, 0o700); err != nil {
		return err
	}
	parent := filepath.ToSlash(filepath.Dir(name))
	if parent == "." {
		parent = ""
	}
	return root.SyncDir(parent)
}

type stopReason struct {
	slug  string
	class string
}

func drainOutcome(slug string, outcome single.Outcome) (schedule.DrainOutcome, string, bool, error) {
	switch outcome.Status {
	case "landed":
		if outcome.Commit == "" || outcome.RunID == "" {
			return schedule.OutcomeStoppedController, "", true, errors.New("landed Build omitted its commit or run identity")
		}
		return schedule.OutcomeLanded, fmt.Sprintf("landed %s %s (Build %s)\n", slug, short(string(outcome.Commit)), short(outcome.RunID)), false, nil
	case "failed", "failed_provider":
		reason, verdict, runID := fallback(outcome.Reason, "unknown"), fallback(outcome.Verdict, "unknown"), outcome.RunID
		if runID == "" {
			return schedule.OutcomeStoppedController, "", true, errors.New("failed Build omitted its run identity")
		}
		kind := schedule.OutcomeFailed
		if outcome.Status == "failed_provider" {
			kind = schedule.OutcomeFailedProvider
		}
		return kind, fmt.Sprintf("failed %s: %s; best candidate %s at refs/kogen/parked/%s (Build %s)\n", slug, reason, verdict, runID, short(runID)), false, nil
	case "parked":
		reason, verdict, runID := fallback(outcome.Reason, "unknown"), fallback(outcome.Verdict, "unknown"), outcome.RunID
		if runID == "" {
			return schedule.OutcomeStoppedController, "", true, errors.New("parked Build omitted its run identity")
		}
		return schedule.OutcomeParked, fmt.Sprintf("parked %s: %s; best candidate %s at refs/kogen/parked/%s (Build %s)\n", slug, reason, verdict, runID, short(runID)), false, nil
	case "stopped":
		class, reason := errorIdentity(outcome.Failure)
		if outcome.Failure == nil {
			class = "environment"
			reason = fallback(outcome.Reason, "build_stopped")
		}
		return stoppedOutcome(class), stoppedLine(slug, class, reason, outcome.RunID), true, nil
	case "skipped":
		return schedule.OutcomeSkipped, fmt.Sprintf("skipped %s: environment/approval_branch_mismatch\n", slug), false, nil
	default:
		return schedule.OutcomeStoppedController, "", true, fmt.Errorf("Build returned unsupported status %q", outcome.Status)
	}
}

func stoppedLine(slug, class, reason, runID string) string {
	line := fmt.Sprintf("stopped %s: %s/%s; it stays queued", slug, fallback(class, "environment"), fallback(reason, "build_stopped"))
	if runID != "" {
		line += fmt.Sprintf(" (Build %s)", short(runID))
	}
	return line + "\n"
}

func stoppedOutcome(class string) schedule.DrainOutcome {
	switch class {
	case "provider":
		return schedule.OutcomeStoppedProvider
	case "controller":
		return schedule.OutcomeStoppedController
	default:
		return schedule.OutcomeStoppedEnvironment
	}
}

func errorIdentity(err error) (string, string) {
	var failure *contract.Failure
	if errors.As(err, &failure) {
		return fallback(string(failure.Class), "controller"), fallback(string(failure.Reason), "internal_error")
	}
	return "controller", "internal_error"
}

func finalLine(observation schedule.QueueObservation, stopped *stopReason) string {
	if stopped != nil {
		article := "a"
		if stopped.class == "environment" {
			article = "an"
		}
		return fmt.Sprintf("queue: stopped because %s hit %s %s error; %d Build(s), %d landed, %d not\n", stopped.slug, article, stopped.class, observation.Built, observation.Landed, observation.Built-observation.Landed)
	}
	if observation.Line == "stopped_on_request" {
		return fmt.Sprintf("queue: stopped on request; %d Build(s), %d landed, %d not\n", observation.Built, observation.Landed, observation.Built-observation.Landed)
	}
	if observation.Built == 0 {
		return "queue: nothing to build\n"
	}
	return fmt.Sprintf("queue: done; %d Build(s), %d landed, %d not\n", observation.Built, observation.Landed, observation.Built-observation.Landed)
}

func normalExit(built, landed uint64) int {
	if built == 0 || landed == built {
		return 0
	}
	return 1
}

func lockFailure(err error) error {
	var lockErr *lock.Error
	if errors.As(err, &lockErr) {
		return failure("environment", lockErr.Reason, 3, err)
	}
	return failure("environment", "queue_lock_failed", 3, err)
}

func detachFailure(err error) error {
	var lockErr *lock.Error
	if errors.As(err, &lockErr) {
		return failure("environment", lockErr.Reason, 3, err)
	}
	return failure("environment", lock.ReasonDetachUnavailable, 3, err)
}

func failure(class, reason string, exit int, cause error) *contract.Failure {
	return &contract.Failure{Class: contract.ErrorClass(class), Reason: contract.ErrorReason(reason), Exit: contract.ExitCode(exit), Cause: cause}
}

func short(value string) string {
	if len(value) > 8 {
		return value[:8]
	}
	return value
}

func fallback(value, otherwise string) string {
	if strings.TrimSpace(value) == "" {
		return otherwise
	}
	return value
}

func exitForSignal(received os.Signal) int {
	switch received {
	case os.Interrupt:
		return 130
	case syscall.SIGTERM:
		return 143
	default:
		return 128
	}
}

func signalReason(exit int) string {
	if exit == 130 {
		return "sigint"
	}
	return "sigterm"
}

func recordInterrupted(stateRoot, runID, reason string) error {
	if !safeRunID(runID) || (reason != "sigint" && reason != "sigterm") {
		return errors.New("queue drain: invalid interrupted run identity or reason")
	}
	root, err := safefs.OpenRoot(stateRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	store, err := journal.NewRunStore(root, "runs/"+runID)
	if err != nil {
		return err
	}
	snapshot, err := store.ReadSnapshot()
	if err != nil {
		return err
	}
	if snapshot.RunID != runID {
		return errors.New("queue drain: run journal identity changed during interruption")
	}
	if snapshot.Status != "running" && snapshot.Status != "stopped" {
		return nil
	}
	snapshot.Status = "running"
	event := journal.NewRunEvent("interrupted", time.Now().UnixMilli())
	if err := event.Set("reason", reason); err != nil {
		return err
	}
	return store.Record(event, snapshot)
}

func safeRunID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func findApproval(approvals []schedule.QueueApproval, slug string) (schedule.QueueApproval, bool) {
	for _, approval := range approvals {
		if approval.Slug == slug {
			return approval, true
		}
	}
	return schedule.QueueApproval{}, false
}
