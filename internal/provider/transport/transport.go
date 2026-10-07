package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/provider/sse"
)

const (
	DefaultFirstByteDeadline = 120 * time.Second
	DefaultIdleDeadline      = 90 * time.Second
	DefaultTotalDeadline     = 20 * time.Minute
	DefaultMaxResponseBytes  = int64(16_000_000)
)

// Kind is the classified result of one HTTP attempt.
type Kind string

const (
	KindTimeout    Kind = "timeout"
	KindStall      Kind = "stall"
	KindTransport  Kind = "transport"
	KindOverload   Kind = "overload"
	KindMalformed  Kind = "malformed"
	KindUsageLimit Kind = "usage_limit"
	KindLogin      Kind = "login"
	KindCancelled  Kind = "cancelled"
)

type Provider string

const (
	ProviderChatGPT Provider = "chatgpt"
	ProviderGrok    Provider = "grok"
)

// Deadlines are per physical HTTP attempt. The first-byte clock starts before
// Prepare runs, so token refresh, credential loading, and request construction
// are inside the attempt wall time. Test callers may provide scaled values.
type Deadlines struct {
	FirstByte    time.Duration
	Idle         time.Duration
	Total        time.Duration
	MaxBodyBytes int64
}

func StandardDeadlines() Deadlines {
	return Deadlines{
		FirstByte:    DefaultFirstByteDeadline,
		Idle:         DefaultIdleDeadline,
		Total:        DefaultTotalDeadline,
		MaxBodyBytes: DefaultMaxResponseBytes,
	}
}

// DeadlinesFromEnvironment applies KOGEN_TIME_SCALE to the provider clocks for
// conformance runs. The body-size limit is an unscaled byte count.
func DeadlinesFromEnvironment() (Deadlines, error) {
	deadlines := StandardDeadlines()
	raw := strings.TrimSpace(os.Getenv("KOGEN_TIME_SCALE"))
	if raw == "" {
		return deadlines, nil
	}
	scale, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(scale) || math.IsInf(scale, 0) || scale <= 0 {
		return Deadlines{}, errors.New("KOGEN_TIME_SCALE must be a finite positive number")
	}
	deadlines.FirstByte = scaledDuration(deadlines.FirstByte, scale)
	deadlines.Idle = scaledDuration(deadlines.Idle, scale)
	deadlines.Total = scaledDuration(deadlines.Total, scale)
	return deadlines, deadlines.validate()
}

func (d Deadlines) validate() error {
	if d.FirstByte <= 0 || d.Idle <= 0 || d.Total <= 0 || d.MaxBodyBytes <= 0 {
		return errors.New("provider attempt deadlines and body limit must be positive")
	}
	return nil
}

// Request prepares one HTTP request after all attempt timers have started.
// Prepare must honor ctx while doing credential or refresh work. BodyBytes may
// be supplied when the request body uses unknown-length streaming.
type Request struct {
	Provider  Provider
	Prepare   func(context.Context) (*http.Request, error)
	BodyBytes int64
	Evidence  contract.RequestEvidence
}

func (r Request) String() string {
	return fmt.Sprintf("transport.Request{provider=%q}", r.Provider)
}

func (r Request) GoString() string { return r.String() }

// Response contains the bounded raw body for the provider SSE assembler.
// TurnState is opaque and intended only for the same conversation thread.
type Response struct {
	StatusCode      int
	Headers         http.Header
	Body            []byte
	BodyBytes       uint64
	FirstBodyByteAt time.Time
	TurnState       []byte
	Evidence        contract.RequestEvidence
}

func (r Response) String() string {
	return fmt.Sprintf("transport.Response{status=%d header_names=%q body_bytes=%d turn_state_present=%t}", r.StatusCode, headerNames(r.Headers), len(r.Body), len(r.TurnState) > 0)
}

func (r Response) GoString() string { return r.String() }

// Failure reports safe timing and partial-body facts. Its diagnostic format
// omits response bytes and header values.
type Failure struct {
	Kind            Kind
	Message         string
	Cause           error
	RetryAfterMS    *uint64
	PartialBody     []byte
	BodyBytes       uint64
	FirstBodyByteAt time.Time
	Evidence        contract.RequestEvidence
}

func (f *Failure) Error() string {
	if f == nil {
		return "<nil>"
	}
	if f.Message != "" {
		return f.Message
	}
	return string(f.Kind)
}

func (f *Failure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Cause
}

// RetryClass lets provider/retry consume transport failures without creating
// a package dependency in the other direction.
func (f *Failure) RetryClass() string {
	if f == nil {
		return ""
	}
	return string(f.Kind)
}

func (f *Failure) RetryAfterMillis() *uint64 {
	if f == nil || f.RetryAfterMS == nil {
		return nil
	}
	value := *f.RetryAfterMS
	return &value
}

// RetryItems exposes only complete raw SSE output items from an interrupted
// response. The retry package appends them to the existing request history.
func (f *Failure) RetryItems() []json.RawMessage {
	if f == nil || len(f.PartialBody) == 0 {
		return nil
	}
	switch f.Kind {
	case KindTimeout, KindStall, KindTransport, KindMalformed:
	default:
		return nil
	}
	assembler := sse.NewAssembler()
	_ = assembler.Feed(f.PartialBody)
	return assembler.CollectedItems()
}

func (f *Failure) String() string {
	if f == nil {
		return "transport.Failure<nil>"
	}
	return fmt.Sprintf("transport.Failure{kind=%q body_bytes=%d}", f.Kind, len(f.PartialBody))
}

func (f *Failure) GoString() string { return f.String() }

type Client struct {
	http      *http.Client
	deadlines Deadlines
}

// New creates a transport with explicit per-attempt limits. A nil HTTP client
// uses net/http defaults without a competing Client.Timeout deadline.
func New(client *http.Client, deadlines Deadlines) (*Client, error) {
	if err := deadlines.validate(); err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{}
	}
	clientCopy := *client
	clientCopy.Timeout = 0
	return &Client{http: &clientCopy, deadlines: deadlines}, nil
}

func NewStandard(client *http.Client) *Client {
	transport, _ := New(client, StandardDeadlines())
	return transport
}

func NewFromEnvironment(client *http.Client) (*Client, error) {
	deadlines, err := DeadlinesFromEnvironment()
	if err != nil {
		return nil, err
	}
	return New(client, deadlines)
}

// Do executes exactly one HTTP attempt. Retry decisions belong to
// provider/retry; callers must not wrap this operation in another retry loop.
func (c *Client) Do(ctx context.Context, input Request) (*Response, error) {
	if c == nil || c.http == nil {
		return nil, failure(input, time.Now(), KindMalformed, "Provider transport is not configured.", nil, nil, 0, time.Time{}, nil, nil)
	}
	started := time.Now()
	if err := ctx.Err(); err != nil {
		return nil, failure(input, started, contextKind(err), contextMessage(err), err, nil, 0, time.Time{}, nil, nil)
	}
	if input.Prepare == nil {
		return nil, failure(input, started, KindMalformed, "Provider request is not prepared.", nil, nil, 0, time.Time{}, nil, nil)
	}

	attemptCtx, cancel := context.WithCancelCause(ctx)
	watch := &deadlineWatch{cancel: cancel, idle: c.deadlines.Idle}
	watch.start(c.deadlines.FirstByte, c.deadlines.Total)
	defer func() {
		watch.stop()
		cancel(context.Canceled)
	}()

	request, err := input.Prepare(attemptCtx)
	if err != nil {
		if kind := watch.failure(); kind != "" {
			return nil, failure(input, started, kind, message(input.Provider, kind), err, nil, 0, time.Time{}, nil, nil)
		}
		if ctx.Err() != nil {
			return nil, failure(input, started, contextKind(ctx.Err()), contextMessage(ctx.Err()), ctx.Err(), nil, 0, time.Time{}, nil, nil)
		}
		return nil, err
	}
	if request == nil || request.URL == nil {
		return nil, failure(input, started, KindMalformed, "Provider request is malformed.", nil, nil, 0, time.Time{}, nil, nil)
	}
	if (request.URL.Scheme != "http" && request.URL.Scheme != "https") || request.URL.Host == "" || request.URL.User != nil {
		return nil, failure(input, started, KindMalformed, "Provider endpoint is invalid.", nil, nil, 0, time.Time{}, request, nil)
	}
	request = request.Clone(attemptCtx)

	response, err := c.http.Do(request)
	if err != nil {
		kind := watch.failure()
		if kind == "" {
			switch {
			case errors.Is(ctx.Err(), context.DeadlineExceeded):
				kind = KindTimeout
			case ctx.Err() != nil, errors.Is(err, context.Canceled):
				kind = KindCancelled
			case isTimeout(err):
				kind = KindTimeout
			default:
				kind = KindTransport
			}
		}
		cause := err
		if ctx.Err() != nil {
			cause = ctx.Err()
			kind = contextKind(ctx.Err())
		}
		return nil, failure(input, started, kind, message(input.Provider, kind), cause, nil, 0, watch.firstByteAt(), request, nil)
	}
	defer response.Body.Close()

	turnState := []byte(nil)
	if input.Provider != ProviderGrok {
		turnState = []byte(response.Header.Get("x-codex-turn-state"))
	}
	body, bodyBytes, readErr := readBody(response.Body, c.deadlines.MaxBodyBytes, watch)
	if readErr != nil {
		kind := watch.failure()
		if kind == "" {
			switch {
			case ctx.Err() != nil:
				kind = contextKind(ctx.Err())
			case isTimeout(readErr) && watch.seenBody():
				kind = KindStall
			case isTimeout(readErr):
				kind = KindTimeout
			default:
				kind = KindTransport
			}
		}
		return nil, failure(input, started, kind, message(input.Provider, kind), readErr, body, bodyBytes, watch.firstByteAt(), request, response)
	}
	if bodyBytes > uint64(c.deadlines.MaxBodyBytes) {
		return nil, failure(input, started, KindMalformed, bodyLimitMessage(input.Provider), nil, body, bodyBytes, watch.firstByteAt(), request, response)
	}
	if kind := watch.failure(); kind != "" {
		return nil, failure(input, started, kind, message(input.Provider, kind), nil, body, bodyBytes, watch.firstByteAt(), request, response)
	}
	if ctx.Err() != nil {
		return nil, failure(input, started, contextKind(ctx.Err()), contextMessage(ctx.Err()), ctx.Err(), nil, bodyBytes, watch.firstByteAt(), request, response)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		kind, publicMessage := classifyHTTP(input.Provider, response.StatusCode, body)
		providerFailure := failure(input, started, kind, publicMessage, nil, body, bodyBytes, watch.firstByteAt(), request, response)
		providerFailure.PartialBody = nil
		return nil, providerFailure
	}

	evidence := evidenceFor(input, request, response.Header, started, time.Now())
	return &Response{
		StatusCode:      response.StatusCode,
		Headers:         response.Header.Clone(),
		Body:            body,
		BodyBytes:       bodyBytes,
		FirstBodyByteAt: watch.firstByteAt(),
		TurnState:       turnState,
		Evidence:        evidence,
	}, nil
}

type deadlineWatch struct {
	mu             sync.Mutex
	cancel         context.CancelCauseFunc
	idle           time.Duration
	seen           bool
	firstAt        time.Time
	kind           Kind
	firstTimer     *time.Timer
	totalTimer     *time.Timer
	idleTimer      *time.Timer
	idleGeneration uint64
}

func (w *deadlineWatch) start(first, total time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.firstTimer = time.AfterFunc(first, w.firstByteExpired)
	w.totalTimer = time.AfterFunc(total, func() { w.fire(KindTimeout) })
}

func (w *deadlineWatch) firstByteExpired() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.seen && w.kind == "" {
		w.kind = KindTimeout
		w.cancel(errors.New(string(KindTimeout)))
	}
}

func (w *deadlineWatch) fire(kind Kind) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.kind == "" {
		w.kind = kind
		w.cancel(errors.New(string(kind)))
	}
}

func (w *deadlineWatch) gotBodyByte() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.seen {
		w.seen = true
		w.firstAt = time.Now()
		if w.firstTimer != nil {
			w.firstTimer.Stop()
		}
	}
	w.idleGeneration++
	generation := w.idleGeneration
	if w.idleTimer != nil {
		w.idleTimer.Stop()
	}
	w.idleTimer = time.AfterFunc(w.idle, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if generation == w.idleGeneration && w.kind == "" {
			w.kind = KindStall
			w.cancel(errors.New(string(KindStall)))
		}
	})
}

func (w *deadlineWatch) seenBody() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seen
}

func (w *deadlineWatch) firstByteAt() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.firstAt
}

func (w *deadlineWatch) failure() Kind {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.kind
}

func (w *deadlineWatch) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.firstTimer != nil {
		w.firstTimer.Stop()
	}
	if w.totalTimer != nil {
		w.totalTimer.Stop()
	}
	if w.idleTimer != nil {
		w.idleTimer.Stop()
	}
}

func readBody(body io.Reader, limit int64, watch *deadlineWatch) ([]byte, uint64, error) {
	var out bytes.Buffer
	buf := make([]byte, 32*1024)
	var bodyBytes uint64
	emptyReads := 0
	for {
		n, err := body.Read(buf)
		if n > 0 {
			emptyReads = 0
			bodyBytes += uint64(n)
			watch.gotBodyByte()
			remaining := limit - int64(out.Len())
			if remaining > 0 {
				take := n
				if int64(take) > remaining {
					take = int(remaining)
				}
				_, _ = out.Write(buf[:take])
			}
			if bodyBytes > uint64(limit) {
				return out.Bytes(), bodyBytes, nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out.Bytes(), bodyBytes, nil
			}
			return out.Bytes(), bodyBytes, err
		}
		if n == 0 {
			emptyReads++
			if emptyReads > 100 {
				return out.Bytes(), bodyBytes, io.ErrNoProgress
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func failure(input Request, started time.Time, kind Kind, publicMessage string, cause error, partial []byte, bodyBytes uint64, firstByteAt time.Time, request *http.Request, response *http.Response) *Failure {
	finished := time.Now()
	var responseHeaders http.Header
	var retryAfter *uint64
	if response != nil {
		responseHeaders = response.Header
		retryAfter = parseRetryAfter(response.Header, response.StatusCode, partial)
	}
	evidence := evidenceFor(input, request, responseHeaders, started, finished)
	return &Failure{
		Kind:            kind,
		Message:         publicMessage,
		Cause:           cause,
		RetryAfterMS:    retryAfter,
		PartialBody:     bytes.Clone(partial),
		BodyBytes:       bodyBytes,
		FirstBodyByteAt: firstByteAt,
		Evidence:        evidence,
	}
}

func evidenceFor(input Request, request *http.Request, responseHeaders http.Header, started, finished time.Time) contract.RequestEvidence {
	evidence := input.Evidence
	if request != nil {
		if request.URL != nil {
			evidence.EndpointHost = request.URL.Hostname()
			evidence.EndpointPath = request.URL.EscapedPath()
		}
		evidence.RoutingHeaderNames = routingHeaderNames(request.Header)
		evidence.BodyBytes = requestBytes(input, request)
	}
	evidence.RequestedAt = started
	evidence.RespondedAt = finished
	evidence.CodexTurnStatePresent = input.Provider != ProviderGrok && responseHeaders.Get("x-codex-turn-state") != ""
	if input.Provider != ProviderGrok && request != nil && request.Header.Get("x-codex-turn-state") != "" {
		evidence.CodexTurnStatePresent = true
	}
	return evidence
}

func requestBytes(input Request, request *http.Request) int64 {
	if request != nil && request.ContentLength >= 0 {
		return request.ContentLength
	}
	return input.BodyBytes
}

func classifyHTTP(provider Provider, status int, body []byte) (Kind, string) {
	text := strings.ToLower(string(body))
	if provider == ProviderGrok {
		switch {
		case status == http.StatusUnauthorized:
			return KindLogin, "Grok rejected this session; run kogen provider login grok."
		case status == http.StatusForbidden:
			return KindLogin, "This Grok account cannot access the requested model."
		case status == http.StatusTooManyRequests || containsAny(text, "usage_limit", "usage limit", "quota exceeded", "rate limit"):
			return KindUsageLimit, "Grok subscription usage limit reached."
		case status >= 500 || containsAny(text, "server_is_overloaded", "overloaded", "overload"):
			return KindOverload, "Grok service is temporarily overloaded."
		default:
			return KindMalformed, fmt.Sprintf("Grok rejected the request (HTTP %d).", status)
		}
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return KindLogin, "ChatGPT rejected the login; sign in again."
	case status == http.StatusTooManyRequests || containsAny(text, "usage_limit", "usage limit"):
		return KindUsageLimit, "ChatGPT subscription usage limit reached. Manage usage: https://chatgpt.com/settings/usage"
	case status >= 500 || strings.Contains(text, "overload"):
		return KindOverload, "ChatGPT service is temporarily overloaded."
	default:
		return KindMalformed, fmt.Sprintf("ChatGPT rejected the request (HTTP %d).", status)
	}
}

func parseRetryAfter(headers http.Header, status int, body []byte) *uint64 {
	if status != http.StatusTooManyRequests && status != http.StatusServiceUnavailable {
		return nil
	}
	if headers != nil {
		if seconds, err := strconv.ParseFloat(strings.TrimSpace(headers.Get("Retry-After")), 64); err == nil && !math.IsNaN(seconds) && !math.IsInf(seconds, 0) && seconds >= 0 {
			millis := uint64(math.Round(seconds * 1000))
			return &millis
		}
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil {
		return nil
	}
	raw, ok := object["resets_in_seconds"]
	if !ok {
		return nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&number) != nil {
		return nil
	}
	seconds, err := number.Float64()
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
		return nil
	}
	millis := uint64(math.Round(seconds * 1000))
	return &millis
}

func message(provider Provider, kind Kind) string {
	if provider == ProviderGrok {
		switch kind {
		case KindTimeout:
			return "Grok request timed out."
		case KindStall:
			return "Provider stream sent nothing for 90 s after it started."
		case KindTransport:
			return "Grok request could not connect."
		case KindMalformed:
			return "Grok returned a malformed response stream."
		default:
			return string(kind)
		}
	}
	switch kind {
	case KindTimeout:
		return "ChatGPT request timed out."
	case KindStall:
		return "Provider stream sent nothing for 90 s after it started."
	case KindTransport:
		return "ChatGPT request could not connect."
	case KindMalformed:
		return "ChatGPT returned a malformed response."
	default:
		return string(kind)
	}
}

func contextKind(err error) Kind {
	if errors.Is(err, context.DeadlineExceeded) {
		return KindTimeout
	}
	return KindCancelled
}

func contextMessage(err error) string {
	if contextKind(err) == KindTimeout {
		return "Provider request timed out."
	}
	return "Provider request was cancelled."
}

func bodyLimitMessage(provider Provider) string {
	if provider == ProviderGrok {
		return "Grok response exceeded the size limit."
	}
	return "ChatGPT response body exceeds 16 MB."
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func isTimeout(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

func headerNames(headers http.Header) []string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, http.CanonicalHeaderKey(name))
	}
	slices.Sort(names)
	return names
}

func routingHeaderNames(headers http.Header) []string {
	allowed := map[string]struct{}{
		"Session-Id": {}, "Thread-Id": {}, "X-Codex-Turn-State": {},
		"X-Grok-Conv-Id": {}, "X-Grok-Session-Id": {}, "X-Grok-Req-Id": {},
		"X-Grok-Model-Override": {}, "X-Grok-Client-Identifier": {},
		"X-Grok-Client-Mode": {}, "X-Grok-Client-Version": {},
	}
	var names []string
	for name := range headers {
		canonical := http.CanonicalHeaderKey(name)
		if _, ok := allowed[canonical]; ok {
			names = append(names, canonical)
		}
	}
	slices.Sort(names)
	return names
}

func scaledDuration(duration time.Duration, scale float64) time.Duration {
	scaled := float64(duration) * scale
	if scaled < 1 {
		return time.Nanosecond
	}
	if scaled > float64(int64(^uint64(0)>>1)) {
		return time.Duration(int64(^uint64(0) >> 1))
	}
	return time.Duration(scaled)
}
