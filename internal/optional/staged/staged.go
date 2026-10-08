// Package staged composes the optional staged recipe around the shared Build
// controller. It owns stage ordering and role pins; provider retry, tools,
// verification, repair, candidate preservation, and landing remain with their
// existing controllers.
package staged

import (
	"context"
	"errors"
	"fmt"

	"kogen-go/internal/build/ladder"
	"kogen-go/internal/build/recipe"
	selection "kogen-go/internal/build/select"
	"kogen-go/internal/contract"
	"kogen-go/internal/gate"
	"kogen-go/internal/project"
	"kogen-go/internal/provider/retry"
	"kogen-go/internal/provider/session"
	"kogen-go/internal/provider/tools"
)

type StageKind string

const (
	StageContext StageKind = "context"
	StagePlan    StageKind = "plan"
	StageBuilder StageKind = "builder"
	StageReview  StageKind = "review"
)

var (
	ErrInvalidRequest = errors.New("staged Build: invalid request")
	ErrInvalidProgram = errors.New("staged Build: invalid recipe or role manifest")
)

// ModelPin is the role-resolved model used by one stage. Fallback is the
// shared provider retry controller's overload target, when that role can
// switch; this package never retries a request itself.
type ModelPin struct {
	AssignedRole contract.RoleName
	Current      contract.RoleSettings
	Fallback     *contract.RoleSettings
}

// StageSpec is the immutable execution description for one recipe stage.
// Auxiliary stages have no callable tools. Builder tool names come directly
// from the resolved recipe's ToolSet, which is also consumed by the provider
// tool controller.
type StageSpec struct {
	Kind         StageKind
	Role         ModelPin
	Rung         *recipe.Rung
	Input        recipe.InputKind
	ToolSet      recipe.ToolSet
	AllowedTools []string
	ToolRole     tools.ToolRole
}

// Program is a role-resolved view of a recipe. Its Build is passed to the
// shared Build controller; the stage pointers describe optional effects that
// run before planning and before a landable candidate is committed.
type Program struct {
	Build    recipe.Build
	Context  *StageSpec
	Plan     *StageSpec
	Builders []StageSpec
	Review   *StageSpec
}

// Resolve composes effective role pins and stage effects for a loaded recipe.
// modelFallback follows the project setting and defaults to true when nil.
// Role settings must already have been merged by project.ResolveRoles.
func Resolve(build recipe.Build, roles contract.RoleManifest, modelFallback *bool) (Program, error) {
	if build.Recipe.Name == "" {
		return Program{}, fmt.Errorf("%w: recipe name is required", ErrInvalidProgram)
	}
	resolvedRecipe, err := recipe.Resolve(build.Recipe.Name, roles)
	if err != nil {
		return Program{}, fmt.Errorf("%w: %v", ErrInvalidProgram, err)
	}
	if len(resolvedRecipe.Rungs) == 0 {
		return Program{}, fmt.Errorf("%w: recipe has no builder stages", ErrInvalidProgram)
	}
	builder, err := resolveRole(roles, contract.RoleName("builder"))
	if err != nil {
		return Program{}, fmt.Errorf("%w: %v", ErrInvalidProgram, err)
	}

	fallbackOn := true
	if modelFallback != nil {
		fallbackOn = *modelFallback
	}
	program := Program{Build: cloneBuild(build)}
	program.Build.Recipe = resolvedRecipe

	if resolvedRecipe.Kind == recipe.Staged {
		contextPin, err := resolvePin(roles, contract.RoleName("context"), fallbackOn)
		if err != nil {
			return Program{}, fmt.Errorf("%w: %v", ErrInvalidProgram, err)
		}
		reviewerPin, err := resolvePin(roles, contract.RoleName("reviewer"), fallbackOn)
		if err != nil {
			return Program{}, fmt.Errorf("%w: %v", ErrInvalidProgram, err)
		}
		if contextPin.Current.Provider != builder.Provider || reviewerPin.Current.Provider != builder.Provider {
			return Program{}, fmt.Errorf("%w: context and reviewer roles must use the effective builder provider", ErrInvalidProgram)
		}
		program.Context = &StageSpec{Kind: StageContext, Role: contextPin, ToolSet: recipe.NoTools}
		program.Review = &StageSpec{Kind: StageReview, Role: reviewerPin, ToolSet: recipe.NoTools}
	}

	if hasPlanInput(resolvedRecipe) {
		planner, err := resolveRole(roles, contract.RoleName("planner"))
		if err != nil {
			return Program{}, fmt.Errorf("%w: %v", ErrInvalidProgram, err)
		}
		if planner.Provider != builder.Provider {
			return Program{}, fmt.Errorf("%w: planner role must use the effective builder provider", ErrInvalidProgram)
		}
		program.Build.Settings.Planner = planner
		program.Plan = &StageSpec{
			Kind:    StagePlan,
			Role:    ModelPin{AssignedRole: contract.RoleName("planner"), Current: planner},
			ToolSet: recipe.NoTools,
		}
	}

	program.Builders = make([]StageSpec, 0, len(resolvedRecipe.Rungs))
	for index := range resolvedRecipe.Rungs {
		rung := cloneRung(resolvedRecipe.Rungs[index])
		if builder.Provider == "grok" {
			// A Grok Build keeps all of its builder stages on the effective Grok
			// role. The recipe's ChatGPT escalation defaults cannot cross the
			// selected provider boundary.
			rung.Model = builder
		}
		builderPin, err := resolveSpecificPin(contract.RoleName("builder"), rung.Model, fallbackOn)
		if err != nil {
			return Program{}, fmt.Errorf("%w: %v", ErrInvalidProgram, err)
		}
		toolRole, allowedTools, err := toolPolicy(rung.Tools)
		if err != nil {
			return Program{}, fmt.Errorf("%w: %v", ErrInvalidProgram, err)
		}
		program.Builders = append(program.Builders, StageSpec{
			Kind: StageBuilder, Role: builderPin, Rung: &rung, Input: rung.Input,
			ToolSet: rung.Tools, AllowedTools: allowedTools, ToolRole: toolRole,
		})
		program.Build.Recipe.Rungs[index] = rung
	}
	return cloneProgram(program), nil
}

// ToolPolicy resolves a recipe ToolSet to the existing provider/tools role and
// callable names. The returned slice is detached from the recipe's schema.
func ToolPolicy(toolSet recipe.ToolSet) (tools.ToolRole, []string, error) {
	return toolPolicy(toolSet)
}

func toolPolicy(toolSet recipe.ToolSet) (tools.ToolRole, []string, error) {
	var role tools.ToolRole
	switch toolSet {
	case recipe.ShellTools:
		role = tools.RoleBuilderShell
	case recipe.DirectTools:
		role = tools.RoleBuilderDirect
	default:
		return "", nil, fmt.Errorf("unsupported builder tool set %q", toolSet)
	}
	names := toolSet.Names()
	if len(names) == 0 {
		return "", nil, fmt.Errorf("builder tool set %q is empty", toolSet)
	}
	return role, append([]string(nil), names...), nil
}

func resolvePin(roles contract.RoleManifest, name contract.RoleName, fallbackOn bool) (ModelPin, error) {
	current, err := resolveRole(roles, name)
	if err != nil {
		return ModelPin{}, err
	}
	return resolveSpecificPin(name, current, fallbackOn)
}

func resolveRole(roles contract.RoleManifest, name contract.RoleName) (contract.RoleSettings, error) {
	settings, ok := roles.Effective[name]
	if !ok || settings.Provider == "" || settings.Model == "" || settings.Effort == "" {
		return contract.RoleSettings{}, fmt.Errorf("effective %s role is unavailable", name)
	}
	if settings.Provider != "chatgpt" && settings.Provider != "grok" {
		return contract.RoleSettings{}, fmt.Errorf("effective %s role has unsupported provider %q", name, settings.Provider)
	}
	return settings, nil
}

func resolveSpecificPin(name contract.RoleName, current contract.RoleSettings, fallbackOn bool) (ModelPin, error) {
	if current.Provider == "" || current.Model == "" || current.Effort == "" {
		return ModelPin{}, fmt.Errorf("effective %s role is incomplete", name)
	}
	if current.Provider != "chatgpt" && current.Provider != "grok" {
		return ModelPin{}, fmt.Errorf("effective %s role has unsupported provider %q", name, current.Provider)
	}
	pin := ModelPin{AssignedRole: name, Current: current}
	fallback, switches, err := retry.ResolveOverloadFallback(name, current, nil, fallbackOn)
	if err != nil {
		return ModelPin{}, err
	}
	if switches {
		pin.Fallback = &fallback
	}
	return pin, nil
}

func hasPlanInput(recipeValue recipe.Recipe) bool {
	for _, rung := range recipeValue.Rungs {
		if rung.Input == recipe.PlanInput {
			return true
		}
	}
	return false
}

// TaskInput carries only the approved task bytes needed by the optional model
// stages. It is never used in protocol identities or routine diagnostics.
type TaskInput struct {
	IntentBytes     []byte
	RequestBytes    []byte
	AcceptanceBytes []byte
}

// CandidateView is the read-only review projection. It carries the exact
// captured diff and gate summary without exposing the mutable GateReport to a
// reviewer adapter.
type CandidateView struct {
	Rung         string
	Rank         int
	AttemptOrder int
	Ref          string
	Diff         []byte
	Verdict      gate.Verdict
	Counts       gate.AcceptanceCounts
}

// StageCall is supplied to the stage adapter. Session is one persistent
// conversation object for this role/stage; the adapter must keep it through
// retries and append-only history changes.
type StageCall struct {
	Kind         StageKind
	Role         ModelPin
	ToolSet      recipe.ToolSet
	AllowedTools []string
	Session      *session.Conversation
	Project      *project.Resolution
	RunID        string
	CacheKey     string
	RecipeName   string
	Task         TaskInput
	Context      string
	Plan         *recipe.Plan
	Candidate    *CandidateView
}

type ReviewResult struct {
	Text string
}

// StageRunner performs the role-bound context, planning, and review model
// calls using the shared provider session/retry/tool controllers. This package
// supplies the resolved pins and stable conversation object, not a transport
// or retry implementation.
type StageRunner interface {
	Context(context.Context, StageCall) (string, error)
	Plan(context.Context, StageCall) (string, error)
	Review(context.Context, StageCall) (ReviewResult, error)
}

type BuildRequest struct {
	Program Program
	Ladder  ladder.Request
}

// BuildController is the existing recipe-aware Build controller boundary. Its
// implementation must route recipe rungs through the shared sequential or
// parallel controllers; it must not introduce an optional-path gate or retry
// layer.
type BuildController interface {
	Run(context.Context, BuildRequest) (ladder.Outcome, error)
}

type Dependencies struct {
	Stages StageRunner
	Build  BuildController
}

type Controller struct{ deps Dependencies }

func NewController(deps Dependencies) (*Controller, error) {
	if deps.Build == nil {
		return nil, errors.New("staged Build controller is required")
	}
	return &Controller{deps: deps}, nil
}

type Request struct {
	Ladder        ladder.Request
	Project       *project.Resolution
	CacheKey      string
	Task          TaskInput
	PlanWrapper   string
	ModelFallback *bool
}

type Outcome struct {
	Program Program
	Context string
	Plan    *recipe.Plan
	Build   ladder.Outcome
	Review  *ReviewResult
}

// Run executes recipe side stages in order around the existing Build
// controller. A successful staged review completes before this method returns
// a landable candidate to the caller, so the caller cannot commit it first.
// Review output is advisory: it never changes the gate report, candidate
// ranking, or landing eligibility.
func (c *Controller) Run(ctx context.Context, request Request) (Outcome, error) {
	if c == nil || c.deps.Build == nil || ctx == nil {
		return Outcome{}, fmt.Errorf("%w: controller and context are required", ErrInvalidRequest)
	}
	if request.Ladder.RunID == "" || request.Ladder.Build.Recipe.Name == "" || request.Project == nil {
		return Outcome{}, fmt.Errorf("%w: run id, resolved project, and recipe are required", ErrInvalidRequest)
	}
	program, err := Resolve(request.Ladder.Build, request.Project.Roles, request.ModelFallback)
	if err != nil {
		return Outcome{}, err
	}
	if (program.Context != nil || program.Plan != nil || program.Review != nil) && c.deps.Stages == nil {
		return Outcome{}, fmt.Errorf("%w: recipe %q requires model stage execution", ErrInvalidRequest, program.Build.Recipe.Name)
	}
	if _, err := session.Bind(session.Binding{
		RunID: request.Ladder.RunID, CacheKey: request.CacheKey,
		Role: "builder", Provider: program.Build.Recipe.Rungs[0].Model.Provider,
		Model: program.Build.Recipe.Rungs[0].Model.Model, Effort: program.Build.Recipe.Rungs[0].Model.Effort,
		Stage: "build", Attempt: "builder", Rung: "build", Epoch: "initial",
	}); err != nil {
		return Outcome{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}

	outcome := Outcome{Program: cloneProgram(program)}
	task := cloneTask(request.Task)
	if program.Context != nil {
		call, err := newStageCall(request, task, program, *program.Context, "context", "build", nil)
		if err != nil {
			return outcome, err
		}
		outcome.Context, err = c.deps.Stages.Context(ctx, call)
		if err != nil {
			return outcome, err
		}
	}
	if program.Plan != nil {
		call, err := newStageCall(request, task, program, *program.Plan, "plan", "build", nil)
		if err != nil {
			return outcome, err
		}
		call.Context = outcome.Context
		text, err := c.deps.Stages.Plan(ctx, call)
		if err != nil {
			return outcome, err
		}
		wordLimit := program.Build.Settings.PlanMaxWords
		if wordLimit == 0 {
			wordLimit = recipe.DefaultPlanMaxWords
		}
		plan, err := recipe.ParsePlan(text, request.PlanWrapper, wordLimit)
		if err != nil {
			return outcome, fmt.Errorf("staged Build planner response is invalid: %w", err)
		}
		outcome.Plan = &plan
	}

	buildRequest := cloneLadderRequest(request.Ladder)
	buildRequest.Build = cloneBuild(program.Build)
	buildRequest.Plan = clonePlan(outcome.Plan)
	outcome.Build, err = c.deps.Build.Run(ctx, BuildRequest{
		Program: cloneProgram(program), Ladder: buildRequest,
	})
	if err != nil {
		return outcome, err
	}
	if program.Review == nil || outcome.Build.Status != ladder.StatusReady {
		return outcome, nil
	}
	if outcome.Build.Candidate == nil {
		return outcome, fmt.Errorf("%w: ready Build outcome has no candidate", ErrInvalidProgram)
	}
	call, err := newStageCall(request, task, program, *program.Review, "review", outcome.Build.Candidate.Rung, outcome.Build.Candidate)
	if err != nil {
		return outcome, err
	}
	call.Context = outcome.Context
	call.Plan = clonePlan(outcome.Plan)
	review, err := c.deps.Stages.Review(ctx, call)
	if err != nil {
		return outcome, err
	}
	outcome.Review = &review
	return outcome, nil
}

func newStageCall(request Request, task TaskInput, program Program, spec StageSpec, attempt, rung string, candidate *selection.Candidate) (StageCall, error) {
	identity, err := session.Bind(session.Binding{
		RunID: request.Ladder.RunID, CacheKey: request.CacheKey,
		Role: spec.Role.AssignedRole, Provider: spec.Role.Current.Provider,
		Model: spec.Role.Current.Model, Effort: spec.Role.Current.Effort,
		Stage: string(spec.Kind), Attempt: attempt, Rung: rung, Epoch: "initial",
	})
	if err != nil {
		return StageCall{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	conversation, err := session.New(identity)
	if err != nil {
		return StageCall{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	return StageCall{
		Kind: spec.Kind, Role: clonePin(spec.Role), ToolSet: spec.ToolSet,
		AllowedTools: append([]string(nil), spec.AllowedTools...), Session: conversation,
		Project: request.Project, RunID: request.Ladder.RunID, CacheKey: request.CacheKey,
		RecipeName: program.Build.Recipe.Name, Task: cloneTask(task),
		Candidate: candidateView(candidate),
	}, nil
}

func cloneBuild(build recipe.Build) recipe.Build {
	rungs := build.Recipe.Rungs
	build.Recipe.Rungs = make([]recipe.Rung, len(rungs))
	for index, rung := range rungs {
		build.Recipe.Rungs[index] = cloneRung(rung)
	}
	if build.Settings.ExperimentalR4 != nil {
		value := *build.Settings.ExperimentalR4
		build.Settings.ExperimentalR4 = &value
	}
	if build.Settings.RepeatFrom != nil {
		value := *build.Settings.RepeatFrom
		build.Settings.RepeatFrom = &value
	}
	return build
}

func cloneProgram(program Program) Program {
	program.Build = cloneBuild(program.Build)
	program.Context = cloneStage(program.Context)
	program.Plan = cloneStage(program.Plan)
	program.Review = cloneStage(program.Review)
	builders := program.Builders
	program.Builders = make([]StageSpec, len(builders))
	for index, stage := range builders {
		program.Builders[index] = *cloneStage(&stage)
	}
	return program
}

func cloneStage(stage *StageSpec) *StageSpec {
	if stage == nil {
		return nil
	}
	copy := *stage
	copy.Role = clonePin(stage.Role)
	copy.AllowedTools = append([]string(nil), stage.AllowedTools...)
	if stage.Rung != nil {
		rung := cloneRung(*stage.Rung)
		copy.Rung = &rung
	}
	return &copy
}

func clonePin(pin ModelPin) ModelPin {
	if pin.Fallback != nil {
		fallback := *pin.Fallback
		pin.Fallback = &fallback
	}
	return pin
}

func cloneRung(rung recipe.Rung) recipe.Rung { return rung }

func cloneTask(task TaskInput) TaskInput {
	return TaskInput{
		IntentBytes:     append([]byte(nil), task.IntentBytes...),
		RequestBytes:    append([]byte(nil), task.RequestBytes...),
		AcceptanceBytes: append([]byte(nil), task.AcceptanceBytes...),
	}
}

func clonePlan(plan *recipe.Plan) *recipe.Plan {
	if plan == nil {
		return nil
	}
	copy := *plan
	return &copy
}

func candidateView(candidate *selection.Candidate) *CandidateView {
	if candidate == nil {
		return nil
	}
	view := &CandidateView{
		Rung: candidate.Rung, Rank: candidate.Rank,
		AttemptOrder: candidate.AttemptOrder, Ref: candidate.Ref,
		Diff: append([]byte(nil), candidate.Diff...),
	}
	if candidate.Gate != nil {
		view.Verdict = candidate.Gate.Verdict()
		view.Counts = candidate.Gate.Counts()
	}
	return view
}

func cloneLadderRequest(request ladder.Request) ladder.Request {
	request.Build = cloneBuild(request.Build)
	request.Plan = clonePlan(request.Plan)
	return request
}
