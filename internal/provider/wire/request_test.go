package wire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"kogen-go/internal/contract"
	"kogen-go/internal/provider/session"
)

func newTestConversation(t *testing.T, stage, attempt, rung string) *session.Conversation {
	t.Helper()
	identity, err := session.Bind(session.Binding{
		RunID: "run_wire_fixture", CacheKey: "cache_wire_fixture", Role: "builder",
		Provider: "chatgpt", Model: "gpt-6-luna", Effort: "max",
		Stage: stage, Attempt: attempt, Rung: rung,
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

func injectedRequest(conversation *session.Conversation) Request {
	return Request{
		Config:       Config{Mode: ModeInjected, UserAgentVersion: "0.1-test"},
		Credentials:  Credentials{Kind: CredentialInjected, AccessToken: "jwt.secret.signature", AccountID: "acct-private"},
		Conversation: conversation, Prefix: DefaultPrefix(), Model: "gpt-6-luna", Effort: "max",
		RoleInstructions: "You are Kogen's builder. Implement the approved Intent.",
		CallableTools:    []string{"shell", "finish", "tool_output"}, ToolChoice: "auto",
	}
}

func TestDefaultPrefixIsTheOrderedSevenSchemaUnion(t *testing.T) {
	prefix := DefaultPrefix()
	if prefix.Digest() == "" || len(prefix.ToolSchemas()) != 7 {
		t.Fatalf("invalid static prefix: digest=%q schemas=%d", prefix.Digest(), len(prefix.ToolSchemas()))
	}
	want := []string{"edit", "finish", "read", "search", "shell", "tool_output", "write"}
	for index, schema := range prefix.ToolSchemas() {
		var value struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(schema, &value); err != nil {
			t.Fatal(err)
		}
		if value.Name != want[index] {
			t.Fatalf("schema order is not canonical: got %q at %d, want %q", value.Name, index, want[index])
		}
	}
	copy := prefix.ToolSchemas()
	copy[0][0] = '['
	if prefix.ToolSchemas()[0][0] != '{' {
		t.Fatal("schema accessor exposed mutable prefix bytes")
	}
}

func TestPrefixRegistryRejectsStaticByteChangesWithoutVersionBump(t *testing.T) {
	registry := &PrefixRegistry{}
	prefix := DefaultPrefix()
	if err := registry.Register("chatgpt", prefix); err != nil {
		t.Fatal(err)
	}
	changed, err := NewPrefix(prefix.Version(), prefix.GenericInstructions()+" Added.", CanonicalToolSchemas())
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("chatgpt", changed); err == nil {
		t.Fatal("same version accepted different generic instruction bytes")
	}
	version := prefix.Version()
	version.Prompt += "-2"
	versioned, err := NewPrefix(version, prefix.GenericInstructions()+" Added.", CanonicalToolSchemas())
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("chatgpt", versioned); err != nil {
		t.Fatalf("a changed version should permit a changed static prefix: %v", err)
	}
}

func TestCanonicalJSONSortsNestedKeysAndPreservesIntegerPrecision(t *testing.T) {
	got, err := CanonicalJSON([]byte(`{"z":9007199254740993,"a":{"y":2,"x":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":{"x":1,"y":2},"z":9007199254740993}` {
		t.Fatalf("unexpected canonical JSON: %s", got)
	}
	if _, err := CanonicalJSON([]byte(`{} {}`)); err == nil {
		t.Fatal("multiple JSON values were accepted")
	}
}

func TestInjectedRequestHasCanonicalControlsAndByteStableAppendPrefix(t *testing.T) {
	conversation := newTestConversation(t, "develop", "builder", "builder")
	if err := conversation.AppendInput(json.RawMessage(`{"role":"user","content":[{"type":"input_text","text":"first request"}]}`)); err != nil {
		t.Fatal(err)
	}
	request := injectedRequest(conversation)
	first, err := Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	identity := conversation.Identity()
	if first.Request.Headers.Get("session-id") != identity.CacheKey || first.Request.Headers.Get("thread-id") != identity.ThreadID {
		t.Fatal("cache and thread headers do not identify the conversation separately")
	}
	if first.Request.Headers.Get("Authorization") != "Bearer jwt.secret.signature" {
		t.Fatal("authorization header missing")
	}
	if err := conversation.AppendControllerMessage("continue after the first response"); err != nil {
		t.Fatal(err)
	}
	second, err := Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(second.Request.Body, retry.Request.Body) {
		t.Fatal("identical retries changed complete request-body bytes")
	}
	if len(first.Request.Body) < 2 || !bytes.HasSuffix(first.Request.Body, []byte("]}")) {
		t.Fatalf("first request did not end in input array and object: %s", first.Request.Body)
	}
	withoutClose := first.Request.Body[:len(first.Request.Body)-2]
	if !bytes.HasPrefix(second.Request.Body, withoutClose) || second.Request.Body[len(withoutClose)] != ',' {
		t.Fatalf("appended request does not extend the exact body prefix\nfirst:  %s\nsecond: %s", first.Request.Body, second.Request.Body)
	}
	if last := strings.LastIndex(string(first.Request.Body), `"input":`); last < strings.LastIndex(string(first.Request.Body), `"parallel_tool_calls":`) {
		t.Fatal("input is not the final body field")
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(second.Request.Body, &body); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body["reasoning"], &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 1 || string(fields["effort"]) != `"max"` {
		t.Fatalf("Luna reasoning controls are wrong: %s", body["reasoning"])
	}
	if string(body["instructions"]) != `"`+DefaultGenericInstructions+`"` {
		t.Fatalf("shared generic instructions changed or were combined with role data: %s", body["instructions"])
	}
	var fullInput []json.RawMessage
	if err := json.Unmarshal(body["input"], &fullInput); err != nil {
		t.Fatal(err)
	}
	if len(fullInput) == 0 || !strings.Contains(string(fullInput[0]), "You are Kogen's builder.") {
		t.Fatalf("role instructions do not follow the shared prefix: %s", body["input"])
	}
	var tools []json.RawMessage
	if err := json.Unmarshal(body["tools"], &tools); err != nil {
		t.Fatal(err)
	}
	if len(tools) != 7 {
		t.Fatalf("role filtered the shared schema union: got %d schemas", len(tools))
	}
	var choice map[string]json.RawMessage
	if err := json.Unmarshal(body["tool_choice"], &choice); err != nil {
		t.Fatal(err)
	}
	var callable []json.RawMessage
	if err := json.Unmarshal(choice["tools"], &callable); err != nil {
		t.Fatal(err)
	}
	if len(callable) != 3 {
		t.Fatalf("tool_choice did not independently restrict calls: %d", len(callable))
	}
	for _, absent := range []string{"previous_response_id", "temperature"} {
		if bytes.Contains(first.Request.Body, []byte(`"`+absent+`"`)) {
			t.Fatalf("request unexpectedly includes %q", absent)
		}
	}
	for _, absent := range []string{"session_id", "conversation_id"} {
		if _, ok := body[absent]; ok {
			t.Fatalf("request unexpectedly includes top-level %q", absent)
		}
	}
}

func TestOwnedRequestKeepsFullSchemasInLeadingInputItem(t *testing.T) {
	conversation := newTestConversation(t, "plan", "planner", "planner")
	if err := conversation.AppendUser("plan the approved request"); err != nil {
		t.Fatal(err)
	}
	request := Request{
		Config:       Config{Mode: ModeOwned},
		Credentials:  Credentials{Kind: CredentialOwned, AccessToken: "owned-token"},
		Conversation: conversation, Prefix: DefaultPrefix(), Model: "gpt-6.1-sol", Effort: "high",
		RoleInstructions: "You are Kogen's planner.", ToolChoice: "auto",
	}
	encoded, err := Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	if encoded.Request.Headers.Get("originator") != "" || encoded.Request.Headers.Get("chatgpt-account-id") != "" {
		t.Fatal("owned request contains injected-only headers")
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(encoded.Request.Body, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["tools"]; ok {
		t.Fatal("owned Responses request has top-level tools")
	}
	if _, ok := body["include"]; ok {
		t.Fatal("owned Responses request has injected-only include")
	}
	var input []json.RawMessage
	if err := json.Unmarshal(body["input"], &input); err != nil {
		t.Fatal(err)
	}
	var additional map[string]json.RawMessage
	if err := json.Unmarshal(input[0], &additional); err != nil {
		t.Fatal(err)
	}
	var schemas []json.RawMessage
	if err := json.Unmarshal(additional["tools"], &schemas); err != nil || len(schemas) != 7 {
		t.Fatalf("owned additional_tools did not carry full schema union: %d, %v", len(schemas), err)
	}
	if string(body["tool_choice"]) != `"none"` {
		t.Fatalf("tool-less role did not disable calls: %s", body["tool_choice"])
	}
}

func TestLiteUsesStableStaticItemsAndDistinctConversationIDs(t *testing.T) {
	build := func(stage string) map[string]json.RawMessage {
		conversation := newTestConversation(t, stage, "shaper", stage)
		request := Request{
			Config:       Config{Mode: ModeLite},
			Credentials:  Credentials{Kind: CredentialInjected, AccessToken: "jwt", AccountID: "acct"},
			Conversation: conversation, Prefix: DefaultPrefix(), Model: "gpt-6-luna", Effort: "high",
			RoleInstructions: "You are shaping an Intent.", CallableTools: []string{"read", "search", "write"},
		}
		encoded, err := Encode(request)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(encoded.Request.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body["instructions"] == nil || string(body["instructions"]) != `""` || body["tools"] != nil {
			t.Fatalf("Lite request fields are wrong: %s", encoded.Request.Body)
		}
		if encoded.Request.Headers.Get("x-openai-internal-codex-responses-lite") != "true" {
			t.Fatal("Lite protocol header missing")
		}
		if encoded.Request.Headers.Get("session_id") != conversation.Identity().SessionID {
			t.Fatal("Lite session ID header is not the distinct run-level ID")
		}
		return body
	}
	first := build("shape-primary")
	second := build("shape-fallback")
	var firstInput, secondInput []json.RawMessage
	if err := json.Unmarshal(first["input"], &firstInput); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second["input"], &secondInput); err != nil {
		t.Fatal(err)
	}
	var firstTools, secondTools, firstShared, secondShared map[string]json.RawMessage
	_ = json.Unmarshal(firstInput[0], &firstTools)
	_ = json.Unmarshal(secondInput[0], &secondTools)
	_ = json.Unmarshal(firstInput[1], &firstShared)
	_ = json.Unmarshal(secondInput[1], &secondShared)
	if !bytes.Equal(firstTools["id"], secondTools["id"]) || !bytes.Equal(firstShared["id"], secondShared["id"]) {
		t.Fatal("static Lite IDs changed across independent conversations")
	}
	if bytes.Equal(firstInput[2], secondInput[2]) {
		t.Fatal("role instruction thread identity did not change")
	}
}

func TestGenerationCapAndLiteRefusalsPrecedeCredentialUse(t *testing.T) {
	conversation := newTestConversation(t, "develop", "builder", "builder")
	cap := uint64(64)
	request := Request{
		Config:       Config{Mode: ModeInjected, EndpointOverride: "http://127.0.0.1:8000/v1/responses"},
		Conversation: conversation, Prefix: DefaultPrefix(), Model: "gpt-6-luna", Effort: "max", GenerationTokens: &cap,
	}
	_, err := Encode(request)
	if err == nil || err.Error() != "Model-generation cap is unsupported on this endpoint/adapter." {
		t.Fatalf("endpoint cap refusal did not happen before credential validation: %v", err)
	}
	request.Config.Mode = ModeLite
	request.GenerationTokens = nil
	request.Model = "gpt-6.1-sol"
	_, err = Encode(request)
	if err == nil || err.Error() != "Unsupported adapter or model-generation cap for this backend." {
		t.Fatalf("invalid Lite model was not refused: %v", err)
	}
}

func TestCrossInvocationStaticPrefixIsStableWhileThreadsDiffer(t *testing.T) {
	one := newTestConversation(t, "shape", "primary", "primary")
	two := newTestConversation(t, "develop", "builder", "R1")
	first, err := Encode(injectedRequest(one))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Encode(injectedRequest(two))
	if err != nil {
		t.Fatal(err)
	}
	if first.StaticPrefixSHA256 != second.StaticPrefixSHA256 {
		t.Fatalf("matching versions produced different static prefix digests: %s %s", first.StaticPrefixSHA256, second.StaticPrefixSHA256)
	}
	if one.Identity().ThreadID == two.Identity().ThreadID {
		t.Fatal("independent conversations reused one thread ID")
	}
	var firstBody, secondBody map[string]json.RawMessage
	_ = json.Unmarshal(first.Request.Body, &firstBody)
	_ = json.Unmarshal(second.Request.Body, &secondBody)
	if !bytes.Equal(firstBody["tools"], secondBody["tools"]) {
		t.Fatal("complete schemas differ across independent conversations")
	}
}

func TestStickyTurnStateIsSentAndNeverPrintedAsRequestDiagnostic(t *testing.T) {
	conversation := newTestConversation(t, "develop", "builder", "builder")
	response := contract.ProviderResponse{Headers: http.Header{"x-codex-turn-state": {"opaque-provider-secret"}}}
	if err := conversation.ApplyResponse(response); err != nil {
		t.Fatal(err)
	}
	request := injectedRequest(conversation)
	encoded, err := Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	if encoded.Request.Headers.Get("x-codex-turn-state") != "opaque-provider-secret" {
		t.Fatal("opaque turn state was not replayed")
	}
	for _, diagnostic := range []string{
		encoded.Request.String(), fmt.Sprintf("%+v", request), fmt.Sprintf("%#v", request.Credentials), fmt.Sprintf("%#v", encoded),
	} {
		if strings.Contains(diagnostic, "opaque-provider-secret") || strings.Contains(diagnostic, "jwt.secret.signature") || strings.Contains(diagnostic, "acct-private") {
			t.Fatal("provider request diagnostic leaked an opaque or credential value")
		}
	}
}

func TestPrefixRejectsMissingSchemaUnion(t *testing.T) {
	_, err := NewPrefix(PrefixVersion{Adapter: "a", Prompt: "p", Tools: "t"}, "shared", CanonicalToolSchemas()[:6])
	if err != nil {
		t.Fatal(err)
	}
	conversation := newTestConversation(t, "develop", "builder", "builder")
	request := injectedRequest(conversation)
	request.Prefix, err = NewPrefix(PrefixVersion{Adapter: "a", Prompt: "p", Tools: "t"}, "shared", CanonicalToolSchemas()[:6])
	if err != nil {
		t.Fatal(err)
	}
	_, err = Encode(request)
	if err == nil || !strings.Contains(err.Error(), "complete canonical seven-schema prefix") {
		t.Fatalf("incomplete schema union was not refused: %v", err)
	}
}
