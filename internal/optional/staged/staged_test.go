package staged

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"kogen-go/internal/build/ladder"
	"kogen-go/internal/build/recipe"
	selection "kogen-go/internal/build/select"
	"kogen-go/internal/contract"
	"kogen-go/internal/project"
)

const (
	testRunID   = "0123456789abcdef0123456789abcdef"
	testCacheID = "cache_test_opaque_key"
)

func TestComposeEveryRecipeUsesRecipeBuilderToolAllowlist(t *testing.T) {
	roles := testRoles("chatgpt")
	names := []string{
		"ladder", "ladder-diverse", "ladder-luna", "ladder-sol-low",
		"ladder-sol-medium", "ladder-sol-high", "plan-shell", "staged",
		"direct", "direct-escalate", "direct-shell", "escalate-shell", "ladder+edge",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			parsed, err := recipe.Parse(name)
			if err != nil {
				t.Fatal(err)
			}
			program, err := Resolve(recipe.Build{Recipe: parsed, Settings: recipe.Settings{PlanMaxWords: 500}}, roles, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(program.Builders) != len(parsed.Rungs) {
				t.Fatalf("builder stages = %d, recipe rungs = %d", len(program.Builders), len(parsed.Rungs))
			}
			for index, stage := range program.Builders {
				wantRole, wantTools, err := ToolPolicy(parsed.Rungs[index].Tools)
				if err != nil {
					t.Fatal(err)
				}
				if stage.ToolRole != wantRole || !reflect.DeepEqual(stage.AllowedTools, wantTools) || stage.ToolSet != parsed.Rungs[index].Tools {
					t.Errorf("rung %d tool policy = role %q, set %q, tools %q; want role %q, set %q, tools %q", index+1, stage.ToolRole, stage.ToolSet, stage.AllowedTools, wantRole, parsed.Rungs[index].Tools, wantTools)
				}
				if stage.Input != parsed.Rungs[index].Input {
					t.Errorf("rung %d input = %q, want recipe input %q", index+1, stage.Input, parsed.Rungs[index].Input)
				}
			}
			wantDirect := parsed.Kind == recipe.Direct || parsed.Kind == recipe.DirectEscalate
			for index, stage := range program.Builders {
				if wantDirect && stage.ToolRole != "builder_direct" {
					t.Errorf("rung %d did not use direct tool controller: %q", index+1, stage.ToolRole)
				}
				if !wantDirect && stage.ToolRole != "builder_shell" {
					t.Errorf("rung %d did not use shell tool controller: %q", index+1, stage.ToolRole)
				}
			}
			if wantPlan := recipeNeedsPlan(parsed); (program.Plan != nil) != wantPlan {
				t.Errorf("plan stage present=%t, want %t", program.Plan != nil, wantPlan)
			}
			if (program.Context != nil) != (parsed.Kind == recipe.Staged) || (program.Review != nil) != (parsed.Kind == recipe.Staged) {
				t.Errorf("context/review stages = %t/%t for %s", program.Context != nil, program.Review != nil, parsed.Kind)
			}
			stageCount := len(program.Builders)
			for _, optional := range []*StageSpec{program.Context, program.Plan, program.Review} {
				if optional != nil {
					stageCount++
				}
			}
			if stageCount != parsed.StageCount() {
				t.Errorf("composed stage count = %d, recipe stage count = %d", stageCount, parsed.StageCount())
			}
		})
	}
}

func TestResolveUsesEffectiveContextAndReviewerPinsAndSharedFallbackPolicy(t *testing.T) {
	roles := testRoles("chatgpt")
	roles.Effective["context"] = contract.RoleSettings{Provider: "chatgpt", Model: "gpt-6-luna", Effort: "max"}
	roles.Effective["reviewer"] = contract.RoleSettings{Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high"}
	parsed, _ := recipe.Parse("staged")
	program, err := Resolve(recipe.Build{Recipe: parsed}, roles, nil)
	if err != nil {
		t.Fatal(err)
	}
	if program.Context.Role.AssignedRole != "context" || program.Context.Role.Current != roles.Effective["context"] {
		t.Fatalf("context pin = %+v", program.Context.Role)
	}
	if program.Context.ToolSet != recipe.NoTools || len(program.Context.AllowedTools) != 0 || program.Plan.ToolSet != recipe.NoTools || len(program.Plan.AllowedTools) != 0 || program.Review.ToolSet != recipe.NoTools || len(program.Review.AllowedTools) != 0 {
		t.Fatal("context, planner, or reviewer stage exposed callable tools")
	}
	wantFallback := contract.RoleSettings{Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "medium"}
	if got := program.Context.Role.Fallback; got == nil || *got != wantFallback {
		t.Fatalf("context overload pin = %+v, want %+v", got, wantFallback)
	}
	if program.Review.Role.AssignedRole != "reviewer" || program.Review.Role.Current != roles.Effective["reviewer"] || program.Review.Role.Fallback == nil || *program.Review.Role.Fallback != wantFallback {
		t.Fatalf("reviewer pin = %+v", program.Review.Role)
	}

	disabled := false
	program, err = Resolve(recipe.Build{Recipe: parsed}, roles, &disabled)
	if err != nil {
		t.Fatal(err)
	}
	if program.Context.Role.Fallback != nil || program.Review.Role.Fallback != nil {
		t.Fatalf("disabled fallback retained pins: context=%+v reviewer=%+v", program.Context.Role.Fallback, program.Review.Role.Fallback)
	}
}

func TestResolveKeepsGrokStagesOnGrokWithoutOverloadFallback(t *testing.T) {
	roles := testRoles("grok")
	parsed, _ := recipe.Parse("direct-escalate")
	program, err := Resolve(recipe.Build{Recipe: parsed}, roles, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range program.Builders {
		if stage.Role.Current.Provider != "grok" || stage.Role.Current.Model != roles.Effective["builder"].Model || stage.Role.Fallback != nil {
			t.Errorf("Grok rung has wrong role pin: %+v", stage.Role)
		}
	}
	if program.Context != nil || program.Review != nil {
		t.Fatal("direct recipe unexpectedly added staged side effects")
	}
}

func TestStagedResolveRequiresEffectiveSameProviderContextAndReviewerRoles(t *testing.T) {
	parsed, _ := recipe.Parse("staged")
	roles := testRoles("chatgpt")
	delete(roles.Effective, "context")
	if _, err := Resolve(recipe.Build{Recipe: parsed}, roles, nil); err == nil {
		t.Fatal("staged recipe resolved without an effective context role")
	}
	roles = testRoles("chatgpt")
	roles.Effective["reviewer"] = contract.RoleSettings{Provider: "grok", Model: "grok-4.6", Effort: "high"}
	if _, err := Resolve(recipe.Build{Recipe: parsed}, roles, nil); err == nil {
		t.Fatal("staged recipe allowed a reviewer role to cross providers")
	}
}

func TestStagedRunOrdersContextPlanSharedBuildAndPreCommitReview(t *testing.T) {
	roles := testRoles("chatgpt")
	parsed, _ := recipe.Parse("staged")
	build := recipe.Build{Recipe: parsed, Settings: recipe.Settings{PlanMaxWords: 500, MaxRungs: 4}}
	var order []string
	var planSession, contextSession, reviewSession string
	stages := &stageRunnerFuncs{
		context: func(_ context.Context, call StageCall) (string, error) {
			order = append(order, string(call.Kind))
			contextSession = call.Session.Identity().ThreadID
			if call.Role.Current != roles.Effective["context"] || len(call.Task.IntentBytes) == 0 {
				t.Fatalf("bad context stage call: %+v", call)
			}
			return "repository context", nil
		},
		plan: func(_ context.Context, call StageCall) (string, error) {
			order = append(order, string(call.Kind))
			planSession = call.Session.Identity().ThreadID
			if call.Context != "repository context" || call.Role.Current != roles.Effective["planner"] {
				t.Fatalf("bad plan stage call: context=%q role=%+v", call.Context, call.Role)
			}
			return validPlan, nil
		},
		review: func(_ context.Context, call StageCall) (ReviewResult, error) {
			order = append(order, string(call.Kind))
			reviewSession = call.Session.Identity().ThreadID
			if call.Context != "repository context" || call.Plan == nil || call.Candidate == nil {
				t.Fatalf("pre-commit review did not receive the selected build: %+v", call)
			}
			if call.Role.Current != roles.Effective["reviewer"] || call.Candidate.Rung != "R1" || string(call.Candidate.Diff) != "reviewed candidate" {
				t.Fatalf("review role/candidate = %+v / %+v", call.Role, call.Candidate)
			}
			return ReviewResult{Text: "advisory review"}, nil
		},
	}
	buildController := buildControllerFunc(func(_ context.Context, request BuildRequest) (ladder.Outcome, error) {
		order = append(order, "build")
		if request.Ladder.Build.Recipe.Name != "staged" || request.Ladder.Plan == nil || request.Ladder.Plan.Difficulty != recipe.Easy {
			t.Fatalf("staged build request = recipe %q plan %+v", request.Ladder.Build.Recipe.Name, request.Ladder.Plan)
		}
		if len(request.Program.Builders) == 0 || request.Program.Builders[0].AllowedTools[0] != "shell" {
			t.Fatalf("staged builder tool program = %+v", request.Program.Builders)
		}
		candidate := selection.Candidate{Rung: "R1", Rank: 1, AttemptOrder: 1, Diff: []byte("reviewed candidate")}
		return ladder.Outcome{
			Status: ladder.StatusReady, Candidate: &candidate,
		}, nil
	})
	controller, err := NewController(Dependencies{Stages: stages, Build: buildController})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := controller.Run(context.Background(), Request{
		Ladder:  ladder.Request{RunID: testRunID, Build: build},
		Project: &project.Resolution{Origin: "/repo", Roles: roles}, CacheKey: testCacheID,
		Task: TaskInput{IntentBytes: []byte("approved intent"), RequestBytes: []byte("change it"), AcceptanceBytes: []byte("assert it")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"context", "plan", "build", "review"}) {
		t.Fatalf("stage order = %v", order)
	}
	if outcome.Context != "repository context" || outcome.Plan == nil || outcome.Review == nil || outcome.Review.Text != "advisory review" {
		t.Fatalf("staged outcome = %+v", outcome)
	}
	if contextSession == planSession || planSession == reviewSession || contextSession == reviewSession {
		t.Fatalf("stages reused conversation threads: %q %q %q", contextSession, planSession, reviewSession)
	}
	if string(outcome.Build.Candidate.Diff) != "reviewed candidate" {
		t.Fatalf("review changed the gate candidate bytes: %q", outcome.Build.Candidate.Diff)
	}
}

func TestDirectRunSkipsContextPlanAndReviewButUsesSharedBuildController(t *testing.T) {
	roles := testRoles("chatgpt")
	parsed, _ := recipe.Parse("direct-escalate")
	var called bool
	controller, err := NewController(Dependencies{Build: buildControllerFunc(func(_ context.Context, request BuildRequest) (ladder.Outcome, error) {
		called = true
		if request.Ladder.Build.Recipe.Name != "direct-escalate" || request.Ladder.Plan != nil {
			t.Fatalf("direct escalation was replanned or lost: recipe=%q plan=%+v", request.Ladder.Build.Recipe.Name, request.Ladder.Plan)
		}
		if len(request.Ladder.Build.Recipe.Rungs) != 3 || request.Ladder.Build.Recipe.Rungs[0].Tools != recipe.DirectTools || request.Ladder.Build.Recipe.Rungs[2].Tools != recipe.DirectTools {
			t.Fatalf("direct escalation changed shared rung policy: %+v", request.Ladder.Build.Recipe.Rungs)
		}
		if len(request.Program.Builders) != 3 || request.Program.Builders[0].ToolRole != "builder_direct" {
			t.Fatalf("direct escalation did not use shared direct tool controller: %+v", request.Program.Builders)
		}
		return ladder.Outcome{Status: ladder.StatusFailed, Reason: "best_candidate"}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := controller.Run(context.Background(), Request{
		Ladder:  ladder.Request{RunID: testRunID, Build: recipe.Build{Recipe: parsed}},
		Project: &project.Resolution{Roles: roles}, CacheKey: testCacheID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called || outcome.Plan != nil || outcome.Context != "" || outcome.Review != nil {
		t.Fatalf("direct outcome = %+v, controller_called=%t", outcome, called)
	}
}

func TestStagedRunStopsBeforeBuildWhenAStageFails(t *testing.T) {
	roles := testRoles("chatgpt")
	parsed, _ := recipe.Parse("staged")
	built := false
	controller, err := NewController(Dependencies{
		Stages: &stageRunnerFuncs{context: func(context.Context, StageCall) (string, error) { return "", errors.New("context failure") }},
		Build: buildControllerFunc(func(context.Context, BuildRequest) (ladder.Outcome, error) {
			built = true
			return ladder.Outcome{}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = controller.Run(context.Background(), Request{
		Ladder:  ladder.Request{RunID: testRunID, Build: recipe.Build{Recipe: parsed}},
		Project: &project.Resolution{Roles: roles}, CacheKey: testCacheID,
	})
	if err == nil || built {
		t.Fatalf("failed context stage reached builder: err=%v built=%t", err, built)
	}
}

const validPlan = "Difficulty: easy\n## Acceptance criteria\nA1 must pass.\n## Technical approach\nMake the requested change.\n## Implementation steps\n1. Change it."

func recipeNeedsPlan(value recipe.Recipe) bool { return hasPlanInput(value) }

func testRoles(provider string) contract.RoleManifest {
	model := "gpt-6.1-sol"
	effort := "high"
	if provider == "grok" {
		model = "grok-4.6"
	}
	roles := contract.RoleManifest{Effective: map[contract.RoleName]contract.RoleSettings{}}
	for _, role := range []contract.RoleName{"builder", "planner", "context", "reviewer", "shaper", "auditor"} {
		roleModel, roleEffort := model, effort
		if role == "builder" && provider == "chatgpt" {
			roleModel, roleEffort = "gpt-6-luna", "max"
		}
		roles.Effective[role] = contract.RoleSettings{Provider: provider, Model: roleModel, Effort: roleEffort}
	}
	return roles
}

type buildControllerFunc func(context.Context, BuildRequest) (ladder.Outcome, error)

func (f buildControllerFunc) Run(ctx context.Context, request BuildRequest) (ladder.Outcome, error) {
	return f(ctx, request)
}

type stageRunnerFuncs struct {
	context func(context.Context, StageCall) (string, error)
	plan    func(context.Context, StageCall) (string, error)
	review  func(context.Context, StageCall) (ReviewResult, error)
}

func (s *stageRunnerFuncs) Context(ctx context.Context, call StageCall) (string, error) {
	if s.context == nil {
		return "", errors.New("unexpected context stage")
	}
	return s.context(ctx, call)
}

func (s *stageRunnerFuncs) Plan(ctx context.Context, call StageCall) (string, error) {
	if s.plan == nil {
		return "", errors.New("unexpected plan stage")
	}
	return s.plan(ctx, call)
}

func (s *stageRunnerFuncs) Review(ctx context.Context, call StageCall) (ReviewResult, error) {
	if s.review == nil {
		return ReviewResult{}, errors.New("unexpected review stage")
	}
	return s.review(ctx, call)
}
