package queuestatus

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"kogen-go/internal/xspec/protocol"
)

// Factories returns the production-backed factories for the queue and status
// slices. The integration owner composes this registry into kogen-xspec.
func Factories() protocol.Registry {
	return protocol.Registry{"queue": QueueFactory, "status": StatusFactory}
}

var errEventValue = errors.New("event requires an object value")

func eventObject(event protocol.Event, required, optional []string) (map[string]json.RawMessage, error) {
	if !event.HasValue || len(event.Value) == 0 || !json.Valid(event.Value) {
		return nil, errEventValue
	}
	decoder := json.NewDecoder(bytes.NewReader(event.Value))
	decoder.UseNumber()
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return nil, errEventValue
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("event value must contain one JSON object")
	}
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, key := range required {
		allowed[key] = true
		if _, exists := fields[key]; !exists {
			return nil, fmt.Errorf("event value requires field %q", key)
		}
	}
	for _, key := range optional {
		allowed[key] = true
	}
	for key := range fields {
		if !allowed[key] {
			return nil, fmt.Errorf("event value has unknown field %q", key)
		}
	}
	return fields, nil
}

func eventString(fields map[string]json.RawMessage, key string) (string, error) {
	raw, ok := fields[key]
	if !ok {
		return "", fmt.Errorf("event value requires field %q", key)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("event value field %q must be a string", key)
	}
	return value, nil
}

func optionalEventString(fields map[string]json.RawMessage, key string) (string, error) {
	raw, ok := fields[key]
	if !ok {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("event value field %q must be a string", key)
	}
	return value, nil
}

func eventInt(fields map[string]json.RawMessage, key string) (int64, error) {
	raw, ok := fields[key]
	if !ok {
		return 0, fmt.Errorf("event value requires field %q", key)
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, fmt.Errorf("event value field %q must be an integer", key)
	}
	value, err := strconv.ParseInt(number.String(), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("event value field %q must be an integer", key)
	}
	return value, nil
}

func eventBool(fields map[string]json.RawMessage, key string) (bool, error) {
	raw, ok := fields[key]
	if !ok {
		return false, fmt.Errorf("event value requires field %q", key)
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, fmt.Errorf("event value field %q must be a boolean", key)
	}
	return value, nil
}

func marshalObservation(value any) (json.RawMessage, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("queuestatus: encode observation: %w", err)
	}
	return json.RawMessage(data), nil
}
