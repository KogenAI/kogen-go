package diagnostic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
)

// SliceName identifies one of the four diagnostic D slices. These names are
// intentionally disjoint from the counted G slices.
type SliceName string

const (
	Gate          SliceName = "gate"
	Orchestration SliceName = "orchestration"
	Accounts      SliceName = "accounts"
	SetupCache    SliceName = "setup-cache"
)

var ErrUnknownSlice = errors.New("diagnostic: unknown D slice")

// Difference identifies one complete JSON path at which observations differ.
// The presence flags distinguish an absent member from a member whose value is
// JSON null. Expected and Actual retain their complete JSON values at Path.
type Difference struct {
	Path            string          `json:"path"`
	ExpectedPresent bool            `json:"expected_present"`
	Expected        json.RawMessage `json:"expected,omitempty"`
	ActualPresent   bool            `json:"actual_present"`
	Actual          json.RawMessage `json:"actual,omitempty"`
}

// Comparison reports every divergence in a full D-slice observation. D rows
// are diagnostic-only and can never contribute to G conformance totals.
type Comparison struct {
	Slice         SliceName    `json:"slice"`
	Class         string       `json:"class"`
	CountsTowardG bool         `json:"counts_toward_g"`
	Match         bool         `json:"match"`
	Divergences   []Difference `json:"divergences"`
}

// MarshalObservation encodes a complete D observation and refuses unknown
// slice names, non-object values, and the private transport's projection
// marker. The transport separately validates observations before writing.
func MarshalObservation(slice SliceName, observation any) (json.RawMessage, error) {
	if !knownSlice(slice) {
		return nil, fmt.Errorf("%w %q", ErrUnknownSlice, slice)
	}
	if observation == nil {
		return nil, errors.New("diagnostic: observation is nil")
	}
	data, err := json.Marshal(observation)
	if err != nil {
		return nil, fmt.Errorf("diagnostic: encode %s observation: %w", slice, err)
	}
	if _, err := decodeObservation(data); err != nil {
		return nil, fmt.Errorf("diagnostic: invalid %s observation: %w", slice, err)
	}
	return json.RawMessage(data), nil
}

// Compare compares two complete observation objects and returns all changed,
// missing, and additional JSON paths in deterministic order. It does not
// project fields or infer acceptance from any one matching field.
func Compare(slice SliceName, expected, actual json.RawMessage) (Comparison, error) {
	if !knownSlice(slice) {
		return Comparison{}, fmt.Errorf("%w %q", ErrUnknownSlice, slice)
	}
	expectedValue, err := decodeObservation(expected)
	if err != nil {
		return Comparison{}, fmt.Errorf("diagnostic: invalid expected %s observation: %w", slice, err)
	}
	actualValue, err := decodeObservation(actual)
	if err != nil {
		return Comparison{}, fmt.Errorf("diagnostic: invalid actual %s observation: %w", slice, err)
	}
	divergences := make([]Difference, 0)
	compareValue("", expectedValue, actualValue, &divergences)
	return Comparison{
		Slice: slice, Class: "D", CountsTowardG: false,
		Match: len(divergences) == 0, Divergences: divergences,
	}, nil
}

func knownSlice(slice SliceName) bool {
	switch slice {
	case Gate, Orchestration, Accounts, SetupCache:
		return true
	default:
		return false
	}
}

func decodeObservation(raw []byte) (map[string]any, error) {
	if len(raw) == 0 || !json.Valid(raw) {
		return nil, errors.New("expected one JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("expected one JSON value")
	}
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return nil, errors.New("observation must be an object")
	}
	if _, projected := object["no_seam"]; projected {
		return nil, errors.New("projected observation is not accepted")
	}
	if last, ok := object["last"].(string); ok && last == "no_seam" {
		return nil, errors.New("projected observation is not accepted")
	}
	return object, nil
}

func compareValue(path string, expected, actual any, out *[]Difference) {
	expectedObject, expectedIsObject := expected.(map[string]any)
	actualObject, actualIsObject := actual.(map[string]any)
	if expectedIsObject && actualIsObject {
		keys := make(map[string]struct{}, len(expectedObject)+len(actualObject))
		for key := range expectedObject {
			keys[key] = struct{}{}
		}
		for key := range actualObject {
			keys[key] = struct{}{}
		}
		ordered := make([]string, 0, len(keys))
		for key := range keys {
			ordered = append(ordered, key)
		}
		sort.Strings(ordered)
		for _, key := range ordered {
			expectedChild, expectedPresent := expectedObject[key]
			actualChild, actualPresent := actualObject[key]
			childPath := path + "/" + escapePointer(key)
			if expectedPresent && actualPresent {
				compareValue(childPath, expectedChild, actualChild, out)
				continue
			}
			appendDifference(childPath, expectedChild, expectedPresent, actualChild, actualPresent, out)
		}
		return
	}
	expectedArray, expectedIsArray := expected.([]any)
	actualArray, actualIsArray := actual.([]any)
	if expectedIsArray && actualIsArray {
		shared := len(expectedArray)
		if len(actualArray) < shared {
			shared = len(actualArray)
		}
		for index := 0; index < shared; index++ {
			compareValue(path+"/"+fmt.Sprint(index), expectedArray[index], actualArray[index], out)
		}
		for index := shared; index < len(expectedArray); index++ {
			appendDifference(path+"/"+fmt.Sprint(index), expectedArray[index], true, nil, false, out)
		}
		for index := shared; index < len(actualArray); index++ {
			appendDifference(path+"/"+fmt.Sprint(index), nil, false, actualArray[index], true, out)
		}
		return
	}
	if !reflect.DeepEqual(expected, actual) {
		appendDifference(path, expected, true, actual, true, out)
	}
}

func appendDifference(path string, expected any, expectedPresent bool, actual any, actualPresent bool, out *[]Difference) {
	difference := Difference{Path: path, ExpectedPresent: expectedPresent, ActualPresent: actualPresent}
	if expectedPresent {
		difference.Expected = encodeRaw(expected)
	}
	if actualPresent {
		difference.Actual = encodeRaw(actual)
	}
	*out = append(*out, difference)
}

func encodeRaw(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("null")
	}
	return encoded
}

func escapePointer(value string) string {
	value = strings.ReplaceAll(value, "~", "~0")
	return strings.ReplaceAll(value, "/", "~1")
}

func errNilProductionResult(name string) error {
	return fmt.Errorf("diagnostic: production %s is nil", name)
}
