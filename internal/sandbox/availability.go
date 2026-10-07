package sandbox

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"kogen-go/internal/contract"
	"kogen-go/internal/sandbox/darwin"
)

var (
	// ErrIntegritySnapshotRequired means an unconfined Build has no trusted
	// checkout/origin integrity observer, so candidate code must not start.
	ErrIntegritySnapshotRequired = errors.New("sandbox: unconfined Build requires an integrity snapshot")
	// ErrIntegrityChanged means checkout/index/tracked bytes or origin refs
	// changed while an unconfined child was running.
	ErrIntegrityChanged = errors.New("sandbox: unconfined child changed protected source integrity")
)

// IntegritySnapshotter returns a stable identity over the checkout HEAD,
// index, tracked bytes and origin refs. The Build composition supplies the
// Git-backed implementation; sandbox refuses an unconfined Build without it.
type IntegritySnapshotter interface {
	Snapshot(context.Context) (string, error)
}

// IntegritySnapshotFunc adapts a function to IntegritySnapshotter.
type IntegritySnapshotFunc func(context.Context) (string, error)

func (f IntegritySnapshotFunc) Snapshot(ctx context.Context) (string, error) { return f(ctx) }

// Availability is the result of a real OS enforcement probe. Checks contains
// stable check names only; it never contains paths, credentials or file data.
type Availability struct {
	Available bool
	Reason    string
	Checks    []string
}

// Probe performs positive and negative checks using the supplied policy. On
// macOS it verifies actual Seatbelt enforcement, not just the presence of
// sandbox-exec. Other hosts remain unavailable until their platform adapter is
// installed.
func Probe(ctx context.Context, processes contract.ProcessRunner, spec contract.ProcessSpec, policy Policy) (Availability, error) {
	if ctx == nil {
		return Availability{}, errors.New("sandbox: probe context is required")
	}
	if processes == nil {
		return Availability{}, errors.New("sandbox: process runner is required")
	}
	if runtime.GOOS != "darwin" {
		return Availability{Reason: "no supported confinement tool is available on this host"}, nil
	}
	if !darwin.ToolAvailable() {
		return Availability{Reason: "macOS sandbox-exec is unavailable"}, nil
	}
	result, err := darwin.Probe(ctx, processes, spec, darwin.Configuration{
		RunDir:             policy.runDir,
		Workspace:          policy.workspace,
		WritablePaths:      append([]string(nil), policy.writablePaths...),
		WriteDeniedPaths:   append([]string(nil), policy.writeDeniedPaths...),
		ProtectedReadPaths: append([]string(nil), policy.protectedReadPaths...),
	})
	if err != nil {
		return Availability{}, err
	}
	return Availability{Available: result.Available, Reason: result.Reason, Checks: append([]string(nil), result.Checks...)}, nil
}

// Execution combines the supervised process effect with its effective
// sandbox mode. Public contract.ProcessResult intentionally remains an
// observation of the process itself and is not rewritten with policy state.
type Execution struct {
	Process     contract.ProcessResult
	Observation Observation
}

// Runner decorates a ProcessRunner with one immutable sandbox policy. It runs
// the real enforcement probe once per runner and reuses only that availability
// result; each confined child still gets a fresh private policy file.
type Runner struct {
	inner     contract.ProcessRunner
	policy    Policy
	integrity IntegritySnapshotter

	probeMu   sync.Mutex
	probed    bool
	available Availability
}

// NewRunner constructs a policy-bound process adapter. A nil integrity port is
// accepted for Shape/approval and confined builds, but fails closed before an
// unconfined Build child starts.
func NewRunner(inner contract.ProcessRunner, policy Policy, integrity IntegritySnapshotter) *Runner {
	return &Runner{inner: inner, policy: policy.clone(), integrity: integrity}
}

// Run executes one child and returns the effective sandbox observation. When
// confinement is unavailable, a Build integrity snapshot brackets the child.
func (r *Runner) Run(ctx context.Context, spec contract.ProcessSpec) (Execution, error) {
	if r == nil || r.inner == nil {
		return Execution{}, errors.New("sandbox: process runner is required")
	}
	if ctx == nil {
		return Execution{}, errors.New("sandbox: context is required")
	}
	observation, wrap, err := r.mode(ctx, spec)
	if err != nil {
		return Execution{}, err
	}

	var before string
	if observation.Status != StatusConfined && r.policy.verifyIntegrity {
		if r.integrity == nil {
			return Execution{}, ErrIntegritySnapshotRequired
		}
		before, err = r.integrity.Snapshot(ctx)
		if err != nil {
			return Execution{}, fmt.Errorf("sandbox: read pre-child integrity snapshot: %w", err)
		}
	}

	child := spec
	var cleanup func() error
	if wrap {
		child, cleanup, err = darwin.Prepare(spec, darwin.Configuration{
			RunDir:             r.policy.runDir,
			Workspace:          r.policy.workspace,
			WritablePaths:      append([]string(nil), r.policy.writablePaths...),
			WriteDeniedPaths:   append([]string(nil), r.policy.writeDeniedPaths...),
			ProtectedReadPaths: append([]string(nil), r.policy.protectedReadPaths...),
		})
		if err != nil {
			return Execution{}, err
		}
	}
	processResult, processErr := r.inner.Run(ctx, child)
	var cleanupErr error
	if cleanup != nil {
		cleanupErr = cleanup()
	}

	var after string
	var snapshotErr error
	if observation.Status != StatusConfined && r.policy.verifyIntegrity && r.integrity != nil {
		after, snapshotErr = r.integrity.Snapshot(ctx)
		if snapshotErr == nil && before != after {
			snapshotErr = ErrIntegrityChanged
		}
	}
	if snapshotErr != nil {
		return Execution{}, errors.Join(snapshotErr, cleanupErr, processErr)
	}
	if cleanupErr != nil {
		return Execution{}, errors.Join(cleanupErr, processErr)
	}
	if processErr != nil {
		return Execution{}, processErr
	}
	if wrap && sandboxExecTargetMissing(processResult.OutputTail) {
		processResult.ExitStatus = intPointer(127)
		processResult.Unavailable = true
	}
	return Execution{Process: processResult, Observation: observation}, nil
}

func (r *Runner) mode(ctx context.Context, spec contract.ProcessSpec) (Observation, bool, error) {
	if !r.policy.enabled {
		return Observation{Status: StatusOff}, false, nil
	}
	if r.policy.alreadyConfined {
		return Observation{Status: StatusConfined}, false, nil
	}
	if r.policy.forcedUnavailable != "" {
		return Observation{Status: StatusUnconfined, WarningReason: r.policy.forcedUnavailable}, false, nil
	}

	r.probeMu.Lock()
	defer r.probeMu.Unlock()
	if !r.probed {
		availability, err := Probe(ctx, r.inner, spec, r.policy)
		if err != nil {
			if ctx.Err() != nil {
				return Observation{}, false, ctx.Err()
			}
			return Observation{}, false, fmt.Errorf("sandbox: enforcement probe: %w", err)
		}
		if ctx.Err() != nil {
			return Observation{}, false, ctx.Err()
		}
		r.available = availability
		r.probed = true
	}
	if !r.available.Available {
		reason := r.available.Reason
		if reason == "" {
			reason = "macOS confinement enforcement probe failed"
		}
		return Observation{Status: StatusUnconfined, WarningReason: reason}, false, nil
	}
	return Observation{Status: StatusConfined}, true, nil
}

func sandboxExecTargetMissing(output []byte) bool {
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "sandbox-exec: execvp() of '") && strings.HasSuffix(line, ": No such file or directory") {
			return true
		}
	}
	return false
}

func intPointer(value int) *int { return &value }
