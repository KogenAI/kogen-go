// Package session owns Shape's two persistent provider conversations and the
// independent allowances and accounting attached to them.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/provider/retry"
	providersession "kogen-go/internal/provider/session"
	"kogen-go/internal/safefs"
	"kogen-go/internal/shape/prompts"
)

const (
	ProfileV13                     = "shape-v1.3"
	MaxConversations               = 2
	MaxPassesPerConversation       = 3
	MaxShaperTurnsPerConversation  = 60
	MaxStyleRepairsPerConversation = 2
	MaxHTTPAttemptsPerRequest      = 4
	AccountingFileName             = "shape-accounting.json"
)

var (
	ErrAlreadyStarted        = errors.New("shape session: primary conversation already started")
	ErrNoConversation        = errors.New("shape session: no active conversation")
	ErrInvalidSession        = errors.New("shape session: factory returned an invalid conversation")
	ErrInvalidPassState      = errors.New("shape session: invalid validation pass transition")
	ErrInvalidTraversalState = errors.New("shape session: invalid validation traversal transition")
	ErrInvalidTurnState      = errors.New("shape session: invalid logical turn transition")
	ErrInvalidAttempt        = errors.New("shape session: invalid HTTP attempt transition")
	ErrAttemptLimit          = errors.New("shape session: HTTP attempt allowance exhausted")
	ErrFallbackNotEligible   = errors.New("shape session: fallback requires primary pass or turn exhaustion")
	ErrFallbackAlreadyUsed   = errors.New("shape session: fallback conversation already started")
	ErrFailureFeedbackNeeded = errors.New("shape session: retain the last validation failure before fallback")
	ErrInvalidOutcome        = errors.New("shape session: outcome must be success or failure")
)

// BudgetCounter identifies the per-conversation allowance that stopped a
// dispatch. A primary exhaustion automatically creates the fresh fallback
// conversation before returning this error; fallback exhaustion is terminal.
type BudgetCounter string

const (
	PassBudget BudgetCounter = "passes"
	TurnBudget BudgetCounter = "logical_turns"
)

// BudgetExhaustedError is returned before a logical request or validation pass
// can exceed its allowance. For conversation 1, the Run starts conversation 2
// before returning the error. For conversation 2, it signals terminal failure.
type BudgetExhaustedError struct {
	Conversation int
	Counter      BudgetCounter
}

func (e *BudgetExhaustedError) Error() string {
	if e == nil {
		return "shape session: budget exhausted"
	}
	return fmt.Sprintf("shape session: conversation %d exhausted %s allowance", e.Conversation, e.Counter)
}

// IsBudgetExhausted reports whether err marks a Shape pass or turn cap.
func IsBudgetExhausted(err error) bool {
	var exhausted *BudgetExhaustedError
	return errors.As(err, &exhausted)
}

// AttemptKind distinguishes a new logical request from provider-level retries
// and stream continuations. All three are HTTP attempts; only First spends a
// shaper logical turn.
type AttemptKind string

const (
	AttemptFirst        AttemptKind = "first"
	AttemptRetry        AttemptKind = "retry"
	AttemptContinuation AttemptKind = "continuation"
)

// RepairKind classifies controller feedback. Style is free with respect to
// validation passes; the other kinds are pass-consuming repairs.
type RepairKind string

const (
	RepairCandidate        RepairKind = "candidate"
	RepairCoverage         RepairKind = "coverage"
	RepairTestAudit        RepairKind = "test_audit"
	RepairCoverageAndAudit RepairKind = "coverage_and_test_audit"
	RepairStyle            RepairKind = "style"
)

// Outcome is the terminal result recorded in shape-accounting.json.
type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeFailure Outcome = "failure"
)

// ConversationSpec tells the factory which effective role tuple to bind to a
// fresh provider conversation. Factory results must have empty history; Run
// appends the exact initial user message after validating the identity.
type ConversationSpec struct {
	Index         int
	AssignedRole  contract.RoleName
	EffectiveRole contract.RoleName
	Settings      contract.RoleSettings
}

// ConversationFactory creates an empty provider conversation with a new
// thread identity. The primary and fallback must share run/cache/session IDs
// while using distinct thread IDs.
type ConversationFactory func(ConversationSpec) (*providersession.Conversation, error)

// Options contains the already-resolved role manifest and the command-entry
// time. StartedAt must be captured before Shape setup so elapsed time includes
// setup, waits, retries, validation and failed attempts.
type Options struct {
	Manifest  contract.RoleManifest
	Factory   ConversationFactory
	StartedAt time.Time
	Now       func() time.Time
}

// Run retains the primary and fallback provider-session pointers, including
// their complete raw history and sticky routing state. It is a single-command,
// sequential controller; callers must not issue concurrent model requests.
type Run struct {
	startedAt time.Time
	now       func() time.Time
	factory   ConversationFactory
	shaper    contract.RoleSettings

	started         bool
	initialMessage  string
	lastFailure     string
	fallbackStarted bool
	exhaustion      *BudgetExhaustedError
	conversations   []*conversationState
	active          *conversationState

	roles        map[contract.RoleName]*roleState
	globalTokens tokenSums
	unknownUsage uint64
	passes       uint64
	traversals   uint64
	finishGuards uint64
	repairs      map[RepairKind]uint64
	auditorTurn  requestState
}

type conversationState struct {
	spec    ConversationSpec
	session *providersession.Conversation

	logicalTurns         uint64
	httpAttempts         uint64
	continuations        uint64
	validationPasses     uint64
	validationTraversals uint64
	finishGuards         uint64
	styleWarnings        uint64
	passNumbers          []int
	repairs              map[RepairKind]uint64
	tokens               tokenSums
	unknownUsage         uint64

	passOpen        bool
	passCounted     bool
	passSlot        int
	traversalOpen   bool
	repairRequested bool

	turnOpen         bool
	attemptOpen      bool
	attemptsThisTurn int
}

type requestState struct {
	turnOpen    bool
	attemptOpen bool
	attempts    int
}

type roleState struct {
	assignedRole  contract.RoleName
	effectiveRole contract.RoleName
	settings      contract.RoleSettings

	logicalTurns     uint64
	httpAttempts     uint64
	continuations    uint64
	validationPasses uint64
	tokens           tokenSums
	unknownUsage     uint64
}

// New creates the Shape run controller. The effective fallback tuple is
// resolved from the manifest and must exactly alias the effective shaper;
// callers cannot provide an independent fallback role.
func New(options Options) (*Run, error) {
	if options.Factory == nil {
		return nil, errors.New("shape session: conversation factory is required")
	}
	fallback, err := retry.ResolveShapeFallback(options.Manifest)
	if err != nil {
		return nil, err
	}
	shaper := options.Manifest.Effective[contract.RoleName("shaper")]
	auditor, ok := options.Manifest.Effective[contract.RoleName("auditor")]
	if !ok || !completeRole(auditor) {
		return nil, errors.New("shape session: effective auditor role is unavailable")
	}
	if !completeRole(shaper) || fallback != shaper {
		return nil, errors.New("shape session: effective fallback does not alias shaper")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	startedAt := options.StartedAt
	if startedAt.IsZero() {
		startedAt = now()
	}
	run := &Run{
		startedAt: startedAt,
		now:       now,
		factory:   options.Factory,
		shaper:    shaper,
		roles:     make(map[contract.RoleName]*roleState, 3),
		repairs:   emptyRepairCounts(),
	}
	run.roles["shaper"] = &roleState{assignedRole: "shaper", effectiveRole: "shaper", settings: shaper}
	run.roles["fallback_shaper"] = &roleState{assignedRole: "fallback_shaper", effectiveRole: "shaper", settings: fallback}
	run.roles["auditor"] = &roleState{assignedRole: "auditor", effectiveRole: "auditor", settings: auditor}
	return run, nil
}

// StartPrimary starts the first fresh conversation and appends initialMessage
// verbatim as its first user input. The same underlying session pointer remains
// active across all primary passes, guards, tools and repairs.
func (r *Run) StartPrimary(initialMessage string) error {
	if r == nil {
		return ErrNoConversation
	}
	if r.started {
		return ErrAlreadyStarted
	}
	r.started = true
	r.initialMessage = initialMessage
	spec := ConversationSpec{Index: 1, AssignedRole: "shaper", EffectiveRole: "shaper", Settings: r.shaper}
	return r.createConversation(spec, initialMessage, nil)
}

func (r *Run) createConversation(spec ConversationSpec, initialMessage string, primary *contract.ConversationIdentity) error {
	if len(r.conversations) >= MaxConversations {
		return ErrFallbackAlreadyUsed
	}
	state := &conversationState{spec: spec, repairs: emptyRepairCounts()}
	r.conversations = append(r.conversations, state)
	r.active = state
	session, err := r.factory(spec)
	if err != nil {
		return fmt.Errorf("shape session: create conversation %d: %w", spec.Index, err)
	}
	if session == nil {
		return ErrInvalidSession
	}
	identity := session.Identity()
	if !identityMatches(identity, spec) {
		return ErrInvalidSession
	}
	snapshot := session.Snapshot()
	if len(snapshot.Protocol.History) != 0 || len(snapshot.Attempts) != 0 || snapshot.Protocol.Routing.HasCodexTurnState {
		return fmt.Errorf("%w: fresh conversation must have empty history and routing state", ErrInvalidSession)
	}
	if primary != nil {
		if identity.RunID != primary.RunID || identity.CacheKey != primary.CacheKey || identity.SessionID != primary.SessionID || identity.ThreadID == primary.ThreadID {
			return fmt.Errorf("%w: fallback must retain run affinity and use a distinct thread", ErrInvalidSession)
		}
	}
	if err := session.AppendUser(initialMessage); err != nil {
		return fmt.Errorf("shape session: append first user message: %w", err)
	}
	state.session = session
	return nil
}

func identityMatches(identity contract.ConversationIdentity, spec ConversationSpec) bool {
	return identity.RunID != "" && identity.CacheKey != "" && identity.ThreadID != "" && identity.SessionID != "" &&
		identity.Role == spec.AssignedRole && identity.Provider == spec.Settings.Provider &&
		identity.Model == spec.Settings.Model && identity.Effort == spec.Settings.Effort
}

// Session returns the active provider session pointer. Pass this same pointer
// to transport and tool code for every request in the conversation; do not
// reconstruct it from returned response items.
func (r *Run) Session() *providersession.Conversation {
	if r == nil || r.active == nil {
		return nil
	}
	return r.active.session
}

// ConversationSession returns the provider pointer for a 1-based conversation
// index, including the primary after fallback has started.
func (r *Run) ConversationSession(index int) *providersession.Conversation {
	if r == nil || index < 1 || index > len(r.conversations) {
		return nil
	}
	return r.conversations[index-1].session
}

// FallbackStarted reports whether primary exhaustion has created the fresh
// fallback conversation, even when its factory failed.
func (r *Run) FallbackStarted() bool { return r != nil && r.fallbackStarted }

// SetLastFailure stores the exact rendered failure feedback for the fresh
// fallback prompt. It stays available after the fallback begins and is never
// copied into the accounting receipt.
func (r *Run) SetLastFailure(feedback string) error {
	if r == nil || feedback == "" {
		return errors.New("shape session: last failure feedback is required")
	}
	r.lastFailure = feedback
	return nil
}

// LastFailure returns the retained feedback for controller prompt construction.
func (r *Run) LastFailure() string {
	if r == nil {
		return ""
	}
	return r.lastFailure
}

// BeginPass reserves the next validation-pass slot in the active conversation.
// It does not count the pass until CountValidationPass is called after the
// traversal reaches a non-style validation result. A style-only traversal can
// therefore use its separate free repair allowance without spending a pass.
// Primary pass exhaustion starts the fresh fallback automatically.
func (r *Run) BeginPass() (int, error) {
	c, err := r.requireConversation()
	if err != nil {
		return 0, err
	}
	if r.exhaustion != nil {
		return 0, r.exhaustion
	}
	if c.passOpen || c.turnOpen || c.attemptOpen {
		return 0, ErrInvalidPassState
	}
	if c.validationPasses >= MaxPassesPerConversation {
		return 0, r.exhaust(PassBudget, c)
	}
	c.passOpen = true
	c.passCounted = false
	c.repairRequested = false
	c.passSlot = (c.spec.Index-1)*MaxPassesPerConversation + int(c.validationPasses) + 1
	return c.passSlot, nil
}

// BeginValidationTraversal records one actual validation traversal within the
// reserved pass. Finish guards do not call this method.
func (r *Run) BeginValidationTraversal() error {
	c, err := r.requireConversation()
	if err != nil {
		return err
	}
	if !c.passOpen || c.traversalOpen || c.turnOpen || c.attemptOpen {
		return ErrInvalidTraversalState
	}
	if err := increment(&c.validationTraversals); err != nil {
		return err
	}
	if err := increment(&r.traversals); err != nil {
		return err
	}
	c.traversalOpen = true
	return nil
}

// RecordStyleTraversal classifies the current validation traversal as style-
// only. It returns true when a free style repair is available. Once the two
// repairs are used, false means leave the finding as a warning and continue the
// same traversal toward a counted validation pass.
func (r *Run) RecordStyleTraversal() (bool, error) {
	c, err := r.requireConversation()
	if err != nil {
		return false, err
	}
	if !c.passOpen || !c.traversalOpen || c.passCounted {
		return false, ErrInvalidTraversalState
	}
	if c.repairs[RepairStyle] >= MaxStyleRepairsPerConversation {
		if err := increment(&c.styleWarnings); err != nil {
			return false, err
		}
		return false, nil
	}
	if err := r.addRepair(c, RepairStyle); err != nil {
		return false, err
	}
	c.traversalOpen = false
	return true, nil
}

// CountValidationPass counts the reserved pass after a traversal produces a
// normal success or candidate failure. Calling it on the last pass is allowed;
// success on that pass remains success.
func (r *Run) CountValidationPass() error {
	c, err := r.requireConversation()
	if err != nil {
		return err
	}
	if !c.passOpen || !c.traversalOpen || c.passCounted {
		return ErrInvalidPassState
	}
	if c.validationPasses >= MaxPassesPerConversation {
		return r.exhaust(PassBudget, c)
	}
	role := r.roles[c.spec.AssignedRole]
	if err := increment(&c.validationPasses); err != nil {
		return err
	}
	if err := increment(&role.validationPasses); err != nil {
		return err
	}
	if err := increment(&r.passes); err != nil {
		return err
	}
	c.passNumbers = append(c.passNumbers, c.passSlot)
	c.passCounted = true
	return nil
}

// RecordRepair counts one pass-consuming repair request in the active counted
// traversal. Simultaneous coverage and test-audit feedback must use the single
// RepairCoverageAndAudit kind so it consumes one repair and one next pass.
func (r *Run) RecordRepair(kind RepairKind) error {
	c, err := r.requireConversation()
	if err != nil {
		return err
	}
	if kind == RepairStyle || !validPassRepair(kind) || !c.passOpen || !c.passCounted || !c.traversalOpen {
		return ErrInvalidTraversalState
	}
	if r.lastFailure == "" {
		return ErrFailureFeedbackNeeded
	}
	if c.repairRequested {
		return errors.New("shape session: combine repair feedback within one traversal")
	}
	if err := r.addRepair(c, kind); err != nil {
		return err
	}
	c.repairRequested = true
	return nil
}

// CompletePass closes a counted validation traversal. A following repair pass
// requires a new BeginPass call and consumes the next allowance slot.
func (r *Run) CompletePass() error {
	c, err := r.requireConversation()
	if err != nil {
		return err
	}
	if !c.passOpen || !c.passCounted || !c.traversalOpen || c.turnOpen || c.attemptOpen {
		return ErrInvalidPassState
	}
	c.passOpen = false
	c.passCounted = false
	c.passSlot = 0
	c.traversalOpen = false
	c.repairRequested = false
	return nil
}

// RecordFinishGuard counts the missing-file guard for the current pass. It
// neither starts validation nor spends a pass or repair.
func (r *Run) RecordFinishGuard() error {
	c, err := r.requireConversation()
	if err != nil {
		return err
	}
	if !c.passOpen || c.passCounted || c.traversalOpen || c.turnOpen || c.attemptOpen {
		return ErrInvalidPassState
	}
	if err := increment(&c.finishGuards); err != nil {
		return err
	}
	return increment(&r.finishGuards)
}

// DispatchShaperAttempt accounts a provider request at its dispatch boundary.
// AttemptFirst charges one logical turn; retries and continuations remain
// attached to that turn. The provider retry package enforces retry policy, and
// this component also prevents a fifth HTTP request for one logical request.
func (r *Run) DispatchShaperAttempt(kind AttemptKind) error {
	c, err := r.requireConversation()
	if err != nil {
		return err
	}
	if r.exhaustion != nil {
		return r.exhaustion
	}
	switch kind {
	case AttemptFirst:
		if !c.passOpen || c.turnOpen || c.attemptOpen {
			return ErrInvalidTurnState
		}
		if c.logicalTurns >= MaxShaperTurnsPerConversation {
			return r.exhaust(TurnBudget, c)
		}
		if err := increment(&c.logicalTurns); err != nil {
			return err
		}
		if err := increment(&r.roles[c.spec.AssignedRole].logicalTurns); err != nil {
			return err
		}
		c.turnOpen = true
		c.attemptsThisTurn = 0
	case AttemptRetry, AttemptContinuation:
		if !c.turnOpen || c.attemptOpen {
			return ErrInvalidTurnState
		}
	default:
		return ErrInvalidAttempt
	}
	if c.attemptsThisTurn >= MaxHTTPAttemptsPerRequest {
		return ErrAttemptLimit
	}
	if err := r.addAttemptCounts(c, kind); err != nil {
		return err
	}
	c.attemptsThisTurn++
	c.attemptOpen = true
	return nil
}

// CompleteShaperAttempt records nullable usage for the just-completed HTTP
// request. A nil usage value increments unknown_usage_attempts; it is not zero.
func (r *Run) CompleteShaperAttempt(usage *contract.TokenUsage) error {
	c, err := r.requireConversation()
	if err != nil {
		return err
	}
	if !c.attemptOpen {
		return ErrInvalidAttempt
	}
	if err := r.addUsage(c, r.roles[c.spec.AssignedRole], usage); err != nil {
		return err
	}
	c.attemptOpen = false
	return nil
}

// EndShaperTurn closes one logical request after its response or terminal
// provider error. It does not infer fallback eligibility from that error.
func (r *Run) EndShaperTurn() error {
	c, err := r.requireConversation()
	if err != nil {
		return err
	}
	if !c.turnOpen || c.attemptOpen {
		return ErrInvalidTurnState
	}
	c.turnOpen = false
	c.attemptsThisTurn = 0
	return nil
}

// DispatchAuditorAttempt accounts one separate auditor request. Auditor turns
// and retries never consume the 60-turn shaper allowance.
func (r *Run) DispatchAuditorAttempt(kind AttemptKind) error {
	if r == nil || r.active == nil || r.active.session == nil {
		return ErrNoConversation
	}
	c := &r.auditorTurn
	switch kind {
	case AttemptFirst:
		if c.turnOpen || c.attemptOpen || !r.active.passOpen || !r.active.passCounted || !r.active.traversalOpen {
			return ErrInvalidTurnState
		}
		if err := increment(&r.roles["auditor"].logicalTurns); err != nil {
			return err
		}
		c.turnOpen = true
		c.attempts = 0
	case AttemptRetry, AttemptContinuation:
		if !c.turnOpen || c.attemptOpen {
			return ErrInvalidTurnState
		}
	default:
		return ErrInvalidAttempt
	}
	if c.attempts >= MaxHTTPAttemptsPerRequest {
		return ErrAttemptLimit
	}
	role := r.roles["auditor"]
	if err := addCount(&role.httpAttempts, 1); err != nil {
		return err
	}
	if kind == AttemptContinuation {
		if err := increment(&role.continuations); err != nil {
			return err
		}
	}
	c.attempts++
	c.attemptOpen = true
	return nil
}

// CompleteAuditorAttempt records nullable usage for an auditor HTTP request.
func (r *Run) CompleteAuditorAttempt(usage *contract.TokenUsage) error {
	if r == nil || !r.auditorTurn.attemptOpen {
		return ErrInvalidAttempt
	}
	if err := r.addUsage(nil, r.roles["auditor"], usage); err != nil {
		return err
	}
	r.auditorTurn.attemptOpen = false
	return nil
}

// EndAuditorTurn closes one logical auditor request.
func (r *Run) EndAuditorTurn() error {
	if r == nil || !r.auditorTurn.turnOpen || r.auditorTurn.attemptOpen {
		return ErrInvalidTurnState
	}
	r.auditorTurn.turnOpen = false
	r.auditorTurn.attempts = 0
	return nil
}

func (r *Run) addAttemptCounts(c *conversationState, kind AttemptKind) error {
	role := r.roles[c.spec.AssignedRole]
	if err := increment(&c.httpAttempts); err != nil {
		return err
	}
	if err := increment(&role.httpAttempts); err != nil {
		return err
	}
	if kind == AttemptContinuation {
		if err := increment(&c.continuations); err != nil {
			return err
		}
		return increment(&role.continuations)
	}
	return nil
}

func (r *Run) addUsage(c *conversationState, role *roleState, usage *contract.TokenUsage) error {
	if usage == nil {
		if err := increment(&r.unknownUsage); err != nil {
			return err
		}
		if err := increment(&role.unknownUsage); err != nil {
			return err
		}
		if c != nil {
			return increment(&c.unknownUsage)
		}
		return nil
	}
	global := r.globalTokens
	roleTotals := role.tokens
	if err := global.add(usage); err != nil {
		return err
	}
	if err := roleTotals.add(usage); err != nil {
		return err
	}
	var conversationTotals tokenSums
	if c != nil {
		conversationTotals = c.tokens
		if err := conversationTotals.add(usage); err != nil {
			return err
		}
	}
	r.globalTokens = global
	role.tokens = roleTotals
	if c != nil {
		c.tokens = conversationTotals
	}
	return nil
}

func (r *Run) addRepair(c *conversationState, kind RepairKind) error {
	if !validRepair(kind) {
		return errors.New("shape session: unknown repair kind")
	}
	conversationCount := c.repairs[kind]
	if err := increment(&conversationCount); err != nil {
		return err
	}
	runCount := r.repairs[kind]
	if err := increment(&runCount); err != nil {
		return err
	}
	c.repairs[kind] = conversationCount
	r.repairs[kind] = runCount
	return nil
}

func (r *Run) requireConversation() (*conversationState, error) {
	if r == nil || r.active == nil || r.active.session == nil {
		return nil, ErrNoConversation
	}
	return r.active, nil
}

func (r *Run) exhaust(counter BudgetCounter, c *conversationState) error {
	err := &BudgetExhaustedError{Conversation: c.spec.Index, Counter: counter}
	r.exhaustion = err
	if c.spec.Index == 1 && !r.fallbackStarted {
		if startErr := r.startFallback(); startErr != nil {
			return errors.Join(err, fmt.Errorf("shape session: start fallback: %w", startErr))
		}
	}
	return err
}

func (r *Run) startFallback() error {
	if r == nil {
		return ErrFallbackNotEligible
	}
	if r.fallbackStarted {
		return ErrFallbackAlreadyUsed
	}
	if r.active == nil || r.active.spec.Index != 1 || r.exhaustion == nil || r.active.session == nil {
		return ErrFallbackNotEligible
	}
	if r.active.turnOpen || r.active.attemptOpen {
		return ErrInvalidTurnState
	}
	feedback := r.lastFailure
	if feedback == "" {
		if r.active.validationPasses > 0 {
			return ErrFailureFeedbackNeeded
		}
		feedback = prompts.ValidationFeedback("shape_turn_limit", "Shaper exhausted its turn limit.")
	}
	primary := r.active.session.Identity()
	r.fallbackStarted = true
	// The abandoned primary traversal remains in its receipt, but cannot be
	// reopened after the fresh fallback starts.
	r.active.passOpen = false
	r.active.passCounted = false
	r.active.traversalOpen = false
	r.active.repairRequested = false
	initial := prompts.FallbackMessage(r.initialMessage, feedback)
	spec := ConversationSpec{Index: 2, AssignedRole: "fallback_shaper", EffectiveRole: "shaper", Settings: r.shaper}
	r.exhaustion = nil
	return r.createConversation(spec, initial, &primary)
}

// TokenTotals contains sums of the token fields providers actually reported.
// A field remains null until at least one non-null value is observed.
type TokenTotals struct {
	Input       *int64 `json:"input"`
	CachedInput *int64 `json:"cached_input"`
	CacheWrite  *int64 `json:"cache_write"`
	Output      *int64 `json:"output"`
	Reasoning   *int64 `json:"reasoning"`
}

// ConversationReceipt records one actual thread and the resources consumed in
// that conversation. Thread IDs are protocol IDs; prompt/history bytes are not
// included.
type ConversationReceipt struct {
	ConversationID       string                `json:"conversation_id,omitempty"`
	SessionID            string                `json:"session_id,omitempty"`
	AssignedRole         contract.RoleName     `json:"assigned_role"`
	EffectiveRole        contract.RoleName     `json:"effective_role"`
	Provider             string                `json:"provider"`
	Model                string                `json:"model"`
	Effort               string                `json:"effort"`
	LogicalTurns         uint64                `json:"logical_turns"`
	HTTPAttempts         uint64                `json:"http_attempts"`
	Continuations        uint64                `json:"continuations"`
	ValidationPasses     uint64                `json:"validation_passes"`
	ValidationTraversals uint64                `json:"validation_traversals"`
	PassNumbers          []int                 `json:"pass_numbers"`
	FinishGuards         uint64                `json:"finish_guards"`
	StyleWarnings        uint64                `json:"style_warnings"`
	Repairs              map[RepairKind]uint64 `json:"repairs"`
	Tokens               TokenTotals           `json:"tokens"`
	UnknownUsageAttempts uint64                `json:"unknown_usage_attempts"`
}

// RoleReceipt aggregates counters by assigned role. It keeps the configured
// role tuple visible when fallback_shaper was never needed, and keeps auditor
// requests separate from the shaper's 60-turn limit.
type RoleReceipt struct {
	AssignedRole         contract.RoleName `json:"assigned_role"`
	EffectiveRole        contract.RoleName `json:"effective_role"`
	Provider             string            `json:"provider"`
	Model                string            `json:"model"`
	Effort               string            `json:"effort"`
	LogicalTurns         uint64            `json:"logical_turns"`
	HTTPAttempts         uint64            `json:"http_attempts"`
	Continuations        uint64            `json:"continuations"`
	ValidationPasses     uint64            `json:"validation_passes"`
	Tokens               TokenTotals       `json:"tokens"`
	UnknownUsageAttempts uint64            `json:"unknown_usage_attempts"`
}

// Receipt is the durable schema-1 accounting record published on both
// successful and failed Shape exits.
type Receipt struct {
	Schema               int                   `json:"schema"`
	Profile              string                `json:"profile"`
	Outcome              Outcome               `json:"outcome"`
	FallbackStarted      bool                  `json:"fallback_started"`
	Conversations        []ConversationReceipt `json:"conversations"`
	Roles                []RoleReceipt         `json:"roles"`
	ValidationPasses     uint64                `json:"validation_passes"`
	ValidationTraversals uint64                `json:"validation_traversals"`
	FinishGuards         uint64                `json:"finish_guards"`
	Repairs              map[RepairKind]uint64 `json:"repairs"`
	Tokens               TokenTotals           `json:"tokens"`
	UnknownUsageAttempts uint64                `json:"unknown_usage_attempts"`
	ElapsedMS            uint64                `json:"elapsed_ms"`
}

// Accounting returns a detached receipt snapshot. It contains role identities
// and usage totals but never prompt text, failure details, response content,
// credential data, header values or opaque turn-state bytes.
func (r *Run) Accounting(outcome Outcome) (Receipt, error) {
	if r == nil || (outcome != OutcomeSuccess && outcome != OutcomeFailure) {
		return Receipt{}, ErrInvalidOutcome
	}
	conversations := make([]ConversationReceipt, 0, len(r.conversations))
	for _, c := range r.conversations {
		identity := contract.ConversationIdentity{}
		if c.session != nil {
			identity = c.session.Identity()
		}
		conversations = append(conversations, ConversationReceipt{
			ConversationID:       identity.ThreadID,
			SessionID:            identity.SessionID,
			AssignedRole:         c.spec.AssignedRole,
			EffectiveRole:        c.spec.EffectiveRole,
			Provider:             c.spec.Settings.Provider,
			Model:                c.spec.Settings.Model,
			Effort:               c.spec.Settings.Effort,
			LogicalTurns:         c.logicalTurns,
			HTTPAttempts:         c.httpAttempts,
			Continuations:        c.continuations,
			ValidationPasses:     c.validationPasses,
			ValidationTraversals: c.validationTraversals,
			PassNumbers:          append([]int(nil), c.passNumbers...),
			FinishGuards:         c.finishGuards,
			StyleWarnings:        c.styleWarnings,
			Repairs:              cloneRepairCounts(c.repairs),
			Tokens:               c.tokens.receipt(),
			UnknownUsageAttempts: c.unknownUsage,
		})
	}
	roles := make([]RoleReceipt, 0, 3)
	for _, key := range []contract.RoleName{"shaper", "fallback_shaper", "auditor"} {
		role := r.roles[key]
		roles = append(roles, RoleReceipt{
			AssignedRole:         role.assignedRole,
			EffectiveRole:        role.effectiveRole,
			Provider:             role.settings.Provider,
			Model:                role.settings.Model,
			Effort:               role.settings.Effort,
			LogicalTurns:         role.logicalTurns,
			HTTPAttempts:         role.httpAttempts,
			Continuations:        role.continuations,
			ValidationPasses:     role.validationPasses,
			Tokens:               role.tokens.receipt(),
			UnknownUsageAttempts: role.unknownUsage,
		})
	}
	elapsed := r.now().Sub(r.startedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	return Receipt{
		Schema:               1,
		Profile:              ProfileV13,
		Outcome:              outcome,
		FallbackStarted:      r.fallbackStarted,
		Conversations:        conversations,
		Roles:                roles,
		ValidationPasses:     r.passes,
		ValidationTraversals: r.traversals,
		FinishGuards:         r.finishGuards,
		Repairs:              cloneRepairCounts(r.repairs),
		Tokens:               r.globalTokens.receipt(),
		UnknownUsageAttempts: r.unknownUsage,
		ElapsedMS:            uint64(elapsed.Milliseconds()),
	}, nil
}

// PublishAccounting atomically publishes schema-1 accounting at the fixed
// scratch-relative name. Rooted private publication replaces a symlink leaf
// itself, fsyncs the file and parent, and does not follow an unsafe path.
func (r *Run) PublishAccounting(root *safefs.Root, outcome Outcome) error {
	if root == nil {
		return safefs.ErrUnsafePath
	}
	receipt, err := r.Accounting(outcome)
	if err != nil {
		return err
	}
	contents, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("shape session: encode accounting receipt: %w", err)
	}
	contents = append(contents, '\n')
	return root.PublishPrivate(AccountingFileName, contents, safefs.PublicationReplace)
}

func (s *tokenSums) add(usage *contract.TokenUsage) error {
	if usage == nil {
		return nil
	}
	for _, pair := range []struct {
		target *optionalSum
		value  *int64
	}{
		{&s.input, usage.Input},
		{&s.cachedInput, usage.CachedInput},
		{&s.cacheWrite, usage.CacheWrite},
		{&s.output, usage.Output},
		{&s.reasoning, usage.Reasoning},
	} {
		if pair.value == nil {
			continue
		}
		if pair.target.seen && ((*pair.value > 0 && pair.target.value > math.MaxInt64-*pair.value) || (*pair.value < 0 && pair.target.value < math.MinInt64-*pair.value)) {
			return errors.New("shape session: token total overflow")
		}
		pair.target.value += *pair.value
		pair.target.seen = true
	}
	return nil
}

func (s tokenSums) receipt() TokenTotals {
	return TokenTotals{
		Input:       s.input.pointer(),
		CachedInput: s.cachedInput.pointer(),
		CacheWrite:  s.cacheWrite.pointer(),
		Output:      s.output.pointer(),
		Reasoning:   s.reasoning.pointer(),
	}
}

type tokenSums struct {
	input       optionalSum
	cachedInput optionalSum
	cacheWrite  optionalSum
	output      optionalSum
	reasoning   optionalSum
}

type optionalSum struct {
	value int64
	seen  bool
}

func (s optionalSum) pointer() *int64 {
	if !s.seen {
		return nil
	}
	value := s.value
	return &value
}

func increment(value *uint64) error { return addCount(value, 1) }

func addCount(value *uint64, amount uint64) error {
	if math.MaxUint64-*value < amount {
		return errors.New("shape session: accounting counter overflow")
	}
	*value += amount
	return nil
}

func completeRole(settings contract.RoleSettings) bool {
	return settings.Provider != "" && settings.Model != "" && settings.Effort != ""
}

func validPassRepair(kind RepairKind) bool {
	switch kind {
	case RepairCandidate, RepairCoverage, RepairTestAudit, RepairCoverageAndAudit:
		return true
	default:
		return false
	}
}

func validRepair(kind RepairKind) bool {
	return kind == RepairStyle || validPassRepair(kind)
}

func emptyRepairCounts() map[RepairKind]uint64 {
	return map[RepairKind]uint64{
		RepairCandidate:        0,
		RepairCoverage:         0,
		RepairTestAudit:        0,
		RepairCoverageAndAudit: 0,
		RepairStyle:            0,
	}
}

func cloneRepairCounts(counts map[RepairKind]uint64) map[RepairKind]uint64 {
	copy := emptyRepairCounts()
	for kind, count := range counts {
		copy[kind] = count
	}
	return copy
}
