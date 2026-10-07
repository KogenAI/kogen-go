package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/intent"
	shapesession "kogen-go/internal/shape/session"
)

const (
	LedgerFileName           = "ledger.json"
	WarningsFileName         = "shape-warnings.json"
	RequirementAuditorSystem = "You are Kogen's requirement auditor. Map every atomic Request constraint to an Acceptance item or an untestable reason. Reply with JSON only."
	TestAuditorSystem        = "You are Kogen's acceptance test auditor. Check that each acceptance test follows the verbatim Request. Reply with JSON only."
)

var (
	ErrInvalidManifest = errors.New("shape audit: effective auditor role is unavailable")
	ErrInvalidInput    = errors.New("shape audit: pass input is incomplete")
)

// LedgerRow is one extracted Request constraint and its Acceptance mapping.
// MapsTo is either an Intent Acceptance id or "untestable: <reason>".
type LedgerRow struct {
	Constraint string `json:"constraint"`
	MapsTo     string `json:"maps_to"`
}

// TestAuditItem is one model verdict about an Acceptance item.
type TestAuditItem struct {
	ID       string `json:"id"`
	Verdict  string `json:"verdict"`
	Citation string `json:"citation"`
	Reason   string `json:"reason"`
}

// Warning has the persisted Shape warning representation.
type Warning struct {
	Code    string   `json:"code"`
	ItemIDs []string `json:"item_ids"`
	Message string   `json:"message"`
}

// Prompt is one tool-less auditor request. Role is resolved from the effective
// manifest for every call; callers must not substitute local model defaults.
type Prompt struct {
	Role         contract.RoleSettings
	System       string
	User         string
	Tools        []string
	AssignedRole contract.RoleName
}

// AttemptAccounting records each actual HTTP attempt independently of the
// shaper's turn allowance. A transport calls Dispatch immediately before a
// request and Complete after every response, including failed requests.
type AttemptAccounting interface {
	Dispatch(shapesession.AttemptKind) error
	Complete(*contract.TokenUsage) error
}

// Accounting receives Shape's auditor-role turn and attempt updates. The
// auditor counters remain separate from the primary and fallback shaper turns.
type Accounting interface {
	DispatchAuditorAttempt(shapesession.AttemptKind) error
	CompleteAuditorAttempt(*contract.TokenUsage) error
	EndAuditorTurn() error
}

// Transport sends one auditor request. It must call attempts.Dispatch before
// each HTTP request and attempts.Complete after it. Resends and stream
// continuations use AttemptRetry and AttemptContinuation respectively.
type Transport interface {
	Audit(context.Context, Prompt, AttemptAccounting) (string, error)
}

// PassInput contains the already validated candidate and base acceptance
// results for one traversal that reached Shape validation steps 7 and 8.
type PassInput struct {
	Request     []byte
	Intent      *intent.Intent
	IntentBytes []byte
	TestBytes   []byte
	BaseResults map[string]bool
}

// RepairFeedback is a single pass-consuming repair request. Kind is combined
// when coverage and test-audit findings arrive in the same traversal.
type RepairFeedback struct {
	Kind   shapesession.RepairKind
	Reason string
	Detail string
}

// Error formats the candidate feedback line retained by the Shape session.
func (f RepairFeedback) Error() string {
	if f.Detail == "" {
		return "candidate/" + f.Reason
	}
	return "candidate/" + f.Reason + ": " + f.Detail
}

// PassResult is the ledger/audit evidence from one eligible traversal. Warnings
// contains the evaluator's accumulated audit warnings, in first-observed order.
type PassResult struct {
	ApprovalSHA256 string
	Ledger         []LedgerRow
	TestAudit      []TestAuditItem
	CoverageGaps   []string
	Warnings       []Warning
	Repair         *RepairFeedback
}

// LedgerDocument is the stable on-disk ledger structure.
type LedgerDocument struct {
	ApprovalSHA256 string      `json:"approval_sha256"`
	Rows           []LedgerRow `json:"rows"`
}

// WarningsDocument is the stable on-disk Shape warning structure.
type WarningsDocument struct {
	ApprovalSHA256 string    `json:"approval_sha256"`
	Warnings       []Warning `json:"warnings"`
}

// Evaluator runs the two audits in order and retains one-shot repair state and
// deduplicated warnings for the whole Shape run, including its fallback turn.
// Use one Evaluator for one Shape run; calls must be sequential.
type Evaluator struct {
	role       contract.RoleSettings
	transport  Transport
	accounting Accounting

	coverageRepairUsed bool
	auditRepairUsed    bool
	warnings           []Warning
	warningKeys        map[string]struct{}
}

// New resolves the auditor role from the supplied manifest. The manifest is
// the only source of provider/model/effort pins for these requests.
func New(manifest contract.RoleManifest, transport Transport, accounting Accounting) (*Evaluator, error) {
	role, ok := manifest.Effective[contract.RoleName("auditor")]
	if !ok || role.Provider == "" || role.Model == "" || role.Effort == "" {
		return nil, ErrInvalidManifest
	}
	if transport == nil || accounting == nil {
		return nil, errors.New("shape audit: transport and accounting are required")
	}
	return &Evaluator{
		role: role, transport: transport, accounting: accounting,
		warningKeys: make(map[string]struct{}),
	}, nil
}

// RunPass always makes the requirement-ledger request before the acceptance-
// test audit. It completes both calls before evaluating coverage or repair
// predicates, so simultaneous findings consume one repair traversal.
func (e *Evaluator) RunPass(ctx context.Context, input PassInput) (PassResult, error) {
	if e == nil || ctx == nil || input.Intent == nil || len(input.IntentBytes) == 0 || len(input.TestBytes) == 0 {
		return PassResult{}, ErrInvalidInput
	}
	for _, item := range input.Intent.Acceptance {
		if _, ok := input.BaseResults[item.ID]; !ok {
			return PassResult{}, fmt.Errorf("%w: base result is missing for %s", ErrInvalidInput, item.ID)
		}
	}
	result := PassResult{ApprovalSHA256: intent.ApprovalSHA256(input.IntentBytes, input.TestBytes)}

	ledgerText, err := e.ask(ctx, RequirementAuditorSystem, requirementMessage(input.Request))
	if err != nil {
		return result, fmt.Errorf("shape audit: requirement auditor request: %w", err)
	}
	ledger, ledgerErr := parseLedger(ledgerText)

	baseOutputs := baseOutputs(input.Intent, input.BaseResults)
	auditText, err := e.ask(ctx, TestAuditorSystem, testAuditMessage(input.Request, input.IntentBytes, input.TestBytes, baseOutputs))
	if err != nil {
		result.Ledger = ledger
		return result, fmt.Errorf("shape audit: acceptance-test auditor request: %w", err)
	}
	auditItems, auditErr := parseTestAudit(auditText)
	if ledgerErr != nil {
		result.Ledger = ledger
		return result, fmt.Errorf("shape audit: %w", ledgerErr)
	}
	if auditErr != nil {
		result.Ledger = ledger
		return result, fmt.Errorf("shape audit: %w", auditErr)
	}

	result.Ledger = ledger
	result.TestAudit = auditItems
	result.CoverageGaps = coverageGaps(input.Request, ledger, input.Intent)
	coverageRepair := len(result.CoverageGaps) != 0 && !e.coverageRepairUsed
	repairableItems, auditWarnings := e.evaluateTestAudit(input.Request, input.Intent, auditItems)
	auditRepair := len(repairableItems) != 0

	if len(result.CoverageGaps) != 0 {
		if coverageRepair {
			e.coverageRepairUsed = true
		} else {
			e.addWarning(Warning{
				Code: "coverage_gap", ItemIDs: ledgerItemIDs(ledger, input.Intent),
				Message: strings.Join(result.CoverageGaps, "; "),
			})
		}
	}
	for _, warning := range auditWarnings {
		e.addWarning(warning)
	}
	if auditRepair {
		e.auditRepairUsed = true
	}
	result.Repair = repairFeedback(coverageRepair, auditRepair, result.CoverageGaps, repairableItems)
	result.Warnings = e.Warnings()
	return result, nil
}

// Warnings returns an independent copy of accumulated audit warnings.
func (e *Evaluator) Warnings() []Warning {
	if e == nil {
		return nil
	}
	result := make([]Warning, len(e.warnings))
	for index, warning := range e.warnings {
		result[index] = Warning{Code: warning.Code, ItemIDs: append([]string(nil), warning.ItemIDs...), Message: warning.Message}
	}
	return result
}

// LedgerBytes encodes this pass's ledger for ledger.json.
func (result PassResult) LedgerBytes() ([]byte, error) {
	return appendJSONNewline(LedgerDocument{ApprovalSHA256: result.ApprovalSHA256, Rows: nonNilRows(result.Ledger)})
}

// WarningsBytes encodes accumulated audit warnings for shape-warnings.json.
// preceding contains other Shape warnings already produced by validation.
func (result PassResult) WarningsBytes(preceding []Warning) ([]byte, error) {
	warnings := append([]Warning(nil), preceding...)
	warnings = append(warnings, result.Warnings...)
	warnings = MergeWarnings(warnings)
	return appendJSONNewline(WarningsDocument{ApprovalSHA256: result.ApprovalSHA256, Warnings: nonNilWarnings(warnings)})
}

// MergeWarnings combines validation and audit warnings, keeping the first
// occurrence of an identical code/item/message tuple.
func MergeWarnings(warnings []Warning) []Warning {
	result := make([]Warning, 0, len(warnings))
	seen := make(map[string]struct{}, len(warnings))
	for _, warning := range warnings {
		if warning.ItemIDs == nil {
			warning.ItemIDs = []string{}
		}
		key, _ := json.Marshal(warning)
		if _, ok := seen[string(key)]; ok {
			continue
		}
		seen[string(key)] = struct{}{}
		warning.ItemIDs = append([]string(nil), warning.ItemIDs...)
		result = append(result, warning)
	}
	return result
}

type attemptMeter struct {
	accounting Accounting
	started    bool
	open       bool
	closed     bool
	attempts   int
	err        error
}

func (m *attemptMeter) Dispatch(kind shapesession.AttemptKind) error {
	if m.err != nil {
		return m.err
	}
	if m.closed || m.open {
		m.err = errors.New("shape audit: auditor attempt is already open or turn is closed")
		return m.err
	}
	if !m.started && kind != shapesession.AttemptFirst {
		m.err = errors.New("shape audit: auditor turn must start with a first attempt")
		return m.err
	}
	if m.started && kind != shapesession.AttemptRetry && kind != shapesession.AttemptContinuation {
		m.err = errors.New("shape audit: retry or continuation required after first attempt")
		return m.err
	}
	if m.attempts >= shapesession.MaxHTTPAttemptsPerRequest {
		m.err = shapesession.ErrAttemptLimit
		return m.err
	}
	if err := m.accounting.DispatchAuditorAttempt(kind); err != nil {
		m.err = err
		return err
	}
	m.started = true
	m.open = true
	m.attempts++
	return nil
}

func (m *attemptMeter) Complete(usage *contract.TokenUsage) error {
	if m.err != nil {
		return m.err
	}
	if !m.open || m.closed {
		m.err = errors.New("shape audit: no auditor HTTP attempt is open")
		return m.err
	}
	if err := m.accounting.CompleteAuditorAttempt(usage); err != nil {
		m.err = err
		return err
	}
	m.open = false
	return nil
}

func (m *attemptMeter) finish(callErr error) error {
	var accountingErr error
	if m.open {
		// A transport must close failed HTTP attempts too. If it did not, retain
		// the attempted call as unknown usage before closing the separate turn.
		accountingErr = m.Complete(nil)
	}
	if m.started {
		if err := m.accounting.EndAuditorTurn(); err != nil {
			accountingErr = errors.Join(accountingErr, err)
		}
		m.closed = true
	}
	if m.err != nil {
		accountingErr = errors.Join(accountingErr, m.err)
	}
	if callErr == nil && !m.started {
		callErr = errors.New("shape audit: transport returned without dispatching an auditor request")
	}
	return errors.Join(callErr, accountingErr)
}

func (e *Evaluator) ask(ctx context.Context, system, user string) (string, error) {
	meter := &attemptMeter{accounting: e.accounting}
	text, callErr := e.transport.Audit(ctx, Prompt{
		AssignedRole: "auditor", Role: e.role, System: system, User: user, Tools: []string{},
	}, meter)
	if err := meter.finish(callErr); err != nil {
		return "", err
	}
	return text, nil
}

func requirementMessage(request []byte) string {
	return "Extract every atomic constraint from this Request and map each to an Acceptance item id, or use `untestable: <reason>`. Return {\"rows\":[{\"constraint\",\"maps_to\"}]} only.\n\nRequest:\n" + string(bytes.ToValidUTF8(request, []byte("�")))
}

func testAuditMessage(request, intentBytes, testBytes []byte, outputs string) string {
	return "Audit each acceptance item against the Request. Reply with {\"items\":[{\"id\",\"verdict\":\"valid|over_strict|infeasible\",\"citation\",\"reason\"}]} only. A citation must be an exact substring of the Request.\n\nRequest:\n" +
		string(bytes.ToValidUTF8(request, []byte("�"))) + "\n\nIntent:\n" +
		string(bytes.ToValidUTF8(intentBytes, []byte("�"))) + "\n\nAcceptance test:\n" +
		string(bytes.ToValidUTF8(testBytes, []byte("�"))) + "\n\nBase output for each item:\n" + outputs
}

func baseOutputs(parsed *intent.Intent, results map[string]bool) string {
	lines := make([]string, 0, len(parsed.Acceptance))
	for _, item := range parsed.Acceptance {
		status := "failed"
		if results[item.ID] {
			status = "passed"
		}
		lines = append(lines, item.ID+": "+status)
	}
	return strings.Join(lines, "\n")
}

func parseLedger(text string) ([]LedgerRow, error) {
	var envelope struct {
		Rows []json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		return nil, errors.New("requirement auditor returned invalid JSON")
	}
	if envelope.Rows == nil {
		return nil, errors.New("requirement auditor reply must contain rows")
	}
	rows := make([]LedgerRow, 0, len(envelope.Rows))
	for _, raw := range envelope.Rows {
		var row struct {
			Constraint *string `json:"constraint"`
			MapsTo     *string `json:"maps_to"`
		}
		if err := json.Unmarshal(raw, &row); err != nil || row.Constraint == nil || row.MapsTo == nil || strings.TrimSpace(*row.Constraint) == "" || strings.TrimSpace(*row.MapsTo) == "" {
			return nil, errors.New("requirement auditor returned a ledger row missing constraint or maps_to")
		}
		rows = append(rows, LedgerRow{Constraint: *row.Constraint, MapsTo: *row.MapsTo})
	}
	return rows, nil
}

func parseTestAudit(text string) ([]TestAuditItem, error) {
	var envelope struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		return nil, errors.New("acceptance test auditor returned invalid JSON")
	}
	if envelope.Items == nil {
		return nil, errors.New("acceptance test auditor reply must contain items")
	}
	items := make([]TestAuditItem, 0, len(envelope.Items))
	for _, raw := range envelope.Items {
		var item struct {
			ID       *string `json:"id"`
			Verdict  *string `json:"verdict"`
			Citation string  `json:"citation"`
			Reason   string  `json:"reason"`
		}
		if err := json.Unmarshal(raw, &item); err != nil || item.ID == nil || item.Verdict == nil || strings.TrimSpace(*item.ID) == "" || strings.TrimSpace(*item.Verdict) == "" {
			return nil, errors.New("acceptance test auditor returned an item missing id or verdict")
		}
		items = append(items, TestAuditItem{ID: *item.ID, Verdict: *item.Verdict, Citation: item.Citation, Reason: item.Reason})
	}
	return items, nil
}

func coverageGaps(request []byte, rows []LedgerRow, parsed *intent.Intent) []string {
	constraints := make([][]byte, 0, len(rows))
	for _, row := range rows {
		constraints = append(constraints, []byte(row.Constraint))
	}
	var gaps []string
	for _, literal := range requestLiterals(request) {
		covered := false
		for _, constraint := range constraints {
			if bytes.Contains(constraint, literal) {
				covered = true
				break
			}
		}
		if !covered {
			gaps = append(gaps, string(literal))
		}
	}
	validIDs := make(map[string]struct{}, len(parsed.Acceptance))
	for _, item := range parsed.Acceptance {
		validIDs[item.ID] = struct{}{}
	}
	for _, row := range rows {
		if strings.HasPrefix(row.MapsTo, "untestable: ") {
			if strings.TrimSpace(strings.TrimPrefix(row.MapsTo, "untestable: ")) == "" {
				gaps = append(gaps, row.Constraint+" has an empty untestable reason")
			}
			continue
		}
		if _, ok := validIDs[row.MapsTo]; !ok {
			gaps = append(gaps, row.Constraint+" maps to missing item "+row.MapsTo)
		}
	}
	sort.Strings(gaps)
	return dedupeStrings(gaps)
}

func requestLiterals(request []byte) [][]byte {
	var values [][]byte
	for index := 0; index < len(request); {
		if request[index] >= '0' && request[index] <= '9' {
			start := index
			for index < len(request) && request[index] >= '0' && request[index] <= '9' {
				index++
			}
			values = append(values, bytes.Clone(request[start:index]))
			continue
		}
		if request[index] == '`' || request[index] == '\'' || request[index] == '"' {
			delimiter := request[index]
			start := index + 1
			if relativeEnd := bytes.IndexByte(request[start:], delimiter); relativeEnd >= 0 {
				end := start + relativeEnd
				if end > start {
					values = append(values, bytes.Clone(request[start:end]))
				}
				index = end + 1
				continue
			}
		}
		index++
	}
	sort.Slice(values, func(i, j int) bool { return bytes.Compare(values[i], values[j]) < 0 })
	unique := values[:0]
	for _, value := range values {
		if len(unique) == 0 || !bytes.Equal(unique[len(unique)-1], value) {
			unique = append(unique, value)
		}
	}
	return unique
}

func validCitation(citation string, request []byte) bool {
	return citation != "" && bytes.Contains(request, []byte(citation))
}

func (e *Evaluator) evaluateTestAudit(request []byte, parsed *intent.Intent, items []TestAuditItem) ([]TestAuditItem, []Warning) {
	acceptance := make(map[string]struct{}, len(parsed.Acceptance))
	order := make([]string, 0, len(parsed.Acceptance))
	for _, item := range parsed.Acceptance {
		acceptance[item.ID] = struct{}{}
		order = append(order, item.ID)
	}
	byID := make(map[string]TestAuditItem, len(items))
	counts := make(map[string]int, len(items))
	var warnings []Warning
	for _, item := range items {
		if _, ok := acceptance[item.ID]; !ok {
			warnings = append(warnings, Warning{Code: "audit_unknown_item", Message: "test auditor returned unknown item " + item.ID})
			continue
		}
		counts[item.ID]++
		byID[item.ID] = item
	}
	for _, id := range order {
		if counts[id] == 0 {
			warnings = append(warnings, Warning{Code: "audit_missing_item", ItemIDs: []string{id}, Message: "test auditor returned no verdict for " + id})
		}
		if counts[id] > 1 {
			warnings = append(warnings, Warning{Code: "audit_duplicate_item", ItemIDs: []string{id}, Message: "test auditor returned multiple verdicts for " + id})
		}
	}

	var repairable []TestAuditItem
	for _, id := range order {
		if counts[id] != 1 {
			continue
		}
		item := byID[id]
		if item.Verdict == "valid" {
			continue
		}
		if item.Verdict != "over_strict" && item.Verdict != "infeasible" {
			warnings = append(warnings, Warning{Code: "audit_unknown_verdict", ItemIDs: []string{id}, Message: "test auditor returned unknown verdict " + item.Verdict})
			continue
		}
		if validCitation(item.Citation, request) && !e.auditRepairUsed {
			repairable = append(repairable, item)
			continue
		}
		warnings = append(warnings, Warning{Code: "audit_" + item.Verdict, ItemIDs: []string{id}, Message: item.Reason})
	}
	if len(repairable) != 0 && !e.auditRepairUsed {
		return repairable, warnings
	}
	return nil, warnings
}

func repairFeedback(coverage, testAudit bool, gaps []string, items []TestAuditItem) *RepairFeedback {
	if !coverage && !testAudit {
		return nil
	}
	feedback := &RepairFeedback{}
	switch {
	case coverage && testAudit:
		feedback.Kind = shapesession.RepairCoverageAndAudit
		feedback.Reason = "coverage_and_test_audit"
		feedback.Detail = "Coverage gaps:\n" + strings.Join(gaps, "\n") + "\n\nAcceptance-test audit:\n" + auditDetails(items)
	case coverage:
		feedback.Kind = shapesession.RepairCoverage
		feedback.Reason = "coverage_gap"
		feedback.Detail = strings.Join(gaps, "\n")
	default:
		feedback.Kind = shapesession.RepairTestAudit
		feedback.Reason = "audit_over_strict"
		feedback.Detail = auditDetails(items)
	}
	return feedback
}

func auditDetails(items []TestAuditItem) string {
	lines := make([]string, 0, len(items))
	for _, item := range items {
		line := item.ID + " " + item.Verdict
		if item.Reason != "" {
			line += ": " + item.Reason
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func ledgerItemIDs(rows []LedgerRow, parsed *intent.Intent) []string {
	valid := make(map[string]struct{}, len(parsed.Acceptance))
	for _, item := range parsed.Acceptance {
		valid[item.ID] = struct{}{}
	}
	seen := make(map[string]struct{})
	var ids []string
	for _, row := range rows {
		if _, ok := valid[row.MapsTo]; ok {
			if _, exists := seen[row.MapsTo]; !exists {
				seen[row.MapsTo] = struct{}{}
				ids = append(ids, row.MapsTo)
			}
		}
	}
	return ids
}

func (e *Evaluator) addWarning(warning Warning) {
	if warning.ItemIDs == nil {
		warning.ItemIDs = []string{}
	}
	encoded, _ := json.Marshal(warning)
	key := string(encoded)
	if _, ok := e.warningKeys[key]; ok {
		return
	}
	e.warningKeys[key] = struct{}{}
	e.warnings = append(e.warnings, warning)
}

func nonNilRows(rows []LedgerRow) []LedgerRow {
	if rows == nil {
		return []LedgerRow{}
	}
	return rows
}

func nonNilWarnings(warnings []Warning) []Warning {
	if warnings == nil {
		return []Warning{}
	}
	return warnings
}

func appendJSONNewline(value any) ([]byte, error) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}
