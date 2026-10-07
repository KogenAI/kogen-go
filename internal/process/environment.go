package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kogen-go/internal/contract"
)

const (
	miseEnvironmentTimeout = 30 * time.Second
	miseOutputLimit        = 1 << 20
	miseLogName            = "mise-env.log"
)

// Environment is a complete environment map. ProcessRunner receives it as a
// sorted []KEY=VALUE list so calls are deterministic and never inherit env.
type Environment map[string]string

// EnvironmentRequest contains the trusted inputs used to construct one
// project-child environment. RunRoot must already be rooted at RunDir by the
// caller; all directories created here remain relative to that capability.
type EnvironmentRequest struct {
	Base              Environment
	RunDir            string
	RunRoot           contract.RootedFS
	ProjectRoot       string
	Workspace         string
	Project           Environment
	KogenRuntimePaths []string
	AdapterStackHomes []string
}

// EnvironmentError reports a construction or mise-probe failure without
// retaining the environment values that caused it.
type EnvironmentError struct {
	Operation string
	Cause     error
	Status    *int
	TimedOut  bool
	LogPath   string
}

func (e *EnvironmentError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Cause != nil {
		return "process environment: " + e.Operation + ": " + e.Cause.Error()
	}
	return fmt.Sprintf("process environment: %s (status=%v, timed_out=%t, log=%s)", e.Operation, e.Status, e.TimedOut, e.LogPath)
}

func (e *EnvironmentError) Unwrap() error { return e.Cause }

// HostEnvironment snapshots the Kogen process environment. Callers should
// pass this snapshot explicitly instead of letting a child inherit os.Environ.
func HostEnvironment() Environment {
	env := make(Environment)
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if ok && key != "" {
			env[key] = value
		}
	}
	return env
}

// ControllerGitEnvironment builds the base environment for Kogen's own Git
// calls. It deliberately ignores project child overrides, preserving the
// user's production identity and signing settings. Hermetic tests use the
// separate policy in internal/testkit instead of changing this production map.
func ControllerGitEnvironment(base Environment) Environment {
	result := make(Environment, len(base))
	for key, value := range base {
		result[key] = value
	}
	return result
}

// BuildChildEnvironment filters the host environment, installs run-local temp
// and mise state, merges a supervised mise JSON probe when available, then
// applies project env values last. Project PATH is copied verbatim.
func BuildChildEnvironment(ctx context.Context, runner contract.ProcessRunner, request EnvironmentRequest) (Environment, error) {
	if ctx == nil {
		return nil, &EnvironmentError{Operation: "context is nil"}
	}
	if runner == nil {
		return nil, &EnvironmentError{Operation: "process runner is nil"}
	}
	if request.RunRoot == nil {
		return nil, &EnvironmentError{Operation: "run directory root is required"}
	}
	if !filepath.IsAbs(request.RunDir) || filepath.Clean(request.RunDir) != request.RunDir {
		return nil, &EnvironmentError{Operation: "run directory must be a clean absolute path"}
	}
	if !filepath.IsAbs(request.Workspace) || filepath.Clean(request.Workspace) != request.Workspace {
		return nil, &EnvironmentError{Operation: "workspace must be a clean absolute path"}
	}
	if !filepath.IsAbs(request.ProjectRoot) || filepath.Clean(request.ProjectRoot) != request.ProjectRoot {
		return nil, &EnvironmentError{Operation: "project root must be a clean absolute path"}
	}
	for key, value := range request.Project {
		if err := validateEnvironmentEntry(key, value); err != nil {
			return nil, &EnvironmentError{Operation: "project environment", Cause: err}
		}
	}

	stackHomes, err := normalizedStackHomes(request.AdapterStackHomes)
	if err != nil {
		return nil, &EnvironmentError{Operation: "adapter stack homes", Cause: err}
	}
	runtimePaths := runtimePaths(request.KogenRuntimePaths)
	child := filteredBase(request.Base, stackHomes, runtimePaths)

	if err := ensurePrivateRunDir(request.RunRoot, "tmp"); err != nil {
		return nil, &EnvironmentError{Operation: "create private temp directory", Cause: err}
	}
	child["TMPDIR"] = filepath.Join(request.RunDir, "tmp")

	if mise, ok := findExecutable("mise", request.Base["PATH"]); ok {
		if err := mergeMiseEnvironment(ctx, runner, &request, mise, runtimePaths, child); err != nil {
			return nil, err
		}
	}

	for key, value := range request.Project {
		child[key] = value
	}
	return child, nil
}

// SetupKeyEnvironment removes the three mise state/trust paths excluded by
// §2.9. TMPDIR remains part of child_env in the v1.3 draft's literal wording.
func SetupKeyEnvironment(environment Environment) Environment {
	result := make(Environment, len(environment))
	for key, value := range environment {
		if key == "MISE_STATE_DIR" || key == "MISE_CACHE_DIR" || key == "MISE_TRUSTED_CONFIG_PATHS" {
			continue
		}
		result[key] = value
	}
	return result
}

func filteredBase(base Environment, stackHomes map[string]struct{}, runtimePaths map[string]struct{}) Environment {
	child := make(Environment)
	for key, value := range base {
		if !baseKeyAllowed(key, stackHomes) {
			continue
		}
		if key == "PATH" && runtimePaths != nil {
			value = withoutRuntimePaths(value, runtimePaths)
		}
		child[key] = value
	}
	return child
}

func baseKeyAllowed(key string, stackHomes map[string]struct{}) bool {
	if key == "PATH" || key == "HOME" || key == "LANG" || key == "LC_ALL" || key == "TERM" || key == "USER" || key == "SHELL" {
		return true
	}
	if strings.HasPrefix(key, "GIT_") || strings.HasPrefix(key, "MISE_") {
		return true
	}
	if strings.HasSuffix(strings.ToLower(key), "_proxy") {
		return true
	}
	_, allowed := stackHomes[key]
	return allowed
}

func normalizedStackHomes(names []string) (map[string]struct{}, error) {
	allowed := map[string]struct{}{"MIX_HOME": {}, "HEX_HOME": {}}
	result := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, ok := allowed[name]; !ok {
			return nil, fmt.Errorf("unsupported stack home %q", name)
		}
		result[name] = struct{}{}
	}
	return result, nil
}

func runtimePaths(explicit []string) map[string]struct{} {
	paths := append([]string(nil), explicit...)
	if executable, err := os.Executable(); err == nil {
		paths = append(paths, filepath.Dir(executable))
	}
	result := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		result[normalizePath(path)] = struct{}{}
	}
	return result
}

func normalizePath(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}

func withoutRuntimePaths(path string, runtimePaths map[string]struct{}) string {
	entries := filepath.SplitList(path)
	filtered := make([]string, 0, len(entries))
	for _, entry := range entries {
		if _, remove := runtimePaths[normalizePath(entry)]; !remove {
			filtered = append(filtered, entry)
		}
	}
	return strings.Join(filtered, string(os.PathListSeparator))
}

func findExecutable(program, path string) (string, bool) {
	for _, directory := range filepath.SplitList(path) {
		candidate := filepath.Join(directory, program)
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if info.Mode().Perm()&0o111 != 0 {
			absolute, err := filepath.Abs(candidate)
			if err == nil {
				return absolute, true
			}
			return candidate, true
		}
	}
	return "", false
}

func ensurePrivateRunDir(root contract.RootedFS, name string) error {
	if err := root.MkdirAll(name, 0o700); err != nil {
		return err
	}
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("directory must be private, non-symlink, and owner-only")
	}
	return nil
}

func mergeMiseEnvironment(ctx context.Context, runner contract.ProcessRunner, request *EnvironmentRequest, mise string, runtimePaths map[string]struct{}, child Environment) error {
	for _, directory := range []string{"mise-state", "mise-cache", "logs"} {
		if err := ensurePrivateRunDir(request.RunRoot, directory); err != nil {
			return &EnvironmentError{Operation: "create private mise directory", Cause: err}
		}
	}
	stateDir := filepath.Join(request.RunDir, "mise-state")
	cacheDir := filepath.Join(request.RunDir, "mise-cache")
	inheritedTrusted := child["MISE_TRUSTED_CONFIG_PATHS"]
	child["MISE_STATE_DIR"] = stateDir
	child["MISE_CACHE_DIR"] = cacheDir
	child["MISE_TRUSTED_CONFIG_PATHS"] = trustedConfigPaths(inheritedTrusted, request.ProjectRoot, request.Workspace)

	logPath := filepath.Join(request.RunDir, "logs", miseLogName)
	probe := contract.ProcessSpec{
		Executable:  mise,
		Args:        []string{"env", "-C", request.Workspace, "--json", "--quiet"},
		Dir:         request.Workspace,
		Env:         environmentList(child),
		Timeout:     miseEnvironmentTimeout,
		OutputLimit: miseOutputLimit + 1,
		LogPath:     logPath,
	}
	result, err := runner.Run(ctx, probe)
	if err != nil {
		return &EnvironmentError{Operation: "run mise environment probe", Cause: err, LogPath: logPath}
	}
	if result.Unavailable || result.TimedOut || result.ExitStatus == nil || *result.ExitStatus != 0 {
		return &EnvironmentError{Operation: "mise env failed", Status: result.ExitStatus, TimedOut: result.TimedOut, LogPath: result.LogPath}
	}
	output, err := request.RunRoot.ReadFile("logs/" + miseLogName)
	if err != nil {
		return &EnvironmentError{Operation: "read mise environment log", Cause: err, LogPath: result.LogPath}
	}
	if len(output) > miseOutputLimit {
		return &EnvironmentError{Operation: "mise env JSON exceeds 1 MiB", LogPath: result.LogPath}
	}
	var values map[string]string
	if err := json.Unmarshal(output, &values); err != nil || values == nil {
		if err == nil {
			err = errors.New("expected a JSON object")
		}
		return &EnvironmentError{Operation: "mise env returned invalid JSON", Cause: err, LogPath: result.LogPath}
	}
	for key, value := range values {
		if err := validateEnvironmentEntry(key, value); err != nil {
			return &EnvironmentError{Operation: "mise environment entry", Cause: err, LogPath: result.LogPath}
		}
		child[key] = value
	}

	// mise cannot redirect its private state or trusted-config roots. Its PATH
	// still wins over the base PATH, with the selected mise installation first.
	child["MISE_STATE_DIR"] = stateDir
	child["MISE_CACHE_DIR"] = cacheDir
	child["MISE_TRUSTED_CONFIG_PATHS"] = trustedConfigPaths(inheritedTrusted, child["MISE_TRUSTED_CONFIG_PATHS"], request.ProjectRoot, request.Workspace)
	path, hasPath := child["PATH"]
	if !hasPath {
		path = request.Base["PATH"]
		path = withoutRuntimePaths(path, runtimePaths)
	}
	entries := []string{filepath.Dir(mise)}
	for _, entry := range filepath.SplitList(path) {
		if _, remove := runtimePaths[normalizePath(entry)]; !remove {
			entries = append(entries, entry)
		}
	}
	child["PATH"] = strings.Join(entries, string(os.PathListSeparator))
	return nil
}

func trustedConfigPaths(sources ...string) string {
	seen := make(map[string]struct{})
	unique := make([]string, 0)
	for _, source := range sources {
		for _, entry := range filepath.SplitList(source) {
			if _, ok := seen[entry]; !ok {
				seen[entry] = struct{}{}
				unique = append(unique, entry)
			}
		}
	}
	sort.Strings(unique)
	return strings.Join(unique, string(os.PathListSeparator))
}

func environmentList(environment Environment) []string {
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+environment[key])
	}
	return result
}

func validateEnvironmentEntry(key, value string) error {
	if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
		return errors.New("invalid environment variable name or value")
	}
	return nil
}
