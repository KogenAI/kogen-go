// Package sse incrementally decodes and assembles ChatGPT Responses events.
//
// The assembler is deliberately independent of HTTP. A transport can feed it
// arbitrary byte chunks, inspect collected items when continuing an interrupted
// request, and call Finish once the response body ends.
package sse

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// MaxResponseBytes is the maximum complete HTTP body accepted by the decoder.
const MaxResponseBytes = 16_000_000

// ErrBodyTooLarge is returned as soon as a feed would take the body over the
// configured limit. Finish still reports a provider error, with any stream
// failure already observed taking precedence over the size violation.
var ErrBodyTooLarge = errors.New("ChatGPT response body exceeds 16 MB")

// ErrorKind identifies the provider-level result of decoding a stream.
type ErrorKind string

const (
	ErrorMalformed  ErrorKind = "malformed"
	ErrorTransport  ErrorKind = "transport"
	ErrorUsageLimit ErrorKind = "usage_limit"
	ErrorOverload   ErrorKind = "overload"
	ErrorIncomplete ErrorKind = "incomplete"
)

// ProviderError is a classified failure in the provider response stream.
type ProviderError struct {
	Kind         ErrorKind
	Message      string
	RetryAfterMS *uint64
	Usage        *Usage
}

func (e *ProviderError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Usage retains unknown counts as nil. Input is the uncached input count and
// is known only when both total and cached input counts are present.
type Usage struct {
	Input       *uint64
	CachedInput *uint64
	CacheWrite  *uint64
	Output      *uint64
	Reasoning   *uint64
}

// ToolCall contains a validated function call. Arguments is always one JSON
// object, whether the provider sent that object directly or encoded it as a
// JSON string.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// Response is the assembled completed Responses API envelope.
type Response struct {
	ID        string
	Text      string
	ToolCalls []ToolCall
	Usage     Usage
	RawItems  []json.RawMessage
}

// Assembler incrementally frames SSE events and assembles one response. It
// keeps no more than the accepted response body plus its decoded result.
type Assembler struct {
	consumed uint64

	line        []byte
	frameData   []byte
	dataLines   int
	pendingCR   bool
	collected   []json.RawMessage
	completed   json.RawMessage
	failure     *ProviderError
	malformed   bool
	finished    bool
	result      *Response
	resultError error
}

// NewAssembler creates an empty incremental SSE assembler.
func NewAssembler() *Assembler { return &Assembler{} }

// Feed accepts any byte chunk, including chunks ending in the middle of a
// UTF-8 sequence, line ending, SSE field, or JSON value.
func (a *Assembler) Feed(chunk []byte) error {
	if a == nil || a.finished {
		return nil
	}
	if len(chunk) == 0 {
		return nil
	}
	if a.consumed >= MaxResponseBytes {
		a.malformed = true
		return ErrBodyTooLarge
	}
	remaining := uint64(MaxResponseBytes) - a.consumed
	if uint64(len(chunk)) > remaining {
		// Parse the accepted prefix before marking the body malformed. This
		// preserves a failure event that appeared before the cap was crossed,
		// while never retaining or parsing bytes beyond the documented cap.
		a.feedBytes(chunk[:int(remaining)])
		a.consumed = MaxResponseBytes
		a.malformed = true
		return ErrBodyTooLarge
	}
	a.feedBytes(chunk)
	a.consumed += uint64(len(chunk))
	return nil
}

// HasItems reports whether at least one response.output_item.done event has
// been received. It is useful to choose identical resend versus continuation.
func (a *Assembler) HasItems() bool {
	return a != nil && len(a.collected) != 0
}

// CollectedItems returns copies of the completed items observed so far, in
// arrival order. The caller cannot mutate the assembler's retained history.
func (a *Assembler) CollectedItems() []json.RawMessage {
	if a == nil || len(a.collected) == 0 {
		return nil
	}
	return cloneItems(a.collected)
}

// Finish flushes a final unterminated line and frame, then assembles the
// completed response. Calling it more than once returns the same result.
func (a *Assembler) Finish() (*Response, error) {
	if a == nil {
		return nil, malformed("ChatGPT returned a malformed response.")
	}
	if !a.finished {
		if a.pendingCR {
			a.pendingCR = false
			a.emitLine()
		} else if len(a.line) != 0 {
			a.emitLine()
		}
		if a.dataLines != 0 {
			a.emitFrame()
		}
		a.finished = true
		a.result, a.resultError = a.assemble()
	}
	if a.resultError != nil {
		return nil, cloneProviderError(a.resultError)
	}
	return cloneResponse(a.result), nil
}

func (a *Assembler) feedBytes(chunk []byte) {
	for _, b := range chunk {
		if a.pendingCR {
			a.pendingCR = false
			a.emitLine()
			if b == '\n' {
				continue
			}
		}
		switch b {
		case '\r':
			a.pendingCR = true
		case '\n':
			a.emitLine()
		default:
			a.line = append(a.line, b)
		}
	}
}

func (a *Assembler) emitLine() {
	if len(a.line) == 0 {
		a.emitFrame()
		return
	}
	if data, ok := bytes.CutPrefix(a.line, []byte("data:")); ok {
		if len(data) != 0 && data[0] == ' ' {
			data = data[1:]
		}
		if a.dataLines != 0 {
			a.frameData = append(a.frameData, '\n')
		}
		a.frameData = append(a.frameData, data...)
		a.dataLines++
	}
	a.line = nil
}

func (a *Assembler) emitFrame() {
	if a.dataLines == 0 {
		return
	}
	data := a.frameData
	a.frameData = nil
	a.dataLines = 0
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return
	}
	if !utf8.Valid(data) || !json.Valid(data) {
		a.malformed = true
		return
	}
	var event map[string]json.RawMessage
	if err := json.Unmarshal(data, &event); err != nil || event == nil {
		a.malformed = true
		return
	}
	a.observeEvent(event, data)
}

func (a *Assembler) observeEvent(event map[string]json.RawMessage, encoded []byte) {
	kind := jsonString(event["type"])
	if errorValue, ok := event["error"]; ok && !isJSONNull(errorValue) ||
		kind == "error" || kind == "response.failed" || kind == "response.incomplete" {
		if a.failure == nil {
			a.failure = eventFailure(kind, event, encoded)
		}
		return
	}
	switch kind {
	case "response.output_item.done":
		item, ok := event["item"]
		if !ok || !json.Valid(item) {
			a.malformed = true
			return
		}
		a.collected = append(a.collected, cloneRaw(item))
	case "response.completed":
		if len(a.completed) != 0 {
			a.malformed = true
			return
		}
		response, ok := event["response"]
		if !ok || !json.Valid(response) {
			a.malformed = true
			return
		}
		a.completed = cloneRaw(response)
	}
}

func (a *Assembler) assemble() (*Response, error) {
	if a.failure != nil {
		return nil, cloneProviderError(a.failure)
	}
	if a.malformed {
		return nil, malformed("ChatGPT returned a malformed response.")
	}
	if len(a.completed) == 0 {
		return nil, malformed("ChatGPT response stream did not complete.")
	}
	responseObject, ok := jsonObject(a.completed)
	if !ok {
		return nil, malformed("ChatGPT returned a malformed response.")
	}
	status := jsonString(responseObject["status"])
	if status == "incomplete" {
		reason := "unknown"
		if details, ok := jsonObject(responseObject["incomplete_details"]); ok {
			if value := jsonString(details["reason"]); value != "" {
				reason = value
			}
		}
		usage := usageFromRaw(responseObject["usage"])
		return nil, &ProviderError{
			Kind:    ErrorIncomplete,
			Message: fmt.Sprintf("Model response incomplete (%s); no tool calls were executed.", reason),
			Usage:   &usage,
		}
	}
	id := jsonString(responseObject["id"])
	if status != "completed" || id == "" {
		return nil, malformed("ChatGPT returned a malformed response.")
	}
	if total, totalOK := jsonUint(responseObject["usage"], "input_tokens"); totalOK {
		if cached, cachedOK := jsonUintPath(responseObject["usage"], "input_tokens_details", "cached_tokens"); cachedOK && cached > total {
			return nil, malformed("ChatGPT returned invalid token usage.")
		}
	}

	items := outputItems(responseObject["output"])
	if len(items) == 0 {
		items = cloneItems(a.collected)
	}
	result := &Response{
		ID:        id,
		ToolCalls: make([]ToolCall, 0),
		RawItems:  cloneItems(items),
		Usage:     usageFromRaw(responseObject["usage"]),
	}
	for _, item := range items {
		object, ok := jsonObject(item)
		if !ok {
			continue
		}
		switch jsonString(object["type"]) {
		case "message":
			result.Text += outputText(object["content"])
		case "function_call":
			if jsonString(object["status"]) == "in_progress" {
				continue
			}
			call, err := parseToolCall(object)
			if err != nil {
				return nil, err
			}
			result.ToolCalls = append(result.ToolCalls, call)
		}
	}
	return result, nil
}

func eventFailure(kind string, event map[string]json.RawMessage, encoded []byte) *ProviderError {
	var expanded any
	_ = json.Unmarshal(encoded, &expanded)
	normalized, _ := json.Marshal(expanded)
	lower := strings.ToLower(string(normalized))
	response, _ := jsonObject(event["response"])
	var usage *Usage
	if len(response) != 0 {
		parsed := usageFromRaw(response["usage"])
		usage = &parsed
	}
	if kind == "response.incomplete" {
		reason := "unknown"
		if details, ok := jsonObject(response["incomplete_details"]); ok {
			if value := jsonString(details["reason"]); value != "" {
				reason = value
			}
		}
		return &ProviderError{
			Kind: ErrorIncomplete, Message: fmt.Sprintf("Model response incomplete (%s); no tool calls were executed.", reason), Usage: usage,
		}
	}
	classified := ErrorTransport
	switch {
	case strings.Contains(lower, "usage_limit"), strings.Contains(lower, "usage limit"), strings.Contains(lower, "rate_limit"), strings.Contains(lower, "rate limit"):
		classified = ErrorUsageLimit
	case strings.Contains(lower, "overload"):
		classified = ErrorOverload
	}
	message := "ChatGPT response stream failed."
	if errorObject, ok := jsonObject(event["error"]); ok {
		if value, exists := errorObject["message"]; exists {
			if decoded, valid := decodeJSONString(value); valid {
				message = decoded
			}
		}
	}
	return &ProviderError{Kind: classified, Message: message, Usage: usage}
}

func parseToolCall(item map[string]json.RawMessage) (ToolCall, error) {
	id := jsonString(item["call_id"])
	name := jsonString(item["name"])
	arguments, ok := item["arguments"]
	if id == "" || name == "" || !ok {
		return ToolCall{}, malformed("ChatGPT returned malformed tool arguments.")
	}
	arguments = bytes.TrimSpace(arguments)
	if len(arguments) != 0 && arguments[0] == '"' {
		text, valid := decodeJSONString(arguments)
		if !valid {
			return ToolCall{}, malformed("ChatGPT returned malformed tool arguments.")
		}
		arguments = []byte(text)
	}
	if !validUniqueJSONObject(arguments) {
		return ToolCall{}, malformed("ChatGPT returned malformed tool arguments.")
	}
	return ToolCall{ID: id, Name: name, Arguments: cloneRaw(arguments)}, nil
}

func outputText(raw json.RawMessage) string {
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var text strings.Builder
	for _, part := range parts {
		object, ok := jsonObject(part)
		if ok && jsonString(object["type"]) == "output_text" {
			text.WriteString(jsonString(object["text"]))
		}
	}
	return text.String()
}

func outputItems(raw json.RawMessage) []json.RawMessage {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	return items
}

func usageFromRaw(raw json.RawMessage) Usage {
	inputTokens, inputOK := jsonUint(raw, "input_tokens")
	cached, cachedOK := jsonUintPath(raw, "input_tokens_details", "cached_tokens")
	if inputOK && cachedOK && cached > inputTokens {
		cachedOK = false
	}
	usage := Usage{
		CachedInput: uintPointer(cached, cachedOK),
		CacheWrite:  uintPointerFrom(raw, "cache_write_tokens"),
		Output:      uintPointerFrom(raw, "output_tokens"),
		Reasoning:   uintPointerPath(raw, "output_tokens_details", "reasoning_tokens"),
	}
	if inputOK && cachedOK && cached <= inputTokens {
		usage.Input = uintPointer(inputTokens-cached, true)
	}
	return usage
}

func jsonUint(raw json.RawMessage, key string) (uint64, bool) {
	object, ok := jsonObject(raw)
	if !ok {
		return 0, false
	}
	number := bytes.TrimSpace(object[key])
	if len(number) == 0 || number[0] < '0' || number[0] > '9' {
		return 0, false
	}
	var value uint64
	err := json.Unmarshal(number, &value)
	return value, err == nil
}

func jsonUintPath(raw json.RawMessage, parent, key string) (uint64, bool) {
	object, ok := jsonObject(raw)
	if !ok {
		return 0, false
	}
	return jsonUint(object[parent], key)
}

func uintPointerFrom(raw json.RawMessage, key string) *uint64 {
	value, ok := jsonUint(raw, key)
	return uintPointer(value, ok)
}

func uintPointerPath(raw json.RawMessage, parent, key string) *uint64 {
	value, ok := jsonUintPath(raw, parent, key)
	return uintPointer(value, ok)
}

func uintPointer(value uint64, ok bool) *uint64 {
	if !ok {
		return nil
	}
	return &value
}

func jsonObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, false
	}
	return object, true
}

func jsonString(raw json.RawMessage) string {
	value, _ := decodeJSONString(raw)
	return value
}

func decodeJSONString(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func malformed(message string) *ProviderError {
	return &ProviderError{Kind: ErrorMalformed, Message: message}
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}

func cloneItems(items []json.RawMessage) []json.RawMessage {
	cloned := make([]json.RawMessage, len(items))
	for i := range items {
		cloned[i] = cloneRaw(items[i])
	}
	return cloned
}

func cloneUsage(usage Usage) Usage {
	clone := func(value *uint64) *uint64 {
		if value == nil {
			return nil
		}
		copied := *value
		return &copied
	}
	return Usage{
		Input: clone(usage.Input), CachedInput: clone(usage.CachedInput), CacheWrite: clone(usage.CacheWrite),
		Output: clone(usage.Output), Reasoning: clone(usage.Reasoning),
	}
}

func cloneProviderError(err error) error {
	provider, ok := err.(*ProviderError)
	if !ok || provider == nil {
		return err
	}
	clone := *provider
	clone.Usage = nil
	if provider.Usage != nil {
		usage := cloneUsage(*provider.Usage)
		clone.Usage = &usage
	}
	if provider.RetryAfterMS != nil {
		value := *provider.RetryAfterMS
		clone.RetryAfterMS = &value
	}
	return &clone
}

func cloneResponse(response *Response) *Response {
	if response == nil {
		return nil
	}
	clone := *response
	clone.Usage = cloneUsage(response.Usage)
	clone.RawItems = cloneItems(response.RawItems)
	clone.ToolCalls = make([]ToolCall, len(response.ToolCalls))
	for i, call := range response.ToolCalls {
		clone.ToolCalls[i] = call
		clone.ToolCalls[i].Arguments = cloneRaw(call.Arguments)
	}
	return &clone
}

// validUniqueJSONObject verifies that arguments are a single object and that
// every object key is unique. Duplicate JSON keys are ambiguous to downstream
// tool implementations, so they are refused rather than silently overwritten.
func validUniqueJSONObject(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	if err := consumeObject(decoder, 0); err != nil {
		return false
	}
	_, err = decoder.Token()
	return err == io.EOF
}

func consumeObject(decoder *json.Decoder, depth int) error {
	if depth > 1000 {
		return errors.New("JSON nesting limit exceeded")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("object key is not a string")
		}
		if _, exists := seen[key]; exists {
			return errors.New("duplicate object key")
		}
		seen[key] = struct{}{}
		if err := consumeValue(decoder, depth+1); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return errors.New("unterminated object")
	}
	return nil
}

func consumeArray(decoder *json.Decoder, depth int) error {
	if depth > 1000 {
		return errors.New("JSON nesting limit exceeded")
	}
	for decoder.More() {
		if err := consumeValue(decoder, depth+1); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim(']') {
		return errors.New("unterminated array")
	}
	return nil
}

func consumeValue(decoder *json.Decoder, depth int) error {
	if depth > 1000 {
		return errors.New("JSON nesting limit exceeded")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		return consumeObject(decoder, depth+1)
	case json.Delim('['):
		return consumeArray(decoder, depth+1)
	default:
		return nil
	}
}
