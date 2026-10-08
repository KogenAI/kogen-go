package parallel

import (
	"context"
	"errors"
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
	"kogen-go/internal/build/recipe"
	"kogen-go/internal/build/repair"
	selection "kogen-go/internal/build/select"
	"kogen-go/internal/contract"
	"kogen-go/internal/gate"
	"kogen-go/internal/gitio"
	"kogen-go/internal/process"
	"kogen-go/internal/testkit"
	"kogen-go/internal/workspace"
)

const parallelTestRunID = "0123456789abcdef0123456789abcdef"
const parallelTestCacheKey = "cache_test-affinity"
const parallelTestBase = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type workspaceFactoryFunc func(context.Context, workspace.CloneRequest) (workspace.Workspace, error)

func (f workspaceFactoryFunc) Create(ctx context.Context, request workspace.CloneRequest) (workspace.Workspace, error) {
	return f(ctx, request)
}

type rungExecutorFunc func(context.Context, RungExecutionRequest) (ladder.RungResult, error)

func (f rungExecutorFunc) Run(ctx context.Context, request RungExecutionRequest) (ladder.RungResult, error) {
	return f(ctx, request)
}

type snapshotterFunc func(context.Context, ladder.SnapshotRequest) error

func (f snapshotterFunc) Preserve(ctx context.Context, request ladder.SnapshotRequest) error {
	return f(ctx, request)
}

type cleanerFunc func(context.Context, workspace.Workspace) error

func (f cleanerFunc) Remove(ctx context.Context, work workspace.Workspace) error {
	return f(ctx, work)
}

type observerFuncs struct {
	parallel func(context.Context, string, int) error
	rung     func(context.Context, ladder.RungRequest) error
}

func (o observerFuncs) ParallelStarted(ctx context.Context, runID string, attempts int) error {
	if o.parallel != nil {
		return o.parallel(ctx, runID, attempts)
	}
	return nil
}

func (o observerFuncs) RungStarted(ctx context.Context, request ladder.RungRequest) error {
	if o.rung != nil {
		return o.rung(ctx, request)
	}
	return nil
}

func TestHardParallelRedRungsContinueInStableOrder(t *testing.T) {
	root := t.TempDir()
	started := make(chan RungExecutionRequest, 2)
	release := make(chan struct{})
	var snapshots []ladder.SnapshotRequest
	var cleanups []string
	var observations []string
	var mu sync.Mutex
	controller := testController(t, Dependencies{
		Workspaces: testWorkspaceFactory(),
		Observer: observerFuncs{
			parallel: func(_ context.Context, runID string, attempts int) error {
				observations = append(observations, fmt.Sprintf("parallel_started:%s:%d", runID, attempts))
				return nil
			},
			rung: func(_ context.Context, request ladder.RungRequest) error {
				observations = append(observations, fmt.Sprintf("rung_started:%s", request.Workspace.Rung))
				return nil
			},
		},
		Rungs: rungExecutorFunc(func(_ context.Context, request RungExecutionRequest) (ladder.RungResult, error) {
			started <- request
			<-release
			return ladder.RungResult{
				Status: ladder.RungFailed, Reason: repair.ReasonNoProgress,
				FailureLines: []string{"acceptance A1 failed", "diff --git a/file b/file", "+implementation"},
				Candidate:    selection.Candidate{Diff: []byte("diff --git a/file b/file\n+implementation\n")},
			}, nil
		}),
		Snapshots: snapshotterFunc(func(_ context.Context, request ladder.SnapshotRequest) error {
			mu.Lock()
			snapshots = append(snapshots, request)
			mu.Unlock()
			return nil
		}),
		Cleaner: cleanerFunc(func(_ context.Context, work workspace.Workspace) error {
			mu.Lock()
			cleanups = append(cleanups, work.Rung)
			mu.Unlock()
			return nil
		}),
	})

	completed := make(chan struct {
		outcome Outcome
		err     error
	}, 1)
	go func() {
		outcome, err := controller.Run(context.Background(), hardRequest(root, 2, func() bool { return true }))
		completed <- struct {
			outcome Outcome
			err     error
		}{outcome, err}
	}()
	first := receiveStart(t, started)
	second := receiveStart(t, started)
	byOrder := map[int]RungExecutionRequest{first.Attempt.AttemptOrder: first, second.Attempt.AttemptOrder: second}
	first, firstExists := byOrder[1]
	second, secondExists := byOrder[2]
	if !firstExists || !secondExists {
		t.Fatalf("started rung orders = %d, %d; want R1 and R2", first.Attempt.AttemptOrder, second.Attempt.AttemptOrder)
	}
	if first.Attempt.Workspace.Path == second.Attempt.Workspace.Path ||
		first.Session == second.Session ||
		first.Session.ProtocolSession().Identity.ThreadID == second.Session.ProtocolSession().Identity.ThreadID {
		t.Fatal("parallel rungs did not receive isolated workspaces and conversation threads")
	}
	if first.Session.ProtocolSession().Identity.CacheKey != parallelTestCacheKey ||
		second.Session.ProtocolSession().Identity.CacheKey != parallelTestCacheKey ||
		first.Session.ProtocolSession().Identity.SessionID != parallelTestCacheKey ||
		second.Session.ProtocolSession().Identity.SessionID != parallelTestCacheKey {
		t.Fatal("parallel conversations did not use the persisted Build affinity for cache and session identity")
	}
	if first.Attempt.Plan == nil || second.Attempt.Plan == nil {
		t.Fatal("both plan-input rungs must receive the validated plan")
	}
	if got := strings.Join(observations, ","); got != "parallel_started:"+parallelTestRunID+":2,rung_started:R1,rung_started:R2" {
		t.Fatalf("start observation sequence = %s", got)
	}
	close(release)
	result := receiveOutcome(t, completed)
	if result.err != nil || result.outcome.Status != StatusContinue || result.outcome.Best == nil || result.outcome.Best.Request.Attempt.AttemptOrder != 1 {
		t.Fatalf("both-red continuation = status %q best=%+v err=%v", result.outcome.Status, result.outcome.Best, result.err)
	}
	if len(result.outcome.Results) != 2 || result.outcome.Results[0].Request.Attempt.AttemptOrder != 1 || result.outcome.Results[1].Request.Attempt.AttemptOrder != 2 {
		t.Fatalf("results were not returned in rung order: %+v", result.outcome.Results)
	}
	if len(result.outcome.FailureSummaries) != 2 || result.outcome.FailureSummaries[0].RungLabel != "R1" || result.outcome.FailureSummaries[1].RungLabel != "R2" {
		t.Fatalf("both-red continuation summaries = %+v", result.outcome.FailureSummaries)
	}
	if strings.Contains(strings.Join(result.outcome.FailureSummaries[0].Lines, "\n"), "implementation") {
		t.Fatalf("failure summary exposed diff content: %+v", result.outcome.FailureSummaries[0])
	}
	if got := rungNames(snapshots); got != "R1,R2" {
		t.Fatalf("snapshot observation order = %s", got)
	}
	if got := strings.Join(cleanups, ","); got != "R1,R2" {
		t.Fatalf("cleanup order = %s", got)
	}
}

func TestHardParallelCancelsAndCleansLoserAfterGreen(t *testing.T) {
	root := t.TempDir()
	green := parallelGreenReport(t)
	var snapshots []ladder.SnapshotRequest
	var cleanups []string
	var mu sync.Mutex
	controller := testController(t, Dependencies{
		Workspaces: testWorkspaceFactory(),
		Rungs: rungExecutorFunc(func(ctx context.Context, request RungExecutionRequest) (ladder.RungResult, error) {
			if request.Attempt.AttemptOrder == 1 {
				<-ctx.Done()
				return ladder.RungResult{}, ctx.Err()
			}
			return landableResult(green), nil
		}),
		Snapshots: snapshotterFunc(func(ctx context.Context, request ladder.SnapshotRequest) error {
			if ctx.Err() != nil {
				return errors.New("snapshot context was cancelled")
			}
			mu.Lock()
			snapshots = append(snapshots, request)
			mu.Unlock()
			return nil
		}),
		Cleaner: cleanerFunc(func(_ context.Context, work workspace.Workspace) error {
			mu.Lock()
			cleanups = append(cleanups, work.Rung)
			mu.Unlock()
			return nil
		}),
	})
	outcome, err := controller.Run(context.Background(), hardRequest(root, 2, func() bool { return true }))
	if err != nil || outcome.Status != StatusWinner || outcome.Winner == nil || outcome.Winner.Request.Attempt.AttemptOrder != 2 {
		t.Fatalf("green R2 outcome = status %q winner=%+v err=%v", outcome.Status, outcome.Winner, err)
	}
	if len(outcome.Results) != 2 || outcome.Results[0].Result.StopReason != "parallel_cancelled" || outcome.Results[1].Result.Status != ladder.RungLandable {
		t.Fatalf("winner cancellation results = %+v", outcome.Results)
	}
	if got := rungNames(snapshots); got != "R1,R2" {
		t.Fatalf("snapshot order = %s", got)
	}
	if got := strings.Join(cleanups, ","); got != "R1" {
		t.Fatalf("only the losing workspace should be removed, got %s", got)
	}
	if outcome.Winner.Request.Attempt.Workspace.Rung != "R2" {
		t.Fatalf("winner workspace = %+v", outcome.Winner.Request.Attempt.Workspace)
	}
}

func TestDualGreenUsesDeterministicSelectorOrder(t *testing.T) {
	root := t.TempDir()
	green := parallelGreenReport(t)
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var cleanups []string
	controller := testController(t, Dependencies{
		Workspaces: testWorkspaceFactory(),
		Rungs: rungExecutorFunc(func(_ context.Context, _ RungExecutionRequest) (ladder.RungResult, error) {
			started <- struct{}{}
			<-release // Deliberately finish both green results after cancellation races in.
			return landableResult(green), nil
		}),
		Snapshots: snapshotterFunc(func(context.Context, ladder.SnapshotRequest) error { return nil }),
		Cleaner: cleanerFunc(func(_ context.Context, work workspace.Workspace) error {
			cleanups = append(cleanups, work.Rung)
			return nil
		}),
	})
	completed := make(chan struct {
		outcome Outcome
		err     error
	}, 1)
	go func() {
		outcome, err := controller.Run(context.Background(), hardRequest(root, 2, func() bool { return true }))
		completed <- struct {
			outcome Outcome
			err     error
		}{outcome, err}
	}()
	receiveStartSignal(t, started)
	receiveStartSignal(t, started)
	close(release)
	result := receiveOutcome(t, completed)
	if result.err != nil || result.outcome.Status != StatusWinner || result.outcome.Winner == nil || result.outcome.Winner.Request.Attempt.AttemptOrder != 1 {
		t.Fatalf("dual-green winner = %+v, err=%v", result.outcome, result.err)
	}
	if len(result.outcome.Results) != 2 || result.outcome.Results[0].Result.Status != ladder.RungLandable || result.outcome.Results[1].Result.Status != ladder.RungLandable {
		t.Fatalf("both green results should remain observed: %+v", result.outcome.Results)
	}
	if got := strings.Join(cleanups, ","); got != "R2" {
		t.Fatalf("deterministic R1 winner should retain R1 and clean R2; got %s", got)
	}
}

func TestHardParallelRespectsHardAndMaxRungsEligibility(t *testing.T) {
	root := t.TempDir()
	called := 0
	controller := testController(t, Dependencies{
		Workspaces: testWorkspaceFactory(),
		Rungs: rungExecutorFunc(func(context.Context, RungExecutionRequest) (ladder.RungResult, error) {
			called++
			return ladder.RungResult{Status: ladder.RungFailed, Reason: repair.ReasonNoProgress}, nil
		}),
		Snapshots: snapshotterFunc(func(context.Context, ladder.SnapshotRequest) error { return nil }),
		Cleaner:   cleanerFunc(func(context.Context, workspace.Workspace) error { return nil }),
	})
	for _, test := range []struct {
		name string
		max  int
		diff recipe.Difficulty
	}{
		{name: "max_rungs_one", max: 1, diff: recipe.Hard},
		{name: "easy_plan", max: 2, diff: recipe.Easy},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := hardRequest(root, test.max, func() bool { return true })
			request.Plan.Difficulty = test.diff
			outcome, err := controller.Run(context.Background(), request)
			if err != nil || outcome.Applicable || outcome.Status != StatusNotApplicable {
				t.Fatalf("not-applicable outcome = %+v, err=%v", outcome, err)
			}
		})
	}
	request := hardRequest(root, 2, func() bool { return false })
	outcome, err := controller.Run(context.Background(), request)
	if err != nil || outcome.Applicable || outcome.Status != StatusNotApplicable {
		t.Fatalf("budget-exhausted outcome = %+v, err=%v", outcome, err)
	}
	if called != 0 {
		t.Fatalf("ineligible plan/cap ran %d rungs", called)
	}
}

func testController(t *testing.T, dependencies Dependencies) *Controller {
	t.Helper()
	if dependencies.Observer == nil {
		dependencies.Observer = observerFuncs{}
	}
	controller, err := New(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

func testWorkspaceFactory() workspaceFactoryFunc {
	return func(_ context.Context, request workspace.CloneRequest) (workspace.Workspace, error) {
		return workspace.Workspace{
			Path:  filepath.Join(request.WorkspacesDir, request.RunID+"-"+request.Rung),
			RunID: request.RunID, Rung: request.Rung, BaseCommit: request.BaseCommit,
		}, nil
	}
}

func hardRequest(root string, maxRungs int, budgetLeft func() bool) Request {
	parsed, _ := recipe.Parse("ladder")
	return Request{
		RunID: parallelTestRunID, CacheKey: parallelTestCacheKey,
		Source: filepath.Join(root, "origin"), WorkspacesDir: filepath.Join(root, "workspaces"),
		BaseCommit: contract.ObjectID(parallelTestBase), Build: recipe.Build{Recipe: parsed, Settings: recipe.Settings{
			MaxRungs: maxRungs, ExperimentalR4: boolPointer(false), RepeatFrom: recipe.DefaultRepeatFrom(),
		}},
		Plan:       &recipe.Plan{Difficulty: recipe.Hard, Text: "Difficulty: hard\n## Acceptance criteria\n## Technical approach\n## Implementation steps"},
		BudgetLeft: budgetLeft,
	}
}

func boolPointer(value bool) *bool { return &value }

func receiveStart(t *testing.T, started <-chan RungExecutionRequest) RungExecutionRequest {
	t.Helper()
	select {
	case request := <-started:
		return request
	case <-time.After(3 * time.Second):
		t.Fatal("both parallel rung workers did not start")
		return RungExecutionRequest{}
	}
}

func receiveStartSignal(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("both parallel rung workers did not start")
	}
}

func receiveOutcome(t *testing.T, completed <-chan struct {
	outcome Outcome
	err     error
}) struct {
	outcome Outcome
	err     error
} {
	t.Helper()
	select {
	case result := <-completed:
		return result
	case <-time.After(8 * time.Second):
		t.Fatal("parallel controller did not finish")
		return struct {
			outcome Outcome
			err     error
		}{}
	}
}

func landableResult(report *gate.GateReport) ladder.RungResult {
	return ladder.RungResult{Status: ladder.RungLandable, Candidate: selection.Candidate{Gate: report}}
}

func rungNames(requests []ladder.SnapshotRequest) string {
	names := make([]string, len(requests))
	for i, request := range requests {
		names[i] = request.Workspace.Rung
	}
	return strings.Join(names, ",")
}

type passingAcceptanceRunner struct{}

func (passingAcceptanceRunner) Run(_ context.Context, input gate.AcceptanceExecution) (acceptance.Result, error) {
	itemPass := make(map[string]bool, len(input.Request.ExpectedItems))
	for _, id := range input.Request.ExpectedItems {
		itemPass[id] = true
	}
	exit := 0
	return acceptance.Result{Process: contract.ProcessResult{ExitStatus: &exit}, ItemPass: itemPass}, nil
}

func parallelGreenReport(t *testing.T) *gate.GateReport {
	t.Helper()
	fixture := testkit.NewGitFixture(t)
	writeParallelFixture(t, filepath.Join(fixture.Checkout, ".kogen/acceptance/greet.sh"), []byte("approved acceptance source\n"), 0o644)
	writeParallelFixture(t, filepath.Join(fixture.Checkout, ".kogen/intents/greet/intent.md"), []byte("approved intent\n"), 0o644)
	writeParallelFixture(t, filepath.Join(fixture.Checkout, "lib/app.txt"), []byte("base\n"), 0o644)
	fixture.Run(t, "add", ".")
	fixture.Run(t, "commit", "--quiet", "--message", "parallel green fixture")
	fixture.Run(t, "push", "--quiet", "origin", "main")
	base := filepath.Join(fixture.Root, "base")
	candidate := filepath.Join(fixture.Root, "candidate")
	fixture.RunIn(t, fixture.Root, "clone", "--quiet", fixture.Origin, base)
	fixture.RunIn(t, fixture.Root, "clone", "--quiet", fixture.Origin, candidate)
	git := gitio.NewWorkspace(process.Supervisor{})
	environment := make(process.Environment)
	for _, entry := range fixture.Environment() {
		key, value, found := strings.Cut(entry, "=")
		if found {
			environment[key] = value
		}
	}
	policy := gitio.WorkspacePolicy(candidate, environment)
	commit := strings.TrimSpace(string(fixture.RunIn(t, candidate, "rev-parse", "HEAD")))
	metadata, err := gitio.LoadBaseMetadata(context.Background(), git, policy, contract.ObjectID(commit))
	if err != nil {
		t.Fatalf("load green fixture base metadata: %v", err)
	}
	trees := acceptance.CandidateTree{Git: git, Policy: policy, Base: metadata}
	baseTree, err := trees.Snapshot(context.Background(), base)
	if err != nil {
		t.Fatalf("snapshot green fixture base: %v", err)
	}
	runDir := filepath.Join(fixture.Root, "run")
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatalf("create gate run directory: %v", err)
	}
	report, err := gate.Run(context.Background(), gate.Request{
		Processes: process.Supervisor{}, AcceptanceRunner: passingAcceptanceRunner{}, Trees: trees,
		BaseWorkspace: base, CandidateWorkspace: candidate, RunDir: runDir,
		ExpectedBaseTree: baseTree, ApprovalSHA256: strings.Repeat("a", 64),
		Acceptance: gate.AcceptancePlan{
			Request: acceptancecommand.Request{Config: acceptancecommand.Config{
				Extension: ".sh", CandidateDir: "test/acceptance", Run: []string{"sh", "{path}"}, Timeout: time.Second,
			}, Slug: "greet", ExpectedItems: []string{"A1"}},
			ApprovedBytes: []byte("approved acceptance source\n"), ChangeItems: []string{"A1"},
		},
	})
	if err != nil {
		t.Fatalf("run green fixture gate: %v", err)
	}
	if !report.IsLandable() {
		t.Fatal("green fixture did not produce a landable report")
	}
	return report
}

func writeParallelFixture(t *testing.T, filename string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(filename, content, mode); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
}
