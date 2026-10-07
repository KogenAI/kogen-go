package repair

import (
	"encoding/json"
	"testing"

	"kogen-go/internal/contract"
	"kogen-go/internal/provider/session"
)

func TestControllerFeedbackAppendsExactMessageToExistingSession(t *testing.T) {
	identity, err := session.Bind(session.Binding{
		RunID: "run_0123456789abcdef", CacheKey: "cache_0123456789abcdef",
		Role: "builder", Provider: "chatgpt", Model: "gpt-6-luna", Effort: "max",
		Stage: "build", Attempt: "builder", Rung: "R1", Epoch: "initial",
	})
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := session.New(identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AppendUser("approved request"); err != nil {
		t.Fatal(err)
	}
	feedback := "gate: 1 errors, 0 warnings; checks ; acceptance 0/1"
	if err := AppendControllerFeedback(conversation, feedback); err != nil {
		t.Fatal(err)
	}
	history := conversation.Snapshot().Protocol.History
	if len(history) != 2 || history[0].Kind != contract.SessionInput || history[1].Kind != contract.SessionControllerMessage {
		t.Fatalf("history kinds = %#v", history)
	}
	var message struct {
		Role    string `json:"role"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(history[1].Raw, &message); err != nil {
		t.Fatal(err)
	}
	want := ControllerFeedbackPrefix + feedback
	if message.Role != "user" || len(message.Content) != 1 || message.Content[0].Text != want {
		t.Fatalf("appended message = %#v, want exact text %q", message, want)
	}
}

func TestProtectedRestoreNotesAppendInOrder(t *testing.T) {
	identity, err := session.Bind(session.Binding{
		RunID: "run_0123456789abcdef", CacheKey: "cache_0123456789abcdef",
		Role: "builder", Provider: "chatgpt", Model: "gpt-6-luna", Effort: "max",
		Stage: "build", Attempt: "builder", Rung: "R1", Epoch: "initial",
	})
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := session.New(identity)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{"intent.md", "acceptance_test"}
	if err := AppendProtectedRestoreMessages(conversation, paths); err != nil {
		t.Fatal(err)
	}
	history := conversation.Snapshot().Protocol.History
	if len(history) != 2 || history[0].Kind != contract.SessionControllerMessage || history[1].Kind != contract.SessionControllerMessage {
		t.Fatalf("protected note history = %#v", history)
	}
	for index, path := range paths {
		var message struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(history[index].Raw, &message); err != nil {
			t.Fatal(err)
		}
		if len(message.Content) != 1 || message.Content[0].Text != ProtectedRestoreMessages([]string{path})[0] {
			t.Fatalf("restore note %d = %#v", index, message)
		}
	}
}
