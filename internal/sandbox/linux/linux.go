// Package linux prepares and probes the pinned Linux bubblewrap sandbox.
//
// The package deliberately depends only on the process effect port. Callers
// must run both probe and prepared child specifications through the same
// supervisor used for every other project process.
package linux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"kogen-go/internal/contract"
)

const (
	// BubblewrapPath is fixed by the Linux host provisioning contract.
	BubblewrapPath = "/usr/bin/bwrap"
	// BubblewrapVersion is the only version admitted by this component.
	BubblewrapVersion = "0.13.0"
	// UnavailableReason is the stable fallback reason used when Linux
	// confinement cannot be established. The shared sandbox runner reports
	// this reason and brackets unconfined Builds with integrity snapshots.
	UnavailableReason = "no supported confinement tool is available on this host"
)

var ErrToolUnavailable = errors.New("sandbox: pinned Linux bubblewrap is unavailable")

// ProbeResult reports stable policy checks only. It never contains paths,
// environment values, credentials, or probe data.
type ProbeResult struct {
	Available bool
	Reason    string
	Checks    []string
}

// ToolAvailable checks the fixed executable's file properties. Probe also
// checks its version and its ability to create the required namespaces.
func ToolAvailable() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	return inspectTool(BubblewrapPath) == nil
}

// Probe checks the pinned executable through the caller's process supervisor,
// then runs positive and negative namespace and filesystem probes under the
// exact policy Prepare uses for project children.
func Probe(ctx context.Context, processes contract.ProcessRunner, template contract.ProcessSpec, configuration Configuration) (ProbeResult, error) {
	if ctx == nil {
		return ProbeResult{}, errors.New("sandbox: probe context is required")
	}
	if processes == nil {
		return ProbeResult{}, errors.New("sandbox: process runner is required")
	}
	if runtime.GOOS != "linux" || inspectTool(BubblewrapPath) != nil {
		return ProbeResult{Reason: UnavailableReason}, nil
	}
	validated, err := configuration.validate()
	if err != nil {
		return ProbeResult{}, err
	}
	if template.LogPath == "" || !isProbeLogPath(template.LogPath, validated.runDir) {
		return ProbeResult{}, errors.New("sandbox: probe requires the caller's private process log path")
	}

	version, err := runSupervised(ctx, processes, template, validated, BubblewrapPath, []string{"--version"}, nil)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("sandbox: inspect pinned bubblewrap version: %w", err)
	}
	if !successful(version) || !matchesPinnedVersion(version.OutputTail) {
		return ProbeResult{Reason: UnavailableReason}, nil
	}

	result, err := probeEnforcement(ctx, processes, template, validated)
	if err != nil {
		return ProbeResult{}, err
	}
	if !result.Available {
		return ProbeResult{Reason: UnavailableReason}, nil
	}
	return result, nil
}

// Prepare wraps spec in the pinned bubblewrap policy. It does not start a
// process; callers must pass the returned spec to their process supervisor.
func Prepare(spec contract.ProcessSpec, configuration Configuration) (contract.ProcessSpec, error) {
	if runtime.GOOS != "linux" || inspectTool(BubblewrapPath) != nil {
		return contract.ProcessSpec{}, ErrToolUnavailable
	}
	validated, err := configuration.validate()
	if err != nil {
		return contract.ProcessSpec{}, err
	}
	return prepareWithTool(spec, validated, BubblewrapPath)
}

func successful(result contract.ProcessResult) bool {
	return !result.Unavailable && !result.TimedOut && result.ExitStatus != nil && *result.ExitStatus == 0
}

func matchesPinnedVersion(output []byte) bool {
	return strings.TrimSpace(string(output)) == "bubblewrap "+BubblewrapVersion
}

func inspectTool(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	mode := info.Mode()
	if !mode.IsRegular() || mode&os.ModeSymlink != 0 || mode.Perm()&0o111 == 0 || mode.Perm()&0o022 != 0 || mode&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return errors.New("sandbox: bubblewrap executable has unsafe type or permissions")
	}
	return nil
}
