// Package wire builds canonical provider request bytes without performing
// network I/O or retaining credentials.
package wire

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"

	"kogen-go/internal/contract"
	"kogen-go/internal/provider/session"
)

const (
	OwnedEndpoint    = "https://api.openai.com/v1/responses"
	InjectedEndpoint = "https://chatgpt.com/backend-api/codex/responses"
)

var processPrefixRegistry PrefixRegistry

// Mode selects the ChatGPT Responses wire shape.
type Mode string

const (
	ModeOwned    Mode = "owned"
	ModeInjected Mode = "injected"
	ModeLite     Mode = "lite"
)

// CredentialKind distinguishes an owned login from injected benchmark/CI
// credentials. Credential values are used only to form request headers.
type CredentialKind string

const (
	CredentialOwned    CredentialKind = "owned"
	CredentialInjected CredentialKind = "injected"
)

// Credentials are transport-boundary values and must not be logged.
type Credentials struct {
	Kind        CredentialKind
	AccessToken string
	AccountID   string
}

// String and Format never reveal credential values, including under `%+v`.
func (c Credentials) String() string {
	return fmt.Sprintf("Credentials{kind=%q access_token_present=%t account_id_present=%t}", c.Kind, c.AccessToken != "", c.AccountID != "")
}

func (c Credentials) GoString() string { return c.String() }

func (c Credentials) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, c.String()) }

// Config contains adapter-level settings for one request.
type Config struct {
	Mode                  Mode
	EndpointOverride      string
	UserAgentVersion      string
	SupportsGenerationCap bool
}

// String and Format omit the endpoint override, which may contain test-only
// query material.
func (c Config) String() string {
	return fmt.Sprintf("WireConfig{mode=%q endpoint_override_set=%t user_agent_version=%q generation_cap_supported=%t}", c.Mode, c.EndpointOverride != "", c.UserAgentVersion, c.SupportsGenerationCap)
}

func (c Config) GoString() string { return c.String() }

func (c Config) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, c.String()) }

// Request contains immutable static prefix material, per-role controls, and a
// pointer to the one persistent conversation object. Variable task data lives
// in Conversation history and is serialized after the static instructions and
// canonical schema union.
type Request struct {
	Config             Config
	Credentials        Credentials
	Conversation       *session.Conversation
	Prefix             Prefix
	Model              string
	Effort             string
	RoleInstructions   string
	CallableTools      []string
	ToolChoice         string
	DevelopmentRequest bool
	GenerationTokens   *uint64
}

// String and Format omit credentials, instructions, schemas, and task history.
func (r Request) String() string {
	var cacheKey, threadID string
	if r.Conversation != nil {
		identity := r.Conversation.Identity()
		cacheKey, threadID = identity.CacheKey, identity.ThreadID
	}
	return fmt.Sprintf("WireRequestInput{mode=%q model=%q effort=%q cache_key=%q thread_id=%q callable_tools=%d role_instructions_present=%t prefix=%s}", r.Config.Mode, r.Model, r.Effort, cacheKey, threadID, len(r.CallableTools), r.RoleInstructions != "", r.Prefix.Digest())
}

func (r Request) GoString() string { return r.String() }

func (r Request) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, r.String()) }

// EncodedRequest includes the complete transport request and a safe digest of
// the immutable generic-instruction/schema prefix for request-level telemetry.
type EncodedRequest struct {
	Request            contract.ProviderRequest
	StaticPrefixSHA256 string
}

// String and Format report only endpoint identity, byte length and the static
// prefix digest; body/header values are excluded.
func (r EncodedRequest) String() string {
	var host, path string
	if endpoint, err := url.Parse(r.Request.Endpoint); err == nil {
		host, path = endpoint.Hostname(), endpoint.EscapedPath()
	}
	return fmt.Sprintf("EncodedRequest{host=%q path=%q body_bytes=%d static_prefix_sha256=%s}", host, path, len(r.Request.Body), r.StaticPrefixSHA256)
}

func (r EncodedRequest) GoString() string { return r.String() }

func (r EncodedRequest) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, r.String()) }

// Build encodes one complete HTTP request. It does not send it.
func Build(request Request) (contract.ProviderRequest, error) {
	encoded, err := Encode(request)
	if err != nil {
		return contract.ProviderRequest{}, err
	}
	return encoded.Request, nil
}

// Encode creates the wire request and returns the safe static prefix digest.
func Encode(request Request) (EncodedRequest, error) {
	if err := validatePrefix(request.Prefix); err != nil {
		return EncodedRequest{}, err
	}
	if request.Conversation == nil {
		return EncodedRequest{}, errors.New("wire: request has no persistent conversation")
	}
	identity := request.Conversation.Identity()
	if identity.CacheKey == "" || identity.ThreadID == "" || identity.SessionID == "" {
		return EncodedRequest{}, errors.New("wire: conversation is missing cache or thread identity")
	}
	if identity.Provider != "chatgpt" {
		return EncodedRequest{}, errors.New("wire: ChatGPT Responses adapter requires a ChatGPT conversation")
	}
	if !safeVersion(request.Model) || !safeVersion(request.Effort) {
		return EncodedRequest{}, errors.New("wire: model and effort must be nonempty protocol tokens")
	}
	endpoint, err := endpointFor(request.Config)
	if err != nil {
		return EncodedRequest{}, err
	}
	if err := validateGenerationCap(endpoint, request.Config, request.Model, request.GenerationTokens); err != nil {
		return EncodedRequest{}, err
	}
	if err := validateCredentials(request.Config.Mode, request.Model, request.GenerationTokens, request.Credentials); err != nil {
		return EncodedRequest{}, err
	}
	if err := validateCallableTools(request.Prefix, request.CallableTools, request.ToolChoice); err != nil {
		return EncodedRequest{}, err
	}
	if err := processPrefixRegistry.Register(identity.Provider, request.Prefix); err != nil {
		return EncodedRequest{}, err
	}
	if err := request.Conversation.SwitchModel(request.Model, request.Effort); err != nil {
		return EncodedRequest{}, err
	}
	input, err := buildInput(request, identity.ThreadID)
	if err != nil {
		return EncodedRequest{}, err
	}
	body, err := encodeBody(request, identity, input)
	if err != nil {
		return EncodedRequest{}, err
	}
	headers := makeHeaders(request, identity)
	return EncodedRequest{
		Request:            contract.ProviderRequest{Endpoint: endpoint.String(), Headers: headers, Body: body},
		StaticPrefixSHA256: request.Prefix.Digest(),
	}, nil
}

func endpointFor(config Config) (*url.URL, error) {
	raw := config.EndpointOverride
	if raw == "" {
		switch config.Mode {
		case ModeOwned:
			raw = OwnedEndpoint
		case ModeInjected, ModeLite:
			raw = InjectedEndpoint
		default:
			return nil, errors.New("wire: unknown Responses adapter mode")
		}
	}
	endpoint, err := url.Parse(raw)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Fragment != "" {
		return nil, errors.New("wire: provider endpoint is invalid")
	}
	return endpoint, nil
}

func validateGenerationCap(endpoint *url.URL, config Config, model string, limit *uint64) error {
	if config.Mode == ModeLite && (model != "gpt-6-luna" || limit != nil) {
		return errors.New("Unsupported adapter or model-generation cap for this backend.")
	}
	if limit == nil {
		return nil
	}
	if *limit < 1 || *limit > 100_000 {
		return errors.New("Model-generation cap must be between 1 and 100000.")
	}
	if endpoint.String() != OwnedEndpoint && !config.SupportsGenerationCap {
		return errors.New("Model-generation cap is unsupported on this endpoint/adapter.")
	}
	return nil
}

func validateCredentials(mode Mode, model string, limit *uint64, credentials Credentials) error {
	if mode != ModeOwned && mode != ModeInjected && mode != ModeLite {
		return errors.New("wire: unknown Responses adapter mode")
	}
	if mode == ModeLite && (credentials.Kind != CredentialInjected || model != "gpt-6-luna" || limit != nil) {
		return errors.New("Unsupported adapter or model-generation cap for this backend.")
	}
	if credentials.AccessToken == "" {
		return errors.New("wire: provider access token is missing")
	}
	if mode != ModeOwned && (credentials.Kind != CredentialInjected || credentials.AccountID == "") {
		return errors.New("wire: injected ChatGPT credentials require an account ID")
	}
	if mode == ModeOwned && credentials.Kind != CredentialOwned {
		return errors.New("wire: owned mode requires owned ChatGPT credentials")
	}
	return nil
}

func validatePrefix(prefix Prefix) error {
	if prefix.digest == "" || len(prefix.schemas) != 7 || len(prefix.toolNameSet) != 7 {
		return errors.New("wire: request requires the complete canonical seven-schema prefix")
	}
	for _, name := range []string{"edit", "finish", "read", "search", "shell", "tool_output", "write"} {
		if !prefix.Allows(name) {
			return errors.New("wire: request requires the complete canonical seven-schema prefix")
		}
	}
	return nil
}

func validateCallableTools(prefix Prefix, names []string, choice string) error {
	if choice != "" && choice != "auto" && choice != "none" && choice != "required" {
		return errors.New("wire: tool choice must be auto, none, or required")
	}
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if !prefix.Allows(name) {
			return fmt.Errorf("wire: callable tool %q is outside the canonical schema union", name)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("wire: duplicate callable tool %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func buildInput(request Request, threadID string) ([]json.RawMessage, error) {
	input := make([]json.RawMessage, 0, len(request.Conversation.ProtocolSession().History)+3)
	switch request.Config.Mode {
	case ModeOwned:
		tools, err := additionalToolsItem(request.Prefix, "")
		if err != nil {
			return nil, err
		}
		input = append(input, tools)
	case ModeLite:
		tools, err := additionalToolsItem(request.Prefix, stableID(request.Prefix.digest, "additional_tools"))
		if err != nil {
			return nil, err
		}
		input = append(input, tools)
		shared, err := developerItem(stableID(request.Prefix.digest, "shared_instructions"), request.Prefix.generic)
		if err != nil {
			return nil, err
		}
		input = append(input, shared)
	}
	if request.RoleInstructions != "" {
		role, err := developerItem(stableID(threadID, "role_instructions"), request.RoleInstructions)
		if err != nil {
			return nil, err
		}
		input = append(input, role)
	}
	history, err := request.Conversation.WireHistory()
	if err != nil {
		return nil, err
	}
	input = append(input, history...)
	return input, nil
}

func additionalToolsItem(prefix Prefix, id string) (json.RawMessage, error) {
	item := map[string]any{
		"type": "additional_tools", "role": "developer", "tools": prefix.schemas,
	}
	if id != "" {
		item["id"] = id
	}
	return CanonicalJSON(mustJSON(item))
}

func developerItem(id, text string) (json.RawMessage, error) {
	item := map[string]any{
		"id": id, "type": "message", "role": "developer",
		"content": []any{map[string]any{"type": "input_text", "text": text}},
	}
	return CanonicalJSON(mustJSON(item))
}

func encodeBody(request Request, identity contract.ConversationIdentity, input []json.RawMessage) ([]byte, error) {
	mode := request.Config.Mode
	instructions := request.Prefix.generic
	if mode == ModeLite {
		instructions = ""
	}
	toolChoice := makeToolChoice(request.CallableTools, request.ToolChoice)
	var reasoning map[string]string
	reasoning = map[string]string{"effort": request.Effort}
	if mode == ModeLite {
		reasoning["context"] = "all_turns"
	} else if request.Model != "gpt-6-luna" {
		reasoning["summary"] = "auto"
	}

	encoder := orderedObject{first: true}
	encoder.field("model", request.Model)
	encoder.field("instructions", instructions)
	if mode == ModeInjected {
		encoder.field("tools", request.Prefix.schemas)
	}
	encoder.field("reasoning", reasoning)
	encoder.field("store", false)
	encoder.field("stream", true)
	if mode != ModeOwned {
		encoder.field("include", []string{"reasoning.encrypted_content"})
	}
	encoder.field("prompt_cache_key", identity.CacheKey)
	encoder.field("tool_choice", toolChoice)
	encoder.field("parallel_tool_calls", false)
	if request.Model == "gpt-6-luna" {
		encoder.field("text", map[string]string{"verbosity": "low"})
	}
	if mode != ModeLite {
		windowID := identity.ThreadID + ":0"
		metadata := map[string]string{
			"session_id":            identity.CacheKey,
			"thread_id":             identity.ThreadID,
			"x-codex-window-id":     windowID,
			"x-codex-turn-metadata": turnMetadata(identity.CacheKey, identity.ThreadID, windowID),
		}
		encoder.field("client_metadata", metadata)
	}
	if request.DevelopmentRequest && request.GenerationTokens != nil {
		encoder.field("max_output_tokens", *request.GenerationTokens)
	}
	encoder.field("input", input)
	return encoder.bytes()
}

func makeToolChoice(callableTools []string, choice string) any {
	if choice == "none" || choice == "required" {
		return choice
	}
	if choice == "" {
		choice = "auto"
	}
	if choice == "auto" && len(callableTools) == 0 {
		return "none"
	}
	names := append([]string(nil), callableTools...)
	sort.Strings(names)
	allowed := make([]map[string]string, 0, len(names))
	for _, name := range names {
		allowed = append(allowed, map[string]string{"type": "function", "name": name})
	}
	return map[string]any{"type": "allowed_tools", "mode": choice, "tools": allowed}
}

func makeHeaders(request Request, identity contract.ConversationIdentity) http.Header {
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+request.Credentials.AccessToken)
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "text/event-stream")
	version := request.Config.UserAgentVersion
	if version == "" {
		version = "0.1"
	}
	if request.Config.Mode == ModeOwned {
		headers.Set("User-Agent", "kogen/0.1")
	} else {
		headers.Set("User-Agent", "kogen/"+version)
		headers.Set("ChatGPT-Account-ID", request.Credentials.AccountID)
		headers.Set("OpenAI-Beta", "responses=experimental")
		headers.Set("Originator", "kogen")
	}
	headers.Set("X-Client-Request-ID", identity.ThreadID)
	headers.Set("Session-ID", identity.CacheKey)
	headers.Set("Thread-ID", identity.ThreadID)
	headers.Set("X-Codex-Window-ID", identity.ThreadID+":0")
	headers.Set("X-Codex-Turn-Metadata", turnMetadata(identity.CacheKey, identity.ThreadID, identity.ThreadID+":0"))
	if routing := request.Conversation.RoutingState(); routing.HasCodexTurnState {
		headers.Set("X-Codex-Turn-State", string(routing.CodexTurnState))
	}
	if request.Config.Mode == ModeLite {
		headers.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true")
		headers.Set("Session_Id", identity.SessionID)
	}
	return headers
}

func turnMetadata(cacheKey, threadID, windowID string) string {
	encoded, _ := json.Marshal(map[string]string{
		"session_id": cacheKey, "thread_id": threadID,
		"window_id": windowID, "request_kind": "turn",
	})
	return string(encoded)
}

func stableID(key, label string) string {
	hash := sha256.Sum256([]byte(key + "\x00" + label))
	return fmt.Sprintf("%x", hash[:])
}

func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

type orderedObject struct {
	buffer bytes.Buffer
	first  bool
	err    error
}

func (o *orderedObject) field(name string, value any) {
	if o.err != nil {
		return
	}
	if !o.first {
		o.buffer.WriteByte(',')
	}
	o.first = false
	key, err := json.Marshal(name)
	if err != nil {
		o.err = err
		return
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		o.err = err
		return
	}
	o.buffer.Write(key)
	o.buffer.WriteByte(':')
	o.buffer.Write(encoded)
}

func (o *orderedObject) bytes() ([]byte, error) {
	if o.err != nil {
		return nil, o.err
	}
	return append(append([]byte{'{'}, o.buffer.Bytes()...), '}'), nil
}
