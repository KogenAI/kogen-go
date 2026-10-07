package repair

import (
	"errors"
	"fmt"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/gate"
)

const (
	// MaxRepairs is the number of repair requests permitted in one rung.
	MaxRepairs = 6
	// ProtectedRestoreLimit ends builder work on the fourth protected restore.
	ProtectedRestoreLimit = 4
)

type Phase string

const (
	PhaseDevelop Phase = "develop"
	PhaseRepair  Phase = "repair"
	PhaseVerify  Phase = "verify"
	PhaseDone    Phase = "done"
)

type Reason string

const (
	ReasonGreen                 Reason = "green"
	ReasonUnverified            Reason = "unverified"
	ReasonNoProgress            Reason = "no_progress"
	ReasonUnchanged             Reason = "unchanged"
	ReasonRepairCap             Reason = "repair_cap"
	ReasonTurnCap               Reason = "turn_cap"
	ReasonWallCap               Reason = "wall_cap"
	ReasonBudget                Reason = "budget"
	ReasonProtectedRestoreLimit Reason = "protected_restore_limit"
)

type Completion string

const (
	CompletionFinished              Completion = "finished"
	CompletionTurnCap               Completion = "turn_cap"
	CompletionWallCap               Completion = "wall_cap"
	CompletionBudget                Completion = "budget"
	CompletionProtectedRestoreLimit Completion = "protected_restore_limit"
	CompletionProviderFailure       Completion = "provider_failure"
)

type EventKind string

const (
	EventDevelopmentFinished EventKind = "development_finished"
	EventRepairFinished      EventKind = "repair_finished"
	EventVerification        EventKind = "verification"
	EventProtectedRestored   EventKind = "protected_restored"
)

// Event is one observed rung transition. RedCount is meaningful only when
// HasRedCount is true; this distinguishes a zero count from a missing count.
type Event struct {
	Kind          EventKind
	Completion    Completion
	ProviderClass string
	Changed       bool
	Red           bool
	Landable      bool
	RedCount      int
	HasRedCount   bool
	Feedback      string
	RestoredPaths []string
}

type EffectKind string

const (
	EffectVerify                   EffectKind = "verify"
	EffectRunRepair                EffectKind = "run_repair"
	EffectAppendControllerMessages EffectKind = "append_controller_messages"
	EffectStopDevelopment          EffectKind = "stop_development"
	EffectFinish                   EffectKind = "finish"
)

// Effect describes the I/O the caller must perform after a pure transition.
// RunRepair.Message already contains the exact controller wrapper to append.
type Effect struct {
	Kind         EffectKind
	RepairNumber int
	Message      string
	Messages     []string
	Reason       Reason
	Landable     bool
}

// State is one rung's repair accounting. A new rung must start with NewState
// so counters never leak across rungs.
type State struct {
	Phase                     Phase
	RepairsUsed               int
	PreviousRedCount          int
	HasPreviousRedCount       bool
	UncountedRedVerifications int
	ProtectedRestores         int
	PendingReason             Reason
	Reason                    Reason
	Landable                  bool
}

func NewState() State { return State{Phase: PhaseDevelop} }

// Step applies one event without performing provider, gate, filesystem, or
// journal I/O. Effects are ordered; in particular protected restore messages
// precede stopping the developer and requesting final verification.
func Step(state State, event Event) (State, []Effect, error) {
	if state.Phase == "" {
		state = NewState()
	}
	if state.Phase == PhaseDone {
		return state, nil, errors.New("repair machine: rung is already finished")
	}
	switch event.Kind {
	case EventDevelopmentFinished:
		if state.Phase != PhaseDevelop {
			return state, nil, fmt.Errorf("repair machine: development_finished in %q phase", state.Phase)
		}
		if err := applyCompletion(&state, event.Completion, event.ProviderClass); err != nil {
			return state, nil, err
		}
		if state.Phase == PhaseDone {
			return state, []Effect{{Kind: EffectFinish, Reason: state.Reason}}, nil
		}
		state.Phase = PhaseVerify
		return state, []Effect{{Kind: EffectVerify}}, nil

	case EventRepairFinished:
		if state.Phase != PhaseRepair {
			return state, nil, fmt.Errorf("repair machine: repair_finished in %q phase", state.Phase)
		}
		if err := applyCompletion(&state, event.Completion, event.ProviderClass); err != nil {
			return state, nil, err
		}
		if state.Phase == PhaseDone {
			return state, []Effect{{Kind: EffectFinish, Reason: state.Reason}}, nil
		}
		if state.PendingReason == "" && !event.Changed {
			state.PendingReason = ReasonUnchanged
		}
		state.Phase = PhaseVerify
		return state, []Effect{{Kind: EffectVerify}}, nil

	case EventVerification:
		if state.Phase != PhaseVerify {
			return state, nil, fmt.Errorf("repair machine: verification in %q phase", state.Phase)
		}
		if event.RedCount < 0 {
			return state, nil, errors.New("repair machine: red count cannot be negative")
		}
		if !event.Red {
			reason := ReasonUnverified
			if event.Landable {
				reason = ReasonGreen
				if isCapReason(state.PendingReason) {
					reason = state.PendingReason
				}
			} else if state.PendingReason != "" && state.PendingReason != ReasonUnchanged {
				reason = state.PendingReason
			}
			return finish(state, reason, event.Landable), nil, nil
		}
		if state.PendingReason != "" {
			return finish(state, state.PendingReason, false), nil, nil
		}
		if event.HasRedCount {
			if state.HasPreviousRedCount && event.RedCount >= state.PreviousRedCount {
				return finish(state, ReasonNoProgress, false), nil, nil
			}
			state.PreviousRedCount = event.RedCount
			state.HasPreviousRedCount = true
		} else {
			if state.UncountedRedVerifications > 0 {
				return finish(state, ReasonNoProgress, false), nil, nil
			}
			state.UncountedRedVerifications++
		}
		if state.RepairsUsed >= MaxRepairs {
			return finish(state, ReasonRepairCap, false), nil, nil
		}
		state.Phase = PhaseRepair
		state.RepairsUsed++
		return state, []Effect{{
			Kind: EffectRunRepair, RepairNumber: state.RepairsUsed,
			Message: ControllerFeedback(event.Feedback),
		}}, nil

	case EventProtectedRestored:
		if state.Phase != PhaseDevelop && state.Phase != PhaseRepair {
			return state, nil, fmt.Errorf("repair machine: protected_restored in %q phase", state.Phase)
		}
		if len(event.RestoredPaths) == 0 {
			return state, nil, errors.New("repair machine: protected restore event has no paths")
		}
		messages := ProtectedRestoreMessages(event.RestoredPaths)
		state.ProtectedRestores += len(event.RestoredPaths)
		effects := []Effect{{Kind: EffectAppendControllerMessages, Messages: messages}}
		if state.ProtectedRestores >= ProtectedRestoreLimit {
			state.PendingReason = ReasonProtectedRestoreLimit
			state.Phase = PhaseVerify
			effects = append(effects, Effect{Kind: EffectStopDevelopment}, Effect{Kind: EffectVerify})
		}
		return state, effects, nil

	default:
		return state, nil, fmt.Errorf("repair machine: unknown event %q", event.Kind)
	}
}

func applyCompletion(state *State, completion Completion, providerClass string) error {
	switch completion {
	case CompletionFinished:
		return nil
	case CompletionTurnCap:
		state.PendingReason = ReasonTurnCap
	case CompletionWallCap:
		state.PendingReason = ReasonWallCap
	case CompletionBudget:
		state.PendingReason = ReasonBudget
	case CompletionProtectedRestoreLimit:
		state.PendingReason = ReasonProtectedRestoreLimit
	case CompletionProviderFailure:
		if providerClass == "" || strings.ContainsAny(providerClass, "\r\n/") {
			return errors.New("repair machine: provider failure requires one provider class")
		}
		state.Phase = PhaseDone
		state.Reason = Reason("provider/" + providerClass)
	default:
		return fmt.Errorf("repair machine: unknown completion %q", completion)
	}
	return nil
}

func finish(state State, reason Reason, landable bool) State {
	state.Phase = PhaseDone
	state.Reason = reason
	state.Landable = landable
	return state
}

func isCapReason(reason Reason) bool {
	switch reason {
	case ReasonTurnCap, ReasonWallCap, ReasonBudget, ReasonProtectedRestoreLimit:
		return true
	default:
		return false
	}
}

// RedCount computes the repair progress count from the immutable gate report.
// Auditor advice is intentionally absent: the v1.3 auditor is observational.
func RedCount(report *gate.GateReport) int {
	if report == nil {
		return 0
	}
	var checks []RedCheck
	for _, check := range report.Checks() {
		if check.Excused || check.Status == contract.CheckGreen {
			continue
		}
		row := RedCheck{Red: true}
		for _, finding := range check.Findings {
			row.Findings = append(row.Findings, contract.FindingIdentity{
				Path: finding.Path, Rule: finding.Rule, Symbol: finding.Symbol,
			})
		}
		checks = append(checks, row)
	}
	failedFixes := 0
	for _, fix := range report.Fixes() {
		if fix.TimedOut || fix.Unavailable || fix.ExitStatus == nil || *fix.ExitStatus != 0 {
			failedFixes++
		}
	}
	counts := report.Counts()
	return CountRed(RedInput{Checks: checks, FailedFixes: failedFixes, FailedItems: counts.Total - counts.Passed})
}

// RedCheck is the countable portion of one non-excused verification check.
type RedCheck struct {
	Red      bool
	Findings []contract.FindingIdentity
}

// RedInput keeps the progress rule independently testable from process I/O.
type RedInput struct {
	Checks      []RedCheck
	FailedFixes int
	FailedItems int
}

// CountRed counts one for each red check with no identities, otherwise each
// distinct (path, rule, symbol) identity across red checks, plus failed fixes
// and approved acceptance items.
func CountRed(input RedInput) int {
	identities := make(map[contract.FindingIdentity]struct{})
	anonymousChecks := 0
	for _, check := range input.Checks {
		if !check.Red {
			continue
		}
		if len(check.Findings) == 0 {
			anonymousChecks++
			continue
		}
		for _, finding := range check.Findings {
			identities[finding] = struct{}{}
		}
	}
	if input.FailedFixes < 0 {
		input.FailedFixes = 0
	}
	if input.FailedItems < 0 {
		input.FailedItems = 0
	}
	return len(identities) + anonymousChecks + input.FailedFixes + input.FailedItems
}
