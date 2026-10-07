package process

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/safefs"
)

type processRunnerFunc func(context.Context, contract.ProcessSpec) (contract.ProcessResult, error)

func (fn processRunnerFunc) Run(ctx context.Context, spec contract.ProcessSpec) (contract.ProcessResult, error) {
	return fn(ctx, spec)
}

type environmentFixture struct {
	rootDir   string
	runDir    string
	workspace string
	project   string
	root      *safefs.Root
}

func newEnvironmentFixture(t *testing.T) environmentFixture {
	t.Helper()
	rootDir := t.TempDir()
	runDir := filepath.Join(rootDir, "run")
	workspace := filepath.Join(rootDir, "workspace")
	project := filepath.Join(rootDir, "project")
	for _, dir := range []string{runDir, workspace, project} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	root, err := safefs.OpenRoot(runDir)
	if err != nil {
		t.Fatalf("open run root: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return environmentFixture{rootDir: rootDir, runDir: runDir, workspace: workspace, project: project, root: root}
}

func TestBuildChildEnvironmentExactAllowlistMiseAndProjectPrecedence(t *testing.T) {
	fixture := newEnvironmentFixture(t)
	miseBin := filepath.Join(fixture.rootDir, "mise-bin")
	runtimeBin := filepath.Join(fixture.rootDir, "kogen-bin")
	otherBin := filepath.Join(fixture.rootDir, "other-bin")
	for _, dir := range []string{miseBin, runtimeBin, otherBin} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	mise := filepath.Join(miseBin, "mise")
	if err := os.WriteFile(mise, []byte("mise stub"), 0o700); err != nil {
		t.Fatalf("create mise: %v", err)
	}
	basePath := strings.Join([]string{miseBin, runtimeBin, otherBin}, string(os.PathListSeparator))
	base := Environment{
		"PATH":                      basePath,
		"HOME":                      "/host/home",
		"LANG":                      "en_US.UTF-8",
		"LC_ALL":                    "en_US.UTF-8",
		"TERM":                      "xterm-256color",
		"USER":                      "worker",
		"SHELL":                     "/bin/zsh",
		"HTTP_PROXY":                "http://proxy.invalid",
		"https_proxy":               "https://proxy.invalid",
		"NO_PROXY":                  "localhost",
		"GIT_CONFIG_GLOBAL":         "/host/gitconfig",
		"GIT_COMMITTER_NAME":        "Production User",
		"MISE_TRUSTED_CONFIG_PATHS": "/existing/trusted",
		"MISE_EXPERIMENTAL":         "1",
		"MIX_HOME":                  "/host/mix",
		"HEX_HOME":                  "/host/hex",
		"KOGEN_SECRET":              "filtered",
		"RUST_LOG":                  "filtered",
	}
	projectValues := map[string]string{
		"MISE_VALUE":   "from-project",
		"PATH":         "/project/bin",
		"TMPDIR":       "/project/tmp",
		"PROJECT_ONLY": "included",
	}
	probeBody, err := json.Marshal(map[string]string{
		"MISE_VALUE":                "from-mise",
		"PATH":                      "/mise/tool/bin",
		"MISE_STATE_DIR":            "/outside/state",
		"MISE_CACHE_DIR":            "/outside/cache",
		"MISE_TRUSTED_CONFIG_PATHS": "/mise/trusted",
		"MISE_ADDED":                "yes",
	})
	if err != nil {
		t.Fatal(err)
	}
	var recorded contract.ProcessSpec
	runner := processRunnerFunc(func(_ context.Context, spec contract.ProcessSpec) (contract.ProcessResult, error) {
		recorded = spec
		if err := os.WriteFile(spec.LogPath, probeBody, 0o600); err != nil {
			return contract.ProcessResult{}, err
		}
		status := 0
		return contract.ProcessResult{ExitStatus: &status, LogPath: spec.LogPath}, nil
	})
	request := EnvironmentRequest{
		Base:              base,
		RunDir:            fixture.runDir,
		RunRoot:           fixture.root,
		ProjectRoot:       fixture.project,
		Workspace:         fixture.workspace,
		Project:           projectValues,
		KogenRuntimePaths: []string{runtimeBin},
		AdapterStackHomes: []string{"MIX_HOME"},
	}
	actual, err := BuildChildEnvironment(context.Background(), runner, request)
	if err != nil {
		t.Fatalf("build child environment: %v", err)
	}
	expected := Environment{
		"PATH":                      "/project/bin",
		"HOME":                      "/host/home",
		"LANG":                      "en_US.UTF-8",
		"LC_ALL":                    "en_US.UTF-8",
		"TERM":                      "xterm-256color",
		"USER":                      "worker",
		"SHELL":                     "/bin/zsh",
		"HTTP_PROXY":                "http://proxy.invalid",
		"https_proxy":               "https://proxy.invalid",
		"NO_PROXY":                  "localhost",
		"GIT_CONFIG_GLOBAL":         "/host/gitconfig",
		"GIT_COMMITTER_NAME":        "Production User",
		"MISE_TRUSTED_CONFIG_PATHS": strings.Join([]string{"/existing/trusted", "/mise/trusted", fixture.project, fixture.workspace}, string(os.PathListSeparator)),
		"MISE_EXPERIMENTAL":         "1",
		"MISE_VALUE":                "from-project",
		"MISE_ADDED":                "yes",
		"MISE_STATE_DIR":            filepath.Join(fixture.runDir, "mise-state"),
		"MISE_CACHE_DIR":            filepath.Join(fixture.runDir, "mise-cache"),
		"MIX_HOME":                  "/host/mix",
		"TMPDIR":                    "/project/tmp",
		"PROJECT_ONLY":              "included",
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Errorf("child environment mismatch\n got: %#v\nwant: %#v", actual, expected)
	}

	probeExpected := Environment{
		"PATH":                      basePathFiltered(miseBin, otherBin),
		"HOME":                      "/host/home",
		"LANG":                      "en_US.UTF-8",
		"LC_ALL":                    "en_US.UTF-8",
		"TERM":                      "xterm-256color",
		"USER":                      "worker",
		"SHELL":                     "/bin/zsh",
		"HTTP_PROXY":                "http://proxy.invalid",
		"https_proxy":               "https://proxy.invalid",
		"NO_PROXY":                  "localhost",
		"GIT_CONFIG_GLOBAL":         "/host/gitconfig",
		"GIT_COMMITTER_NAME":        "Production User",
		"MISE_TRUSTED_CONFIG_PATHS": strings.Join([]string{"/existing/trusted", fixture.project, fixture.workspace}, string(os.PathListSeparator)),
		"MISE_EXPERIMENTAL":         "1",
		"MIX_HOME":                  "/host/mix",
		"TMPDIR":                    filepath.Join(fixture.runDir, "tmp"),
		"MISE_STATE_DIR":            filepath.Join(fixture.runDir, "mise-state"),
		"MISE_CACHE_DIR":            filepath.Join(fixture.runDir, "mise-cache"),
	}
	expectedMise, _ := filepath.Abs(mise)
	if recorded.Executable != expectedMise || !reflect.DeepEqual(recorded.Args, []string{"env", "-C", fixture.workspace, "--json", "--quiet"}) {
		t.Errorf("unexpected mise invocation: executable=%q args=%q", recorded.Executable, recorded.Args)
	}
	if recorded.Dir != fixture.workspace || recorded.Timeout != 30*time.Second || recorded.OutputLimit != miseOutputLimit+1 || recorded.LogPath != filepath.Join(fixture.runDir, "logs", miseLogName) {
		t.Errorf("unexpected mise process policy: %#v", recorded)
	}
	if !reflect.DeepEqual(recorded.Env, environmentList(probeExpected)) {
		t.Errorf("mise probe environment mismatch\n got: %q\nwant: %q", recorded.Env, environmentList(probeExpected))
	}
	for _, name := range []string{"tmp", "mise-state", "mise-cache", "logs"} {
		info, err := os.Stat(filepath.Join(fixture.runDir, name))
		if err != nil {
			t.Errorf("stat %s: %v", name, err)
		} else if info.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %04o, want 0700", name, info.Mode().Perm())
		}
	}
}

func TestTrustedConfigPathsPreservesPrecedenceWhenTMPDIRSortsFirst(t *testing.T) {
	tmpDir := "/dev/shm/kogen-run"
	if tmpDir >= "/existing/trusted" {
		t.Fatalf("test TMPDIR %q must sort before inherited trusted paths", tmpDir)
	}
	project := filepath.Join(tmpDir, "project")
	workspace := filepath.Join(tmpDir, "workspace")

	got := trustedConfigPaths("/existing/trusted", "/mise/trusted", project, workspace)
	want := strings.Join([]string{"/existing/trusted", "/mise/trusted", project, workspace}, string(os.PathListSeparator))
	if got != want {
		t.Fatalf("trusted config paths = %q, want source precedence %q", got, want)
	}
}

func basePathFiltered(miseBin, otherBin string) string {
	return strings.Join([]string{miseBin, otherBin}, string(os.PathListSeparator))
}

func TestBuildChildEnvironmentUsesSupervisedMiseProbe(t *testing.T) {
	fixture := newEnvironmentFixture(t)
	bin := filepath.Join(fixture.rootDir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	mise := filepath.Join(bin, "mise")
	if err := os.WriteFile(mise, []byte("#!/bin/sh\nprintf '{\"FROM_MISE\":\"yes\"}\\n'\n"), 0o700); err != nil {
		t.Fatalf("write mise stub: %v", err)
	}
	request := EnvironmentRequest{
		Base:        Environment{"PATH": bin},
		RunDir:      fixture.runDir,
		RunRoot:     fixture.root,
		ProjectRoot: fixture.project,
		Workspace:   fixture.workspace,
	}
	actual, err := BuildChildEnvironment(context.Background(), Supervisor{}, request)
	if err != nil {
		t.Fatalf("build child environment with supervised mise: %v", err)
	}
	if actual["FROM_MISE"] != "yes" {
		t.Fatalf("mise variable = %q, want yes", actual["FROM_MISE"])
	}
	if got := actual["PATH"]; got != strings.Join([]string{bin, bin}, string(os.PathListSeparator)) {
		t.Fatalf("PATH = %q", got)
	}
	if _, err := fixture.root.ReadFile("logs/" + miseLogName); err != nil {
		t.Fatalf("supervisor did not publish mise log: %v", err)
	}
}

func TestMiseEmptyPathOverridesTheBasePathBeforePrependingMise(t *testing.T) {
	fixture := newEnvironmentFixture(t)
	bin := filepath.Join(fixture.rootDir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	mise := filepath.Join(bin, "mise")
	if err := os.WriteFile(mise, []byte("stub"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := processRunnerFunc(func(_ context.Context, spec contract.ProcessSpec) (contract.ProcessResult, error) {
		if err := os.WriteFile(spec.LogPath, []byte(`{"PATH":""}`), 0o600); err != nil {
			return contract.ProcessResult{}, err
		}
		return successfulResult(), nil
	})
	actual, err := BuildChildEnvironment(context.Background(), runner, EnvironmentRequest{
		Base:   Environment{"PATH": strings.Join([]string{bin, "/base/bin"}, string(os.PathListSeparator))},
		RunDir: fixture.runDir, RunRoot: fixture.root, ProjectRoot: fixture.project, Workspace: fixture.workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	if actual["PATH"] != bin {
		t.Fatalf("PATH = %q, want only mise directory %q", actual["PATH"], bin)
	}
}

func TestBuildChildEnvironmentWithoutMiseAndGitBaseSplit(t *testing.T) {
	fixture := newEnvironmentFixture(t)
	base := Environment{
		"PATH":                "/base/bin",
		"HOME":                "/base/home",
		"GIT_CONFIG_GLOBAL":   "/production/gitconfig",
		"GIT_COMMITTER_NAME":  "Production Name",
		"GIT_COMMITTER_EMAIL": "production@example.invalid",
		"XDG_CONFIG_HOME":     "/production/config",
		"KOGEN_SECRET":        "preserved-for-controller",
	}
	project := Environment{"GIT_CONFIG_GLOBAL": "/project/should-not-control-git", "PROJECT_ONLY": "yes"}
	var calls int
	runner := processRunnerFunc(func(context.Context, contract.ProcessSpec) (contract.ProcessResult, error) {
		calls++
		return contract.ProcessResult{}, errors.New("unexpected mise invocation")
	})
	actual, err := BuildChildEnvironment(context.Background(), runner, EnvironmentRequest{
		Base: base, RunDir: fixture.runDir, RunRoot: fixture.root,
		ProjectRoot: fixture.project, Workspace: fixture.workspace, Project: project,
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("mise probe called %d times", calls)
	}
	if !reflect.DeepEqual(actual, Environment{
		"PATH": "/base/bin", "HOME": "/base/home", "GIT_CONFIG_GLOBAL": "/project/should-not-control-git",
		"GIT_COMMITTER_NAME": "Production Name", "GIT_COMMITTER_EMAIL": "production@example.invalid",
		"TMPDIR": filepath.Join(fixture.runDir, "tmp"), "PROJECT_ONLY": "yes",
	}) {
		t.Fatalf("unexpected no-mise env: %#v", actual)
	}
	gitEnv := ControllerGitEnvironment(base)
	if gitEnv["GIT_CONFIG_GLOBAL"] != "/production/gitconfig" || gitEnv["GIT_COMMITTER_NAME"] != "Production Name" || gitEnv["XDG_CONFIG_HOME"] != "/production/config" || gitEnv["KOGEN_SECRET"] != "preserved-for-controller" {
		t.Fatalf("controller Git did not preserve the production base environment: %#v", gitEnv)
	}
	if project["GIT_CONFIG_GLOBAL"] == gitEnv["GIT_CONFIG_GLOBAL"] {
		t.Fatal("project child override leaked into controller Git environment")
	}
}

func TestSetupKeyEnvironmentExcludesMiseStatePathsButKeepsTMPDIR(t *testing.T) {
	first := Environment{
		"PATH": "/tools/bin", "NODE_OPTIONS": "--conditions=production",
		"TMPDIR": "/run/one/tmp", "MISE_STATE_DIR": "/run/one/mise-state",
		"MISE_CACHE_DIR": "/run/one/mise-cache", "MISE_TRUSTED_CONFIG_PATHS": "/project:/workspace",
	}
	second := Environment{
		"PATH": "/tools/bin", "NODE_OPTIONS": "--conditions=production",
		"TMPDIR": "/run/two/tmp", "MISE_STATE_DIR": "/run/two/mise-state",
		"MISE_CACHE_DIR": "/run/two/mise-cache", "MISE_TRUSTED_CONFIG_PATHS": "/project:/workspace",
	}
	wantFirst := Environment{"PATH": "/tools/bin", "NODE_OPTIONS": "--conditions=production", "TMPDIR": "/run/one/tmp"}
	wantSecond := Environment{"PATH": "/tools/bin", "NODE_OPTIONS": "--conditions=production", "TMPDIR": "/run/two/tmp"}
	if !reflect.DeepEqual(SetupKeyEnvironment(first), wantFirst) || !reflect.DeepEqual(SetupKeyEnvironment(second), wantSecond) {
		t.Fatalf("unexpected setup-key env: %#v / %#v", SetupKeyEnvironment(first), SetupKeyEnvironment(second))
	}
	second["NODE_OPTIONS"] = "--conditions=development"
	if reflect.DeepEqual(SetupKeyEnvironment(first), SetupKeyEnvironment(second)) {
		t.Fatal("setup-affecting environment change was discarded")
	}
}

func TestBuildChildEnvironmentRejectsUnsafeAndInvalidMiseResults(t *testing.T) {
	t.Run("temporary directory symlink", func(t *testing.T) {
		fixture := newEnvironmentFixture(t)
		if err := os.Mkdir(filepath.Join(fixture.runDir, "elsewhere"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("elsewhere", filepath.Join(fixture.runDir, "tmp")); err != nil {
			t.Fatal(err)
		}
		_, err := BuildChildEnvironment(context.Background(), processRunnerFunc(func(context.Context, contract.ProcessSpec) (contract.ProcessResult, error) {
			t.Fatal("runner called before temp directory validation")
			return contract.ProcessResult{}, nil
		}), EnvironmentRequest{
			Base: Environment{"PATH": "/no-mise"}, RunDir: fixture.runDir, RunRoot: fixture.root,
			ProjectRoot: fixture.project, Workspace: fixture.workspace,
		})
		if err == nil {
			t.Fatal("accepted symlink temp directory")
		}
	})

	for _, test := range []struct {
		name   string
		output []byte
		result contract.ProcessResult
	}{
		{name: "invalid json", output: []byte("not json"), result: successfulResult()},
		{name: "oversized output", output: []byte(strings.Repeat("x", miseOutputLimit+1)), result: successfulResult()},
		{name: "timeout", output: []byte(`{"OK":"no"}`), result: contract.ProcessResult{TimedOut: true}},
		{name: "unavailable", output: []byte(`{}`), result: contract.ProcessResult{Unavailable: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newEnvironmentFixture(t)
			bin := filepath.Join(fixture.rootDir, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			mise := filepath.Join(bin, "mise")
			if err := os.WriteFile(mise, []byte("stub"), 0o700); err != nil {
				t.Fatal(err)
			}
			runner := processRunnerFunc(func(_ context.Context, spec contract.ProcessSpec) (contract.ProcessResult, error) {
				if err := os.WriteFile(spec.LogPath, []byte(test.output), 0o600); err != nil {
					return contract.ProcessResult{}, err
				}
				result := test.result
				result.LogPath = spec.LogPath
				return result, nil
			})
			_, err := BuildChildEnvironment(context.Background(), runner, EnvironmentRequest{
				Base: Environment{"PATH": bin}, RunDir: fixture.runDir, RunRoot: fixture.root,
				ProjectRoot: fixture.project, Workspace: fixture.workspace,
			})
			if err == nil {
				t.Fatal("accepted invalid mise result")
			}
		})
	}
}

func successfulResult() contract.ProcessResult {
	status := 0
	return contract.ProcessResult{ExitStatus: &status}
}
