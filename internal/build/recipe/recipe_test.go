package recipe

import (
	"strings"
	"testing"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/project"
	"kogen-go/internal/yamlmini"
)

func TestAllowedRecipesAndEdgeSuffix(t *testing.T) {
	want := []string{"ladder", "ladder-diverse", "ladder-luna", "ladder-sol-low", "ladder-sol-medium", "ladder-sol-high", "plan-shell", "staged", "direct", "direct-escalate", "direct-shell", "escalate-shell"}
	for _, name := range want {
		if _, err := Parse(name); err != nil {
			t.Errorf("Parse(%q): %v", name, err)
		}
	}
	for _, name := range []string{"ladder", "ladder-diverse", "ladder-luna", "ladder-sol-low", "ladder-sol-medium", "ladder-sol-high"} {
		recipe, err := Parse(name + "+edge")
		if err != nil || !recipe.Edge {
			t.Errorf("Parse(%q): recipe=%+v err=%v", name+"+edge", recipe, err)
		}
	}
	for _, name := range []string{"", "Ladder", "unknown", "staged+edge", "direct+edge", "ladder+edge+edge"} {
		if _, err := Parse(name); err == nil {
			t.Errorf("Parse(%q) unexpectedly succeeded", name)
		} else if !strings.Contains(err.Error(), "build.recipe must be one of") {
			t.Errorf("Parse(%q) error = %q", name, err)
		}
	}
}

func TestRecipeRungsAndInputs(t *testing.T) {
	ladder, err := Resolve("ladder", defaultRoles("chatgpt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ladder.Rungs) != 4 {
		t.Fatalf("ladder has %d rungs", len(ladder.Rungs))
	}
	wantNames := []string{"builder", "sol-medium", "sol-high", "raw-request"}
	wantModels := []string{"gpt-6-luna", "gpt-6.1-sol", "gpt-6.1-sol", "gpt-6.1-sol"}
	wantEfforts := []string{"max", "medium", "high", "high"}
	for i, rung := range ladder.Rungs {
		if rung.Index != i+1 || rung.Name != wantNames[i] || rung.Model.Model != wantModels[i] || rung.Model.Effort != wantEfforts[i] || rung.Tools != ShellTools {
			t.Errorf("rung %d = %+v", i+1, rung)
		}
	}
	if ladder.Rungs[3].Input != RequestAndAcceptance || ladder.Rungs[2].Input != PlanInput {
		t.Errorf("default ladder input kinds = %q, %q", ladder.Rungs[2].Input, ladder.Rungs[3].Input)
	}
	diverse, err := Parse("ladder-diverse")
	if err != nil {
		t.Fatal(err)
	}
	if diverse.Rungs[1].Name != "sol-medium-raw" || diverse.Rungs[1].Input != RequestAndAcceptance {
		t.Fatalf("diverse second rung = %+v", diverse.Rungs[1])
	}
	for _, name := range []string{"ladder-luna", "ladder-sol-low", "ladder-sol-medium", "ladder-sol-high"} {
		recipe, err := Parse(name)
		if err != nil {
			t.Fatal(err)
		}
		if len(recipe.Rungs) != 4 || recipe.Rungs[0].Name != "builder" || recipe.Rungs[1].Name != "fresh-2" || recipe.Rungs[2].Name != "fresh-3" || recipe.Rungs[3].Name != "raw-request" {
			t.Errorf("%s rung names = %+v", name, recipe.Rungs)
		}
		for _, rung := range recipe.Rungs {
			if rung.Model.Model != recipe.Rungs[0].Model.Model || rung.Model.Effort != recipe.Rungs[0].Model.Effort {
				t.Errorf("%s is not uniform: %+v", name, recipe.Rungs)
				break
			}
		}
	}
	direct, err := Parse("direct")
	if err != nil || direct.Rungs[0].Tools != DirectTools || direct.Rungs[0].Input != RequestAndAcceptance {
		t.Fatalf("direct recipe = %+v, err=%v", direct, err)
	}
	if got := strings.Join(direct.Rungs[0].Tools.Names(), ","); got != "read,search,edit,write,shell,finish,tool_output" {
		t.Fatalf("direct tools = %q", got)
	}
	if (Recipe{Kind: PlanShell, Rungs: []Rung{{}}}).StageCount() != 2 {
		t.Fatal("plan-shell must include plan and builder stages")
	}
}

func TestResolveUsesEffectiveBuilderRoleAndKeepsRecipeEscalationModels(t *testing.T) {
	roles := defaultRoles("grok")
	recipe, err := Resolve("ladder", roles)
	if err != nil {
		t.Fatal(err)
	}
	if recipe.Rungs[0].Model.Provider != "grok" || recipe.Rungs[0].Model.Model != "grok-4.6" || recipe.Rungs[0].Model.Effort != "high" {
		t.Fatalf("first rung ignored effective Grok builder: %+v", recipe.Rungs[0])
	}
	if recipe.Rungs[1].Model.Provider != "chatgpt" || recipe.Rungs[1].Model.Model != "gpt-6.1-sol" {
		t.Fatalf("recipe escalation changed with builder role: %+v", recipe.Rungs[1])
	}
	if _, err := Resolve("ladder", contract.RoleManifest{}); err == nil {
		t.Fatal("missing effective builder role unexpectedly resolved")
	}
	staged, err := Resolve("staged", defaultRoles("chatgpt"))
	if err != nil || staged.Rungs[0].Model.Model != "gpt-6-luna" {
		t.Fatalf("staged first rung should use the resolved builder role: %+v err=%v", staged.Rungs[0], err)
	}
}

func TestLoadAppliesProjectMachineDefaultsAndPreservesRawExperimentSetting(t *testing.T) {
	projectBuild := yamlmini.Mapping{
		"recipe":         "ladder-luna",
		"wall_minutes":   "12",
		"plan_max_words": "450",
		"edge_tests":     "false",
		"ladder": yamlmini.Mapping{
			"max_rungs":       "2",
			"experimental_r4": "false",
		},
	}
	machineBuild := yamlmini.Mapping{
		"recipe":         "ladder-sol-low",
		"budget_ms":      "600000",
		"plan_max_words": "700",
		"edge_tests":     "true",
		"ladder": yamlmini.Mapping{
			"max_rungs": "3",
		},
	}
	resolved := &project.Resolution{
		Config:  &project.Config{Raw: yamlmini.Mapping{"build": projectBuild}},
		Machine: &project.MachineConfig{Build: machineBuild},
		Roles:   defaultRoles("chatgpt"),
	}
	build, err := Load(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if build.Recipe.Name != "ladder-luna" || build.Settings.Budget != 12*time.Minute || build.Settings.PlanMaxWords != 450 || build.Settings.MaxRungs != 2 || build.Settings.EdgeTests {
		t.Fatalf("project settings did not win: recipe=%q settings=%+v", build.Recipe.Name, build.Settings)
	}
	if build.Settings.ExperimentalR4 == nil || *build.Settings.ExperimentalR4 {
		t.Fatalf("raw experimental_r4 setting lost: %+v", build.Settings.ExperimentalR4)
	}
	if build.Settings.RepeatFrom == nil || *build.Settings.RepeatFrom != 2 || build.Settings.PlannerTools != NoTools || build.Settings.PlannerTimeout != 15*time.Minute || build.Settings.PlannerInputLimit != PlannerRequestLimit {
		t.Fatalf("planner/repeat contract = %+v", build.Settings)
	}
}

func TestRepeatCycleAndBudgetStop(t *testing.T) {
	recipe, err := Parse("ladder")
	if err != nil {
		t.Fatal(err)
	}
	first, ok, err := recipe.NextAttempt(4, 2, DefaultRepeatFrom(), true)
	if err != nil || !ok || first.Name != "sol-high-2" || first.Rung.Index != 3 {
		t.Fatalf("first repeat = %+v, ok=%v, err=%v", first, ok, err)
	}
	second, ok, err := recipe.NextAttempt(5, 2, DefaultRepeatFrom(), true)
	if err != nil || !ok || second.Name != "raw-request-2" || second.Rung.Index != 4 {
		t.Fatalf("second repeat = %+v, ok=%v, err=%v", second, ok, err)
	}
	third, ok, err := recipe.NextAttempt(6, 3, DefaultRepeatFrom(), true)
	if err != nil || !ok || third.Name != "sol-high-3" {
		t.Fatalf("third repeat = %+v, ok=%v, err=%v", third, ok, err)
	}
	if _, ok, err := recipe.NextAttempt(4, 2, nil, true); err != nil || ok {
		t.Fatalf("nil repeat_from should stop: ok=%v err=%v", ok, err)
	}
	if _, ok, err := recipe.NextAttempt(4, 2, DefaultRepeatFrom(), false); err != nil || ok {
		t.Fatalf("exhausted budget should stop: ok=%v err=%v", ok, err)
	}
}

func TestBuildNextAttemptHonorsMaxRungs(t *testing.T) {
	recipe, err := Resolve("ladder", defaultRoles("chatgpt"))
	if err != nil {
		t.Fatal(err)
	}
	limited := Build{Recipe: recipe, Settings: Settings{MaxRungs: 2, RepeatFrom: DefaultRepeatFrom()}}
	second, ok, err := limited.NextAttempt(1, 1, true)
	if err != nil || !ok || second.Rung.Name != "sol-medium" {
		t.Fatalf("second allowed rung=%+v ok=%v err=%v", second, ok, err)
	}
	if _, ok, err := limited.NextAttempt(2, 2, true); err != nil || ok {
		t.Fatalf("max_rungs=2 should stop before rung3/repeats: ok=%v err=%v", ok, err)
	}
	full := Build{Recipe: recipe, Settings: Settings{MaxRungs: 4, RepeatFrom: DefaultRepeatFrom()}}
	if _, ok, err := full.NextAttempt(3, 1, true); err == nil || ok || !strings.Contains(err.Error(), "experimental_r4") {
		t.Fatalf("unspecified R4 policy should remain explicit: ok=%v err=%v", ok, err)
	}
	experimentalOff := false
	full.Settings.ExperimentalR4 = &experimentalOff
	if attempt, ok, err := full.NextAttempt(3, 2, true); err != nil || !ok || attempt.Rung.Name == "raw-request" {
		t.Fatalf("experimental_r4=false should suppress R4: attempt=%+v ok=%v err=%v", attempt, ok, err)
	}
	experimentalOn := true
	full.Settings.ExperimentalR4 = &experimentalOn
	if attempt, ok, err := full.NextAttempt(3, 1, true); err != nil || !ok || attempt.Name != "raw-request" {
		t.Fatalf("experimental_r4=true should allow R4: attempt=%+v ok=%v err=%v", attempt, ok, err)
	}
	if attempt, ok, err := full.NextAttempt(4, 2, true); err != nil || !ok || attempt.Name != "sol-high-2" {
		t.Fatalf("full ladder repeat=%+v ok=%v err=%v", attempt, ok, err)
	}
}

func TestBuildAndLandingClockPauseProviderWaits(t *testing.T) {
	start := time.Unix(1_000, 0)
	budget := NewBuildBudget(start, 0)
	if got := budget.StageWall(start); got != MaxStageWall {
		t.Fatalf("initial stage wall=%s", got)
	}
	if got := budget.StageWall(start.Add(40 * time.Minute)); got != 20*time.Minute {
		t.Fatalf("stage wall should use remaining build time: %s", got)
	}
	if !budget.Pause(start.Add(20*time.Minute)) || budget.Pause(start.Add(25*time.Minute)) {
		t.Fatal("pause should only start once")
	}
	if got := budget.Used(start.Add(50 * time.Minute)); got != 20*time.Minute {
		t.Fatalf("used while paused=%s", got)
	}
	if !budget.Resume(start.Add(50*time.Minute)) || budget.Resume(start.Add(51*time.Minute)) {
		t.Fatal("resume should only close an active pause")
	}
	if got := budget.Report(start.Add(55 * time.Minute)); got.UsedMS != int64((25*time.Minute).Milliseconds()) || got.PausedMS != int64((30*time.Minute).Milliseconds()) {
		t.Fatalf("report=%+v", got)
	}
	if got := budget.Remaining(start.Add(90 * time.Minute)); got != 0 || !budget.Exhausted(start.Add(90*time.Minute)) {
		t.Fatalf("budget should exhaust after 60 active minutes: remaining=%s", got)
	}

	landingStart := start.Add(55 * time.Minute)
	if !budget.BeginLanding(landingStart) || budget.BeginLanding(landingStart.Add(time.Second)) {
		t.Fatal("landing allowance should start once")
	}
	if got, ok := budget.LandingRemaining(landingStart.Add(4 * time.Minute)); !ok || got != 6*time.Minute {
		t.Fatalf("landing remaining=%s present=%v", got, ok)
	}
	budget.Pause(landingStart.Add(5 * time.Minute))
	if got, ok := budget.LandingRemaining(landingStart.Add(25 * time.Minute)); !ok || got != 5*time.Minute {
		t.Fatalf("landing wait should pause its allowance: %s present=%v", got, ok)
	}
	budget.Resume(landingStart.Add(25 * time.Minute))
	if got, ok := budget.LandingRemaining(landingStart.Add(27 * time.Minute)); !ok || got != 3*time.Minute {
		t.Fatalf("landing allowance after wait=%s present=%v", got, ok)
	}
}

func TestParsePlanFormatAndWrapperCount(t *testing.T) {
	response := "Difficulty: hard\n## Acceptance criteria\nA1 passes\n## Technical approach\nUse the existing controller.\n## Implementation steps\nImplement and verify."
	plan, err := ParsePlan(response, "wrapper context", 300)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Difficulty != Hard || plan.Text != response || plan.WordCount != len(strings.Fields("wrapper context\n"+response)) {
		t.Fatalf("plan=%+v", plan)
	}
	for _, invalid := range []string{
		"\nDifficulty: easy\n## Acceptance criteria\n## Technical approach\n## Implementation steps",
		"Difficulty: medium\n## Acceptance criteria\n## Technical approach\n## Implementation steps",
		"Difficulty: easy\n## Technical approach\n## Acceptance criteria\n## Implementation steps",
		"Difficulty: easy\n## Acceptance criteria\n## Implementation steps",
	} {
		if _, err := ParsePlan(invalid, "", 300); err == nil {
			t.Errorf("invalid plan unexpectedly accepted: %q", invalid)
		}
	}
	if _, err := ParsePlan(response, strings.Repeat("wrapper ", 300), 300); err == nil || !strings.Contains(err.Error(), "including wrapper text") {
		t.Fatalf("wrapper words were not counted: %v", err)
	}
	if _, err := ParsePlan(response, "", 299); err == nil {
		t.Fatal("out-of-range plan word cap unexpectedly accepted")
	}
}

func defaultRoles(provider string) contract.RoleManifest {
	manifest, err := project.ResolveRoles(provider, nil, nil)
	if err != nil {
		panic(err)
	}
	return manifest
}
