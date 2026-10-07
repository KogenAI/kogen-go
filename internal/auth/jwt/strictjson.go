package jwt

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

type jsonNumber string

func (n jsonNumber) int64() (int64, error) {
	var value int64
	err := json.Unmarshal([]byte(n), &value)
	return value, err
}

func parseJSONObject(data []byte) (map[string]any, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("JSON input is not UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := parseJSONValue(decoder, 0)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("JSON value is not an object")
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, errors.New("trailing JSON value")
		}
		return nil, err
	}
	return object, nil
}

func parseJSONValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, errors.New("JSON nesting is too deep")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			object := make(map[string]any)
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, errors.New("JSON object key is invalid")
				}
				if _, duplicate := object[key]; duplicate {
					return nil, errors.New("duplicate JSON object key")
				}
				member, err := parseJSONValue(decoder, depth+1)
				if err != nil {
					return nil, err
				}
				object[key] = member
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return nil, errors.New("JSON object is incomplete")
			}
			return object, nil
		case '[':
			array := make([]any, 0)
			for decoder.More() {
				item, err := parseJSONValue(decoder, depth+1)
				if err != nil {
					return nil, err
				}
				array = append(array, item)
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return nil, errors.New("JSON array is incomplete")
			}
			return array, nil
		default:
			return nil, errors.New("unexpected JSON delimiter")
		}
	case json.Number:
		return jsonNumber(value.String()), nil
	case string, bool, nil:
		return value, nil
	default:
		return nil, errors.New("unsupported JSON value")
	}
}
