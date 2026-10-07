package audit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"kogen-go/internal/contract"
	"kogen-go/internal/intent"
	shapesession "kogen-go/internal/shape/session"
)

const testIntent = "---\ntitle: Audit\nsize: small\ndomains: [app]\n---\nA small behavior.\n\n## Acceptance\n- A1: It honors the named contract.\n- A2: It returns the required output.\n\n## Verify\n- A1: test domain=app\n- A2: test domain=app\n\n## Notes\nApproach: update the handler while preserving the contract.\n\n## Request\nImplement 2 outputs named `alpha` and \"gamma\"."

const testSource = "package acceptance\n// A1\n// A2\n"

type fakeTransport struct {
	responses []string
	calls     []Prompt
	sequence  []string
	retryAt   map[int]bool
}

func (f *fakeTransport) Audit(_ context.Context, prompt Prompt, meter AttemptAccounting) (string, error) {
	index := len(f.calls)
	f.calls = append(f.calls, prompt)
	if prompt.System == RequirementAuditorSystem {
		f.sequence = append(f.sequence, "ledger")
	} else if prompt.System == TestAuditorSystem {
		f.sequence = append(f.sequence, "test_audit")
	} else {
		f.sequence = append(f.sequence, "unknown")
	}
	if err := meter.Dispatch(shapesession.AttemptFirst); err != nil {
		return "", err
	}
	if err := meter.Complete(nil); err != nil {
		return "", err
	}
	if f.retryAt[index] {
		if err := meter.Dispatch(shapesession.AttemptContinuation); err != nil {
			return "", err
		}
		if err := meter.Complete(&contract.TokenUsage{Input: int64ptr(12)}); err != nil {
			return "", err
		}
	}
	if index >= len(f.responses) {
		return "", errors.New("unexpected audit request")
	}
	return f.responses[index], nil
}

type fakeAccounting struct {
	turns         int
	attempts      int
	continuations int
	unknown       int
	open          bool
}

func (a *fakeAccounting) DispatchAuditorAttempt(kind shapesession.AttemptKind) error {
	if a.open {
		return errors.New("attempt already open")
	}
	if kind == shapesession.AttemptFirst {
		a.turns++
	} else if kind == shapesession.AttemptContinuation {
		a.continuations++
	}
	a.attempts++
	a.open = true
	return nil
}

func (a *fakeAccounting) CompleteAuditorAttempt(usage *contract.TokenUsage) error {
	if !a.open {
		return errors.New("no attempt open")
	}
	if usage == nil {
		a.unknown++
	}
	a.open = false
	return nil
}

func (a *fakeAccounting) EndAuditorTurn() error {
	if a.open {
		return errors.New("attempt left open")
	}
	return nil
}

func newEvaluator(t *testing.T, transport Transport, accounting Accounting) *Evaluator {
	t.Helper()
	evaluator, err := New(contract.RoleManifest{Effective: map[contract.RoleName]contract.RoleSettings{
		"auditor": {Provider: "grok", Model: "grok-4.6", Effort: "high"},
	}}, transport, accounting)
	if err != nil {
		t.Fatal(err)
	}
	return evaluator
}

func makePassInput(t *testing.T) PassInput {
	t.Helper()
	parsed, err := intent.Parse("audit-demo", []byte(testIntent))
	if err != nil {
		t.Fatal(err)
	}
	return PassInput{
		Request: []byte("Implement 2 outputs named `alpha` and \"gamma\"."),
		Intent:  parsed, IntentBytes: []byte(testIntent), TestBytes: []byte(testSource),
		BaseResults: map[string]bool{"A1": false, "A2": true},
	}
}

func TestBothAuditsRunInOrderWithResolvedRoleAndOneCombinedRepair(t *testing.T) {
	transport := &fakeTransport{responses: []string{
		`{"rows":[{"constraint":"2 outputs named alpha","maps_to":"A1"},{"constraint":"gamma output","maps_to":"A9"}]}`,
		`{"items":[{"id":"A1","verdict":"over_strict","citation":"2 outputs","reason":"the assertion exceeds the Request"},{"id":"A2","verdict":"valid","citation":"","reason":""}]}`,
		`{"rows":[{"constraint":"2 outputs named alpha","maps_to":"A1"},{"constraint":"gamma output","maps_to":"A9"}]}`,
		`{"items":[{"id":"A1","verdict":"over_strict","citation":"2 outputs","reason":"still too strict"},{"id":"A2","verdict":"valid"}]}`,
	}, retryAt: map[int]bool{1: true}}
	accounting := &fakeAccounting{}
	evaluator := newEvaluator(t, transport, accounting)
	input := makePassInput(t)

	first, err := evaluator.RunPass(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Repair == nil || first.Repair.Kind != shapesession.RepairCoverageAndAudit {
		t.Fatalf("combined repair = %#v", first.Repair)
	}
	if !strings.Contains(first.Repair.Detail, "gamma") || !strings.Contains(first.Repair.Detail, "A1 over_strict") {
		t.Fatalf("combined feedback omits a finding: %q", first.Repair.Detail)
	}
	if got := strings.Join(transport.sequence[:2], ","); got != "ledger,test_audit" {
		t.Fatalf("first audit order = %q", got)
	}
	for _, call := range transport.calls[:2] {
		if call.AssignedRole != "auditor" || call.Role != (contract.RoleSettings{Provider: "grok", Model: "grok-4.6", Effort: "high"}) {
			t.Fatalf("request lost resolved auditor role: %#v", call)
		}
		if len(call.Tools) != 0 {
			t.Fatalf("auditor request was not tool-less: %#v", call.Tools)
		}
	}
	if !strings.Contains(transport.calls[1].User, "A1: failed\nA2: passed") {
		t.Fatalf("test audit omitted ordered base results: %q", transport.calls[1].User)
	}

	second, err := evaluator.RunPass(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if second.Repair != nil {
		t.Fatalf("second audit repeated a repair: %#v", second.Repair)
	}
	if got := strings.Join(transport.sequence[2:], ","); got != "ledger,test_audit" {
		t.Fatalf("second audit order = %q", got)
	}
	if len(second.Warnings) != 2 || second.Warnings[0].Code != "coverage_gap" || second.Warnings[1].Code != "audit_over_strict" {
		t.Fatalf("persistent findings did not become warnings: %#v", second.Warnings)
	}
	if accounting.turns != 4 || accounting.attempts != 5 || accounting.continuations != 1 || accounting.unknown != 4 {
		t.Fatalf("auditor usage was not separately accounted: %#v", accounting)
	}
	if accounting.open {
		t.Fatal("auditor attempt remained open")
	}
}

func TestCoverageChecksRawRequestLiteralsAndRequiresExistingOrUntestableMappings(t *testing.T) {
	parsed, err := intent.Parse("audit-demo", []byte(testIntent))
	if err != nil {
		t.Fatal(err)
	}
	request := []byte("2 `alpha` \"gamma\" 'zeta' 2")
	rows := []LedgerRow{
		{Constraint: "2 items named alpha", MapsTo: "A1"},
		{Constraint: "the gamma item", MapsTo: "A2"},
		{Constraint: "zeta behavior", MapsTo: "untestable: external system cannot be observed"},
	}
	if gaps := coverageGaps(request, rows, parsed); len(gaps) != 0 {
		t.Fatalf("covered literals and valid mappings produced gaps: %#v", gaps)
	}
	rows[2].MapsTo = "untestable: "
	rows = append(rows, LedgerRow{Constraint: "another", MapsTo: "A77"})
	gaps := coverageGaps(request, rows, parsed)
	if !containsString(gaps, "zeta behavior has an empty untestable reason") || !containsString(gaps, "another maps to missing item A77") {
		t.Fatalf("invalid ledger mappings were not rejected: %#v", gaps)
	}
}

func TestCitationsMustBeExactRequestSubstringsAndBadCoverageStillRunsTestAudit(t *testing.T) {
	transport := &fakeTransport{responses: []string{
		`not-json`,
		`{"items":[{"id":"A1","verdict":"over_strict","citation":"a phrase from the intent","reason":"unsupported"}]}`,
	}}
	accounting := &fakeAccounting{}
	evaluator := newEvaluator(t, transport, accounting)
	_, err := evaluator.RunPass(context.Background(), makePassInput(t))
	if err == nil || !strings.Contains(err.Error(), "requirement auditor returned invalid JSON") {
		t.Fatalf("malformed ledger error = %v", err)
	}
	if got := strings.Join(transport.sequence, ","); got != "ledger,test_audit" {
		t.Fatalf("test audit was skipped after malformed ledger: %q", got)
	}
	if accounting.turns != 2 || accounting.attempts != 2 {
		t.Fatalf("both auditor requests were not accounted: %#v", accounting)
	}
	if validCitation("a phrase from the intent", []byte("Request says another phrase")) {
		t.Fatal("citation outside the raw Request was accepted")
	}
}

func TestMalformedAuditEntriesWarnAndDoNotRepair(t *testing.T) {
	transport := &fakeTransport{responses: []string{
		`{"rows":[{"constraint":"2 alpha gamma","maps_to":"A1"}]}`,
		`{"items":[{"id":"A1","verdict":"over_strict","citation":"not present in Request","reason":"no evidence"},{"id":"A1","verdict":"valid"},{"id":"A99","verdict":"valid"}]}`,
	}}
	accounting := &fakeAccounting{}
	evaluator := newEvaluator(t, transport, accounting)
	result, err := evaluator.RunPass(context.Background(), makePassInput(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.Repair != nil {
		t.Fatalf("invalid citation/duplicate/unknown audit requested repair: %#v", result.Repair)
	}
	for _, code := range []string{"audit_duplicate_item", "audit_missing_item", "audit_unknown_item"} {
		if !containsWarning(result.Warnings, code) {
			t.Fatalf("warning %q missing from %#v", code, result.Warnings)
		}
	}
}

func TestUncitedNonValidVerdictWarnsWithoutRepair(t *testing.T) {
	transport := &fakeTransport{responses: []string{
		`{"rows":[{"constraint":"2 outputs alpha gamma","maps_to":"A1"},{"constraint":"the quoted gamma result","maps_to":"A2"}]}`,
		`{"items":[{"id":"A1","verdict":"infeasible","citation":"not in the Request","reason":"assertion cannot be run"},{"id":"A2","verdict":"valid"}]}`,
	}}
	result, err := newEvaluator(t, transport, &fakeAccounting{}).RunPass(context.Background(), makePassInput(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.Repair != nil {
		t.Fatalf("uncited non-valid verdict requested repair: %#v", result.Repair)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Code != "audit_infeasible" || result.Warnings[0].ItemIDs[0] != "A1" {
		t.Fatalf("uncited verdict warning = %#v", result.Warnings)
	}
}

func TestArtifactEncodingUsesLedgerAndWarningsSchema(t *testing.T) {
	result := PassResult{
		Ledger:   []LedgerRow{{Constraint: "keep alpha", MapsTo: "A1"}},
		Warnings: []Warning{{Code: "audit_infeasible", ItemIDs: []string{"A1"}, Message: "citation absent"}},
	}
	result.ApprovalSHA256 = strings.Repeat("b", 64)
	ledger, err := result.LedgerBytes()
	if err != nil {
		t.Fatal(err)
	}
	var ledgerDocument LedgerDocument
	if err := json.Unmarshal(ledger, &ledgerDocument); err != nil {
		t.Fatal(err)
	}
	if ledgerDocument.ApprovalSHA256 != strings.Repeat("b", 64) || len(ledgerDocument.Rows) != 1 || ledgerDocument.Rows[0].MapsTo != "A1" {
		t.Fatalf("ledger artifact = %s", ledger)
	}
	warnings, err := result.WarningsBytes([]Warning{{Code: "lint_todo", Message: "leave intact"}})
	if err != nil {
		t.Fatal(err)
	}
	var warningsDocument WarningsDocument
	if err := json.Unmarshal(warnings, &warningsDocument); err != nil {
		t.Fatal(err)
	}
	if len(warningsDocument.Warnings) != 2 || warningsDocument.Warnings[0].Code != "lint_todo" || warningsDocument.Warnings[1].Code != "audit_infeasible" {
		t.Fatalf("warning artifact = %s", warnings)
	}
	if !strings.HasSuffix(string(ledger), "\n") || !strings.HasSuffix(string(warnings), "\n") {
		t.Fatal("JSON artifacts must end with a newline")
	}
}

func int64ptr(value int64) *int64 { return &value }

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsWarning(values []Warning, code string) bool {
	for _, value := range values {
		if value.Code == code {
			return true
		}
	}
	return false
}
