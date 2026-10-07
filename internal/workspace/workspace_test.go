package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/process"
	"kogen-go/internal/testkit"
)

func TestCreateUsesNoHardlinksEmptyTemplateAndDetachedBase(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	workspacesDir := filepath.Join(fixture.Root, "workspaces")
	if err := os.Mkdir(workspacesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSpace(string(fixture.Run(t, "rev-parse", "HEAD")))
	baseID, err := gitio.ParseObjectID(base)
	if err != nil {
		t.Fatal(err)
	}
	policy := contract.GitPolicy{
		WorkingDirectory: workspacesDir,
		Environment:      fixture.Environment(),
		Timeout:          30 * time.Second,
		StdoutLimit:      1 << 20,
	}
	git := &recordingGit{inner: gitio.NewWorkspace(process.Supervisor{})}
	factory := Factory{Git: git, Policy: policy}
	created, err := factory.Create(context.Background(), CloneRequest{
		Source: fixture.Checkout, WorkspacesDir: workspacesDir,
		RunID: "0123456789abcdef0123456789abcdef", Rung: "R1", BaseCommit: baseID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.BaseCommit != baseID || created.Rung != "R1" {
		t.Fatalf("workspace identity = %#v", created)
	}
	if len(git.calls) == 0 || !containsArgs(git.calls[0], "--local", "--no-hardlinks", "--no-checkout", "--template=", "--") {
		t.Fatalf("clone invocation is missing required isolation flags: %#v", git.calls)
	}
	if got := strings.TrimSpace(string(fixture.RunIn(t, created.Path, "rev-parse", "HEAD"))); got != base {
		t.Fatalf("workspace HEAD = %q, want %q", got, base)
	}
	if got := strings.TrimSpace(string(fixture.RunIn(t, created.Path, "rev-parse", "--abbrev-ref", "HEAD"))); got != "HEAD" {
		t.Fatalf("workspace HEAD abbreviation = %q, want detached HEAD", got)
	}

	sourceInfo, err := os.Stat(filepath.Join(fixture.Checkout, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	workspaceInfo, err := os.Stat(filepath.Join(created.Path, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	sourceStat := sourceInfo.Sys().(*syscall.Stat_t)
	workspaceStat := workspaceInfo.Sys().(*syscall.Stat_t)
	if sourceStat.Dev == workspaceStat.Dev && sourceStat.Ino == workspaceStat.Ino {
		t.Fatal("workspace shares a hardlinked checkout file")
	}
}

func TestCreateRefusesExistingWorkspaceWithoutRemovingIt(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	workspacesDir := filepath.Join(fixture.Root, "workspaces")
	if err := os.Mkdir(workspacesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	leaf := "0123456789abcdef0123456789abcdef-R1"
	if err := os.Mkdir(filepath.Join(workspacesDir, leaf), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(workspacesDir, leaf, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	base, err := gitio.ParseObjectID(strings.TrimSpace(string(fixture.Run(t, "rev-parse", "HEAD"))))
	if err != nil {
		t.Fatal(err)
	}
	factory := Factory{Git: gitio.NewWorkspace(process.Supervisor{}), Policy: contract.GitPolicy{Environment: fixture.Environment()}}
	_, err = factory.Create(context.Background(), CloneRequest{
		Source: fixture.Checkout, WorkspacesDir: workspacesDir,
		RunID: "0123456789abcdef0123456789abcdef", Rung: "R1", BaseCommit: base,
	})
	if !errors.Is(err, ErrWorkspaceExists) {
		t.Fatalf("Create error = %v, want ErrWorkspaceExists", err)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "keep" {
		t.Fatalf("existing workspace marker = %q, err=%v", got, err)
	}
}

func TestSeedDirectoryClonesOrCopiesWithoutSharingInodes(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source")
	destinationRoot := filepath.Join(root, "destination")
	for _, name := range []string{sourceRoot, destinationRoot, filepath.Join(sourceRoot, "cache", "nested")} {
		if err := os.MkdirAll(name, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	sourceFile := filepath.Join(sourceRoot, "cache", "artifact")
	if err := os.WriteFile(sourceFile, []byte("cached bytes\x00\n"), 0o751); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../artifact", filepath.Join(sourceRoot, "cache", "nested", "current")); err != nil {
		t.Fatal(err)
	}
	report, err := SeedDirectory(sourceRoot, "cache", destinationRoot, "cache")
	if err != nil {
		t.Fatal(err)
	}
	if report.Files != 1 || report.COWCopies+report.PlainCopies != 1 || report.Symlinks != 1 {
		t.Fatalf("seed report = %#v", report)
	}
	destinationFile := filepath.Join(destinationRoot, "cache", "artifact")
	got, err := os.ReadFile(destinationFile)
	if err != nil || string(got) != "cached bytes\x00\n" {
		t.Fatalf("seeded bytes = %q, err=%v", got, err)
	}
	info, err := os.Stat(destinationFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o751 {
		t.Fatalf("seeded mode = %o, want 751", info.Mode().Perm())
	}
	sourceInfo, _ := os.Stat(sourceFile)
	destinationStat := info.Sys().(*syscall.Stat_t)
	sourceStat := sourceInfo.Sys().(*syscall.Stat_t)
	if sourceStat.Dev == destinationStat.Dev && sourceStat.Ino == destinationStat.Ino {
		t.Fatal("seeded file shares a hardlinked inode with setup cache")
	}
	if err := os.WriteFile(destinationFile, []byte("workspace edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(sourceFile); err != nil || string(got) != "cached bytes\x00\n" {
		t.Fatalf("workspace edit changed setup cache: %q, err=%v", got, err)
	}
	if got, err := os.Readlink(filepath.Join(destinationRoot, "cache", "nested", "current")); err != nil || got != "../artifact" {
		t.Fatalf("seeded symlink = %q, err=%v", got, err)
	}
}

func TestSeedDirectoryRejectsEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source")
	destinationRoot := filepath.Join(root, "destination")
	for _, name := range []string{sourceRoot, destinationRoot, filepath.Join(sourceRoot, "cache")} {
		if err := os.MkdirAll(name, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../../outside", filepath.Join(sourceRoot, "cache", "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := SeedDirectory(sourceRoot, "cache", destinationRoot, "cache"); err == nil {
		t.Fatal("escaping setup symlink was accepted")
	}
}

func TestInstallApprovedWritesExactBytesAndRemovesStagedSource(t *testing.T) {
	workspaceRoot := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspaceRoot, "test"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "test", "source.exs"), []byte("staged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspaceRoot, "test", "candidate.exs")); err != nil {
		t.Fatal(err)
	}
	files, err := InstallApproved(workspaceRoot, InstallPlan{
		Files:   []ApprovedFile{{Path: "test/candidate.exs", Bytes: []byte("approved"), GitMode: gitModeExec}},
		Removes: []string{"test/source.exs"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].GitMode != gitModeExec || files[0].SHA256 == "" {
		t.Fatalf("installed identity = %#v", files)
	}
	got, err := os.ReadFile(filepath.Join(workspaceRoot, "test", "candidate.exs"))
	if err != nil || string(got) != "approved" {
		t.Fatalf("candidate bytes = %q, err=%v", got, err)
	}
	info, err := os.Stat(filepath.Join(workspaceRoot, "test", "candidate.exs"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("candidate mode = %v, err=%v", info, err)
	}
	if _, err := os.Lstat(filepath.Join(workspaceRoot, "test", "source.exs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged source remains: %v", err)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "unchanged" {
		t.Fatalf("symlink target changed: %q, err=%v", got, err)
	}
}

func TestInstallApprovedRejectsDigestAndUnsafeMetadataPaths(t *testing.T) {
	root := t.TempDir()
	if _, err := InstallApproved(root, InstallPlan{Files: []ApprovedFile{{Path: "lib/file", Bytes: []byte("x"), ExpectedSHA256: "bad"}}}); err == nil {
		t.Fatal("mismatched approved digest was accepted")
	}
	if _, err := InstallApproved(root, InstallPlan{Files: []ApprovedFile{{Path: ".git/config", Bytes: []byte("unsafe")}}}); err == nil {
		t.Fatal("Git metadata path was accepted")
	}
}

type scriptedProcessRunner struct {
	results []contract.ProcessResult
	specs   []contract.ProcessSpec
}

type recordingGit struct {
	inner contract.GitPort
	calls [][]string
}

func (g *recordingGit) Exec(ctx context.Context, args []string, stdin []byte, policy contract.GitPolicy) (contract.GitResult, error) {
	g.calls = append(g.calls, append([]string(nil), args...))
	return g.inner.Exec(ctx, args, stdin, policy)
}

func containsArgs(args []string, required ...string) bool {
	set := make(map[string]bool, len(args))
	for _, arg := range args {
		set[arg] = true
	}
	for _, arg := range required {
		if !set[arg] {
			return false
		}
	}
	return true
}

func (r *scriptedProcessRunner) Run(_ context.Context, spec contract.ProcessSpec) (contract.ProcessResult, error) {
	r.specs = append(r.specs, spec)
	if len(r.results) == 0 {
		return contract.ProcessResult{}, errors.New("no scripted result")
	}
	result := r.results[0]
	r.results = r.results[1:]
	return result, nil
}

func status(value int) *int { return &value }

func TestRunSetupRetriesOnceAndRetainsBothAttemptLogs(t *testing.T) {
	runner := &scriptedProcessRunner{results: []contract.ProcessResult{
		{ExitStatus: status(1)}, {ExitStatus: status(0)},
	}}
	outcome, err := RunSetup(context.Background(), runner, []contract.ProcessSpec{{
		Executable: "mix", Dir: t.TempDir(), LogPath: filepath.Join(t.TempDir(), "setup.log"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Attempts) != 2 || len(runner.specs) != 2 || len(outcome.Attempts[0].Results) != 1 || len(outcome.Attempts[1].Results) != 1 {
		t.Fatalf("attempt count = %d, calls=%d", len(outcome.Attempts), len(runner.specs))
	}
	if runner.specs[0].LogPath == runner.specs[1].LogPath || !strings.HasSuffix(runner.specs[0].LogPath, ".attempt-1") || !strings.HasSuffix(runner.specs[1].LogPath, ".attempt-2") {
		t.Fatalf("attempt log paths = %q, %q", runner.specs[0].LogPath, runner.specs[1].LogPath)
	}
}

func TestRunSetupFailureAndBaseAcceptanceUnavailable(t *testing.T) {
	runner := &scriptedProcessRunner{results: []contract.ProcessResult{
		{ExitStatus: status(1)}, {ExitStatus: status(127), Unavailable: true},
		{ExitStatus: status(127), Unavailable: true},
	}}
	outcome, err := RunSetup(context.Background(), runner, []contract.ProcessSpec{{
		Executable: "mix", Dir: t.TempDir(), LogPath: filepath.Join(t.TempDir(), "setup.log"),
	}})
	if !errors.Is(err, ErrSetupFailed) || len(outcome.Attempts) != 2 {
		t.Fatalf("setup outcome=%#v error=%v", outcome, err)
	}
	missing, err := RunBaseAcceptance(context.Background(), "R1", runner, contract.ProcessSpec{
		Executable: "mix", Dir: t.TempDir(), LogPath: filepath.Join(t.TempDir(), "base.log"),
	})
	if !errors.Is(err, ErrToolMissing) || missing.ExitStatus == nil || *missing.ExitStatus != 127 {
		t.Fatalf("base acceptance result=%#v error=%v", missing, err)
	}
}

func TestRunSetupRetriesTheEntireCommandSequence(t *testing.T) {
	runner := &scriptedProcessRunner{results: []contract.ProcessResult{
		{ExitStatus: status(0)}, {ExitStatus: status(1)},
		{ExitStatus: status(0)}, {ExitStatus: status(0)},
	}}
	logDir := t.TempDir()
	specs := []contract.ProcessSpec{
		{Executable: "mix", Dir: t.TempDir(), LogPath: filepath.Join(logDir, "setup-one.log")},
		{Executable: "mix", Dir: t.TempDir(), LogPath: filepath.Join(logDir, "setup-two.log")},
	}
	outcome, err := RunSetup(context.Background(), runner, specs)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Attempts) != 2 || len(outcome.Attempts[0].Results) != 2 || len(outcome.Attempts[1].Results) != 2 || len(runner.specs) != 4 {
		t.Fatalf("setup outcome=%#v calls=%d", outcome, len(runner.specs))
	}
	if !strings.HasSuffix(runner.specs[0].LogPath, ".attempt-1-step-1") || !strings.HasSuffix(runner.specs[1].LogPath, ".attempt-1-step-2") || !strings.HasSuffix(runner.specs[2].LogPath, ".attempt-2-step-1") || !strings.HasSuffix(runner.specs[3].LogPath, ".attempt-2-step-2") {
		t.Fatalf("setup logs = %#v", []string{runner.specs[0].LogPath, runner.specs[1].LogPath, runner.specs[2].LogPath, runner.specs[3].LogPath})
	}
}

func TestRunBaseAcceptanceRunsOnceOnlyOnFirstRung(t *testing.T) {
	runner := &scriptedProcessRunner{results: []contract.ProcessResult{{ExitStatus: status(1)}}}
	if _, err := RunBaseAcceptance(context.Background(), "R2", runner, contract.ProcessSpec{
		Executable: "mix", Dir: t.TempDir(), LogPath: filepath.Join(t.TempDir(), "base.log"),
	}); err == nil || len(runner.specs) != 0 {
		t.Fatalf("later rung base acceptance: error=%v calls=%d", err, len(runner.specs))
	}
	result, err := RunBaseAcceptance(context.Background(), "R1", runner, contract.ProcessSpec{
		Executable: "mix", Dir: t.TempDir(), LogPath: filepath.Join(t.TempDir(), "base.log"),
	})
	if err != nil || result.ExitStatus == nil || *result.ExitStatus != 1 || len(runner.specs) != 1 {
		t.Fatalf("red base result=%#v error=%v calls=%d", result, err, len(runner.specs))
	}
}
