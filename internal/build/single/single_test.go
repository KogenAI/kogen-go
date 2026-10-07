package single

import (
	"context"
	"errors"
	"testing"

	"kogen-go/internal/contract"
	"kogen-go/internal/gate"
	"kogen-go/internal/journal"
	"kogen-go/internal/landing/integrate"
	"kogen-go/internal/project"
	"kogen-go/internal/workspace"
)

type approvalSourceFunc func(context.Context, *project.Resolution, string) (*Approval, error)

func (f approvalSourceFunc) Load(ctx context.Context, resolved *project.Resolution, slug string) (*Approval, error) {
	return f(ctx, resolved, slug)
}

type claimSourceFunc func(context.Context, *project.Resolution, string) (Claim, bool, error)

func (f claimSourceFunc) Acquire(ctx context.Context, resolved *project.Resolution, runID string) (Claim, bool, error) {
	return f(ctx, resolved, runID)
}

type testAgent struct{ calls int }

func (a *testAgent) Plan(context.Context, PlanRequest) (PlanResult, error) {
	a.calls++
	return PlanResult{}, nil
}

func (a *testAgent) Develop(context.Context, DevelopRequest) (Development, error) {
	a.calls++
	return Development{}, nil
}

type testWorkspaceFactory struct{}

func (testWorkspaceFactory) Create(context.Context, workspace.CloneRequest) (workspace.Workspace, error) {
	return workspace.Workspace{}, errors.New("unexpected workspace creation")
}

type testVerifier struct{}

func (testVerifier) Verify(context.Context, VerifyRequest) (*gate.GateReport, error) {
	return nil, errors.New("unexpected verification")
}

type testLander struct{}

func (testLander) Land(context.Context, LandingRequest) (integrate.Result, error) {
	return integrate.Result{}, errors.New("unexpected landing")
}

type testCandidatePreserver struct{}

func (testCandidatePreserver) Preserve(context.Context, CandidateRequest) (journal.RecoveryRecord, error) {
	return journal.RecoveryRecord{}, errors.New("unexpected candidate preservation")
}

func TestInvalidApprovalStopsBeforeClaimAndProvider(t *testing.T) {
	loaded := false
	claimed := false
	agent := &testAgent{}
	controller, err := NewController(Dependencies{
		Approvals: approvalSourceFunc(func(context.Context, *project.Resolution, string) (*Approval, error) {
			loaded = true
			return &Approval{}, nil
		}),
		Claims: claimSourceFunc(func(context.Context, *project.Resolution, string) (Claim, bool, error) {
			claimed = true
			return nil, false, nil
		}),
		Agent: agent, Workspaces: testWorkspaceFactory{}, Verifier: testVerifier{},
		Lander: testLander{}, Candidates: testCandidatePreserver{}, Git: invalidApprovalGit{},
	})
	if err != nil {
		t.Fatal(err)
	}

	outcome, runErr := controller.Run(context.Background(), Request{
		Project: &project.Resolution{Origin: "/repo", StateRoot: "/state", Base: "main"},
		Slug:    "valid-slug",
	})
	var failure *contract.Failure
	if !errors.As(runErr, &failure) || outcome.Status != "stopped" || outcome.Reason != "approval_invalid" {
		t.Fatalf("invalid approval result = %+v, error = %v", outcome, runErr)
	}
	if !loaded || claimed || agent.calls != 0 {
		t.Fatalf("approval loaded=%t claim acquired=%t provider calls=%d", loaded, claimed, agent.calls)
	}
}

type invalidApprovalGit struct{}

func (invalidApprovalGit) Exec(context.Context, []string, []byte, contract.GitPolicy) (contract.GitResult, error) {
	return contract.GitResult{}, errors.New("unexpected Git invocation")
}
