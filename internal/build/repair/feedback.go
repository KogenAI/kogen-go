package repair

import (
	"errors"
	"fmt"

	"kogen-go/internal/provider/session"
)

const ControllerFeedbackPrefix = "Kogen's controller reported this failure. Continue the same session and fix it:\n\n"

const protectedRestoreTemplate = "You changed %s; acceptance tests and the Intent are read-only and have been restored. Make the implementation satisfy them."

// ControllerFeedback preserves the gate's exact bytes beneath the required
// controller wrapper. The caller appends this as a new user history item.
func ControllerFeedback(feedback string) string { return ControllerFeedbackPrefix + feedback }

// AppendControllerFeedback appends feedback to the existing conversation. It
// never replaces history or changes the instruction prefix.
func AppendControllerFeedback(conversation *session.Conversation, feedback string) error {
	if conversation == nil {
		return errors.New("repair machine: cannot append feedback to a nil conversation")
	}
	return conversation.AppendControllerMessage(ControllerFeedback(feedback))
}

// ProtectedRestoreMessages returns one exact controller note per restored
// path, in the order reported by the protection restorer.
func ProtectedRestoreMessages(paths []string) []string {
	messages := make([]string, 0, len(paths))
	for _, path := range paths {
		messages = append(messages, fmt.Sprintf(protectedRestoreTemplate, path))
	}
	return messages
}

// AppendProtectedRestoreMessages must be called after the batch's raw response
// items and tool outputs have been appended to the same persistent session.
func AppendProtectedRestoreMessages(conversation *session.Conversation, paths []string) error {
	if conversation == nil {
		return errors.New("repair machine: cannot append protected restore notes to a nil conversation")
	}
	for _, message := range ProtectedRestoreMessages(paths) {
		if err := conversation.AppendControllerMessage(message); err != nil {
			return err
		}
	}
	return nil
}
