package streamsession

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"kogen-go/internal/contract"
	"kogen-go/internal/provider/retry"
	providersession "kogen-go/internal/provider/session"
	"kogen-go/internal/provider/wire"
	"kogen-go/internal/xspec/protocol"
)

func TestStreamOverloadSwitchComesFromProductionRetryPort(t *testing.T) {
	stream := newStreamSlice()
	applyStream(t, stream, "Open", `{"role":"builder","model":"luna","fallbackOn":true,"refreshable":true,"bounded":true,"wall":100000,"mode":"build"}`)
	applyStream(t, stream, "Result", `{"kind":"overload","items":false}`)
	if stream.obs.Decision != "retry" || stream.obs.Attempt != 2 || stream.obs.Delay != 2_000 || stream.obs.Overloads != 1 {
		t.Fatalf("first overload observation = %+v", stream.obs)
	}
	applyStream(t, stream, "Result", `{"kind":"overload","items":false}`)
	if stream.obs.Decision != "switch" || stream.obs.Model != "sol" || stream.obs.Attempt != 3 || stream.obs.Delay != 0 || stream.obs.Overloads != 0 {
		t.Fatalf("fallback observation = %+v", stream.obs)
	}
	applyStream(t, stream, "Result", `{"kind":"ok","items":false}`)
	if stream.obs.Decision != "success" || stream.obs.Phase != streamPhaseIdle || stream.obs.Model != "sol" {
		t.Fatalf("success observation = %+v", stream.obs)
	}
}

func TestStreamAuthCapabilityAndBuildWaitComeFromProductionRetryPort(t *testing.T) {
	stream := newStreamSlice()
	applyStream(t, stream, "Open", `{"role":"builder","model":"luna","fallbackOn":true,"refreshable":true,"bounded":true,"wall":100000,"mode":"build"}`)
	applyStream(t, stream, "Result", `{"kind":"login","items":false}`)
	if stream.obs.Decision != "refresh" || !stream.obs.Refreshed || stream.obs.Phase != streamPhaseOpen {
		t.Fatalf("refreshable login observation = %+v", stream.obs)
	}
	applyStream(t, stream, "Result", `{"kind":"login","items":false}`)
	if stream.obs.Decision != "pause" || stream.obs.Waited != 300_000 || stream.obs.Phase != streamPhaseIdle || stream.obs.Exit != 0 {
		t.Fatalf("post-refresh Build login observation = %+v", stream.obs)
	}

	if err := stream.Reset(context.Background()); err != nil {
		t.Fatal(err)
	}
	applyStream(t, stream, "Open", `{"role":"builder","model":"luna","fallbackOn":true,"refreshable":false,"bounded":true,"wall":100000,"mode":"build"}`)
	applyStream(t, stream, "Result", `{"kind":"login","items":false}`)
	// The current production retry port pauses this Build failure even without
	// refresh capability. The migrated Quint scenario expects a terminal stop;
	// keep this adapter tied to production and report that divergence at replay.
	if stream.obs.Decision != "pause" || stream.obs.Refreshed || stream.obs.Waited != 300_000 || stream.obs.Exit != 0 || stream.obs.Reason != "provider/login" {
		t.Fatalf("non-refreshable login production observation = %+v", stream.obs)
	}
}

func TestStreamGrokAndWallBudgetCapabilitiesComeFromProductionRetryPort(t *testing.T) {
	stream := newStreamSlice()
	applyStream(t, stream, "Open", `{"role":"builder","model":"grok","fallbackOn":true,"refreshable":true,"bounded":true,"wall":100000,"mode":"build"}`)
	if stream.obs.FallbackOn {
		t.Fatalf("Grok fallback capability should be false: %+v", stream.obs)
	}
	applyStream(t, stream, "Result", `{"kind":"overload","items":false}`)
	applyStream(t, stream, "Result", `{"kind":"overload","items":false}`)
	if stream.obs.Decision != "retry" || stream.obs.Model != "grok" || stream.obs.Overloads != 2 {
		t.Fatalf("Grok overload observation = %+v", stream.obs)
	}

	if err := stream.Reset(context.Background()); err != nil {
		t.Fatal(err)
	}
	applyStream(t, stream, "Open", `{"role":"builder","model":"luna","fallbackOn":false,"refreshable":true,"bounded":true,"wall":1000,"mode":"build"}`)
	applyStream(t, stream, "Result", `{"kind":"overload","items":false}`)
	if stream.obs.Decision != "stop" || stream.obs.Phase != streamPhaseStopped || stream.obs.Attempt != 1 || stream.obs.Delay != 0 {
		t.Fatalf("unaffordable retry observation = %+v", stream.obs)
	}
}

func TestStreamCheckpointUsesProductionCheckpointAndSessionPorts(t *testing.T) {
	stream := newStreamSlice()
	applyStream(t, stream, "Open", `{"role":"builder","model":"luna","fallbackOn":true,"refreshable":true,"bounded":true,"wall":100000,"mode":"build"}`)
	applyStream(t, stream, "Checkpoint", `{"kind":"valid"}`)
	if stream.obs.Decision != "checkpoint" || stream.obs.Checkpoint != "accepted" || stream.obs.Continuations != 1 || stream.checkpointState == nil {
		t.Fatalf("accepted checkpoint observation = %+v", stream.obs)
	}

	applyStream(t, stream, "Open", `{"role":"builder","model":"luna","fallbackOn":true,"refreshable":true,"bounded":true,"wall":100000,"mode":"build"}`)
	applyStream(t, stream, "Checkpoint", `{"kind":"oversized"}`)
	if stream.obs.Decision != "stop" || stream.obs.Exit != 1 || stream.obs.Reason != "continuation_failed" || stream.obs.Checkpoint != "failed" {
		t.Fatalf("oversized checkpoint observation = %+v", stream.obs)
	}
}

func TestSessionUsesOneProductionConversationUntilIdentityBoundary(t *testing.T) {
	session := newSessionSlice()
	applySession(t, session, "Bind", `{"stage":"develop","attempt":"","rung":""}`)
	first := session.conversation
	firstIdentity := first.Identity()
	if session.obs.Version != "v2" || session.obs.Attempt != "builder" || session.obs.Rung != "builder" || session.obs.KeyChanged {
		t.Fatalf("initial bind observation = %+v", session.obs)
	}
	applySession(t, session, "Turn", "")
	applySession(t, session, "Repair", "")
	if session.conversation != first || session.conversation.Identity().ThreadID != firstIdentity.ThreadID || len(session.conversation.ProtocolSession().History) != 2 {
		t.Fatalf("turn/repair replaced or lost the persistent conversation: %s", session.conversation)
	}
	applySession(t, session, "Epoch", `{"name":"summarizer"}`)
	if session.conversation == first || session.conversation.Identity().ThreadID == firstIdentity.ThreadID ||
		session.conversation.Identity().CacheKey != firstIdentity.CacheKey || len(session.conversation.ProtocolSession().History) != 2 || !session.obs.KeyChanged {
		t.Fatalf("summarizer epoch did not create a new thread with copied history: obs=%+v session=%s", session.obs, session.conversation)
	}
	applySession(t, session, "Accept", `{"ok":true}`)
	if session.obs.Epoch != "digest" || !session.obs.KeyChanged || len(session.conversation.ProtocolSession().History) != 3 {
		t.Fatalf("accepted checkpoint did not create the compacted continuation: obs=%+v session=%s", session.obs, session.conversation)
	}
	beforeModel := session.conversation.Identity().ThreadID
	applySession(t, session, "Model", `{"name":"sol"}`)
	if session.conversation.Identity().ThreadID != beforeModel || session.obs.Model != "sol" || session.obs.KeyChanged {
		t.Fatalf("model change altered thread identity: obs=%+v", session.obs)
	}
	applySession(t, session, "Lite", "")
	if session.obs.Lite != "v1" || session.conversation.Identity().SessionID == session.conversation.Identity().CacheKey {
		t.Fatalf("Lite did not use its separate production session ID: obs=%+v", session.obs)
	}
}

func TestSessionSharedAffinityAndStaticPrefixUseProductionPorts(t *testing.T) {
	session := newSessionSlice()
	applySession(t, session, "AffinityScope", `{"shared":true}`)
	applySession(t, session, "Bind", `{"stage":"develop","attempt":"builder","rung":"builder"}`)
	cacheKey := session.conversation.Identity().CacheKey
	threadID := session.conversation.Identity().ThreadID
	applySession(t, session, "NewRun", `{"name":"run-2"}`)
	if session.obs.AffinityChanged || session.obs.RunName != "run-2" {
		t.Fatalf("shared affinity changed across runs: %+v", session.obs)
	}
	applySession(t, session, "Bind", `{"stage":"develop","attempt":"builder","rung":"builder"}`)
	if session.conversation.Identity().CacheKey != cacheKey || session.conversation.Identity().ThreadID == threadID {
		t.Fatalf("shared affinity did not preserve only the cache key: %s", session.conversation)
	}

	applySession(t, session, "Prefix", `{"provider":"chatgpt","model":"sol","adapter":"a1","prompt":"p1","bytes":"static"}`)
	applySession(t, session, "Prefix", `{"provider":"chatgpt","model":"sol","adapter":"a1","prompt":"p1","bytes":"static"}`)
	if session.obs.Last != "ok" || len(session.obs.Prefixes) != 1 {
		t.Fatalf("stable static prefix was not retained: %+v", session.obs)
	}
	applySession(t, session, "Prefix", `{"provider":"chatgpt","model":"sol","adapter":"a1","prompt":"p1","bytes":"timestamp-pollution"}`)
	if session.obs.Last != "static_prefix_changed" || len(session.obs.Prefixes) != 1 || session.obs.Prefixes[pythonTupleKey("chatgpt", "sol", "a1", "p1")] != "static" {
		t.Fatalf("changed static prefix was not refused atomically: %+v", session.obs)
	}
}

func TestRawWireAssertionsRemainIndependentOfReplayObservation(t *testing.T) {
	conversation := newWireConversation(t)
	if err := conversation.AppendUser("approved request"); err != nil {
		t.Fatal(err)
	}
	request := testWireRequest(conversation)
	first, err := wire.Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	retryBytes, err := wire.Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Request.Body, retryBytes.Request.Body) {
		t.Fatal("identical retry request bodies changed bytes")
	}
	identity := conversation.Identity()
	if first.Request.Headers.Get("session-id") != identity.CacheKey || first.Request.Headers.Get("thread-id") != identity.ThreadID {
		t.Fatalf("cache/thread headers do not match production identities: cache=%q thread=%q", first.Request.Headers.Get("session-id"), first.Request.Headers.Get("thread-id"))
	}

	if err := conversation.ApplyResponse(contract.ProviderResponse{
		RawItems: []json.RawMessage{json.RawMessage(`{"type":"message","content":[]}`)},
		Headers:  http.Header{"X-Codex-Turn-State": []string{"opaque-route"}},
		Evidence: contract.RequestEvidence{ThreadID: identity.ThreadID},
	}); err != nil {
		t.Fatal(err)
	}
	if err := conversation.AppendToolOutput("call-1", "result"); err != nil {
		t.Fatal(err)
	}
	if err := conversation.AppendControllerMessage("later feedback"); err != nil {
		t.Fatal(err)
	}
	second, err := wire.Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	firstWithoutClose := bytes.TrimSuffix(first.Request.Body, []byte("]}"))
	if !bytes.HasSuffix(first.Request.Body, []byte("]}")) || !bytes.HasPrefix(second.Request.Body, firstWithoutClose) ||
		len(second.Request.Body) <= len(firstWithoutClose) || second.Request.Body[len(firstWithoutClose)] != ',' {
		t.Fatal("B1 without its final ]} is not the byte prefix of B2 followed by a comma")
	}
	if bytes.Contains(second.Request.Body, []byte("previous_response_id")) || !bytes.Contains(second.Request.Body, []byte(`"store":false`)) {
		t.Fatalf("request violated stateless Responses controls: %s", second.Request.Body)
	}
	if state := conversation.RoutingState(); !state.HasCodexTurnState || string(state.CodexTurnState) != "opaque-route" {
		t.Fatalf("sticky routing state was not retained internally: %s", state)
	}
}

func TestRawRetryAttemptPreservesCompleteWireBody(t *testing.T) {
	conversation := newWireConversation(t)
	if err := conversation.AppendUser("retry request"); err != nil {
		t.Fatal(err)
	}
	encoded, err := wire.Encode(testWireRequest(conversation))
	if err != nil {
		t.Fatal(err)
	}
	initial := retry.Request{Provider: "chatgpt", Model: "gpt-6-luna", Effort: "max", Body: bytes.Clone(encoded.Request.Body), Headers: encoded.Request.Headers.Clone()}
	clock := newReplayClock()
	seen := make([][]byte, 0, 2)
	_, err = retry.Run(context.Background(), initial, retry.Config{
		Mode: retry.ModeBuild, Role: "builder",
		Current: contract.RoleSettings{Provider: "chatgpt", Model: "gpt-6-luna", Effort: "max"},
		Clock:   clock, Jitter: ceilingJitter{},
	}, nil, func(_ context.Context, request retry.Request, _ retry.AttemptMode) (retry.Outcome, error) {
		seen = append(seen, bytes.Clone(request.Body))
		if len(seen) == 1 {
			return retry.Outcome{}, &retry.Failure{Class: retry.ClassOverload}
		}
		return retry.Outcome{Value: "ok"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || !bytes.Equal(seen[0], encoded.Request.Body) || !bytes.Equal(seen[1], encoded.Request.Body) {
		t.Fatal("retry port changed complete request bytes on an identical resend")
	}
}

func TestModelSwitchRetainsRawHistoryAndDropsEncryptedReasoningOnWire(t *testing.T) {
	conversation := newWireConversation(t)
	if err := conversation.AppendInput(json.RawMessage(`{"type":"reasoning","encrypted_content":"ciphertext","summary":"retained"}`)); err != nil {
		t.Fatal(err)
	}
	request := testWireRequest(conversation)
	before, err := wire.Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(before.Request.Body), "ciphertext") {
		t.Fatal("fixture did not serialize prior-model encrypted reasoning")
	}
	if err := conversation.SwitchModel("gpt-6.1-sol", "medium"); err != nil {
		t.Fatal(err)
	}
	request.Model, request.Effort = "gpt-6.1-sol", "medium"
	after, err := wire.Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after.Request.Body), "ciphertext") || !strings.Contains(string(after.Request.Body), "summary") {
		t.Fatal("model switch did not remove only encrypted reasoning from the wire copy")
	}
	if !strings.Contains(string(conversation.ProtocolSession().History[0].Raw), "ciphertext") {
		t.Fatal("model switch mutated retained raw history")
	}
}

func newWireConversation(t *testing.T) *providersession.Conversation {
	t.Helper()
	identity, err := providersession.Bind(providersession.Binding{
		RunID: "run_xspec_wire", CacheKey: "cache_xspec_wire", Role: "builder",
		Provider: "chatgpt", Model: "gpt-6-luna", Effort: "max",
		Stage: "develop", Attempt: "builder", Rung: "builder", Epoch: "initial",
	})
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := providersession.New(identity)
	if err != nil {
		t.Fatal(err)
	}
	return conversation
}

func testWireRequest(conversation *providersession.Conversation) wire.Request {
	return wire.Request{
		Config:           wire.Config{Mode: wire.ModeInjected, UserAgentVersion: "xspec-test"},
		Credentials:      wire.Credentials{Kind: wire.CredentialInjected, AccessToken: "fake-token", AccountID: "fake-account"},
		Conversation:     conversation,
		Prefix:           wire.DefaultPrefix(),
		Model:            "gpt-6-luna",
		Effort:           "max",
		RoleInstructions: "builder role instructions",
		CallableTools:    []string{"shell"},
		ToolChoice:       "auto",
	}
}

func applyStream(t *testing.T, stream *streamSlice, tag, raw string) {
	t.Helper()
	event := protocol.Event{Tag: tag}
	if raw != "" {
		event.Value = json.RawMessage(raw)
		event.HasValue = true
	}
	if err := stream.Apply(context.Background(), event); err != nil {
		t.Fatalf("apply stream %s: %v", tag, err)
	}
}

func applySession(t *testing.T, replay *sessionSlice, tag, raw string) {
	t.Helper()
	event := protocol.Event{Tag: tag}
	if raw != "" {
		event.Value = json.RawMessage(raw)
		event.HasValue = true
	}
	if err := replay.Apply(context.Background(), event); err != nil {
		t.Fatalf("apply session %s: %v", tag, err)
	}
}
