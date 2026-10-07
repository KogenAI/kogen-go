//go:build darwin || linux

package testkit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/journal"
	"kogen-go/internal/process"
	"kogen-go/internal/recovery"
	"kogen-go/internal/recovery/preserve"
	"kogen-go/internal/safefs"
)

const adversarialRunID = "abcdef0123456789abcdef0123456789"

func TestAdversarialPublicationMatrix(t *testing.T) {
	rootPath := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "target")
	if err := os.WriteFile(target, []byte("outside stays unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(rootPath, "symlink-leaf")); err != nil {
		t.Fatal(err)
	}
	root, err := safefs.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	if err := root.Publish("symlink-leaf", []byte("replacement bytes\n"), 0o600, safefs.PublicationReplace); err != nil {
		t.Fatalf("replace symlink publication leaf: %v", err)
	}
	leafInfo, err := os.Lstat(filepath.Join(rootPath, "symlink-leaf"))
	if err != nil || !leafInfo.Mode().IsRegular() {
		t.Fatalf("replacement leaf kind = %v, %v; want regular file", leafInfo, err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "outside stays unchanged\n" {
		t.Fatalf("symlink target = %q, %v", got, err)
	}

	linked := filepath.Join(rootPath, "hardlinked-leaf")
	alias := filepath.Join(rootPath, "hardlink-alias")
	if err := os.WriteFile(linked, []byte("shared inode\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(linked, alias); err != nil {
		t.Fatal(err)
	}
	if err := root.Publish("hardlinked-leaf", []byte("must not replace\n"), 0o600, safefs.PublicationReplace); !errors.Is(err, safefs.ErrUnsafeFile) {
		t.Fatalf("hardlink publication error = %v, want ErrUnsafeFile", err)
	}
	for _, name := range []string{"hardlinked-leaf", "hardlink-alias"} {
		if got, err := os.ReadFile(filepath.Join(rootPath, name)); err != nil || string(got) != "shared inode\n" {
			t.Fatalf("hardlink %q = %q, %v", name, got, err)
		}
	}

	fifo := filepath.Join(rootPath, "fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []struct {
		name string
		call func() error
	}{
		{name: "publish", call: func() error { return root.Publish("fifo", []byte("x"), 0o600, safefs.PublicationReplace) }},
		{name: "open-read", call: func() error {
			file, err := root.OpenRead("fifo")
			if file != nil {
				_ = file.Close()
			}
			return err
		}},
	} {
		t.Run("fifo-"+operation.name, func(t *testing.T) {
			finished := make(chan error, 1)
			go func() { finished <- operation.call() }()
			select {
			case err := <-finished:
				if !errors.Is(err, safefs.ErrUnsafeFile) {
					t.Fatalf("FIFO %s error = %v, want ErrUnsafeFile", operation.name, err)
				}
			case <-time.After(time.Second):
				t.Fatalf("FIFO %s blocked", operation.name)
			}
		})
	}
}

func TestAdversarialGitIgnoreAndWorkspacePoisoning(t *testing.T) {
	fixture := NewGitFixture(t)
	git := gitio.NewWorkspace(process.Supervisor{})
	env := processEnvironment(fixture.Environment())
	policy := gitio.WorkspacePolicy(fixture.Checkout, env)

	writeAdversarialFile(t, fixture.Checkout, ".gitignore", []byte("README.md\n*.candidate\n!keep.candidate\n.hidden\n"), 0o644)
	writeAdversarialFile(t, fixture.Checkout, "README.md", []byte("tracked, even though newly ignored\n"), 0o644)
	writeAdversarialFile(t, fixture.Checkout, "drop.candidate", []byte("ignored by workspace rules\n"), 0o644)
	writeAdversarialFile(t, fixture.Checkout, "keep.candidate", []byte("negation wins\n"), 0o644)
	writeAdversarialFile(t, fixture.Checkout, ".hidden", []byte("ignored dotfile\n"), 0o644)
	writeAdversarialFile(t, fixture.Checkout, "visible.txt", []byte("outside private excludes\n"), 0o644)
	writeAdversarialFile(t, fixture.Checkout, ".gitattributes", []byte("README.md filter=poison\n"), 0o644)
	filterMarker := filepath.Join(fixture.Root, "filter-ran")
	fsmonitorMarker := filepath.Join(fixture.Root, "fsmonitor-ran")
	filter := writeAdversarialHelper(t, fixture.Root, "filter", "printf ran > \"$KOGEN_FILTER_MARKER\"\ncat\n")
	fsmonitor := writeAdversarialHelper(t, fixture.Root, "fsmonitor", "printf ran > \"$KOGEN_FSMONITOR_MARKER\"\nprintf 'dirty\\n'\n")
	fixture.Run(t, "config", "--local", "filter.poison.clean", filter)
	fixture.Run(t, "config", "--local", "filter.poison.required", "true")
	fixture.Run(t, "config", "--local", "core.fsmonitor", fsmonitor)
	fixture.Run(t, "config", "--local", "core.hooksPath", filepath.Join(fixture.Root, "malicious-hooks"))
	globalExcludes := filepath.Join(fixture.Root, "global-excludes")
	if err := os.WriteFile(globalExcludes, []byte("keep.candidate\nvisible.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.Run(t, "config", "--local", "core.excludesFile", globalExcludes)
	if err := os.WriteFile(filepath.Join(fixture.Checkout, ".git", "info", "exclude"), []byte("keep.candidate\nvisible.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env["KOGEN_FILTER_MARKER"] = filterMarker
	env["KOGEN_FSMONITOR_MARKER"] = fsmonitorMarker
	policy = gitio.WorkspacePolicy(fixture.Checkout, env)

	base := contract.ObjectID(strings.TrimSpace(string(fixture.Run(t, "rev-parse", "HEAD"))))
	tree, err := gitio.BuildCandidateTree(context.Background(), git, policy, base)
	if err != nil {
		t.Fatalf("build candidate tree with poisoned workspace Git config: %v", err)
	}
	for path, want := range map[string]string{
		"README.md":      "tracked, even though newly ignored\n",
		"keep.candidate": "negation wins\n",
		"visible.txt":    "outside private excludes\n",
		".gitattributes": "README.md filter=poison\n",
	} {
		got := gitOutput(t, git, policy, "show", string(tree)+":"+path)
		if string(got) != want {
			t.Errorf("candidate %q = %q, want %q", path, got, want)
		}
	}
	for _, path := range []string{"drop.candidate", ".hidden"} {
		if _, err := gitOutputE(git, policy, "show", string(tree)+":"+path); err == nil {
			t.Errorf("candidate retained ignored path %q", path)
		}
	}
	for _, marker := range []string{filterMarker, fsmonitorMarker} {
		if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("workspace helper ran: %s (stat error %v)", marker, err)
		}
	}
}

func TestAdversarialSigningHelperHangIsSupervised(t *testing.T) {
	fixture := NewGitFixture(t)
	marker := filepath.Join(fixture.Root, "signer-started")
	helper := writeAdversarialHelper(t, fixture.Root, "hanging-signer", "printf started > \"$KOGEN_SIGNER_MARKER\"\nwhile :; do sleep 60; done\n")
	global := filepath.Join(fixture.Root, "signing.gitconfig")
	config := fmt.Sprintf("[user]\n\tname = Kogen Signing Fixture\n\temail = signing@example.invalid\n\tsigningkey = fixture-key\n[commit]\n\tgpgsign = true\n[gpg]\n\tprogram = %s\n", helper)
	if err := os.WriteFile(global, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	env := processEnvironment(fixture.Environment())
	env["GIT_CONFIG_GLOBAL"] = global
	env["KOGEN_SIGNER_MARKER"] = marker
	policy := gitio.OriginPolicy(fixture.Checkout, env)
	policy.Timeout = time.Second
	port := gitio.NewRefPort(gitio.NewOrigin(process.Supervisor{}), policy)
	tree := contract.ObjectID(strings.TrimSpace(string(fixture.Run(t, "rev-parse", "HEAD^{tree}"))))
	parent := contract.ObjectID(strings.TrimSpace(string(fixture.Run(t, "rev-parse", "HEAD"))))

	started := time.Now()
	_, err := port.CommitTree(context.Background(), contract.CommitTreeRequest{
		Tree: tree, Parents: []contract.ObjectID{parent}, Message: []byte("supervised signer fixture\n"),
	})
	if err == nil {
		t.Fatal("CommitTree succeeded with a hanging signing helper")
	}
	var gitErr *gitio.GitError
	if !errors.As(err, &gitErr) || gitErr.Kind != gitio.GitErrorTimeout || !gitErr.TimedOut {
		t.Fatalf("CommitTree error = %v, want supervised Git timeout", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("hanging signing helper took %s to stop", elapsed)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "started" {
		t.Fatalf("signing helper marker = %q, %v; helper did not run", got, err)
	}
}

func TestAdversarialRecoveryAdoptsUnrecordedSnapshotAndKeepsLaterWork(t *testing.T) {
	fixture := NewGitFixture(t)
	stateRoot := filepath.Join(fixture.Root, "state")
	if err := os.Mkdir(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	stateRoot, err := filepath.EvalSymlinks(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSpace(string(fixture.Run(t, "rev-parse", "HEAD")))
	workspace := filepath.Join(stateRoot, adversarialRunID+"-R1")
	fixture.RunIn(t, fixture.Root, "clone", "--quiet", "--local", "--no-hardlinks", "--no-checkout", "--template=", "--", fixture.Checkout, workspace)
	fixture.RunIn(t, workspace, "checkout", "--quiet", "--detach", base)
	env := processEnvironment(fixture.Environment())
	originGit := gitio.NewOrigin(process.Supervisor{})
	preserver, err := preserve.New(preserve.Config{
		StateRoot: stateRoot, WorkspaceGit: gitio.NewWorkspace(process.Supervisor{}),
		WorkspaceEnvironment: env, OriginGit: originGit,
		OriginPolicy: gitio.OriginPolicy(fixture.Checkout, env),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer preserver.Close()
	request := recovery.PreservationRequest{
		RunID: adversarialRunID,
		Workspace: recovery.Workspace{
			Name: "R1", RelativePath: adversarialRunID + "-R1", AbsolutePath: workspace, Directory: true,
		},
		Base: contract.ObjectID(base),
	}
	writeAdversarialFile(t, workspace, "README.md", []byte("latest before first snapshot\n"), 0o644)
	writeAdversarialFile(t, workspace, "untracked.txt", []byte("first recoverable state\n"), 0o600)
	writeAdversarialFile(t, workspace, "run.sh", []byte("#!/bin/sh\nexit 0\n"), 0o755)
	if err := os.Symlink("missing-target", filepath.Join(workspace, "link")); err != nil {
		t.Fatal(err)
	}

	first, err := preserver.Preserve(context.Background(), request)
	if err != nil || first.Ref == nil || first.Tree == nil || first.Verification != "unverified" {
		t.Fatalf("first recovery snapshot = %#v, %v", first, err)
	}
	// No run record is written between calls: this is the publication/record
	// crash boundary, so recovery must adopt the existing create-only ref.
	adopted, err := preserver.Preserve(context.Background(), request)
	if err != nil || adopted.Ref == nil || *adopted.Ref != *first.Ref || adopted.Tree == nil || *adopted.Tree != *first.Tree {
		t.Fatalf("adopt unrecorded recovery snapshot = %#v, %v; first=%#v", adopted, err, first)
	}
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("later repair bytes after prior snapshot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "later.txt"), []byte("newer recoverable state\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	later, err := preserver.Preserve(context.Background(), request)
	if err != nil || later.Archive == nil || later.Verification != "unverified" {
		t.Fatalf("later recovery snapshot = %#v, %v; want distinct unverified archive", later, err)
	}
	if _, err := os.Stat(filepath.Join(stateRoot, filepath.FromSlash(*later.Archive))); err != nil {
		t.Fatalf("later snapshot archive is not durable at its recorded identity: %v", err)
	}
	refs := gitio.NewRefPort(originGit, gitio.OriginPolicy(fixture.Checkout, env))
	retained, err := refs.ReadRef(context.Background(), *first.Ref)
	if err != nil || !retained.Exists {
		t.Fatalf("prior recovery ref was removed: %+v, %v", retained, err)
	}
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("preservation removed or changed the source workspace: %v", err)
	}
}

func TestAdversarialPostCASRecoveryPreservesLaterEditsAsUnverified(t *testing.T) {
	fixture := NewGitFixture(t)
	stateRoot := filepath.Join(fixture.Root, "state")
	if err := os.Mkdir(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	stateRoot, err := filepath.EvalSymlinks(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	base := contract.ObjectID(strings.TrimSpace(string(fixture.Run(t, "rev-parse", "HEAD"))))
	workspace := filepath.Join(stateRoot, adversarialRunID+"-R1")
	fixture.RunIn(t, fixture.Root, "clone", "--quiet", "--local", "--no-hardlinks", "--no-checkout", "--template=", "--", fixture.Checkout, workspace)
	fixture.RunIn(t, workspace, "checkout", "--quiet", "--detach", string(base))

	env := processEnvironment(fixture.Environment())
	originGit := gitio.NewOrigin(process.Supervisor{})
	originPolicy := gitio.OriginPolicy(fixture.Checkout, env)
	refs := gitio.NewRefPort(originGit, originPolicy)
	baseBytes := gitOutput(t, originGit, originPolicy, "show", string(base)+":README.md")
	ctx := context.Background()
	candidateTree, err := gitio.WriteExactTree(ctx, originGit, originPolicy, []gitio.TreeFile{
		{Path: "README.md", Mode: gitio.GitModeRegular, Bytes: baseBytes},
		{Path: "landed.txt", Mode: gitio.GitModeRegular, Bytes: []byte("verified landing bytes\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := strings.TrimSpace(string(fixture.Run(t, "commit-tree", string(candidateTree), "-p", string(base), "-m", "verified candidate")))
	updated, err := refs.CompareAndSwap(ctx, contract.RefUpdate{
		Name: "refs/heads/main", Expected: base, Next: contract.ObjectID(candidate),
	})
	if err != nil || !updated.Updated {
		t.Fatalf("advance base for post-CAS fixture: result=%+v err=%v", updated, err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "later-only.txt"), []byte("post-CAS unverified progress\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(stateRoot, "runs"), 0o700); err != nil {
		t.Fatal(err)
	}
	stateRootFS, err := safefs.OpenRoot(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer stateRootFS.Close()
	store, err := journal.NewRunStore(stateRootFS, "runs/"+adversarialRunID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := journal.RunSnapshot{
		Schema: 2, RunID: adversarialRunID, Slug: "post-cas-fixture",
		ApprovalSHA256: strings.Repeat("a", 64), ApprovalCommit: string(base),
		TargetBranch: "main", Status: "running", OwnerPID: 1,
		OwnerStartedMS: 1, StartedMS: 2,
		Landing: &journal.LandingRecord{
			ApprovalCommit: string(base), RunID: adversarialRunID, ExpectedParent: string(base),
			FinalTree: string(candidateTree), CandidateCommit: candidate,
		},
	}
	if err := store.Create(snapshot); err != nil {
		t.Fatal(err)
	}
	started := journal.NewRunEvent("started", 3)
	if err := started.Set("base_sha", string(base)); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(started, snapshot); err != nil {
		t.Fatal(err)
	}
	preserver, err := preserve.New(preserve.Config{
		StateRoot: stateRoot, WorkspaceGit: gitio.NewWorkspace(process.Supervisor{}),
		WorkspaceEnvironment: env, OriginGit: originGit, OriginPolicy: originPolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer preserver.Close()
	controller, err := recovery.NewController(recovery.Config{
		StateRoot: stateRoot, Git: originGit, GitPolicy: originPolicy,
		Refs: refs, Writers: adversarialNoopWriters{}, Preserver: preserver,
		Owners: adversarialDeadOwner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()

	report, err := controller.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Runs) != 1 || report.Runs[0].Status != "landed" || report.Runs[0].Reason != "reconciled" || report.Runs[0].CleanupPending {
		t.Fatalf("post-CAS recovery result = %#v, issues=%#v", report.Runs, report.Issues)
	}
	if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preserved workspace was not cleaned after successful publication: %v", err)
	}
	current, err := store.ReadSnapshot()
	if err != nil || current.Status != "landed" || len(current.Recovery) != 1 || current.Recovery[0].Verification != "unverified" {
		t.Fatalf("post-CAS snapshot = %#v, %v", current, err)
	}
	if current.Recovery[0].Tree == nil || *current.Recovery[0].Tree == string(candidateTree) {
		t.Fatalf("post-CAS recovery tree = %v, want later unverified tree distinct from landing", current.Recovery[0].Tree)
	}
	if current.Recovery[0].Ref == nil {
		t.Fatal("post-CAS recovery record has no retained ref")
	}
	if got := string(gitOutput(t, originGit, originPolicy, "show", *current.Recovery[0].Tree+":later-only.txt")); got != "post-CAS unverified progress\n" {
		t.Fatalf("post-CAS recovered bytes = %q", got)
	}
	landed, err := refs.ReadRef(ctx, "refs/heads/main")
	if err != nil || !landed.Exists || string(landed.Target) != candidate {
		t.Fatalf("base ref after recovery = %+v, %v; wanted landed candidate %s", landed, err, candidate)
	}
}

type adversarialNoopWriters struct{}

func (adversarialNoopWriters) StopRun(context.Context, recovery.RunIdentity) error { return nil }

type adversarialDeadOwner struct{}

func (adversarialDeadOwner) Alive(context.Context, int64, int64) (bool, error) { return false, nil }

func processEnvironment(entries []string) process.Environment {
	environment := make(process.Environment, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			environment[key] = value
		}
	}
	return environment
}

func writeAdversarialFile(t *testing.T, root, name string, contents []byte, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func writeAdversarialHelper(t *testing.T, root, name, contents string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+contents), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func gitOutput(t *testing.T, git *gitio.Runner, policy contract.GitPolicy, args ...string) []byte {
	t.Helper()
	output, err := gitOutputE(git, policy, args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return output
}

func gitOutputE(git *gitio.Runner, policy contract.GitPolicy, args ...string) ([]byte, error) {
	result, err := git.Exec(context.Background(), args, nil, policy)
	if err != nil {
		return nil, err
	}
	if result.Process.ExitStatus == nil || *result.Process.ExitStatus != 0 {
		return nil, fmt.Errorf("git %s exited with status %v: %s", strings.Join(args, " "), result.Process.ExitStatus, result.StderrTail)
	}
	return result.Stdout, nil
}
