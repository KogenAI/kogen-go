package session

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"kogen-go/internal/contract"
)

func testIdentity(t *testing.T) contract.ConversationIdentity {
	t.Helper()
	identity, err := Bind(Binding{
		RunID: "run_fixture", CacheKey: "cache_fixture", Role: "builder",
		Provider: "chatgpt", Model: "gpt-6-luna", Effort: "max", Stage: "develop",
	})
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestBindKeepsRunAffinityAndDerivesDistinctStableThreads(t *testing.T) {
	identity := testIdentity(t)
	if identity.Attempt != "builder" || identity.Rung != "builder" || identity.Epoch != "initial" {
		t.Fatalf("defaults not applied: %#v", identity)
	}
	if identity.CacheKey != "cache_fixture" || identity.SessionID == identity.CacheKey || identity.ThreadID == identity.CacheKey {
		t.Fatalf("protocol identities are not distinct: %#v", identity)
	}
	otherRung, err := Bind(Binding{
		RunID: identity.RunID, CacheKey: identity.CacheKey, Role: identity.Role,
		Provider: identity.Provider, Model: "gpt-6.1-sol", Effort: "medium",
		Stage: identity.Stage, Attempt: identity.Attempt, Rung: "2", Epoch: identity.Epoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	if otherRung.CacheKey != identity.CacheKey || otherRung.ThreadID == identity.ThreadID {
		t.Fatalf("rung must change thread only: first=%#v second=%#v", identity, otherRung)
	}
	restarted := DeriveThreadID(identity.RunID, identity.Stage, identity.Attempt, identity.Rung, identity.Epoch)
	if restarted != identity.ThreadID {
		t.Fatalf("thread ID did not survive identity reconstruction: %q != %q", restarted, identity.ThreadID)
	}
}

func TestOpaqueIDsAreSafeAndDoNotExposePaths(t *testing.T) {
	id, err := NewRunAffinityKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "cache_") || strings.ContainsAny(id, "/\\:") {
		t.Fatalf("unsafe affinity ID: %q", id)
	}
	other, err := NewRunAffinityKey()
	if err != nil {
		t.Fatal(err)
	}
	if id == other {
		t.Fatal("independent invocations received the same affinity key")
	}
}

func TestConversationHistoryIsAppendOnlyAndSnapshotSurvivesRestart(t *testing.T) {
	conversation, err := New(testIdentity(t))
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(` {"type":"reasoning", "summary":[], "encrypted_content":"opaque"} `)
	if err := conversation.Append(contract.SessionResponseItem, raw); err != nil {
		t.Fatal(err)
	}
	raw[0] = '[' // the retained copy cannot be rewritten by the caller
	if err := conversation.AppendControllerMessage("keep this repair note"); err != nil {
		t.Fatal(err)
	}
	if err := conversation.AppendToolOutput("call-1", "exit 0"); err != nil {
		t.Fatal(err)
	}
	usage := &contract.TokenUsage{Input: ptr[int64](10), CachedInput: ptr[int64](3)}
	response := contract.ProviderResponse{Usage: usage}
	response.Headers = http.Header{"x-codex-turn-state": {"opaque-routing"}}
	if err := conversation.ApplyResponse(response); err != nil {
		t.Fatal(err)
	}
	conversation.AppendAttemptUsage(nil)
	usage.Input = ptr[int64](99) // rows are copied on append

	snapshot := conversation.Snapshot()
	restored, err := Restore(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.ProtocolSession().History) != 3 {
		t.Fatalf("raw history length changed: %d", len(restored.ProtocolSession().History))
	}
	if string(restored.ProtocolSession().History[0].Raw) != ` {"type":"reasoning", "summary":[], "encrypted_content":"opaque"} ` {
		t.Fatalf("raw response bytes were rewritten: %q", restored.ProtocolSession().History[0].Raw)
	}
	if got := restored.RoutingState(); !got.HasCodexTurnState || string(got.CodexTurnState) != "opaque-routing" {
		t.Fatalf("sticky routing state was not restored: %#v", got)
	}
	rows := restored.Attempts()
	if len(rows) != 2 || rows[0].Usage == nil || rows[0].Usage.Input == nil || *rows[0].Usage.Input != 10 || rows[1].Usage != nil {
		t.Fatalf("nullable usage rows not retained: %#v", rows)
	}
	if restored.ProtocolSession() == conversation.ProtocolSession() {
		t.Fatal("snapshot restore should create a distinct object")
	}
}

func TestApplyResponseKeepsStickyTurnStateWhenLaterReplyOmitsIt(t *testing.T) {
	conversation, err := New(testIdentity(t))
	if err != nil {
		t.Fatal(err)
	}
	first := contract.ProviderResponse{Headers: http.Header{"X-Codex-Turn-State": {"state-1"}}}
	if err := conversation.ApplyResponse(first); err != nil {
		t.Fatal(err)
	}
	if err := conversation.ApplyResponse(contract.ProviderResponse{}); err != nil {
		t.Fatal(err)
	}
	if got := conversation.RoutingState(); !got.HasCodexTurnState || string(got.CodexTurnState) != "state-1" {
		t.Fatalf("missing response header cleared sticky state: %#v", got)
	}
}

func TestApplyResponseRejectsAnotherThreadsTurnState(t *testing.T) {
	conversation, err := New(testIdentity(t))
	if err != nil {
		t.Fatal(err)
	}
	response := contract.ProviderResponse{Evidence: contract.RequestEvidence{ThreadID: "thread_other"}}
	response.Headers = http.Header{"x-codex-turn-state": {"must-not-cross-threads"}}
	if err := conversation.ApplyResponse(response); err == nil {
		t.Fatal("response from another thread was appended")
	}
	if got := conversation.RoutingState(); got.HasCodexTurnState {
		t.Fatalf("another thread's routing state was captured: %#v", got)
	}
}

func TestModelSwitchFiltersEncryptedReasoningOnlyFromWireCopy(t *testing.T) {
	conversation, err := New(testIdentity(t))
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"type":"reasoning","summary":[{"type":"summary_text","text":"summary"}],"encrypted_content":"secret"}`)
	if err := conversation.Append(contract.SessionResponseItem, raw); err != nil {
		t.Fatal(err)
	}
	if err := conversation.SwitchModel("gpt-6.1-sol", "medium"); err != nil {
		t.Fatal(err)
	}
	items, err := conversation.WireHistory()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(items[0]), "encrypted_content") || !strings.Contains(string(items[0]), "summary_text") {
		t.Fatalf("model switch did not keep summary and remove encrypted payload: %s", items[0])
	}
	if !strings.Contains(string(conversation.ProtocolSession().History[0].Raw), "encrypted_content") {
		t.Fatal("model switch rewrote append-only raw history")
	}
	if conversation.Snapshot().DropEncryptedBefore != 1 {
		t.Fatal("model switch marker was not persisted")
	}
	newModelItem := json.RawMessage(`{"type":"reasoning","encrypted_content":"new-model-payload"}`)
	if err := conversation.Append(contract.SessionResponseItem, newModelItem); err != nil {
		t.Fatal(err)
	}
	items, err = conversation.WireHistory()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(items[0]), "encrypted_content") || !strings.Contains(string(items[1]), "new-model-payload") {
		t.Fatalf("new-model reasoning was not preserved after a model switch: %#v", items)
	}
}

func TestConversationDiagnosticsDoNotPrintPromptOrRoutingValues(t *testing.T) {
	conversation, err := New(testIdentity(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AppendInput(json.RawMessage(`{"role":"user","content":"private prompt"}`)); err != nil {
		t.Fatal(err)
	}
	response := contract.ProviderResponse{Headers: http.Header{"x-codex-turn-state": {"private routing token"}}}
	if err := conversation.ApplyResponse(response); err != nil {
		t.Fatal(err)
	}
	for _, diagnostic := range []string{fmt.Sprintf("%+v", conversation), fmt.Sprintf("%#v", conversation.Snapshot())} {
		if strings.Contains(diagnostic, "private prompt") || strings.Contains(diagnostic, "private routing token") {
			t.Fatalf("conversation diagnostic leaked content: %s", diagnostic)
		}
	}
}

func TestContinuationAppendsRawItemsThenOneControllerMessage(t *testing.T) {
	conversation, err := New(testIdentity(t))
	if err != nil {
		t.Fatal(err)
	}
	partial := json.RawMessage(`{"type":"function_call","call_id":"c1","name":"shell","arguments":"{}"}`)
	if err := conversation.AppendContinuation([]json.RawMessage{partial}); err != nil {
		t.Fatal(err)
	}
	history := conversation.ProtocolSession().History
	if len(history) != 2 || history[0].Kind != contract.SessionResponseItem || history[1].Kind != contract.SessionControllerMessage {
		t.Fatalf("continuation order is wrong: %#v", history)
	}
	if !strings.Contains(string(history[1].Raw), ContinuationInstruction) {
		t.Fatalf("continuation instruction missing: %s", history[1].Raw)
	}
}

func ptr[T any](value T) *T { return &value }
