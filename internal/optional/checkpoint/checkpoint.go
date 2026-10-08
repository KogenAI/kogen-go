// Package checkpoint prepares opt-in Build context checkpoints.
//
// It keeps the checkpoint transition separate from the Build controller: a
// caller decides when the configured serialized-history threshold is reached,
// makes the returned no-tools summarizer request, validates its text with
// BuildCheckpoint, and starts a fresh builder conversation with
// NewContinuation. The caller retains the Build's existing approval, worktree,
// and turn/time budgets when it records the continuation event.
package checkpoint

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"kogen-go/internal/contract"
	"kogen-go/internal/provider/session"
	"kogen-go/internal/provider/wire"
)

const (
	// MinimumContextBytes is the smallest supported explicit opt-in threshold.
	MinimumContextBytes = 16_000

	// ContinuationPrefix is mandatory at the beginning of an accepted checkpoint.
	ContinuationPrefix = "Continuation of the same approved Build.\n\n"

	// ContinuationFailedReason is the terminal Build reason for an invalid,
	// empty, or oversized summarizer result.
	ContinuationFailedReason = "continuation_failed"

	// ContinuationEventName is the journal event counted by status rendering
	// after the caller accepts a checkpoint and starts the next epoch.
	ContinuationEventName = "context_continuation"

	// ToolChoiceNone disables callable tools while retaining the complete shared
	// provider schema prefix in the serialized request.
	ToolChoiceNone = "none"
)

// SummarizerInstructions are role-specific instructions placed after the
// shared generic instruction/schema prefix. The summarizer returns a compact
// account of useful work state; it cannot authorize changes or invoke tools.
const SummarizerInstructions = "You are the builder summarizer for the same approved Build. Summarize the current conversation for a fresh builder thread. Preserve the approved request and plan's requirements, completed work, current state, unresolved failures, and constraints. Do not claim unverified work succeeded. Return only the checkpoint text; the controller adds the continuation marker."

// ErrContinuationFailed marks a terminal continuation failure. Callers should
// preserve this reason in the Build record rather than retrying the same
// checkpoint request.
var ErrContinuationFailed = errors.New(ContinuationFailedReason)

// ValidateContextBytes accepts omission (zero) and values at or above the
// normative minimum. The machine-level default must remain omitted/disabled.
func ValidateContextBytes(value int) error {
	if value == 0 {
		return nil
	}
	if value < MinimumContextBytes {
		return fmt.Errorf("build.context_bytes must be at least %d", MinimumContextBytes)
	}
	return nil
}

// ShouldCheckpoint reports whether an enabled serialized-history threshold has
// been reached. contextBytes is a byte count, not a token count. Invalid
// thresholds are rejected by ValidateContextBytes before this is called.
func ShouldCheckpoint(historyBytes, contextBytes int) bool {
	return historyBytes >= contextBytes && contextBytes >= MinimumContextBytes
}

// SummarizerTurn is one builder-role request on a fresh checkpoint epoch.
// Prefix is the same immutable static prefix used by the ordinary builder
// request. CallableTools is always empty and ToolChoice is always "none".
type SummarizerTurn struct {
	Conversation     *session.Conversation
	Prefix           wire.Prefix
	RoleInstructions string
	CallableTools    []string
	ToolChoice       string
}

// PrepareSummarizer copies the active conversation's raw history into a new
// thread for one tool-less summarizer request. The builder role, provider,
// model, effort, stage, attempt, rung, run ID, and cache-affinity key are kept.
// Routing state is deliberately not copied because it belongs to the prior
// thread. The supplied immutable static prefix is retained unchanged.
func PrepareSummarizer(current *session.Conversation, turn int, prefix wire.Prefix) (SummarizerTurn, error) {
	if current == nil {
		return SummarizerTurn{}, errors.New("checkpoint: current builder conversation is required")
	}
	if turn < 1 {
		return SummarizerTurn{}, errors.New("checkpoint: summarizer turn must be positive")
	}
	if prefix.Digest() == "" {
		return SummarizerTurn{}, errors.New("checkpoint: a complete shared static prefix is required")
	}
	base := current.Identity()
	if err := validateBuilderIdentity(base); err != nil {
		return SummarizerTurn{}, err
	}
	identity, err := bindEpoch(base, "checkpoint-"+strconv.Itoa(turn))
	if err != nil {
		return SummarizerTurn{}, err
	}
	conversation, err := session.New(identity)
	if err != nil {
		return SummarizerTurn{}, err
	}
	for _, item := range current.ProtocolSession().History {
		if err := conversation.Append(item.Kind, item.Raw); err != nil {
			return SummarizerTurn{}, fmt.Errorf("checkpoint: copy summarizer history: %w", err)
		}
	}
	return SummarizerTurn{
		Conversation: conversation, Prefix: prefix,
		RoleInstructions: SummarizerInstructions,
		CallableTools:    []string{}, ToolChoice: ToolChoiceNone,
	}, nil
}

// ApprovedInputs are the exact serialized request and plan items approved for
// this Build. NewContinuation appends these raw bytes without decoding,
// re-encoding, or normalizing them.
type ApprovedInputs struct {
	Request json.RawMessage
	Plan    json.RawMessage
}

// Checkpoint is an immutable accepted input item. Its accessors return copies
// so callers cannot change the bytes after the epoch digest has been derived.
type Checkpoint struct {
	item  json.RawMessage
	text  string
	epoch string
}

// Item returns a copy of the canonical checkpoint input item.
func (c Checkpoint) Item() json.RawMessage { return bytes.Clone(c.item) }

// Text returns the checkpoint's full text, including ContinuationPrefix.
func (c Checkpoint) Text() string { return c.text }

// Epoch returns the lowercase SHA-256 digest of the canonical checkpoint item.
func (c Checkpoint) Epoch() string { return c.epoch }

// BuildCheckpoint validates summarizer output and builds the canonical input
// item for the next builder thread. maxBytes bounds the complete serialized
// checkpoint item, including its JSON structure and mandatory prefix.
func BuildCheckpoint(summary string, maxBytes int) (Checkpoint, error) {
	if maxBytes < 1 || !utf8.ValidString(summary) || strings.TrimSpace(summary) == "" {
		return Checkpoint{}, ErrContinuationFailed
	}
	text := ContinuationPrefix + summary
	item, err := canonicalUserItem(text)
	if err != nil || len(item) > maxBytes {
		return Checkpoint{}, ErrContinuationFailed
	}
	digest := sha256.Sum256(item)
	return Checkpoint{
		item: item, text: text, epoch: hex.EncodeToString(digest[:]),
	}, nil
}

// ContinuationTurn is the fresh builder session following an accepted
// checkpoint. The new conversation shares the static prefix and run affinity
// with its previous session while using the checkpoint digest as its epoch.
type ContinuationTurn struct {
	Conversation *session.Conversation
	Prefix       wire.Prefix
}

// NewContinuation creates the compacted builder session. Its ordered history
// is exactly the approved request item, approved plan item, and checkpoint
// item. Earlier conversation items and routing state stay with the prior
// thread; the Build controller retains its budgets and worktree separately.
func NewContinuation(builder contract.ConversationIdentity, approved ApprovedInputs, checkpoint Checkpoint, prefix wire.Prefix) (ContinuationTurn, error) {
	if prefix.Digest() == "" {
		return ContinuationTurn{}, errors.New("checkpoint: a complete shared static prefix is required")
	}
	if err := validateBuilderIdentity(builder); err != nil {
		return ContinuationTurn{}, err
	}
	if !json.Valid(approved.Request) || !json.Valid(approved.Plan) {
		return ContinuationTurn{}, errors.New("checkpoint: approved request and plan must be valid raw JSON items")
	}
	if !validCheckpoint(checkpoint) {
		return ContinuationTurn{}, ErrContinuationFailed
	}
	identity, err := bindEpoch(builder, checkpoint.epoch)
	if err != nil {
		return ContinuationTurn{}, err
	}
	conversation, err := session.New(identity)
	if err != nil {
		return ContinuationTurn{}, err
	}
	for _, raw := range []json.RawMessage{approved.Request, approved.Plan, checkpoint.item} {
		if err := conversation.Append(contract.SessionInput, raw); err != nil {
			return ContinuationTurn{}, fmt.Errorf("checkpoint: build compacted history: %w", err)
		}
	}
	return ContinuationTurn{Conversation: conversation, Prefix: prefix}, nil
}

func validateBuilderIdentity(identity contract.ConversationIdentity) error {
	if identity.Role != "builder" || identity.RunID == "" || identity.CacheKey == "" ||
		identity.Stage == "" || identity.Attempt == "" || identity.Rung == "" ||
		identity.Provider == "" || identity.Model == "" || identity.Effort == "" {
		return errors.New("checkpoint: a complete builder conversation identity is required")
	}
	return nil
}

func bindEpoch(base contract.ConversationIdentity, epoch string) (contract.ConversationIdentity, error) {
	return session.Bind(session.Binding{
		RunID: base.RunID, CacheKey: base.CacheKey, Role: base.Role,
		Provider: base.Provider, Model: base.Model, Effort: base.Effort,
		Stage: base.Stage, Attempt: base.Attempt, Rung: base.Rung, Epoch: epoch,
	})
}

func validCheckpoint(checkpoint Checkpoint) bool {
	if !json.Valid(checkpoint.item) || !strings.HasPrefix(checkpoint.text, ContinuationPrefix) ||
		strings.TrimSpace(strings.TrimPrefix(checkpoint.text, ContinuationPrefix)) == "" {
		return false
	}
	canonical, err := wire.CanonicalJSON(checkpoint.item)
	if err != nil || !bytes.Equal(canonical, checkpoint.item) {
		return false
	}
	var message struct {
		Content []struct {
			Text string `json:"text"`
			Type string `json:"type"`
		} `json:"content"`
		Role string `json:"role"`
	}
	if err := json.Unmarshal(checkpoint.item, &message); err != nil || message.Role != "user" ||
		len(message.Content) != 1 || message.Content[0].Type != "input_text" ||
		message.Content[0].Text != checkpoint.text {
		return false
	}
	digest := sha256.Sum256(checkpoint.item)
	return checkpoint.epoch == hex.EncodeToString(digest[:])
}

// canonicalUserItem encodes the single supported checkpoint item shape with
// lexicographically ordered JSON fields and stable UTF-8 text bytes.
func canonicalUserItem(text string) (json.RawMessage, error) {
	raw, err := json.Marshal(map[string]any{
		"content": []any{map[string]string{"text": text, "type": "input_text"}},
		"role":    "user",
	})
	if err != nil {
		return nil, err
	}
	return wire.CanonicalJSON(raw)
}
