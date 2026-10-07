package journal

import (
	"crypto/sha256"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"kogen-go/internal/contract"
)

var (
	telemetryTokenPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,127}$`)
	modelTokenPattern     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]{0,127}$`)
	opaqueIDPattern       = regexp.MustCompile(`^[A-Za-z0-9_:-]{1,256}$`)
	pathSegmentPattern    = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,80}$`)
)

var allowedRoutingHeaders = map[string]struct{}{
	"session-id":                             {},
	"session_id":                             {},
	"thread-id":                              {},
	"x-client-request-id":                    {},
	"x-codex-turn-state":                     {},
	"x-codex-window-id":                      {},
	"x-codex-turn-metadata":                  {},
	"x-grok-conv-id":                         {},
	"x-grok-session-id":                      {},
	"x-openai-internal-codex-responses-lite": {},
}

// NullableUsage keeps unknown provider measurements distinct from measured
// zeros. A missing usage object is serialized as null; missing fields inside a
// partial usage object are also serialized as null.
type NullableUsage struct {
	Input       *int64 `json:"input"`
	CachedInput *int64 `json:"cached_input"`
	CacheWrite  *int64 `json:"cache_write"`
	Output      *int64 `json:"output"`
	Reasoning   *int64 `json:"reasoning"`
}

// TranscriptRecord is the allowlisted provider metadata persisted in
// transcript.jsonl. It deliberately has no request body, response body, prompt,
// credential, header-value, or opaque routing-state value field.
type TranscriptRecord struct {
	Kind                  string         `json:"kind"`
	Role                  string         `json:"role"`
	Provider              string         `json:"provider"`
	Model                 string         `json:"model"`
	Effort                string         `json:"effort"`
	Usage                 *NullableUsage `json:"usage"`
	WallMS                int64          `json:"wall_ms"`
	StartedAtMS           int64          `json:"started_at_ms"`
	EndedAtMS             int64          `json:"ended_at_ms"`
	PromptCacheKey        string         `json:"prompt_cache_key"`
	CacheKey              string         `json:"cache_key"`
	ThreadID              string         `json:"thread_id"`
	ConversationID        string         `json:"conversation_id"`
	EndpointHost          string         `json:"endpoint_host"`
	EndpointPath          string         `json:"endpoint_path"`
	RoutingHeaders        []string       `json:"routing_headers"`
	BodyBytes             int64          `json:"body_bytes"`
	PrefixSHA256          []string       `json:"prefix_sha256"`
	CodexTurnStatePresent bool           `json:"codex_turn_state_present"`
}

// RequestIdentity contains only stable protocol and role identifiers. It has no
// field for credentials, prompt text, or filesystem paths.
type RequestIdentity struct {
	CacheKey string
	ThreadID string
	Role     contract.RoleName
	Provider string
	Model    string
	Effort   string
}

// EvidenceFromRequest extracts journal-safe metadata from a complete wire
// request. It strips URL userinfo/query/fragment, drops non-routing header
// names, never copies header values or body bytes, and hashes only the requested
// body prefixes.
func EvidenceFromRequest(request contract.ProviderRequest, identity RequestIdentity, requestedAt, respondedAt time.Time, usage *contract.TokenUsage, prefixEnds ...int) (contract.RequestEvidence, error) {
	endpoint, err := url.Parse(request.Endpoint)
	if err != nil || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.Hostname() == "" {
		return contract.RequestEvidence{}, fmt.Errorf("%w: invalid provider endpoint", ErrInvalidRecord)
	}
	host := endpoint.Hostname()
	if port := endpoint.Port(); port != "" {
		host = net.JoinHostPort(host, port)
	}
	host, err = cleanHost(host)
	if err != nil {
		return contract.RequestEvidence{}, err
	}
	endpointPath, err := cleanEndpointPath(endpoint.EscapedPath())
	if err != nil {
		return contract.RequestEvidence{}, err
	}
	if !opaqueIDPattern.MatchString(identity.CacheKey) || !opaqueIDPattern.MatchString(identity.ThreadID) ||
		!telemetryTokenPattern.MatchString(string(identity.Role)) || !telemetryTokenPattern.MatchString(identity.Provider) ||
		!modelTokenPattern.MatchString(identity.Model) || !telemetryTokenPattern.MatchString(identity.Effort) {
		return contract.RequestEvidence{}, fmt.Errorf("%w: unsafe provider request identity", ErrInvalidRecord)
	}
	digests := make([]string, 0, len(prefixEnds))
	for _, end := range prefixEnds {
		if end < 0 || end > len(request.Body) {
			return contract.RequestEvidence{}, fmt.Errorf("%w: request prefix boundary outside body", ErrInvalidRecord)
		}
		digest := sha256.Sum256(request.Body[:end])
		digests = append(digests, fmt.Sprintf("%x", digest))
	}
	var routingHeaders []string
	codexTurnStatePresent := false
	for name := range request.Headers {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "x-codex-turn-state" {
			codexTurnStatePresent = true
		}
		if _, allowed := allowedRoutingHeaders[name]; allowed {
			routingHeaders = append(routingHeaders, name)
		}
	}
	routingHeaders, err = safeRoutingHeaders(routingHeaders)
	if err != nil {
		return contract.RequestEvidence{}, err
	}
	return contract.RequestEvidence{
		EndpointHost: host, EndpointPath: endpointPath,
		RequestedAt: requestedAt, RespondedAt: respondedAt,
		BodyBytes: int64(len(request.Body)), PrefixSHA256: digests,
		CacheKey: identity.CacheKey, ThreadID: identity.ThreadID,
		Role: identity.Role, Provider: identity.Provider, Model: identity.Model, Effort: identity.Effort,
		RoutingHeaderNames: routingHeaders, CodexTurnStatePresent: codexTurnStatePresent,
		Usage: cloneContractUsage(usage),
	}, nil
}

// TranscriptFromEvidence converts the shared transport evidence into the
// journal's strict allowlist. Header values and body bytes are not copied.
func TranscriptFromEvidence(evidence contract.RequestEvidence, wallMS int64) (TranscriptRecord, error) {
	usage := usageFromContract(evidence.Usage)
	routingHeaders, err := safeRoutingHeaders(evidence.RoutingHeaderNames)
	if err != nil {
		return TranscriptRecord{}, err
	}
	host, err := cleanHost(evidence.EndpointHost)
	if err != nil {
		return TranscriptRecord{}, err
	}
	endpointPath, err := cleanEndpointPath(evidence.EndpointPath)
	if err != nil {
		return TranscriptRecord{}, err
	}
	prefixDigests := append([]string{}, evidence.PrefixSHA256...)
	record := TranscriptRecord{
		Kind:                  "model_call",
		Role:                  string(evidence.Role),
		Provider:              evidence.Provider,
		Model:                 evidence.Model,
		Effort:                evidence.Effort,
		Usage:                 usage,
		WallMS:                wallMS,
		StartedAtMS:           evidence.RequestedAt.UnixMilli(),
		EndedAtMS:             evidence.RespondedAt.UnixMilli(),
		PromptCacheKey:        evidence.CacheKey,
		CacheKey:              evidence.CacheKey,
		ThreadID:              evidence.ThreadID,
		ConversationID:        evidence.ThreadID,
		EndpointHost:          host,
		EndpointPath:          endpointPath,
		RoutingHeaders:        routingHeaders,
		BodyBytes:             evidence.BodyBytes,
		PrefixSHA256:          prefixDigests,
		CodexTurnStatePresent: evidence.CodexTurnStatePresent,
	}
	if evidence.RequestedAt.IsZero() {
		record.StartedAtMS = 0
	}
	if evidence.RespondedAt.IsZero() {
		record.EndedAtMS = 0
	}
	if err := record.Validate(); err != nil {
		return TranscriptRecord{}, err
	}
	return record, nil
}

func (r TranscriptRecord) Validate() error {
	if r.Kind != "model_call" || !telemetryTokenPattern.MatchString(r.Role) || !telemetryTokenPattern.MatchString(r.Provider) || !modelTokenPattern.MatchString(r.Model) || !telemetryTokenPattern.MatchString(r.Effort) {
		return fmt.Errorf("%w: invalid transcript identity", ErrInvalidRecord)
	}
	if !opaqueIDPattern.MatchString(r.CacheKey) || !opaqueIDPattern.MatchString(r.ThreadID) || r.ConversationID != r.ThreadID || r.PromptCacheKey != r.CacheKey {
		return fmt.Errorf("%w: unsafe transcript protocol identity", ErrInvalidRecord)
	}
	if _, err := cleanHost(r.EndpointHost); err != nil {
		return err
	}
	safePath, err := cleanEndpointPath(r.EndpointPath)
	if err != nil {
		return err
	}
	if safePath != r.EndpointPath {
		return fmt.Errorf("%w: transcript endpoint path is not redacted", ErrInvalidRecord)
	}
	if r.WallMS < 0 || r.BodyBytes < 0 || r.StartedAtMS < 0 || r.EndedAtMS < 0 {
		return fmt.Errorf("%w: negative telemetry measurement", ErrInvalidRecord)
	}
	for _, digest := range r.PrefixSHA256 {
		if !isHex(digest, 64) {
			return fmt.Errorf("%w: invalid prefix digest", ErrInvalidRecord)
		}
	}
	if _, err := safeRoutingHeaders(r.RoutingHeaders); err != nil {
		return err
	}
	if r.Usage != nil {
		for _, value := range []*int64{r.Usage.Input, r.Usage.CachedInput, r.Usage.CacheWrite, r.Usage.Output, r.Usage.Reasoning} {
			if value != nil && *value < 0 {
				return fmt.Errorf("%w: negative usage measurement", ErrInvalidRecord)
			}
		}
		if r.Usage.Input != nil && r.Usage.CachedInput != nil && *r.Usage.CachedInput > *r.Usage.Input {
			return fmt.Errorf("%w: cached input exceeds input usage", ErrInvalidRecord)
		}
	}
	return nil
}

// ModelStageEvent makes the Build's per-request event from the same
// privacy-filtered record written to the transcript.
func ModelStageEvent(evidence contract.RequestEvidence, stage, rung string, wallMS int64) (RunEvent, error) {
	record, err := TranscriptFromEvidence(evidence, wallMS)
	if err != nil {
		return RunEvent{}, err
	}
	if !telemetryTokenPattern.MatchString(stage) || (rung != "" && !telemetryTokenPattern.MatchString(rung)) {
		return RunEvent{}, fmt.Errorf("%w: invalid model stage identity", ErrInvalidRecord)
	}
	event := NewRunEvent("model_stage", record.EndedAtMS)
	values := map[string]any{
		"stage": stage, "rung": rung, "model": record.Model, "effort": record.Effort,
		"tokens": record.Usage, "wall_ms": wallMS, "prompt_cache_key": record.PromptCacheKey,
		"cache_key": record.CacheKey, "thread_id": record.ThreadID,
		"conversation_id": record.ConversationID, "endpoint_host": record.EndpointHost,
		"endpoint_path": record.EndpointPath, "routing_headers": record.RoutingHeaders,
		"body_bytes": record.BodyBytes, "prefix_sha256": record.PrefixSHA256,
		"request_started_ms": record.StartedAtMS, "response_ended_ms": record.EndedAtMS,
		"codex_turn_state_present": record.CodexTurnStatePresent,
	}
	for key, value := range values {
		if err := event.Set(key, value); err != nil {
			return RunEvent{}, err
		}
	}
	return event, nil
}

func usageFromContract(usage *contract.TokenUsage) *NullableUsage {
	if usage == nil {
		return nil
	}
	return &NullableUsage{
		Input: cloneInt64(usage.Input), CachedInput: cloneInt64(usage.CachedInput),
		CacheWrite: cloneInt64(usage.CacheWrite), Output: cloneInt64(usage.Output),
		Reasoning: cloneInt64(usage.Reasoning),
	}
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func safeRoutingHeaders(headers []string) ([]string, error) {
	clean := make([]string, 0, len(headers))
	seen := make(map[string]struct{}, len(headers))
	for _, header := range headers {
		name := strings.ToLower(strings.TrimSpace(header))
		if _, ok := allowedRoutingHeaders[name]; !ok {
			return nil, fmt.Errorf("%w: routing header name is not allowlisted", ErrInvalidRecord)
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		clean = append(clean, name)
	}
	sort.Strings(clean)
	return clean, nil
}

func cleanHost(host string) (string, error) {
	if host == "" || strings.ContainsAny(host, "/?#@\\\r\n\t ") {
		return "", fmt.Errorf("%w: unsafe endpoint host", ErrInvalidRecord)
	}
	u, err := url.Parse("https://" + host)
	if err != nil || u.User != nil || u.Host != host || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%w: unsafe endpoint host", ErrInvalidRecord)
	}
	return strings.ToLower(host), nil
}

func cleanEndpointPath(endpointPath string) (string, error) {
	if endpointPath == "" || !strings.HasPrefix(endpointPath, "/") || strings.ContainsAny(endpointPath, "?#\\\r\n\t ") || strings.Contains(endpointPath, "//") {
		return "", fmt.Errorf("%w: unsafe endpoint path", ErrInvalidRecord)
	}
	parsed, err := url.ParseRequestURI(endpointPath)
	if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.IsAbs() {
		return "", fmt.Errorf("%w: unsafe endpoint path", ErrInvalidRecord)
	}
	segments := strings.Split(strings.TrimPrefix(parsed.EscapedPath(), "/"), "/")
	for i, segment := range segments {
		if segment == "" {
			continue
		}
		if !pathSegmentPattern.MatchString(segment) || segment == "." || segment == ".." {
			return "", fmt.Errorf("%w: unsafe endpoint path", ErrInvalidRecord)
		}
		if looksCredentialLike(segment) {
			segments[i] = "redacted"
		}
	}
	return "/" + strings.Join(segments, "/"), nil
}

func looksCredentialLike(segment string) bool {
	lower := strings.ToLower(segment)
	if len(segment) > 32 && isHex(segment, len(segment)) {
		return true
	}
	if strings.Contains(segment, "@") || strings.HasPrefix(lower, "bearer") ||
		strings.Contains(lower, "secret") || strings.Contains(lower, "credential") ||
		strings.Contains(lower, "api_key") || strings.Contains(lower, "apikey") || strings.Contains(lower, "token") {
		return true
	}
	if len(segment) > 32 && strings.Count(segment, ".") >= 2 {
		return true
	}
	return false
}

func cloneContractUsage(usage *contract.TokenUsage) *contract.TokenUsage {
	if usage == nil {
		return nil
	}
	return &contract.TokenUsage{
		Input: cloneInt64(usage.Input), CachedInput: cloneInt64(usage.CachedInput),
		CacheWrite: cloneInt64(usage.CacheWrite), Output: cloneInt64(usage.Output),
		Reasoning: cloneInt64(usage.Reasoning),
	}
}
