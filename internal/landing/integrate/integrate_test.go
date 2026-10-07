package integrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kogen-go/internal/acceptance"
	"kogen-go/internal/acceptance/command"
	"kogen-go/internal/contract"
	"kogen-go/internal/gate"
	"kogen-go/internal/gitio"
	"kogen-go/internal/journal"
	"kogen-go/internal/landing/commit"
	"kogen-go/internal/landing/publish"
	"kogen-go/internal/process"
	"kogen-go/internal/safefs"
	"kogen-go/internal/testkit"
)

const testRunID = "0123456789abcdef0123456789abcdef"

func TestRefreshRecordsMovedBaseAndMergesCompleteFreshBaseline(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	base := commitAt(t, fixture, "refs/heads/main")
	baseTree := treeAt(t, fixture, string(base))
	workspace := cloneWorkspace(t, fixture, base, "refresh workspace")
	moved := moveBase(t, fixture, "external.txt", "new base\n")
	store, snapshot, closeStore := newRun(t, fixture, base)
	defer closeStore()
	var calls int

	result, err := Refresh(context.Background(), RefreshRequest{
		Processes:   process.Supervisor{},
		Environment: fixtureEnvironment(fixture),
		Repository:  fixture.Origin,
		Workspace:   workspace,
		Branch:      "main",
		Previous:    Base{Commit: base, Tree: baseTree},
		Checks:      []contract.CheckSpec{{Name: "check-a"}, {Name: "check-b"}},
		Baseline:    &gate.CheckBaseline{Tree: string(baseTree), Checks: []gate.BaselineCheck{{Name: "check-a", Status: contract.CheckRed}}},
		RunStore:    store,
		Snapshot:    snapshot,
		RunBaseChecks: func(_ context.Context, current Base, checks []contract.CheckSpec) ([]gate.BaselineCheck, error) {
			calls++
			if current.Commit != moved || len(checks) != 2 {
				return nil, fmt.Errorf("unexpected moved base/check set: %+v %+v", current, checks)
			}
			return []gate.BaselineCheck{
				{Name: "check-b", Status: contract.CheckGreen},
				{Name: "check-a", Status: contract.CheckRed, Findings: []contract.FindingIdentity{{Rule: "fixture/failure", Symbol: "one"}}},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if !result.Moved || result.Base.Commit != moved || result.Baseline == nil || result.Baseline.Tree != string(result.Base.Tree) {
		t.Fatalf("Refresh() = %+v, want moved tree-bound base", result)
	}
	if calls != 1 {
		t.Fatalf("base check runner calls = %d, want 1", calls)
	}
	if got := []string{result.Baseline.Checks[0].Name, result.Baseline.Checks[1].Name}; strings.Join(got, ",") != "check-a,check-b" {
		t.Fatalf("merged baseline order = %v", got)
	}
	if len(result.History) != 2 || result.History[1].Tree != string(result.Base.Tree) {
		t.Fatalf("baseline history = %+v, want prior and moved trees", result.History)
	}
	if got := strings.TrimSpace(string(fixture.RunIn(t, workspace, "rev-parse", "refs/kogen/moved-base/main"))); got != string(moved) {
		t.Fatalf("private refreshed ref = %s, want %s", got, moved)
	}
	events, err := store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Event != "base_moved_at_start" || events[1].Event != "base_check_baseline" {
		t.Fatalf("moved-base events = %#v", events)
	}
}

func TestLandRepairsSamePathConflictReverifiesAndPublishesOnlyAfterCAS(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	oldBase := commitAt(t, fixture, "refs/heads/main")
	workspace := cloneWorkspace(t, fixture, oldBase, "landing workspace")
	environment := fixtureEnvironment(fixture)
	workspaceGit := gitio.NewWorkspace(process.Supervisor{})
	workspacePolicy := gitio.WorkspacePolicy(workspace, environment)
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("candidate change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	candidate := makeCandidate(t, workspaceGit, workspacePolicy, oldBase, "Initial candidate\n\nKogen-Intent: moved-fixture\n")
	moveBase(t, fixture, "external.txt", "new base file\n")
	if err := os.WriteFile(filepath.Join(fixture.Checkout, "README.md"), []byte("base change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture.Run(t, "add", "README.md")
	fixture.Run(t, "commit", "--quiet", "--message", "move conflicting base file")
	fixture.Run(t, "push", "--quiet", "origin", "main")
	moved := commitAt(t, fixture, "refs/heads/main")

	store, snapshot, closeStore := newRun(t, fixture, oldBase)
	defer closeStore()
	verifier := &realGateVerifier{t: t, fixture: fixture, environment: environment}
	repairer := &conflictRepairer{}
	clock := &testClock{now: time.Unix(1_800_000_000, 0)}
	var publishCalls int

	result, err := Land(context.Background(), Request{
		Processes:   process.Supervisor{},
		Environment: environment,
		Repository:  fixture.Origin,
		Workspace:   workspace,
		Branch:      "main",
		RunID:       testRunID,
		Candidate:   candidate,
		RunStore:    store,
		Snapshot:    snapshot,
		Clock:       clock,
		Verifier:    verifier,
		Repairer:    repairer,
		CreateCandidate: func(ctx context.Context, base Base, report *gate.GateReport) (commit.Result, error) {
			receipt, ok := report.Receipt()
			if !ok || !report.IsLandable() || receipt.BaseTree != string(base.Tree) {
				return commit.Result{}, errors.New("candidate factory did not receive a real moved-base receipt")
			}
			verifiedTree, err := gitio.ParseObjectID(receipt.CandidateTree)
			if err != nil {
				return commit.Result{}, err
			}
			refs := gitio.NewRefPort(workspaceGit, workspacePolicy)
			message := []byte("Repaired candidate\n\nKogen-Intent: moved-fixture\n")
			commitID, err := refs.CommitTree(ctx, contract.CommitTreeRequest{Tree: verifiedTree, Parents: []contract.ObjectID{base.Commit}, Message: message})
			if err != nil {
				return commit.Result{}, err
			}
			return commit.Result{Commit: commitID, Parent: base.Commit, VerifiedTree: verifiedTree, Tree: verifiedTree, Message: message}, nil
		},
		Publish: func(ctx context.Context, value commit.Result) (publish.Result, error) {
			publishCalls++
			if publishCalls == 1 {
				return publish.Result{Kind: publish.BaseMoved, Commit: value.Commit, Tree: value.Tree}, nil
			}
			return publish.Publish(ctx, publish.Request{
				Environment: environment,
				Repository:  fixture.Origin,
				Store:       store,
				Snapshot:    snapshot,
				Candidate:   value,
				Clock:       clock,
			})
		},
		ReleaseClaim: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("Land() error = %v", err)
	}
	if result.Kind != OutcomeLanded || publishCalls != 2 || verifier.calls != 1 || repairer.calls != 1 {
		t.Fatalf("Land()=%+v publish=%d verify=%d repair=%d", result, publishCalls, verifier.calls, repairer.calls)
	}
	if result.Base.Commit != moved || result.Base.Tree == "" {
		t.Fatalf("landed base = %+v, want refreshed tip %s", result.Base, moved)
	}
	if len(result.CandidateRefs) != 2 {
		t.Fatalf("preserved candidate refs = %v, want original and repaired candidates", result.CandidateRefs)
	}
	if got := strings.TrimSpace(string(fixture.RunIn(t, fixture.Origin, "rev-parse", "refs/heads/main"))); got != string(result.Commit) {
		t.Fatalf("published base = %s, want final candidate %s", got, result.Commit)
	}
	if got := string(fixture.RunIn(t, fixture.Origin, "show", string(result.Commit)+":README.md")); got != "merged candidate and base\n" {
		t.Fatalf("landed README = %q", got)
	}
	if got := string(fixture.RunIn(t, fixture.Origin, "show", string(result.Commit)+":external.txt")); got != "new base file\n" {
		t.Fatalf("landed base addition = %q", got)
	}
	for _, ref := range result.CandidateRefs {
		if got := strings.TrimSpace(string(fixture.RunIn(t, fixture.Origin, "rev-parse", ref))); got == "" {
			t.Fatalf("preserved candidate ref %s has no object", ref)
		}
	}
	events, err := store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	if eventCount(events, "verification") != 1 || eventCount(events, "repair") != 1 || eventCount(events, "repaired") != 1 {
		t.Fatalf("landing repair events = %#v", events)
	}
	if snapshot.Status != "landed" {
		t.Fatalf("run status = %q, want landed", snapshot.Status)
	}
}

func TestIntegrateMovedCleanRebaseRunsFullGateWithoutRepair(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	oldBase := commitAt(t, fixture, "refs/heads/main")
	workspace := cloneWorkspace(t, fixture, oldBase, "clean rebase workspace")
	environment := fixtureEnvironment(fixture)
	workspaceGit := gitio.NewWorkspace(process.Supervisor{})
	workspacePolicy := gitio.WorkspacePolicy(workspace, environment)
	if err := os.WriteFile(filepath.Join(workspace, "candidate.txt"), []byte("candidate change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	candidate := makeCandidate(t, workspaceGit, workspacePolicy, oldBase, "Clean move candidate\n\nKogen-Intent: moved-fixture\n")
	moved := moveBase(t, fixture, "external.txt", "new base file\n")
	store, snapshot, closeStore := newRun(t, fixture, oldBase)
	defer closeStore()
	git, policy := gitio.NewWorkspace(process.Supervisor{}), gitio.WorkspacePolicy(workspace, environment)
	origin, originPolicy := originGit(process.Supervisor{}, fixture.Origin, environment)
	if err := resetWorkspaceGitConfig(context.Background(), git, policy, workspace); err != nil {
		t.Fatal(err)
	}
	if err := preserveCandidate(context.Background(), git, policy, origin, originPolicy, candidateRefName(testRunID, 0), candidate); err != nil {
		t.Fatalf("preserve original candidate: %v", err)
	}
	verifier := &realGateVerifier{t: t, fixture: fixture, environment: environment}
	request := Request{
		Workspace: workspace, Branch: "main", RunID: testRunID, RunStore: store, Snapshot: snapshot,
		Verifier: verifier, Repairer: repairerFunc(func(context.Context, RepairRequest) error {
			return errors.New("clean moved-base candidate must not request repair")
		}),
		CreateCandidate: func(ctx context.Context, base Base, report *gate.GateReport) (commit.Result, error) {
			receipt, ok := report.Receipt()
			if !ok || !report.IsLandable() {
				return commit.Result{}, errors.New("clean rebase gate did not produce a landable receipt")
			}
			tree, err := gitio.ParseObjectID(receipt.CandidateTree)
			if err != nil {
				return commit.Result{}, err
			}
			message := []byte("Clean rebase candidate\n\nKogen-Intent: moved-fixture\n")
			id, err := gitio.NewRefPort(git, policy).CommitTree(ctx, contract.CommitTreeRequest{Tree: tree, Parents: []contract.ObjectID{base.Commit}, Message: message})
			if err != nil {
				return commit.Result{}, err
			}
			return commit.Result{Commit: id, Parent: base.Commit, VerifiedTree: tree, Tree: tree, Message: message}, nil
		},
	}
	clock := &testClock{now: time.Unix(1_800_000_000, 0)}
	updated, base, parked, err := integrateMoved(context.Background(), request, fixture.Origin, workspace, git, policy, origin, originPolicy, candidate, time.Now().Add(time.Minute), clock, 1)
	if err != nil {
		t.Fatalf("integrateMoved() error = %v", err)
	}
	if parked || base.Commit != moved || verifier.calls != 1 || updated.Parent != moved {
		t.Fatalf("clean moved integration = candidate=%+v base=%+v parked=%t verifier=%d", updated, base, parked, verifier.calls)
	}
	if got := strings.TrimSpace(string(fixture.RunIn(t, fixture.Origin, "rev-parse", candidateRefName(testRunID, 0)))); got != string(candidate.Commit) {
		t.Fatalf("original candidate ref = %s, want %s", got, candidate.Commit)
	}
	if got := strings.TrimSpace(string(fixture.RunIn(t, fixture.Origin, "rev-parse", candidateRefName(testRunID, 1)))); got != string(updated.Commit) {
		t.Fatalf("reverified candidate ref = %s, want %s", got, updated.Commit)
	}
}

func TestLandParksImpossibleRebaseWithoutReplacingBaseOrLosingCandidate(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	oldBase := commitAt(t, fixture, "refs/heads/main")
	workspace := cloneWorkspace(t, fixture, oldBase, "impossible workspace")
	environment := fixtureEnvironment(fixture)
	workspaceGit := gitio.NewWorkspace(process.Supervisor{})
	workspacePolicy := gitio.WorkspacePolicy(workspace, environment)
	candidate := makeCandidate(t, workspaceGit, workspacePolicy, oldBase, "Candidate\n\nKogen-Intent: moved-fixture\n")
	oldTree := treeAt(t, fixture, string(oldBase))
	orphanTree := string(oldTree)
	orphan := strings.TrimSpace(string(fixture.Run(t, "commit-tree", orphanTree, "-m", "unrelated replacement tip")))
	fixture.Run(t, "push", "--quiet", "--force", "origin", orphan+":refs/heads/main")
	store, snapshot, closeStore := newRun(t, fixture, oldBase)
	defer closeStore()
	var releases int
	var verifierCalls int
	var publishCalls int
	result, err := Land(context.Background(), Request{
		Environment: environment, Repository: fixture.Origin, Workspace: workspace,
		Branch: "main", RunID: testRunID, Candidate: candidate, RunStore: store, Snapshot: snapshot,
		Verifier: verifierFunc(func(context.Context, VerifyRequest) (*gate.GateReport, error) {
			verifierCalls++
			return nil, errors.New("impossible rebase must park before verification")
		}),
		Repairer: repairerFunc(func(context.Context, RepairRequest) error { return errors.New("unexpected repair") }),
		CreateCandidate: func(context.Context, Base, *gate.GateReport) (commit.Result, error) {
			return commit.Result{}, errors.New("unexpected candidate creation")
		},
		Publish: func(_ context.Context, candidate commit.Result) (publish.Result, error) {
			publishCalls++
			return publish.Result{Kind: publish.BaseMoved, Commit: candidate.Commit, Tree: candidate.Tree}, nil
		},
		ReleaseClaim: func(context.Context) error { releases++; return nil },
	})
	if err != nil {
		t.Fatalf("Land() error = %v", err)
	}
	if result.Kind != OutcomeParked || verifierCalls != 0 || publishCalls != 1 || releases != 1 {
		t.Fatalf("Land()=%+v verify=%d publish=%d release=%d", result, verifierCalls, publishCalls, releases)
	}
	if got := strings.TrimSpace(string(fixture.RunIn(t, fixture.Origin, "rev-parse", "refs/heads/main"))); got != orphan {
		t.Fatalf("target base changed to %s, want unrelated tip %s", got, orphan)
	}
	if got := strings.TrimSpace(string(fixture.RunIn(t, fixture.Origin, "rev-parse", result.ParkedRef))); got != string(candidate.Commit) {
		t.Fatalf("parked candidate ref = %s, want original candidate %s", got, candidate.Commit)
	}
	if snapshot.Status != "parked" {
		t.Fatalf("run status = %q, want parked", snapshot.Status)
	}
	events, err := store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	if eventCount(events, "rebase_impossible") != 1 || eventCount(events, "finished") != 1 {
		t.Fatalf("impossible-rebase events = %#v", events)
	}
}

type realGateVerifier struct {
	t           *testing.T
	fixture     *testkit.GitFixture
	environment process.Environment
	calls       int
}

func (v *realGateVerifier) Verify(ctx context.Context, input VerifyRequest) (*gate.GateReport, error) {
	v.t.Helper()
	v.calls++
	baseWorkspace := filepath.Join(v.fixture.Root, fmt.Sprintf("gate-base-%d", v.calls))
	if err := cloneAt(v.t, v.fixture, input.Base.Commit, baseWorkspace); err != nil {
		return nil, err
	}
	runDir := filepath.Join(v.fixture.Root, fmt.Sprintf("gate-run-%d", v.calls))
	if err := os.Mkdir(runDir, 0o700); err != nil {
		return nil, err
	}
	git := gitio.NewWorkspace(process.Supervisor{})
	policy := gitio.WorkspacePolicy(input.Workspace, v.environment)
	metadata, err := gitio.LoadBaseMetadata(ctx, git, policy, input.Base.Commit)
	if err != nil {
		return nil, err
	}
	trees := acceptance.CandidateTree{Git: git, Policy: policy, Base: metadata}
	return gate.Run(ctx, gate.Request{
		Processes: process.Supervisor{}, AcceptanceRunner: passAcceptance{}, Trees: trees,
		BaseWorkspace: baseWorkspace, CandidateWorkspace: input.Workspace, RunDir: runDir,
		ExpectedBaseTree: string(input.Base.Tree), ApprovalSHA256: strings.Repeat("a", 64),
		Acceptance: gate.AcceptancePlan{Request: command.Request{
			Config: command.Config{Extension: ".sh", CandidateDir: "test/acceptance", Run: []string{"sh", "{path}"}, Timeout: time.Second},
			Slug:   "moved-fixture", ExpectedItems: []string{"A1"}, Environment: v.environment,
		}, ApprovedBytes: []byte("#!/bin/sh\nexit 0\n"), ChangeItems: []string{"A1"}},
		HomeDir: v.fixture.Home, TempDir: v.fixture.Root,
	})
}

type passAcceptance struct{}

func (passAcceptance) Run(_ context.Context, execution gate.AcceptanceExecution) (acceptance.Result, error) {
	exit := 0
	passed := make(map[string]bool, len(execution.Request.ExpectedItems))
	for _, item := range execution.Request.ExpectedItems {
		passed[item] = true
	}
	return acceptance.Result{Process: contract.ProcessResult{ExitStatus: &exit}, ItemPass: passed}, nil
}

type conflictRepairer struct{ calls int }

func (r *conflictRepairer) Repair(_ context.Context, request RepairRequest) error {
	r.calls++
	if len(request.Paths) != 1 || request.Paths[0] != "README.md" || !strings.Contains(request.Feedback, "README.md") {
		return fmt.Errorf("repair feedback omitted exact conflict paths: %+v", request)
	}
	return os.WriteFile(filepath.Join(request.Workspace, "README.md"), []byte("merged candidate and base\n"), 0o644)
}

type verifierFunc func(context.Context, VerifyRequest) (*gate.GateReport, error)

func (f verifierFunc) Verify(ctx context.Context, request VerifyRequest) (*gate.GateReport, error) {
	return f(ctx, request)
}

type repairerFunc func(context.Context, RepairRequest) error

func (f repairerFunc) Repair(ctx context.Context, request RepairRequest) error {
	return f(ctx, request)
}

type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }

func (c *testClock) Sleep(_ context.Context, delay time.Duration) error {
	c.now = c.now.Add(delay)
	return nil
}

func cloneAt(t testing.TB, fixture *testkit.GitFixture, base contract.ObjectID, directory string) error {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(directory), 0o700); err != nil {
		return err
	}
	fixture.RunIn(t, fixture.Root, "clone", "--quiet", "--no-hardlinks", fixture.Origin, directory)
	fixture.RunIn(t, directory, "checkout", "--quiet", "--detach", string(base))
	return nil
}

func cloneWorkspace(t testing.TB, fixture *testkit.GitFixture, base contract.ObjectID, name string) string {
	t.Helper()
	directory := filepath.Join(fixture.Root, name)
	if err := cloneAt(t, fixture, base, directory); err != nil {
		t.Fatal(err)
	}
	return directory
}

func commitAt(t testing.TB, fixture *testkit.GitFixture, ref string) contract.ObjectID {
	t.Helper()
	return contract.ObjectID(strings.TrimSpace(string(fixture.Run(t, "rev-parse", ref))))
}

func treeAt(t testing.TB, fixture *testkit.GitFixture, commit string) contract.ObjectID {
	t.Helper()
	return contract.ObjectID(strings.TrimSpace(string(fixture.Run(t, "rev-parse", commit+"^{tree}"))))
}

func moveBase(t testing.TB, fixture *testkit.GitFixture, name, contents string) contract.ObjectID {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fixture.Checkout, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture.Run(t, "add", name)
	fixture.Run(t, "commit", "--quiet", "--message", "move target base")
	fixture.Run(t, "push", "--quiet", "origin", "main")
	return commitAt(t, fixture, "refs/heads/main")
}

func makeCandidate(t testing.TB, git contract.GitPort, policy contract.GitPolicy, parent contract.ObjectID, message string) commit.Result {
	t.Helper()
	ctx := context.Background()
	metadata, err := gitio.LoadBaseMetadata(ctx, git, policy, parent)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := gitio.SnapshotCandidateTree(ctx, git, policy, metadata)
	if err != nil {
		t.Fatal(err)
	}
	refs := gitio.NewRefPort(git, policy)
	commitID, err := refs.CommitTree(ctx, contract.CommitTreeRequest{Tree: tree, Parents: []contract.ObjectID{parent}, Message: []byte(message)})
	if err != nil {
		t.Fatal(err)
	}
	return commit.Result{Commit: commitID, Parent: parent, VerifiedTree: tree, Tree: tree, Message: []byte(message)}
}

func fixtureEnvironment(fixture *testkit.GitFixture) process.Environment {
	environment := make(process.Environment)
	for _, row := range fixture.Environment() {
		key, value, ok := strings.Cut(row, "=")
		if ok {
			environment[key] = value
		}
	}
	return environment
}

func newRun(t testing.TB, fixture *testkit.GitFixture, approvalCommit contract.ObjectID) (*journal.RunStore, *journal.RunSnapshot, func()) {
	t.Helper()
	root, err := safefs.OpenRoot(fixture.Root)
	if err != nil {
		t.Fatalf("open journal root: %v", err)
	}
	snapshot := &journal.RunSnapshot{
		Schema: 2, RunID: testRunID, Slug: "moved-fixture", ApprovalSHA256: strings.Repeat("a", 64),
		ApprovalCommit: string(approvalCommit), TargetBranch: "main", Status: "running",
		OwnerPID: int64(os.Getpid()), OwnerStartedMS: time.Now().UnixMilli(), StartedMS: time.Now().UnixMilli(),
	}
	store, err := journal.NewRunStore(root, "state/runs/"+testRunID)
	if err != nil {
		_ = root.Close()
		t.Fatalf("create run store: %v", err)
	}
	if err := store.Create(*snapshot); err != nil {
		_ = root.Close()
		t.Fatalf("initialize run store: %v", err)
	}
	return store, snapshot, func() { _ = root.Close() }
}

func eventCount(events []journal.RunEvent, name string) int {
	count := 0
	for _, event := range events {
		if event.Event == name {
			count++
		}
	}
	return count
}

var _ Verifier = (*realGateVerifier)(nil)
var _ Repairer = (*conflictRepairer)(nil)
