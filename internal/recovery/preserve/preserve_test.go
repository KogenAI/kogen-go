package preserve

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/journal"
	"kogen-go/internal/process"
	"kogen-go/internal/recovery"
	"kogen-go/internal/safefs"
	"kogen-go/internal/testkit"
)

const preserveTestRunID = "0123456789abcdef0123456789abcdef"

func TestPreserveCapturesLatestBaseRelativeBytesModesAndSymlinks(t *testing.T) {
	h := newHarness(t, nil, nil, nil)
	defer h.close(t)
	write(t, h.checkout, "tracked.txt", []byte("tracked base\n"), 0o644)
	write(t, h.checkout, "deleted.txt", []byte("delete me\n"), 0o644)
	write(t, h.checkout, "mode.sh", []byte("#!/bin/sh\n"), 0o644)
	write(t, h.checkout, ".gitignore", []byte("ignored.dat\n"), 0o644)
	h.fixture.RunIn(t, h.checkout, "add", "tracked.txt", "deleted.txt", "mode.sh", ".gitignore")
	h.fixture.RunIn(t, h.checkout, "commit", "--quiet", "-m", "prepare recovery base")
	h.base = strings.TrimSpace(string(h.fixture.RunIn(t, h.checkout, "rev-parse", "HEAD")))
	h.replaceWorkspaceAtBase(t)

	write(t, h.workspace, "tracked.txt", []byte("latest tracked edit\x00\xff\n"), 0o644)
	if err := os.Remove(filepath.Join(h.workspace, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, h.workspace, "new.txt", []byte("untracked nonignored\n"), 0o600)
	write(t, h.workspace, "ignored.dat", []byte("ignored by Git\n"), 0o600)
	if err := os.Chmod(filepath.Join(h.workspace, "mode.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing-target", filepath.Join(h.workspace, "link")); err != nil {
		t.Fatal(err)
	}

	record, err := h.preserver.Preserve(context.Background(), h.request())
	if err != nil {
		t.Fatalf("preserve latest workspace: %v", err)
	}
	if record.Ref == nil || record.Tree == nil || record.Archive != nil || record.Verification != "unverified" {
		t.Fatalf("preservation record = %#v, want unverified ref/tree", record)
	}
	ref, err := h.refs.ReadRef(context.Background(), *record.Ref)
	if err != nil || !ref.Exists {
		t.Fatalf("read preserved ref = %+v, %v", ref, err)
	}
	tree, err := h.refs.ResolveTree(context.Background(), string(ref.Target))
	if err != nil || string(tree) != *record.Tree {
		t.Fatalf("preserved tree = %q, %v; record tree %q", tree, err, *record.Tree)
	}
	entries := h.treeEntries(t, tree)
	if !bytes.Equal(entries["tracked.txt"].bytes, []byte("latest tracked edit\x00\xff\n")) {
		t.Fatalf("tracked edit = %q", entries["tracked.txt"].bytes)
	}
	if _, exists := entries["deleted.txt"]; exists {
		t.Fatal("tracked deletion was not preserved")
	}
	if string(entries["new.txt"].bytes) != "untracked nonignored\n" {
		t.Fatal("untracked nonignored file was not preserved")
	}
	if _, exists := entries["ignored.dat"]; exists {
		t.Fatal("untracked ignored file was included in candidate tree")
	}
	if entries["mode.sh"].mode != "100755" {
		t.Fatalf("executable mode = %q, want 100755", entries["mode.sh"].mode)
	}
	if entries["link"].mode != "120000" || string(entries["link"].bytes) != "missing-target" {
		t.Fatalf("symlink entry = %#v", entries["link"])
	}
	if contents, err := os.ReadFile(filepath.Join(h.workspace, "tracked.txt")); err != nil || !bytes.Equal(contents, []byte("latest tracked edit\x00\xff\n")) {
		t.Fatalf("preservation altered the workspace: %q, %v", contents, err)
	}
}

func TestCreateOnlyRefAdoptionAndLaterWorkGetSeparateArchiveIdentity(t *testing.T) {
	h := newHarness(t, nil, nil, nil)
	defer h.close(t)
	write(t, h.workspace, "progress.txt", []byte("first snapshot\n"), 0o600)

	first, err := h.preserver.Preserve(context.Background(), h.request())
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := h.preserver.Preserve(context.Background(), h.request())
	if err != nil || !sameRecord(first, repeated) {
		t.Fatalf("adopt unrecorded ref: first=%#v repeat=%#v err=%v", first, repeated, err)
	}
	oldRef, err := h.refs.ReadRef(context.Background(), *first.Ref)
	if err != nil || !oldRef.Exists {
		t.Fatalf("read first ref: %+v, %v", oldRef, err)
	}

	write(t, h.workspace, "progress.txt", []byte("later different bytes\n"), 0o600)
	later, err := h.preserver.Preserve(context.Background(), h.request())
	if err != nil {
		t.Fatalf("preserve later work separately: %v", err)
	}
	if later.Archive == nil || later.Ref != nil || later.Tree != nil {
		t.Fatalf("later snapshot = %#v, want distinct archive", later)
	}
	after, err := h.refs.ReadRef(context.Background(), *first.Ref)
	if err != nil || !after.Exists || after.Target != oldRef.Target {
		t.Fatalf("earlier snapshot ref was replaced: before=%+v after=%+v err=%v", oldRef, after, err)
	}
	archivePath := filepath.Join(h.stateRoot, filepath.FromSlash(*later.Archive))
	archiveBytes, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read durable fallback archive: %v", err)
	}
	if err := verifyArchive(archiveBytes, h.request(), "candidate_tree", 0); err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(archivePath)
	if err != nil || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("archive permissions = %v, %v; want 0600", fileInfo, err)
	}
	directoryInfo, err := os.Stat(filepath.Dir(archivePath))
	if err != nil || directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("archive directory permissions = %v, %v; want 0700", directoryInfo, err)
	}
	laterAgain, err := h.preserver.Preserve(context.Background(), h.request())
	if err != nil || !sameRecord(later, laterAgain) {
		t.Fatalf("adopt archive after record-publication crash: record=%#v repeat=%#v err=%v", later, laterAgain, err)
	}
}

func TestRefPublicationFailureFallsBackToAdoptableLosslessArchive(t *testing.T) {
	h := newHarness(t, nil, &failingCASRefs{}, nil)
	defer h.close(t)
	contents := []byte("archive fallback\x00with binary\xff\n")
	write(t, h.workspace, "private.bin", contents, 0o755)
	if err := os.Symlink("../outside", filepath.Join(h.workspace, "link")); err != nil {
		t.Fatal(err)
	}

	first, err := h.preserver.Preserve(context.Background(), h.request())
	if err != nil {
		t.Fatal(err)
	}
	if first.Archive == nil || first.Ref != nil || first.Tree != nil {
		t.Fatalf("fallback record = %#v, want archive-only identity", first)
	}
	repeated, err := h.preserver.Preserve(context.Background(), h.request())
	if err != nil || !sameRecord(first, repeated) {
		t.Fatalf("retry did not adopt deterministic archive: first=%#v repeat=%#v err=%v", first, repeated, err)
	}
	archiveBytes, err := os.ReadFile(filepath.Join(h.stateRoot, filepath.FromSlash(*first.Archive)))
	if err != nil {
		t.Fatal(err)
	}
	manifest, data, err := unpackArchive(archiveBytes)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Kind != "candidate_tree" || manifest.Base != h.base {
		t.Fatalf("archive manifest identity = %#v", manifest)
	}
	var foundBinary, foundLink bool
	for index, entry := range manifest.Entries {
		name, err := base64.RawStdEncoding.DecodeString(entry.PathBase64)
		if err != nil {
			t.Fatal(err)
		}
		start := data.offsets[index]
		end := start + int(entry.Size)
		if string(name) == "private.bin" {
			foundBinary = bytes.Equal(data.content[start:end], contents) && entry.Mode == gitio.GitModeExecutable
		}
		if string(name) == "link" {
			foundLink = string(data.content[start:end]) == "../outside" && entry.Mode == gitio.GitModeSymlink
		}
	}
	if !foundBinary || !foundLink {
		t.Fatalf("archive omitted latest bytes or symlink: binary=%t symlink=%t", foundBinary, foundLink)
	}
}

func TestGitTreeFailureStillArchivesWorkspaceWithoutLosingIgnoredFiles(t *testing.T) {
	h := newHarness(t, gitFailurePort{message: "injected workspace Git failure"}, nil, nil)
	defer h.close(t)
	write(t, h.workspace, ".gitignore", []byte("ignored.txt\n"), 0o600)
	write(t, h.workspace, "ignored.txt", []byte("kept despite Git failure\n"), 0o600)
	record, err := h.preserver.Preserve(context.Background(), h.request())
	if err != nil {
		t.Fatalf("raw workspace archive fallback: %v", err)
	}
	if record.Archive == nil {
		t.Fatalf("fallback record = %#v, want archive", record)
	}
	archive, err := os.ReadFile(filepath.Join(h.stateRoot, filepath.FromSlash(*record.Archive)))
	if err != nil {
		t.Fatal(err)
	}
	manifest, data, err := unpackArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Kind != "raw_workspace" {
		t.Fatalf("archive kind = %q", manifest.Kind)
	}
	var found bool
	for index, entry := range manifest.Entries {
		name, _ := base64.RawStdEncoding.DecodeString(entry.PathBase64)
		if string(name) == "ignored.txt" {
			start := data.offsets[index]
			if string(data.content[start:start+int(entry.Size)]) == "kept despite Git failure\n" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("raw fallback lost an ignored file after candidate Git failed")
	}
}

func TestBothPublicationFailuresLeaveWorkspaceAndPriorCandidateRef(t *testing.T) {
	h := newHarness(t, nil, &failingCASRefs{}, failingArchiveStore{})
	defer h.close(t)
	write(t, h.workspace, "progress.txt", []byte("recoverable\n"), 0o600)
	priorTree, err := gitio.WriteExactTree(context.Background(), h.originGit, h.originPolicy, []gitio.TreeFile{{
		Path: "prior.txt", Mode: gitio.GitModeRegular, Bytes: []byte("prior\n"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	priorRef := "refs/kogen/candidates/" + preserveTestRunID + "/R0"
	if _, err := gitio.NewRefPort(h.originGit, h.originPolicy).CompareAndSwap(context.Background(), contract.RefUpdate{Name: priorRef, Next: priorTree}); err != nil {
		t.Fatal(err)
	}
	before, err := h.refs.ReadRef(context.Background(), priorRef)
	if err != nil || !before.Exists {
		t.Fatalf("read previous candidate: %+v, %v", before, err)
	}

	if _, err := h.preserver.Preserve(context.Background(), h.request()); err == nil {
		t.Fatal("preserve unexpectedly succeeded when ref and archive publication both failed")
	}
	if _, err := os.Stat(h.workspace); err != nil {
		t.Fatalf("workspace was not retained after both failures: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(h.workspace, "progress.txt"))
	if err != nil || string(contents) != "recoverable\n" {
		t.Fatalf("latest bytes changed after publication failures: %q, %v", contents, err)
	}
	after, err := h.refs.ReadRef(context.Background(), priorRef)
	if err != nil || !after.Exists || after.Target != before.Target {
		t.Fatalf("previous candidate ref changed: before=%+v after=%+v err=%v", before, after, err)
	}
}

func TestControllerRetainsPendingWorkspaceOnDualFailureAndRetriesTerminalCleanup(t *testing.T) {
	h := newHarness(t, nil, &failingCASRefs{}, failingArchiveStore{})
	defer h.close(t)
	write(t, h.workspace, "progress.txt", []byte("terminal recovery bytes\n"), 0o600)

	priorTree, err := gitio.WriteExactTree(context.Background(), h.originGit, h.originPolicy, []gitio.TreeFile{{
		Path: "prior.txt", Mode: gitio.GitModeRegular, Bytes: []byte("prior candidate\n"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	priorRef := "refs/kogen/candidates/" + preserveTestRunID + "/R0"
	if _, err := h.refs.CompareAndSwap(context.Background(), contract.RefUpdate{Name: priorRef, Next: priorTree}); err != nil {
		t.Fatal(err)
	}

	if err := os.Mkdir(filepath.Join(h.stateRoot, "runs"), 0o700); err != nil {
		t.Fatal(err)
	}
	stateRoot, err := safefs.OpenRoot(h.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer stateRoot.Close()
	store, err := journal.NewRunStore(stateRoot, "runs/"+preserveTestRunID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := journal.RunSnapshot{
		Schema: 2, RunID: preserveTestRunID, Slug: "terminal-recovery",
		ApprovalSHA256: strings.Repeat("1", 64), ApprovalCommit: h.base,
		TargetBranch: "main", Status: "running", OwnerPID: int64(os.Getpid()),
		OwnerStartedMS: 1000, StartedMS: 2000,
	}
	if err := store.Create(snapshot); err != nil {
		t.Fatal(err)
	}
	started := journal.NewRunEvent("started", 2001)
	if err := started.Set("base_sha", h.base); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(started, snapshot); err != nil {
		t.Fatal(err)
	}
	controller, err := recovery.NewController(recovery.Config{
		StateRoot: h.stateRoot, Git: gitFailurePort{message: "claim should not exist"},
		GitPolicy: contract.GitPolicy{WorkingDirectory: h.stateRoot},
		Refs:      h.preserver.refs, Writers: noOpWriters{}, Preserver: h.preserver,
		Owners: deadOwner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()

	first, err := controller.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Runs) != 1 || !first.Runs[0].CleanupPending || first.Runs[0].Status != "failed" || first.Runs[0].Reason != "crashed" {
		t.Fatalf("dual failure did not retain terminal pending state: %#v", first)
	}
	if _, err := os.Stat(h.workspace); err != nil {
		t.Fatalf("workspace removed after dual publication failure: %v", err)
	}
	failedSnapshot, err := store.ReadSnapshot()
	if err != nil || failedSnapshot.Status != "failed" || !failedSnapshot.CleanupPending || len(failedSnapshot.Recovery) != 0 {
		t.Fatalf("failed recovery snapshot = %#v, %v", failedSnapshot, err)
	}
	priorAfter, err := h.refs.ReadRef(context.Background(), priorRef)
	if err != nil || !priorAfter.Exists || priorAfter.Target != priorTree {
		t.Fatalf("prior sole candidate changed: %+v, %v", priorAfter, err)
	}

	// The terminal outcome is fixed. A later recovery retries preservation and
	// cleanup, using the real safefs create-only archive implementation.
	h.preserver.archives = &rootedArchiveStore{root: h.preserver.root}
	second, err := controller.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Runs) != 1 || second.Runs[0].Status != "failed" || second.Runs[0].Reason != "crashed" || !second.Runs[0].Retried || second.Runs[0].CleanupPending {
		t.Fatalf("terminal retry changed status or remained pending: %#v", second)
	}
	if _, err := os.Stat(h.workspace); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("successfully preserved workspace remains: %v", err)
	}
	finishedSnapshot, err := store.ReadSnapshot()
	if err != nil || finishedSnapshot.Status != "failed" || finishedSnapshot.CleanupPending || len(finishedSnapshot.Recovery) != 1 || finishedSnapshot.Recovery[0].Archive == nil {
		t.Fatalf("successful retry snapshot = %#v, %v", finishedSnapshot, err)
	}
	if after, err := h.refs.ReadRef(context.Background(), priorRef); err != nil || !after.Exists || after.Target != priorTree {
		t.Fatalf("previous candidate changed during retry: %+v, %v", after, err)
	}
	events, err := store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	var finished, cleanupFailures, preserved int
	for _, event := range events {
		switch event.Event {
		case "finished":
			finished++
		case "cleanup_failure":
			cleanupFailures++
		case "recovery_preserved":
			preserved++
		}
	}
	if finished != 1 || cleanupFailures != 1 || preserved != 1 {
		t.Fatalf("terminal/preservation event counts finished=%d cleanup_failure=%d recovery_preserved=%d", finished, cleanupFailures, preserved)
	}
}

func TestArchiveEncodingIncludesManifestAndChecksum(t *testing.T) {
	h := newHarness(t, nil, nil, nil)
	defer h.close(t)
	archive, err := encodeArchive(h.request(), "candidate_tree", strings.Repeat("a", 40), []gitio.TreeFile{{
		Path: "data.bin", Mode: gitio.GitModeRegular, Bytes: []byte{0, 1, 255},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyArchive(archive, h.request(), "candidate_tree", 1); err != nil {
		t.Fatal(err)
	}
	archive[len(archive)-1] ^= 1
	if _, _, err := unpackArchive(archive); err == nil {
		t.Fatal("archive checksum corruption was not detected")
	}
}

type harness struct {
	fixture      *testkit.GitFixture
	stateRoot    string
	checkout     string
	workspace    string
	base         string
	env          process.Environment
	workspaceGit contract.GitPort
	originGit    contract.GitPort
	originPolicy contract.GitPolicy
	refs         RefEffects
	preserver    *Preserver
}

func newHarness(t *testing.T, workspaceGit contract.GitPort, refs RefEffects, archives ArchiveStore) *harness {
	t.Helper()
	fixture := testkit.NewGitFixture(t)
	stateRoot := filepath.Join(fixture.Root, "state")
	if err := os.Mkdir(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	var err error
	stateRoot, err = filepath.EvalSymlinks(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSpace(string(fixture.RunIn(t, fixture.Checkout, "rev-parse", "HEAD")))
	workspace := filepath.Join(stateRoot, preserveTestRunID+"-R1")
	fixture.RunIn(t, fixture.Root, "clone", "--quiet", "--local", "--no-hardlinks", "--no-checkout", "--template=", "--", fixture.Checkout, workspace)
	fixture.RunIn(t, workspace, "checkout", "--quiet", "--detach", base)
	env := environment(fixture.Environment())
	if workspaceGit == nil {
		workspaceGit = gitio.NewWorkspace(process.Supervisor{})
	}
	originGit := gitio.NewOrigin(process.Supervisor{})
	originPolicy := gitio.OriginPolicy(fixture.Checkout, env)
	originRefs := gitio.NewRefPort(originGit, originPolicy)
	publishRefs := refs
	if publishRefs == nil {
		publishRefs = originRefs
	}
	if injected, ok := publishRefs.(*failingCASRefs); ok && injected.delegate == nil {
		injected.delegate = originRefs
	}
	preserver, err := New(Config{
		StateRoot: stateRoot, WorkspaceGit: workspaceGit, WorkspaceEnvironment: env,
		OriginGit: originGit, OriginPolicy: originPolicy, Refs: publishRefs, Archives: archives,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{
		fixture: fixture, stateRoot: stateRoot, checkout: fixture.Checkout,
		workspace: workspace, base: base, env: env, workspaceGit: workspaceGit,
		originGit: originGit, originPolicy: originPolicy, refs: originRefs, preserver: preserver,
	}
}

func (h *harness) close(t *testing.T) {
	t.Helper()
	if err := h.preserver.Close(); err != nil {
		t.Errorf("close preserver: %v", err)
	}
}

func (h *harness) request() recovery.PreservationRequest {
	return recovery.PreservationRequest{
		RunID: preserveTestRunID,
		Workspace: recovery.Workspace{
			Name: "R1", RelativePath: preserveTestRunID + "-R1",
			AbsolutePath: h.workspace, Directory: true,
		},
		Base: contract.ObjectID(h.base),
	}
}

func (h *harness) replaceWorkspaceAtBase(t *testing.T) {
	t.Helper()
	if err := os.RemoveAll(h.workspace); err != nil {
		t.Fatal(err)
	}
	h.fixture.RunIn(t, h.fixture.Root, "clone", "--quiet", "--local", "--no-hardlinks", "--no-checkout", "--template=", "--", h.checkout, h.workspace)
	h.fixture.RunIn(t, h.workspace, "checkout", "--quiet", "--detach", h.base)
}

func (h *harness) treeEntries(t *testing.T, tree contract.ObjectID) map[string]treeEntry {
	t.Helper()
	result, err := h.originGit.Exec(context.Background(), []string{"ls-tree", "-r", "-z", "--full-tree", string(tree)}, nil, h.originPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireSuccess("inspect test recovery tree", result); err != nil {
		t.Fatal(err)
	}
	entries := make(map[string]treeEntry)
	for _, row := range splitNUL(result.Stdout) {
		metadata, name, ok := bytes.Cut(row, []byte{'\t'})
		if !ok {
			t.Fatalf("malformed test tree row %q", row)
		}
		fields := strings.Fields(string(metadata))
		blob, err := h.originGit.Exec(context.Background(), []string{"cat-file", "blob", fields[2]}, nil, h.originPolicy)
		if err != nil {
			t.Fatalf("read test recovery blob %q: %v", name, err)
		}
		if err := requireSuccess("read test recovery blob", blob); err != nil {
			t.Fatal(err)
		}
		entries[string(name)] = treeEntry{mode: fields[0], bytes: blob.Stdout}
	}
	return entries
}

type treeEntry struct {
	mode  string
	bytes []byte
}

func environment(entries []string) process.Environment {
	result := make(process.Environment)
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			result[key] = value
		}
	}
	return result
}

func write(t *testing.T, root, name string, contents []byte, mode os.FileMode) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, contents, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filename, mode); err != nil {
		t.Fatal(err)
	}
}

func sameRecord(left, right journal.RecoveryRecord) bool {
	return left.Workspace == right.Workspace && left.Base == right.Base &&
		stringValue(left.Tree) == stringValue(right.Tree) && stringValue(left.Ref) == stringValue(right.Ref) &&
		stringValue(left.Archive) == stringValue(right.Archive) && left.Verification == right.Verification
}

func stringValue(value *string) string {
	if value == nil {
		return "<nil>"
	}
	return *value
}

type failingCASRefs struct{ delegate RefEffects }

func (r *failingCASRefs) ReadRef(ctx context.Context, name string) (contract.RefObservation, error) {
	return r.delegate.ReadRef(ctx, name)
}
func (r *failingCASRefs) ResolveTree(ctx context.Context, revision string) (contract.ObjectID, error) {
	return r.delegate.ResolveTree(ctx, revision)
}
func (r *failingCASRefs) IsAncestor(ctx context.Context, ancestor, descendant contract.ObjectID) (bool, error) {
	return r.delegate.IsAncestor(ctx, ancestor, descendant)
}
func (*failingCASRefs) CompareAndSwap(context.Context, contract.RefUpdate) (contract.RefUpdateResult, error) {
	return contract.RefUpdateResult{}, errors.New("injected create-only ref publication failure")
}

type failingArchiveStore struct{}

func (failingArchiveStore) Read(string) ([]byte, error) { return nil, fs.ErrNotExist }
func (failingArchiveStore) Publish(string, []byte) error {
	return errors.New("injected private archive publication failure")
}

type gitFailurePort struct{ message string }

func (g gitFailurePort) Exec(context.Context, []string, []byte, contract.GitPolicy) (contract.GitResult, error) {
	return contract.GitResult{}, errors.New(g.message)
}

type noOpWriters struct{}

func (noOpWriters) StopRun(context.Context, recovery.RunIdentity) error { return nil }

type deadOwner struct{}

func (deadOwner) Alive(context.Context, int64, int64) (bool, error) { return false, nil }

type archiveData struct {
	content []byte
	offsets []int
}

func verifyArchive(encoded []byte, request recovery.PreservationRequest, kind string, count int) error {
	manifest, _, err := unpackArchive(encoded)
	if err != nil {
		return err
	}
	if manifest.Schema != 1 || manifest.Kind != kind || manifest.RunID != request.RunID || manifest.Workspace != request.Workspace.Name || manifest.Base != string(request.Base) {
		return errors.New("archive manifest does not bind the requested recovery identity")
	}
	if count != 0 && len(manifest.Entries) != count {
		return errors.New("archive manifest entry count is unexpected")
	}
	return nil
}

func unpackArchive(encoded []byte) (archiveManifest, archiveData, error) {
	var manifest archiveManifest
	if !bytes.HasPrefix(encoded, archiveMagic) || len(encoded) < len(archiveMagic)+8+sha256.Size {
		return manifest, archiveData{}, errors.New("archive magic or length is invalid")
	}
	position := len(archiveMagic)
	headerSize := binary.BigEndian.Uint64(encoded[position : position+8])
	position += 8
	if headerSize > uint64(len(encoded)-position-sha256.Size) {
		return manifest, archiveData{}, errors.New("archive manifest length is invalid")
	}
	headerEnd := position + int(headerSize)
	if err := json.Unmarshal(encoded[position:headerEnd], &manifest); err != nil {
		return manifest, archiveData{}, err
	}
	position = headerEnd
	dataStart := position
	offsets := make([]int, len(manifest.Entries))
	for index, entry := range manifest.Entries {
		if entry.Size > uint64(len(encoded)-position-sha256.Size) {
			return manifest, archiveData{}, errors.New("archive entry exceeds payload")
		}
		offsets[index] = position - dataStart
		contents := encoded[position : position+int(entry.Size)]
		digest := sha256.Sum256(contents)
		if entry.SHA256 != fmt.Sprintf("%x", digest[:]) {
			return manifest, archiveData{}, errors.New("archive entry checksum mismatch")
		}
		position += int(entry.Size)
	}
	if position+sha256.Size != len(encoded) {
		return manifest, archiveData{}, errors.New("archive has trailing or missing bytes")
	}
	checksum := sha256.Sum256(encoded[:position])
	if !bytes.Equal(encoded[position:], checksum[:]) {
		return manifest, archiveData{}, errors.New("archive checksum mismatch")
	}
	return manifest, archiveData{content: encoded[dataStart:position], offsets: offsets}, nil
}
