package landingrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kogen-go/internal/acceptance"
	acceptancecommand "kogen-go/internal/acceptance/command"
	"kogen-go/internal/contract"
	"kogen-go/internal/gate"
	"kogen-go/internal/gitio"
	"kogen-go/internal/journal"
	"kogen-go/internal/landing/commit"
	"kogen-go/internal/landing/integrate"
	"kogen-go/internal/landing/publish"
	"kogen-go/internal/process"
	"kogen-go/internal/recovery"
	"kogen-go/internal/safefs"
	"kogen-go/internal/testkit"
)

const (
	replayRunID = "0123456789abcdef0123456789abcdef"
	otherRunID  = "fedcba9876543210fedcba9876543210"
)

func TestCrashAfterBaseCASRecoversLandingAndPreservesLatestWorkspace(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	environment := fixtureEnvironment(fixture)
	base := objectAt(t, fixture, "refs/heads/main")
	candidate := makeCandidate(t, fixture, base, "candidate was verified\n")
	stateRoot := filepath.Join(fixture.Root, "state")
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(stateRoot, replayRunID+"-R1")
	fixture.Run(t, "clone", "--quiet", "--local", "--no-hardlinks", "--no-checkout", fixture.Checkout, workspace)
	fixture.RunIn(t, workspace, "checkout", "--quiet", "--detach", string(base))
	writeLatestWorkspace(t, workspace)

	store, snapshot, closeStore := newRun(t, fixture, base, replayRunID)
	defer closeStore()
	createClaim(t, fixture, base, replayRunID)
	crash := errors.New("injected process death after base CAS")
	_, err := publish.Publish(context.Background(), publish.Request{
		Processes:   process.Supervisor{},
		Environment: environment,
		Repository:  fixture.Checkout,
		Store:       store,
		Snapshot:    snapshot,
		Candidate:   candidate,
		Clock:       &testClock{now: time.UnixMilli(10_000)},
		Observer: publish.ObserverFunc(func(point publish.Point, _ commit.Result) error {
			if point == publish.BaseCASSucceeded {
				return crash
			}
			return nil
		}),
	})
	if !errors.Is(err, crash) {
		t.Fatalf("Publish() error = %v, want injected crash", err)
	}
	if got := objectAt(t, fixture, "refs/heads/main"); got != candidate.Commit {
		t.Fatalf("base tip after crash = %s, want candidate %s", got, candidate.Commit)
	}
	incoming := "refs/kogen/incoming/" + replayRunID
	if got := objectAt(t, fixture, incoming); got != candidate.Commit {
		t.Fatalf("incoming ref after crash = %s, want candidate %s", got, candidate.Commit)
	}
	if snapshot.Status != "running" || snapshot.Landing == nil || snapshot.Landing.CandidateCommit != string(candidate.Commit) {
		t.Fatalf("durable landing record after crash = %#v", snapshot)
	}

	var order []string
	refs := gitio.NewRefPort(gitio.NewOrigin(process.Supervisor{}), gitio.OriginPolicy(fixture.Checkout, environment))
	preserver := &gitPreserver{
		processes: process.Supervisor{}, environment: environment,
		originPath: fixture.Checkout,
		order:      &order,
	}
	controller, err := recovery.NewController(recovery.Config{
		StateRoot: stateRoot,
		Git:       gitio.NewOrigin(process.Supervisor{}),
		GitPolicy: gitio.OriginPolicy(fixture.Checkout, environment),
		Refs:      refs,
		Writers: writerFunc(func(_ context.Context, identity recovery.RunIdentity) error {
			if identity.RunID != replayRunID {
				return fmt.Errorf("stopped unexpected run %q", identity.RunID)
			}
			order = append(order, "stop")
			return nil
		}),
		Preserver: preserver,
		Owners:    ownerFunc(func(context.Context, int64, int64) (bool, error) { return false, nil }),
		Now:       func() time.Time { return time.UnixMilli(20_000) },
	})
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}
	defer controller.Close()

	report, err := controller.Recover(context.Background())
	if err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if len(report.Runs) != 1 || report.Runs[0].Status != "landed" || report.Runs[0].Reason != "reconciled" || report.Runs[0].CleanupPending {
		t.Fatalf("recovery result = %#v, want reconciled landed run", report)
	}
	if got, want := strings.Join(order, ","), "stop,preserve"; got != want {
		t.Fatalf("recovery effect order = %q, want %q", got, want)
	}
	if _, err := os.Lstat(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preserved workspace remains or cannot be inspected: %v", err)
	}
	if _, err := refs.ReadRef(context.Background(), incoming); err != nil {
		t.Fatal(err)
	} else if ref, _ := refs.ReadRef(context.Background(), incoming); ref.Exists {
		t.Fatalf("landed incoming ref remains at %s", ref.Target)
	}
	if claim, err := refs.ReadRef(context.Background(), "refs/kogen/claim"); err != nil {
		t.Fatal(err)
	} else if claim.Exists {
		t.Fatalf("owned claim remains at %s", claim.Target)
	}

	current, err := store.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "landed" || current.CleanupPending || len(current.Recovery) != 1 {
		t.Fatalf("recovered run snapshot = %#v", current)
	}
	recoveryRecord := current.Recovery[0]
	if recoveryRecord.Verification != "unverified" || recoveryRecord.Tree == nil || recoveryRecord.Ref == nil {
		t.Fatalf("recovery record = %#v, want retained unverified Git tree", recoveryRecord)
	}
	preservedCommit := objectAt(t, fixture, *recoveryRecord.Ref)
	if got := strings.TrimSpace(string(fixture.Run(t, "show", string(preservedCommit)+":latest.txt"))); got != "later unsnapshotted work" {
		t.Fatalf("preserved latest bytes = %q", got)
	}
	if got := string(fixture.Run(t, "ls-tree", string(preservedCommit), "latest.sh")); !strings.HasPrefix(got, "100755 blob ") {
		t.Fatalf("preserved executable mode = %q, want 100755", got)
	}
	if got := string(fixture.Run(t, "ls-tree", string(preservedCommit), "latest-link")); !strings.HasPrefix(got, "120000 blob ") {
		t.Fatalf("preserved symlink mode = %q, want 120000", got)
	}
	if got := strings.TrimSpace(string(fixture.Run(t, "show", string(preservedCommit)+":latest-link"))); got != "latest.txt" {
		t.Fatalf("preserved symlink target = %q", got)
	}
	tracked, err := refs.ReadRef(context.Background(), *recoveryRecord.Ref)
	if err != nil || !tracked.Exists {
		t.Fatalf("recovery ref lookup = %#v, %v", tracked, err)
	}
	resolvedTree, err := refs.ResolveTree(context.Background(), string(tracked.Target))
	if err != nil || string(resolvedTree) != *recoveryRecord.Tree {
		t.Fatalf("recovery ref tree = %q, record tree = %q, err = %v", resolvedTree, *recoveryRecord.Tree, err)
	}
	if !strings.Contains(string(fixture.Run(t, "ls-tree", "-r", "--name-only", string(preservedCommit))), ".gitignore") {
		t.Fatal("preserved tree lost the ignore file used to exclude ignored work")
	}
	if strings.Contains(string(fixture.Run(t, "ls-tree", "-r", "--name-only", string(preservedCommit))), "ignored.tmp") {
		t.Fatal("ignored untracked file was included in the recovery snapshot")
	}
	events, err := store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	if !hasEvent(events, "reconciled") || !hasEvent(events, "recovery_preserved") {
		t.Fatalf("recovery events = %#v, want reconciled and recovery_preserved", events)
	}
}

func TestMovedBaseRebasesAndReverifiesBeforeRealPublication(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	environment := fixtureEnvironment(fixture)
	oldBase := objectAt(t, fixture, "refs/heads/main")
	workspace := filepath.Join(fixture.Root, "moved-base-workspace")
	fixture.Run(t, "clone", "--quiet", "--local", "--no-hardlinks", "--no-checkout", fixture.Origin, workspace)
	fixture.RunIn(t, workspace, "checkout", "--quiet", "--detach", string(oldBase))
	candidate := makeWorkspaceCandidate(t, fixture, workspace, oldBase, "candidate survives the move\n")
	movedBase := moveBase(t, fixture, "independent.txt", "new base content\n")
	store, snapshot, closeStore := newRun(t, fixture, oldBase, replayRunID)
	defer closeStore()

	var verifyCalls, publishCalls int
	clock := &testClock{now: time.UnixMilli(50_000)}
	workspaceGit := gitio.NewWorkspace(process.Supervisor{})
	workspacePolicy := gitio.WorkspacePolicy(workspace, environment)
	result, err := integrate.Land(context.Background(), integrate.Request{
		Processes: process.Supervisor{}, Environment: environment,
		Repository: fixture.Origin, Workspace: workspace, Branch: "main", RunID: replayRunID,
		Candidate: candidate, RunStore: store, Snapshot: snapshot, Clock: clock,
		Verifier: verifierFunc(func(ctx context.Context, input integrate.VerifyRequest) (*gate.GateReport, error) {
			verifyCalls++
			baseWorkspace := filepath.Join(fixture.Root, fmt.Sprintf("verify-base-%d", verifyCalls))
			if err := cloneAt(t, fixture, input.Base.Commit, baseWorkspace); err != nil {
				return nil, err
			}
			runDir := filepath.Join(fixture.Root, fmt.Sprintf("verify-run-%d", verifyCalls))
			if err := os.Mkdir(runDir, 0o700); err != nil {
				return nil, err
			}
			git := gitio.NewWorkspace(process.Supervisor{})
			policy := gitio.WorkspacePolicy(input.Workspace, environment)
			metadata, err := gitio.LoadBaseMetadata(ctx, git, policy, input.Base.Commit)
			if err != nil {
				return nil, err
			}
			return gate.Run(ctx, gate.Request{
				Processes: process.Supervisor{}, AcceptanceRunner: productionAcceptance{
					runner: acceptancecommand.Runner{Processes: process.Supervisor{}, Trees: acceptance.CandidateTree{Git: git, Policy: policy, Base: metadata}},
				},
				Trees:         acceptance.CandidateTree{Git: git, Policy: policy, Base: metadata},
				BaseWorkspace: baseWorkspace, CandidateWorkspace: input.Workspace, RunDir: runDir,
				ExpectedBaseTree: string(input.Base.Tree), ApprovalSHA256: strings.Repeat("a", 64),
				Acceptance: gate.AcceptancePlan{
					Request: acceptancecommand.Request{
						Config: acceptancecommand.Config{Extension: ".sh", CandidateDir: "test/acceptance", Run: []string{"sh", "{path}"}, Timeout: time.Second},
						Slug:   "landing-recovery-replay", ExpectedItems: []string{"A1"}, Environment: environment,
					},
					ApprovedBytes: []byte("#!/bin/sh\nprintf '%s\\n' '{\"tag\":\"landing-recovery-replay/A1\",\"test\":\"fixture\",\"status\":\"passed\"}' > \"$KOGEN_LEDGER_REPORT\"\n"),
					ChangeItems:   []string{"A1"},
				},
				HomeDir: fixture.Home, TempDir: fixture.Root,
			})
		}),
		Repairer: repairerFunc(func(context.Context, integrate.RepairRequest) error {
			return errors.New("non-conflicting moved base must not request a repair")
		}),
		CreateCandidate: func(ctx context.Context, base integrate.Base, report *gate.GateReport) (commit.Result, error) {
			receipt, ok := report.Receipt()
			if !ok || !report.IsLandable() || receipt.BaseTree != string(base.Tree) {
				return commit.Result{}, errors.New("moved-base gate did not return a landable receipt for the refreshed tree")
			}
			tree, err := gitio.ParseObjectID(receipt.CandidateTree)
			if err != nil {
				return commit.Result{}, err
			}
			message := []byte("Rebased candidate\n\nKogen-Intent: landing-recovery-replay\n")
			id, err := gitio.NewRefPort(workspaceGit, workspacePolicy).CommitTree(ctx, contract.CommitTreeRequest{
				Tree: tree, Parents: []contract.ObjectID{base.Commit}, Message: message,
			})
			if err != nil {
				return commit.Result{}, err
			}
			return commit.Result{Commit: id, Parent: base.Commit, VerifiedTree: tree, Tree: tree, Message: message}, nil
		},
		Publish: func(ctx context.Context, value commit.Result) (publish.Result, error) {
			publishCalls++
			return publish.Publish(ctx, publish.Request{
				Processes: process.Supervisor{}, Environment: environment, Repository: fixture.Origin,
				Store: store, Snapshot: snapshot, Candidate: value, Clock: clock,
			})
		},
		ReleaseClaim: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("Land() error = %v", err)
	}
	if result.Kind != integrate.OutcomeLanded || result.Base.Commit != movedBase || verifyCalls != 1 || publishCalls != 2 {
		t.Fatalf("moved-base landing = %#v (verify=%d publish=%d)", result, verifyCalls, publishCalls)
	}
	if got := contract.ObjectID(strings.TrimSpace(string(fixture.RunIn(t, fixture.Origin, "rev-parse", "--verify", "refs/heads/main")))); got != result.Commit {
		t.Fatalf("target tip = %s, want landed candidate %s", got, result.Commit)
	}
	parents := strings.Fields(string(fixture.RunIn(t, fixture.Origin, "show", "-s", "--format=%P", string(result.Commit))))
	if len(parents) != 1 || parents[0] != string(movedBase) {
		t.Fatalf("landed commit parents = %v, want sole refreshed base %s", parents, movedBase)
	}
	if got := string(fixture.RunIn(t, fixture.Origin, "show", string(result.Commit)+":README.md")); got != "candidate survives the move\n" {
		t.Fatalf("landed candidate bytes = %q", got)
	}
	if got := string(fixture.RunIn(t, fixture.Origin, "show", string(result.Commit)+":independent.txt")); got != "new base content\n" {
		t.Fatalf("landed refreshed-base bytes = %q", got)
	}
	if len(result.CandidateRefs) != 2 {
		t.Fatalf("candidate refs = %v, want original and rebased candidates retained", result.CandidateRefs)
	}
	events, err := store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	if !hasEvent(events, "landing_retry") || !hasEvent(events, "landing_prepared") || snapshot.Status != "landed" {
		t.Fatalf("landing events/status = %#v / %q", events, snapshot.Status)
	}
}

func TestLostBaseCASRetriesAgainstRealCompetingTip(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	environment := fixtureEnvironment(fixture)
	base := objectAt(t, fixture, "refs/heads/main")
	candidate := makeCandidate(t, fixture, base, "candidate from old base\n")
	store, snapshot, closeStore := newRun(t, fixture, base, replayRunID)
	defer closeStore()
	clock := &testClock{now: time.UnixMilli(30_000)}
	var competingTip contract.ObjectID
	var moved bool
	result, err := publish.Publish(context.Background(), publish.Request{
		Processes: process.Supervisor{}, Environment: environment, Repository: fixture.Checkout,
		Store: store, Snapshot: snapshot, Candidate: candidate, Clock: clock,
		Observer: publish.ObserverFunc(func(point publish.Point, _ commit.Result) error {
			if point != publish.BeforeBaseCAS || moved {
				return nil
			}
			moved = true
			tree := strings.TrimSpace(string(fixture.Run(t, "rev-parse", string(base)+"^{tree}")))
			competingTip = contract.ObjectID(strings.TrimSpace(string(fixture.Run(t, "commit-tree", tree, "-p", string(base), "-m", "independent base advance"))))
			fixture.Run(t, "update-ref", "refs/heads/main", string(competingTip), string(base))
			return nil
		}),
	})
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if !moved || result.Kind != publish.BaseMoved || result.Commit != candidate.Commit {
		t.Fatalf("Publish() = %#v (moved=%t), want moved-base result for candidate", result, moved)
	}
	if got := objectAt(t, fixture, "refs/heads/main"); got != competingTip {
		t.Fatalf("competing base tip = %s, want %s", got, competingTip)
	}
	if got, want := clock.waits, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}; !sameDurations(got, want) {
		t.Fatalf("CAS retry delays = %v, want %v", got, want)
	}
	if ref, err := gitio.NewRefPort(gitio.NewOrigin(process.Supervisor{}), gitio.OriginPolicy(fixture.Checkout, environment)).ReadRef(context.Background(), "refs/kogen/incoming/"+replayRunID); err != nil {
		t.Fatal(err)
	} else if ref.Exists {
		t.Fatalf("incoming ref remains after lost CAS retry: %#v", ref)
	}
	events, err := store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	delays := make([]int64, 0, 4)
	for _, event := range events {
		if event.Event != "landing_retry" {
			continue
		}
		var delay int64
		if err := json.Unmarshal(event.Fields["delay_ms"], &delay); err != nil {
			t.Fatalf("decode landing_retry delay: %v", err)
		}
		delays = append(delays, delay)
	}
	if got, want := delays, []int64{1000, 2000, 4000, 0}; !sameInt64s(got, want) {
		t.Fatalf("durable landing retry delays = %v, want %v", got, want)
	}
}

func TestDirtyCheckedOutBaseProducesExactWarningWithoutOverwritingBytes(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	environment := fixtureEnvironment(fixture)
	base := objectAt(t, fixture, "refs/heads/main")
	candidate := makeCandidate(t, fixture, base, "candidate commit\n")
	dirtyPath := filepath.Join(fixture.Checkout, "local-only.txt")
	if err := os.WriteFile(dirtyPath, []byte("keep local work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, snapshot, closeStore := newRun(t, fixture, base, otherRunID)
	defer closeStore()
	result, err := publish.Publish(context.Background(), publish.Request{
		Processes: process.Supervisor{}, Environment: environment, Repository: fixture.Checkout,
		Store: store, Snapshot: snapshot, Candidate: candidate,
		Clock: &testClock{now: time.UnixMilli(40_000)},
	})
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	canonicalCheckout, err := filepath.EvalSymlinks(fixture.Checkout)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("land: warning: landed %s on main; your checkout at %s has local changes and was not updated; run `git reset --keep %s`, or merge it yourself", candidate.Commit, canonicalCheckout, candidate.Commit)
	if len(result.Warnings) != 1 || result.Warnings[0] != want {
		t.Fatalf("landing warnings = %#v, want exact warning %q", result.Warnings, want)
	}
	if got, err := os.ReadFile(dirtyPath); err != nil || string(got) != "keep local work\n" {
		t.Fatalf("dirty checkout bytes = %q, %v", got, err)
	}
	if got := objectAt(t, fixture, "refs/heads/main"); got != candidate.Commit {
		t.Fatalf("published base = %s, want %s", got, candidate.Commit)
	}
	events, err := store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	if !hasEvent(events, "landing_warning") {
		t.Fatalf("landing journal omitted warning event: %#v", events)
	}
}

type gitPreserver struct {
	processes   contract.ProcessRunner
	environment process.Environment
	originPath  string
	order       *[]string
}

func (p *gitPreserver) Preserve(ctx context.Context, request recovery.PreservationRequest) (journal.RecoveryRecord, error) {
	if p.order != nil {
		*p.order = append(*p.order, "preserve")
	}
	workspaceGit := gitio.NewWorkspace(p.processes)
	workspacePolicy := gitio.WorkspacePolicy(request.Workspace.AbsolutePath, p.environment)
	tree, err := gitio.BuildCandidateTree(ctx, workspaceGit, workspacePolicy, request.Base)
	if err != nil {
		return journal.RecoveryRecord{}, fmt.Errorf("snapshot latest workspace: %w", err)
	}
	message := []byte("Kogen recovery preservation\n")
	commitID, err := gitio.NewRefPort(workspaceGit, workspacePolicy).CommitTree(ctx, contract.CommitTreeRequest{
		Tree: tree, Parents: []contract.ObjectID{request.Base}, Message: message,
	})
	if err != nil {
		return journal.RecoveryRecord{}, fmt.Errorf("commit preserved workspace tree: %w", err)
	}
	ref := "refs/kogen/candidates/" + request.RunID + "/recovery-" + request.Workspace.Name
	pushed, err := workspaceGit.Exec(ctx, []string{
		"push", "--no-verify", "--force-with-lease=" + ref + ":",
		p.originPath, string(commitID) + ":" + ref,
	}, nil, workspacePolicy)
	if err != nil {
		return journal.RecoveryRecord{}, fmt.Errorf("publish preserved commit: %w", err)
	}
	if pushed.Process.ExitStatus == nil || *pushed.Process.ExitStatus != 0 || pushed.Process.TimedOut || pushed.Process.Unavailable {
		return journal.RecoveryRecord{}, errors.New("publish preserved commit failed")
	}
	treeText := string(tree)
	return journal.RecoveryRecord{
		Workspace: request.Workspace.Name, Base: string(request.Base), Tree: &treeText,
		Ref: &ref, Verification: "unverified",
	}, nil
}

type writerFunc func(context.Context, recovery.RunIdentity) error

func (f writerFunc) StopRun(ctx context.Context, identity recovery.RunIdentity) error {
	return f(ctx, identity)
}

type ownerFunc func(context.Context, int64, int64) (bool, error)

func (f ownerFunc) Alive(ctx context.Context, pid, started int64) (bool, error) {
	return f(ctx, pid, started)
}

type verifierFunc func(context.Context, integrate.VerifyRequest) (*gate.GateReport, error)

func (f verifierFunc) Verify(ctx context.Context, request integrate.VerifyRequest) (*gate.GateReport, error) {
	return f(ctx, request)
}

type repairerFunc func(context.Context, integrate.RepairRequest) error

func (f repairerFunc) Repair(ctx context.Context, request integrate.RepairRequest) error {
	return f(ctx, request)
}

type productionAcceptance struct {
	runner acceptancecommand.Runner
}

func (r productionAcceptance) Run(ctx context.Context, execution gate.AcceptanceExecution) (acceptance.Result, error) {
	return r.runner.Run(ctx, execution.Request)
}

type testClock struct {
	now   time.Time
	waits []time.Duration
}

func (c *testClock) Now() time.Time { return c.now }

func (c *testClock) Sleep(ctx context.Context, delay time.Duration) error {
	c.waits = append(c.waits, delay)
	return ctx.Err()
}

func newRun(t *testing.T, fixture *testkit.GitFixture, base contract.ObjectID, runID string) (*journal.RunStore, *journal.RunSnapshot, func()) {
	t.Helper()
	root, err := safefs.OpenRoot(fixture.Root)
	if err != nil {
		t.Fatalf("open journal root: %v", err)
	}
	snapshot := &journal.RunSnapshot{
		Schema: 2, RunID: runID, Slug: "landing-recovery-replay",
		ApprovalSHA256: strings.Repeat("a", 64), ApprovalCommit: string(base),
		TargetBranch: "main", Status: "running", OwnerPID: int64(os.Getpid()),
		OwnerStartedMS: 1, StartedMS: 1, Recovery: []journal.RecoveryRecord{},
	}
	store, err := journal.NewRunStore(root, "state/runs/"+runID)
	if err != nil {
		_ = root.Close()
		t.Fatalf("create run store: %v", err)
	}
	if err := store.Create(*snapshot); err != nil {
		_ = root.Close()
		t.Fatalf("initialize run snapshot: %v", err)
	}
	started := journal.NewRunEvent("started", 1)
	if err := started.Set("base_sha", string(base)); err != nil {
		_ = root.Close()
		t.Fatal(err)
	}
	if err := store.Record(started, *snapshot); err != nil {
		_ = root.Close()
		t.Fatalf("record starting base: %v", err)
	}
	return store, snapshot, func() { _ = root.Close() }
}

func makeCandidate(t *testing.T, fixture *testkit.GitFixture, parent contract.ObjectID, contents string) commit.Result {
	t.Helper()
	worktree := filepath.Join(fixture.Root, "candidate-"+string(parent[:8]))
	fixture.Run(t, "worktree", "add", "--detach", worktree, string(parent))
	defer func() { fixture.Run(t, "worktree", "remove", "--force", worktree) }()
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write candidate bytes: %v", err)
	}
	fixture.RunIn(t, worktree, "add", "README.md")
	tree := strings.TrimSpace(string(fixture.RunIn(t, worktree, "write-tree")))
	message := []byte("Landing replay fixture\n\nKogen-Intent: landing-recovery-replay\n")
	commitID := strings.TrimSpace(string(fixture.RunIn(t, worktree,
		"commit-tree", tree, "-p", string(parent), "-m", string(message))))
	return commit.Result{
		Commit: contract.ObjectID(commitID), Parent: parent,
		VerifiedTree: contract.ObjectID(tree), Tree: contract.ObjectID(tree), Message: message,
	}
}

func makeWorkspaceCandidate(t *testing.T, fixture *testkit.GitFixture, workspace string, parent contract.ObjectID, contents string) commit.Result {
	t.Helper()
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write candidate bytes: %v", err)
	}
	fixture.RunIn(t, workspace, "add", "README.md")
	tree := strings.TrimSpace(string(fixture.RunIn(t, workspace, "write-tree")))
	message := []byte("Initial landing replay candidate\n\nKogen-Intent: landing-recovery-replay\n")
	commitID := strings.TrimSpace(string(fixture.RunIn(t, workspace,
		"commit-tree", tree, "-p", string(parent), "-m", string(message))))
	return commit.Result{
		Commit: contract.ObjectID(commitID), Parent: parent,
		VerifiedTree: contract.ObjectID(tree), Tree: contract.ObjectID(tree), Message: message,
	}
}

func cloneAt(t *testing.T, fixture *testkit.GitFixture, base contract.ObjectID, directory string) error {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(directory), 0o700); err != nil {
		return err
	}
	fixture.RunIn(t, fixture.Root, "clone", "--quiet", "--no-hardlinks", fixture.Origin, directory)
	fixture.RunIn(t, directory, "checkout", "--quiet", "--detach", string(base))
	return nil
}

func moveBase(t *testing.T, fixture *testkit.GitFixture, name, contents string) contract.ObjectID {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fixture.Checkout, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture.Run(t, "add", name)
	fixture.Run(t, "commit", "--quiet", "--message", "move base for landing replay")
	fixture.Run(t, "push", "--quiet", "origin", "main")
	return objectAt(t, fixture, "refs/heads/main")
}

func createClaim(t *testing.T, fixture *testkit.GitFixture, base contract.ObjectID, runID string) {
	t.Helper()
	worktree := filepath.Join(fixture.Root, "claim-worktree")
	fixture.Run(t, "worktree", "add", "--detach", worktree, string(base))
	defer func() { fixture.Run(t, "worktree", "remove", "--force", worktree) }()
	claimPath := filepath.Join(worktree, ".kogen", "claim")
	if err := os.MkdirAll(filepath.Dir(claimPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claimPath, []byte(runID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.RunIn(t, worktree, "add", ".kogen/claim")
	tree := strings.TrimSpace(string(fixture.RunIn(t, worktree, "write-tree")))
	claimCommit := strings.TrimSpace(string(fixture.RunIn(t, worktree,
		"commit-tree", tree, "-m", "Kogen project claim\n\nKogen-Run: "+runID)))
	fixture.Run(t, "update-ref", "refs/kogen/claim", claimCommit)
}

func writeLatestWorkspace(t *testing.T, workspace string) {
	t.Helper()
	if err := os.Remove(filepath.Join(workspace, "README.md")); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		".gitignore":  "ignored.tmp\n",
		"latest.txt":  "later unsnapshotted work",
		"latest.sh":   "#!/bin/sh\necho preserved\n",
		"ignored.tmp": "must not be preserved",
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(contents), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.Chmod(filepath.Join(workspace, "latest.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("latest.txt", filepath.Join(workspace, "latest-link")); err != nil {
		t.Fatal(err)
	}
}

func fixtureEnvironment(fixture *testkit.GitFixture) process.Environment {
	environment := make(process.Environment)
	for _, entry := range fixture.Environment() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			environment[key] = value
		}
	}
	return environment
}

func objectAt(t *testing.T, fixture *testkit.GitFixture, ref string) contract.ObjectID {
	t.Helper()
	return contract.ObjectID(strings.TrimSpace(string(fixture.Run(t, "rev-parse", "--verify", ref))))
}

func hasEvent(events []journal.RunEvent, name string) bool {
	for _, event := range events {
		if event.Event == name {
			return true
		}
	}
	return false
}

func sameDurations(left, right []time.Duration) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func sameInt64s(left, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
