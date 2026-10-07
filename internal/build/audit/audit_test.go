package audit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"kogen-go/internal/contract"
	"kogen-go/internal/gate"
	"kogen-go/internal/provider/session"
)

type fakeTransport struct {
	reply  string
	err    error
	prompt Prompt
	calls  int
}

func (f *fakeTransport) Audit(_ context.Context, prompt Prompt) (string, error) {
	f.calls++
	f.prompt = prompt
	return f.reply, f.err
}

type adviceCollector struct{ items []gate.AuditAdvice }

func (c *adviceCollector) RecordAuditAdvice(items []gate.AuditAdvice) {
	c.items = append(c.items, items...)
}

func TestBeforeRepairUsesResolvedToollessRoleAndRecordsObservationalAdvice(t *testing.T) {
	transport := &fakeTransport{reply: `{"items":[{"id":"A2","verdict":"over_strict","reason":"extra assertion"},{"id":"A1","verdict":"valid","reason":"matches Request"}]}`}
	role := contract.RoleSettings{Provider: "grok", Model: "grok-4.6", Effort: "high"}
	auditor, err := New(contract.RoleManifest{Effective: map[contract.RoleName]contract.RoleSettings{"auditor": role}}, transport)
	if err != nil {
		t.Fatal(err)
	}
	session := newAuditSession(t, role, "R1")
	input := Input{
		Rung: "R1", AcceptanceOnlyRed: true, FailedItemIDs: []string{"A1", "A2"},
		Request:              []byte("Implement the requested widget."),
		AcceptanceTestSource: []byte("test \"widget\" do\n  assert widget()\nend\n"),
		FailureOutput:        []byte("assertion failed at widget_test.go:2"),
		CandidateDiff:        []byte(strings.Repeat("🙂", DiffLimitCharacters+1)),
		Session:              session,
	}
	collector := &adviceCollector{}
	result, err := auditor.BeforeRepair(context.Background(), input, collector)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Audited || result.Receipt.Mode != ModeObservational || result.Receipt.Demoted || len(result.Receipt.AdvisoryItems) != 0 {
		t.Fatalf("unexpected observational receipt: %+v", result.Receipt)
	}
	if len(result.Receipt.Items) != 2 || result.Receipt.Items[0].ID != "A1" || result.Receipt.Items[1].ID != "A2" {
		t.Fatalf("receipt did not preserve failed-item order: %+v", result.Receipt.Items)
	}
	if transport.calls != 1 {
		t.Fatalf("audit calls = %d, want 1", transport.calls)
	}
	if transport.prompt.AssignedRole != "auditor" || transport.prompt.Role != role || transport.prompt.Session != session {
		t.Fatalf("prompt did not use the resolved role and same session: %+v", transport.prompt)
	}
	if transport.prompt.Tools == nil || len(transport.prompt.Tools) != 0 {
		t.Fatalf("Build auditor must be tool-less, got tools %#v", transport.prompt.Tools)
	}
	if !strings.HasPrefix(transport.prompt.System, AuditorMarker) {
		t.Fatalf("system prompt is missing the test-auditor marker: %q", transport.prompt.System)
	}
	for _, required := range []string{
		"Failed acceptance items: A1, A2", "Verbatim Request:\nImplement the requested widget.",
		"Acceptance test source:\ntest \"widget\" do", "Failure output:\nassertion failed",
		"Candidate diff (clipped to 60000 characters):\n",
	} {
		if !strings.Contains(transport.prompt.User, required) {
			t.Errorf("prompt missing %q", required)
		}
	}
	if !strings.HasSuffix(transport.prompt.User, strings.Repeat("🙂", DiffLimitCharacters)) {
		t.Fatal("candidate diff was not clipped at the Unicode character limit")
	}
	if history := session.Snapshot().Protocol.History; len(history) != 1 || history[0].Kind != contract.SessionInput {
		t.Fatalf("audit input was not appended to its persistent session: %+v", history)
	}
	if len(collector.items) != 2 || collector.items[1].Verdict != "over_strict" {
		t.Fatalf("gate received unexpected display-only advice: %+v", collector.items)
	}

	feedback := result.AppendRepairAdvice("acceptance A2 failed")
	for _, expected := range []string{
		"acceptance A2 failed", "A2 over_strict: extra assertion",
		"every approved acceptance item remains required", "full verification before landing",
	} {
		if !strings.Contains(feedback, expected) {
			t.Errorf("repair feedback missing %q", expected)
		}
	}
	if strings.Contains(feedback, "A1 valid") {
		t.Fatal("valid observations should not add repair advice")
	}

	event, err := result.Event(17)
	if err != nil {
		t.Fatal(err)
	}
	if event.Event != "audit" {
		t.Fatalf("unexpected event type %q", event.Event)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	var mode string
	var demoted bool
	var advisory []string
	if err := json.Unmarshal(fields["mode"], &mode); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(fields["demoted"], &demoted); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(fields["advisory_items"], &advisory); err != nil {
		t.Fatal(err)
	}
	if mode != ModeObservational || demoted || advisory == nil || len(advisory) != 0 {
		t.Fatalf("journal receipt is not observational: %s", encoded)
	}
	if fields["acceptance_demoted"] != nil {
		t.Fatalf("reserved demotion event leaked into receipt: %s", encoded)
	}
}

func TestWitnessAndNonAcceptanceFailuresSkipWithoutCallingAuditor(t *testing.T) {
	transport := &fakeTransport{reply: `{"items":[]}`}
	auditor, err := New(auditManifest(), transport)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []Input{
		{Rung: "R1", AcceptanceOnlyRed: true, WitnessMode: true},
		{Rung: "R1", AcceptanceOnlyRed: false},
		{Rung: "R1", AcceptanceOnlyRed: true},
	} {
		result, err := auditor.BeforeRepair(context.Background(), input, nil)
		if err != nil {
			t.Fatalf("skipped input returned an error: %v", err)
		}
		if result.Audited || result.Receipt.Mode != ModeObservational || result.Receipt.Demoted || len(result.Receipt.AdvisoryItems) != 0 {
			t.Fatalf("skip changed audit policy: %+v", result)
		}
		if _, err := result.Event(1); err == nil {
			t.Fatal("skipped audit unexpectedly produced an audit event")
		}
	}
	if transport.calls != 0 {
		t.Fatalf("skipped paths called auditor %d times", transport.calls)
	}
}

func TestMalformedUnknownAndDuplicateRowsOnlyWarn(t *testing.T) {
	transport := &fakeTransport{reply: `{"items":[` +
		`{"id":"A1","verdict":"over_strict","reason":"first"},` +
		`{"id":"A1","verdict":"contradicts","reason":"second"},` +
		`{"id":"A9","verdict":"valid","reason":"unknown item"},` +
		`{"id":"A2","verdict":"maybe","reason":"unknown verdict"},` +
		`{"id":"A4","verdict":"valid"}]}`}
	auditor, err := New(auditManifest(), transport)
	if err != nil {
		t.Fatal(err)
	}
	input := validInput(t, "R1", auditManifest().Effective["auditor"])
	input.FailedItemIDs = []string{"A1", "A2", "A3", "A4"}
	collector := &adviceCollector{}
	result, err := auditor.BeforeRepair(context.Background(), input, collector)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Receipt.Items) != 0 || len(collector.items) != 0 {
		t.Fatalf("ambiguous or invalid rows became advice: %+v / %+v", result.Receipt.Items, collector.items)
	}
	for _, code := range []string{duplicateItemWarning, unknownItemWarning, unknownVerdictWarning, malformedItemWarning, missingItemWarning} {
		if !hasWarning(result.Warnings, code) {
			t.Errorf("warning %q missing from %+v", code, result.Warnings)
		}
	}
	if result.Receipt.Demoted || len(result.Receipt.AdvisoryItems) != 0 {
		t.Fatal("invalid auditor rows changed eligibility fields")
	}
	if result.AppendRepairAdvice("gate feedback") != "gate feedback" {
		t.Fatal("invalid rows added repair instructions")
	}
}

func TestGarbledReplyAndProviderFailureAreNonBlockingWarnings(t *testing.T) {
	for _, test := range []struct {
		name      string
		transport *fakeTransport
		warning   string
	}{
		{name: "garbled", transport: &fakeTransport{reply: "not JSON"}, warning: invalidReplyWarning},
		{name: "provider failure", transport: &fakeTransport{err: errors.New("offline")}, warning: transportWarning},
	} {
		t.Run(test.name, func(t *testing.T) {
			auditor, err := New(auditManifest(), test.transport)
			if err != nil {
				t.Fatal(err)
			}
			input := validInput(t, "R1", auditManifest().Effective["auditor"])
			result, err := auditor.BeforeRepair(context.Background(), input, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Audited || !hasWarning(result.Warnings, test.warning) {
				t.Fatalf("expected a non-blocking %q warning, got %+v", test.warning, result)
			}
			if result.Receipt.Demoted || len(result.Receipt.AdvisoryItems) != 0 {
				t.Fatal("warning changed observational receipt")
			}
		})
	}
}

func TestAuditRunsAtMostOncePerItemAcrossBuildAndRejectsMismatchedRoleSession(t *testing.T) {
	transport := &fakeTransport{reply: `{"items":[{"id":"A1","verdict":"valid","reason":"matches"}]}`}
	role := auditManifest().Effective["auditor"]
	auditor, err := New(auditManifest(), transport)
	if err != nil {
		t.Fatal(err)
	}
	input := validInput(t, "R1", role)
	first, err := auditor.BeforeRepair(context.Background(), input, nil)
	if err != nil || !first.Audited {
		t.Fatalf("first call: result=%+v err=%v", first, err)
	}
	second, err := auditor.BeforeRepair(context.Background(), input, nil)
	if err != nil || second.Audited || second.SkippedReason != "rung_already_audited" {
		t.Fatalf("second call was not skipped: result=%+v err=%v", second, err)
	}

	transport.reply = `{"items":[{"id":"A2","verdict":"contradicts","reason":"new item"}]}`
	secondRung := validInput(t, "R2", role)
	secondRung.FailedItemIDs = []string{"A1", "A2"}
	third, err := auditor.BeforeRepair(context.Background(), secondRung, nil)
	if err != nil || !third.Audited || len(third.Receipt.Items) != 1 || third.Receipt.Items[0].ID != "A2" {
		t.Fatalf("second rung did not audit only the new failed item: result=%+v err=%v", third, err)
	}
	if !strings.Contains(transport.prompt.User, "Failed acceptance items: A2") {
		t.Fatalf("previously audited item was sent again: %q", transport.prompt.User)
	}
	if transport.calls != 2 {
		t.Fatalf("transport calls = %d, want one per newly audited item set", transport.calls)
	}

	wrongRole := role
	wrongRole.Model = "gpt-6-luna"
	invalid := validInput(t, "R3", wrongRole)
	if _, err := auditor.BeforeRepair(context.Background(), invalid, nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("mismatched session role error = %v, want ErrInvalidInput", err)
	}
}

func TestNewRequiresResolvedAuditorRole(t *testing.T) {
	if _, err := New(contract.RoleManifest{Effective: map[contract.RoleName]contract.RoleSettings{}}, &fakeTransport{}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("New error = %v, want ErrInvalidManifest", err)
	}
}

func auditManifest() contract.RoleManifest {
	return contract.RoleManifest{Effective: map[contract.RoleName]contract.RoleSettings{
		"auditor": {Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high"},
	}}
}

func validInput(t *testing.T, rung string, role contract.RoleSettings) Input {
	t.Helper()
	return Input{
		Rung: rung, AcceptanceOnlyRed: true, FailedItemIDs: []string{"A1"},
		Request: []byte("verbatim request"), AcceptanceTestSource: []byte("acceptance source"),
		FailureOutput: []byte("assertion failed"), CandidateDiff: []byte("candidate diff"),
		Session: newAuditSession(t, role, rung),
	}
}

func newAuditSession(t *testing.T, role contract.RoleSettings, rung string) *session.Conversation {
	t.Helper()
	conversation, err := session.New(contract.ConversationIdentity{
		RunID: "run_example", CacheKey: "cache_example",
		ThreadID:  session.DeriveThreadID("run_example", ConversationStage, ConversationAttempt, rung, ConversationEpoch),
		SessionID: "session_example",
		Role:      "auditor", Stage: ConversationStage, Attempt: ConversationAttempt, Rung: rung,
		Epoch: ConversationEpoch, Provider: role.Provider, Model: role.Model, Effort: role.Effort,
	})
	if err != nil {
		t.Fatal(err)
	}
	return conversation
}

func hasWarning(warnings []Warning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}
