package yamlmini

import (
	"strings"
)

// Value is a node in the supported YAML subset. Scalars are always strings;
// collections are Mapping and Sequence values.
type Value any

// Mapping is a YAML block or flow mapping with string keys.
type Mapping map[string]Value

// Sequence is a YAML block or flow sequence.
type Sequence []Value

type syntaxError struct {
	line    int
	order   int
	message string
}

func (e *syntaxError) Error() string { return e.message }

func syntax(line, order int, message string) *syntaxError {
	return &syntaxError{line: line, order: order, message: message}
}

// Parse parses the strict YAML subset used by Kogen project and frontmatter
// files. It deliberately does not apply YAML scalar coercions.
func Parse(source []byte) (Value, error) {
	var value Value
	_, issue := preflight(source, func(lines []string, errors *[]candidate) {
		parser := newBlockParser(lines)
		parsed, err := parser.document()
		if err != nil {
			addCandidate(errors, err.line, err.order, err.message)
			return
		}
		value = parsed
	})
	if issue != nil {
		return nil, issue
	}
	return value, nil
}

func decodeQuoted(value string, line int) (string, *syntaxError) {
	value = strings.TrimSpace(value)
	if len(value) < 2 {
		return "", syntax(line, 11, "unterminated quoted string")
	}
	switch value[0] {
	case '\'':
		var result strings.Builder
		for index := 1; index < len(value); index++ {
			if value[index] != '\'' {
				result.WriteByte(value[index])
				continue
			}
			if index+1 < len(value) && value[index+1] == '\'' {
				result.WriteByte('\'')
				index++
				continue
			}
			if strings.TrimSpace(value[index+1:]) != "" {
				return "", syntax(line, 12, "text after closing quote")
			}
			return result.String(), nil
		}
		return "", syntax(line, 11, "unterminated quoted string")
	case '"':
		var result strings.Builder
		for index := 1; index < len(value); index++ {
			ch := value[index]
			if ch == '"' {
				if strings.TrimSpace(value[index+1:]) != "" {
					return "", syntax(line, 12, "text after closing quote")
				}
				return result.String(), nil
			}
			if ch != '\\' {
				result.WriteByte(ch)
				continue
			}
			index++
			if index >= len(value) {
				return "", syntax(line, 11, "unterminated quoted string")
			}
			switch value[index] {
			case 'n':
				result.WriteByte('\n')
			case 't':
				result.WriteByte('\t')
			case '"', '\\', '/':
				result.WriteByte(value[index])
			case 'u':
				return "", syntax(line, 9, "Unicode escape \\u is not supported")
			default:
				return "", syntax(line, 10, "unsupported escape \\"+string(value[index]))
			}
		}
		return "", syntax(line, 11, "unterminated quoted string")
	default:
		return "", syntax(line, 14, "malformed YAML scalar")
	}
}
