package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestConversationSessionRetainsAppendOnlyRawItems(t *testing.T) {
	session := &ConversationSession{Identity: ConversationIdentity{ThreadID: "opaque-thread"}}
	input := json.RawMessage(`{"role":"user","content":"raw"}`)
	if err := session.Append(SessionInput, input); err != nil {
		t.Fatal(err)
	}
	input[2] = 'X'
	response := json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque"}`)
	if err := session.Append(SessionResponseItem, response); err != nil {
		t.Fatal(err)
	}
	if len(session.History) != 2 {
		t.Fatalf("history length = %d, want 2", len(session.History))
	}
	if got, want := session.History[0].Kind, SessionInput; got != want {
		t.Fatalf("first item kind = %q, want %q", got, want)
	}
	if !bytes.Equal(session.History[0].Raw, []byte(`{"role":"user","content":"raw"}`)) {
		t.Fatalf("appended input bytes changed with caller buffer: %s", session.History[0].Raw)
	}
	if !bytes.Equal(session.History[1].Raw, response) {
		t.Fatalf("response bytes changed: %s", session.History[1].Raw)
	}
}

func TestDiagnosticsRedactWireValuesAndConversationContents(t *testing.T) {
	request := ProviderRequest{
		Endpoint: "https://provider.example.invalid/v1/responses?query=private-query",
		Headers:  http.Header{"Authorization": []string{"Bearer private-token"}, "X-Codex-Turn-State": []string{"private-routing"}},
		Body:     []byte(`{"prompt":"private-prompt"}`),
	}
	response := ProviderResponse{
		StatusCode: 200,
		Headers:    http.Header{"X-Codex-Turn-State": []string{"private-response-routing"}},
		RawItems:   []json.RawMessage{json.RawMessage(`{"text":"private-response"}`)},
	}
	session := ConversationSession{Identity: ConversationIdentity{ThreadID: "opaque-thread"}, Routing: RoutingState{CodexTurnState: []byte("private-session-routing"), HasCodexTurnState: true}}
	if err := session.Append(SessionInput, json.RawMessage(`{"prompt":"private-history"}`)); err != nil {
		t.Fatal(err)
	}

	diagnostics := fmt.Sprintf("%v %#v %v %#v %v %#v", request, request, response, response, session, session)
	for _, secret := range []string{"private-query", "private-token", "private-routing", "private-prompt", "private-response", "private-response-routing", "private-session-routing", "private-history"} {
		if strings.Contains(diagnostics, secret) {
			t.Errorf("diagnostics leaked %q: %s", secret, diagnostics)
		}
	}
	if !strings.Contains(diagnostics, "Authorization") || !strings.Contains(diagnostics, "body_bytes=") || !strings.Contains(diagnostics, "turn_state_present=true") {
		t.Fatalf("diagnostics omitted safe request evidence: %s", diagnostics)
	}
}
