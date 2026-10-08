package diagnostic

import (
	"kogen-go/internal/build/ladder"
	"kogen-go/internal/build/recipe"
	"kogen-go/internal/build/repair"
	buildselect "kogen-go/internal/build/select"
)

// OrchestrationObservation is a snapshot of one production ladder outcome and
// the production selector report, when the caller has one. It contains result
// values only; it does not synthesize phase transitions or rerun selection.
type OrchestrationObservation struct {
	Status            ladder.Status          `json:"status"`
	Reason            string                 `json:"reason"`
	LastAttemptReason string                 `json:"last_attempt_reason"`
	Build             BuildObservation       `json:"build"`
	Candidates        []CandidateObservation `json:"candidates"`
	Workspaces        []WorkspaceObservation `json:"workspaces"`
	Summaries         []FailureSummary       `json:"summaries"`
	Candidate         *CandidateObservation  `json:"candidate"`
	Selection         *SelectionObservation  `json:"selection"`
}

type BuildObservation struct {
	Recipe   RecipeObservation `json:"recipe"`
	Settings BuildSettings     `json:"settings"`
}

type RecipeObservation struct {
	Name  string            `json:"name"`
	Kind  recipe.Kind       `json:"kind"`
	Edge  bool              `json:"edge"`
	Rungs []RungObservation `json:"rungs"`
}

type RungObservation struct {
	Index    int              `json:"index"`
	Name     string           `json:"name"`
	Provider string           `json:"provider"`
	Model    string           `json:"model"`
	Effort   string           `json:"effort"`
	Input    recipe.InputKind `json:"input"`
	Tools    recipe.ToolSet   `json:"tools"`
}

type BuildSettings struct {
	RecipeName        string          `json:"recipe_name"`
	BudgetMS          int64           `json:"budget_ms"`
	PlanMaxWords      int             `json:"plan_max_words"`
	MaxRungs          int             `json:"max_rungs"`
	EdgeTests         bool            `json:"edge_tests"`
	ExperimentalR4    *bool           `json:"experimental_r4"`
	RepeatFrom        *int            `json:"repeat_from"`
	Planner           RoleObservation `json:"planner"`
	PlannerTools      recipe.ToolSet  `json:"planner_tools"`
	PlannerTimeoutMS  int64           `json:"planner_timeout_ms"`
	PlannerInputLimit int             `json:"planner_input_limit"`
}

type CandidateObservation struct {
	Rung           string           `json:"rung"`
	Rank           int              `json:"rank"`
	AttemptOrder   int              `json:"attempt_order"`
	Ref            string           `json:"ref"`
	Diff           []byte           `json:"diff"`
	SnapshotReason repair.Reason    `json:"snapshot_reason"`
	Gate           *GateObservation `json:"gate"`
}

type RoleObservation struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
}

type WorkspaceObservation struct {
	Path       string `json:"path"`
	RunID      string `json:"run_id"`
	Rung       string `json:"rung"`
	BaseCommit string `json:"base_commit"`
}

type FailureSummary struct {
	RungLabel string   `json:"rung_label"`
	Attempt   string   `json:"attempt"`
	Model     string   `json:"model"`
	EndReason string   `json:"end_reason"`
	Lines     []string `json:"lines"`
}

type CandidateDiff struct {
	Rung string `json:"rung"`
	Diff []byte `json:"diff"`
}

type SelectionObservation struct {
	Candidates     []buildselect.CandidateSnapshot `json:"candidates"`
	CandidateDiffs []CandidateDiff                 `json:"candidate_diffs"`
	Ranking        []buildselect.CandidateSummary  `json:"ranking"`
	Winner         buildselect.CandidateSummary    `json:"winner"`
	BestUnverified *buildselect.CandidateSummary   `json:"best_unverified"`
	BestDiff       []byte                          `json:"best_diff"`
	BestDiffPath   string                          `json:"best_diff_path"`
	AuditMode      string                          `json:"audit_mode"`
	Demoted        bool                            `json:"demoted"`
	AdvisoryItems  []string                        `json:"advisory_items"`
}

// ObserveOrchestration maps a production ladder Outcome and its already
// computed production selection report. A nil report stays nil; this adapter
// never invokes a replacement selector or invents a winner.
func ObserveOrchestration(outcome ladder.Outcome, selected *buildselect.Report) (OrchestrationObservation, error) {
	observation := OrchestrationObservation{
		Status: outcome.Status, Reason: outcome.Reason, LastAttemptReason: outcome.LastAttemptReason,
		Build: buildObservation(outcome.Build), Candidates: make([]CandidateObservation, 0, len(outcome.Candidates)),
		Workspaces: make([]WorkspaceObservation, 0, len(outcome.Workspaces)),
		Summaries:  make([]FailureSummary, 0, len(outcome.Summaries)),
	}
	for _, candidate := range outcome.Candidates {
		mapped, err := observeCandidate(candidate)
		if err != nil {
			return OrchestrationObservation{}, err
		}
		observation.Candidates = append(observation.Candidates, mapped)
	}
	for _, work := range outcome.Workspaces {
		observation.Workspaces = append(observation.Workspaces, WorkspaceObservation{
			Path: work.Path, RunID: work.RunID, Rung: work.Rung, BaseCommit: string(work.BaseCommit),
		})
	}
	for _, summary := range outcome.Summaries {
		lines := append([]string(nil), summary.Lines...)
		if lines == nil {
			lines = []string{}
		}
		observation.Summaries = append(observation.Summaries, FailureSummary{
			RungLabel: summary.RungLabel, Attempt: summary.Attempt, Model: summary.Model,
			EndReason: summary.EndReason, Lines: lines,
		})
	}
	if outcome.Candidate != nil {
		candidate, err := observeCandidate(*outcome.Candidate)
		if err != nil {
			return OrchestrationObservation{}, err
		}
		observation.Candidate = &candidate
	}
	if selected != nil {
		mapped := selectionObservation(*selected, outcome.Candidates)
		observation.Selection = &mapped
	}
	return observation, nil
}

func buildObservation(build recipe.Build) BuildObservation {
	rungs := make([]RungObservation, 0, len(build.Recipe.Rungs))
	for _, rung := range build.Recipe.Rungs {
		rungs = append(rungs, RungObservation{
			Index: rung.Index, Name: rung.Name, Provider: rung.Model.Provider, Model: rung.Model.Model,
			Effort: rung.Model.Effort, Input: rung.Input, Tools: rung.Tools,
		})
	}
	settings := build.Settings
	return BuildObservation{
		Recipe: RecipeObservation{Name: build.Recipe.Name, Kind: build.Recipe.Kind, Edge: build.Recipe.Edge, Rungs: rungs},
		Settings: BuildSettings{
			RecipeName: settings.RecipeName, BudgetMS: settings.Budget.Milliseconds(),
			PlanMaxWords: settings.PlanMaxWords, MaxRungs: settings.MaxRungs, EdgeTests: settings.EdgeTests,
			ExperimentalR4: cloneBool(settings.ExperimentalR4), RepeatFrom: cloneInteger(settings.RepeatFrom),
			Planner: RoleObservation{
				Provider: settings.Planner.Provider, Model: settings.Planner.Model, Effort: settings.Planner.Effort,
			}, PlannerTools: settings.PlannerTools,
			PlannerTimeoutMS: settings.PlannerTimeout.Milliseconds(), PlannerInputLimit: settings.PlannerInputLimit,
		},
	}
}

func observeCandidate(candidate buildselect.Candidate) (CandidateObservation, error) {
	observation := CandidateObservation{
		Rung: candidate.Rung, Rank: candidate.Rank, AttemptOrder: candidate.AttemptOrder,
		Ref: candidate.Ref, Diff: append([]byte(nil), candidate.Diff...), SnapshotReason: candidate.SnapshotReason,
	}
	if candidate.Gate != nil {
		mapped, err := ObserveGate(candidate.Gate)
		if err != nil {
			return CandidateObservation{}, err
		}
		observation.Gate = &mapped
	}
	return observation, nil
}

func selectionObservation(report buildselect.Report, candidates []buildselect.Candidate) SelectionObservation {
	selection := SelectionObservation{
		Candidates:     append([]buildselect.CandidateSnapshot(nil), report.Candidates...),
		CandidateDiffs: make([]CandidateDiff, 0, len(candidates)),
		Ranking:        append([]buildselect.CandidateSummary(nil), report.Ranking...),
		Winner:         report.Winner, BestDiff: append([]byte(nil), report.BestDiff...),
		BestDiffPath: report.BestDiffPath, AuditMode: report.AuditMode,
		Demoted: report.Demoted, AdvisoryItems: append([]string(nil), report.AdvisoryItems...),
	}
	if report.BestUnverified != nil {
		best := *report.BestUnverified
		selection.BestUnverified = &best
	}
	for _, candidate := range candidates {
		selection.CandidateDiffs = append(selection.CandidateDiffs, CandidateDiff{
			Rung: candidate.Rung, Diff: append([]byte(nil), candidate.Diff...),
		})
	}
	if selection.Candidates == nil {
		selection.Candidates = []buildselect.CandidateSnapshot{}
	}
	if selection.Ranking == nil {
		selection.Ranking = []buildselect.CandidateSummary{}
	}
	if selection.AdvisoryItems == nil {
		selection.AdvisoryItems = []string{}
	}
	return selection
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneInteger(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
