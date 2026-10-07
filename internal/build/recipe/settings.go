package recipe

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/project"
	"kogen-go/internal/yamlmini"
)

const DefaultMaxRungs = 4

type Settings struct {
	RecipeName        string
	Budget            time.Duration
	PlanMaxWords      int
	MaxRungs          int
	EdgeTests         bool
	ExperimentalR4    *bool // raw experimental setting; policy for R4 remains unresolved in the draft.
	RepeatFrom        *int
	Planner           contract.RoleSettings
	PlannerTools      ToolSet
	PlannerTimeout    time.Duration
	PlannerInputLimit int
}

type Build struct {
	Recipe   Recipe
	Settings Settings
}

// Load resolves recipe and budget settings from an already validated project
// resolution. Scalar settings follow project → machine → default precedence;
// role models come only from project.ResolveRoles' effective manifest.
func Load(resolved *project.Resolution) (Build, error) {
	if resolved == nil {
		return Build{}, fmt.Errorf("project resolution is required")
	}
	projectBuild := yamlmini.Mapping(nil)
	if resolved.Config != nil {
		projectBuild, _ = resolved.Config.Raw["build"].(yamlmini.Mapping)
	}
	var machineBuild yamlmini.Mapping
	if resolved.Machine != nil {
		machineBuild = resolved.Machine.Build
	}

	recipeName := stringSetting(projectBuild, machineBuild, "recipe", "ladder")
	recipe, err := Resolve(recipeName, resolved.Roles)
	if err != nil {
		return Build{}, err
	}
	planner, ok := resolved.Roles.Effective[contract.RoleName("planner")]
	if !ok || planner.Provider == "" || planner.Model == "" || planner.Effort == "" {
		return Build{}, fmt.Errorf("effective planner role is required to resolve recipe %q", recipeName)
	}

	budget, err := resolveBudget(projectBuild, machineBuild)
	if err != nil {
		return Build{}, err
	}
	wordLimit, err := resolveWordLimit(projectBuild, machineBuild)
	if err != nil {
		return Build{}, err
	}
	maxRungs, err := resolveMaxRungs(projectBuild, machineBuild)
	if err != nil {
		return Build{}, err
	}
	experimentalR4, err := optionalBoolLadder(projectBuild, machineBuild, "experimental_r4")
	if err != nil {
		return Build{}, err
	}
	settings := Settings{
		RecipeName:        recipeName,
		Budget:            budget,
		PlanMaxWords:      wordLimit,
		MaxRungs:          maxRungs,
		EdgeTests:         boolSetting(projectBuild, machineBuild, "edge_tests", false) || recipe.Edge,
		ExperimentalR4:    experimentalR4,
		RepeatFrom:        DefaultRepeatFrom(),
		Planner:           planner,
		PlannerTools:      NoTools,
		PlannerTimeout:    time.Duration(PlannerTimeoutMS) * time.Millisecond,
		PlannerInputLimit: PlannerRequestLimit,
	}
	return Build{Recipe: recipe, Settings: settings}, nil
}

func resolveBudget(projectBuild, machineBuild yamlmini.Mapping) (time.Duration, error) {
	// budget_ms is the precise spelling; wall_minutes is the accepted whole-
	// minute spelling. Project values win over machine values, including when
	// the two sources use different spellings.
	for _, source := range []yamlmini.Mapping{projectBuild, machineBuild} {
		if value, exists := source["budget_ms"]; exists {
			n, ok := unsigned(value)
			if !ok || n == 0 || n > uint64(math.MaxInt64/int64(time.Millisecond)) {
				return 0, fmt.Errorf("build.budget_ms must be a positive integer within the supported clock range")
			}
			return time.Duration(n) * time.Millisecond, nil
		}
		if value, exists := source["wall_minutes"]; exists {
			n, ok := unsigned(value)
			if !ok || n == 0 || n > uint64(math.MaxInt64/int64(time.Minute)) {
				return 0, fmt.Errorf("build.wall_minutes must be a positive integer within the supported clock range")
			}
			return time.Duration(n) * time.Minute, nil
		}
	}
	return DefaultBuildWall, nil
}

func resolveWordLimit(projectBuild, machineBuild yamlmini.Mapping) (int, error) {
	value, exists := setting(projectBuild, machineBuild, "plan_max_words")
	if !exists {
		return DefaultPlanMaxWords, nil
	}
	n, ok := unsigned(value)
	if !ok || n < MinPlanMaxWords || n > MaxPlanMaxWords {
		return 0, fmt.Errorf("build.plan_max_words must be an integer from %d to %d", MinPlanMaxWords, MaxPlanMaxWords)
	}
	return int(n), nil
}

func resolveMaxRungs(projectBuild, machineBuild yamlmini.Mapping) (int, error) {
	projectLadder, _ := projectBuild["ladder"].(yamlmini.Mapping)
	machineLadder, _ := machineBuild["ladder"].(yamlmini.Mapping)
	value, exists := setting(projectLadder, machineLadder, "max_rungs")
	if !exists {
		return DefaultMaxRungs, nil
	}
	n, ok := unsigned(value)
	if !ok || n < 1 || n > DefaultMaxRungs {
		return 0, fmt.Errorf("build.ladder.max_rungs must be an integer from 1 to %d", DefaultMaxRungs)
	}
	return int(n), nil
}

func optionalBoolLadder(projectBuild, machineBuild yamlmini.Mapping, key string) (*bool, error) {
	projectLadder, _ := projectBuild["ladder"].(yamlmini.Mapping)
	machineLadder, _ := machineBuild["ladder"].(yamlmini.Mapping)
	value, exists := setting(projectLadder, machineLadder, key)
	if !exists {
		return nil, nil
	}
	parsed, ok := boolean(value)
	if !ok {
		return nil, fmt.Errorf("build.ladder.%s must be true or false", key)
	}
	return &parsed, nil
}

func stringSetting(projectBuild, machineBuild yamlmini.Mapping, key, fallback string) string {
	value, exists := setting(projectBuild, machineBuild, key)
	if !exists {
		return fallback
	}
	result, _ := value.(string)
	return result
}

func boolSetting(projectBuild, machineBuild yamlmini.Mapping, key string, fallback bool) bool {
	value, exists := setting(projectBuild, machineBuild, key)
	if !exists {
		return fallback
	}
	result, ok := boolean(value)
	if !ok {
		return fallback
	}
	return result
}

func setting(project, machine yamlmini.Mapping, key string) (yamlmini.Value, bool) {
	if value, ok := project[key]; ok {
		return value, true
	}
	value, ok := machine[key]
	return value, ok
}

func unsigned(value yamlmini.Value) (uint64, bool) {
	text, ok := value.(string)
	if !ok || text == "" || text[0] == '+' || text[0] == '-' {
		return 0, false
	}
	n, err := strconv.ParseUint(text, 10, 64)
	return n, err == nil
}

func boolean(value yamlmini.Value) (bool, bool) {
	text, ok := value.(string)
	if !ok {
		return false, false
	}
	switch text {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}
