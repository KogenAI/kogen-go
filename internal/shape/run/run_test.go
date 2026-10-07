package run

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"kogen-go/internal/contract"
	providersession "kogen-go/internal/provider/session"
	"kogen-go/internal/safefs"
	shapesession "kogen-go/internal/shape/session"
	"kogen-go/internal/shape/validate"
)

func TestExecutePublishesFailureAccountingForRawRequestError(t *testing.T) {
	scratch := t.TempDir()
	root, err := safefs.OpenRoot(scratch)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	checkout := t.TempDir()
	started := time.Now().Add(-2 * time.Second)
	now := started.Add(2500 * time.Millisecond)
	runner, err := New(Options{
		Checkout: checkout, Slug: "shape-task", Request: []byte(" \r\n\t"),
		Manifest: testManifest(), Factory: func(shapesession.ConversationSpec) (*providersession.Conversation, error) {
			return nil, errors.New("factory must not run for an empty request")
		},
		ScratchDir: scratch, ScratchRoot: root, StartedAt: started, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Execute(context.Background())
	if err == nil || FormatError(err) != "intent/request_unavailable: stdin: empty" {
		t.Fatalf("Execute error = %v, rendered %q", err, FormatError(err))
	}
	encoded, err := root.ReadFile(shapesession.AccountingFileName)
	if err != nil {
		t.Fatalf("failure accounting was not published: %v", err)
	}
	var receipt shapesession.Receipt
	if err := json.Unmarshal(encoded, &receipt); err != nil {
		t.Fatalf("decode accounting receipt: %v", err)
	}
	if receipt.Outcome != shapesession.OutcomeFailure || receipt.ElapsedMS != 2500 || receipt.ValidationPasses != 0 {
		t.Fatalf("failure receipt = %#v", receipt)
	}
}

func TestResultTextKeepsPublicShapeOutputAndWarningLayout(t *testing.T) {
	result := Result{
		IntentPath:     filepath.Join("/work", ".kogen", "intents", "shape-task", "intent.md"),
		AcceptancePath: filepath.Join("/work", ".kogen", "acceptance", "shape-task_test.go"),
		Rounds:         2,
		Warnings:       []validate.Warning{{Code: "feasibility_concern", ItemIDs: []string{}, Message: "review this concern"}},
		Calls: []ModelCall{{
			Settings: contract.RoleSettings{Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high"},
			Usage:    &contract.TokenUsage{Input: int64Pointer(20), CachedInput: int64Pointer(4), Output: int64Pointer(5), Reasoning: int64Pointer(2)},
			WallMS:   99,
		}},
		TranscriptPath: filepath.Join("/scratch", TranscriptFileName),
	}
	got := result.Text("shape-task")
	want := "Intent: /work/.kogen/intents/shape-task/intent.md\n" +
		"Acceptance test: /work/.kogen/acceptance/shape-task_test.go\n" +
		"Validated after 2 round(s).\n" +
		"Warnings\n" +
		"  - feasibility_concern: - — review this concern\n" +
		"shape gpt-6.1-sol/high input=20 cached=4 output=5 reasoning=2 wall_ms=99\n" +
		"Transcript: /scratch/transcript.jsonl\n" +
		"Next: kogen intent approve shape-task\n"
	if got != want {
		t.Fatalf("rendered Shape result:\n%s\nwant:\n%s", got, want)
	}
}

func TestFormatErrorIndentsDetails(t *testing.T) {
	err := &contract.Failure{
		Class: "candidate", Reason: "intent_parse_failed", Exit: 1,
		Message: "candidate/intent_parse_failed: first detail\nsecond detail",
	}
	if got, want := FormatError(err), "candidate/intent_parse_failed: first detail\n  second detail"; got != want {
		t.Fatalf("FormatError() = %q, want %q", got, want)
	}
}

func TestFormatErrorKeepsProviderRetryFollowup(t *testing.T) {
	err := &contract.Failure{
		Class: "provider", Reason: "overload", Exit: 4,
		Message: "provider/overload: provider remained overloaded",
	}
	want := "provider/overload: provider remained overloaded\n" +
		"shape/provider_failed: shaping stopped on a provider error after its retries; no Intent was written. Run kogen intent shape again, or write the Intent yourself."
	if got := FormatError(err); got != want {
		t.Fatalf("FormatError() = %q, want %q", got, want)
	}
}

func TestIncompleteProviderAttemptBecomesUnknownUsage(t *testing.T) {
	rootPath := t.TempDir()
	root, err := safefs.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	factory := func(spec shapesession.ConversationSpec) (*providersession.Conversation, error) {
		identity, err := providersession.Bind(providersession.Binding{
			RunID: "run_shape", CacheKey: "cache_shape", Role: spec.AssignedRole,
			Provider: spec.Settings.Provider, Model: spec.Settings.Model, Effort: spec.Settings.Effort,
			Stage: "shape", Attempt: string(spec.AssignedRole), Rung: "shape", Epoch: "initial",
		})
		if err != nil {
			return nil, err
		}
		return providersession.New(identity)
	}
	state, err := shapesession.New(shapesession.Options{Manifest: testManifest(), Factory: factory})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.StartPrimary("initial"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.BeginPass(); err != nil {
		t.Fatal(err)
	}
	meter := &attemptMeter{session: state, identity: state.Session().Identity()}
	if err := meter.Dispatch(shapesession.AttemptFirst); err != nil {
		t.Fatal(err)
	}
	if err := meter.RecordRequest(contract.RequestEvidence{
		EndpointHost: "provider.test", EndpointPath: "/v1/responses", BodyBytes: 42,
		CacheKey: "cache_shape", ThreadID: state.Session().Identity().ThreadID,
		Role: "shaper", Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high",
	}); err != nil {
		t.Fatal(err)
	}
	if err := meter.finish(); err != nil {
		t.Fatal(err)
	}
	if err := state.PublishAccounting(root, shapesession.OutcomeFailure); err != nil {
		t.Fatal(err)
	}
	encoded, err := root.ReadFile(shapesession.AccountingFileName)
	if err != nil {
		t.Fatal(err)
	}
	var receipt shapesession.Receipt
	if err := json.Unmarshal(encoded, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.UnknownUsageAttempts != 1 || receipt.Roles[0].HTTPAttempts != 1 || receipt.Roles[0].LogicalTurns != 1 {
		t.Fatalf("incomplete attempt was not preserved: %#v", receipt)
	}
}

func testManifest() contract.RoleManifest {
	shaper := contract.RoleSettings{Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high"}
	return contract.RoleManifest{Effective: map[contract.RoleName]contract.RoleSettings{
		"shaper":  shaper,
		"auditor": {Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high"},
	}, FallbackShaper: shaper}
}

func int64Pointer(value int64) *int64 { return &value }
