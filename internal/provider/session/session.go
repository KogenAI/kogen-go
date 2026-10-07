// Package session owns persisted conversation identity, append-only raw
// history, provider routing state, and per-attempt usage observations.
package session

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"kogen-go/internal/contract"
)

const ContinuationInstruction = "The response stream was interrupted. Continue the same turn from the received progress above. Preserve its findings and constraints; do not restart the task or repeat completed work. Proposed tool calls above were not executed; reissue any still needed."

var (
	protocolIDPattern = regexp.MustCompile(`^[A-Za-z0-9_:-]{1,256}$`)
	identityPart      = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
)

// Binding describes the stable run and one conversation within that run.
// CacheKey must be generated once for the run and persisted with its run
// record; it must not be regenerated for another stage, attempt, rung, or
// epoch.
type Binding struct {
	RunID    string
	CacheKey string
	Role     contract.RoleName
	Provider string
	Model    string
	Effort   string
	Stage    string
	Attempt  string
	Rung     string
	Epoch    string
}

// NewOpaqueID creates a protocol-safe ID from operating-system randomness.
// It never includes a path, credential, account name, or timestamp.
func NewOpaqueID() (string, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("create opaque session identity: %w", err)
	}
	return "kgo_" + base64.RawURLEncoding.EncodeToString(random[:]), nil
}

// NewRunID creates the persisted protocol identity used to derive stable
// conversation thread IDs after restart.
func NewRunID() (string, error) {
	id, err := NewOpaqueID()
	if err != nil {
		return "", err
	}
	return "run_" + strings.TrimPrefix(id, "kgo_"), nil
}

// NewRunAffinityKey creates the one persisted prompt-cache affinity key for a
// Build or Shape invocation. A later stage reuses this exact value.
func NewRunAffinityKey() (string, error) {
	id, err := NewOpaqueID()
	if err != nil {
		return "", err
	}
	return "cache_" + strings.TrimPrefix(id, "kgo_"), nil
}

// Bind resolves protocol defaults and derives a stable thread ID from the
// run/stage/attempt/rung/epoch tuple. Paths, model, effort, and credentials
// are not part of either wire identity.
func Bind(binding Binding) (contract.ConversationIdentity, error) {
	if !protocolIDPattern.MatchString(binding.RunID) || !protocolIDPattern.MatchString(binding.CacheKey) {
		return contract.ConversationIdentity{}, errors.New("session: run and cache identities must be opaque protocol IDs")
	}
	if !identityPart.MatchString(string(binding.Role)) || !identityPart.MatchString(binding.Provider) ||
		!identityPart.MatchString(binding.Model) || !identityPart.MatchString(binding.Effort) ||
		!identityPart.MatchString(binding.Stage) {
		return contract.ConversationIdentity{}, errors.New("session: role, provider, model, effort, and stage must be nonempty protocol tokens")
	}
	if binding.Attempt == "" {
		binding.Attempt = "builder"
	}
	if binding.Rung == "" {
		binding.Rung = binding.Attempt
	}
	if binding.Epoch == "" {
		binding.Epoch = "initial"
	}
	if !identityPart.MatchString(binding.Attempt) || !identityPart.MatchString(binding.Rung) || !identityPart.MatchString(binding.Epoch) {
		return contract.ConversationIdentity{}, errors.New("session: attempt, rung, and epoch must be protocol tokens")
	}
	threadID := DeriveThreadID(binding.RunID, binding.Stage, binding.Attempt, binding.Rung, binding.Epoch)
	return contract.ConversationIdentity{
		RunID: binding.RunID, CacheKey: binding.CacheKey, ThreadID: threadID,
		SessionID: DeriveLiteSessionID(binding.CacheKey), Role: binding.Role,
		Stage: binding.Stage, Attempt: binding.Attempt, Rung: binding.Rung, Epoch: binding.Epoch,
		Provider: binding.Provider, Model: binding.Model, Effort: binding.Effort,
	}, nil
}

// DeriveThreadID is stable for a given tuple and intentionally independent of
// model and effort so provider fallback stays in the same conversation.
func DeriveThreadID(runID, stage, attempt, rung, epoch string) string {
	return "thread_" + hashFields("kogen:responses:v2", runID, stage, attempt, rung, epoch)
}

// DeriveLiteSessionID is a distinct stable run-level protocol ID. It is not
// the cache key or conversation thread ID.
func DeriveLiteSessionID(cacheKey string) string {
	return "lite_" + hashFields("kogen:responses:lite:v1", cacheKey)
}

func hashFields(fields ...string) string {
	hash := sha256.New()
	for index, field := range fields {
		if index != 0 {
			_, _ = hash.Write([]byte{0})
		}
		_, _ = io.WriteString(hash, field)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// UsageRecord preserves one nullable usage value per HTTP attempt. A nil Usage
// is an unknown measurement, never an observed zero.
type UsageRecord struct {
	Usage *contract.TokenUsage `json:"usage"`
}

// Snapshot is the persisted state required to resume one conversation. The
// cache key and thread ID are opaque IDs, not paths or credentials.
type Snapshot struct {
	Protocol            contract.ConversationSession `json:"protocol"`
	Attempts            []UsageRecord                `json:"attempts"`
	DropEncryptedBefore int                          `json:"drop_encrypted_before"`
}

// String, GoString and Format intentionally omit raw history and routing
// values from diagnostics.
func (s Snapshot) String() string {
	return fmt.Sprintf("SessionSnapshot{history_items=%d usage_attempts=%d turn_state_present=%t drop_encrypted_before=%d}", len(s.Protocol.History), len(s.Attempts), s.Protocol.Routing.HasCodexTurnState, s.DropEncryptedBefore)
}

func (s Snapshot) GoString() string { return s.String() }

func (s Snapshot) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, s.String()) }

// Conversation wraps the shared transport session with the append-only usage
// history and model-switch marker that must survive persistence.
type Conversation struct {
	protocol            contract.ConversationSession
	attempts            []UsageRecord
	dropEncryptedBefore int
}

// String, GoString and Format keep prompt, response, and opaque routing bytes
// out of routine diagnostics.
func (c *Conversation) String() string {
	if c == nil {
		return "Conversation{nil}"
	}
	return fmt.Sprintf("Conversation{run_id=%q cache_key=%q thread_id=%q history_items=%d usage_attempts=%d turn_state_present=%t}", c.protocol.Identity.RunID, c.protocol.Identity.CacheKey, c.protocol.Identity.ThreadID, len(c.protocol.History), len(c.attempts), c.protocol.Routing.HasCodexTurnState)
}

func (c *Conversation) GoString() string { return c.String() }

func (c *Conversation) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, c.String()) }

// New creates a conversation from a validated identity. Use the same
// Conversation pointer for every turn, retry, guard, validation and repair in
// the conversation.
func New(identity contract.ConversationIdentity) (*Conversation, error) {
	if err := validateIdentity(identity); err != nil {
		return nil, err
	}
	return &Conversation{protocol: contract.ConversationSession{Identity: identity}}, nil
}

// Restore reconstructs a conversation without rewriting any history bytes.
func Restore(snapshot Snapshot) (*Conversation, error) {
	if err := validateIdentity(snapshot.Protocol.Identity); err != nil {
		return nil, err
	}
	protocol := cloneProtocol(snapshot.Protocol)
	for _, item := range protocol.History {
		if !validSessionItemKind(item.Kind) || !json.Valid(item.Raw) {
			return nil, errors.New("session: persisted history contains an invalid item")
		}
	}
	conversation := &Conversation{
		protocol:            protocol,
		attempts:            cloneUsageRecords(snapshot.Attempts),
		dropEncryptedBefore: snapshot.DropEncryptedBefore,
	}
	if conversation.dropEncryptedBefore < 0 || conversation.dropEncryptedBefore > len(conversation.protocol.History) {
		return nil, errors.New("session: persisted encrypted-reasoning boundary is outside history")
	}
	return conversation, nil
}

// Snapshot returns a deep copy suitable for the run record. Callers publish it
// through the durable journal/store owned by the run layer.
func (c *Conversation) Snapshot() Snapshot {
	if c == nil {
		return Snapshot{}
	}
	return Snapshot{
		Protocol:            cloneProtocol(c.protocol),
		Attempts:            cloneUsageRecords(c.attempts),
		DropEncryptedBefore: c.dropEncryptedBefore,
	}
}

// ProtocolSession returns the stable object expected by contract.ProviderPort.
// Do not replace it between turns.
func (c *Conversation) ProtocolSession() *contract.ConversationSession {
	if c == nil {
		return nil
	}
	return &c.protocol
}

// Identity returns a copy of the current protocol identity.
func (c *Conversation) Identity() contract.ConversationIdentity {
	if c == nil {
		return contract.ConversationIdentity{}
	}
	return c.protocol.Identity
}

// Append retains a validated raw item by copying its bytes. It never parses or
// reconstructs earlier history.
func (c *Conversation) Append(kind contract.SessionItemKind, raw json.RawMessage) error {
	if c == nil {
		return errors.New("session: nil conversation")
	}
	return c.protocol.Append(kind, raw)
}

// AppendInput adds an input item to this conversation's history.
func (c *Conversation) AppendInput(raw json.RawMessage) error {
	return c.Append(contract.SessionInput, raw)
}

// AppendUser appends a canonical Responses user message.
func (c *Conversation) AppendUser(text string) error {
	raw, err := json.Marshal(userMessage{Role: "user", Content: []inputText{{Type: "input_text", Text: text}}})
	if err != nil {
		return err
	}
	return c.AppendInput(raw)
}

// AppendControllerMessage appends controller feedback without modifying the
// instruction prefix or any earlier input item.
func (c *Conversation) AppendControllerMessage(text string) error {
	raw, err := json.Marshal(userMessage{Role: "user", Content: []inputText{{Type: "input_text", Text: text}}})
	if err != nil {
		return err
	}
	return c.Append(contract.SessionControllerMessage, raw)
}

// AppendToolOutput appends a tool result after its raw response call item.
func (c *Conversation) AppendToolOutput(callID, output string) error {
	if callID == "" {
		return errors.New("session: tool output requires a call ID")
	}
	raw, err := json.Marshal(toolOutput{Type: "function_call_output", CallID: callID, Output: output})
	if err != nil {
		return err
	}
	return c.Append(contract.SessionToolOutput, raw)
}

// AppendContinuation adds received partial response items and the continuation
// controller message in their specified order. It is safe to call repeatedly.
func (c *Conversation) AppendContinuation(items []json.RawMessage) error {
	if c == nil {
		return errors.New("session: nil conversation")
	}
	for _, item := range items {
		if !json.Valid(item) {
			return errors.New("session: response item is not valid JSON")
		}
	}
	for _, item := range items {
		if err := c.Append(contract.SessionResponseItem, item); err != nil {
			return err
		}
	}
	return c.AppendControllerMessage(ContinuationInstruction)
}

// ApplyResponse appends raw output items, captures sticky routing state, and
// records nullable usage from one completed HTTP response.
func (c *Conversation) ApplyResponse(response contract.ProviderResponse) error {
	if c == nil {
		return errors.New("session: nil conversation")
	}
	for _, item := range response.RawItems {
		if !json.Valid(item) {
			return errors.New("session: response item is not valid JSON")
		}
	}
	if response.Evidence.ThreadID != "" && response.Evidence.ThreadID != c.protocol.Identity.ThreadID {
		return errors.New("session: response belongs to a different conversation thread")
	}
	for _, item := range response.RawItems {
		if err := c.Append(contract.SessionResponseItem, item); err != nil {
			return err
		}
	}
	if value, ok := headerValue(response.Headers, "x-codex-turn-state"); ok {
		c.protocol.Routing.CodexTurnState = []byte(value)
		c.protocol.Routing.HasCodexTurnState = true
	}
	c.AppendAttemptUsage(response.Usage)
	return nil
}

// AppendAttemptUsage retains one nullable usage row for a request attempt.
func (c *Conversation) AppendAttemptUsage(usage *contract.TokenUsage) {
	if c == nil {
		return
	}
	c.attempts = append(c.attempts, UsageRecord{Usage: cloneUsage(usage)})
}

// Attempts returns a deep copy of the per-attempt nullable usage rows.
func (c *Conversation) Attempts() []UsageRecord {
	if c == nil {
		return nil
	}
	return cloneUsageRecords(c.attempts)
}

// SwitchModel updates the model/effort for the next request. Once the model
// changes, encrypted reasoning from every earlier model remains in raw history
// but is omitted from subsequent replay bodies.
func (c *Conversation) SwitchModel(model, effort string) error {
	if c == nil {
		return errors.New("session: nil conversation")
	}
	if !identityPart.MatchString(model) || !identityPart.MatchString(effort) {
		return errors.New("session: model and effort must be protocol tokens")
	}
	if c.protocol.Identity.Model != "" && c.protocol.Identity.Model != model {
		c.dropEncryptedBefore = len(c.protocol.History)
	}
	c.protocol.Identity.Model = model
	c.protocol.Identity.Effort = effort
	return nil
}

// RoutingState returns a copy; its opaque value must only be sent back to the
// same thread and must never be logged.
func (c *Conversation) RoutingState() contract.RoutingState {
	if c == nil {
		return contract.RoutingState{}
	}
	routing := c.protocol.Routing
	routing.CodexTurnState = bytes.Clone(routing.CodexTurnState)
	return routing
}

// WireHistory returns deterministic JSON encodings for the current request.
// The retained raw history remains byte-for-byte untouched. Object keys are
// sorted on the wire, and a model switch strips old encrypted reasoning only
// from the returned copies.
func (c *Conversation) WireHistory() ([]json.RawMessage, error) {
	if c == nil {
		return nil, errors.New("session: nil conversation")
	}
	items := make([]json.RawMessage, 0, len(c.protocol.History))
	for index, item := range c.protocol.History {
		canonical, err := canonicalItem(item.Raw, index < c.dropEncryptedBefore)
		if err != nil {
			return nil, err
		}
		items = append(items, canonical)
	}
	return items, nil
}

func validateIdentity(identity contract.ConversationIdentity) error {
	if !protocolIDPattern.MatchString(identity.RunID) || !protocolIDPattern.MatchString(identity.CacheKey) ||
		!protocolIDPattern.MatchString(identity.ThreadID) || !protocolIDPattern.MatchString(identity.SessionID) {
		return errors.New("session: identity contains a missing or unsafe protocol ID")
	}
	if !identityPart.MatchString(string(identity.Role)) || !identityPart.MatchString(identity.Provider) ||
		!identityPart.MatchString(identity.Model) || !identityPart.MatchString(identity.Effort) ||
		!identityPart.MatchString(identity.Stage) || !identityPart.MatchString(identity.Attempt) ||
		!identityPart.MatchString(identity.Rung) || !identityPart.MatchString(identity.Epoch) {
		return errors.New("session: identity contains a missing or unsafe role token")
	}
	return nil
}

func validSessionItemKind(kind contract.SessionItemKind) bool {
	switch kind {
	case contract.SessionInput, contract.SessionResponseItem, contract.SessionControllerMessage, contract.SessionToolOutput:
		return true
	default:
		return false
	}
}

func cloneProtocol(protocol contract.ConversationSession) contract.ConversationSession {
	clone := protocol
	clone.History = make([]contract.SessionItem, len(protocol.History))
	for index, item := range protocol.History {
		clone.History[index] = contract.SessionItem{Kind: item.Kind, Raw: bytes.Clone(item.Raw)}
	}
	clone.Routing.CodexTurnState = bytes.Clone(protocol.Routing.CodexTurnState)
	return clone
}

func cloneUsage(usage *contract.TokenUsage) *contract.TokenUsage {
	if usage == nil {
		return nil
	}
	copy := *usage
	copy.Input = cloneInt64(usage.Input)
	copy.CachedInput = cloneInt64(usage.CachedInput)
	copy.CacheWrite = cloneInt64(usage.CacheWrite)
	copy.Output = cloneInt64(usage.Output)
	copy.Reasoning = cloneInt64(usage.Reasoning)
	return &copy
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneUsageRecords(records []UsageRecord) []UsageRecord {
	if records == nil {
		return nil
	}
	cloned := make([]UsageRecord, len(records))
	for index, record := range records {
		cloned[index] = UsageRecord{Usage: cloneUsage(record.Usage)}
	}
	return cloned
}

func headerValue(headers http.Header, name string) (string, bool) {
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) != 0 {
			return values[len(values)-1], true
		}
	}
	return "", false
}

func canonicalItem(raw []byte, dropEncrypted bool) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("session: decode retained history item: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("session: history item contains multiple JSON values")
		}
		return nil, fmt.Errorf("session: trailing history data: %w", err)
	}
	if dropEncrypted {
		if object, ok := value.(map[string]any); ok && object["type"] == "reasoning" {
			delete(object, "encrypted_content")
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("session: encode retained history item: %w", err)
	}
	return json.RawMessage(encoded), nil
}

type userMessage struct {
	Role    string      `json:"role"`
	Content []inputText `json:"content"`
}

type inputText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolOutput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}
