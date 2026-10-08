package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kogen-go/internal/acceptance"
	acceptancecommand "kogen-go/internal/acceptance/command"
	"kogen-go/internal/build/ladder"
	"kogen-go/internal/build/parallel"
	"kogen-go/internal/build/recipe"
	selection "kogen-go/internal/build/select"
	"kogen-go/internal/contract"
	"kogen-go/internal/gate"
	"kogen-go/internal/gitio"
	"kogen-go/internal/process"
	"kogen-go/internal/project"
	"kogen-go/internal/safefs"
	"kogen-go/internal/testkit"
	"kogen-go/internal/workspace"
	"kogen-go/internal/yamlmini"
)

const (
	ladderWiringTestRunID  = "0123456789abcdef0123456789abcdef"
	ladderWiringTestCache  = "cache_build-affinity"
	ladderWiringTestCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestLadderWiringResolvesRolesAndContinuesAfterHardDualRed(t *testing.T) {
	resolved := ladderWiringProject(3, nil)
	resolved.Roles.Effective[contract.RoleName("builder")] = contract.RoleSettings{
		Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high",
	}
	resolved.Roles.Effective[contract.RoleName("planner")] = contract.RoleSettings{
		Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high",
	}
	workspaceRequests := make([]workspace.CloneRequest, 0, 3)
	snapshots := make([]ladder.SnapshotRequest, 0, 3)
	var callsMu sync.Mutex
	var calls []parallel.RungExecutionRequest
	wiring, err := newLadderWiring(resolved, ladderWiringDependencies{
		Workspaces: wiringWorkspaceFactory(func(_ context.Context, request workspace.CloneRequest) (workspace.Workspace, error) {
			workspaceRequests = append(workspaceRequests, request)
			return workspace.Workspace{Path: filepath.Join(request.WorkspacesDir, request.RunID+"-"+request.Rung), RunID: request.RunID, Rung: request.Rung, BaseCommit: request.BaseCommit}, nil
		}),
		Rungs: wiringRungExecutor(func(_ context.Context, request parallel.RungExecutionRequest) (ladder.RungResult, error) {
			callsMu.Lock()
			calls = append(calls, request)
			callsMu.Unlock()
			if request.Session == nil {
				return ladder.RungResult{}, fmt.Errorf("rung %s has no persistent session", request.Attempt.Workspace.Rung)
			}
			identity := request.Session.ProtocolSession().Identity
			if identity.CacheKey != ladderWiringTestCache || identity.SessionID != ladderWiringTestCache {
				return ladder.RungResult{}, fmt.Errorf("rung %s lost Build affinity: %+v", request.Attempt.Workspace.Rung, identity)
			}
			if request.Attempt.Workspace.Rung == "R3" && (len(request.Attempt.PriorFailures) != 2 || request.Attempt.PriorFailures[0].RungLabel != "R1" || request.Attempt.PriorFailures[1].RungLabel != "R2") {
				return ladder.RungResult{}, fmt.Errorf("R3 prior failures were not carried forward: %+v", request.Attempt.PriorFailures)
			}
			return ladder.RungResult{
				Status: ladder.RungFailed, Reason: "no_progress",
				FailureLines: []string{"acceptance A1: failed"},
				Candidate:    selection.Candidate{Diff: []byte(fmt.Sprintf("diff --git a/rung-%s b/rung-%s\n+candidate\n", request.Attempt.Workspace.Rung, request.Attempt.Workspace.Rung))},
			}, nil
		}),
		Snapshots: wiringSnapshotter(func(_ context.Context, request ladder.SnapshotRequest) error {
			snapshots = append(snapshots, request)
			return nil
		}),
		Cleaner:  wiringCleaner(func(context.Context, workspace.Workspace) error { return nil }),
		Observer: wiringObserver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := wiring.build.Recipe.Rungs[0].Model; got.Model != "gpt-6.1-sol" || got.Effort != "high" {
		t.Fatalf("first rung ignored the effective builder role: %+v", got)
	}
	if got := wiring.build.Recipe.Rungs[1].Model; got.Model != "gpt-6.1-sol" || got.Effort != "medium" {
		t.Fatalf("recipe-owned escalation model was overwritten by the builder role: %+v", got)
	}

	budgetChecks := 0
	result, err := wiring.Run(context.Background(), ladderWiringRequest{
		RunID: ladderWiringTestRunID, CacheKey: ladderWiringTestCache,
		Source: filepath.Join(t.TempDir(), "origin"), WorkspacesDir: filepath.Join(t.TempDir(), "workspaces"),
		BaseCommit: contract.ObjectID(ladderWiringTestCommit),
		Plan:       &recipe.Plan{Difficulty: recipe.Hard, Text: "Difficulty: hard\n## Acceptance criteria\n## Technical approach\n## Implementation steps"},
		BudgetLeft: func() bool {
			budgetChecks++
			return budgetChecks <= 2 // parallel entry and the single remaining rung
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ladder.StatusFailed || !result.HasSelection || result.Selection.Demoted || result.Selection.AuditMode != selection.AuditModeObservational || len(result.Selection.AdvisoryItems) != 0 {
		t.Fatalf("dual-red ladder result/report = %+v", result)
	}
	if got := rungLabels(result.Candidates); got != "R1,R2,R3" {
		t.Fatalf("candidate snapshots = %s, want stable R1/R2/R3 labels", got)
	}
	if got := snapshotLabels(snapshots); got != "R1:1,R2:2,R3:3" {
		t.Fatalf("preserved snapshots = %s, want globally ordered rung identities", got)
	}
	if len(workspaceRequests) != 3 || workspaceRequests[2].Rung != "R3" {
		t.Fatalf("later rung did not receive its own R3 workspace: %+v", workspaceRequests)
	}
	if len(calls) != 3 || calls[0].Session == calls[1].Session || calls[0].Session.ProtocolSession().Identity.ThreadID == calls[1].Session.ProtocolSession().Identity.ThreadID {
		t.Fatalf("parallel session objects/threads are not isolated: %+v", calls)
	}
	for _, call := range calls {
		if call.Session.ProtocolSession().Identity.SessionID != ladderWiringTestCache {
			t.Fatalf("session id does not equal persisted Build affinity: %+v", call.Session.ProtocolSession().Identity)
		}
	}
}

func TestRemainingLadderRungsHonorsExperimentalR4Policy(t *testing.T) {
	for _, test := range []struct {
		name           string
		maxRungs       int
		experimental   *bool
		wantAvailable  int
		wantUnresolved bool
	}{
		{name: "unresolved raw rung", maxRungs: 4, wantAvailable: 3, wantUnresolved: true},
		{name: "raw rung disabled", maxRungs: 4, experimental: boolPointer(false), wantAvailable: 3},
		{name: "raw rung enabled", maxRungs: 4, experimental: boolPointer(true), wantAvailable: 4},
		{name: "three-rung cap", maxRungs: 3, wantAvailable: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolved := ladderWiringProject(test.maxRungs, test.experimental)
			build, err := recipe.Load(resolved)
			if err != nil {
				t.Fatal(err)
			}
			available, unresolved := remainingLadderRungs(build)
			if len(available) != test.wantAvailable || unresolved != test.wantUnresolved {
				t.Fatalf("available rungs=%d unresolved=%t, want %d/%t", len(available), unresolved, test.wantAvailable, test.wantUnresolved)
			}
		})
	}
}

func TestLadderWiringCancelsLosingParallelRungAndKeepsGreenCandidateEligible(t *testing.T) {
	green := wiringGreenGate(t)
	green.RecordAuditAdvice([]gate.AuditAdvice{{ID: "A1", Verdict: "over_strict", Reason: "display-only test advice"}})
	resolved := ladderWiringProject(2, nil)
	var mu sync.Mutex
	var snapshots []ladder.SnapshotRequest
	var cleaned []string
	wiring, err := newLadderWiring(resolved, ladderWiringDependencies{
		Workspaces: wiringWorkspaceFactory(func(_ context.Context, request workspace.CloneRequest) (workspace.Workspace, error) {
			return workspace.Workspace{Path: filepath.Join(request.WorkspacesDir, request.RunID+"-"+request.Rung), RunID: request.RunID, Rung: request.Rung, BaseCommit: request.BaseCommit}, nil
		}),
		Rungs: wiringRungExecutor(func(ctx context.Context, request parallel.RungExecutionRequest) (ladder.RungResult, error) {
			if request.Attempt.Workspace.Rung == "R1" {
				<-ctx.Done()
				return ladder.RungResult{}, ctx.Err()
			}
			return ladder.RungResult{Status: ladder.RungLandable, Candidate: selection.Candidate{
				Diff: []byte("diff --git a/lib/app.go b/lib/app.go\n+landable\n"), Gate: green,
			}}, nil
		}),
		Snapshots: wiringSnapshotter(func(_ context.Context, request ladder.SnapshotRequest) error {
			mu.Lock()
			snapshots = append(snapshots, request)
			mu.Unlock()
			return nil
		}),
		Cleaner: wiringCleaner(func(_ context.Context, work workspace.Workspace) error {
			mu.Lock()
			cleaned = append(cleaned, work.Rung)
			mu.Unlock()
			return nil
		}),
		Observer: wiringObserver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := wiring.Run(context.Background(), ladderWiringRequest{
		RunID: ladderWiringTestRunID, CacheKey: ladderWiringTestCache,
		Source: filepath.Join(t.TempDir(), "origin"), WorkspacesDir: filepath.Join(t.TempDir(), "workspaces"),
		BaseCommit: contract.ObjectID(ladderWiringTestCommit),
		Plan:       &recipe.Plan{Difficulty: recipe.Hard, Text: "Difficulty: hard\n## Acceptance criteria\n## Technical approach\n## Implementation steps"},
		BudgetLeft: func() bool { return true },
	})
	if err != nil || result.Status != ladder.StatusReady || result.Selected == nil || result.Selected.Rung != "R2" {
		t.Fatalf("parallel green selection = status %q selected=%+v err=%v", result.Status, result.Selected, err)
	}
	if !result.Selected.Gate.IsLandable() || result.Selected.Gate.Counts() != (gate.AcceptanceCounts{Passed: 1, Total: 1}) {
		t.Fatal("observational auditor advice changed green candidate eligibility or counts")
	}
	if got := snapshotLabels(snapshots); got != "R1:1,R2:2" {
		t.Fatalf("parallel snapshots = %s", got)
	}
	if strings.Join(cleaned, ",") != "R1" {
		t.Fatalf("losing rung cleanup = %v, want only R1", cleaned)
	}
}

type wiringWorkspaceFactory func(context.Context, workspace.CloneRequest) (workspace.Workspace, error)

func (factory wiringWorkspaceFactory) Create(ctx context.Context, request workspace.CloneRequest) (workspace.Workspace, error) {
	return factory(ctx, request)
}

type wiringRungExecutor func(context.Context, parallel.RungExecutionRequest) (ladder.RungResult, error)

func (executor wiringRungExecutor) Run(ctx context.Context, request parallel.RungExecutionRequest) (ladder.RungResult, error) {
	return executor(ctx, request)
}

type wiringSnapshotter func(context.Context, ladder.SnapshotRequest) error

func (snapshotter wiringSnapshotter) Preserve(ctx context.Context, request ladder.SnapshotRequest) error {
	return snapshotter(ctx, request)
}

type wiringCleaner func(context.Context, workspace.Workspace) error

func (cleaner wiringCleaner) Remove(ctx context.Context, work workspace.Workspace) error {
	return cleaner(ctx, work)
}

type wiringObserver struct{}

func (wiringObserver) ParallelStarted(context.Context, string, int) error    { return nil }
func (wiringObserver) RungStarted(context.Context, ladder.RungRequest) error { return nil }

func ladderWiringProject(maxRungs int, experimentalR4 *bool) *project.Resolution {
	build := yamlmini.Mapping{
		"recipe": "ladder",
		"ladder": yamlmini.Mapping{"max_rungs": fmt.Sprint(maxRungs)},
	}
	if experimentalR4 != nil {
		build["ladder"].(yamlmini.Mapping)["experimental_r4"] = fmt.Sprint(*experimentalR4)
	}
	return &project.Resolution{
		Config: &project.Config{Raw: yamlmini.Mapping{"build": build}},
		Roles: contract.RoleManifest{Effective: map[contract.RoleName]contract.RoleSettings{
			"builder": {Provider: "chatgpt", Model: "gpt-6-luna", Effort: "max"},
			"planner": {Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high"},
		}},
	}
}

func boolPointer(value bool) *bool { return &value }

type wiringPassingAcceptance struct{}

func (wiringPassingAcceptance) Run(_ context.Context, execution gate.AcceptanceExecution) (acceptance.Result, error) {
	itemPass := make(map[string]bool, len(execution.Request.ExpectedItems))
	for _, id := range execution.Request.ExpectedItems {
		itemPass[id] = true
	}
	exit := 0
	return acceptance.Result{Process: contract.ProcessResult{ExitStatus: &exit}, ItemPass: itemPass}, nil
}

func wiringGreenGate(t *testing.T) *gate.GateReport {
	t.Helper()
	fixture := testkit.NewGitFixture(t)
	writeWiringFixture(t, filepath.Join(fixture.Checkout, ".kogen/acceptance/greet.sh"), []byte("approved acceptance source\n"), 0o644)
	writeWiringFixture(t, filepath.Join(fixture.Checkout, ".kogen/intents/greet/intent.md"), []byte("approved intent\n"), 0o644)
	writeWiringFixture(t, filepath.Join(fixture.Checkout, "lib/app.go"), []byte("package app\n"), 0o644)
	fixture.Run(t, "add", ".")
	fixture.Run(t, "commit", "--quiet", "--message", "ladder green fixture")
	fixture.Run(t, "push", "--quiet", "origin", "main")
	base, candidate := filepath.Join(fixture.Root, "base"), filepath.Join(fixture.Root, "candidate")
	fixture.RunIn(t, fixture.Root, "clone", "--quiet", fixture.Origin, base)
	fixture.RunIn(t, fixture.Root, "clone", "--quiet", fixture.Origin, candidate)
	git := gitio.NewWorkspace(process.Supervisor{})
	environment := make(process.Environment)
	for _, entry := range fixture.Environment() {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			environment[name] = value
		}
	}
	policy := gitio.WorkspacePolicy(candidate, environment)
	commit := strings.TrimSpace(string(fixture.RunIn(t, candidate, "rev-parse", "HEAD")))
	metadata, err := gitio.LoadBaseMetadata(context.Background(), git, policy, contract.ObjectID(commit))
	if err != nil {
		t.Fatalf("load green candidate base metadata: %v", err)
	}
	trees := acceptance.CandidateTree{Git: git, Policy: policy, Base: metadata}
	baseTree, err := trees.Snapshot(context.Background(), base)
	if err != nil {
		t.Fatalf("snapshot green base: %v", err)
	}
	runDir := filepath.Join(fixture.Root, "run")
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	report, err := gate.Run(context.Background(), gate.Request{
		Processes: process.Supervisor{}, AcceptanceRunner: wiringPassingAcceptance{}, Trees: trees, Roots: safefs.Opener{},
		BaseWorkspace: base, CandidateWorkspace: candidate, RunDir: runDir,
		ExpectedBaseTree: baseTree, ApprovalSHA256: strings.Repeat("a", 64),
		Acceptance: gate.AcceptancePlan{
			Request: acceptancecommand.Request{Config: acceptancecommand.Config{
				Extension: ".sh", CandidateDir: "test/acceptance", Run: []string{"sh", "{path}"}, Timeout: time.Second,
			}, Slug: "greet", ExpectedItems: []string{"A1"}},
			ApprovedBytes: []byte("approved acceptance source\n"), ChangeItems: []string{"A1"},
		},
	})
	if err != nil || !report.IsLandable() {
		t.Fatalf("create real green gate report: report=%v err=%v", report, err)
	}
	return report
}

func writeWiringFixture(t *testing.T, path string, contents []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, mode); err != nil {
		t.Fatal(err)
	}
}

func rungLabels(items []selection.Candidate) string {
	labels := make([]string, len(items))
	for index, item := range items {
		labels[index] = item.Rung
	}
	return strings.Join(labels, ",")
}

func snapshotLabels(items []ladder.SnapshotRequest) string {
	labels := make([]string, len(items))
	for index, item := range items {
		labels[index] = fmt.Sprintf("%s:%d", item.Candidate.Rung, item.Candidate.AttemptOrder)
	}
	return strings.Join(labels, ",")
}
