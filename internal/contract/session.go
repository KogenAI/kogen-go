package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// SessionItemKind identifies one immutable input/history item.
type SessionItemKind string

const (
	SessionInput             SessionItemKind = "input"
	SessionResponseItem      SessionItemKind = "response_item"
	SessionControllerMessage SessionItemKind = "controller_message"
	SessionToolOutput        SessionItemKind = "tool_output"
)

// SessionItem retains the complete raw JSON bytes in append-only conversation
// order. Existing items are never reconstructed or normalized between turns.
type SessionItem struct {
	Kind SessionItemKind
	Raw  json.RawMessage
}

// String and GoString report only the item kind and size, never the prompt or
// response bytes.
func (i SessionItem) String() string {
	return fmt.Sprintf("SessionItem{kind=%q raw_bytes=%d}", i.Kind, len(i.Raw))
}

func (i SessionItem) GoString() string { return i.String() }

// ConversationIdentity contains protocol and run-affinity identifiers only.
// Persisted IDs must never contain filesystem paths or credential material.
type ConversationIdentity struct {
	RunID     string
	CacheKey  string
	ThreadID  string
	SessionID string
	Role      RoleName
	Stage     string
	Attempt   string
	Rung      string
	Epoch     string
	Provider  string
	Model     string
	Effort    string
}

// RoutingState contains opaque provider routing state scoped to this thread.
// The value is replayed internally and only its presence is journaled.
type RoutingState struct {
	CodexTurnState    []byte
	HasCodexTurnState bool
}

// String and GoString expose only routing-state presence and size.
func (r RoutingState) String() string {
	return fmt.Sprintf("RoutingState{codex_turn_state_present=%t bytes=%d}", r.HasCodexTurnState, len(r.CodexTurnState))
}

func (r RoutingState) GoString() string { return r.String() }

// ConversationSession is the one persistent object for an entire conversation.
// It owns input, raw response items, controller messages, tool outputs, protocol
// identities and sticky routing state; callers must pass this same pointer to
// every provider turn and repair stage in that conversation.
type ConversationSession struct {
	Identity ConversationIdentity
	History  []SessionItem
	Routing  RoutingState
}

// String and GoString keep conversation history and opaque routing values out
// of ordinary diagnostics.
func (s ConversationSession) String() string {
	i := s.Identity
	return fmt.Sprintf("ConversationSession{run_id=%q cache_key=%q thread_id=%q session_id=%q role=%q stage=%q attempt=%q rung=%q epoch=%q provider=%q model=%q effort=%q history_items=%d turn_state_present=%t}", i.RunID, i.CacheKey, i.ThreadID, i.SessionID, i.Role, i.Stage, i.Attempt, i.Rung, i.Epoch, i.Provider, i.Model, i.Effort, len(s.History), s.Routing.HasCodexTurnState)
}

func (s ConversationSession) GoString() string { return s.String() }

// Append preserves raw bytes and copies them so a caller cannot rewrite earlier
// request history through a reused backing array.
func (s *ConversationSession) Append(kind SessionItemKind, raw json.RawMessage) error {
	if s == nil {
		return errors.New("cannot append to a nil conversation session")
	}
	switch kind {
	case SessionInput, SessionResponseItem, SessionControllerMessage, SessionToolOutput:
	default:
		return errors.New("unknown conversation item kind")
	}
	if !json.Valid(raw) {
		return errors.New("conversation item is not valid JSON")
	}
	s.History = append(s.History, SessionItem{Kind: kind, Raw: bytes.Clone(raw)})
	return nil
}
