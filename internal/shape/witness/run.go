package witness

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/gate"
	"kogen-go/internal/shape/validate"
)

const (
	ModeNone                      = "none"
	ModeWitness                   = "witness"
	AuditorRole contract.RoleName = "auditor"

	DefaultRedRounds = 2
	DefaultTimeLimit = 20 * time.Minute
	DefaultOutputCap = int64(60_000)
)

var (
	ErrInvalidRequest       = errors.New("witness: invalid request")
	ErrOutputBudgetExceeded = errors.New("witness: output token budget exceeded")
	ErrOutputUsageUnknown   = errors.New("witness: output token usage is unknown")
	ErrOutputReservation    = errors.New("witness: output token reservation is invalid")
)

// Difficulty selects the witness Build rungs. Hard runs both R1 and R2; the
// effect adapter owns the same workspace isolation and deterministic winner
// selection used by the Build ladder.
type Difficulty string

const (
	DifficultyEasy Difficulty = "easy"
	DifficultyHard Difficulty = "hard"
)

// Request is the exact validated Shape material needed to create a witness.
// ModeNone is a no-op and makes no effect calls. Empty RedRounds selects the
// contract default of two adjudication/repair rounds.
type Request struct {
	Mode            string
	Slug            string
	BaseSHA         string
	Difficulty      Difficulty
	RedRounds       int
	AuditorSettings contract.RoleSettings
	IntentBytes     []byte
	RequestBytes    []byte
	AcceptanceBytes []byte
}

// Result describes only observed witness evidence. A non-nil Proof is created
// only after a real gate report is landable and its exact binary diff has been
// bound to the published witness ref.
type Result struct {
	Feasibility string
	Proof       *Proof
	Warnings    []validate.Warning
	RedRounds   int
}

const (
	FeasibilityNotChecked     = "not checked"
	FeasibilityProven         = "PROVEN"
	FeasibilityProvenConcerns = "PROVEN WITH CONCERNS"
	FeasibilityUnproven       = "UNPROVEN"
)

// Session is an opaque handle to the private throwaway witness workspace(s).
// Implementations must not expose its path through IDs or provider prompts.
type Session any

// Effects are the production boundary for witness work. Begin must materialize
// a throwaway workspace from BaseSHA and prepare the real sandbox. RunRound
// must execute the real gate without Build-auditor demotion and return its
// immutable report. Adjudicate is exactly one tool-less test-auditor request.
// Repair applies only the scopes authorized by the judgments; it never removes
// an acceptance item. Publish binds the green candidate's real Git commit and
// exact binary diff under refs/kogen/witness/<slug>.
type Effects interface {
	Begin(context.Context, BeginRequest) (Session, error)
	RunRound(context.Context, RoundRequest) (RoundResult, error)
	Adjudicate(context.Context, AdjudicationRequest) ([]byte, error)
	Repair(context.Context, RepairRequest) error
	Publish(context.Context, PublishRequest) (Publication, error)
	Close(context.Context, Session) error
}

type BeginRequest struct {
	Slug        string
	BaseSHA     string
	IntentBytes []byte
	Request     []byte
	Acceptance  []byte
	Budget      *Budget
}

type RoundRequest struct {
	Round  int
	Rungs  []string
	Budget *Budget
}

// RoundResult carries the immutable production gate report and the exact
// inputs needed to explain failed assertions to the test auditor.
type RoundResult struct {
	Report        *gate.GateReport
	TestSource    []byte
	FailureOutput []byte
	CandidateDiff []byte
}

type AdjudicationRequest struct {
	Round         int
	FailedIDs     []string
	Request       []byte
	TestSource    []byte
	FailureOutput []byte
	CandidateDiff []byte
	Instructions  string
	ToolChoice    string
	Role          contract.RoleName
	Settings      contract.RoleSettings
	Budget        *Budget
}

type RepairScope string

const (
	RepairTest    RepairScope = "test"
	RepairWitness RepairScope = "witness"
)

// RepairItem gives the repair driver only the adjudication for a failed item.
// TEST-WRONG targets the candidate acceptance source; WITNESS-WRONG targets
// the implementation while keeping the acceptance source intact.
type RepairItem struct {
	ID       string
	Scope    RepairScope
	Verdict  Verdict
	Citation string
	Reason   string
}

type RepairRequest struct {
	Round  int
	Items  []RepairItem
	Budget *Budget
}

type PublishRequest struct {
	Slug    string
	BaseSHA string
	Verdict string
	Session Session
}

type Publication struct {
	Ref       string
	Commit    string
	BaseSHA   string
	DiffBytes []byte
}

// Run executes the opt-in witness flow. Failed gate assertions are adjudicated
// in input order. Invalid, missing, duplicate, or UNDECIDED judgments create a
// feasibility_concern warning and never alter the gate. Actionable judgments
// may request a scoped repair, but another actual green gate is still required
// before a proof can be published.
func Run(ctx context.Context, request Request, effects Effects) (result Result, err error) {
	if request.Mode == ModeNone || request.Mode == "" {
		return Result{Feasibility: FeasibilityNotChecked}, nil
	}
	if request.Mode != ModeWitness || ctx == nil || effects == nil ||
		!safeSlug(request.Slug) || !validObjectID(request.BaseSHA) ||
		(request.Difficulty != DifficultyEasy && request.Difficulty != DifficultyHard) {
		return Result{}, ErrInvalidRequest
	}
	if request.RedRounds < 0 {
		return Result{}, ErrInvalidRequest
	}
	if request.AuditorSettings.Provider == "" || request.AuditorSettings.Model == "" || request.AuditorSettings.Effort == "" {
		return Result{}, fmt.Errorf("%w: resolved auditor role settings are required", ErrInvalidRequest)
	}
	redRounds := request.RedRounds
	if redRounds == 0 {
		redRounds = DefaultRedRounds
	}
	if len(request.IntentBytes) == 0 || len(request.AcceptanceBytes) == 0 {
		return Result{}, fmt.Errorf("%w: Intent and acceptance bytes are required", ErrInvalidRequest)
	}

	result.Feasibility = FeasibilityUnproven
	budget := NewBudget(DefaultOutputCap)
	witnessCtx, cancel := context.WithTimeout(ctx, DefaultTimeLimit)
	defer cancel()
	session, beginErr := effects.Begin(witnessCtx, BeginRequest{
		Slug: request.Slug, BaseSHA: request.BaseSHA,
		IntentBytes: cloneBytes(request.IntentBytes), Request: cloneBytes(request.RequestBytes),
		Acceptance: cloneBytes(request.AcceptanceBytes), Budget: budget,
	})
	if beginErr != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		if errors.Is(beginErr, context.DeadlineExceeded) || errors.Is(witnessCtx.Err(), context.DeadlineExceeded) {
			return result, nil
		}
		return Result{}, beginErr
	}
	if session == nil {
		return Result{}, fmt.Errorf("%w: effects returned no throwaway session", ErrInvalidRequest)
	}
	defer func() {
		closeErr := effects.Close(context.WithoutCancel(ctx), session)
		if closeErr != nil {
			if err == nil {
				err = fmt.Errorf("close witness workspace: %w", closeErr)
			} else {
				err = errors.Join(err, fmt.Errorf("close witness workspace: %w", closeErr))
			}
			result.Proof = nil
			result.Feasibility = FeasibilityUnproven
		}
	}()

	rungs := []string{"R1"}
	if request.Difficulty == DifficultyHard {
		rungs = append(rungs, "R2")
	}
	warnings := make([]validate.Warning, 0)
	for round := 1; ; round++ {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		if err := witnessBudgetState(ctx, witnessCtx, budget); err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrOutputBudgetExceeded) || errors.Is(err, ErrOutputUsageUnknown) {
				result.Warnings = appendWarnings(warnings, validate.Warning{
					Code: "feasibility_concern", ItemIDs: []string{},
					Message: "The witness probe stopped before its resource budget could establish proof.",
				})
				return result, nil
			}
			return Result{}, err
		}
		attempt, runErr := effects.RunRound(witnessCtx, RoundRequest{
			Round: round, Rungs: append([]string(nil), rungs...), Budget: budget,
		})
		if runErr != nil {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(witnessCtx.Err(), context.DeadlineExceeded) ||
				errors.Is(runErr, ErrOutputBudgetExceeded) || errors.Is(runErr, ErrOutputUsageUnknown) {
				result.Warnings = appendWarnings(warnings, resourceWarning())
				return result, nil
			}
			return Result{}, runErr
		}
		if budgetErr := budget.Status(); budgetErr != nil {
			if errors.Is(budgetErr, ErrOutputBudgetExceeded) || errors.Is(budgetErr, ErrOutputUsageUnknown) {
				result.Warnings = appendWarnings(warnings, resourceWarning())
				return result, nil
			}
			return Result{}, budgetErr
		}
		if attempt.Report == nil {
			return Result{}, fmt.Errorf("%w: witness round has no production gate report", ErrInvalidRequest)
		}
		if attempt.Report.IsLandable() {
			if budgetErr := budget.Finish(); budgetErr != nil {
				if errors.Is(budgetErr, ErrOutputBudgetExceeded) || errors.Is(budgetErr, ErrOutputUsageUnknown) {
					result.Warnings = appendWarnings(warnings, resourceWarning())
					return result, nil
				}
				return Result{}, budgetErr
			}
			verdict := VerdictProven
			result.Feasibility = FeasibilityProven
			if len(warnings) != 0 {
				verdict = VerdictProvenWithConcerns
				result.Feasibility = FeasibilityProvenConcerns
			}
			published, publishErr := effects.Publish(witnessCtx, PublishRequest{
				Slug: request.Slug, BaseSHA: request.BaseSHA, Verdict: string(verdict), Session: session,
			})
			if publishErr != nil {
				if errors.Is(publishErr, context.DeadlineExceeded) || errors.Is(witnessCtx.Err(), context.DeadlineExceeded) {
					result.Feasibility = FeasibilityUnproven
					result.Warnings = appendWarnings(warnings, resourceWarning())
					return result, nil
				}
				return Result{}, publishErr
			}
			proof, proofErr := proofFromPublication(request.Slug, request.BaseSHA, verdict, published)
			if proofErr != nil {
				return Result{}, proofErr
			}
			result.Proof = &proof
			result.Warnings = cloneWarnings(warnings)
			return result, nil
		}

		failedIDs := failedAcceptanceIDs(attempt.Report)
		if len(failedIDs) == 0 {
			result.Warnings = cloneWarnings(warnings)
			return result, nil
		}
		result.RedRounds = round
		adjudicationRequest := AdjudicationRequest{
			Round: round, FailedIDs: append([]string(nil), failedIDs...),
			Request: cloneBytes(request.RequestBytes), TestSource: cloneBytes(attempt.TestSource),
			FailureOutput: cloneBytes(attempt.FailureOutput), CandidateDiff: cloneBytes(attempt.CandidateDiff),
			Instructions: AdjudicatorInstructions(), ToolChoice: "none", Role: AuditorRole,
			Settings: request.AuditorSettings, Budget: budget,
		}
		reply, auditErr := effects.Adjudicate(witnessCtx, adjudicationRequest)
		if auditErr != nil {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			if errors.Is(auditErr, context.DeadlineExceeded) || errors.Is(witnessCtx.Err(), context.DeadlineExceeded) ||
				errors.Is(auditErr, ErrOutputBudgetExceeded) || errors.Is(auditErr, ErrOutputUsageUnknown) {
				result.Warnings = appendWarnings(warnings, resourceWarning())
				return result, nil
			}
			return Result{}, auditErr
		}
		if budgetErr := budget.Status(); budgetErr != nil {
			if errors.Is(budgetErr, ErrOutputBudgetExceeded) || errors.Is(budgetErr, ErrOutputUsageUnknown) {
				result.Warnings = appendWarnings(warnings, resourceWarning())
				return result, nil
			}
			return Result{}, budgetErr
		}
		judgments := DecodeAdjudications(reply, failedIDs)
		repairItems, judgmentWarnings := repairPlan(judgments)
		warnings = appendWarnings(warnings, judgmentWarnings...)
		if len(repairItems) == 0 || round >= redRounds {
			result.Warnings = cloneWarnings(warnings)
			return result, nil
		}
		if err := effects.Repair(witnessCtx, RepairRequest{
			Round: round, Items: cloneRepairItems(repairItems), Budget: budget,
		}); err != nil {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(witnessCtx.Err(), context.DeadlineExceeded) ||
				errors.Is(err, ErrOutputBudgetExceeded) || errors.Is(err, ErrOutputUsageUnknown) {
				result.Warnings = appendWarnings(warnings, resourceWarning())
				return result, nil
			}
			return Result{}, err
		}
		if budgetErr := budget.Status(); budgetErr != nil {
			if errors.Is(budgetErr, ErrOutputBudgetExceeded) || errors.Is(budgetErr, ErrOutputUsageUnknown) {
				result.Warnings = appendWarnings(warnings, resourceWarning())
				return result, nil
			}
			return Result{}, budgetErr
		}
	}
}

func failedAcceptanceIDs(report *gate.GateReport) []string {
	if report == nil {
		return nil
	}
	acceptance := report.InitialAcceptance()
	if retry, ok := report.RetryAcceptance(); ok {
		acceptance = retry
	}
	failed := make([]string, 0)
	for id, passed := range acceptance.ItemPass {
		if !passed {
			failed = append(failed, id)
		}
	}
	sort.Strings(failed)
	return failed
}

func resourceWarning() validate.Warning {
	return validate.Warning{
		Code: "feasibility_concern", ItemIDs: []string{},
		Message: "The witness probe stopped before its resource budget could establish proof.",
	}
}

func witnessBudgetState(parent, witness context.Context, budget *Budget) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if err := witness.Err(); err != nil {
		return err
	}
	if budget == nil {
		return ErrInvalidRequest
	}
	return budget.Status()
}

func appendWarnings(existing []validate.Warning, warnings ...validate.Warning) []validate.Warning {
	for _, warning := range warnings {
		duplicate := false
		for _, prior := range existing {
			if prior.Code == warning.Code && prior.Message == warning.Message && equalStrings(prior.ItemIDs, warning.ItemIDs) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			warning.ItemIDs = append([]string(nil), warning.ItemIDs...)
			existing = append(existing, warning)
		}
	}
	return existing
}

func cloneWarnings(warnings []validate.Warning) []validate.Warning {
	if warnings == nil {
		return []validate.Warning{}
	}
	cloned := make([]validate.Warning, len(warnings))
	for index, warning := range warnings {
		cloned[index] = warning
		cloned[index].ItemIDs = append([]string(nil), warning.ItemIDs...)
	}
	return cloned
}

func cloneRepairItems(items []RepairItem) []RepairItem {
	return append([]RepairItem(nil), items...)
}

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
