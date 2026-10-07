package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/safefs"
)

// TreeSnapshotter captures the exact candidate tree for a workspace. The base
// implementation below delegates eligibility and Git ignore semantics to
// gitio, which binds every snapshot to immutable base metadata.
type TreeSnapshotter interface {
	Snapshot(context.Context, string) (string, error)
}

// CandidateTree snapshots one workspace relative to the immutable tree used to
// create its Git base metadata. It never consults the workspace index or HEAD.
type CandidateTree struct {
	Git    contract.GitPort
	Policy contract.GitPolicy
	Base   *gitio.BaseMetadata
}

func (t CandidateTree) Snapshot(ctx context.Context, workdir string) (string, error) {
	if t.Git == nil || t.Base == nil {
		return "", errors.New("acceptance: Git port and immutable base metadata are required")
	}
	policy := t.Policy
	policy.WorkingDirectory = workdir
	tree, err := gitio.SnapshotCandidateTree(ctx, t.Git, policy, t.Base)
	if err != nil {
		return "", err
	}
	return string(tree), nil
}

// Adapter supervises a configured command check and reports its actual process
// status and before/after candidate tree. A nonzero child status is represented
// in CheckResult; only failures to observe or supervise the command are errors.
// RunDirectory is the private run root where bounded process logs are stored.
type Adapter struct {
	Processes contract.ProcessRunner
	Trees     TreeSnapshotter
	Roots     contract.RootOpener
	RunDir    string
}

var _ contract.AcceptanceAdapter = (*Adapter)(nil)

// Run performs one configured check in workdir. Env is a complete child
// environment; no host environment is inherited implicitly.
func (a *Adapter) Run(ctx context.Context, workdir string, spec contract.CheckSpec) (contract.CheckResult, error) {
	if ctx == nil || a == nil || a.Processes == nil || a.Trees == nil {
		return contract.CheckResult{}, errors.New("acceptance: context, process runner, and tree snapshotter are required")
	}
	if spec.Name == "" || spec.Program == "" {
		return contract.CheckResult{}, errors.New("acceptance: check name and program are required")
	}
	runDir, root, err := a.openRunRoot()
	if err != nil {
		return contract.CheckResult{}, err
	}
	defer closeRoot(root)
	if err := root.MkdirAll("logs", 0o700); err != nil {
		return contract.CheckResult{}, fmt.Errorf("acceptance: prepare log directory: %w", err)
	}
	before, err := a.Trees.Snapshot(ctx, workdir)
	if err != nil {
		return contract.CheckResult{}, fmt.Errorf("acceptance: snapshot tree before check: %w", err)
	}
	nameDigest := sha256.Sum256([]byte(spec.Name))
	logPath := filepath.Join(runDir, "logs", "check-"+hex.EncodeToString(nameDigest[:8])+".log")
	processResult, runErr := a.Processes.Run(ctx, contract.ProcessSpec{
		Executable:      spec.Program,
		Args:            append([]string(nil), spec.Args...),
		Dir:             workdir,
		Env:             append([]string(nil), spec.Env...),
		Timeout:         spec.Timeout,
		OutputLimit:     0,
		OutputTailLimit: 0,
		LogPath:         logPath,
	})
	after, afterErr := a.Trees.Snapshot(ctx, workdir)
	if afterErr != nil {
		return contract.CheckResult{}, errors.Join(runErr, fmt.Errorf("acceptance: snapshot tree after check: %w", afterErr))
	}
	result := contract.CheckResult{
		Name:        spec.Name,
		ExitStatus:  processResult.ExitStatus,
		TimedOut:    processResult.TimedOut,
		Unavailable: processResult.Unavailable || processResult.ExitStatus == nil || exitUnavailable(processResult.ExitStatus),
		OutputTail:  append([]byte(nil), processResult.OutputTail...),
		TreeBefore:  before,
		TreeAfter:   after,
	}
	switch {
	case before != after:
		result.Status = contract.CheckMutating
	case processResult.TimedOut:
		result.Status = contract.CheckTimeout
	case result.Unavailable:
		result.Status = contract.CheckUnavailable
	case processResult.ExitStatus != nil && *processResult.ExitStatus == 0:
		result.Status = contract.CheckGreen
	default:
		result.Status = contract.CheckRed
	}
	return result, runErr
}

func (a *Adapter) openRunRoot() (string, contract.RootedFS, error) {
	if a.RunDir == "" || !filepath.IsAbs(a.RunDir) || filepath.Clean(a.RunDir) != a.RunDir {
		return "", nil, errors.New("acceptance: run directory must be a clean absolute path")
	}
	roots := a.Roots
	if roots == nil {
		roots = safefs.Opener{}
	}
	root, err := roots.OpenRoot(a.RunDir)
	if err != nil {
		return "", nil, fmt.Errorf("acceptance: open run directory: %w", err)
	}
	return a.RunDir, root, nil
}

func exitUnavailable(status *int) bool { return status != nil && (*status == 126 || *status == 127) }

func closeRoot(root contract.RootedFS) {
	if closer, ok := root.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}

// SortedEnvironment converts an environment map into the deterministic,
// explicit KEY=VALUE list required by ProcessRunner.
func SortedEnvironment(environment map[string]string) ([]string, error) {
	keys := make([]string, 0, len(environment))
	for key, value := range environment {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("acceptance: invalid environment entry %q", key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]string, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, key+"="+environment[key])
	}
	return entries, nil
}

// EnsurePrivateRunDirectories creates the private directories shared by check
// and command adapters through the same rooted filesystem capability.
func EnsurePrivateRunDirectories(root contract.RootedFS) error {
	if root == nil {
		return errors.New("acceptance: run root is required")
	}
	for _, name := range []string{"logs", "reports"} {
		if err := root.MkdirAll(name, 0o700); err != nil {
			return fmt.Errorf("acceptance: prepare %s directory: %w", name, err)
		}
		info, err := root.Lstat(name)
		if err != nil {
			return fmt.Errorf("acceptance: inspect %s directory: %w", name, err)
		}
		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("acceptance: %s directory must be private and real", name)
		}
	}
	return nil
}

// EnsurePrivateDirectoryPath creates each report-parent segment beneath the
// run root and rejects symlink or broadly accessible directory leaves.
func EnsurePrivateDirectoryPath(root contract.RootedFS, relative string) error {
	if root == nil || relative == "" || !fs.ValidPath(relative) || strings.Contains(relative, "\\") {
		return errors.New("acceptance: report parent must be a rooted relative path")
	}
	current := ""
	for _, part := range strings.Split(relative, "/") {
		if current == "" {
			current = part
		} else {
			current += "/" + part
		}
		if err := root.MkdirAll(current, 0o700); err != nil {
			return fmt.Errorf("acceptance: create report directory: %w", err)
		}
		info, err := root.Lstat(current)
		if err != nil {
			return fmt.Errorf("acceptance: inspect report directory: %w", err)
		}
		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("acceptance: report directory %q must be private and real", current)
		}
	}
	return nil
}

// NormalizeTimeout returns the configured timeout or the 600 s acceptance
// timeout default from project configuration.
func NormalizeTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return 600 * time.Second
	}
	return timeout
}
