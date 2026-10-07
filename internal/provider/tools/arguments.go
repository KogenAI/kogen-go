package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

const (
	invalidArgumentsText = "ERROR (invalid_arguments): Tool arguments do not match the schema."
	invalidLimitText     = "ERROR: limit must be between 1 and 400."
)

// ToolError is a tool result that must be returned to the model verbatim.
// Cause is retained for local diagnostics while Error stays contract-stable.
type ToolError struct {
	Message string
	Cause   error
}

func (e *ToolError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Message
}

func (e *ToolError) Unwrap() error { return e.Cause }

func toolError(message string) error { return &ToolError{Message: message} }

func wrappedToolError(message string, cause error) error {
	return &ToolError{Message: message, Cause: cause}
}

// FileArguments is the normalized argument set shared by the direct file
// tools. Unused fields remain zero for a given tool name.
type FileArguments struct {
	Path    string
	Pattern string
	Content string
	Old     string
	New     string
	Offset  int
	Limit   int
}

// ParseFileArguments validates the arguments used by read, search, write and
// edit. Unknown fields are ignored as required by the provider tool contract.
func ParseFileArguments(name string, raw json.RawMessage) (FileArguments, error) {
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil || object == nil {
		return FileArguments{}, toolError(invalidArgumentsText)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return FileArguments{}, toolError(invalidArgumentsText)
	}

	var args FileArguments
	switch name {
	case ToolRead:
		path, err := requiredString(object, "path")
		if err != nil {
			return FileArguments{}, err
		}
		args.Path = path
		args.Offset = 1
		args.Limit = 200
		if rawOffset, ok := object["offset"]; ok {
			value, err := unsignedInteger(rawOffset)
			if err != nil || value == 0 || value > uint64(^uint(0)>>1) {
				return FileArguments{}, toolError(invalidArgumentsText)
			}
			args.Offset = int(value)
		}
		if rawLimit, ok := object["limit"]; ok {
			value, err := unsignedInteger(rawLimit)
			if err != nil {
				return FileArguments{}, toolError(invalidArgumentsText)
			}
			if value < 1 || value > 400 {
				return FileArguments{}, toolError(invalidLimitText)
			}
			args.Limit = int(value)
		}
	case ToolSearch:
		pattern, err := requiredString(object, "pattern")
		if err != nil {
			return FileArguments{}, err
		}
		args.Pattern = pattern
		args.Path = "."
		if rawPath, ok := object["path"]; ok {
			value, ok := decodedString(rawPath)
			if !ok {
				return FileArguments{}, toolError(invalidArgumentsText)
			}
			args.Path = value
		}
	case ToolWrite:
		path, err := requiredString(object, "path")
		if err != nil {
			return FileArguments{}, err
		}
		content, err := requiredString(object, "content")
		if err != nil {
			return FileArguments{}, err
		}
		args.Path, args.Content = path, content
	case ToolEdit:
		path, err := requiredString(object, "path")
		if err != nil {
			return FileArguments{}, err
		}
		oldText, err := requiredString(object, "old")
		if err != nil {
			return FileArguments{}, err
		}
		newText, err := requiredString(object, "new")
		if err != nil {
			return FileArguments{}, err
		}
		args.Path, args.Old, args.New = path, oldText, newText
	default:
		return FileArguments{}, toolError(invalidArgumentsText)
	}
	return args, nil
}

func requiredString(object map[string]json.RawMessage, key string) (string, error) {
	raw, ok := object[key]
	if !ok {
		return "", toolError(invalidArgumentsText)
	}
	value, ok := decodedString(raw)
	if !ok {
		return "", toolError(invalidArgumentsText)
	}
	return value, nil
}

func decodedString(raw json.RawMessage) (string, bool) {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", false
	}
	value, ok := decoded.(string)
	return value, ok
}

func unsignedInteger(raw json.RawMessage) (uint64, error) {
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, err
	}
	value, err := strconv.ParseUint(number.String(), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("not an unsigned integer: %w", err)
	}
	return value, nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra json.RawMessage
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("unexpected trailing JSON value")
}
