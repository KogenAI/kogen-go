package gitio

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/process"
	"kogen-go/internal/testkit"
)

func TestWorkspacePolicySupervisesAndDisablesWorkspaceHelpers(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	root := t.TempDir()
	markers := filepath.Join(root, "markers")
	if err := os.Mkdir(markers, 0o700); err != nil {
		t.Fatal(err)
	}
	filterMarker := filepath.Join(root, "filter-ran")
	fsmonitorMarker := filepath.Join(root, "fsmonitor-ran")
	hookMarker := filepath.Join(root, "hook-ran")
	filter := writeHelper(t, root, "filter", "printf ran > "+shellQuote(filterMarker)+"\ncat\n")
	fsmonitor := writeHelper(t, root, "fsmonitor", "printf ran > "+shellQuote(fsmonitorMarker)+"\nprintf 'dirty\\n'\n")
	hook := writeHelper(t, root, "hook", "printf ran > "+shellQuote(hookMarker)+"\n")
	if err := os.MkdirAll(filepath.Join(fixture.Checkout, "workspace-hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.Checkout, "workspace-hooks", "pre-commit"), []byte("#!/bin/sh\n"+hook+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.Checkout, ".gitattributes"), []byte("filtered.txt filter=unsafe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.Run(t, "config", "--local", "core.hooksPath", "workspace-hooks")
	fixture.Run(t, "config", "--local", "core.fsmonitor", fsmonitor)
	fixture.Run(t, "config", "--local", "filter.unsafe.clean", filter)
	fixture.Run(t, "config", "--local", "filter.unsafe.smudge", filter)
	fixture.Run(t, "config", "--local", "filter.unsafe.required", "true")

	env := environmentMap(fixture.Environment())
	env["GIT_CONFIG_GLOBAL"] = filepath.Join(root, "poison-global-config")
	env["GIT_CONFIG_PARAMETERS"] = "core.hooksPath=" + filepath.Join(root, "outside-hooks")
	policy := WorkspacePolicy(fixture.Checkout, env)
	if got := environmentValue(policy.Environment, "GIT_CONFIG_GLOBAL"); got != "/dev/null" {
		t.Fatalf("workspace global config = %q, want /dev/null", got)
	}
	if got := environmentValue(policy.Environment, "GIT_CONFIG_PARAMETERS"); got != "" {
		t.Fatalf("workspace retained GIT_CONFIG_PARAMETERS=%q", got)
	}
	runner := NewWorkspace(process.Supervisor{})
	ctx := context.Background()

	result, err := runner.Exec(ctx, []string{"hash-object", "--stdin"}, []byte("exact input\n"), policy)
	if err != nil {
		t.Fatalf("hash-object: %v", err)
	}
	if result.Process.ExitStatus == nil || *result.Process.ExitStatus != 0 {
		t.Fatalf("hash-object exit status = %v, stderr=%q", result.Process.ExitStatus, result.StderrTail)
	}
	content := []byte("exact input\n")
	header := []byte(fmt.Sprintf("blob %d\x00", len(content)))
	objectBytes := append(header, content...)
	objectHash := sha1.Sum(objectBytes)
	want := hex.EncodeToString(objectHash[:])
	if got := strings.TrimSpace(string(result.Stdout)); got != want {
		t.Fatalf("hash-object returned %q, want raw-byte hash %q", got, want)
	}
	if _, err := os.Stat(filterMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace filter ran: stat error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.Checkout, "filtered.txt"), []byte("unfiltered workspace file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	added, err := runner.Exec(ctx, []string{"add", "--", "filtered.txt"}, nil, policy)
	if err != nil || added.Process.ExitStatus == nil || *added.Process.ExitStatus != 0 {
		t.Fatalf("controlled git add = status %v, stderr %q, error %v", added.Process.ExitStatus, added.StderrTail, err)
	}
	if _, err := os.Stat(filterMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("git add ran workspace filter: stat error = %v", err)
	}
	if _, err := runner.Exec(ctx, []string{"hash-object", "--filters", "--stdin", "--path=filtered.txt"}, []byte("exact input\n"), policy); err == nil {
		t.Fatal("controlled hash-object accepted filter execution")
	}

	status, err := runner.Exec(ctx, []string{"status", "--short"}, nil, policy)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Process.ExitStatus == nil || *status.Process.ExitStatus != 0 {
		t.Fatalf("status exit status = %v, stderr=%q", status.Process.ExitStatus, status.StderrTail)
	}
	if _, err := os.Stat(fsmonitorMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace fsmonitor ran: stat error = %v", err)
	}

	commit, err := runner.Exec(ctx, []string{"commit", "--allow-empty", "-F", "-"}, []byte("controlled workspace commit\n"), policy)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if commit.Process.ExitStatus == nil || *commit.Process.ExitStatus != 0 {
		t.Fatalf("commit exit status = %v, stderr=%q", commit.Process.ExitStatus, commit.StderrTail)
	}
	if _, err := os.Stat(hookMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace hook ran: stat error = %v", err)
	}
}

func TestRunnerSeparatesStreamsAndTimesOutHangingGit(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	fakeBin := t.TempDir()
	startedPath := filepath.Join(t.TempDir(), "git-started")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	fakeGit := writeHelper(t, fakeBin, "git", "for arg do if [ \"$arg\" = config ]; then exec \"$REAL_GIT\" \"$@\"; fi; done\nprintf 'stdout-before-hang\\n'; printf 'stderr-before-hang\\n' >&2; printf ran > "+shellQuote(startedPath)+"; exec sleep 20\n")
	if err := os.Chmod(fakeGit, 0o700); err != nil {
		t.Fatal(err)
	}
	env := environmentMap(fixture.Environment())
	env["PATH"] = fakeBin + string(os.PathListSeparator) + env["PATH"]
	env["REAL_GIT"] = realGit
	policy := WorkspacePolicy(fixture.Checkout, env)
	policy.Timeout = time.Second

	started := time.Now()
	result, err := NewWorkspace(process.Supervisor{}).Exec(context.Background(), []string{"status"}, nil, policy)
	var gitErr *GitError
	if !errors.As(err, &gitErr) || gitErr.Kind != GitErrorTimeout || !gitErr.TimedOut {
		status := -1
		if result.Process.ExitStatus != nil {
			status = *result.Process.ExitStatus
		}
		t.Fatalf("hanging Git error = %#v, want typed timeout; status=%d process_tail=%q", err, status, result.Process.OutputTail)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("hanging Git stopped after %s, want under 3s including the supervised config probe", elapsed)
	}
	if got, want := string(result.Stdout), "stdout-before-hang\n"; got != want {
		t.Fatalf("captured stdout = %q, want %q; stderr=%q", got, want, result.StderrTail)
	}
	if got, want := string(result.StderrTail), "stderr-before-hang\n"; got != want {
		t.Fatalf("captured stderr tail = %q, want %q", got, want)
	}
	if _, err := os.Stat(startedPath); err != nil {
		t.Fatalf("fake Git did not start: %v", err)
	}
}

func TestOriginPolicyUsesGlobalIdentityAndSigningHelperNotLocalHelper(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	root := t.TempDir()
	globalConfig := filepath.Join(root, "global.gitconfig")
	localHelperRan := filepath.Join(root, "local-helper-ran")
	globalHelperRan := filepath.Join(root, "global-helper-ran")
	localHelper := writeHelper(t, root, "local-gpg", "printf ran > "+shellQuote(localHelperRan)+"\nexit 0\n")
	globalHelper := writeHelper(t, root, "global-gpg", "printf ran > "+shellQuote(globalHelperRan)+"\nexec sleep 20\n")
	contents := fmt.Sprintf("[user]\n\tname = Global User\n\temail = global@example.invalid\n\tsigningkey = test-key\n[commit]\n\tgpgsign = true\n[gpg]\n\tprogram = %s\n", globalHelper)
	if err := os.WriteFile(globalConfig, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.Run(t, "config", "--local", "gpg.program", localHelper)
	fixture.Run(t, "config", "--local", "commit.gpgsign", "false")
	tree := strings.TrimSpace(string(fixture.Run(t, "rev-parse", "HEAD^{tree}")))
	env := environmentMap(fixture.Environment())
	env["GIT_CONFIG_GLOBAL"] = globalConfig
	policy := OriginPolicy(fixture.Checkout, env)
	policy.Timeout = time.Second
	originRunner := NewOrigin(process.Supervisor{})
	setting, settingErr := originRunner.Exec(context.Background(), []string{"config", "--get", "commit.gpgsign"}, nil, policy)
	if settingErr != nil || setting.Process.ExitStatus == nil || *setting.Process.ExitStatus != 0 || strings.TrimSpace(string(setting.Stdout)) != "true" {
		t.Fatalf("global commit.gpgsign setting = status %v, output %q, error %v", setting.Process.ExitStatus, setting.Stdout, settingErr)
	}
	program, programErr := originRunner.Exec(context.Background(), []string{"config", "--get", "gpg.program"}, nil, policy)
	if programErr != nil || program.Process.ExitStatus == nil || *program.Process.ExitStatus != 0 || strings.TrimSpace(string(program.Stdout)) != globalHelper {
		t.Fatalf("global gpg.program setting = status %v, output %q, error %v", program.Process.ExitStatus, program.Stdout, programErr)
	}

	started := time.Now()
	_, err := originRunner.Exec(context.Background(), []string{"commit-tree", tree}, []byte("signed commit message\n"), policy)
	var gitErr *GitError
	if !errors.As(err, &gitErr) || gitErr.Kind != GitErrorTimeout || !gitErr.TimedOut {
		t.Fatalf("signing helper error = %#v, want typed timeout", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("signing helper stopped after %s, want under 3s", elapsed)
	}
	if _, err := os.Stat(globalHelperRan); err != nil {
		t.Fatalf("global signing helper was not run: %v", err)
	}
	if _, err := os.Stat(localHelperRan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository-local helper ran: stat error = %v", err)
	}
}

func TestRefPortCreatesCommitsAndUsesCompareAndSwap(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	policy := WorkspacePolicy(fixture.Checkout, environmentMap(fixture.Environment()))
	refs := NewRefPort(NewWorkspace(process.Supervisor{}), policy)
	ctx := context.Background()
	format, err := refs.ObjectFormat(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if format != ObjectFormatSHA1 {
		t.Fatalf("fixture format = %q, want sha1", format)
	}
	parent, err := refs.ResolveCommit(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := refs.ResolveTree(ctx, string(parent))
	if err != nil {
		t.Fatal(err)
	}
	created, err := refs.CommitTree(ctx, contract.CommitTreeRequest{
		Tree: tree, Parents: []contract.ObjectID{parent}, Message: []byte("ref port candidate\n\nKogen-Test: true\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := "refs/kogen/candidates/0123456789abcdef0123456789abcdef/r1"
	update, err := refs.CompareAndSwap(ctx, contract.RefUpdate{Name: ref, Next: created})
	if err != nil || !update.Updated {
		t.Fatalf("create-only ref update = %+v, %v", update, err)
	}
	read, err := refs.ReadRef(ctx, ref)
	if err != nil || !read.Exists || read.Target != created {
		t.Fatalf("read created ref = %+v, %v", read, err)
	}
	ancestor, err := refs.IsAncestor(ctx, parent, created)
	if err != nil || !ancestor {
		t.Fatalf("parent ancestry = %t, %v", ancestor, err)
	}
	_, err = refs.CompareAndSwap(ctx, contract.RefUpdate{Name: ref, Next: parent})
	var conflict *GitError
	if !errors.As(err, &conflict) || conflict.Kind != GitErrorRefConflict || !errors.Is(err, ErrRefConflict) {
		t.Fatalf("stale CAS error = %#v, want typed conflict", err)
	}
	removed, err := refs.DeleteRefCAS(ctx, ref, created)
	if err != nil || !removed.Updated {
		t.Fatalf("CAS delete = %+v, %v", removed, err)
	}
	missing, err := refs.ReadRef(ctx, ref)
	if err != nil || missing.Exists {
		t.Fatalf("deleted ref observation = %+v, %v", missing, err)
	}
}

func TestObjectIDRequiresFullSHA1OrSHA256(t *testing.T) {
	for _, value := range []string{
		strings.Repeat("a", 40), strings.Repeat("b", 64),
	} {
		if _, err := ParseObjectID(value); err != nil {
			t.Errorf("ParseObjectID(%q): %v", value, err)
		}
	}
	for _, value := range []string{"abc123", strings.Repeat("a", 39), strings.Repeat("a", 63), strings.Repeat("A", 64), strings.Repeat("z", 64)} {
		if _, err := ParseObjectID(value); err == nil {
			t.Errorf("ParseObjectID(%q) unexpectedly succeeded", value)
		}
	}
}

func TestRefPortSupportsSHA256ObjectFormat(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	root := filepath.Join(t.TempDir(), "sha256")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.RunIn(t, root, "init", "--quiet", "--object-format=sha256", "--initial-branch=main")
	fixture.RunIn(t, root, "config", "user.name", "SHA256 Fixture")
	fixture.RunIn(t, root, "config", "user.email", "sha256@example.invalid")
	fixture.RunIn(t, root, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("sha256 fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.RunIn(t, root, "add", "README.md")
	fixture.RunIn(t, root, "commit", "--quiet", "-m", "seed")
	policy := WorkspacePolicy(root, environmentMap(fixture.Environment()))
	refs := NewRefPort(NewWorkspace(process.Supervisor{}), policy)
	format, err := refs.ObjectFormat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if format != ObjectFormatSHA256 {
		t.Fatalf("object format = %q, want sha256", format)
	}
	commit, err := refs.ResolveCommit(context.Background(), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(commit) != 64 {
		t.Fatalf("resolved SHA-256 commit id has %d characters", len(commit))
	}
}

func TestExecRejectsPolicyOverridesAndOversizedArgv(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	policy := WorkspacePolicy(fixture.Checkout, environmentMap(fixture.Environment()))
	runner := NewWorkspace(process.Supervisor{})
	for _, args := range [][]string{{"-c", "core.hooksPath=/tmp"}, {"status", "-c", "core.fsmonitor=true"}, {"status", "--git-dir=/tmp/untrusted"}} {
		if _, err := runner.Exec(context.Background(), args, nil, policy); err == nil {
			t.Errorf("Exec(%q) unexpectedly accepted a policy override", args)
		}
	}
	if _, err := runner.Exec(context.Background(), []string{"status", strings.Repeat("x", 4097)}, nil, policy); err == nil {
		t.Fatal("Exec accepted an argv element over 4 KiB")
	}
}

func environmentMap(entries []string) process.Environment {
	result := make(process.Environment, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			result[key] = value
		}
	}
	return result
}

func environmentValue(entries []string, key string) string {
	for _, entry := range entries {
		name, value, ok := strings.Cut(entry, "=")
		if ok && name == key {
			return value
		}
	}
	return ""
}

func writeHelper(t *testing.T, directory, name, body string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
