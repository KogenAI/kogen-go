package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"kogen-go/internal/contract"
	"kogen-go/internal/gate"
	"kogen-go/internal/journal"
	"kogen-go/internal/provider/session"
)

const (
	ModeObservational     = "observational"
	AuditorMarker         = "You are Kogen's acceptance test auditor."
	DiffLimitCharacters   = 60_000
	ConversationStage     = "audit"
	ConversationAttempt   = "auditor"
	ConversationEpoch     = "initial"
	invalidReplyWarning   = "audit_malformed_reply"
	unknownItemWarning    = "audit_unknown_item"
	duplicateItemWarning  = "audit_duplicate_item"
	missingItemWarning    = "audit_missing_item"
	unknownVerdictWarning = "audit_unknown_verdict"
	malformedItemWarning  = "audit_malformed_item"
	transportWarning      = "audit_unavailable"
)

var (
	ErrInvalidManifest = errors.New("build audit: effective auditor role is unavailable")
	ErrInvalidInput    = errors.New("build audit: input is incomplete or inconsistent")
	rungPattern        = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	itemIDPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

// Verdict is the auditor's diagnosis. OverStrict and Contradicts are advice;
// neither can remove an approved item from verification or ranking.
type Verdict string

const (
	VerdictValid       Verdict = "valid"
	VerdictOverStrict  Verdict = "over_strict"
	VerdictContradicts Verdict = "contradicts"
)

// Item is one accepted, unambiguous observation about a failed item.
type Item struct {
	ID      string  `json:"id"`
	Verdict Verdict `json:"verdict"`
	Reason  string  `json:"reason"`
}

// Warning records an unusable, incomplete, or unrecognized auditor response.
// It is separate from the receipt so bad advice cannot masquerade as a verdict.
type Warning struct {
	Code    string   `json:"code"`
	ItemIDs []string `json:"item_ids"`
	Message string   `json:"message"`
}

// Receipt is the v1.3-draft observational audit projection. Demoted and
// AdvisoryItems are fixed policy outputs, not values inferred from the model.
type Receipt struct {
	Mode          string   `json:"mode"`
	Rung          string   `json:"rung"`
	Items         []Item   `json:"items"`
	Demoted       bool     `json:"demoted"`
	AdvisoryItems []string `json:"advisory_items"`
}

// Result carries the receipt, parser/provider warnings, and whether a model
// request was attempted. Skipped opportunities do not create audit events.
type Result struct {
	Receipt       Receipt   `json:"receipt"`
	Warnings      []Warning `json:"warnings"`
	Audited       bool      `json:"-"`
	SkippedReason string    `json:"-"`
}

// Input is prepared by the rung controller after it has established that the
// verification is red only because approved acceptance items failed. Failed
// IDs should be in approved acceptance order. Session must be a fresh,
// auditor-bound conversation for this rung.
type Input struct {
	Rung                 string
	AcceptanceOnlyRed    bool
	WitnessMode          bool
	FailedItemIDs        []string
	Request              []byte
	AcceptanceTestSource []byte
	FailureOutput        []byte
	CandidateDiff        []byte
	Session              *session.Conversation
}

// Prompt describes the one tool-less auditor request. Role always comes from
// the effective role manifest. Tools is an empty callable allowlist; transport
// must retain the complete canonical schema union and restrict calls with
// tool_choice=none. It must also retain response items and routing state on the
// supplied Session and keep retries within that same conversation.
type Prompt struct {
	AssignedRole contract.RoleName
	Role         contract.RoleSettings
	System       string
	User         string
	Tools        []string
	Session      *session.Conversation
}

// Transport sends the single logical Build-audit request. Provider retries
// and stream continuations belong inside this call and use Prompt.Session.
type Transport interface {
	Audit(context.Context, Prompt) (string, error)
}

// AdviceRecorder is the gate's display-only advice channel. Its method cannot
// update the gate verdict, receipt, counts, or landability.
type AdviceRecorder interface {
	RecordAuditAdvice([]gate.AuditAdvice)
}

// Auditor resolves the configured auditor once and permits at most one audit
// request per failed item across a Build. Share one instance across its rungs.
type Auditor struct {
	role      contract.RoleSettings
	transport Transport

	mu           sync.Mutex
	auditedRungs map[string]struct{}
	auditedItems map[string]struct{}
}

// New binds this auditor to the effective project/machine/default-resolved
// role. It never substitutes a package-local model default.
func New(manifest contract.RoleManifest, transport Transport) (*Auditor, error) {
	role, ok := manifest.Effective[contract.RoleName("auditor")]
	if !ok || role.Provider == "" || role.Model == "" || role.Effort == "" {
		return nil, ErrInvalidManifest
	}
	if transport == nil {
		return nil, errors.New("build audit: transport is required")
	}
	return &Auditor{
		role: role, transport: transport,
		auditedRungs: make(map[string]struct{}), auditedItems: make(map[string]struct{}),
	}, nil
}

// BeforeRepair audits an eligible red acceptance-only gate and makes the
// observation available before the rung's next repair. Witness mode and
// failures outside acceptance items skip the auditor. Transport failures are
// warnings so optional advice cannot block the real repair or gate.
func (a *Auditor) BeforeRepair(ctx context.Context, input Input, recorder AdviceRecorder) (Result, error) {
	result := newResult(input.Rung)
	if input.WitnessMode {
		result.SkippedReason = "witness_mode"
		return result, nil
	}
	if !input.AcceptanceOnlyRed {
		result.SkippedReason = "gate_not_acceptance_only_red"
		return result, nil
	}
	if len(input.FailedItemIDs) == 0 {
		result.SkippedReason = "no_failed_items"
		return result, nil
	}
	if a == nil {
		return Result{}, ErrInvalidInput
	}
	a.mu.Lock()
	_, alreadyAudited := a.auditedRungs[input.Rung]
	a.mu.Unlock()
	if alreadyAudited {
		result.SkippedReason = "rung_already_audited"
		return result, nil
	}
	if err := validateInput(ctx, input, a); err != nil {
		return Result{}, err
	}

	a.mu.Lock()
	if _, exists := a.auditedRungs[input.Rung]; exists {
		a.mu.Unlock()
		result.SkippedReason = "rung_already_audited"
		return result, nil
	}
	pendingIDs := make([]string, 0, len(input.FailedItemIDs))
	for _, id := range input.FailedItemIDs {
		if _, exists := a.auditedItems[id]; !exists {
			pendingIDs = append(pendingIDs, id)
		}
	}
	if len(pendingIDs) == 0 {
		a.mu.Unlock()
		result.SkippedReason = "failed_items_already_audited"
		return result, nil
	}
	a.auditedRungs[input.Rung] = struct{}{}
	for _, id := range pendingIDs {
		a.auditedItems[id] = struct{}{}
	}
	a.mu.Unlock()

	input.FailedItemIDs = pendingIDs
	user := userMessage(input)
	if err := input.Session.AppendUser(user); err != nil {
		a.mu.Lock()
		delete(a.auditedRungs, input.Rung)
		for _, id := range pendingIDs {
			delete(a.auditedItems, id)
		}
		a.mu.Unlock()
		return Result{}, fmt.Errorf("build audit: append auditor input: %w", err)
	}

	result.Audited = true
	reply, err := a.transport.Audit(ctx, Prompt{
		AssignedRole: contract.RoleName("auditor"), Role: a.role,
		System: auditorInstructions, User: user, Tools: []string{}, Session: input.Session,
	})
	if err != nil {
		result.Warnings = append(result.Warnings, Warning{
			Code: transportWarning, ItemIDs: []string{}, Message: "Build auditor request failed; verification remains unchanged",
		})
		return result, nil
	}

	items, warnings := parseReply(reply, input.FailedItemIDs)
	result.Receipt.Items = items
	result.Warnings = warnings
	if recorder != nil && len(items) != 0 {
		advice := make([]gate.AuditAdvice, 0, len(items))
		for _, item := range items {
			advice = append(advice, gate.AuditAdvice{ID: item.ID, Verdict: string(item.Verdict), Reason: item.Reason})
		}
		recorder.RecordAuditAdvice(advice)
	}
	return result, nil
}

// Event returns the standard observational journal receipt. It is unavailable
// when the auditor was skipped, and it never emits item acceptance events.
func (r Result) Event(ts int64) (journal.RunEvent, error) {
	if !r.Audited {
		return journal.RunEvent{}, errors.New("build audit: skipped result has no audit event")
	}
	items := make([]journal.AuditItem, 0, len(r.Receipt.Items))
	for _, item := range r.Receipt.Items {
		items = append(items, journal.AuditItem{ID: item.ID, Verdict: string(item.Verdict), Reason: item.Reason})
	}
	return journal.AuditEvent(ts, r.Receipt.Rung, items)
}

// AppendRepairAdvice returns feedback with valid over_strict/contradicts
// observations appended. The warning explicitly keeps every approved item
// required and directs the builder to repair implementation bytes for a real
// verification pass.
func (r Result) AppendRepairAdvice(feedback string) string {
	if !r.Audited {
		return feedback
	}
	var advice []Item
	for _, item := range r.Receipt.Items {
		if item.Verdict == VerdictOverStrict || item.Verdict == VerdictContradicts {
			advice = append(advice, item)
		}
	}
	if len(advice) == 0 {
		return feedback
	}
	var builder strings.Builder
	if feedback != "" {
		builder.WriteString(feedback)
		builder.WriteString("\n\n")
	}
	builder.WriteString("Build auditor observations (advisory only; every approved acceptance item remains required):\n")
	for _, item := range advice {
		builder.WriteString("- ")
		builder.WriteString(item.ID)
		builder.WriteByte(' ')
		builder.WriteString(string(item.Verdict))
		if item.Reason != "" {
			builder.WriteString(": ")
			builder.WriteString(item.Reason)
		}
		builder.WriteByte('\n')
	}
	builder.WriteString("Repair the implementation while preserving the approved acceptance source. All approved items must pass the full verification before landing.")
	return builder.String()
}

const auditorInstructions = AuditorMarker + " Check whether each failed acceptance test follows the verbatim Request. Reply with JSON only in this form: {\"items\":[{\"id\":\"A1\",\"verdict\":\"valid|over_strict|contradicts\",\"reason\":\"...\"}]} ."

func newResult(rung string) Result {
	return Result{
		Receipt:  Receipt{Mode: ModeObservational, Rung: rung, Items: []Item{}, Demoted: false, AdvisoryItems: []string{}},
		Warnings: []Warning{},
	}
}

func validateInput(ctx context.Context, input Input, auditor *Auditor) error {
	if ctx == nil || ctx.Err() != nil || auditor == nil || input.Session == nil ||
		!rungPattern.MatchString(input.Rung) || len(input.Request) == 0 || len(input.AcceptanceTestSource) == 0 {
		return ErrInvalidInput
	}
	for _, value := range [][]byte{input.Request, input.AcceptanceTestSource, input.FailureOutput, input.CandidateDiff} {
		if !utf8.Valid(value) {
			return fmt.Errorf("%w: auditor inputs must be UTF-8 text", ErrInvalidInput)
		}
	}
	seen := make(map[string]struct{}, len(input.FailedItemIDs))
	for _, id := range input.FailedItemIDs {
		if !itemIDPattern.MatchString(id) {
			return fmt.Errorf("%w: failed item id %q is invalid", ErrInvalidInput, id)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("%w: failed item id %q is duplicated", ErrInvalidInput, id)
		}
		seen[id] = struct{}{}
	}
	identity := input.Session.Identity()
	if identity.Role != contract.RoleName("auditor") || identity.Provider != auditor.role.Provider ||
		identity.Model != auditor.role.Model || identity.Effort != auditor.role.Effort ||
		identity.Stage != ConversationStage || identity.Attempt != ConversationAttempt ||
		identity.Rung != input.Rung || identity.Epoch != ConversationEpoch {
		return fmt.Errorf("%w: session identity does not match the resolved auditor role and rung", ErrInvalidInput)
	}
	if identity.ThreadID != session.DeriveThreadID(identity.RunID, ConversationStage, ConversationAttempt, input.Rung, ConversationEpoch) {
		return fmt.Errorf("%w: auditor thread id does not match the run/stage/attempt/rung/epoch tuple", ErrInvalidInput)
	}
	snapshot := input.Session.Snapshot()
	if len(snapshot.Protocol.History) != 0 || snapshot.Protocol.Routing.HasCodexTurnState {
		return fmt.Errorf("%w: auditor conversation must start with empty history and routing state", ErrInvalidInput)
	}
	return nil
}

func userMessage(input Input) string {
	diff := clipCharacters(string(input.CandidateDiff), DiffLimitCharacters)
	return "Failed acceptance items: " + strings.Join(input.FailedItemIDs, ", ") +
		"\n\nVerbatim Request:\n" + string(input.Request) +
		"\n\nAcceptance test source:\n" + string(input.AcceptanceTestSource) +
		"\n\nFailure output:\n" + string(input.FailureOutput) +
		fmt.Sprintf("\n\nCandidate diff (clipped to %d characters):\n", DiffLimitCharacters) + diff
}

func clipCharacters(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func parseReply(reply string, expectedIDs []string) ([]Item, []Warning) {
	var envelope struct {
		Items json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(reply), &envelope); err != nil || len(envelope.Items) == 0 {
		return malformedReplyWarnings(expectedIDs)
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(envelope.Items, &rows); err != nil || rows == nil {
		return malformedReplyWarnings(expectedIDs)
	}

	expected := make(map[string]struct{}, len(expectedIDs))
	counts := make(map[string]int, len(expectedIDs))
	valid := make(map[string]Item, len(expectedIDs))
	var warnings []Warning
	for _, id := range expectedIDs {
		expected[id] = struct{}{}
	}
	for index, raw := range rows {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			warnings = append(warnings, Warning{Code: malformedItemWarning, ItemIDs: []string{}, Message: fmt.Sprintf("auditor item %d is not an object", index+1)})
			continue
		}
		id, ok := rawString(fields, "id")
		if !ok || !itemIDPattern.MatchString(id) {
			warnings = append(warnings, Warning{Code: malformedItemWarning, ItemIDs: []string{}, Message: fmt.Sprintf("auditor item %d has no valid id", index+1)})
			continue
		}
		if _, known := expected[id]; !known {
			warnings = append(warnings, Warning{Code: unknownItemWarning, ItemIDs: []string{id}, Message: "auditor returned an id that is not a failed approved item"})
			continue
		}
		counts[id]++
		verdictText, verdictOK := rawString(fields, "verdict")
		reason, reasonOK := rawString(fields, "reason")
		if !verdictOK || !reasonOK {
			warnings = append(warnings, Warning{Code: malformedItemWarning, ItemIDs: []string{id}, Message: "auditor item must contain string verdict and reason fields"})
			continue
		}
		verdict := Verdict(verdictText)
		if verdict != VerdictValid && verdict != VerdictOverStrict && verdict != VerdictContradicts {
			warnings = append(warnings, Warning{Code: unknownVerdictWarning, ItemIDs: []string{id}, Message: "auditor returned an unknown verdict"})
			continue
		}
		valid[id] = Item{ID: id, Verdict: verdict, Reason: reason}
	}

	items := make([]Item, 0, len(expectedIDs))
	for _, id := range expectedIDs {
		switch counts[id] {
		case 0:
			warnings = append(warnings, Warning{Code: missingItemWarning, ItemIDs: []string{id}, Message: "auditor returned no item for a failed acceptance item"})
		case 1:
			if item, ok := valid[id]; ok {
				items = append(items, item)
			}
		default:
			delete(valid, id)
			warnings = append(warnings, Warning{Code: duplicateItemWarning, ItemIDs: []string{id}, Message: "auditor returned multiple rows for a failed acceptance item"})
		}
	}
	return items, dedupeWarnings(warnings)
}

func malformedReplyWarnings(expectedIDs []string) ([]Item, []Warning) {
	warnings := []Warning{{Code: invalidReplyWarning, ItemIDs: []string{}, Message: "auditor reply is not a JSON object with an items array"}}
	for _, id := range expectedIDs {
		warnings = append(warnings, Warning{Code: missingItemWarning, ItemIDs: []string{id}, Message: "auditor returned no item for a failed acceptance item"})
	}
	return []Item{}, dedupeWarnings(warnings)
}

func rawString(fields map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := fields[key]
	if !ok {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func dedupeWarnings(warnings []Warning) []Warning {
	if len(warnings) == 0 {
		return []Warning{}
	}
	seen := make(map[string]struct{}, len(warnings))
	result := make([]Warning, 0, len(warnings))
	for _, warning := range warnings {
		if warning.ItemIDs == nil {
			warning.ItemIDs = []string{}
		}
		encoded, _ := json.Marshal(warning)
		if _, exists := seen[string(encoded)]; exists {
			continue
		}
		seen[string(encoded)] = struct{}{}
		result = append(result, warning)
	}
	return result
}
