package darwin

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/safefs"
)

const probeContents = "kogen-sandbox-probe\n"

type probeTarget struct {
	path  string
	label string
}

// Probe runs a child under a temporary Seatbelt profile. It proves writes in
// the workspace, run temp, system temp and each existing declared cache; it
// also proves that a protected read and an explicitly denied write fail.
func Probe(ctx context.Context, processes contract.ProcessRunner, template contract.ProcessSpec, configuration Configuration) (result ProbeResult, resultErr error) {
	if ctx == nil || processes == nil {
		return ProbeResult{}, errors.New("sandbox: probe context and process runner are required")
	}
	if !ToolAvailable() {
		return ProbeResult{Reason: "macOS sandbox-exec is unavailable"}, nil
	}
	runDir, workspace, err := validateConfiguration(configuration)
	if err != nil {
		return ProbeResult{}, err
	}
	configuration.RunDir = runDir
	configuration.Workspace = workspace
	root, err := openPrivateRunRoot(runDir)
	if err != nil {
		return ProbeResult{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()

	nonce, err := randomName("sandbox-probe", "")
	if err != nil {
		return ProbeResult{}, err
	}
	secretName := "tmp/" + nonce + "-secret"
	scriptName := "tmp/" + nonce + "-script.sh"
	deniedName := "tmp/" + nonce + "-denied"
	secretPath := filepath.Join(runDir, secretName)
	scriptPath := filepath.Join(runDir, scriptName)
	deniedPath := filepath.Join(runDir, deniedName)

	artifacts := []string{secretPath, scriptPath, deniedPath}
	var allowedTargets []probeTarget
	allowedTargets, err = probeTargets(configuration, nonce)
	if err != nil {
		return ProbeResult{}, err
	}
	for _, target := range allowedTargets {
		artifacts = append(artifacts, target.path)
	}
	defer func() {
		var cleanupErr error
		for _, path := range artifacts {
			cleanupErr = errors.Join(cleanupErr, removeAbsoluteFile(path))
		}
		resultErr = errors.Join(resultErr, cleanupErr)
	}()

	if err := root.Publish(secretName, []byte("probe-secret-must-not-be-readable\n"), 0o600, safefs.PublicationCreateOnly); err != nil {
		return ProbeResult{}, fmt.Errorf("sandbox: stage protected-read probe: %w", err)
	}
	profilePolicy := configuration
	profilePolicy.ProtectedReadPaths = append(append([]string(nil), configuration.ProtectedReadPaths...), secretPath)
	profilePolicy.WriteDeniedPaths = append(append([]string(nil), configuration.WriteDeniedPaths...), deniedPath)
	profilePolicy.WritablePaths = append(append([]string(nil), configuration.WritablePaths...), runTempPath(runDir))
	if err := root.Publish(scriptName, probeScript(allowedTargets, deniedPath, secretPath), 0o600, safefs.PublicationCreateOnly); err != nil {
		return ProbeResult{}, fmt.Errorf("sandbox: stage enforcement probe: %w", err)
	}

	probePolicy := profilePolicy
	probeSpec := template
	probeSpec.Executable = "/bin/sh"
	probeSpec.Args = []string{scriptPath}
	probeSpec.Dir = workspace
	probeSpec.Env = []string{"PATH=/usr/bin:/bin"}
	probeSpec.Stdin = nil
	probeSpec.Timeout = 3 * time.Second
	probeSpec.OutputLimit = 4096
	probeSpec.OutputTailLimit = 4096
	if template.LogPath == "" {
		return ProbeResult{}, errors.New("sandbox: probe requires the caller's private process log path")
	}
	probeLog, err := randomName("sandbox-probe", ".log")
	if err != nil {
		return ProbeResult{}, err
	}
	probeSpec.LogPath = filepath.Join(filepath.Dir(template.LogPath), probeLog)
	wrapped, cleanupProfile, err := Prepare(probeSpec, probePolicy)
	if err != nil {
		return ProbeResult{}, err
	}
	processResult, runErr := processes.Run(ctx, wrapped)
	cleanupErr := cleanupProfile()
	if ctx.Err() != nil {
		return ProbeResult{}, errors.Join(ctx.Err(), cleanupErr)
	}
	if runErr != nil {
		return ProbeResult{}, errors.Join(fmt.Errorf("sandbox: run enforcement probe: %w", runErr), cleanupErr)
	}
	if cleanupErr != nil {
		return ProbeResult{}, cleanupErr
	}
	if processResult.Unavailable || processResult.TimedOut || processResult.ExitStatus == nil || *processResult.ExitStatus != 0 {
		return ProbeResult{Reason: "macOS sandbox positive/negative enforcement probe failed"}, nil
	}

	checks := make([]string, 0, len(allowedTargets)+2)
	for _, target := range allowedTargets {
		if err := requireFileContents(target.path, []byte(probeContents)); err != nil {
			return ProbeResult{Reason: "macOS sandbox allowed-write probe failed"}, nil
		}
		checks = append(checks, target.label)
	}
	if _, err := root.Lstat(deniedName); err == nil {
		return ProbeResult{Reason: "macOS sandbox denied-write probe failed"}, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return ProbeResult{}, fmt.Errorf("sandbox: inspect denied-write probe: %w", err)
	}
	checks = append(checks, "denied-write")
	checks = append(checks, "denied-secret-read")
	sort.Strings(checks)
	return ProbeResult{Available: true, Checks: checks}, nil
}

func probeTargets(configuration Configuration, nonce string) ([]probeTarget, error) {
	requested := []probeTarget{
		{path: configuration.Workspace, label: "workspace-write"},
		{path: runTempPath(configuration.RunDir), label: "run-temp-write"},
		{path: "/tmp", label: "system-temp-write"},
	}
	for _, path := range configuration.WritablePaths {
		if path == configuration.Workspace || path == runTempPath(configuration.RunDir) || path == "/tmp" {
			continue
		}
		requested = append(requested, probeTarget{path: path, label: "declared-cache-write"})
	}
	seen := make(map[string]struct{}, len(requested))
	result := make([]probeTarget, 0, len(requested))
	for index, target := range requested {
		path, err := cleanPath(target.path)
		if err != nil {
			return nil, err
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			if index < 3 {
				return nil, fmt.Errorf("sandbox: required probe directory is unavailable")
			}
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() {
			if index < 3 {
				return nil, errors.New("sandbox: required probe path is not a directory")
			}
			continue
		}
		if _, ok := seen[resolved]; ok {
			continue
		}
		seen[resolved] = struct{}{}
		leaf := fmt.Sprintf("kogen-sandbox-probe-%s-%d", nonce, len(result))
		result = append(result, probeTarget{path: filepath.Join(resolved, leaf), label: target.label})
	}
	return result, nil
}

func probeScript(allowed []probeTarget, denied, secret string) []byte {
	var script strings.Builder
	script.WriteString("#!/bin/sh\nset -eu\n")
	for _, target := range allowed {
		fmt.Fprintf(&script, "printf '%s' > %s\n", probeContents, shellLiteral(target.path))
	}
	fmt.Fprintf(&script, "if printf denied > %s 2>/dev/null; then exit 72; fi\n", shellLiteral(denied))
	fmt.Fprintf(&script, "if /bin/cat %s >/dev/null 2>&1; then exit 73; fi\n", shellLiteral(secret))
	script.WriteString("exit 0\n")
	return []byte(script.String())
}

func shellLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func runTempPath(runDir string) string { return filepath.Join(runDir, "tmp") }

func requireFileContents(path string, want []byte) error {
	root, err := safefs.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	got, err := root.ReadFile(filepath.Base(path))
	if err != nil {
		return err
	}
	if string(got) != string(want) {
		return errors.New("sandbox: probe write had unexpected content")
	}
	return nil
}

func removeAbsoluteFile(path string) error {
	root, err := safefs.OpenRoot(filepath.Dir(path))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	removeErr := root.Remove(filepath.Base(path))
	closeErr := root.Close()
	if errors.Is(removeErr, fs.ErrNotExist) {
		removeErr = nil
	}
	return errors.Join(removeErr, closeErr)
}
