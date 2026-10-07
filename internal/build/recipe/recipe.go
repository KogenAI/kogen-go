package recipe

import (
	"fmt"
	"strings"

	"kogen-go/internal/contract"
)

const (
	PlannerRequestLimit = 160_000
	PlannerTimeoutMS    = 900_000
	DefaultPlanMaxWords = 500
	MinPlanMaxWords     = 300
	MaxPlanMaxWords     = 2_000
)

var allowedRecipes = []string{
	"ladder", "ladder-diverse", "ladder-luna", "ladder-sol-low",
	"ladder-sol-medium", "ladder-sol-high", "plan-shell", "staged",
	"direct", "direct-escalate", "direct-shell", "escalate-shell",
}

type Kind string

const (
	Ladder         Kind = "ladder"
	PlanShell      Kind = "plan-shell"
	Staged         Kind = "staged"
	Direct         Kind = "direct"
	DirectEscalate Kind = "direct-escalate"
	DirectShell    Kind = "direct-shell"
	EscalateShell  Kind = "escalate-shell"
)

type InputKind string

const (
	PlanInput            InputKind = "plan"
	RequestAndAcceptance InputKind = "request-and-acceptance"
)

type ToolSet string

const (
	NoTools     ToolSet = "none"
	ShellTools  ToolSet = "shell"
	DirectTools ToolSet = "direct"
)

func (t ToolSet) Names() []string {
	switch t {
	case ShellTools:
		return []string{"shell", "finish", "tool_output"}
	case DirectTools:
		return []string{"read", "search", "edit", "write", "shell", "finish", "tool_output"}
	default:
		return []string{}
	}
}

type Rung struct {
	Index int
	Name  string
	Model contract.RoleSettings
	Input InputKind
	Tools ToolSet
}

type Recipe struct {
	Name  string
	Kind  Kind
	Edge  bool
	Rungs []Rung
}

type RecipeError struct {
	Rejected string
}

func (e *RecipeError) Error() string {
	return fmt.Sprintf("build.recipe must be one of %s; rejected value %q", strings.Join(allowedRecipes, ", "), e.Rejected)
}

// Parse constructs a recipe from its exact public spelling. The +edge suffix
// is accepted for ladder recipes only; aliases and case folding are rejected.
func Parse(name string) (Recipe, error) {
	base, edge := name, false
	if strings.HasSuffix(name, "+edge") {
		base = strings.TrimSuffix(name, "+edge")
		edge = true
	}

	kind := Ladder
	var rungs []Rung
	switch base {
	case "ladder":
		rungs = defaultLadder(false)
	case "ladder-diverse":
		rungs = defaultLadder(true)
	case "ladder-luna":
		rungs = uniformLadder(model("chatgpt", "gpt-6-luna", "max"))
	case "ladder-sol-low":
		rungs = uniformLadder(model("chatgpt", "gpt-6.1-sol", "low"))
	case "ladder-sol-medium":
		rungs = uniformLadder(model("chatgpt", "gpt-6.1-sol", "medium"))
	case "ladder-sol-high":
		rungs = uniformLadder(model("chatgpt", "gpt-6.1-sol", "high"))
	case "plan-shell":
		kind = PlanShell
		rungs = []Rung{newRung(1, "builder", model("chatgpt", "gpt-6-luna", "max"), PlanInput, ShellTools)}
	case "staged":
		kind = Staged
		rungs = defaultLadder(false)
	case "direct":
		kind = Direct
		rungs = directRungs(false, false)
	case "direct-escalate":
		kind = DirectEscalate
		rungs = directRungs(true, false)
	case "direct-shell":
		kind = DirectShell
		rungs = directRungs(false, true)
	case "escalate-shell":
		kind = EscalateShell
		rungs = directRungs(true, true)
	default:
		return Recipe{}, &RecipeError{Rejected: name}
	}
	if edge && kind != Ladder {
		return Recipe{}, &RecipeError{Rejected: name}
	}
	return Recipe{Name: name, Kind: kind, Edge: edge, Rungs: rungs}, nil
}

// Resolve applies the effective builder role to recipes whose first stage is
// the configured builder. Escalation models remain recipe-owned; uniform-model
// recipe variants retain the model selected by their explicit recipe name.
// Role defaults and project/machine precedence are owned by project.ResolveRoles.
func Resolve(name string, roles contract.RoleManifest) (Recipe, error) {
	recipe, err := Parse(name)
	if err != nil {
		return Recipe{}, err
	}
	builder, ok := roles.Effective[contract.RoleName("builder")]
	if !ok || builder.Provider == "" || builder.Model == "" || builder.Effort == "" {
		return Recipe{}, fmt.Errorf("effective builder role is required to resolve recipe %q", name)
	}
	if len(recipe.Rungs) != 0 && (recipe.Kind == PlanShell || recipe.Kind == Staged || recipe.Kind == Direct || recipe.Kind == DirectEscalate || recipe.Kind == DirectShell || recipe.Kind == EscalateShell || isConfiguredBuilderLadder(name)) {
		recipe.Rungs[0].Model = builder
	}
	return recipe, nil
}

func isConfiguredBuilderLadder(name string) bool {
	base := strings.TrimSuffix(name, "+edge")
	return base == "ladder" || base == "ladder-diverse"
}

type Schedule struct {
	Parallel bool
	Rungs    []Rung
}

// EntrySchedule starts the first two ladder attempts concurrently for a hard
// plan unless the configured cap leaves only one available rung.
func (r Recipe) EntrySchedule(hard bool, maxRungs int) Schedule {
	if maxRungs < 1 {
		maxRungs = 1
	}
	count := len(r.Rungs)
	if count > maxRungs {
		count = maxRungs
	}
	if count == 0 {
		return Schedule{}
	}
	if hard && (r.Kind == Ladder || r.Kind == Staged) && count >= 2 {
		return Schedule{Parallel: true, Rungs: append([]Rung(nil), r.Rungs[:2]...)}
	}
	return Schedule{Rungs: []Rung{r.Rungs[0]}}
}

// StageCount includes pre/post stages defined by the recipe; each rung is one
// builder stage. Staged reserves context and review around planning/building.
func (r Recipe) StageCount() int {
	switch r.Kind {
	case PlanShell:
		return 1 + len(r.Rungs) // plan, then its builder
	case Direct, DirectShell:
		return len(r.Rungs)
	case DirectEscalate, EscalateShell:
		return len(r.Rungs)
	case Staged:
		return 3 + len(r.Rungs) // context, plan, rungs, review
	default:
		return 1 + len(r.Rungs) // plan, then rungs
	}
}

type RungAttempt struct {
	Rung   Rung
	Number int
	Name   string
}

// NextAttempt returns the next original rung, then cycles from repeatFrom
// (zero-based) when supplied. Repeats are fresh attempts and are named with
// their attempt ordinal, for example sol-high-2 and raw-request-3.
func (r Recipe) NextAttempt(completedRungs, repeatNumber int, repeatFrom *int, budgetLeft bool) (RungAttempt, bool, error) {
	if !budgetLeft || len(r.Rungs) == 0 {
		return RungAttempt{}, false, nil
	}
	if completedRungs < 0 {
		return RungAttempt{}, false, fmt.Errorf("completed rung count cannot be negative")
	}
	if completedRungs < len(r.Rungs) {
		rung := r.Rungs[completedRungs]
		return RungAttempt{Rung: rung, Number: 1, Name: rung.Name}, true, nil
	}
	if repeatFrom == nil {
		return RungAttempt{}, false, nil
	}
	if *repeatFrom < 0 || *repeatFrom >= len(r.Rungs) {
		return RungAttempt{}, false, fmt.Errorf("repeat_from must be a zero-based rung index from 0 to %d", len(r.Rungs)-1)
	}
	cycleLength := len(r.Rungs) - *repeatFrom
	index := *repeatFrom + (completedRungs-len(r.Rungs))%cycleLength
	if repeatNumber < 2 {
		repeatNumber = 2
	}
	rung := r.Rungs[index]
	return RungAttempt{Rung: rung, Number: repeatNumber, Name: fmt.Sprintf("%s-%d", rung.Name, repeatNumber)}, true, nil
}

// NextAttempt applies the resolved max_rungs cap before considering the repeat
// cycle. A repeat start beyond the configured cap means the cycle is empty.
func (b Build) NextAttempt(completedRungs, repeatNumber int, budgetLeft bool) (RungAttempt, bool, error) {
	if len(b.Recipe.Rungs) == 0 || b.Settings.MaxRungs < 1 {
		return RungAttempt{}, false, nil
	}
	count := b.Settings.MaxRungs
	if count > len(b.Recipe.Rungs) {
		count = len(b.Recipe.Rungs)
	}
	if count >= 4 {
		if b.Settings.ExperimentalR4 == nil {
			if completedRungs >= 3 {
				return RungAttempt{}, false, fmt.Errorf("build.ladder.experimental_r4 does not resolve whether the experimental raw-request rung is enabled")
			}
		} else if !*b.Settings.ExperimentalR4 {
			count = 3
		}
	}
	recipe := b.Recipe
	recipe.Rungs = recipe.Rungs[:count]
	repeatFrom := b.Settings.RepeatFrom
	if repeatFrom != nil && *repeatFrom >= count {
		repeatFrom = nil
	}
	return recipe.NextAttempt(completedRungs, repeatNumber, repeatFrom, budgetLeft)
}

// DefaultRepeatFrom returns the spec's repeat start: the third rung (index 2).
func DefaultRepeatFrom() *int {
	index := 2
	return &index
}

func defaultLadder(diverse bool) []Rung {
	secondName, secondInput := "sol-medium", PlanInput
	if diverse {
		secondName, secondInput = "sol-medium-raw", RequestAndAcceptance
	}
	return []Rung{
		newRung(1, "builder", model("chatgpt", "gpt-6-luna", "max"), PlanInput, ShellTools),
		newRung(2, secondName, model("chatgpt", "gpt-6.1-sol", "medium"), secondInput, ShellTools),
		newRung(3, "sol-high", model("chatgpt", "gpt-6.1-sol", "high"), PlanInput, ShellTools),
		newRung(4, "raw-request", model("chatgpt", "gpt-6.1-sol", "high"), RequestAndAcceptance, ShellTools),
	}
}

func uniformLadder(value contract.RoleSettings) []Rung {
	names := []string{"builder", "fresh-2", "fresh-3", "raw-request"}
	rungs := make([]Rung, 0, len(names))
	for index, name := range names {
		input := PlanInput
		if index == len(names)-1 {
			input = RequestAndAcceptance
		}
		rungs = append(rungs, newRung(index+1, name, value, input, ShellTools))
	}
	return rungs
}

func directRungs(escalate, shellOnly bool) []Rung {
	tools := DirectTools
	if shellOnly {
		tools = ShellTools
	}
	rungs := []Rung{newRung(1, "builder", model("chatgpt", "gpt-6-luna", "max"), RequestAndAcceptance, tools)}
	if escalate {
		rungs = append(rungs,
			newRung(2, "fresh-2", model("chatgpt", "gpt-6.1-sol", "medium"), RequestAndAcceptance, tools),
			newRung(3, "fresh-3", model("chatgpt", "gpt-6.1-sol", "high"), RequestAndAcceptance, tools),
		)
	}
	return rungs
}

func model(provider, id, effort string) contract.RoleSettings {
	return contract.RoleSettings{Provider: provider, Model: id, Effort: effort}
}

func newRung(index int, name string, model contract.RoleSettings, input InputKind, tools ToolSet) Rung {
	return Rung{Index: index, Name: name, Model: model, Input: input, Tools: tools}
}
