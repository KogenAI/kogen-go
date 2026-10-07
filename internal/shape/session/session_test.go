package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kogen-go/internal/contract"
	providersession "kogen-go/internal/provider/session"
	"kogen-go/internal/safefs"
)

func TestPrimaryPassExhaustionStartsFreshAliasedFallback(t *testing.T) {
	settings := []contract.RoleSettings{
		{Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high"},
		{Provider: "chatgpt", Model: "gpt-6-luna", Effort: "max"},
		{Provider: "grok", Model: "grok-4.6", Effort: "high"},
	}
	for _, role := range settings {
		t.Run(role.Provider+"-"+role.Model, func(t *testing.T) {
			factory := newFactory()
			run := newTestRun(t, role, factory, nil)
			initial := "Slug: item\n\nTask statement:\nraw request bytes\r\n\nWrite the Intent to intent.md and its acceptance test to test.exs."
			draftPath := filepath.Join(t.TempDir(), "intent.md")
			if err := os.WriteFile(draftPath, []byte("draft bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := run.StartPrimary(initial); err != nil {
				t.Fatal(err)
			}
			primary := run.Session()
			if primary == nil {
				t.Fatal("primary provider session was not created")
			}
			if err := run.SetLastFailure("candidate/intent_parse_failed: missing title"); err != nil {
				t.Fatal(err)
			}
			for pass := 1; pass <= MaxPassesPerConversation; pass++ {
				slot, err := run.BeginPass()
				if err != nil || slot != pass {
					t.Fatalf("primary pass slot = %d, %v; want %d", slot, err, pass)
				}
				if pass == 1 {
					for repair := 0; repair < MaxStyleRepairsPerConversation; repair++ {
						if err := run.BeginValidationTraversal(); err != nil {
							t.Fatal(err)
						}
						if allowed, err := run.RecordStyleTraversal(); err != nil || !allowed {
							t.Fatalf("primary style repair %d = %t, %v", repair+1, allowed, err)
						}
					}
					if err := run.BeginValidationTraversal(); err != nil {
						t.Fatal(err)
					}
					if allowed, err := run.RecordStyleTraversal(); err != nil || allowed {
						t.Fatalf("third primary style finding should warn: %t, %v", allowed, err)
					}
				} else if err := run.BeginValidationTraversal(); err != nil {
					t.Fatal(err)
				}
				if err := run.CountValidationPass(); err != nil {
					t.Fatal(err)
				}
				if err := run.RecordRepair(RepairCandidate); err != nil {
					t.Fatal(err)
				}
				if err := run.CompletePass(); err != nil {
					t.Fatal(err)
				}
			}

			_, err := run.BeginPass()
			var exhausted *BudgetExhaustedError
			if !errors.As(err, &exhausted) || exhausted.Conversation != 1 || exhausted.Counter != PassBudget {
				t.Fatalf("primary exhaustion = %#v, %v", exhausted, err)
			}
			if !run.FallbackStarted() {
				t.Fatal("primary pass exhaustion did not start fallback")
			}
			fallback := run.Session()
			if fallback == nil || fallback == primary {
				t.Fatal("fallback did not get a fresh persistent session")
			}
			primaryID, fallbackID := primary.Identity(), fallback.Identity()
			if primaryID.ThreadID == fallbackID.ThreadID || primaryID.RunID != fallbackID.RunID ||
				primaryID.CacheKey != fallbackID.CacheKey || primaryID.SessionID != fallbackID.SessionID {
				t.Fatalf("fallback did not preserve run affinity with a fresh thread: primary=%#v fallback=%#v", primaryID, fallbackID)
			}
			if fallbackID.Role != "fallback_shaper" || fallbackID.Provider != role.Provider ||
				fallbackID.Model != role.Model || fallbackID.Effort != role.Effort {
				t.Fatalf("fallback did not inherit effective shaper tuple: %#v, want %#v", fallbackID, role)
			}
			if run.LastFailure() != "candidate/intent_parse_failed: missing title" {
				t.Fatalf("last failure was discarded at fallback: %q", run.LastFailure())
			}
			if got := string(fallback.ProtocolSession().History[0].Raw); !strings.Contains(got, "raw request bytes") || !strings.Contains(got, "Last validation failure") || !strings.Contains(got, "missing title") {
				t.Fatalf("fresh fallback input did not preserve original request and failure: %s", got)
			}
			if slot, err := run.BeginPass(); err != nil || slot != 4 {
				t.Fatalf("first fallback pass slot = %d, %v; want 4", slot, err)
			}
			if err := run.BeginValidationTraversal(); err != nil {
				t.Fatal(err)
			}
			if allowed, err := run.RecordStyleTraversal(); err != nil || !allowed {
				t.Fatalf("fallback style allowance did not reset: %t, %v", allowed, err)
			}
			if len(factory.specs) != 2 || factory.specs[1].Settings != role || factory.specs[1].AssignedRole != "fallback_shaper" {
				t.Fatalf("fallback factory specs = %#v", factory.specs)
			}
			receipt, err := run.Accounting(OutcomeFailure)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.Conversations[0].Repairs[RepairStyle] != 2 || receipt.Conversations[1].Repairs[RepairStyle] != 1 {
				t.Fatalf("style repair counters did not remain per conversation: %#v", receipt.Conversations)
			}
			// Starting the fallback did not touch the shaper's draft files.
			if got, err := os.ReadFile(draftPath); err != nil || string(got) != "draft bytes" {
				t.Fatalf("draft changed around fallback: %q, %v", got, err)
			}
		})
	}
}

func TestTurnAllowanceResetsForFallbackAndFallbackExhaustionIsTerminal(t *testing.T) {
	factory := newFactory()
	role := contract.RoleSettings{Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high"}
	run := newTestRun(t, role, factory, nil)
	if err := run.StartPrimary("initial request"); err != nil {
		t.Fatal(err)
	}
	primary := run.Session()
	if _, err := run.BeginPass(); err != nil {
		t.Fatal(err)
	}
	for turn := 0; turn < MaxShaperTurnsPerConversation; turn++ {
		if err := run.DispatchShaperAttempt(AttemptFirst); err != nil {
			t.Fatalf("primary turn %d: %v", turn+1, err)
		}
		if err := run.CompleteShaperAttempt(nil); err != nil {
			t.Fatal(err)
		}
		if err := run.EndShaperTurn(); err != nil {
			t.Fatal(err)
		}
	}
	if err := run.DispatchShaperAttempt(AttemptFirst); !IsBudgetExhausted(err) {
		t.Fatalf("61st primary turn error = %v, want exhaustion", err)
	}
	if !run.FallbackStarted() || run.Session() == primary {
		t.Fatal("primary turn exhaustion did not create a fresh fallback session")
	}
	if slot, err := run.BeginPass(); err != nil || slot != 4 {
		t.Fatalf("first fallback pass slot = %d, %v; want 4", slot, err)
	}
	if err := run.DispatchShaperAttempt(AttemptFirst); err != nil {
		t.Fatalf("fallback first turn did not get a fresh allowance: %v", err)
	}
	if err := run.CompleteShaperAttempt(&contract.TokenUsage{}); err != nil {
		t.Fatal(err)
	}
	if err := run.EndShaperTurn(); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < MaxPassesPerConversation; pass++ {
		if pass > 0 {
			if _, err := run.BeginPass(); err != nil {
				t.Fatal(err)
			}
		}
		if err := run.BeginValidationTraversal(); err != nil {
			t.Fatal(err)
		}
		if err := run.CountValidationPass(); err != nil {
			t.Fatal(err)
		}
		if err := run.CompletePass(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := run.BeginPass(); !IsBudgetExhausted(err) {
		t.Fatalf("fallback pass exhaustion = %v", err)
	}
	if len(factory.specs) != 2 || !run.FallbackStarted() {
		t.Fatalf("fallback exhaustion started another conversation: %#v", factory.specs)
	}
	receipt, err := run.Accounting(OutcomeFailure)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Conversations[0].LogicalTurns != MaxShaperTurnsPerConversation || receipt.Conversations[1].LogicalTurns != 1 {
		t.Fatalf("turn counters did not reset per conversation: %#v", receipt.Conversations)
	}
	if receipt.Conversations[1].PassNumbers[0] != 4 || receipt.ValidationPasses != 3 {
		t.Fatalf("pass slots or actual pass count are wrong: %#v", receipt)
	}
}

func TestLastTurnAndLastPassCanSucceedWithoutFallback(t *testing.T) {
	t.Run("last turn", func(t *testing.T) {
		factory := newFactory()
		role := contract.RoleSettings{Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high"}
		run := newTestRun(t, role, factory, nil)
		if err := run.StartPrimary("initial request"); err != nil {
			t.Fatal(err)
		}
		if _, err := run.BeginPass(); err != nil {
			t.Fatal(err)
		}
		for turn := 0; turn < MaxShaperTurnsPerConversation; turn++ {
			if err := run.DispatchShaperAttempt(AttemptFirst); err != nil {
				t.Fatal(err)
			}
			if err := run.CompleteShaperAttempt(&contract.TokenUsage{}); err != nil {
				t.Fatal(err)
			}
			if err := run.EndShaperTurn(); err != nil {
				t.Fatal(err)
			}
		}
		if err := run.BeginValidationTraversal(); err != nil {
			t.Fatal(err)
		}
		if err := run.CountValidationPass(); err != nil {
			t.Fatal(err)
		}
		if err := run.CompletePass(); err != nil {
			t.Fatal(err)
		}
		if run.FallbackStarted() {
			t.Fatal("valid result on turn 60 incorrectly started fallback")
		}
	})

	t.Run("last pass", func(t *testing.T) {
		factory := newFactory()
		role := contract.RoleSettings{Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high"}
		run := newTestRun(t, role, factory, nil)
		if err := run.StartPrimary("initial request"); err != nil {
			t.Fatal(err)
		}
		for pass := 1; pass <= MaxPassesPerConversation; pass++ {
			slot, err := run.BeginPass()
			if err != nil || slot != pass {
				t.Fatalf("pass %d slot = %d, %v", pass, slot, err)
			}
			if err := run.DispatchShaperAttempt(AttemptFirst); err != nil {
				t.Fatal(err)
			}
			if err := run.CompleteShaperAttempt(&contract.TokenUsage{}); err != nil {
				t.Fatal(err)
			}
			if err := run.EndShaperTurn(); err != nil {
				t.Fatal(err)
			}
			if err := run.BeginValidationTraversal(); err != nil {
				t.Fatal(err)
			}
			if err := run.CountValidationPass(); err != nil {
				t.Fatal(err)
			}
			if pass < MaxPassesPerConversation {
				if err := run.SetLastFailure("candidate/intent_parse_failed: fixture"); err != nil {
					t.Fatal(err)
				}
				if err := run.RecordRepair(RepairCandidate); err != nil {
					t.Fatal(err)
				}
			}
			if err := run.CompletePass(); err != nil {
				t.Fatal(err)
			}
		}
		receipt, err := run.Accounting(OutcomeSuccess)
		if err != nil {
			t.Fatal(err)
		}
		if run.FallbackStarted() || receipt.ValidationPasses != 3 || receipt.Conversations[0].PassNumbers[2] != 3 {
			t.Fatalf("last allowed pass did not remain successful: %#v", receipt)
		}
	})
}

func TestStylesFinishGuardsRetriesContinuationsAndAuditsAreSeparate(t *testing.T) {
	factory := newFactory()
	role := contract.RoleSettings{Provider: "grok", Model: "grok-4.6", Effort: "high"}
	run := newTestRun(t, role, factory, nil)
	if err := run.StartPrimary("private request contents"); err != nil {
		t.Fatal(err)
	}
	if _, err := run.BeginPass(); err != nil {
		t.Fatal(err)
	}
	if err := run.DispatchShaperAttempt(AttemptFirst); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteShaperAttempt(nil); err != nil {
		t.Fatal(err)
	}
	if err := run.DispatchShaperAttempt(AttemptRetry); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteShaperAttempt(tokenUsage(10, 4)); err != nil {
		t.Fatal(err)
	}
	if err := run.DispatchShaperAttempt(AttemptContinuation); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteShaperAttempt(tokenUsage(5, 3)); err != nil {
		t.Fatal(err)
	}
	if err := run.EndShaperTurn(); err != nil {
		t.Fatal(err)
	}
	if err := run.RecordFinishGuard(); err != nil {
		t.Fatal(err)
	}

	conversation := run.Session()
	response := contract.ProviderResponse{
		RawItems: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"raw-response"}`)},
		Headers:  http.Header{"X-Codex-Turn-State": {"opaque-routing-value"}},
	}
	if err := conversation.ApplyResponse(response); err != nil {
		t.Fatal(err)
	}
	if err := conversation.AppendControllerMessage("append-only repair note"); err != nil {
		t.Fatal(err)
	}
	if err := conversation.AppendToolOutput("call-1", "tool output bytes"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := run.DispatchShaperAttempt(AttemptFirst); err != nil {
			t.Fatal(err)
		}
		if err := run.CompleteShaperAttempt(tokenUsage(int64(20+i), 7)); err != nil {
			t.Fatal(err)
		}
		if err := run.EndShaperTurn(); err != nil {
			t.Fatal(err)
		}
		if err := run.BeginValidationTraversal(); err != nil {
			t.Fatal(err)
		}
		repair, err := run.RecordStyleTraversal()
		if err != nil {
			t.Fatal(err)
		}
		if repair != (i < MaxStyleRepairsPerConversation) {
			t.Fatalf("style repair %d allowed = %t", i+1, repair)
		}
	}
	// The third style finding was left as a warning, so its traversal is still
	// active and becomes the one counted validation pass.
	if err := run.CountValidationPass(); err != nil {
		t.Fatal(err)
	}
	if err := run.SetLastFailure("candidate/coverage_gap: simultaneous coverage and audit feedback"); err != nil {
		t.Fatal(err)
	}
	if err := run.RecordRepair(RepairCoverageAndAudit); err != nil {
		t.Fatal(err)
	}
	if err := run.RecordRepair(RepairTestAudit); err == nil {
		t.Fatal("separate simultaneous audit repairs should have been combined")
	}
	if err := run.DispatchAuditorAttempt(AttemptFirst); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteAuditorAttempt(nil); err != nil {
		t.Fatal(err)
	}
	if err := run.DispatchAuditorAttempt(AttemptContinuation); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteAuditorAttempt(tokenUsage(2, 1)); err != nil {
		t.Fatal(err)
	}
	if err := run.EndAuditorTurn(); err != nil {
		t.Fatal(err)
	}
	if err := run.CompletePass(); err != nil {
		t.Fatal(err)
	}

	if conversation != run.Session() {
		t.Fatal("normal shaper turn replaced the persistent provider session")
	}
	protocol := conversation.ProtocolSession()
	if len(protocol.History) != 4 || !protocol.Routing.HasCodexTurnState || string(protocol.Routing.CodexTurnState) != "opaque-routing-value" {
		t.Fatalf("raw history or sticky routing state was lost: %#v", conversation.Snapshot())
	}
	if string(protocol.History[1].Raw) != `{"type":"reasoning","encrypted_content":"raw-response"}` ||
		!strings.Contains(string(protocol.History[2].Raw), "append-only repair note") ||
		!strings.Contains(string(protocol.History[3].Raw), "tool output bytes") {
		t.Fatalf("response/controller/tool items were not appended in order: %#v", protocol.History)
	}
	receipt, err := run.Accounting(OutcomeFailure)
	if err != nil {
		t.Fatal(err)
	}
	primary := receipt.Conversations[0]
	if primary.LogicalTurns != 4 || primary.HTTPAttempts != 6 || primary.Continuations != 1 {
		t.Fatalf("shaper logical turns, attempts, or continuations incorrect: %#v", primary)
	}
	if primary.ValidationPasses != 1 || primary.ValidationTraversals != 3 || primary.FinishGuards != 1 ||
		primary.Repairs[RepairStyle] != 2 || primary.StyleWarnings != 1 || primary.Repairs[RepairCoverageAndAudit] != 1 {
		t.Fatalf("pass, style, guard or repair accounting incorrect: %#v", primary)
	}
	if receipt.Roles[2].LogicalTurns != 1 || receipt.Roles[2].HTTPAttempts != 2 || receipt.Roles[2].Continuations != 1 {
		t.Fatalf("auditor requests were not kept separate: %#v", receipt.Roles[2])
	}
	if receipt.UnknownUsageAttempts != 2 || receipt.Tokens.Input == nil || *receipt.Tokens.Input != 80 ||
		receipt.Tokens.Output == nil || *receipt.Tokens.Output != 29 ||
		primary.Tokens.Input == nil || *primary.Tokens.Input != 78 || primary.Tokens.Output == nil || *primary.Tokens.Output != 28 {
		t.Fatalf("known and unknown usage totals incorrect: %#v", receipt)
	}
	if strings.Contains(fmt.Sprintf("%+v", receipt), "private request contents") || strings.Contains(fmt.Sprintf("%+v", receipt), "opaque-routing-value") {
		t.Fatalf("receipt leaked prompt or routing state: %+v", receipt)
	}
}

func TestProviderErrorDoesNotTriggerFallback(t *testing.T) {
	factory := newFactory()
	role := contract.RoleSettings{Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high"}
	run := newTestRun(t, role, factory, nil)
	if err := run.StartPrimary("request"); err != nil {
		t.Fatal(err)
	}
	if _, err := run.BeginPass(); err != nil {
		t.Fatal(err)
	}
	if err := run.DispatchShaperAttempt(AttemptFirst); err != nil {
		t.Fatal(err)
	}
	// A nil usage row represents an observed provider error/partial attempt.
	if err := run.CompleteShaperAttempt(nil); err != nil {
		t.Fatal(err)
	}
	if err := run.EndShaperTurn(); err != nil {
		t.Fatal(err)
	}
	if run.FallbackStarted() || len(factory.specs) != 1 {
		t.Fatalf("provider failure started fallback: %#v", factory.specs)
	}
}

func TestConversationFactoryEnvironmentErrorDoesNotTriggerFallback(t *testing.T) {
	factory := newFactory()
	factory.errAt = 1
	role := contract.RoleSettings{Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high"}
	run := newTestRun(t, role, factory, nil)
	if err := run.StartPrimary("request"); err == nil {
		t.Fatal("primary conversation factory failure was ignored")
	}
	if run.FallbackStarted() || len(factory.specs) != 1 {
		t.Fatalf("environment setup error started fallback: %#v", factory.specs)
	}
	receipt, err := run.Accounting(OutcomeFailure)
	if err != nil || len(receipt.Conversations) != 1 || receipt.Conversations[0].ConversationID != "" {
		t.Fatalf("failed primary setup was not accounted: %#v, %v", receipt, err)
	}
}

func TestPublishAccountingUsesRootedPrivateReplacement(t *testing.T) {
	factory := newFactory()
	role := contract.RoleSettings{Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high"}
	var now = time.Date(2026, time.October, 8, 0, 0, 0, 0, time.UTC)
	run := newTestRun(t, role, factory, func() time.Time { return now })
	if err := run.StartPrimary("secret prompt bytes"); err != nil {
		t.Fatal(err)
	}
	rootPath := t.TempDir()
	victim := filepath.Join(rootPath, "victim")
	if err := os.WriteFile(victim, []byte("must survive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("victim", filepath.Join(rootPath, AccountingFileName)); err != nil {
		t.Fatal(err)
	}
	root, err := safefs.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	now = now.Add(1250 * time.Millisecond)
	if err := run.PublishAccounting(root, OutcomeSuccess); err != nil {
		t.Fatal(err)
	}
	contents, err := root.ReadFile(AccountingFileName)
	if err != nil {
		t.Fatal(err)
	}
	var receipt Receipt
	if err := json.Unmarshal(contents, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Schema != 1 || receipt.Profile != ProfileV13 || receipt.Outcome != OutcomeSuccess || receipt.ElapsedMS != 1250 {
		t.Fatalf("published receipt metadata = %#v", receipt)
	}
	if strings.Contains(string(contents), "secret prompt bytes") || strings.Contains(string(contents), "opaque-routing") {
		t.Fatalf("published receipt leaked private conversation material: %s", contents)
	}
	info, err := os.Lstat(filepath.Join(rootPath, AccountingFileName))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("accounting publication mode/type = %v, %v", info, err)
	}
	if got, err := os.ReadFile(victim); err != nil || string(got) != "must survive" {
		t.Fatalf("publication followed symlink victim: %q, %v", got, err)
	}
	if err := run.PublishAccounting(root, OutcomeFailure); err != nil {
		t.Fatal(err)
	}
	contents, err = root.ReadFile(AccountingFileName)
	if err != nil || json.Unmarshal(contents, &receipt) != nil || receipt.Outcome != OutcomeFailure {
		t.Fatalf("failure receipt was not published: %s, %v", contents, err)
	}
}

type testFactory struct {
	specs []ConversationSpec
	errAt int
}

func newFactory() *testFactory { return &testFactory{} }

func (f *testFactory) create(spec ConversationSpec) (*providersession.Conversation, error) {
	f.specs = append(f.specs, spec)
	if f.errAt == spec.Index {
		return nil, errors.New("factory failure")
	}
	identity, err := providersession.Bind(providersession.Binding{
		RunID: "run_shape_fixture", CacheKey: "cache_shape_fixture", Role: spec.AssignedRole,
		Provider: spec.Settings.Provider, Model: spec.Settings.Model, Effort: spec.Settings.Effort,
		Stage: "shape", Attempt: fmt.Sprintf("conversation-%d", spec.Index), Rung: "shape", Epoch: "initial",
	})
	if err != nil {
		return nil, err
	}
	return providersession.New(identity)
}

func newTestRun(t *testing.T, shaper contract.RoleSettings, factory *testFactory, now func() time.Time) *Run {
	t.Helper()
	if factory == nil {
		factory = newFactory()
	}
	if now == nil {
		now = func() time.Time { return time.Date(2026, time.October, 8, 0, 0, 0, 0, time.UTC) }
	}
	manifest := contract.RoleManifest{
		Effective: map[contract.RoleName]contract.RoleSettings{
			"shaper":  shaper,
			"auditor": {Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high"},
		},
		FallbackShaper: shaper,
	}
	run, err := New(Options{
		Manifest:  manifest,
		Factory:   factory.create,
		StartedAt: now(),
		Now:       now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func tokenUsage(input, output int64) *contract.TokenUsage {
	return &contract.TokenUsage{Input: &input, Output: &output}
}
