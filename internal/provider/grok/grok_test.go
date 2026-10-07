package grok

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	authgrok "kogen-go/internal/auth/grok"
	"kogen-go/internal/auth/vault"
	"kogen-go/internal/contract"
	"kogen-go/internal/provider/retry"
	"kogen-go/internal/provider/session"
	"kogen-go/internal/provider/transport"
	"kogen-go/internal/provider/wire"
)

func TestOverloadRetriesKeepGrokModelAndAppendFullWireHistory(t *testing.T) {
	type capturedRequest struct {
		headers http.Header
		body    []byte
	}
	var captured []capturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured = append(captured, capturedRequest{headers: r.Header.Clone(), body: body})
		if len(captured) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"server_is_overloaded"}`))
			return
		}
		writeCompleted(w, len(captured))
	}))
	defer server.Close()

	adapter, store := newTestAdapter(t, server, "work", "work-access", time.Now().Add(time.Hour))
	defer store.Close()
	conversation := newConversation(t, "primary", "grok", "cache_shape_run")
	if err := conversation.AppendUser("first raw request"); err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: time.Now()}
	result, err := adapter.Respond(context.Background(), Request{
		Conversation: conversation, Prefix: wire.DefaultPrefix(),
		RoleInstructions: "You are Kogen's Grok shaper.", Mode: retry.ModeShape,
		Clock: clock, Jitter: zeroJitter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(captured) != 2 || len(result.Attempts) != 2 {
		t.Fatalf("provider requests=%d evidence=%d, want two", len(captured), len(result.Attempts))
	}
	if len(result.Retries) != 1 || result.Retries[0].Kind != "provider_retry" || result.Retries[0].Reason != "provider/overload" {
		t.Fatalf("overload events = %#v", result.Retries)
	}
	if !bytesEqual(captured[0].body, captured[1].body) {
		t.Fatal("identical overload retry changed request body bytes")
	}
	ids := make([]string, len(captured))
	for index, request := range captured {
		ids[index] = request.headers.Get("X-Grok-Req-Id")
		if !isUUIDv4(ids[index]) {
			t.Errorf("request %d UUID = %q", index+1, ids[index])
		}
		if request.headers.Get("Authorization") != "Bearer work-access" {
			t.Errorf("request %d used wrong selected account", index+1)
		}
		if request.headers.Get("X-Grok-Model-Override") != "grok-4.6" || request.headers.Get("X-Grok-Client-Mode") != "headless" || request.headers.Get("X-Xai-Token-Auth") != "xai-grok-cli" || request.headers.Get("X-AuthenticateResponse") != "authenticate-response" {
			t.Errorf("request %d is missing Grok proxy headers: %v", index+1, request.headers)
		}
		if request.headers.Get("X-Grok-Conv-Id") != "cache_shape_run" || request.headers.Get("X-Grok-Session-Id") != "cache_shape_run" {
			t.Errorf("request %d did not retain cache affinity", index+1)
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(request.body, &body); err != nil {
			t.Fatal(err)
		}
		var model string
		var reasoning map[string]string
		var instructions string
		var tools []json.RawMessage
		var input []json.RawMessage
		_ = json.Unmarshal(body["model"], &model)
		_ = json.Unmarshal(body["reasoning"], &reasoning)
		_ = json.Unmarshal(body["instructions"], &instructions)
		_ = json.Unmarshal(body["tools"], &tools)
		_ = json.Unmarshal(body["input"], &input)
		if model != "grok-4.6" || reasoning["effort"] != "high" {
			t.Errorf("request %d switched the Grok model/effort: %s %#v", index+1, model, reasoning)
		}
		if len(tools) != 7 || instructions != wire.DefaultGenericInstructions || len(input) < 2 || !strings.Contains(string(input[0]), "Grok shaper") {
			t.Errorf("request %d omitted the full static prefix or role instructions", index+1)
		}
		if _, exists := body["previous_response_id"]; exists {
			t.Errorf("request %d used provider-side response state", index+1)
		}
	}
	if ids[0] == ids[1] {
		t.Fatal("each HTTP attempt must have a fresh x-grok-req-id")
	}
	if result.Response == nil || result.Response.Text != "grok reply" {
		t.Fatalf("assembled response = %#v", result.Response)
	}
	if result.Response.Usage.Input == nil || *result.Response.Usage.Input != 75 || result.Response.Usage.CachedInput == nil || *result.Response.Usage.CachedInput != 25 || result.Response.Usage.Output == nil || *result.Response.Usage.Output != 7 || result.Response.Usage.Reasoning == nil || *result.Response.Usage.Reasoning != 2 {
		t.Fatalf("usage was not converted with cached input separated: %#v", result.Response.Usage)
	}
	if len(conversation.Attempts()) != 2 || conversation.Attempts()[0].Usage != nil || conversation.Attempts()[1].Usage == nil {
		t.Fatalf("per-attempt nullable usage = %#v", conversation.Attempts())
	}
	if result.Attempts[0].EndpointPath != "/v1/responses" || result.Attempts[0].Provider != "grok" || result.Attempts[1].Usage == nil || len(result.Attempts[1].RoutingHeaderNames) == 0 {
		t.Fatalf("request telemetry is incomplete: %#v", result.Attempts)
	}

	firstBody := append([]byte(nil), captured[1].body...)
	if err := conversation.AppendUser("second turn"); err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Respond(context.Background(), Request{
		Conversation: conversation, Prefix: wire.DefaultPrefix(),
		RoleInstructions: "You are Kogen's Grok shaper.", Mode: retry.ModeShape,
		Clock: clock, Jitter: zeroJitter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	secondBody := captured[2].body
	if !strings.HasSuffix(string(firstBody), "]}") || !strings.HasPrefix(string(secondBody), string(firstBody[:len(firstBody)-2])) || secondBody[len(firstBody)-2] != ',' {
		t.Fatal("appended turn did not preserve the complete earlier Grok body as a byte prefix")
	}
}

func TestUnauthorizedRefreshReplaysOnceWithFreshAttemptUUID(t *testing.T) {
	var providerCalls, refreshCalls int
	var authHeaders, requestIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			refreshCalls++
			_, _ = w.Write([]byte(`{"access_token":"new-access","expires_in":3600}`))
		case "/v1/responses":
			providerCalls++
			authHeaders = append(authHeaders, r.Header.Get("Authorization"))
			requestIDs = append(requestIDs, r.Header.Get("X-Grok-Req-Id"))
			if providerCalls == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}
			writeCompleted(w, providerCalls)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	home := t.TempDir()
	store := openGrokStore(t, home)
	if err := store.PutGrok("work", vault.GrokCredential{
		AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix(),
		ClientID: "grok-client", TokenEndpoint: server.URL + "/token",
	}); err != nil {
		t.Fatal(err)
	}
	manager, err := authgrok.New(authgrok.Options{Home: home, Label: "work", IssuerURL: server.URL, AllowLocalHTTP: true, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	transportClient, err := transport.New(server.Client(), transport.Deadlines{
		FirstByte: time.Second, Idle: time.Second, Total: 3 * time.Second, MaxBodyBytes: transport.DefaultMaxResponseBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(Config{Auth: manager, Transport: transportClient, Endpoint: server.URL + "/v1/responses", ClientVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	conversation := newConversation(t, "primary", "grok", "cache_auth_run")
	if err := conversation.AppendUser("refresh after 401"); err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Respond(context.Background(), Request{
		Conversation: conversation, Prefix: wire.DefaultPrefix(), Mode: retry.ModeShape,
		Clock: &testClock{now: time.Now()}, Jitter: zeroJitter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if providerCalls != 2 || refreshCalls != 1 {
		t.Fatalf("provider calls=%d refresh calls=%d, want 2 and 1", providerCalls, refreshCalls)
	}
	if authHeaders[0] != "Bearer old-access" || authHeaders[1] != "Bearer new-access" || requestIDs[0] == requestIDs[1] || !isUUIDv4(requestIDs[0]) || !isUUIDv4(requestIDs[1]) {
		t.Fatalf("401 replay auth/request ids = %#v %#v", authHeaders, requestIDs)
	}
	if result.Response == nil || len(result.Attempts) != 2 || len(result.Retries) != 0 {
		t.Fatalf("401 replay result = %#v", result)
	}
}

func TestPartialGrokStreamContinuesInTheSameThreadAndAppendsRawHistory(t *testing.T) {
	var bodies [][]byte
	var requestIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		requestIDs = append(requestIDs, r.Header.Get("X-Grok-Req-Id"))
		if len(bodies) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"id\":\"partial-message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"partial \"}]}}\n\n"))
			return
		}
		writeCompleted(w, len(bodies))
	}))
	defer server.Close()
	adapter, store := newTestAdapter(t, server, "work", "work-access", time.Now().Add(time.Hour))
	defer store.Close()
	conversation := newConversation(t, "primary", "grok", "cache_partial_run")
	if err := conversation.AppendUser("resume the response"); err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Respond(context.Background(), Request{
		Conversation: conversation, Prefix: wire.DefaultPrefix(), Mode: retry.ModeShape,
		Clock: &testClock{now: time.Now()}, Jitter: zeroJitter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || len(result.Attempts) != 2 || !result.Resumed {
		t.Fatalf("partial stream attempts/evidence/resumed = %d/%d/%t", len(bodies), len(result.Attempts), result.Resumed)
	}
	if !strings.HasSuffix(string(bodies[0]), "]}") || !strings.HasPrefix(string(bodies[1]), string(bodies[0][:len(bodies[0])-2])) || bodies[1][len(bodies[0])-2] != ',' {
		t.Fatal("continuation body did not append to the exact prior body prefix")
	}
	if requestIDs[0] == requestIDs[1] || !isUUIDv4(requestIDs[0]) || !isUUIDv4(requestIDs[1]) {
		t.Fatalf("continuation request ids = %#v", requestIDs)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(bodies[1], &body); err != nil {
		t.Fatal(err)
	}
	var input []json.RawMessage
	if err := json.Unmarshal(body["input"], &input); err != nil {
		t.Fatal(err)
	}
	if len(input) < 3 || !strings.Contains(string(input[len(input)-2]), "partial-message") || !strings.Contains(string(input[len(input)-1]), session.ContinuationInstruction) {
		t.Fatalf("continuation did not retain raw partial output and appended instruction: %s", body["input"])
	}
	if result.Response == nil || result.Response.Text != "partial grok reply" {
		t.Fatalf("continued text = %#v", result.Response)
	}
	if len(conversation.Attempts()) != 2 || conversation.Attempts()[0].Usage != nil {
		t.Fatalf("partial attempt usage was not retained as unknown: %#v", conversation.Attempts())
	}
}

func TestGrokShapeFallbackRetainsResolvedProviderModelAndAccount(t *testing.T) {
	var authHeaders []string
	var cacheHeaders, modelHeaders, threadIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		cacheHeaders = append(cacheHeaders, r.Header.Get("X-Grok-Conv-Id"))
		modelHeaders = append(modelHeaders, r.Header.Get("X-Grok-Model-Override"))
		threadIDs = append(threadIDs, r.Header.Get("Thread-ID"))
		writeCompleted(w, len(authHeaders))
	}))
	defer server.Close()
	adapter, store := newTestAdapter(t, server, "selected-grok", "selected-access", time.Now().Add(time.Hour))
	defer store.Close()
	shaper := contract.RoleSettings{Provider: "grok", Model: "grok-4.6", Effort: "high"}
	manifest := contract.RoleManifest{Effective: map[contract.RoleName]contract.RoleSettings{"shaper": shaper}, FallbackShaper: shaper}
	resolved, err := retry.ResolveShapeFallback(manifest)
	if err != nil || resolved != shaper {
		t.Fatalf("effective Grok Shape fallback = %#v, %v", resolved, err)
	}
	manifest.FallbackShaper = contract.RoleSettings{Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high"}
	if _, err := retry.ResolveShapeFallback(manifest); err == nil {
		t.Fatal("cross-provider fallback_shaper override was accepted")
	}

	primary := newConversation(t, "primary", "grok", "cache_shape_fallback")
	fallback := newConversation(t, "fallback", "grok", "cache_shape_fallback")
	if primary.Identity().ThreadID == fallback.Identity().ThreadID || primary.Identity().CacheKey != fallback.Identity().CacheKey {
		t.Fatal("Shape fallback must be a fresh thread on the same run affinity")
	}
	for _, conversation := range []*session.Conversation{primary, fallback} {
		if _, err := adapter.Respond(context.Background(), Request{
			Conversation: conversation, Prefix: wire.DefaultPrefix(),
			RoleInstructions: "Effective Grok shaper instructions.", Mode: retry.ModeShape,
			Clock: &testClock{now: time.Now()}, Jitter: zeroJitter{},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(authHeaders) != 2 || authHeaders[0] != "Bearer selected-access" || authHeaders[1] != "Bearer selected-access" {
		t.Fatalf("fallback did not retain the selected Grok account: %#v", authHeaders)
	}
	if cacheHeaders[0] != cacheHeaders[1] || cacheHeaders[0] != "cache_shape_fallback" || modelHeaders[0] != "grok-4.6" || modelHeaders[1] != "grok-4.6" {
		t.Fatalf("fallback changed cache/provider model: cache=%v model=%v", cacheHeaders, modelHeaders)
	}
	if threadIDs[0] != "" || threadIDs[1] != "" {
		t.Fatal("Grok wire unexpectedly exposed a ChatGPT thread header")
	}
}

func TestGrokForbiddenModelAndAccountRefusals(t *testing.T) {
	var providerCalls, authCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			authCalls++
			_, _ = w.Write([]byte(`{"access_token":"should-not-refresh","expires_in":3600}`))
		case "/v1/responses":
			providerCalls++
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"model is not available"}`))
		}
	}))
	defer server.Close()
	adapter, store := newTestAdapter(t, server, "work", "work-access", time.Now().Add(time.Hour))
	defer store.Close()
	conversation := newConversation(t, "primary", "grok", "cache_refusal")
	_, err := adapter.Respond(context.Background(), Request{
		Conversation: conversation, Prefix: wire.DefaultPrefix(), Mode: retry.ModeShape,
		Clock: &testClock{now: time.Now()}, Jitter: zeroJitter{},
	})
	if err == nil || err.Error() != "This Grok account cannot access the requested model." {
		t.Fatalf("HTTP 403 refusal = %v", err)
	}
	if providerCalls != 1 || authCalls != 0 {
		t.Fatalf("403 must not trigger refresh/replay: provider=%d auth=%d", providerCalls, authCalls)
	}

	chatgptConversation := newConversation(t, "primary", "chatgpt", "cache_refusal")
	if _, err := adapter.Encode(Request{Conversation: chatgptConversation, Prefix: wire.DefaultPrefix()}, "token"); err == nil {
		t.Fatal("Grok adapter accepted a ChatGPT conversation identity")
	}
}

func newTestAdapter(t *testing.T, server *httptest.Server, label, access string, expiry time.Time) (*Adapter, *vault.Store) {
	t.Helper()
	home := t.TempDir()
	store := openGrokStore(t, home)
	if err := store.PutGrok(label, vault.GrokCredential{
		AccessToken: access, RefreshToken: "refresh-secret", ExpiresAt: expiry.Unix(),
		Scopes: []string{"grok-cli:access"}, ClientID: "grok-client", TokenEndpoint: server.URL + "/token",
	}); err != nil {
		t.Fatal(err)
	}
	manager, err := authgrok.New(authgrok.Options{Home: home, Label: label, IssuerURL: server.URL, AllowLocalHTTP: true, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	transportClient, err := transport.New(server.Client(), transport.Deadlines{
		FirstByte: time.Second, Idle: time.Second, Total: 3 * time.Second, MaxBodyBytes: transport.DefaultMaxResponseBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(Config{Auth: manager, Transport: transportClient, Endpoint: server.URL + "/v1/responses", ClientVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return adapter, store
}

func openGrokStore(t *testing.T, home string) *vault.Store {
	t.Helper()
	store, err := vault.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newConversation(t *testing.T, attempt, provider, cacheKey string) *session.Conversation {
	t.Helper()
	identity, err := session.Bind(session.Binding{
		RunID: "run_grok_component", CacheKey: cacheKey,
		Role: contract.RoleName("shaper"), Provider: provider, Model: "grok-4.6", Effort: "high",
		Stage: "shape", Attempt: attempt, Rung: "shape", Epoch: "initial",
	})
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := session.New(identity)
	if err != nil {
		t.Fatal(err)
	}
	return conversation
}

func writeCompleted(w http.ResponseWriter, call int) {
	_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"grok-%d\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"message-%d\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"grok reply\"}]}],\"usage\":{\"input_tokens\":100,\"output_tokens\":7,\"cache_write_tokens\":4,\"input_tokens_details\":{\"cached_tokens\":25},\"output_tokens_details\":{\"reasoning_tokens\":2}}}}\n\ndata: [DONE]\n\n", call, call)
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func isUUIDv4(value string) bool {
	matched, _ := regexp.MatchString(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, value)
	return matched
}

type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }
func (c *testClock) Sleep(_ context.Context, duration time.Duration) error {
	c.now = c.now.Add(duration)
	return nil
}

type zeroJitter struct{}

func (zeroJitter) Uint64n(upper uint64) (uint64, error) {
	if upper == 0 {
		return 0, fmt.Errorf("zero jitter bound")
	}
	return 0, nil
}
