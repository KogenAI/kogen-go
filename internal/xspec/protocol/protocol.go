// Package protocol implements the private xspec/1 JSON-lines transport.
//
// The package owns framing and validation only. Slice factories are injected
// by command integration and must connect to the same production transitions
// as the public CLI. There is deliberately no default factory, no no-seam
// observation, and no observation projection API.
package protocol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// MaxLineBytes bounds one UTF-8 JSONL request, excluding its newline. The
// optional carriage return in a CRLF terminator is counted toward the bound.
const MaxLineBytes = 16_000_000

var (
	// ErrUnknownSlice reports that integration did not provide a factory for a
	// requested private slice. It is not replaced with a placeholder adapter.
	ErrUnknownSlice = errors.New("unknown private slice")

	// ErrUnsupportedOperation reports an operation outside xspec/1. In
	// particular, projection requests are never served by this transport.
	ErrUnsupportedOperation = errors.New("unsupported protocol operation")

	// ErrUnknownEventTag reports an event tag that the selected slice does not
	// define. It cannot be converted into a synthetic bad-event observation.
	ErrUnknownEventTag = errors.New("unknown event tag")
)

// Event is the transport-level tagged event. Value is nil when the event did
// not contain a value field; the JSON value null is represented by the bytes
// "null" and HasValue=true.
type Event struct {
	Tag      string
	Value    json.RawMessage
	HasValue bool
}

// Slice is implemented by an integration-provided production adapter.
//
// HasEventTag must recognize only tags in the slice's event schema. Apply
// validates the selected tag's value type and invokes the production
// transition. Observe must return the complete slice observation, not a
// selected subset. The transport validates that it is one JSON object, rejects
// the reserved no_seam marker, and writes the object unchanged as the response
// line.
type Slice interface {
	Reset(context.Context) error
	HasEventTag(string) bool
	Apply(context.Context, Event) error
	Observe(context.Context) (json.RawMessage, error)
}

// Factory creates one isolated slice adapter for a process. Integration
// factories own fixture setup, including a blank temporary HOME and temporary
// origins for effectful slices. If the returned slice implements io.Closer,
// Serve closes it on EOF and on every error path.
type Factory func(context.Context) (Slice, error)

// Registry maps the one private slice argument to its integration factory.
// It contains no built-in or fallback slice implementations.
type Registry map[string]Factory

// Serve runs one long-lived xspec/1 process for sliceName. Each accepted
// request produces exactly one complete observation line and flushes it before
// reading the next request. A malformed or refused request returns an error;
// it never produces a substitute observation.
func Serve(ctx context.Context, sliceName string, factories Registry, input io.Reader, output io.Writer) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if input == nil {
		return errors.New("protocol input is nil")
	}
	if output == nil {
		return errors.New("protocol output is nil")
	}
	factory, ok := factories[sliceName]
	if !ok || factory == nil {
		return fmt.Errorf("%w %q", ErrUnknownSlice, sliceName)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("protocol canceled: %w", err)
	}
	adapter, err := factory(ctx)
	if err != nil {
		return fmt.Errorf("create slice %q: %w", sliceName, err)
	}
	if adapter == nil {
		return fmt.Errorf("create slice %q: factory returned nil", sliceName)
	}
	if closer, ok := adapter.(io.Closer); ok {
		defer func() {
			if closeErr := closer.Close(); closeErr != nil {
				closeErr = fmt.Errorf("close slice %q: %w", sliceName, closeErr)
				if err == nil {
					err = closeErr
				} else {
					err = errors.Join(err, closeErr)
				}
			}
		}()
	}

	writer, flush := flushingWriter(output)
	reader := bufio.NewReaderSize(input, 64*1024)
	for lineNumber := 1; ; lineNumber++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("protocol canceled before line %d: %w", lineNumber, err)
		}
		line, eof, readErr := readLine(reader)
		if readErr != nil {
			return fmt.Errorf("read protocol line %d: %w", lineNumber, readErr)
		}
		if eof && len(line) == 0 {
			return nil
		}
		if !utf8.Valid(line) {
			return fmt.Errorf("protocol line %d is not valid UTF-8", lineNumber)
		}
		response, processErr := handleLine(ctx, adapter, line, lineNumber)
		if processErr != nil {
			return processErr
		}
		if err := writeResponse(writer, response); err != nil {
			return fmt.Errorf("write protocol line %d: %w", lineNumber, err)
		}
		if err := flush(); err != nil {
			return fmt.Errorf("flush protocol line %d: %w", lineNumber, err)
		}
		if eof {
			return nil
		}
	}
}

func handleLine(ctx context.Context, adapter Slice, line []byte, lineNumber int) (json.RawMessage, error) {
	request, err := decodeObject(line, "op", "event")
	if err != nil {
		return nil, fmt.Errorf("protocol line %d: invalid request: %w", lineNumber, err)
	}
	op, ok := stringField(request, "op")
	if !ok || op == "" {
		return nil, fmt.Errorf("protocol line %d: request requires non-empty string field `op`", lineNumber)
	}

	switch op {
	case "reset":
		if _, hasEvent := request["event"]; hasEvent {
			return nil, fmt.Errorf("protocol line %d: reset request must not contain `event`", lineNumber)
		}
		if err := adapter.Reset(ctx); err != nil {
			return nil, fmt.Errorf("protocol line %d: reset slice: %w", lineNumber, err)
		}
	case "apply":
		rawEvent, hasEvent := request["event"]
		if !hasEvent {
			return nil, fmt.Errorf("protocol line %d: apply request requires object field `event`", lineNumber)
		}
		eventObject, err := decodeObject(rawEvent, "tag", "value")
		if err != nil {
			return nil, fmt.Errorf("protocol line %d: invalid event: %w", lineNumber, err)
		}
		tag, ok := stringField(eventObject, "tag")
		if !ok || tag == "" {
			return nil, fmt.Errorf("protocol line %d: event requires non-empty string field `tag`", lineNumber)
		}
		if !adapter.HasEventTag(tag) {
			return nil, fmt.Errorf("protocol line %d: %w %q", lineNumber, ErrUnknownEventTag, tag)
		}
		event := Event{Tag: tag}
		if value, hasValue := eventObject["value"]; hasValue {
			event.Value = cloneRaw(value)
			event.HasValue = true
		}
		if err := adapter.Apply(ctx, event); err != nil {
			return nil, fmt.Errorf("protocol line %d: apply event %q: %w", lineNumber, tag, err)
		}
	default:
		return nil, fmt.Errorf("protocol line %d: %w %q", lineNumber, ErrUnsupportedOperation, op)
	}

	observation, err := adapter.Observe(ctx)
	if err != nil {
		return nil, fmt.Errorf("protocol line %d: observe slice: %w", lineNumber, err)
	}
	if err := validateFullObservation(observation); err != nil {
		return nil, fmt.Errorf("protocol line %d: invalid full observation: %w", lineNumber, err)
	}
	return cloneRaw(observation), nil
}

func decodeObject(raw []byte, allowed ...string) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || !json.Valid(raw) {
		return nil, errors.New("expected one JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, errors.New("expected one JSON object")
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, errors.New("expected one JSON object")
	}
	allowedKeys := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedKeys[key] = struct{}{}
	}
	object := make(map[string]json.RawMessage, len(allowed))
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, errors.New("invalid object key")
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("object key is not a string")
		}
		if len(allowedKeys) > 0 {
			if _, ok := allowedKeys[key]; !ok {
				return nil, fmt.Errorf("unknown field %q", key)
			}
		}
		if _, duplicate := object[key]; duplicate {
			return nil, fmt.Errorf("duplicate field %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("invalid value for field %q", key)
		}
		object[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, errors.New("unterminated JSON object")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("expected one JSON value")
	}
	return object, nil
}

func stringField(object map[string]json.RawMessage, field string) (string, bool) {
	raw, ok := object[field]
	if !ok {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func validateFullObservation(raw json.RawMessage) error {
	if len(raw) == 0 || !utf8.Valid(raw) || !json.Valid(raw) {
		return errors.New("observation is not valid UTF-8 JSON")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("observation must be a complete JSON object")
	}
	if _, err := decodeObject(trimmed); err != nil {
		return errors.New("observation must be a complete JSON object")
	}
	var observationValue any
	if err := json.Unmarshal(trimmed, &observationValue); err != nil {
		return errors.New("observation must be a complete JSON object")
	}
	if containsNoSeamMarker(observationValue) {
		return errors.New("observation contains the reserved no_seam marker")
	}
	return nil
}

func containsNoSeamMarker(value any) bool {
	switch value := value.(type) {
	case string:
		return value == "no_seam"
	case []any:
		for _, item := range value {
			if containsNoSeamMarker(item) {
				return true
			}
		}
	case map[string]any:
		for key, item := range value {
			if key == "no_seam" {
				return true
			}
			if containsNoSeamMarker(item) {
				return true
			}
		}
	}
	return false
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}

func readLine(reader *bufio.Reader) (line []byte, eof bool, err error) {
	for {
		fragment, readErr := reader.ReadSlice('\n')
		terminated := readErr == nil
		if terminated {
			fragment = fragment[:len(fragment)-1]
		}
		if len(line)+len(fragment) > MaxLineBytes {
			return nil, false, fmt.Errorf("request exceeds %d-byte limit", MaxLineBytes)
		}
		line = append(line, fragment...)
		switch readErr {
		case nil:
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			return line, false, nil
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			return line, true, nil
		default:
			return nil, false, readErr
		}
	}
}

func flushingWriter(output io.Writer) (io.Writer, func() error) {
	if flusher, ok := output.(interface{ Flush() error }); ok {
		return output, flusher.Flush
	}
	writer := bufio.NewWriterSize(output, 64*1024)
	return writer, writer.Flush
}

func writeResponse(writer io.Writer, observation json.RawMessage) error {
	line := make([]byte, len(observation)+1)
	copy(line, observation)
	line[len(observation)] = '\n'
	for len(line) > 0 {
		written, err := writer.Write(line)
		if written < 0 || written > len(line) {
			return errors.New("invalid write count")
		}
		line = line[written:]
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
