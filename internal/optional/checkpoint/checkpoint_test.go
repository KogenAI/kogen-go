package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"kogen-go/internal/contract"
	"kogen-go/internal/provider/session"
	"kogen-go/internal/provider/wire"
)

func TestContextBytesIsAnExplicitOptInThreshold(t *testing.T) {
	if err := ValidateContextBytes(0); err != nil {
		t.Fatalf("omitted threshold should keep checkpointing disabled: %v", err)
	}
	if err := ValidateContextBytes(MinimumContextBytes - 1); err == nil {
		t.Fatal("threshold below 16000 bytes was accepted")
	}
	if err := ValidateContextBytes(MinimumContextBytes); err != nil {
		t.Fatalf("minimum threshold rejected: %v", err)
	}
	if !ShouldCheckpoint(MinimumContextBytes, MinimumContextBytes) ||
		ShouldCheckpoint(MinimumContextBytes-1, MinimumContextBytes) ||
		ShouldCheckpoint(MinimumContextBytes*2, 0) {
		t.Fatal("checkpoint threshold decision did not respect explicit opt-in and byte boundary")
	}
}

func TestPrepareSummarizerUsesNewEpochSameAffinityNoToolsAndSharedPrefix(t *testing.T) {
	prefix := wire.DefaultPrefix()
	base, err := session.New(builderIdentity(t, "initial"))
	if err != nil {
		t.Fatal(err)
	}
	request := json.RawMessage(` {"role":"user","content":[{"type":"input_text","text":"approved request"}]} `)
	plan := json.RawMessage(`{"role":"user","content":[{"type":"input_text","text":"approved plan"}]}`)
	response := json.RawMessage(` {"type":"message","id":"response-1","content":[]} `)
	for _, item := range []struct {
		kind contract.SessionItemKind
		raw  json.RawMessage
	}{
		{contract.SessionInput, request},
		{contract.SessionInput, plan},
		{contract.SessionResponseItem, response},
	} {
		if err := base.Append(item.kind, item.raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := base.ApplyResponse(contract.ProviderResponse{
		Headers: http.Header{"X-Codex-Turn-State": {"belongs-to-old-thread"}},
	}); err != nil {
		t.Fatal(err)
	}

	summarizer, err := PrepareSummarizer(base, 7, prefix)
	if err != nil {
		t.Fatal(err)
	}
	identity := summarizer.Conversation.Identity()
	original := base.Identity()
	if identity.Epoch != "checkpoint-7" || identity.ThreadID == original.ThreadID {
		t.Fatalf("summarizer did not start a checkpoint epoch: %#v", identity)
	}
	if identity.RunID != original.RunID || identity.CacheKey != original.CacheKey ||
		identity.SessionID != original.SessionID || identity.Role != original.Role ||
		identity.Provider != original.Provider || identity.Model != original.Model ||
		identity.Effort != original.Effort || identity.Stage != original.Stage ||
		identity.Attempt != original.Attempt || identity.Rung != original.Rung {
		t.Fatalf("summarizer changed builder identity or run affinity: before=%#v after=%#v", original, identity)
	}
	if len(summarizer.CallableTools) != 0 || summarizer.ToolChoice != ToolChoiceNone {
		t.Fatalf("summarizer tools are not disabled: %#v", summarizer)
	}
	if summarizer.RoleInstructions != SummarizerInstructions || summarizer.Prefix.Digest() != prefix.Digest() {
		t.Fatal("summarizer changed role instructions or the shared immutable prefix")
	}
	if summarizer.Conversation.RoutingState().HasCodexTurnState {
		t.Fatal("routing state from the old thread crossed into the checkpoint epoch")
	}
	wantHistory := base.ProtocolSession().History
	gotHistory := summarizer.Conversation.ProtocolSession().History
	if len(gotHistory) != len(wantHistory) {
		t.Fatalf("summarizer history length = %d, want %d", len(gotHistory), len(wantHistory))
	}
	for index := range wantHistory {
		if gotHistory[index].Kind != wantHistory[index].Kind || string(gotHistory[index].Raw) != string(wantHistory[index].Raw) {
			t.Fatalf("summarizer rewrote history item %d: got=%s want=%s", index, gotHistory[index].Raw, wantHistory[index].Raw)
		}
	}

	encoded, err := wire.Encode(wire.Request{
		Config: wire.Config{
			Mode: wire.ModeInjected, EndpointOverride: "https://provider.invalid/v1/responses",
			UserAgentVersion: "test",
		},
		Credentials:  wire.Credentials{Kind: wire.CredentialInjected, AccessToken: "test-only", AccountID: "test-only"},
		Conversation: summarizer.Conversation, Prefix: summarizer.Prefix,
		Model: identity.Model, Effort: identity.Effort,
		RoleInstructions: summarizer.RoleInstructions,
		CallableTools:    summarizer.CallableTools,
		ToolChoice:       summarizer.ToolChoice,
	})
	if err != nil {
		t.Fatalf("encode local summarizer request: %v", err)
	}
	var body struct {
		PromptCacheKey string            `json:"prompt_cache_key"`
		ToolChoice     string            `json:"tool_choice"`
		Tools          []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(encoded.Request.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.ToolChoice != ToolChoiceNone || len(body.Tools) != 7 || body.PromptCacheKey != original.CacheKey {
		t.Fatalf("wire request lost no-tools/shared-prefix/run-affinity semantics: %#v", body)
	}
	if encoded.StaticPrefixSHA256 != prefix.Digest() {
		t.Fatal("summarizer request changed the shared static prefix digest")
	}
}

func TestBuildCheckpointBoundsCanonicalItemAndHashesItsEpoch(t *testing.T) {
	checkpoint, err := BuildCheckpoint("Preserve the pending change and failed check.", 16_000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(checkpoint.Text(), ContinuationPrefix) {
		t.Fatalf("checkpoint text lacks the required prefix: %q", checkpoint.Text())
	}
	item := checkpoint.Item()
	var message struct {
		Content []struct {
			Text string `json:"text"`
			Type string `json:"type"`
		} `json:"content"`
		Role string `json:"role"`
	}
	if err := json.Unmarshal(item, &message); err != nil {
		t.Fatal(err)
	}
	if message.Role != "user" || len(message.Content) != 1 ||
		message.Content[0].Type != "input_text" || message.Content[0].Text != checkpoint.Text() {
		t.Fatalf("checkpoint has an invalid input-item shape: %s", item)
	}
	digest := sha256.Sum256(item)
	if checkpoint.Epoch() != hex.EncodeToString(digest[:]) {
		t.Fatalf("epoch = %q, want canonical item SHA-256 %x", checkpoint.Epoch(), digest)
	}
	if _, err := BuildCheckpoint("Preserve the pending change and failed check.", len(item)-1); !errors.Is(err, ErrContinuationFailed) {
		t.Fatalf("oversized checkpoint error = %v, want terminal continuation failure", err)
	}
	if _, err := BuildCheckpoint(" \n\t ", 16_000); !errors.Is(err, ErrContinuationFailed) {
		t.Fatalf("empty checkpoint error = %v, want terminal continuation failure", err)
	}
	if _, err := BuildCheckpoint(string([]byte{0xff}), 16_000); !errors.Is(err, ErrContinuationFailed) {
		t.Fatalf("invalid UTF-8 checkpoint error = %v, want terminal continuation failure", err)
	}
}

func TestNewContinuationPreservesApprovedBytesAndStartsDigestEpoch(t *testing.T) {
	prefix := wire.DefaultPrefix()
	base := builderIdentity(t, "initial")
	checkpoint, err := BuildCheckpoint("Keep the unverified edit and the latest failure.", 16_000)
	if err != nil {
		t.Fatal(err)
	}
	approved := ApprovedInputs{
		Request: json.RawMessage(` { "role" : "user", "content" : [{"type":"input_text","text":"approved raw request"}] } `),
		Plan:    json.RawMessage(`{"role":"user","content":[{"type":"input_text","text":"approved raw plan"}]}`),
	}
	continued, err := NewContinuation(base, approved, checkpoint, prefix)
	if err != nil {
		t.Fatal(err)
	}
	identity := continued.Conversation.Identity()
	if identity.Epoch != checkpoint.Epoch() || identity.ThreadID == base.ThreadID ||
		identity.CacheKey != base.CacheKey || identity.SessionID != base.SessionID {
		t.Fatalf("continuation did not keep affinity and open the checkpoint digest epoch: %#v", identity)
	}
	if continued.Prefix.Digest() != prefix.Digest() {
		t.Fatal("continuation changed the shared static prefix")
	}
	history := continued.Conversation.ProtocolSession().History
	if len(history) != 3 {
		t.Fatalf("compacted history has %d items, want request, plan, and checkpoint", len(history))
	}
	for index, want := range []json.RawMessage{approved.Request, approved.Plan, checkpoint.Item()} {
		if history[index].Kind != contract.SessionInput || string(history[index].Raw) != string(want) {
			t.Fatalf("compacted history item %d was rewritten or reordered: got=%s want=%s", index, history[index].Raw, want)
		}
	}
	if continued.Conversation.RoutingState().HasCodexTurnState {
		t.Fatal("prior thread routing state crossed into the continuation")
	}
	if ContinuationEventName != "context_continuation" {
		t.Fatalf("status-counted journal event changed: %q", ContinuationEventName)
	}
	if _, err := NewContinuation(base, approved, Checkpoint{}, prefix); !errors.Is(err, ErrContinuationFailed) {
		t.Fatalf("invalid checkpoint error = %v, want terminal continuation failure", err)
	}
}

func builderIdentity(t *testing.T, epoch string) contract.ConversationIdentity {
	t.Helper()
	identity, err := session.Bind(session.Binding{
		RunID: "run_checkpoint_fixture", CacheKey: "cache_checkpoint_fixture",
		Role: "builder", Provider: "chatgpt", Model: "gpt-6-luna", Effort: "max",
		Stage: "develop", Attempt: "builder", Rung: "1", Epoch: epoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	return identity
}
