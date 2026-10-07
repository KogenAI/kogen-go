// Package gitio is the supervised boundary for controller-owned Git effects.
package gitio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/process"
)

const (
	gitStdoutFileEnv = "KOGEN_INTERNAL_GIT_STDOUT_FILE"
	gitStderrFileEnv = "KOGEN_INTERNAL_GIT_STDERR_FILE"
	gitFileLimitEnv  = "KOGEN_INTERNAL_GIT_FILE_LIMIT_BLOCKS"
	gitShellScript   = `ulimit -f "$KOGEN_INTERNAL_GIT_FILE_LIMIT_BLOCKS" || exit 126; /bin/cat 2>/dev/null | exec "$@" >"$KOGEN_INTERNAL_GIT_STDOUT_FILE" 2>"$KOGEN_INTERNAL_GIT_STDERR_FILE"`
)

type errorKind string

// GitErrorKind identifies the boundary at which a Git operation failed.
type GitErrorKind = errorKind

const (
	ErrorInvalid     errorKind = "invalid"
	ErrorUnavailable errorKind = "unavailable"
	ErrorTimeout     errorKind = "timeout"
	ErrorProcess     errorKind = "process"
	ErrorExit        errorKind = "exit"
	ErrorOutputLimit errorKind = "output_limit"
	ErrorRefConflict errorKind = "ref_conflict"
)

const (
	GitErrorInvalid     GitErrorKind = ErrorInvalid
	GitErrorUnavailable GitErrorKind = ErrorUnavailable
	GitErrorTimeout     GitErrorKind = ErrorTimeout
	GitErrorProcess     GitErrorKind = ErrorProcess
	GitErrorExit        GitErrorKind = ErrorExit
	GitErrorOutputLimit GitErrorKind = ErrorOutputLimit
	GitErrorRefConflict GitErrorKind = ErrorRefConflict
)

// ErrRefConflict identifies a failed compare-and-swap update.
var ErrRefConflict = errors.New("git ref compare-and-swap conflict")

// GitError retains a typed operation failure and bounded diagnostics.
type GitError struct {
	Kind        GitErrorKind
	Operation   string
	ExitStatus  *int
	TimedOut    bool
	Unavailable bool
	Detail      string
	Cause       error
}

func (e *GitError) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := "git"
	if e.Operation != "" {
		message += " " + e.Operation
	}
	if e.Detail != "" {
		message += ": " + e.Detail
	} else {
		message += ": " + string(e.Kind)
	}
	if e.Cause != nil && e.Detail == "" {
		message += ": " + e.Cause.Error()
	}
	return message
}

func (e *GitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *GitError) Is(target error) bool {
	return target == ErrRefConflict && e != nil && e.Kind == ErrorRefConflict
}

type operationMode uint8

const (
	controlledWorkspace operationMode = iota + 1
	userOrigin
)

// Runner implements contract.GitPort. Its operation mode is fixed at
// construction: workspace calls suppress all inherited config and helpers;
// origin calls retain user identity/signing and pin helper choices from the
// user's global config. Git itself always runs under the process supervisor.
type Runner struct {
	processRunner contract.ProcessRunner
	mode          operationMode
	configMu      sync.Mutex
	configCache   map[string][]configEntry
}

var _ contract.GitPort = (*Runner)(nil)

// New returns a runner with the controlled-workspace policy.
func New(processRunner contract.ProcessRunner) *Runner {
	return NewWorkspace(processRunner)
}

// NewWorkspace returns a runner whose immutable policy is safe for controlled
// workspace operations.
func NewWorkspace(processRunner contract.ProcessRunner) *Runner {
	return newRunner(processRunner, controlledWorkspace)
}

// NewOrigin returns a runner whose immutable policy preserves the user's
// global identity and signing setup for landing operations.
func NewOrigin(processRunner contract.ProcessRunner) *Runner {
	return newRunner(processRunner, userOrigin)
}

func newRunner(processRunner contract.ProcessRunner, mode operationMode) *Runner {
	if processRunner == nil {
		processRunner = process.Supervisor{}
	}
	return &Runner{
		processRunner: processRunner,
		mode:          mode,
		configCache:   make(map[string][]configEntry),
	}
}

// Exec sends one argument-vector Git invocation through the configured
// supervisor. Non-zero Git exit status is an observation, not an error here;
// operation-specific helpers decide whether a status is acceptable.
func (r *Runner) Exec(ctx context.Context, args []string, stdin []byte, policy contract.GitPolicy) (contract.GitResult, error) {
	if r == nil || r.processRunner == nil {
		return contract.GitResult{}, &GitError{Kind: ErrorInvalid, Operation: operationName(args), Detail: "process runner is required"}
	}
	if err := validateInvocation(ctx, args, policy, r.mode); err != nil {
		return contract.GitResult{}, err
	}
	base, err := parseEnvironment(policy.Environment)
	if err != nil {
		return contract.GitResult{}, &GitError{Kind: ErrorInvalid, Operation: operationName(args), Detail: err.Error(), Cause: err}
	}
	prepared := prepareEnvironment(base, r.mode)
	localOverrides, err := r.localOperationOverrides(ctx, policy, prepared)
	if err != nil {
		return contract.GitResult{}, err
	}
	prepared = addConfigEnvironment(prepared, localOverrides)
	if r.mode == userOrigin {
		entries, err := r.globalConfigOverrides(ctx, policy, prepared)
		if err != nil {
			return contract.GitResult{}, err
		}
		prepared = addConfigEnvironment(prepared, append(localOverrides, entries...))
		args = withGlobalSigning(args, entries)
	}
	return r.run(ctx, args, stdin, policy, prepared, true)
}

func (r *Runner) localOperationOverrides(ctx context.Context, policy contract.GitPolicy, env map[string]string) ([]configEntry, error) {
	result, err := r.run(ctx, []string{"config", "--local", "--null", "--name-only", "--get-regexp", `^(filter\.|diff\.)`}, nil, policy, env, false)
	if err != nil {
		return nil, err
	}
	if result.Process.ExitStatus == nil {
		return nil, &GitError{Kind: ErrorProcess, Operation: "config --local", Detail: "supervisor returned no exit status"}
	}
	status := *result.Process.ExitStatus
	if status == 1 {
		return nil, nil
	}
	if status != 0 {
		detail := strings.ToLower(string(result.StderrTail))
		if status == 128 && (strings.Contains(detail, "not in a git directory") || strings.Contains(detail, "not a git repository")) {
			return nil, nil
		}
		return nil, exitError("config --local", result)
	}
	keys, err := nulValues(result.Stdout)
	if err != nil {
		return nil, &GitError{Kind: ErrorProcess, Operation: "config --local", Detail: "local helper config returned malformed NUL-delimited data", Cause: err}
	}
	overrides := make(map[string]string)
	for _, rawKey := range keys {
		key := strings.ToLower(rawKey)
		switch {
		case strings.HasPrefix(key, "filter."):
			if strings.HasSuffix(key, ".required") {
				overrides[key] = "false"
			} else if strings.HasSuffix(key, ".clean") || strings.HasSuffix(key, ".smudge") || strings.HasSuffix(key, ".process") {
				overrides[key] = ""
			}
		case key == "diff.external":
			overrides[key] = ""
		case strings.HasPrefix(key, "diff.") && (strings.HasSuffix(key, ".textconv") || strings.HasSuffix(key, ".command")):
			overrides[key] = ""
		}
	}
	keys = keys[:0]
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]configEntry, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, configEntry{key: key, value: overrides[key]})
	}
	return entries, nil
}

func (r *Runner) run(ctx context.Context, args []string, stdin []byte, policy contract.GitPolicy, env map[string]string, keepLog bool) (contract.GitResult, error) {
	operation := operationName(args)
	gitPath, err := executableFromPath("git", env["PATH"], policy.WorkingDirectory)
	if err != nil {
		return contract.GitResult{}, &GitError{Kind: ErrorUnavailable, Operation: operation, Unavailable: true, Detail: "git executable is unavailable", Cause: err}
	}
	processResult, stdout, stderr, runErr := r.runSupervised(ctx, gitPath, args, stdin, policy, env, keepLog)
	result := contract.GitResult{Process: processResult, Stdout: stdout, StderrTail: stderr}
	if runErr != nil {
		kind := ErrorProcess
		if processResult.TimedOut || errors.Is(runErr, context.DeadlineExceeded) {
			kind = ErrorTimeout
		}
		return result, &GitError{
			Kind:       kind,
			Operation:  operation,
			TimedOut:   kind == ErrorTimeout,
			ExitStatus: processResult.ExitStatus,
			Detail:     strings.TrimSpace(string(stderr)),
			Cause:      runErr,
		}
	}
	if processResult.TimedOut {
		return result, &GitError{Kind: ErrorTimeout, Operation: operation, TimedOut: true, ExitStatus: processResult.ExitStatus, Detail: strings.TrimSpace(string(stderr))}
	}
	if processResult.Unavailable {
		return result, &GitError{Kind: ErrorUnavailable, Operation: operation, Unavailable: true, ExitStatus: processResult.ExitStatus, Detail: strings.TrimSpace(string(stderr))}
	}
	if processResult.ExitStatus == nil {
		return result, &GitError{Kind: ErrorProcess, Operation: operation, Detail: "supervisor returned no exit status"}
	}
	if int64(len(stdout)) > policyLimit(policy.StdoutLimit, GitOutputLimit) {
		return result, &GitError{Kind: ErrorOutputLimit, Operation: operation, ExitStatus: processResult.ExitStatus, Detail: "stdout exceeded the configured capture limit"}
	}
	if *processResult.ExitStatus == 0 {
		discardPrivateOutputLog(&result.Process)
	}
	return result, nil
}

func (r *Runner) runSupervised(ctx context.Context, gitPath string, args []string, stdin []byte, policy contract.GitPolicy, env map[string]string, keepLog bool) (contract.ProcessResult, []byte, []byte, error) {
	logDir, err := os.MkdirTemp("", "kogen-git-")
	if err != nil {
		return contract.ProcessResult{}, nil, nil, &GitError{Kind: ErrorProcess, Operation: operationName(args), Detail: "create private Git log directory", Cause: err}
	}
	if err := os.Chmod(logDir, 0o700); err != nil {
		_ = os.RemoveAll(logDir)
		return contract.ProcessResult{}, nil, nil, &GitError{Kind: ErrorProcess, Operation: operationName(args), Detail: "protect private Git log directory", Cause: err}
	}
	stdoutPath := filepath.Join(logDir, "git.stdout")
	stderrPath := filepath.Join(logDir, "git.stderr")
	for _, path := range []string{stdoutPath, stderrPath} {
		file, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if createErr != nil {
			_ = os.RemoveAll(logDir)
			return contract.ProcessResult{}, nil, nil, &GitError{Kind: ErrorProcess, Operation: operationName(args), Detail: "create private Git output file", Cause: createErr}
		}
		if closeErr := file.Close(); closeErr != nil {
			_ = os.RemoveAll(logDir)
			return contract.ProcessResult{}, nil, nil, &GitError{Kind: ErrorProcess, Operation: operationName(args), Detail: "close private Git output file", Cause: closeErr}
		}
	}
	supervisorLogPath := filepath.Join(logDir, "supervisor.log")

	childEnv := cloneMap(env)
	childEnv[gitStdoutFileEnv] = stdoutPath
	childEnv[gitStderrFileEnv] = stderrPath
	childEnv[gitFileLimitEnv] = strconv.FormatInt((process.MaximumOutputLimit+511)/512, 10)
	processSpec := contract.ProcessSpec{
		Executable:      "/bin/sh",
		Args:            append([]string{"-c", gitShellScript, "kogen-git", gitPath}, safeGitArgs(r.mode, args)...),
		Dir:             policy.WorkingDirectory,
		Env:             environmentList(childEnv),
		Stdin:           append([]byte(nil), stdin...),
		Timeout:         timeout(policy.Timeout),
		OutputLimit:     1 << 20,
		OutputTailLimit: process.OutputTailBytes,
		LogPath:         supervisorLogPath,
	}
	processResult, runErr := r.processRunner.Run(ctx, processSpec)
	stdout, stdoutErr := readPrivateLog(stdoutPath, process.MaximumOutputLimit)
	stderr, stderrErr := readPrivateTail(stderrPath, tailLimit(policy.StderrTailLimit))
	if stdoutErr != nil && runErr == nil {
		runErr = fmt.Errorf("read supervised Git stdout: %w", stdoutErr)
	}
	if stderrErr != nil && runErr == nil {
		runErr = fmt.Errorf("read supervised Git stderr: %w", stderrErr)
	}
	processResult.LogPath = stdoutPath
	_ = os.Remove(supervisorLogPath)
	if !keepLog {
		_ = os.RemoveAll(logDir)
		processResult.LogPath = ""
	}
	return processResult, stdout, stderr, runErr
}

func (r *Runner) globalConfigOverrides(ctx context.Context, policy contract.GitPolicy, env map[string]string) ([]configEntry, error) {
	fingerprint := environmentFingerprint(env)
	r.configMu.Lock()
	if entries, ok := r.configCache[fingerprint]; ok {
		copyEntries := append([]configEntry(nil), entries...)
		r.configMu.Unlock()
		return copyEntries, nil
	}
	r.configMu.Unlock()

	keys := []string{
		"user.name", "user.email", "user.signingkey", "commit.gpgsign",
		"gpg.format", "gpg.program", "gpg.ssh.program", "gpg.x509.program",
		"core.sshCommand", "core.askPass", "credential.helper",
	}
	values := make(map[string][]string, len(keys))
	for _, key := range keys {
		result, err := r.run(ctx, []string{"config", "--global", "--null", "--get-all", key}, nil, policy, env, false)
		if err != nil {
			return nil, err
		}
		status := *result.Process.ExitStatus
		if status == 1 {
			values[key] = nil
			continue
		}
		if status != 0 {
			return nil, exitError(key, result)
		}
		parsed, err := nulValues(result.Stdout)
		if err != nil {
			return nil, &GitError{Kind: ErrorProcess, Operation: "config --global " + key, Detail: "global config returned malformed NUL-delimited data", Cause: err}
		}
		values[key] = parsed
	}
	entries := makeGlobalOverrides(values)
	r.configMu.Lock()
	if existing, ok := r.configCache[fingerprint]; ok {
		entries = append([]configEntry(nil), existing...)
	} else {
		r.configCache[fingerprint] = append([]configEntry(nil), entries...)
	}
	r.configMu.Unlock()
	return entries, nil
}

type configEntry struct {
	key   string
	value string
}

func makeGlobalOverrides(values map[string][]string) []configEntry {
	defaults := map[string]string{
		"user.name": "", "user.email": "", "user.signingkey": "",
		"commit.gpgsign": "false", "gpg.format": "openpgp", "gpg.program": "gpg",
		"gpg.ssh.program": "ssh-keygen", "gpg.x509.program": "gpgsm", "core.sshCommand": "", "core.askPass": "",
	}
	keys := []string{
		"user.name", "user.email", "user.signingkey", "commit.gpgsign",
		"gpg.format", "gpg.program", "gpg.ssh.program", "gpg.x509.program", "core.sshCommand", "core.askPass",
	}
	entries := make([]configEntry, 0, len(keys)+len(values["credential.helper"])+1)
	for _, key := range keys {
		value := defaults[key]
		if configured := values[key]; len(configured) > 0 {
			value = configured[len(configured)-1]
		}
		entries = append(entries, configEntry{key: key, value: value})
	}
	// An empty helper clears repository-local helpers; configured global helpers
	// are then re-added in order. Git invokes each as a child of the supervised
	// process group.
	entries = append(entries, configEntry{key: "credential.helper", value: ""})
	for _, helper := range values["credential.helper"] {
		entries = append(entries, configEntry{key: "credential.helper", value: helper})
	}
	return entries
}

func withGlobalSigning(args []string, entries []configEntry) []string {
	if len(args) == 0 || args[0] != "commit-tree" || !globalCommitSigningEnabled(entries) {
		return args
	}
	for _, arg := range args[1:] {
		if arg == "-S" || strings.HasPrefix(arg, "-S") || arg == "--gpg-sign" || strings.HasPrefix(arg, "--gpg-sign=") || arg == "--no-gpg-sign" {
			return args
		}
	}
	return append([]string{"commit-tree", "-S"}, args[1:]...)
}

func globalCommitSigningEnabled(entries []configEntry) bool {
	for _, entry := range entries {
		if entry.key != "commit.gpgsign" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(entry.value)) {
		case "true", "yes", "on", "1":
			return true
		}
	}
	return false
}

func safeGitArgs(mode operationMode, args []string) []string {
	result := []string{
		"--no-pager",
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=false",
		"-c", "core.autocrlf=false",
		"-c", "core.filemode=true",
		"-c", "core.excludesFile=/dev/null",
		"-c", "core.attributesFile=/dev/null",
		"-c", "core.pager=cat",
		"-c", "color.ui=false",
		"-c", "gc.auto=0",
		"-c", "maintenance.auto=false",
	}
	if mode == controlledWorkspace {
		result = append(result, "-c", "commit.gpgsign=false", "-c", "credential.helper=", "-c", "core.sshCommand=", "-c", "core.askPass=")
	}
	if len(args) > 0 {
		command := args[0]
		switch command {
		case "hash-object":
			if !containsArg(args[1:], "--no-filters") {
				result = append(result, args...)
				result = append(result, "--no-filters")
				return result
			}
		case "diff", "diff-tree", "show":
			if !containsArg(args[1:], "--no-ext-diff") {
				result = append(result, command, "--no-ext-diff", "--no-textconv")
				result = append(result, args[1:]...)
				return result
			}
		case "commit":
			if !containsArg(args[1:], "--no-verify") {
				result = append(result, command, "--no-verify")
				result = append(result, args[1:]...)
				return result
			}
		}
	}
	return append(result, args...)
}

func validateInvocation(ctx context.Context, args []string, policy contract.GitPolicy, mode operationMode) error {
	if ctx == nil {
		return &GitError{Kind: ErrorInvalid, Operation: operationName(args), Detail: "context is required"}
	}
	if len(args) == 0 || args[0] == "" {
		return &GitError{Kind: ErrorInvalid, Operation: "", Detail: "Git subcommand is required"}
	}
	if !filepath.IsAbs(policy.WorkingDirectory) || filepath.Clean(policy.WorkingDirectory) != policy.WorkingDirectory || strings.ContainsRune(policy.WorkingDirectory, '\x00') {
		return &GitError{Kind: ErrorInvalid, Operation: operationName(args), Detail: "working directory must be a clean absolute path"}
	}
	info, err := os.Stat(policy.WorkingDirectory)
	if err != nil || !info.IsDir() {
		return &GitError{Kind: ErrorInvalid, Operation: operationName(args), Detail: "working directory must exist and be a directory", Cause: err}
	}
	if policy.Timeout < 0 {
		return &GitError{Kind: ErrorInvalid, Operation: operationName(args), Detail: "timeout must not be negative"}
	}
	if policy.StdoutLimit < 0 || policy.StdoutLimit >= process.MaximumOutputLimit {
		return &GitError{Kind: ErrorInvalid, Operation: operationName(args), Detail: "stdout limit is outside the supported range"}
	}
	if policy.StderrTailLimit < 0 || policy.StderrTailLimit > process.OutputTailBytes {
		return &GitError{Kind: ErrorInvalid, Operation: operationName(args), Detail: "stderr tail limit is outside the supported range"}
	}
	for _, arg := range args {
		if strings.ContainsRune(arg, '\x00') {
			return &GitError{Kind: ErrorInvalid, Operation: operationName(args), Detail: "argument contains NUL"}
		}
		if len(arg) > process.MaximumArgumentSize {
			return &GitError{Kind: ErrorInvalid, Operation: operationName(args), Detail: "argument exceeds the 4 KiB process limit"}
		}
		if isUnsafeGlobalOption(arg) {
			return &GitError{Kind: ErrorInvalid, Operation: operationName(args), Detail: "caller may not override the fixed Git operation policy"}
		}
	}
	if args[0] == "hash-object" {
		if containsArg(args[1:], "--filters") {
			return &GitError{Kind: ErrorInvalid, Operation: "hash-object", Detail: "exact-byte object writes must not use filters"}
		}
		for _, arg := range args[1:] {
			if arg == "--path" || strings.HasPrefix(arg, "--path=") {
				return &GitError{Kind: ErrorInvalid, Operation: "hash-object", Detail: "path conversion is unavailable for exact-byte object writes"}
			}
		}
	}
	if mode == controlledWorkspace {
		for _, arg := range args[1:] {
			if arg == "-S" || strings.HasPrefix(arg, "-S") || arg == "--gpg-sign" || strings.HasPrefix(arg, "--gpg-sign=") ||
				arg == "--textconv" || arg == "--ext-diff" {
				return &GitError{Kind: ErrorInvalid, Operation: operationName(args), Detail: "controlled workspace operations may not enable external helpers"}
			}
		}
	}
	return nil
}

func isUnsafeGlobalOption(arg string) bool {
	if arg == "-c" || strings.HasPrefix(arg, "-c") || arg == "--config-env" || strings.HasPrefix(arg, "--config-env=") ||
		arg == "--git-dir" || strings.HasPrefix(arg, "--git-dir=") || arg == "--work-tree" || strings.HasPrefix(arg, "--work-tree=") ||
		arg == "-C" || strings.HasPrefix(arg, "-C") || arg == "--exec-path" || strings.HasPrefix(arg, "--exec-path=") {
		return true
	}
	return false
}

func prepareEnvironment(input map[string]string, mode operationMode) map[string]string {
	env := cloneMap(input)
	if mode == controlledWorkspace {
		env["GIT_CONFIG_GLOBAL"] = "/dev/null"
		env["GIT_CONFIG_NOSYSTEM"] = "1"
		env["GIT_ATTR_NOSYSTEM"] = "1"
		env["GIT_PAGER"] = "cat"
		env["PAGER"] = "cat"
		for key := range env {
			if workspaceHelperEnvironment(key) {
				delete(env, key)
			}
		}
	}
	env["GIT_TERMINAL_PROMPT"] = "0"
	env["GIT_CONFIG_NOSYSTEM"] = "1"
	env["GIT_ATTR_NOSYSTEM"] = "1"
	deleteGitConfigEnvironment(process.Environment(env))
	return env
}

func addConfigEnvironment(env map[string]string, entries []configEntry) map[string]string {
	result := cloneMap(env)
	deleteGitConfigEnvironment(process.Environment(result))
	result["GIT_CONFIG_COUNT"] = strconv.Itoa(len(entries))
	for index, entry := range entries {
		result["GIT_CONFIG_KEY_"+strconv.Itoa(index)] = entry.key
		result["GIT_CONFIG_VALUE_"+strconv.Itoa(index)] = entry.value
	}
	return result
}

func parseEnvironment(entries []string) (map[string]string, error) {
	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, '\x00') {
			return nil, errors.New("environment must contain NUL-free KEY=value entries")
		}
		if _, duplicate := result[key]; duplicate {
			return nil, fmt.Errorf("duplicate environment key %q", key)
		}
		result[key] = value
	}
	return result, nil
}

func environmentList(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+env[key])
	}
	return result
}

func executableFromPath(name, path, cwd string) (string, error) {
	if path == "" {
		return "", errors.New("PATH is empty")
	}
	for _, directory := range filepath.SplitList(path) {
		if directory == "" {
			directory = "."
		}
		if !filepath.IsAbs(directory) {
			directory = filepath.Join(cwd, directory)
		}
		candidate := filepath.Join(directory, name)
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			return "", err
		}
		return absolute, nil
	}
	return "", fmt.Errorf("%s not found on child PATH", name)
}

func readPrivateLog(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, limit))
}

func readPrivateTail(path string, limit int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	start := info.Size() - int64(limit)
	if start < 0 {
		start = 0
	}
	return io.ReadAll(io.NewSectionReader(file, start, int64(limit)))
}

func discardPrivateOutputLog(result *contract.ProcessResult) {
	if result == nil || result.LogPath == "" {
		return
	}
	directory := filepath.Dir(result.LogPath)
	_ = os.RemoveAll(directory)
	result.LogPath = ""
}

func tailLimit(value int) int {
	if value == 0 {
		return process.OutputTailBytes
	}
	return value
}

func policyLimit(value, fallback int64) int64 {
	if value == 0 {
		return fallback
	}
	return value
}

func timeout(value time.Duration) time.Duration {
	if value == 0 {
		return GitTimeout
	}
	return value
}

func cloneMap(input map[string]string) map[string]string {
	result := make(map[string]string, len(input)+4)
	for key, value := range input {
		result[key] = value
	}
	return result
}

func operationName(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func containsArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func environmentFingerprint(env map[string]string) string {
	entries := environmentList(env)
	hasher := sha256.New()
	for _, entry := range entries {
		_, _ = io.WriteString(hasher, entry)
		_, _ = hasher.Write([]byte{0})
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func nulValues(input []byte) ([]string, error) {
	if len(input) == 0 {
		return []string{}, nil
	}
	if input[len(input)-1] != 0 {
		return nil, errors.New("missing NUL terminator")
	}
	parts := bytes.Split(input[:len(input)-1], []byte{0})
	values := make([]string, len(parts))
	for index, part := range parts {
		if bytes.IndexByte(part, 0) >= 0 {
			return nil, errors.New("embedded NUL value")
		}
		values[index] = string(part)
	}
	return values, nil
}

func exitError(operation string, result contract.GitResult) *GitError {
	status := 0
	if result.Process.ExitStatus != nil {
		status = *result.Process.ExitStatus
	}
	detail := strings.TrimSpace(string(result.StderrTail))
	if detail == "" {
		detail = fmt.Sprintf("Git exited with status %d", status)
	}
	return &GitError{Kind: ErrorExit, Operation: operation, ExitStatus: &status, Detail: detail}
}
