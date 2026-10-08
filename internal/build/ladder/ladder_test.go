package ladder

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"kogen-go/internal/build/recipe"
	"kogen-go/internal/build/repair"
	"kogen-go/internal/build/select"
	"kogen-go/internal/contract"
	"kogen-go/internal/workspace"
)

const testRunID = "0123456789abcdef0123456789abcdef"

type workspaceFactoryFunc func(context.Context, workspace.CloneRequest) (workspace.Workspace, error)

func (f workspaceFactoryFunc) Create(ctx context.Context, request workspace.CloneRequest) (workspace.Workspace, error) {
	return f(ctx, request)
}

type rungExecutorFunc func(context.Context, RungRequest) (RungResult, error)

func (f rungExecutorFunc) Run(ctx context.Context, request RungRequest) (RungResult, error) {
	return f(ctx, request)
}

type snapshotterFunc func(context.Context, SnapshotRequest) error

func (f snapshotterFunc) Preserve(ctx context.Context, request SnapshotRequest) error {
	return f(ctx, request)
}

func TestRunEscalatesWithFreshWorkspacesPlanAndDiffFreeFailureSummaries(t *testing.T) {
	root := t.TempDir()
	build := ladderBuild(t, 3, nil)
	var workspaceRequests []workspace.CloneRequest
	var rungRequests []RungRequest
	var snapshots []SnapshotRequest
	controller, err := New(Dependencies{
		Workspaces: workspaceFactoryFunc(func(_ context.Context, request workspace.CloneRequest) (workspace.Workspace, error) {
			workspaceRequests = append(workspaceRequests, request)
			return workspace.Workspace{
				Path:  filepath.Join(request.WorkspacesDir, request.RunID+"-"+request.Rung),
				RunID: request.RunID, Rung: request.Rung, BaseCommit: request.BaseCommit,
			}, nil
		}),
		Rungs: rungExecutorFunc(func(_ context.Context, request RungRequest) (RungResult, error) {
			rungRequests = append(rungRequests, cloneAttemptRequest(request))
			diff := []byte("diff --git a/lib/file.go b/lib/file.go\n@@ -1 +1 @@\n-old\n+new\n")
			return RungResult{
				Status: RungFailed, Reason: repair.ReasonRepairCap,
				FailureLines: []string{
					"first failure", "diff --git a/lib/file.go b/lib/file.go", "@@ -1 +1 @@",
					"-old", "+new", "acceptance A2: failed", "gate: 1 errors",
				},
				Candidate: selection.Candidate{Diff: diff},
			}, nil
		}),
		Snapshots: snapshotterFunc(func(_ context.Context, request SnapshotRequest) error {
			snapshots = append(snapshots, request)
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	budgetChecks := 0
	outcome, err := controller.Run(context.Background(), Request{
		RunID: testRunID, Source: filepath.Join(root, "origin"), WorkspacesDir: filepath.Join(root, "workspaces"),
		BaseCommit: contract.ObjectID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), Build: build,
		Plan:       &recipe.Plan{Difficulty: recipe.Easy, Text: "Difficulty: easy\n## Acceptance criteria\n## Technical approach\n## Implementation steps"},
		BudgetLeft: func() bool { budgetChecks++; return budgetChecks <= 3 },
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != StatusFailed || outcome.Reason != "best_candidate" || outcome.LastAttemptReason != string(repair.ReasonRepairCap) {
		t.Fatalf("failed ladder outcome = %+v", outcome)
	}
	if len(workspaceRequests) != 3 || len(rungRequests) != 3 || len(snapshots) != 3 || len(outcome.Candidates) != 3 {
		t.Fatalf("attempts/workspaces/snapshots/candidates = %d/%d/%d/%d", len(rungRequests), len(workspaceRequests), len(snapshots), len(outcome.Candidates))
	}
	for i, request := range workspaceRequests {
		if request.Rung != fmt.Sprintf("R%d", i+1) || request.BaseCommit != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
			t.Fatalf("workspace request %d = %+v", i, request)
		}
		if outcome.Workspaces[i].Path == outcome.Workspaces[0].Path && i != 0 {
			t.Fatalf("attempt %d reused workspace %q", i+1, outcome.Workspaces[i].Path)
		}
		if snapshots[i].Candidate.Ref != outcome.Candidates[i].Ref || snapshots[i].CandidateRef != outcome.Candidates[i].Ref {
			t.Fatalf("attempt %d snapshot reference = %+v, candidate = %+v", i+1, snapshots[i], outcome.Candidates[i])
		}
	}
	if rungRequests[0].Plan == nil || rungRequests[1].Plan == nil || rungRequests[2].Plan == nil {
		t.Fatal("plan was not supplied to later plan-based rungs")
	}
	if rungRequests[0].EscalationReason != "" || rungRequests[1].EscalationReason != string(repair.ReasonRepairCap) {
		t.Fatalf("escalation reasons = %q, %q", rungRequests[0].EscalationReason, rungRequests[1].EscalationReason)
	}
	if len(rungRequests[1].PriorFailures) != 1 {
		t.Fatalf("second attempt prior summaries = %+v", rungRequests[1].PriorFailures)
	}
	for _, summary := range rungRequests[1].PriorFailures {
		if strings.Contains(strings.Join(summary.Lines, "\n"), "diff --git") || strings.Contains(strings.Join(summary.Lines, "\n"), "old") || strings.Contains(strings.Join(summary.Lines, "\n"), "new") {
			t.Fatalf("prior summary exposed a diff: %+v", summary)
		}
		if len(summary.Lines) > 5 {
			t.Fatalf("prior summary contains %d lines", len(summary.Lines))
		}
		for _, line := range summary.Lines {
			if len([]rune(line)) > 180 {
				t.Fatalf("prior summary line exceeds 180 runes: %q", line)
			}
		}
	}
}

func TestRunRepeatsUseConfiguredRawRungAndExcludePlanFromRawInput(t *testing.T) {
	root := t.TempDir()
	experimentalR4 := true
	build := ladderBuild(t, 4, &experimentalR4)
	var attempts []RungRequest
	var preserved []SnapshotRequest
	controller := mustController(t, Dependencies{
		Workspaces: testWorkspaceFactory(),
		Rungs: rungExecutorFunc(func(_ context.Context, request RungRequest) (RungResult, error) {
			attempts = append(attempts, cloneAttemptRequest(request))
			if len(attempts) == 6 {
				return RungResult{Status: RungStopped, StopReason: "provider_outage"}, nil
			}
			return RungResult{Status: RungFailed, Reason: repair.ReasonNoProgress, FailureLines: []string{"acceptance A1: failed"}}, nil
		}),
		Snapshots: snapshotterFunc(func(_ context.Context, request SnapshotRequest) error {
			preserved = append(preserved, request)
			return nil
		}),
	})
	outcome, err := controller.Run(context.Background(), validRequest(root, build, func() bool { return true }))
	var stopped *StopError
	if !errors.As(err, &stopped) || stopped.Reason != "provider_outage" {
		t.Fatalf("stop error = %v", err)
	}
	if outcome.Status != StatusStopped || outcome.Reason != "provider_outage" || len(attempts) != 6 || len(preserved) != 6 {
		t.Fatalf("stopped outcome/attempts/snapshots = %+v / %d / %d", outcome, len(attempts), len(preserved))
	}
	want := []string{"builder", "sol-medium", "sol-high", "raw-request", "sol-high-2", "raw-request-2"}
	for index, request := range attempts {
		if request.Attempt.Name != want[index] {
			t.Fatalf("attempt %d name = %q, want %q", index+1, request.Attempt.Name, want[index])
		}
		if index == 5 && request.Plan != nil {
			t.Fatal("raw-request repeat received the implementation plan")
		}
		if index == 4 && request.Plan == nil {
			t.Fatal("plan-based repeated rung did not receive the plan")
		}
		if index > 0 && request.EscalationReason != string(repair.ReasonNoProgress) {
			t.Fatalf("attempt %d escalation reason = %q", index+1, request.EscalationReason)
		}
	}
	if outcome.Build.Settings.ExperimentalR4 == nil || !*outcome.Build.Settings.ExperimentalR4 {
		t.Fatalf("raw experimental_r4 config was not retained: %+v", outcome.Build.Settings)
	}
}

func TestRunRespectsMaxRungsAndDistinguishesFailedFromStopped(t *testing.T) {
	root := t.TempDir()
	build := ladderBuild(t, 2, nil)
	runs := 0
	controller := mustController(t, Dependencies{
		Workspaces: testWorkspaceFactory(),
		Rungs: rungExecutorFunc(func(context.Context, RungRequest) (RungResult, error) {
			runs++
			return RungResult{Status: RungFailed, Reason: repair.ReasonUnchanged}, nil
		}),
		Snapshots: snapshotterFunc(func(context.Context, SnapshotRequest) error { return nil }),
	})
	outcome, err := controller.Run(context.Background(), validRequest(root, build, func() bool { return true }))
	if err != nil || outcome.Status != StatusFailed || runs != 2 || len(outcome.Candidates) != 2 {
		t.Fatalf("max_rungs outcome = %+v, runs=%d, err=%v", outcome, runs, err)
	}

	runs = 0
	controller = mustController(t, Dependencies{
		Workspaces: testWorkspaceFactory(),
		Rungs: rungExecutorFunc(func(context.Context, RungRequest) (RungResult, error) {
			runs++
			return RungResult{Status: RungStopped, StopReason: "login_required"}, nil
		}),
		Snapshots: snapshotterFunc(func(context.Context, SnapshotRequest) error { return nil }),
	})
	outcome, err = controller.Run(context.Background(), validRequest(root, build, func() bool { return true }))
	var stopped *StopError
	if !errors.As(err, &stopped) || outcome.Status != StatusStopped || outcome.Reason != "login_required" || runs != 1 {
		t.Fatalf("stop outcome = %+v, runs=%d, err=%v", outcome, runs, err)
	}
}

func TestRunStopsAtUnresolvedRawRungConfigAndKeepsEarlierSnapshots(t *testing.T) {
	root := t.TempDir()
	build := ladderBuild(t, 4, nil)
	runs, snapshots := 0, 0
	controller := mustController(t, Dependencies{
		Workspaces: testWorkspaceFactory(),
		Rungs: rungExecutorFunc(func(context.Context, RungRequest) (RungResult, error) {
			runs++
			return RungResult{Status: RungFailed, Reason: repair.ReasonRepairCap}, nil
		}),
		Snapshots: snapshotterFunc(func(context.Context, SnapshotRequest) error {
			snapshots++
			return nil
		}),
	})
	outcome, err := controller.Run(context.Background(), validRequest(root, build, func() bool { return true }))
	var stopped *StopError
	if !errors.As(err, &stopped) || stopped.Reason != "recipe_attempt_invalid" || !strings.Contains(err.Error(), "experimental_r4") {
		t.Fatalf("unresolved R4 result = %+v, err=%v", outcome, err)
	}
	if outcome.Status != StatusStopped || runs != 3 || snapshots != 3 || len(outcome.Candidates) != 3 {
		t.Fatalf("unresolved R4 discarded earlier results: outcome=%+v runs=%d snapshots=%d", outcome, runs, snapshots)
	}
}

func TestRunRejectsWorkspaceReuseWithoutLosingPriorSnapshot(t *testing.T) {
	root := t.TempDir()
	build := ladderBuild(t, 3, nil)
	created := 0
	snapshots := 0
	controller := mustController(t, Dependencies{
		Workspaces: workspaceFactoryFunc(func(_ context.Context, request workspace.CloneRequest) (workspace.Workspace, error) {
			created++
			path := filepath.Join(request.WorkspacesDir, request.RunID+"-R1")
			return workspace.Workspace{Path: path, RunID: request.RunID, Rung: request.Rung, BaseCommit: request.BaseCommit}, nil
		}),
		Rungs: rungExecutorFunc(func(context.Context, RungRequest) (RungResult, error) {
			return RungResult{Status: RungFailed, Reason: repair.ReasonNoProgress}, nil
		}),
		Snapshots: snapshotterFunc(func(context.Context, SnapshotRequest) error {
			snapshots++
			return nil
		}),
	})
	outcome, err := controller.Run(context.Background(), validRequest(root, build, func() bool { return true }))
	var stopped *StopError
	if !errors.As(err, &stopped) || stopped.Reason != "workspace_invalid" || outcome.Status != StatusStopped {
		t.Fatalf("workspace reuse result = %+v, err=%v", outcome, err)
	}
	if created != 2 || snapshots != 1 || len(outcome.Candidates) != 1 {
		t.Fatalf("workspace reuse changed prior preservation: created=%d snapshots=%d candidates=%d", created, snapshots, len(outcome.Candidates))
	}
}

func ladderBuild(t *testing.T, maxRungs int, experimentalR4 *bool) recipe.Build {
	t.Helper()
	parsed, err := recipe.Parse("ladder")
	if err != nil {
		t.Fatal(err)
	}
	return recipe.Build{Recipe: parsed, Settings: recipe.Settings{
		MaxRungs: maxRungs, ExperimentalR4: experimentalR4, RepeatFrom: recipe.DefaultRepeatFrom(),
	}}
}

func testWorkspaceFactory() WorkspaceFactory {
	return workspaceFactoryFunc(func(_ context.Context, request workspace.CloneRequest) (workspace.Workspace, error) {
		return workspace.Workspace{
			Path:  filepath.Join(request.WorkspacesDir, request.RunID+"-"+request.Rung),
			RunID: request.RunID, Rung: request.Rung, BaseCommit: request.BaseCommit,
		}, nil
	})
}

func validRequest(root string, build recipe.Build, budget func() bool) Request {
	return Request{
		RunID: testRunID, Source: filepath.Join(root, "origin"), WorkspacesDir: filepath.Join(root, "workspaces"),
		BaseCommit: contract.ObjectID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), Build: build,
		Plan:       &recipe.Plan{Difficulty: recipe.Easy, Text: "Difficulty: easy\n## Acceptance criteria\n## Technical approach\n## Implementation steps"},
		BudgetLeft: budget,
	}
}

func mustController(t *testing.T, deps Dependencies) *Controller {
	t.Helper()
	controller, err := New(deps)
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

func cloneAttemptRequest(request RungRequest) RungRequest {
	request.Build = cloneBuild(request.Build)
	if request.Plan != nil {
		plan := *request.Plan
		request.Plan = &plan
	}
	request.PriorFailures = cloneSummaries(request.PriorFailures)
	return request
}
