package integrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/gate"
	"kogen-go/internal/gitio"
	"kogen-go/internal/journal"
	"kogen-go/internal/process"
)

// Base is the immutable commit/tree pair on which one Build phase operates.
type Base struct {
	Commit contract.ObjectID
	Tree   contract.ObjectID
}

// BaselineRunner runs each configured check once against a scratch copy of
// base. It must return observations, not inferred booleans. The returned rows
// are checked against Checks so an incomplete set cannot replace the baseline.
type BaselineRunner func(context.Context, Base, []contract.CheckSpec) ([]gate.BaselineCheck, error)

// RefreshRequest identifies the approval base, the private candidate
// workspace, and the current check definitions at Build start.
type RefreshRequest struct {
	Processes       contract.ProcessRunner
	Environment     process.Environment
	Repository      string
	Workspace       string
	Branch          string
	Previous        Base
	Checks          []contract.CheckSpec
	Baseline        *gate.CheckBaseline
	BaselineHistory []gate.CheckBaseline
	RunStore        *journal.RunStore
	Snapshot        *journal.RunSnapshot
	Clock           contract.Clock
	RunBaseChecks   BaselineRunner
}

// RefreshResult is the base selected for the Build. Baseline is always bound
// to Base.Tree; History retains prior exact-tree observations for the run.
type RefreshResult struct {
	Base     Base
	Moved    bool
	Baseline *gate.CheckBaseline
	History  []gate.CheckBaseline
}

// Refresh resolves the target branch from the origin into a private ref in the
// Build workspace before Build work starts. When it moved, it durably records
// the move, runs the complete configured base-check set once, and returns the
// new tree-bound baseline for the caller to use in the gate.
func Refresh(ctx context.Context, request RefreshRequest) (RefreshResult, error) {
	if ctx == nil {
		return RefreshResult{}, errors.New("landing integration: context is required")
	}
	if err := validateBase(request.Previous); err != nil {
		return RefreshResult{}, fmt.Errorf("landing integration: previous Build base: %w", err)
	}
	repository, workspace, err := validatePaths(request.Repository, request.Workspace)
	if err != nil {
		return RefreshResult{}, err
	}
	git, policy, err := workspaceGit(request.Processes, workspace, request.Environment)
	if err != nil {
		return RefreshResult{}, err
	}
	if err := resetWorkspaceGitConfig(ctx, git, policy, workspace); err != nil {
		return RefreshResult{}, err
	}
	base, tree, err := fetchBase(ctx, git, policy, repository, request.Branch)
	if err != nil {
		return RefreshResult{}, err
	}
	result := RefreshResult{
		Base:    Base{Commit: base, Tree: tree},
		History: cloneBaselineHistory(request.BaselineHistory),
	}
	if request.Baseline != nil {
		result.Baseline = cloneBaseline(request.Baseline)
		result.History = mergeBaselineHistory(result.History, *request.Baseline)
	}
	if base == request.Previous.Commit {
		if tree != request.Previous.Tree {
			return RefreshResult{}, errors.New("landing integration: target commit resolved to a different tree than the approval base")
		}
		return result, nil
	}
	result.Moved = true
	if request.RunStore == nil || request.Snapshot == nil || request.Snapshot.Status != "running" || request.Snapshot.TargetBranch != request.Branch {
		return RefreshResult{}, errors.New("landing integration: moved Build requires its running journal and matching branch")
	}
	clock := request.Clock
	if clock == nil {
		clock = wallClock{}
	}
	if err := record(request.RunStore, request.Snapshot, clock, "base_moved_at_start", map[string]any{
		"previous_base": request.Previous.Commit,
		"base_commit":   base,
		"base_tree":     tree,
	}); err != nil {
		return RefreshResult{}, err
	}
	if len(request.Checks) != 0 && request.RunBaseChecks == nil {
		return RefreshResult{}, errors.New("landing integration: moved Build requires fresh base checks")
	}
	rows := []gate.BaselineCheck{}
	if request.RunBaseChecks != nil {
		rows, err = request.RunBaseChecks(ctx, Base{Commit: base, Tree: tree}, append([]contract.CheckSpec(nil), request.Checks...))
		if err != nil {
			return RefreshResult{}, fmt.Errorf("landing integration: run moved-base checks: %w", err)
		}
	}
	if err := validateBaselineRows(request.Checks, rows); err != nil {
		return RefreshResult{}, err
	}
	baseline := gate.CheckBaseline{Tree: string(tree), Checks: orderBaselineRows(request.Checks, rows)}
	result.Baseline = &baseline
	result.History = mergeBaselineHistory(result.History, baseline)
	if err := setSnapshotField(request.Snapshot, "build_base_commit", string(base)); err != nil {
		return RefreshResult{}, err
	}
	if err := setSnapshotField(request.Snapshot, "build_base_tree", string(tree)); err != nil {
		return RefreshResult{}, err
	}
	if err := setSnapshotField(request.Snapshot, "base_check_baseline", baseline); err != nil {
		return RefreshResult{}, err
	}
	if err := setSnapshotField(request.Snapshot, "base_check_history", result.History); err != nil {
		return RefreshResult{}, err
	}
	if err := record(request.RunStore, request.Snapshot, clock, "base_check_baseline", map[string]any{
		"base_commit": base,
		"base_tree":   tree,
		"checks":      baseline.Checks,
	}); err != nil {
		return RefreshResult{}, err
	}
	return result, nil
}

func orderBaselineRows(checks []contract.CheckSpec, rows []gate.BaselineCheck) []gate.BaselineCheck {
	byName := make(map[string]gate.BaselineCheck, len(rows))
	for _, row := range rows {
		byName[row.Name] = row
	}
	ordered := make([]gate.BaselineCheck, 0, len(checks))
	for _, check := range checks {
		ordered = append(ordered, byName[check.Name])
	}
	return cloneBaselineRows(ordered)
}

func validateBase(base Base) error {
	for name, value := range map[string]contract.ObjectID{"commit": base.Commit, "tree": base.Tree} {
		if err := gitio.ValidateObjectID(value); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func validateBaselineRows(checks []contract.CheckSpec, rows []gate.BaselineCheck) error {
	want := make(map[string]struct{}, len(checks))
	for _, check := range checks {
		if check.Name == "" {
			return errors.New("landing integration: check definition has an empty name")
		}
		if _, exists := want[check.Name]; exists {
			return fmt.Errorf("landing integration: duplicate check definition %q", check.Name)
		}
		want[check.Name] = struct{}{}
	}
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if _, exists := want[row.Name]; !exists {
			return fmt.Errorf("landing integration: baseline returned an unknown check %q", row.Name)
		}
		if _, exists := seen[row.Name]; exists {
			return fmt.Errorf("landing integration: baseline repeated check %q", row.Name)
		}
		seen[row.Name] = struct{}{}
		switch row.Status {
		case contract.CheckGreen, contract.CheckRed, contract.CheckUnavailable, contract.CheckTimeout, contract.CheckMutating:
		default:
			return fmt.Errorf("landing integration: baseline check %q has invalid status %q", row.Name, row.Status)
		}
	}
	if len(seen) != len(want) {
		missing := make([]string, 0, len(want)-len(seen))
		for name := range want {
			if _, ok := seen[name]; !ok {
				missing = append(missing, name)
			}
		}
		sort.Strings(missing)
		return fmt.Errorf("landing integration: moved-base baseline omitted checks: %v", missing)
	}
	return nil
}

func mergeBaselineHistory(history []gate.CheckBaseline, next gate.CheckBaseline) []gate.CheckBaseline {
	out := cloneBaselineHistory(history)
	for index := range out {
		if out[index].Tree == next.Tree {
			out[index] = *cloneBaseline(&next)
			return out
		}
	}
	out = append(out, *cloneBaseline(&next))
	return out
}

func cloneBaselineHistory(history []gate.CheckBaseline) []gate.CheckBaseline {
	out := make([]gate.CheckBaseline, len(history))
	for index := range history {
		out[index] = *cloneBaseline(&history[index])
	}
	return out
}

func cloneBaseline(input *gate.CheckBaseline) *gate.CheckBaseline {
	if input == nil {
		return nil
	}
	return &gate.CheckBaseline{Tree: input.Tree, Checks: cloneBaselineRows(input.Checks)}
}

func cloneBaselineRows(rows []gate.BaselineCheck) []gate.BaselineCheck {
	out := make([]gate.BaselineCheck, len(rows))
	for index, row := range rows {
		out[index] = row
		out[index].Findings = append([]contract.FindingIdentity(nil), row.Findings...)
		if row.ExitStatus != nil {
			status := *row.ExitStatus
			out[index].ExitStatus = &status
		}
	}
	return out
}

func setSnapshotField(snapshot *journal.RunSnapshot, name string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("landing integration: encode run field %q: %w", name, err)
	}
	if snapshot.Fields == nil {
		snapshot.Fields = make(map[string]json.RawMessage)
	}
	snapshot.Fields[name] = encoded
	return nil
}

func record(store *journal.RunStore, snapshot *journal.RunSnapshot, clock contract.Clock, name string, fields map[string]any) error {
	if store == nil || snapshot == nil {
		return fmt.Errorf("landing integration: cannot persist %s without the run journal", name)
	}
	event := journal.NewRunEvent(name, clock.Now().UnixMilli())
	for key, value := range fields {
		if err := event.Set(key, value); err != nil {
			return fmt.Errorf("landing integration: encode %s.%s: %w", name, key, err)
		}
	}
	if err := store.Record(event, *snapshot); err != nil {
		return fmt.Errorf("landing integration: persist %s: %w", name, err)
	}
	return nil
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

func (wallClock) Sleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
