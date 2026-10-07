package linux

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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

func probeEnforcement(ctx context.Context, processes contract.ProcessRunner, template contract.ProcessSpec, config validatedConfiguration) (result ProbeResult, resultErr error) {
	runRoot, err := safefs.OpenRoot(config.runDir)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("sandbox: open rooted run directory: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, runRoot.Close()) }()

	nonce, err := randomName("sandbox-probe")
	if err != nil {
		return ProbeResult{}, err
	}
	secretName := "tmp/" + nonce + "-secret"
	scriptName := "tmp/" + nonce + "-script.sh"
	deniedName := "tmp/" + nonce + "-denied"
	secretPath := filepath.Join(config.runDir, secretName)
	scriptPath := filepath.Join(config.runDir, scriptName)
	deniedPath := filepath.Join(config.runDir, deniedName)
	secretContents := "probe-secret-" + nonce
	deniedContents := []byte("probe-denied-file-must-remain-unchanged\n")

	allowedTargets, err := probeTargets(config, nonce)
	if err != nil {
		return ProbeResult{}, err
	}
	artifacts := []string{secretPath, scriptPath, deniedPath}
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

	if err := runRoot.Publish(secretName, []byte(secretContents+"\n"), 0o600, safefs.PublicationCreateOnly); err != nil {
		return ProbeResult{}, fmt.Errorf("sandbox: stage protected-read probe: %w", err)
	}
	if err := runRoot.Publish(deniedName, deniedContents, 0o600, safefs.PublicationCreateOnly); err != nil {
		return ProbeResult{}, fmt.Errorf("sandbox: stage denied-write probe: %w", err)
	}
	if err := runRoot.Publish(scriptName, probeScript(allowedTargets, deniedPath, secretPath, secretContents, config), 0o600, safefs.PublicationCreateOnly); err != nil {
		return ProbeResult{}, fmt.Errorf("sandbox: stage enforcement probe: %w", err)
	}

	probePolicy := config
	probePolicy.writablePaths = append(append([]string(nil), config.writablePaths...), filepath.Join(config.runDir, "tmp"))
	probePolicy.writeDeniedPaths = append(append([]string(nil), config.writeDeniedPaths...), deniedPath)
	probePolicy.protectedReadPaths = append(append([]string(nil), config.protectedReadPaths...), secretPath)
	probeSpec := template
	probeSpec.Executable = "/bin/sh"
	probeSpec.Args = []string{scriptPath}
	probeSpec.Dir = config.workspace
	probeSpec.Env = []string{"PATH=/usr/bin:/bin"}
	probeSpec.Stdin = nil
	probeSpec.Timeout = 5 * time.Second
	probeSpec.OutputLimit = 4096
	probeSpec.OutputTailLimit = 4096

	wrapped, err := prepareWithTool(probeSpec, probePolicy, BubblewrapPath)
	if err != nil {
		return ProbeResult{}, err
	}
	processResult, err := runSupervised(ctx, processes, wrapped, config, wrapped.Executable, wrapped.Args, wrapped.Stdin)
	if err != nil {
		return ProbeResult{}, errors.Join(fmt.Errorf("sandbox: run namespace enforcement probe: %w", err), cleanupFiles(artifacts))
	}
	if !successful(processResult) {
		return ProbeResult{Reason: UnavailableReason}, nil
	}

	for _, target := range allowedTargets {
		if err := requireFileContents(target.path, []byte(probeContents)); err != nil {
			return ProbeResult{Reason: UnavailableReason}, nil
		}
	}
	contents, err := runRoot.ReadFile(deniedName)
	if err != nil || string(contents) != string(deniedContents) {
		return ProbeResult{Reason: UnavailableReason}, nil
	}
	if bytes.Contains(processResult.OutputTail, []byte(secretContents)) {
		return ProbeResult{Reason: UnavailableReason}, nil
	}

	checks := make([]string, 0, len(allowedTargets)+7)
	for _, target := range allowedTargets {
		checks = append(checks, target.label)
	}
	checks = append(checks, "denied-write", "denied-secret-read", "mount-namespace", "pid-namespace", "ipc-namespace", "uts-namespace", "network-shared", "capabilities-dropped")
	sort.Strings(checks)
	return ProbeResult{Available: true, Checks: checks}, nil
}

func runSupervised(ctx context.Context, processes contract.ProcessRunner, template contract.ProcessSpec, config validatedConfiguration, executable string, args []string, stdin []byte) (contract.ProcessResult, error) {
	name, err := randomName("sandbox-probe-log")
	if err != nil {
		return contract.ProcessResult{}, err
	}
	logPath := filepath.Join(config.runDir, "logs", name+".log")
	logSpec := template
	logSpec.Executable = executable
	logSpec.Args = append([]string(nil), args...)
	logSpec.Dir = config.workspace
	logSpec.Env = []string{"PATH=/usr/bin:/bin"}
	logSpec.Stdin = append([]byte(nil), stdin...)
	logSpec.LogPath = logPath
	if logSpec.Timeout == 0 {
		logSpec.Timeout = 5 * time.Second
	}
	if logSpec.OutputLimit == 0 {
		logSpec.OutputLimit = 4096
	}
	if logSpec.OutputTailLimit == 0 {
		logSpec.OutputTailLimit = 4096
	}
	result, runErr := processes.Run(ctx, logSpec)
	removeErr := removeAbsoluteFile(logPath)
	return result, errors.Join(runErr, removeErr)
}

func probeTargets(config validatedConfiguration, nonce string) ([]probeTarget, error) {
	requested := []probeTarget{
		{path: config.workspace, label: "workspace-write"},
		{path: filepath.Join(config.runDir, "tmp"), label: "run-temp-write"},
		{path: "/tmp", label: "system-temp-write"},
	}
	for _, path := range config.writablePaths {
		if path == config.workspace || path == filepath.Join(config.runDir, "tmp") || path == "/tmp" {
			continue
		}
		requested = append(requested, probeTarget{path: path, label: "declared-cache-write"})
	}
	seen := make(map[string]struct{}, len(requested))
	result := make([]probeTarget, 0, len(requested))
	for index, target := range requested {
		path, exists, err := resolveExistingPath(target.path)
		if err != nil {
			return nil, fmt.Errorf("sandbox: resolve probe directory: %w", err)
		}
		if !exists {
			if index < 3 {
				return nil, errors.New("sandbox: required probe directory is unavailable")
			}
			continue
		}
		if err := requireDirectory(path); err != nil {
			if index < 3 {
				return nil, errors.New("sandbox: required probe path is not a directory")
			}
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		result = append(result, probeTarget{path: filepath.Join(path, "kogen-"+nonce+"-"+fmt.Sprint(len(result))), label: target.label})
	}
	return result, nil
}

func probeScript(allowed []probeTarget, denied, secret, secretContents string, _ validatedConfiguration) []byte {
	var script strings.Builder
	script.WriteString("#!/bin/sh\nset -eu\n")
	for _, target := range allowed {
		fmt.Fprintf(&script, "printf '%s' > %s\n", probeContents, shellLiteral(target.path))
	}
	fmt.Fprintf(&script, "if printf denied > %s 2>/dev/null; then exit 72; fi\n", shellLiteral(denied))
	fmt.Fprintf(&script, "if IFS= read -r value < %s 2>/dev/null; then [ \"$value\" != %s ] || exit 73; fi\n", shellLiteral(secret), shellLiteral(secretContents))
	for _, namespace := range []string{"mnt", "pid", "ipc", "uts"} {
		hostID, err := os.Readlink("/proc/self/ns/" + namespace)
		if err != nil {
			fmt.Fprintf(&script, "exit 74 # %s namespace identity unavailable\n", namespace)
			continue
		}
		fmt.Fprintf(&script, "[ \"$(/usr/bin/readlink /proc/self/ns/%s)\" != %s ] || exit 74\n", namespace, shellLiteral(hostID))
	}
	if networkID, err := os.Readlink("/proc/self/ns/net"); err == nil {
		fmt.Fprintf(&script, "[ \"$(/usr/bin/readlink /proc/self/ns/net)\" = %s ] || exit 75\n", shellLiteral(networkID))
	} else {
		script.WriteString("exit 75 # network namespace identity unavailable\n")
	}
	script.WriteString("cap_eff=$(sed -n 's/^CapEff:[[:space:]]*//p' /proc/self/status)\ncase \"$cap_eff\" in ''|*[!0]*) exit 76;; esac\nexit 0\n")
	return []byte(script.String())
}

func shellLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

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
	if path == "" {
		return nil
	}
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

func cleanupFiles(paths []string) error {
	var err error
	for _, path := range paths {
		err = errors.Join(err, removeAbsoluteFile(path))
	}
	return err
}

func randomName(prefix string) (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", fmt.Errorf("sandbox: generate private name: %w", err)
	}
	return prefix + "-" + hex.EncodeToString(data[:]), nil
}

func isProbeLogPath(path, runDir string) bool {
	rel, err := filepath.Rel(runDir, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return filepath.Clean(filepath.Dir(rel)) == "logs"
}
