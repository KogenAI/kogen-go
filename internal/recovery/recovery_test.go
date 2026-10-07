package recovery

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/journal"
	"kogen-go/internal/safefs"
)

const (
	testRunID  = "0123456789abcdef0123456789abcdef"
	testBase   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testTip    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testCommit = "cccccccccccccccccccccccccccccccccccccccc"
	testClaim  = "dddddddddddddddddddddddddddddddddddddddd"
	testRec    = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	testTree   = "ffffffffffffffffffffffffffffffffffffffff"
)

type ownerProbeFunc func(context.Context, int64, int64) (bool, error)

func (fn ownerProbeFunc) Alive(ctx context.Context, pid, started int64) (bool, error) {
	return fn(ctx, pid, started)
}

type writerSpy struct {
	mu    sync.Mutex
	stops []RunIdentity
	order *[]string
	err   error
}

func (w *writerSpy) StopRun(_ context.Context, identity RunIdentity) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stops = append(w.stops, identity)
	if w.order != nil {
		*w.order = append(*w.order, "stop")
	}
	return w.err
}

type refsSpy struct {
	mu        sync.Mutex
	refs      map[string]contract.ObjectID
	trees     map[contract.ObjectID]contract.ObjectID
	ancestors map[[2]contract.ObjectID]bool
	deleted   []string
}

func newRefsSpy() *refsSpy {
	return &refsSpy{
		refs: make(map[string]contract.ObjectID), trees: make(map[contract.ObjectID]contract.ObjectID),
		ancestors: make(map[[2]contract.ObjectID]bool),
	}
}

func (r *refsSpy) ReadRef(_ context.Context, name string) (contract.RefObservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	target, exists := r.refs[name]
	return contract.RefObservation{Name: name, Target: target, Exists: exists}, nil
}

func (r *refsSpy) ResolveTree(_ context.Context, revision string) (contract.ObjectID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tree, ok := r.trees[contract.ObjectID(revision)]
	if !ok {
		return "", fmt.Errorf("tree not found for %s", revision)
	}
	return tree, nil
}

func (r *refsSpy) IsAncestor(_ context.Context, ancestor, descendant contract.ObjectID) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ancestors[[2]contract.ObjectID{ancestor, descendant}], nil
}

func (r *refsSpy) CompareAndSwap(_ context.Context, update contract.RefUpdate) (contract.RefUpdateResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, exists := r.refs[update.Name]
	if !exists || current != update.Expected {
		return contract.RefUpdateResult{Updated: false, ObservedTarget: current}, nil
	}
	if update.Next == "" {
		delete(r.refs, update.Name)
		r.deleted = append(r.deleted, update.Name)
	} else {
		r.refs[update.Name] = update.Next
	}
	return contract.RefUpdateResult{Updated: true, ObservedTarget: update.Next}, nil
}

type gitSpy struct{ claim string }

func (g gitSpy) Exec(_ context.Context, args []string, _ []byte, _ contract.GitPolicy) (contract.GitResult, error) {
	if len(args) != 3 || args[0] != "cat-file" || args[1] != "blob" {
		return contract.GitResult{}, fmt.Errorf("unexpected Git command: %v", args)
	}
	status := 0
	return contract.GitResult{Process: contract.ProcessResult{ExitStatus: &status}, Stdout: []byte(g.claim)}, nil
}

type preserverSpy struct {
	mu      sync.Mutex
	refs    *refsSpy
	order   *[]string
	err     error
	calls   []PreservationRequest
	content []string
}

func (p *preserverSpy) Preserve(_ context.Context, request PreservationRequest) (journal.RecoveryRecord, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, request)
	bytes, err := os.ReadFile(filepath.Join(request.Workspace.AbsolutePath, "latest.txt"))
	if err == nil {
		p.content = append(p.content, string(bytes))
	} else {
		p.content = append(p.content, "<missing>")
	}
	if p.order != nil {
		*p.order = append(*p.order, "preserve:"+request.Workspace.Name)
	}
	if p.err != nil {
		return journal.RecoveryRecord{}, p.err
	}
	ref := "refs/kogen/candidates/" + request.RunID + "/recovery-" + request.Workspace.Name
	p.refs.mu.Lock()
	p.refs.refs[ref] = contract.ObjectID(testRec)
	p.refs.trees[contract.ObjectID(testRec)] = contract.ObjectID(testTree)
	p.refs.mu.Unlock()
	tree := testTree
	return journal.RecoveryRecord{
		Workspace: request.Workspace.Name, Base: string(request.Base),
		Tree: &tree, Ref: &ref, Verification: "unverified",
	}, nil
}

func TestRecoverPostCASPreservesBeforeCleanupAndReconcilesLanded(t *testing.T) {
	stateRoot, root, store, workspacePath := runningFixture(t, true, false)
	defer root.Close()
	order := make([]string, 0)
	refs := newRefsSpy()
	refs.refs["refs/heads/main"] = testTip
	refs.ancestors[[2]contract.ObjectID{testCommit, testTip}] = true
	refs.refs["refs/kogen/incoming/"+testRunID] = testCommit
	refs.refs[claimRef] = testClaim
	writers := &writerSpy{order: &order}
	preserver := &preserverSpy{refs: refs, order: &order}
	controller := newTestController(t, stateRoot, refs, writers, preserver, func(context.Context, int64, int64) (bool, error) { return false, nil }, testRunID)
	defer controller.Close()

	report, err := controller.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Runs) != 1 || report.Runs[0].Status != "landed" || report.Runs[0].Reason != "reconciled" || report.Runs[0].CleanupPending {
		t.Fatalf("unexpected recovery result: %#v", report)
	}
	if _, err := os.Stat(workspacePath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("preserved workspace still exists or could not be inspected: %v", err)
	}
	if len(order) != 2 || order[0] != "stop" || order[1] != "preserve:R1" {
		t.Fatalf("writer and preservation order = %#v", order)
	}
	if len(preserver.content) != 1 || preserver.content[0] != "latest bytes\n" {
		t.Fatalf("preserver did not see latest bytes: %#v", preserver.content)
	}
	snapshot, err := store.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != "landed" || snapshot.CleanupPending || len(snapshot.Recovery) != 1 || snapshot.Recovery[0].Verification != "unverified" {
		t.Fatalf("wrong durable run state: %#v", snapshot)
	}
	events, err := store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	var reconciled, preserved int
	for _, event := range events {
		if event.Event == "reconciled" {
			reconciled++
		}
		if event.Event == "recovery_preserved" {
			preserved++
		}
	}
	if reconciled != 1 || preserved != 1 {
		t.Fatalf("reconciliation/preservation event counts = %d/%d", reconciled, preserved)
	}
	refs.mu.Lock()
	defer refs.mu.Unlock()
	if _, exists := refs.refs[claimRef]; exists {
		t.Fatal("owned claim was not released")
	}
	if _, exists := refs.refs["refs/kogen/incoming/"+testRunID]; exists {
		t.Fatal("landed incoming ref was not removed")
	}
	if _, exists := refs.refs["refs/kogen/candidates/"+testRunID+"/recovery-R1"]; !exists {
		t.Fatal("recovery candidate ref was removed")
	}
}

func TestFailedPreservationRetainsWorkspaceAndTerminalRetryDoesNotRetryBuild(t *testing.T) {
	stateRoot, root, store, workspacePath := runningFixture(t, false, false)
	defer root.Close()
	refs := newRefsSpy()
	refs.refs[claimRef] = testClaim
	writers := &writerSpy{}
	preserver := &preserverSpy{refs: refs, err: errors.New("injected publication failure")}
	controller := newTestController(t, stateRoot, refs, writers, preserver, func(context.Context, int64, int64) (bool, error) { return false, nil }, testRunID)
	defer controller.Close()

	first, err := controller.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Runs) != 1 || first.Runs[0].Status != "failed" || first.Runs[0].Reason != "crashed" || !first.Runs[0].CleanupPending {
		t.Fatalf("unexpected failed preservation result: %#v", first)
	}
	if _, err := os.Stat(workspacePath); err != nil {
		t.Fatalf("failed preservation did not retain workspace: %v", err)
	}
	snapshot, err := store.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != "failed" || !snapshot.CleanupPending {
		t.Fatalf("failed outcome/pending state not durable: %#v", snapshot)
	}
	if _, exists := refs.refs["refs/kogen/incoming/"+testRunID]; exists {
		t.Fatal("failed run's incoming candidate ref was removed")
	}

	preserver.err = nil
	second, err := controller.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Runs) != 1 || second.Runs[0].Status != "failed" || second.Runs[0].Reason != "crashed" || second.Runs[0].CleanupPending || !second.Runs[0].Retried {
		t.Fatalf("unexpected pending cleanup retry: %#v", second)
	}
	if _, err := os.Stat(workspacePath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("successfully preserved workspace remains: %v", err)
	}
	snapshot, err = store.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != "failed" || snapshot.CleanupPending || len(snapshot.Recovery) != 1 {
		t.Fatalf("cleanup retry changed terminal outcome or lost recovery: %#v", snapshot)
	}
	events, err := store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	finished := 0
	for _, event := range events {
		if event.Event == "finished" {
			finished++
		}
	}
	if finished != 1 {
		t.Fatalf("terminal outcome was rewritten %d times", finished)
	}
}

func TestLiveOwnerIsUntouched(t *testing.T) {
	stateRoot, root, store, workspacePath := runningFixture(t, false, false)
	defer root.Close()
	refs := newRefsSpy()
	writers := &writerSpy{}
	preserver := &preserverSpy{refs: refs}
	controller := newTestController(t, stateRoot, refs, writers, preserver, func(context.Context, int64, int64) (bool, error) { return true, nil }, testRunID)
	defer controller.Close()

	report, err := controller.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Runs) != 0 || len(report.Skipped) != 1 || report.Skipped[0].Reason != "owner_alive" {
		t.Fatalf("live run was not reported as skipped: %#v", report)
	}
	if len(writers.stops) != 0 || len(preserver.calls) != 0 {
		t.Fatalf("live owner received recovery effects: stops=%d preserves=%d", len(writers.stops), len(preserver.calls))
	}
	if _, err := os.Stat(workspacePath); err != nil {
		t.Fatalf("live workspace was changed: %v", err)
	}
	snapshot, err := store.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != "running" {
		t.Fatalf("live run status = %q, want running", snapshot.Status)
	}
}

func TestRecoveryOnlyReleasesClaimOwnedByRun(t *testing.T) {
	stateRoot, root, _, _ := runningFixture(t, false, true)
	defer root.Close()
	refs := newRefsSpy()
	refs.refs[claimRef] = testClaim
	writers := &writerSpy{}
	preserver := &preserverSpy{refs: refs}
	controller := newTestController(t, stateRoot, refs, writers, preserver, func(context.Context, int64, int64) (bool, error) { return false, nil }, "fedcba9876543210fedcba9876543210")
	defer controller.Close()

	if _, err := controller.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	refs.mu.Lock()
	defer refs.mu.Unlock()
	if target, exists := refs.refs[claimRef]; !exists || target != testClaim {
		t.Fatal("recovery released a claim owned by another run")
	}
}

func TestWriterStopFailureKeepsWorkspaceAndOwnedClaim(t *testing.T) {
	stateRoot, root, store, workspacePath := runningFixture(t, false, false)
	defer root.Close()
	refs := newRefsSpy()
	refs.refs[claimRef] = testClaim
	writers := &writerSpy{err: errors.New("writer still active")}
	preserver := &preserverSpy{refs: refs}
	controller := newTestController(t, stateRoot, refs, writers, preserver, func(context.Context, int64, int64) (bool, error) { return false, nil }, testRunID)
	defer controller.Close()

	report, err := controller.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Runs) != 1 || report.Runs[0].Status != "failed" || report.Runs[0].Reason != "crashed" || !report.Runs[0].CleanupPending {
		t.Fatalf("unexpected writer-stop failure result: %#v", report)
	}
	if len(preserver.calls) != 0 {
		t.Fatal("preservation ran while a writer could still be active")
	}
	if _, err := os.Stat(workspacePath); err != nil {
		t.Fatalf("workspace was removed before writers stopped: %v", err)
	}
	if target, exists := refs.refs[claimRef]; !exists || target != testClaim {
		t.Fatal("owned claim was released while writers could still be active")
	}
	snapshot, err := store.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != "failed" || !snapshot.CleanupPending {
		t.Fatalf("terminal status or cleanup obligation was lost: %#v", snapshot)
	}
}

func TestRemoveWorkspaceTreeDoesNotFollowLinksOrMutateHardlinkAliases(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, testRunID+"-R1")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(workspace, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "link")); err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := removeWorkspaceTree(canonicalRoot, testRunID+"-R1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workspace); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("workspace remains after descriptor-rooted deletion: %v", err)
	}
	contents, err := os.ReadFile(outside)
	if err != nil || string(contents) != "keep\n" {
		t.Fatalf("outside target was modified: %q, %v", contents, err)
	}
}

func TestNativeOwnerProbeDetectsCurrentPIDAndRejectsReusedIdentity(t *testing.T) {
	probe := NativeOwnerProbe{}
	ctx := context.Background()
	started, exists, err := nativeProcessStartedMS(int64(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("current process identity is not present")
	}
	alive, err := probe.Alive(ctx, int64(os.Getpid()), started)
	if err != nil || !alive {
		t.Fatalf("current process probe = %t, %v", alive, err)
	}
	alive, err = probe.Alive(ctx, int64(os.Getpid()), started-60_000)
	if err != nil || alive {
		t.Fatalf("reused PID probe = %t, %v, want false", alive, err)
	}
}

func runningFixture(t *testing.T, landed, terminalPending bool) (string, *safefs.Root, *journal.RunStore, string) {
	t.Helper()
	stateRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(stateRoot, "runs"), 0o700); err != nil {
		t.Fatal(err)
	}
	workspacePath := filepath.Join(stateRoot, testRunID+"-R1")
	if err := os.Mkdir(workspacePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspacePath, "latest.txt"), []byte("latest bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := safefs.OpenRoot(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.NewRunStore(root, "runs/"+testRunID)
	if err != nil {
		_ = root.Close()
		t.Fatal(err)
	}
	status := "running"
	if terminalPending {
		status = "failed"
	}
	snapshot := journal.RunSnapshot{
		Schema: 2, RunID: testRunID, Slug: "alpha",
		ApprovalSHA256: strings.Repeat("1", 64), ApprovalCommit: testBase,
		TargetBranch: "main", Status: status, OwnerPID: int64(os.Getpid()), OwnerStartedMS: 1_000,
		StartedMS: 2_000, CleanupPending: terminalPending,
	}
	if landed {
		snapshot.Landing = &journal.LandingRecord{
			ApprovalCommit: testBase, RunID: testRunID, ExpectedParent: testBase,
			FinalTree: testTree, CandidateCommit: testCommit,
		}
	}
	if err := store.Create(snapshot); err != nil {
		_ = root.Close()
		t.Fatal(err)
	}
	started := journal.NewRunEvent("started", 2_001)
	if err := started.Set("base_sha", testBase); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(started, snapshot); err != nil {
		t.Fatal(err)
	}
	if terminalPending {
		finished := journal.NewRunEvent("finished", 2_002)
		_ = finished.Set("status", "failed")
		_ = finished.Set("reason", "crashed")
		if err := store.Record(finished, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	return stateRoot, root, store, workspacePath
}

func newTestController(t *testing.T, stateRoot string, refs *refsSpy, writers *writerSpy, preserver *preserverSpy, owners ownerProbeFunc, claimOwner string) *Controller {
	t.Helper()
	if preserver.refs == nil {
		preserver.refs = refs
	}
	if claimOwner == "" {
		claimOwner = testRunID
	}
	controller, err := NewController(Config{
		StateRoot: stateRoot, Git: gitSpy{claim: claimOwner + "\n"},
		GitPolicy: contract.GitPolicy{WorkingDirectory: stateRoot}, Refs: refs,
		Writers: writers, Preserver: preserver, Owners: owners,
		Now: func() time.Time { return time.UnixMilli(9_000) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return controller
}
