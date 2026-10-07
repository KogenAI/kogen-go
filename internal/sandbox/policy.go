// Package sandbox selects host confinement and checks source integrity when a
// project child has to run without an operating-system sandbox.
package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Status is the effective confinement mode of one child invocation.
type Status string

const (
	StatusConfined   Status = "confined"
	StatusUnconfined Status = "unconfined"
	StatusOff        Status = "off"
)

// Observation accompanies each process result so callers can journal the
// actual mode and print an unavailable warning once for their command.
type Observation struct {
	Status        Status
	WarningReason string
}

// Policy is immutable after construction by convention. Runner copies all
// slices so later caller mutation cannot change a child policy mid-run.
type Policy struct {
	enabled            bool
	alreadyConfined    bool
	forcedUnavailable  string
	verifyIntegrity    bool
	workspace          string
	runDir             string
	writablePaths      []string
	writeDeniedPaths   []string
	protectedReadPaths []string
}

// NewBuildPolicy creates the §5.3 policy for candidate-code commands. The
// candidate workspace is writable, while checkout and origin writes are
// denied. Build callers must supply an IntegritySnapshotter to NewRunner.
func NewBuildPolicy(enabled bool, workspace, checkout, origin, runDir string, host map[string]string) (Policy, error) {
	policy, err := newPolicy(enabled, workspace, runDir, host)
	if err != nil {
		return Policy{}, err
	}
	policy.verifyIntegrity = true
	for _, path := range []string{checkout, origin} {
		if path == "" {
			continue
		}
		path, err = cleanAbsolutePath(path)
		if err != nil {
			return Policy{}, fmt.Errorf("sandbox: invalid protected write path: %w", err)
		}
		policy.writeDeniedPaths = append(policy.writeDeniedPaths, path)
	}
	return policy, nil
}

// NewCheckoutPolicy creates the policy used by shaping and approval, which
// run in the checkout and therefore need checkout write access.
func NewCheckoutPolicy(enabled bool, checkout, runDir string, host map[string]string) (Policy, error) {
	return newPolicy(enabled, checkout, runDir, host)
}

func newPolicy(enabled bool, workspace, runDir string, host map[string]string) (Policy, error) {
	workspace, err := cleanAbsolutePath(workspace)
	if err != nil {
		return Policy{}, fmt.Errorf("sandbox: invalid workspace: %w", err)
	}
	if workspace == string(filepath.Separator) {
		return Policy{}, errors.New("sandbox: workspace cannot be the filesystem root")
	}
	runDir, err = cleanAbsolutePath(runDir)
	if err != nil {
		return Policy{}, fmt.Errorf("sandbox: invalid run directory: %w", err)
	}
	if runDir == string(filepath.Separator) {
		return Policy{}, errors.New("sandbox: run directory cannot be the filesystem root")
	}

	policy := Policy{
		enabled:   enabled,
		workspace: workspace,
		runDir:    runDir,
		writablePaths: []string{
			workspace,
			filepath.Join(runDir, "logs"),
			filepath.Join(runDir, "tmp"),
			filepath.Join(runDir, "reports"),
			filepath.Join(runDir, "mise-state"),
			filepath.Join(runDir, "mise-cache"),
			"/tmp",
		},
	}
	if host == nil {
		host = map[string]string{}
	}
	if host["KOGEN_SANDBOXED"] == "1" {
		policy.alreadyConfined = true
	}
	if host["KOGEN_SANDBOX"] == "unavailable" {
		policy.forcedUnavailable = "forced by KOGEN_SANDBOX=unavailable"
	}
	if home := host["HOME"]; home != "" {
		home, err = cleanAbsolutePath(home)
		if err != nil {
			return Policy{}, errors.New("sandbox: HOME must be an absolute path")
		}
		policy.writablePaths = append(policy.writablePaths,
			filepath.Join(home, ".cache/mise"),
			filepath.Join(home, ".hex"),
			filepath.Join(home, ".cache/rebar3"),
			filepath.Join(home, ".npm"),
			filepath.Join(home, ".cargo/registry"),
			filepath.Join(home, ".cargo/git"),
			filepath.Join(home, ".cache/go-build"),
		)
		policy.protectedReadPaths = append(policy.protectedReadPaths,
			filepath.Join(home, ".kogen/credentials"),
			filepath.Join(home, ".ssh"),
			filepath.Join(home, ".gnupg"),
			filepath.Join(home, ".codex"),
		)
		credentialsDir := filepath.Join(home, ".kogen")
		if entries, readErr := os.ReadDir(credentialsDir); readErr == nil {
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "credentials") {
					policy.protectedReadPaths = append(policy.protectedReadPaths, filepath.Join(credentialsDir, entry.Name()))
				}
			}
		}
	}
	if authPath := host["KOGEN_AUTH_PATH"]; authPath != "" {
		authPath, err = cleanAbsolutePath(authPath)
		if err != nil {
			return Policy{}, errors.New("sandbox: KOGEN_AUTH_PATH must be an absolute path")
		}
		policy.protectedReadPaths = append(policy.protectedReadPaths, authPath)
	}
	if moduleCache := host["GOMODCACHE"]; moduleCache != "" && filepath.IsAbs(moduleCache) {
		if moduleCache, err = cleanAbsolutePath(moduleCache); err != nil {
			return Policy{}, fmt.Errorf("sandbox: invalid GOMODCACHE: %w", err)
		}
		policy.writablePaths = append(policy.writablePaths, moduleCache)
	}
	return policy, nil
}

// WithWritablePath adds a declared writable location, such as an adapter
// cache, without exposing the internal slice.
func (p Policy) WithWritablePath(path string) (Policy, error) {
	path, err := cleanAbsolutePath(path)
	if err != nil {
		return Policy{}, err
	}
	if path == string(filepath.Separator) {
		return Policy{}, errors.New("sandbox: writable path cannot be the filesystem root")
	}
	p.writablePaths = append(append([]string(nil), p.writablePaths...), path)
	return p, nil
}

// WithWriteDeniedPath adds a path that must remain unwritable by the child.
func (p Policy) WithWriteDeniedPath(path string) (Policy, error) {
	path, err := cleanAbsolutePath(path)
	if err != nil {
		return Policy{}, err
	}
	p.writeDeniedPaths = append(append([]string(nil), p.writeDeniedPaths...), path)
	return p, nil
}

// WithProtectedReadPath adds a credential or other protected read location.
func (p Policy) WithProtectedReadPath(path string) (Policy, error) {
	path, err := cleanAbsolutePath(path)
	if err != nil {
		return Policy{}, err
	}
	p.protectedReadPaths = append(append([]string(nil), p.protectedReadPaths...), path)
	return p, nil
}

// WithIntegrityCheck controls the source integrity guard. Build policies
// enable it by default; the option is useful for focused component fixtures.
func (p Policy) WithIntegrityCheck(required bool) Policy {
	p.verifyIntegrity = required
	return p
}

func cleanAbsolutePath(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, '\x00') {
		return "", errors.New("path must be a clean absolute path")
	}
	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("path contains a control character")
		}
	}
	return path, nil
}

func (p Policy) clone() Policy {
	p.writablePaths = append([]string(nil), p.writablePaths...)
	p.writeDeniedPaths = append([]string(nil), p.writeDeniedPaths...)
	p.protectedReadPaths = append([]string(nil), p.protectedReadPaths...)
	return p
}
