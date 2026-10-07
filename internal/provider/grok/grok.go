// Package grok implements the xAI Responses adapter over shared session,
// transport, retry, and SSE components.
package grok

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"

	authgrok "kogen-go/internal/auth/grok"
	"kogen-go/internal/contract"
	"kogen-go/internal/provider/retry"
	"kogen-go/internal/provider/session"
	"kogen-go/internal/provider/sse"
	"kogen-go/internal/provider/transport"
	"kogen-go/internal/provider/wire"
)

const (
	DefaultEndpoint = "https://cli-chat-proxy.grok.com/v1/responses"
	DefaultVersion  = "0.1.0"
)

var grokPrefixRegistry wire.PrefixRegistry

// Config selects the Grok account and endpoint. Endpoint is an explicit fake
// provider seam; the production default is xAI's CLI proxy.
type Config struct {
	Auth          *authgrok.Client
	Transport     *transport.Client
	Endpoint      string
	ClientVersion string
}

// Adapter sends complete Grok Responses requests and applies the shared
// retry policy. Its auth manager is bound to one selected Grok account.
type Adapter struct {
	auth      *authgrok.Client
	transport *transport.Client
	endpoint  string
	version   string
}

// Request describes one logical provider turn. The conversation pointer must
// be reused for retries and same-conversation repairs.
type Request struct {
	Conversation     *session.Conversation
	Prefix           wire.Prefix
	RoleInstructions string
	Mode             retry.Mode
	Budget           *retry.WallBudget
	RetryState       *retry.State
	Clock            contract.Clock
	Jitter           contract.JitterSource
}

// Result keeps the parsed response alongside per-request retry and telemetry
// facts; credentials and header values are never returned here.
type Result struct {
	Response *sse.Response
	Attempts []contract.RequestEvidence
	Retries  []retry.Event
	Resumed  bool
}

// EncodedRequest is the exact request produced by the Grok wire adapter.
type EncodedRequest struct {
	Request         contract.ProviderRequest
	StaticPrefixSHA string
}

// String omits body and header values while retaining useful wire metadata.
func (r EncodedRequest) String() string {
	endpointHost, endpointPath := "", ""
	if endpoint, err := url.Parse(r.Request.Endpoint); err == nil {
		endpointHost, endpointPath = endpoint.Hostname(), endpoint.EscapedPath()
	}
	return fmt.Sprintf("GrokEncodedRequest{host=%q path=%q body_bytes=%d prefix_sha256=%s}", endpointHost, endpointPath, len(r.Request.Body), r.StaticPrefixSHA)
}

func (r EncodedRequest) GoString() string { return r.String() }

// New creates an adapter. When Transport is nil, standard provider deadlines
// and the KOGEN_TIME_SCALE test seam are applied.
func New(config Config) (*Adapter, error) {
	if config.Auth == nil {
		return nil, errors.New("grok adapter requires a saved-account auth client")
	}
	if config.Endpoint == "" {
		config.Endpoint = os.Getenv("KOGEN_PROVIDER_URL")
	}
	if config.Endpoint == "" {
		config.Endpoint = DefaultEndpoint
	}
	if err := validateEndpoint(config.Endpoint); err != nil {
		return nil, err
	}
	if config.ClientVersion == "" {
		config.ClientVersion = DefaultVersion
	}
	if !safeHeaderToken(config.ClientVersion) {
		return nil, errors.New("grok adapter client version is invalid")
	}
	if config.Transport == nil {
		var err error
		config.Transport, err = transport.NewFromEnvironment(nil)
		if err != nil {
			return nil, err
		}
	}
	return &Adapter{auth: config.Auth, transport: config.Transport, endpoint: config.Endpoint, version: config.ClientVersion}, nil
}

// Encode builds one complete Grok HTTP request. Each call generates a fresh
// request UUID, so callers must invoke it for every physical attempt.
func (a *Adapter) Encode(request Request, accessToken string) (EncodedRequest, error) {
	if a == nil {
		return EncodedRequest{}, errors.New("grok adapter is not configured")
	}
	if request.Prefix.Digest() == "" {
		request.Prefix = wire.DefaultPrefix()
	}
	return encode(a.endpoint, a.version, request, accessToken)
}

// Respond performs a logical Grok turn, including per-attempt auth refresh,
// provider retry/continuation, SSE assembly, and nullable usage retention.
func (a *Adapter) Respond(ctx context.Context, request Request) (Result, error) {
	var result Result
	if a == nil || a.auth == nil || a.transport == nil {
		return result, errors.New("grok adapter is not configured")
	}
	if ctx == nil {
		return result, errors.New("grok provider context is required")
	}
	if request.Conversation == nil {
		return result, errors.New("grok request requires a persistent conversation")
	}
	if request.Mode != retry.ModeBuild && request.Mode != retry.ModeShape {
		return result, errors.New("grok request mode must be build or shape")
	}
	identity := request.Conversation.Identity()
	if identity.Provider != "grok" || identity.Model == "" || identity.Effort == "" {
		return result, errors.New("grok request conversation must bind an effective Grok provider, model, and effort")
	}
	if request.Prefix.Digest() == "" {
		request.Prefix = wire.DefaultPrefix()
	}
	if err := validatePrefix(request.Prefix); err != nil {
		return result, err
	}
	if err := grokPrefixRegistry.Register("grok", request.Prefix); err != nil {
		return result, err
	}
	initialBody, err := encodeBody(request)
	if err != nil {
		return result, err
	}
	baseHistory, err := request.Conversation.WireHistory()
	if err != nil {
		return result, err
	}
	current := retry.Request{
		Provider: "grok", Model: identity.Model, Effort: identity.Effort,
		Body: bytes.Clone(initialBody), History: baseHistory,
	}
	lastAccessToken := ""
	appendedRetryItems := 0

	attempts := make([]contract.RequestEvidence, 0, 2)
	attempt := func(attemptCtx context.Context, current retry.Request, mode retry.AttemptMode) (retry.Outcome, error) {
		transportRequest := transport.Request{
			Provider: transport.ProviderGrok,
			Evidence: contract.RequestEvidence{
				CacheKey: identity.CacheKey, ThreadID: identity.ThreadID,
				Role: identity.Role, Provider: "grok", Model: current.Model,
				Effort: current.Effort, PrefixSHA256: []string{request.Prefix.Digest()},
			},
			Prepare: func(prepareCtx context.Context) (*http.Request, error) {
				var credentialAccess string
				if mode.ForceRefresh {
					credential, _, err := a.auth.AfterUnauthorized(prepareCtx, lastAccessToken)
					if err != nil {
						return nil, err
					}
					credentialAccess = credential.AccessToken
				} else {
					credential, err := a.auth.ForRequest(prepareCtx)
					if err != nil {
						return nil, err
					}
					credentialAccess = credential.AccessToken
				}
				lastAccessToken = credentialAccess
				encoded, err := encode(a.endpoint, a.version, request, credentialAccess)
				if err != nil {
					return nil, err
				}
				if !bytes.Equal(encoded.Request.Body, current.Body) {
					return nil, errors.New("grok retry body changed outside an appended continuation")
				}
				providerRequest, err := http.NewRequestWithContext(prepareCtx, http.MethodPost, encoded.Request.Endpoint, bytes.NewReader(current.Body))
				if err != nil {
					return nil, errors.New("grok provider request could not be constructed")
				}
				providerRequest.Header = encoded.Request.Headers.Clone()
				return providerRequest, nil
			},
		}
		response, doErr := a.transport.Do(attemptCtx, transportRequest)
		if doErr != nil {
			var failure *transport.Failure
			if errors.As(doErr, &failure) {
				attempts = append(attempts, failure.Evidence)
				if failure.Evidence.EndpointHost != "" {
					request.Conversation.AppendAttemptUsage(nil)
				}
				if failure.Kind == transport.KindLogin && failure.Message == "Grok rejected this session; run kogen provider login grok." {
					return retry.Outcome{}, &retry.Failure{
						Class: retry.ClassLogin, Message: "Grok rejected this session; run `kogen provider login grok`.",
						Cause: doErr, RetryAfterMS: failure.RetryAfterMS,
					}
				}
				if failure.Kind == transport.KindLogin && failure.Message == "This Grok account cannot access the requested model." {
					return retry.Outcome{}, &retry.Failure{Class: retry.ClassUnsupported, Message: failure.Message, Cause: doErr}
				}
			}
			return retry.Outcome{}, doErr
		}
		assembler := sse.NewAssembler()
		feedErr := assembler.Feed(response.Body)
		assembled, parseErr := assembler.Finish()
		if feedErr != nil && parseErr == nil {
			parseErr = feedErr
		}
		if parseErr != nil {
			var providerErr *sse.ProviderError
			var usage *contract.TokenUsage
			if errors.As(parseErr, &providerErr) && providerErr.Usage != nil {
				usage = contractUsage(*providerErr.Usage)
			}
			request.Conversation.AppendAttemptUsage(usage)
			evidence := response.Evidence
			evidence.Usage = usage
			attempts = append(attempts, evidence)
			return retry.Outcome{}, classifySSEError(parseErr, assembler.CollectedItems())
		}
		usage := contractUsage(assembled.Usage)
		request.Conversation.AppendAttemptUsage(usage)
		evidence := response.Evidence
		evidence.Usage = usage
		attempts = append(attempts, evidence)
		return retry.Outcome{Value: assembled, RawItems: assembled.RawItems, Text: assembled.Text}, nil
	}

	state := request.RetryState
	if state == nil {
		state = &retry.State{}
	}
	retryResult, err := retry.Run(ctx, current, retry.Config{
		Mode: request.Mode, Role: identity.Role,
		Current:    contract.RoleSettings{Provider: "grok", Model: identity.Model, Effort: identity.Effort},
		FallbackOn: true, Refreshable: true, Budget: request.Budget,
		Clock: request.Clock, Jitter: request.Jitter,
		Hooks: retry.Hooks{
			Continue: func(_ context.Context, current retry.Request, items []json.RawMessage, instruction string) (retry.Request, error) {
				if instruction != session.ContinuationInstruction {
					return retry.Request{}, errors.New("grok continuation instruction did not match the shared policy")
				}
				if err := request.Conversation.AppendContinuation(items); err != nil {
					return retry.Request{}, err
				}
				appendedRetryItems += len(items)
				body, err := encodeBody(request)
				if err != nil {
					return retry.Request{}, err
				}
				history, err := request.Conversation.WireHistory()
				if err != nil {
					return retry.Request{}, err
				}
				current.Body = bytes.Clone(body)
				current.History = history
				current.Model = identity.Model
				current.Effort = identity.Effort
				return current, nil
			},
		},
	}, state, attempt)
	result.Attempts = attempts
	result.Retries = append([]retry.Event(nil), retryResult.Events...)
	result.Resumed = retryResult.Resumed
	if err != nil {
		for _, raw := range retryResult.RawItems[appendedRetryItems:] {
			if appendErr := request.Conversation.Append(contract.SessionResponseItem, raw); appendErr != nil {
				return result, appendErr
			}
		}
		return result, err
	}
	assembled, ok := retryResult.Outcome.Value.(*sse.Response)
	if !ok || assembled == nil {
		return result, errors.New("grok provider returned an invalid assembled response")
	}
	for _, raw := range assembled.RawItems {
		if err := request.Conversation.Append(contract.SessionResponseItem, raw); err != nil {
			return result, err
		}
	}
	assembledCopy := *assembled
	assembledCopy.RawItems = cloneRawItems(assembled.RawItems)
	assembledCopy.Text = retryResult.Outcome.Text
	result.Response = &assembledCopy
	return result, nil
}

func encode(endpoint, version string, request Request, accessToken string) (EncodedRequest, error) {
	if accessToken == "" {
		return EncodedRequest{}, errors.New("grok access token is missing")
	}
	if request.Conversation == nil {
		return EncodedRequest{}, errors.New("grok request requires a persistent conversation")
	}
	identity := request.Conversation.Identity()
	if identity.Provider != "grok" || identity.CacheKey == "" || identity.ThreadID == "" || identity.Model == "" || identity.Effort == "" {
		return EncodedRequest{}, errors.New("grok request conversation identity is incomplete")
	}
	if strings.ContainsAny(identity.Model+identity.Effort, "\r\n\x00") || strings.TrimSpace(identity.Model) == "" || strings.TrimSpace(identity.Effort) == "" {
		return EncodedRequest{}, errors.New("grok model and effort must be nonempty protocol values")
	}
	parsedEndpoint, err := url.Parse(endpoint)
	if err != nil || (parsedEndpoint.Scheme != "http" && parsedEndpoint.Scheme != "https") || parsedEndpoint.Hostname() == "" || parsedEndpoint.User != nil || parsedEndpoint.Fragment != "" {
		return EncodedRequest{}, errors.New("grok provider endpoint is invalid")
	}
	if version == "" || !safeHeaderToken(version) {
		return EncodedRequest{}, errors.New("grok client version is invalid")
	}
	encodedBody, err := encodeBody(request)
	if err != nil {
		return EncodedRequest{}, err
	}
	requestID, err := NewRequestID()
	if err != nil {
		return EncodedRequest{}, err
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+accessToken)
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "text/event-stream")
	headers.Set("User-Agent", "kogen/"+version)
	headers.Set("X-Xai-Token-Auth", "xai-grok-cli")
	headers.Set("X-AuthenticateResponse", "authenticate-response")
	headers.Set("X-Grok-Model-Override", identity.Model)
	headers.Set("X-Grok-Client-Identifier", "kogen")
	headers.Set("X-Grok-Client-Mode", "headless")
	headers.Set("X-Grok-Client-Version", version)
	headers.Set("X-Grok-Req-Id", requestID)
	if identity.CacheKey != "" {
		headers.Set("X-Grok-Conv-Id", identity.CacheKey)
		headers.Set("X-Grok-Session-Id", identity.CacheKey)
	}
	return EncodedRequest{
		Request:         contract.ProviderRequest{Endpoint: parsedEndpoint.String(), Headers: headers, Body: encodedBody},
		StaticPrefixSHA: request.Prefix.Digest(),
	}, nil
}

func encodeBody(request Request) ([]byte, error) {
	if request.Conversation == nil {
		return nil, errors.New("grok request requires a persistent conversation")
	}
	identity := request.Conversation.Identity()
	if identity.Provider != "grok" || identity.CacheKey == "" || identity.ThreadID == "" || identity.Model == "" || identity.Effort == "" {
		return nil, errors.New("grok request conversation identity is incomplete")
	}
	if strings.ContainsAny(identity.Model+identity.Effort, "\r\n\x00") || strings.TrimSpace(identity.Model) == "" || strings.TrimSpace(identity.Effort) == "" {
		return nil, errors.New("grok model and effort must be nonempty protocol values")
	}
	if err := validatePrefix(request.Prefix); err != nil {
		return nil, err
	}
	if err := grokPrefixRegistry.Register("grok", request.Prefix); err != nil {
		return nil, err
	}
	roleItem, err := roleInstructionItem(identity.ThreadID, request.RoleInstructions)
	if err != nil {
		return nil, err
	}
	history, err := request.Conversation.WireHistory()
	if err != nil {
		return nil, err
	}
	input := make([]json.RawMessage, 0, len(history)+1)
	if len(roleItem) > 0 {
		input = append(input, roleItem)
	}
	input = append(input, history...)
	body := orderedObject{first: true}
	body.field("model", identity.Model)
	body.field("instructions", request.Prefix.GenericInstructions())
	body.field("tools", request.Prefix.ToolSchemas())
	body.field("reasoning", map[string]string{"effort": identity.Effort})
	body.field("store", false)
	body.field("stream", true)
	body.field("include", []string{"reasoning.encrypted_content"})
	if identity.CacheKey != "" {
		body.field("prompt_cache_key", identity.CacheKey)
	}
	body.field("input", input)
	return body.bytes()
}

func roleInstructionItem(threadID, instructions string) (json.RawMessage, error) {
	if instructions == "" {
		return nil, nil
	}
	hash := sha256.Sum256([]byte(threadID + "\x00role_instructions"))
	item := map[string]any{
		"id": hex.EncodeToString(hash[:]), "type": "message", "role": "developer",
		"content": []any{map[string]any{"type": "input_text", "text": instructions}},
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return nil, err
	}
	return wire.CanonicalJSON(raw)
}

func validatePrefix(prefix wire.Prefix) error {
	if prefix.Digest() == "" {
		return errors.New("grok request requires the canonical static prefix")
	}
	schemas := prefix.ToolSchemas()
	wantNames := []string{"edit", "finish", "read", "search", "shell", "tool_output", "write"}
	if len(schemas) != len(wantNames) {
		return errors.New("grok request requires the complete seven-schema tool union")
	}
	for index, schema := range schemas {
		var item struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(schema, &item); err != nil || item.Type != "function" || item.Name != wantNames[index] {
			return errors.New("grok request requires the canonical ordered seven-schema tool union")
		}
	}
	return nil
}

func validateEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("grok provider endpoint is invalid")
	}
	return nil
}

func safeHeaderToken(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, b := range []byte(value) {
		if b < 0x21 || b > 0x7e {
			return false
		}
	}
	return true
}

// NewRequestID returns a random RFC 4122 version-4 identifier.
func NewRequestID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create Grok request identity: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func classifySSEError(err error, partialItems []json.RawMessage) error {
	var providerErr *sse.ProviderError
	if !errors.As(err, &providerErr) {
		return &retry.Failure{Class: retry.ClassMalformed, Message: "Grok returned a malformed response stream.", Cause: err, PartialItems: cloneRawItems(partialItems)}
	}
	class := retry.ClassMalformed
	switch providerErr.Kind {
	case sse.ErrorTransport:
		class = retry.ClassTransport
	case sse.ErrorUsageLimit:
		class = retry.ClassUsageLimit
	case sse.ErrorOverload:
		class = retry.ClassOverload
	case sse.ErrorIncomplete:
		class = retry.ClassIncomplete
	}
	return &retry.Failure{
		Class: class, Message: providerErr.Message, Cause: err,
		RetryAfterMS: cloneUint64(providerErr.RetryAfterMS), PartialItems: cloneRawItems(partialItems),
	}
}

func contractUsage(usage sse.Usage) *contract.TokenUsage {
	return &contract.TokenUsage{
		Input: uintToInt64(usage.Input), CachedInput: uintToInt64(usage.CachedInput),
		CacheWrite: uintToInt64(usage.CacheWrite), Output: uintToInt64(usage.Output),
		Reasoning: uintToInt64(usage.Reasoning),
	}
}

func uintToInt64(value *uint64) *int64 {
	if value == nil || *value > math.MaxInt64 {
		return nil
	}
	converted := int64(*value)
	return &converted
}

func cloneUint64(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneRawItems(items []json.RawMessage) []json.RawMessage {
	if items == nil {
		return nil
	}
	cloned := make([]json.RawMessage, len(items))
	for i := range items {
		cloned[i] = bytes.Clone(items[i])
	}
	return cloned
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
