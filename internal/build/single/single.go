// Package single runs one approved Build rung from immutable approval through
// verification, landing or candidate preservation, and terminal cleanup.
package single

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/gate"
	"kogen-go/internal/gitio"
	"kogen-go/internal/intent"
	"kogen-go/internal/journal"
	"kogen-go/internal/landing/commit"
	"kogen-go/internal/landing/integrate"
	"kogen-go/internal/process"
	"kogen-go/internal/project"
	"kogen-go/internal/protection"
	"kogen-go/internal/provider/session"
	"kogen-go/internal/safefs"
	"kogen-go/internal/workspace"
	"kogen-go/internal/yamlmini"
)

const (
	RungName       = "R1"
	defaultBudget  = 60 * time.Minute
	defaultPlanMax = 500
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var runIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Approval is the immutable, validated package consumed by a Build. Approval
// loaders must resolve the approval ref and exact blobs; Run validates all
// byte digests again before it acquires the claim or creates state.
type Approval struct {
	Slug                 string
	Commit               contract.ObjectID
	ApprovalSHA256       string
	IntentSHA256         string
	TargetBranch         string
	BaseCommit           contract.ObjectID
	BaseTree             contract.ObjectID
	IntentBytes          []byte
	AcceptanceBytes      []byte
	AcceptanceSourcePath string
	CandidatePath        string
	Intent               *intent.Intent
	Protection           *protection.Protector
	Baseline             *gate.CheckBaseline
	Checks               []contract.CheckSpec
	Fixes                []contract.CheckSpec
}

// ApprovalSource reads an immutable approval package from the resolved
// project's approval ref. Load must not contact a provider or mutate state.
type ApprovalSource interface {
	Load(context.Context, *project.Resolution, string) (*Approval, error)
}

// Claim is the capability to release the Build claim this controller acquired.
type Claim interface {
	Release(context.Context) error
}

// ClaimSource takes the origin-wide Build claim after package validation.
// acquired=false means another run owns it and no run directory was created.
type ClaimSource interface {
	Acquire(context.Context, *project.Resolution, string) (claim Claim, acquired bool, err error)
}

// PlanRequest binds one planner conversation to this Build and its immutable
// base. The provider driver owns wire construction and request telemetry.
type PlanRequest struct {
	Project  *project.Resolution
	Approval *Approval
	Base     integrate.Base
	RunID    string
	CacheKey string
	RunDir   string
	Budget   time.Duration
	MaxWords int
	Session  *session.Conversation
	RunRoot  contract.RootedFS
}

type PlanResult struct {
	Text       string
	Difficulty string
}

// DevelopRequest gives one builder conversation the approved bytes, plan,
// private workspace and protection capability. A provider implementation
// must retain the same Session pointer through tool calls, finish guards and
// controller feedback until it returns.
type DevelopRequest struct {
	Project       *project.Resolution
	Approval      *Approval
	Base          integrate.Base
	RunID         string
	CacheKey      string
	RunDir        string
	Workspace     workspace.Workspace
	WorkspaceRoot contract.RootedFS
	RunRoot       contract.RootedFS
	Plan          PlanResult
	Budget        time.Duration
	Session       *session.Conversation
}

type Development struct {
	// Completion is "finish", "turn_cap", "wall_cap", or "budget". Cap
	// completion still proceeds to verification of the current workspace.
	Completion string
	Turns      int
	Repairs    int
}

// Agent performs real model-backed planning and development. It is deliberately
// stage-shaped so the controller can enforce call order and session identity.
type Agent interface {
	Plan(context.Context, PlanRequest) (PlanResult, error)
	Develop(context.Context, DevelopRequest) (Development, error)
}

type VerifyRequest struct {
	Project   *project.Resolution
	Approval  *Approval
	Base      integrate.Base
	Workspace workspace.Workspace
	RunDir    string
	Baseline  *gate.CheckBaseline
}

// Verifier returns the immutable report produced by the production gate.
type Verifier interface {
	Verify(context.Context, VerifyRequest) (*gate.GateReport, error)
}

// LandingRequest supplies the real one-parent commit and gate receipt to the
// moved-base/CAS integration layer.
type LandingRequest struct {
	Project   *project.Resolution
	Approval  *Approval
	Base      integrate.Base
	Workspace workspace.Workspace
	RunID     string
	Store     *journal.RunStore
	Snapshot  *journal.RunSnapshot
	Candidate commit.Result
	Report    *gate.GateReport
}

type Lander interface {
	Land(context.Context, LandingRequest) (integrate.Result, error)
}

type BaseRefreshRequest struct {
	Project  *project.Resolution
	Approval *Approval
	Current  integrate.Base
	RunID    string
	RunDir   string
	Store    *journal.RunStore
	Snapshot *journal.RunSnapshot
}

// BaseRefresher implements B2 when the target moved since approval. It must
// return a fresh exact-tree baseline before the planner or builder is called.
type BaseRefresher interface {
	Refresh(context.Context, BaseRefreshRequest) (integrate.Base, *gate.CheckBaseline, error)
}

type CandidateRequest struct {
	Project   *project.Resolution
	Workspace workspace.Workspace
	Base      integrate.Base
	RunID     string
	Store     *journal.RunStore
	Snapshot  *journal.RunSnapshot
}

// CandidatePreserver publishes the current workspace as an unverified,
// create-only candidate before terminal cleanup when it did not land.
type CandidatePreserver interface {
	Preserve(context.Context, CandidateRequest) (journal.RecoveryRecord, error)
}

// SandboxProbe returns a warning for an unavailable optional sandbox. Such a
// warning does not refuse a Build; a probe execution error does.
type SandboxProbe interface {
	Probe(context.Context, *project.Resolution, string) (warning string, err error)
}

type Dependencies struct {
	Approvals  ApprovalSource
	Claims     ClaimSource
	Agent      Agent
	Workspaces interface {
		Create(context.Context, workspace.CloneRequest) (workspace.Workspace, error)
	}
	Verifier     Verifier
	Lander       Lander
	BaseRefresh  BaseRefresher
	Candidates   CandidatePreserver
	Sandbox      SandboxProbe
	Git          contract.GitPort
	Processes    contract.ProcessRunner
	Environment  process.Environment
	OriginPolicy contract.GitPolicy
	Clock        contract.Clock
}

type Request struct {
	Project   *project.Resolution
	Slug      string
	DrainBase string
}

type Outcome struct {
	Status  string
	RunID   string
	Reason  string
	Commit  contract.ObjectID
	Verdict string
	Failure *contract.Failure
}

type Controller struct{ deps Dependencies }

func NewController(dependencies Dependencies) (*Controller, error) {
	if dependencies.Approvals == nil || dependencies.Claims == nil || dependencies.Agent == nil ||
		dependencies.Workspaces == nil || dependencies.Verifier == nil || dependencies.Lander == nil ||
		dependencies.Candidates == nil || dependencies.Git == nil {
		return nil, errors.New("single Build controller requires approval, claim, agent, workspace, gate, landing, candidate, and Git ports")
	}
	if dependencies.Clock == nil {
		dependencies.Clock = wallClock{}
	}
	if dependencies.Processes == nil {
		dependencies.Processes = process.Supervisor{}
	}
	return &Controller{deps: dependencies}, nil
}

// Run executes one Build. Approval is loaded and revalidated before claim,
// run creation, sandbox setup, or any provider call.
func (c *Controller) Run(ctx context.Context, request Request) (Outcome, error) {
	if ctx == nil || request.Project == nil || request.Project.Origin == "" || request.Project.StateRoot == "" || request.Project.Base == "" {
		return Outcome{Status: "stopped", Reason: "request_invalid"}, buildFailure("controller", "request_invalid", 70, errors.New("resolved project and context are required"))
	}
	if !slugPattern.MatchString(request.Slug) || len(request.Slug) < 3 || len(request.Slug) > 48 {
		return Outcome{Status: "stopped", Reason: "approval_invalid"}, buildFailure("controller", "approval_invalid", 1, errors.New("invalid Intent slug"))
	}
	approved, err := c.deps.Approvals.Load(ctx, request.Project, request.Slug)
	if err != nil {
		return Outcome{Status: "stopped", Reason: "approval_invalid"}, buildFailure("controller", "approval_invalid", 1, err)
	}
	if err := validateApproval(request.Project, request.Slug, approved); err != nil {
		return Outcome{Status: "stopped", Reason: "approval_invalid"}, buildFailure("controller", "approval_invalid", 1, err)
	}
	drainBase := request.DrainBase
	if drainBase == "" {
		drainBase = request.Project.Base
	}
	if approved.TargetBranch != drainBase {
		return Outcome{Status: "skipped", Reason: "branch_mismatch"}, nil
	}
	if err := c.validateReady(request.Project, approved); err != nil {
		return Outcome{Status: "stopped", Reason: "controller_invalid"}, buildFailure("controller", "controller_invalid", 70, err)
	}

	runID, err := newRunID()
	if err != nil {
		return Outcome{Status: "stopped", Reason: "run_identity_failed"}, buildFailure("controller", "run_identity_failed", 70, err)
	}
	cacheKey, err := session.NewRunAffinityKey()
	if err != nil {
		return Outcome{Status: "stopped", RunID: runID, Reason: "run_identity_failed"}, buildFailure("controller", "run_identity_failed", 70, err)
	}
	claim, acquired, err := c.deps.Claims.Acquire(ctx, request.Project, runID)
	if err != nil {
		return Outcome{Status: "stopped", RunID: runID, Reason: "claim_failed"}, buildFailure("environment", "claim_failed", 70, err)
	}
	if !acquired || claim == nil {
		return Outcome{Status: "stopped", RunID: runID, Reason: "build_already_claimed"}, buildFailure("environment", "build_already_claimed", 75, errors.New("another Build owns the project claim"))
	}

	stateRoot, err := safefs.OpenRoot(request.Project.StateRoot)
	if err != nil {
		_ = claim.Release(ctx)
		return Outcome{Status: "stopped", RunID: runID, Reason: "state_root_unavailable"}, buildFailure("environment", "state_root_unavailable", 70, err)
	}
	defer stateRoot.Close()
	if err := stateRoot.MkdirAll("runs", 0o700); err != nil {
		_ = claim.Release(ctx)
		return Outcome{Status: "stopped", RunID: runID, Reason: "state_root_unavailable"}, buildFailure("environment", "state_root_unavailable", 70, err)
	}
	if err := stateRoot.MkdirAll("workspaces", 0o700); err != nil {
		_ = claim.Release(ctx)
		return Outcome{Status: "stopped", RunID: runID, Reason: "state_root_unavailable"}, buildFailure("environment", "state_root_unavailable", 70, err)
	}

	started := c.deps.Clock.Now()
	deadline := started.Add(budgetFor(request.Project))
	ownerStarted, err := currentProcessStartedMS()
	if err != nil {
		_ = claim.Release(ctx)
		return Outcome{Status: "stopped", RunID: runID, Reason: "owner_identity_failed"}, buildFailure("environment", "owner_identity_failed", 70, err)
	}
	snapshot := journal.RunSnapshot{
		Schema: 2, RunID: runID, Slug: approved.Slug, ApprovalSHA256: approved.ApprovalSHA256,
		ApprovalCommit: string(approved.Commit), TargetBranch: approved.TargetBranch, Status: "running",
		OwnerPID: int64(os.Getpid()), OwnerStartedMS: ownerStarted, StartedMS: started.UnixMilli(),
		Recovery: []journal.RecoveryRecord{}, Fields: make(map[string]json.RawMessage),
	}
	if err := setField(&snapshot, "cache_key", cacheKey); err != nil {
		_ = claim.Release(ctx)
		return Outcome{Status: "stopped", RunID: runID, Reason: "run_journal_failed"}, buildFailure("controller", "run_journal_failed", 70, err)
	}
	if err := setField(&snapshot, "recipe", "single"); err != nil {
		_ = claim.Release(ctx)
		return Outcome{Status: "stopped", RunID: runID, Reason: "run_journal_failed"}, buildFailure("controller", "run_journal_failed", 70, err)
	}
	store, err := journal.NewRunStore(stateRoot, "runs/"+runID)
	if err == nil {
		err = store.Create(snapshot)
	}
	if err != nil {
		_ = claim.Release(ctx)
		return Outcome{Status: "stopped", RunID: runID, Reason: "run_journal_failed"}, buildFailure("controller", "run_journal_failed", 70, err)
	}
	runDir := filepath.Join(request.Project.StateRoot, "runs", runID)
	runRoot, err := safefs.OpenRoot(runDir)
	if err == nil {
		for _, dir := range []string{"logs", "tmp", "reports"} {
			if err = runRoot.MkdirAll(dir, 0o700); err != nil {
				break
			}
		}
	}
	if err != nil {
		if runRoot != nil {
			_ = runRoot.Close()
		}
		return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, nil, integrate.Base{Commit: approved.BaseCommit, Tree: approved.BaseTree}, cacheKey, runDir, "run_directory_unavailable", err)
	}
	defer runRoot.Close()

	base := integrate.Base{Commit: approved.BaseCommit, Tree: approved.BaseTree}
	if err := record(store, &snapshot, c.deps.Clock, "started", map[string]any{
		"approval_commit": approved.Commit, "base_sha": base.Commit, "recipe": "single",
		"max_rungs": 1, "land": request.Project.Land, "budget_ms": budgetFor(request.Project).Milliseconds(),
		"roles": roleFields(request.Project.Roles),
	}); err != nil {
		return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, nil, base, cacheKey, runDir, "run_journal_failed", err)
	}

	warning := ""
	if c.deps.Sandbox != nil {
		warning, err = c.deps.Sandbox.Probe(ctx, request.Project, runDir)
		if err != nil {
			return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, nil, base, cacheKey, runDir, "sandbox_probe_failed", err)
		}
		if warning != "" {
			if err := record(store, &snapshot, c.deps.Clock, "sandbox_unavailable", map[string]any{"warning": warning}); err != nil {
				return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, nil, base, cacheKey, runDir, "run_journal_failed", err)
			}
		}
	}

	currentCommit, err := gitio.NewRefPort(c.deps.Git, c.deps.OriginPolicy).ResolveCommit(ctx, request.Project.Base)
	if err != nil {
		return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, nil, base, cacheKey, runDir, "base_read_failed", err)
	}
	currentTree, err := gitio.NewRefPort(c.deps.Git, c.deps.OriginPolicy).ResolveTree(ctx, string(currentCommit))
	if err != nil {
		return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, nil, base, cacheKey, runDir, "base_read_failed", err)
	}
	if currentCommit != base.Commit {
		if c.deps.BaseRefresh == nil {
			return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, nil, base, cacheKey, runDir, "base_refresh_unavailable", errors.New("base moved and no fresh baseline port is configured"))
		}
		base, approved.Baseline, err = c.deps.BaseRefresh.Refresh(ctx, BaseRefreshRequest{
			Project: request.Project, Approval: approved, Current: integrate.Base{Commit: currentCommit, Tree: currentTree},
			RunID: runID, RunDir: runDir, Store: store, Snapshot: &snapshot,
		})
		if err != nil {
			return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, nil, base, cacheKey, runDir, "base_refresh_failed", err)
		}
		if base.Commit != currentCommit || base.Tree != currentTree || approved.Baseline == nil || approved.Baseline.Tree != string(currentTree) {
			return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, nil, base, cacheKey, runDir, "base_refresh_invalid", errors.New("base refresh did not return the observed base and its exact check baseline"))
		}
	} else if currentTree != base.Tree {
		return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, nil, base, cacheKey, runDir, "controller_base_tree_mismatch", errors.New("approval base tree does not match the resolved base commit"))
	}
	if err := setField(&snapshot, "build_base_commit", string(base.Commit)); err != nil {
		return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, nil, base, cacheKey, runDir, "run_journal_failed", err)
	}
	if err := setField(&snapshot, "build_base_tree", string(base.Tree)); err != nil {
		return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, nil, base, cacheKey, runDir, "run_journal_failed", err)
	}
	if err := store.WriteSnapshot(snapshot); err != nil {
		return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, nil, base, cacheKey, runDir, "run_journal_failed", err)
	}

	plannerSettings, ok := request.Project.Roles.Effective[contract.RoleName("planner")]
	if !ok {
		return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, nil, base, cacheKey, runDir, "role_unavailable", errors.New("effective planner role is missing"))
	}
	plannerSession, err := newConversation(runID, cacheKey, "planner", plannerSettings, "plan", "plan", RungName)
	if err != nil {
		return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, nil, base, cacheKey, runDir, "session_identity_failed", err)
	}
	plan := PlanResult{}
	planSkipped := remainingBudget(c.deps.Clock.Now(), deadline) <= 0
	maxPlanWords := planMaxWords(request.Project)
	if !planSkipped {
		plan, err = c.deps.Agent.Plan(ctx, PlanRequest{
			Project: request.Project, Approval: approved, Base: base, RunID: runID, CacheKey: cacheKey,
			RunDir: runDir, Budget: remainingBudget(c.deps.Clock.Now(), deadline), MaxWords: maxPlanWords,
			Session: plannerSession, RunRoot: runRoot,
		})
		if err != nil {
			_ = persistSession(&snapshot, "planner_session", plannerSession)
			return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, nil, base, cacheKey, runDir, providerReason(err, "plan_failed"), err)
		}
		if strings.TrimSpace(plan.Text) == "" || (plan.Difficulty != "easy" && plan.Difficulty != "hard") || len(strings.Fields(plan.Text)) > maxPlanWords {
			return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, nil, base, cacheKey, runDir, "plan_invalid", errors.New("planner response omitted a valid difficulty or plan, or exceeded the plan word limit"))
		}
	}
	if err := persistSession(&snapshot, "planner_session", plannerSession); err != nil {
		return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, nil, base, cacheKey, runDir, "run_journal_failed", err)
	}
	planEvent := "plan"
	planFields := map[string]any{"difficulty": plan.Difficulty, "words": len(strings.Fields(plan.Text))}
	if planSkipped {
		planEvent = "plan_skipped"
		planFields["reason"] = "budget"
	} else if err := runRoot.Publish("plan.md", []byte(plan.Text), 0o600, safefs.PublicationReplace); err != nil {
		return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, nil, base, cacheKey, runDir, "plan_persist_failed", err)
	}
	if err := record(store, &snapshot, c.deps.Clock, planEvent, planFields); err != nil {
		return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, nil, base, cacheKey, runDir, "run_journal_failed", err)
	}

	workspaceFactory := c.deps.Workspaces
	work, err := workspaceFactory.Create(ctx, workspace.CloneRequest{
		Source: request.Project.Origin, WorkspacesDir: filepath.Join(request.Project.StateRoot, "workspaces"),
		RunID: runID, Rung: RungName, BaseCommit: base.Commit,
	})
	if err != nil {
		return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, nil, base, cacheKey, runDir, workspaceReason(err), err)
	}
	if _, err := workspace.InstallApproved(work.Path, workspace.InstallPlan{
		Files: []workspace.ApprovedFile{
			{Path: ".kogen/intents/" + approved.Slug + "/intent.md", Bytes: approved.IntentBytes},
			{Path: approved.CandidatePath, Bytes: approved.AcceptanceBytes},
		},
		Removes: []string{approved.AcceptanceSourcePath},
	}); err != nil {
		return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, &work, base, cacheKey, runDir, "workspace_write_failed", err)
	}
	workspaceRoot, err := safefs.OpenRoot(work.Path)
	if err != nil {
		return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, &work, base, cacheKey, runDir, "workspace_unavailable", err)
	}
	defer workspaceRoot.Close()
	if _, err := approved.Protection.RestoreAfterBatch(workspaceRoot); err != nil {
		return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, &work, base, cacheKey, runDir, "protected_restore_failed", err)
	}
	builderSettings := request.Project.Roles.Effective[contract.RoleName("builder")]
	development := Development{Completion: "budget"}
	if remainingBudget(c.deps.Clock.Now(), deadline) > 0 {
		if err := record(store, &snapshot, c.deps.Clock, "rung_started", map[string]any{"rung": RungName, "model": builderSettings.Model}); err != nil {
			return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, &work, base, cacheKey, runDir, "run_journal_failed", err)
		}
		builderSession, err := newConversation(runID, cacheKey, "builder", builderSettings, "build", "builder", RungName)
		if err != nil {
			return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, &work, base, cacheKey, runDir, "session_identity_failed", err)
		}
		development, err = c.deps.Agent.Develop(ctx, DevelopRequest{
			Project: request.Project, Approval: approved, Base: base, RunID: runID, CacheKey: cacheKey,
			RunDir: runDir, Workspace: work, WorkspaceRoot: workspaceRoot, RunRoot: runRoot,
			Plan: plan, Budget: remainingBudget(c.deps.Clock.Now(), deadline), Session: builderSession,
		})
		if err != nil {
			_ = persistSession(&snapshot, "builder_session", builderSession)
			return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, &work, base, cacheKey, runDir, providerReason(err, "provider_failed"), err)
		}
		if err := persistSession(&snapshot, "builder_session", builderSession); err != nil {
			return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, &work, base, cacheKey, runDir, "run_journal_failed", err)
		}
	} else {
		if err := record(store, &snapshot, c.deps.Clock, "rung_skipped", map[string]any{"rung": RungName, "reason": "budget"}); err != nil {
			return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, &work, base, cacheKey, runDir, "run_journal_failed", err)
		}
	}
	if err := validateDevelopment(development); err != nil {
		return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, &work, base, cacheKey, runDir, "provider_protocol_invalid", err)
	}
	if err := record(store, &snapshot, c.deps.Clock, "model_stage", map[string]any{
		"stage": "build", "rung": RungName, "completion": development.Completion,
		"turns": development.Turns, "repairs": development.Repairs,
	}); err != nil {
		return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, &work, base, cacheKey, runDir, "run_journal_failed", err)
	}
	if _, err := approved.Protection.RestoreAfterBatch(workspaceRoot); err != nil {
		return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, &work, base, cacheKey, runDir, "protected_restore_failed", err)
	}
	if _, err := approved.Protection.Guard(workspaceRoot); err != nil {
		return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, &work, base, cacheKey, runDir, "protected_guard_failed", err)
	}

	report, err := c.deps.Verifier.Verify(ctx, VerifyRequest{
		Project: request.Project, Approval: approved, Base: base, Workspace: work, RunDir: runDir, Baseline: approved.Baseline,
	})
	if err != nil || report == nil {
		if err == nil {
			err = errors.New("gate returned no verification report")
		}
		return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, &work, base, cacheKey, runDir, "gate_failed", err)
	}
	verdict := string(report.Verdict())
	if receipt, ok := report.Receipt(); ok {
		if err := record(store, &snapshot, c.deps.Clock, "verification", map[string]any{
			"rung": RungName, "tree": receipt.CandidateTree, "result": verdict,
			"checks": report.Checks(), "acceptance": report.Counts(),
		}); err != nil {
			return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, &work, base, cacheKey, runDir, "run_journal_failed", err)
		}
	} else if err := record(store, &snapshot, c.deps.Clock, "verification", map[string]any{"rung": RungName, "result": verdict}); err != nil {
		return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, &work, base, cacheKey, runDir, "run_journal_failed", err)
	}
	if err := setField(&snapshot, "verdict", verdict); err != nil {
		return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, &work, base, cacheKey, runDir, "run_journal_failed", err)
	}

	if report.IsLandable() {
		candidate, createErr := commit.Create(ctx, commit.Request{
			Processes: c.deps.Processes, Environment: c.deps.Environment, Workspace: work.Path,
			BaseCommit: base.Commit, Slug: approved.Slug, IntentBytes: approved.IntentBytes,
			AcceptanceSourcePath: approved.AcceptanceSourcePath, CandidatePath: approved.CandidatePath,
			CandidateBytes: approved.AcceptanceBytes, Protection: approved.Protection, Gate: report,
		})
		if createErr != nil {
			return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, &work, base, cacheKey, runDir, "commit_failed", createErr)
		}
		landed, landErr := c.deps.Lander.Land(ctx, LandingRequest{
			Project: request.Project, Approval: approved, Base: base, Workspace: work, RunID: runID,
			Store: store, Snapshot: &snapshot, Candidate: candidate, Report: report,
		})
		if landErr != nil {
			return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, &work, base, cacheKey, runDir, "landing_failed", landErr)
		}
		status := string(landed.Kind)
		if landed.Kind != integrate.OutcomeLanded && landed.Kind != integrate.OutcomeParked {
			return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, &work, base, cacheKey, runDir, "landing_result_invalid", errors.New("landing returned an unknown outcome"))
		}
		if landed.Commit != candidate.Commit || landed.Tree != candidate.Tree || len(landed.CandidateRefs) == 0 {
			return c.stop(ctx, request.Project, approved, runID, store, &snapshot, claim, &work, base, cacheKey, runDir, "landing_result_invalid", errors.New("landing result omitted or changed the preserved candidate identity"))
		}
		var landingRecordErr error
		if len(landed.CleanupErrors) != 0 {
			detail := []byte(strings.Join(landed.CleanupErrors, "\n"))
			landingRecordErr = store.RecordCleanupFailure(snapshot, c.deps.Clock.Now().UnixMilli(), "landing", detail)
			snapshot.CleanupPending = true
		}
		if err := c.terminal(ctx, request.Project, approved, runID, store, &snapshot, claim, &work, base, cacheKey, runDir, status, string(landed.Kind), candidate.Commit, verdict, true); err != nil {
			var failure *contract.Failure
			_ = errors.As(err, &failure)
			return Outcome{Status: status, RunID: runID, Reason: string(landed.Kind), Commit: candidate.Commit, Verdict: verdict, Failure: failure}, err
		}
		if landingRecordErr != nil {
			failure := buildFailure("controller", "landing_cleanup_record_failed", 70, errors.Join(errors.New(strings.Join(landed.CleanupErrors, "; ")), landingRecordErr))
			return Outcome{Status: status, RunID: runID, Reason: string(landed.Kind), Commit: candidate.Commit, Verdict: verdict, Failure: failure}, failure
		}
		return Outcome{Status: status, RunID: runID, Reason: string(landed.Kind), Commit: candidate.Commit, Verdict: verdict}, nil
	}

	if err := c.terminal(ctx, request.Project, approved, runID, store, &snapshot, claim, &work, base, cacheKey, runDir, "failed", development.Completion, "", verdict, false); err != nil {
		var failure *contract.Failure
		_ = errors.As(err, &failure)
		return Outcome{Status: "failed", RunID: runID, Reason: development.Completion, Verdict: verdict, Failure: failure}, err
	}
	return Outcome{Status: "failed", RunID: runID, Reason: development.Completion, Verdict: verdict}, nil
}

func (c *Controller) stop(ctx context.Context, project *project.Resolution, approved *Approval, runID string, store *journal.RunStore, snapshot *journal.RunSnapshot, claim Claim, work *workspace.Workspace, base integrate.Base, cacheKey, runDir, reason string, cause error) (Outcome, error) {
	failure := buildFailure(errorClass(cause, "environment"), reason, 70, cause)
	if store == nil || snapshot == nil {
		if claim != nil {
			if releaseErr := claim.Release(ctx); releaseErr != nil {
				failure.Cause = errors.Join(failure.Cause, releaseErr)
			}
		}
		return Outcome{Status: "stopped", RunID: runID, Reason: reason, Failure: failure}, failure
	}
	preserved := work == nil
	if work != nil {
		if err := c.preserveWorkspace(ctx, project, approved, runID, store, snapshot, *work, base); err != nil {
			cleanupErr := store.RecordCleanupFailure(*snapshot, c.deps.Clock.Now().UnixMilli(), filepath.Base(work.Path), []byte(err.Error()))
			snapshot.CleanupPending = true
			failure.Cause = errors.Join(failure.Cause, err, cleanupErr)
		} else {
			preserved = true
		}
	}
	snapshot.Status = "stopped"
	if err := record(store, snapshot, c.deps.Clock, "finished", map[string]any{"status": "stopped", "reason": reason}); err != nil {
		failure.Cause = errors.Join(failure.Cause, err)
		return Outcome{Status: "stopped", RunID: runID, Reason: reason, Failure: failure}, failure
	}
	if work != nil && preserved {
		if err := removeWorkspace(project.StateRoot, stateRelative(project.StateRoot, work.Path)); err != nil {
			_ = store.RecordCleanupFailure(*snapshot, c.deps.Clock.Now().UnixMilli(), filepath.Base(work.Path), []byte(err.Error()))
			snapshot.CleanupPending = true
			failure.Cause = errors.Join(failure.Cause, err)
		}
	}
	if claim != nil {
		if err := claim.Release(ctx); err != nil {
			_ = store.RecordCleanupFailure(*snapshot, c.deps.Clock.Now().UnixMilli(), "claim", []byte(err.Error()))
			failure.Cause = errors.Join(failure.Cause, err)
		}
	}
	return Outcome{Status: "stopped", RunID: runID, Reason: reason, Failure: failure}, failure
}

func (c *Controller) terminal(ctx context.Context, project *project.Resolution, approved *Approval, runID string, store *journal.RunStore, snapshot *journal.RunSnapshot, claim Claim, work *workspace.Workspace, base integrate.Base, cacheKey, runDir, status, reason string, commitID contract.ObjectID, verdict string, candidateAlreadyPreserved bool) error {
	preserved := work == nil || candidateAlreadyPreserved
	var terminalErr error
	if work != nil && !candidateAlreadyPreserved {
		if err := c.preserveWorkspace(ctx, project, approved, runID, store, snapshot, *work, base); err != nil {
			recordErr := store.RecordCleanupFailure(*snapshot, c.deps.Clock.Now().UnixMilli(), filepath.Base(work.Path), []byte(err.Error()))
			terminalErr = errors.Join(err, recordErr)
			snapshot.CleanupPending = true
		} else {
			preserved = true
		}
	}
	snapshot.Status = status
	fields := map[string]any{"status": status, "reason": reason, "rung": RungName, "verdict": verdict}
	if commitID != "" {
		fields["commit"] = commitID
	}
	if !finishedStatusPersisted(store, status) {
		if err := record(store, snapshot, c.deps.Clock, "finished", fields); err != nil {
			return buildFailure("controller", "run_journal_failed", 70, errors.Join(terminalErr, err))
		}
	}
	if work != nil && preserved {
		if err := removeWorkspace(project.StateRoot, stateRelative(project.StateRoot, work.Path)); err != nil {
			if recordErr := store.RecordCleanupFailure(*snapshot, c.deps.Clock.Now().UnixMilli(), filepath.Base(work.Path), []byte(err.Error())); recordErr != nil {
				terminalErr = errors.Join(terminalErr, err, recordErr)
			} else {
				terminalErr = errors.Join(terminalErr, err)
			}
			snapshot.CleanupPending = true
		}
	}
	if claim != nil {
		if err := claim.Release(ctx); err != nil {
			if recordErr := store.RecordCleanupFailure(*snapshot, c.deps.Clock.Now().UnixMilli(), "claim", []byte(err.Error())); recordErr != nil {
				terminalErr = errors.Join(terminalErr, err, recordErr)
			} else {
				terminalErr = errors.Join(terminalErr, err)
			}
		}
	}
	if terminalErr != nil {
		return buildFailure("controller", "terminal_cleanup_failed", 70, terminalErr)
	}
	return nil
}

func (c *Controller) preserveWorkspace(ctx context.Context, project *project.Resolution, approved *Approval, runID string, store *journal.RunStore, snapshot *journal.RunSnapshot, work workspace.Workspace, base integrate.Base) error {
	recovery, err := c.deps.Candidates.Preserve(ctx, CandidateRequest{Project: project, Workspace: work, Base: base, RunID: runID, Store: store, Snapshot: snapshot})
	if err != nil {
		return err
	}
	if recovery.Workspace == "" || recovery.Base != string(base.Commit) || recovery.Verification != "unverified" {
		return errors.New("candidate preserver returned an invalid unverified record")
	}
	return store.RecordRecoveryPreserved(*snapshot, c.deps.Clock.Now().UnixMilli(), recovery)
}

func validateApproval(project *project.Resolution, slug string, approval *Approval) error {
	if approval == nil || approval.Slug != slug || approval.TargetBranch == "" || approval.TargetBranch != project.Base {
		return errors.New("approval identity or target branch does not match the project")
	}
	if err := gitio.ValidateObjectID(approval.Commit); err != nil {
		return fmt.Errorf("approval commit: %w", err)
	}
	if err := gitio.ValidateObjectID(approval.BaseCommit); err != nil {
		return fmt.Errorf("approval base: %w", err)
	}
	if err := gitio.ValidateObjectID(approval.BaseTree); err != nil {
		return fmt.Errorf("approval base tree: %w", err)
	}
	if len(approval.ApprovalSHA256) != 64 || intent.ApprovalSHA256(approval.IntentBytes, approval.AcceptanceBytes) != approval.ApprovalSHA256 {
		return errors.New("approval digest does not match its exact Intent and acceptance bytes")
	}
	if len(approval.IntentSHA256) != 64 || intent.IntentSHA256(approval.IntentBytes) != approval.IntentSHA256 {
		return errors.New("Intent digest does not match its exact bytes")
	}
	parsed, err := intent.Parse(slug, approval.IntentBytes)
	if err != nil {
		return fmt.Errorf("approved Intent is invalid: %w", err)
	}
	if approval.Intent != nil && !bytes.Equal(approval.Intent.RawBytes(), parsed.RawBytes()) {
		return errors.New("parsed Intent does not match the approved Intent bytes")
	}
	if len(approval.AcceptanceBytes) == 0 || !safeApprovalPath(approval.AcceptanceSourcePath) || !safeApprovalPath(approval.CandidatePath) || approval.AcceptanceSourcePath == approval.CandidatePath {
		return errors.New("approval acceptance bytes or paths are invalid")
	}
	if approval.Protection == nil {
		return errors.New("approval is missing its protected manifest")
	}
	expectedSource, expectedCandidate, err := acceptancePaths(project, slug)
	if err != nil {
		return fmt.Errorf("resolve approved acceptance paths: %w", err)
	}
	if approval.AcceptanceSourcePath != expectedSource || approval.CandidatePath != expectedCandidate {
		return errors.New("approval acceptance paths do not match the resolved project")
	}
	for _, item := range parsed.Verify {
		if len(item.Words) == 0 {
			return errors.New("approved Intent contains an empty Verify row")
		}
	}
	return nil
}

func (c *Controller) validateReady(resolved *project.Resolution, approved *Approval) error {
	if c.deps.OriginPolicy.WorkingDirectory == "" || c.deps.OriginPolicy.WorkingDirectory != resolved.Origin {
		return errors.New("origin Git policy must name the resolved repository")
	}
	if c.deps.Environment == nil || c.deps.Environment["PATH"] == "" {
		return errors.New("controller environment with PATH is required")
	}
	if approved.Baseline == nil || approved.Baseline.Tree != string(approved.BaseTree) {
		return errors.New("approval is missing its base check baseline")
	}
	if _, ok := resolved.Roles.Effective[contract.RoleName("planner")]; !ok {
		return errors.New("effective planner role is missing")
	}
	if _, ok := resolved.Roles.Effective[contract.RoleName("builder")]; !ok {
		return errors.New("effective builder role is missing")
	}
	return nil
}

func newConversation(runID, cacheKey, role string, settings contract.RoleSettings, stage, attempt, rung string) (*session.Conversation, error) {
	identity, err := session.Bind(session.Binding{
		RunID: runID, CacheKey: cacheKey, Role: contract.RoleName(role), Provider: settings.Provider,
		Model: settings.Model, Effort: settings.Effort, Stage: stage, Attempt: attempt, Rung: rung, Epoch: "initial",
	})
	if err != nil {
		return nil, err
	}
	return session.New(identity)
}

func validateDevelopment(value Development) error {
	if value.Turns < 0 || value.Repairs < 0 {
		return errors.New("provider returned negative Build accounting")
	}
	switch value.Completion {
	case "finish", "turn_cap", "wall_cap", "budget":
		return nil
	default:
		return fmt.Errorf("provider returned unknown Build completion %q", value.Completion)
	}
}

func record(store *journal.RunStore, snapshot *journal.RunSnapshot, clock contract.Clock, name string, fields map[string]any) error {
	event := journal.NewRunEvent(name, clock.Now().UnixMilli())
	for key, value := range fields {
		if err := event.Set(key, value); err != nil {
			return err
		}
	}
	return store.Record(event, *snapshot)
}

func setField(snapshot *journal.RunSnapshot, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if snapshot.Fields == nil {
		snapshot.Fields = make(map[string]json.RawMessage)
	}
	snapshot.Fields[key] = encoded
	return nil
}

func persistSession(snapshot *journal.RunSnapshot, key string, conversation *session.Conversation) error {
	if conversation == nil {
		return errors.New("cannot persist a nil provider conversation")
	}
	return setField(snapshot, key, conversation.Snapshot())
}

func buildFailure(class, reason string, exit int, cause error) *contract.Failure {
	return &contract.Failure{Class: contract.ErrorClass(class), Reason: contract.ErrorReason(reason), Exit: contract.ExitCode(exit), Message: fmt.Sprintf("%s/%s", class, reason), Cause: cause}
}

func providerReason(err error, fallback string) string {
	var failure *contract.Failure
	if errors.As(err, &failure) && failure.Reason != "" {
		return string(failure.Reason)
	}
	return fallback
}

func errorClass(err error, fallback string) string {
	var failure *contract.Failure
	if errors.As(err, &failure) && failure.Class != "" {
		return string(failure.Class)
	}
	return fallback
}

func workspaceReason(err error) string {
	if errors.Is(err, workspace.ErrSetupFailed) {
		return "setup_failed"
	}
	if errors.Is(err, workspace.ErrToolMissing) {
		return "tool_missing"
	}
	return "workspace_create_failed"
}

func safeApprovalPath(value string) bool {
	if value == "" || !fs.ValidPath(value) || filepath.IsAbs(value) || filepath.ToSlash(filepath.Clean(value)) != value || strings.ContainsAny(value, "\\\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".git" || part == ".." {
			return false
		}
	}
	return true
}

func stateRelative(stateRoot, workspacePath string) string {
	relative, err := filepath.Rel(stateRoot, workspacePath)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
		return ""
	}
	return filepath.ToSlash(relative)
}

func removeWorkspace(stateRoot, relative string) error {
	if relative == "" || !fs.ValidPath(relative) {
		return errors.New("workspace cleanup path is outside the state root")
	}
	root, err := safefs.OpenRoot(stateRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	return removeEntry(root, relative)
}

func removeEntry(root *safefs.Root, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() && info.Mode()&fs.ModeSymlink == 0 {
		entries, err := root.ReadDir(name)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := removeEntry(root, name+"/"+entry.Name()); err != nil {
				return err
			}
		}
	}
	return root.Remove(name)
}

func budgetFor(resolved *project.Resolution) time.Duration {
	if resolved == nil {
		return defaultBudget
	}
	if milliseconds, ok := resolvedBuildInt(resolved, "budget_ms"); ok && milliseconds > 0 && time.Duration(milliseconds) <= time.Duration(1<<63-1)/time.Millisecond {
		return time.Duration(milliseconds) * time.Millisecond
	}
	if minutes, ok := resolvedBuildInt(resolved, "wall_minutes"); ok && minutes > 0 && time.Duration(minutes) <= time.Duration(1<<63-1)/time.Minute {
		return time.Duration(minutes) * time.Minute
	}
	return defaultBudget
}

func planMaxWords(resolved *project.Resolution) int {
	if value, ok := resolvedBuildInt(resolved, "plan_max_words"); ok && value >= 300 && value <= 2000 {
		return value
	}
	return defaultPlanMax
}

func resolvedBuildInt(resolved *project.Resolution, key string) (int, bool) {
	if resolved == nil {
		return 0, false
	}
	if resolved.Config != nil {
		if build, ok := resolved.Config.Raw["build"].(yamlmini.Mapping); ok {
			if value, ok := build[key].(int); ok {
				return value, true
			}
		}
	}
	if resolved.Machine != nil {
		if value, ok := resolved.Machine.Build[key].(int); ok {
			return value, true
		}
	}
	return 0, false
}

func remainingBudget(now, deadline time.Time) time.Duration {
	if !now.Before(deadline) {
		return 0
	}
	return deadline.Sub(now)
}

func finishedStatusPersisted(store *journal.RunStore, status string) bool {
	if store == nil {
		return false
	}
	snapshot, err := store.ReadSnapshot()
	if err != nil || snapshot.Status != status {
		return false
	}
	events, err := store.ReadEvents()
	if err != nil {
		return false
	}
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Event != "finished" {
			continue
		}
		var recorded string
		if json.Unmarshal(events[index].Fields["status"], &recorded) != nil {
			return false
		}
		return recorded == status
	}
	return false
}

func roleFields(roles contract.RoleManifest) map[string]any {
	result := make(map[string]any)
	for name, settings := range roles.Effective {
		result[string(name)] = map[string]string{"provider": settings.Provider, "model": settings.Model, "effort": settings.Effort}
	}
	return result
}

func newRunID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(random[:]), nil
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }
func (wallClock) Sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
