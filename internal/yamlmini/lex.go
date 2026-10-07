package yamlmini

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type flowOpen struct {
	char byte
	line int
}

func scanLines(lines []string, errors *[]candidate) {
	var flow []flowOpen
	for index, source := range lines {
		lineNo := index + 1
		line := strings.TrimSuffix(source, "\r")
		if strings.ContainsRune(line, '\t') {
			addCandidate(errors, lineNo, 3, "tab character: indent with spaces")
		}

		visible := stripComment(line)
		trimmed := strings.TrimSpace(visible)
		if strings.HasPrefix(trimmed, "%") || trimmed == "---" || trimmed == "..." {
			addCandidate(errors, lineNo, 4, "directives and document markers are not allowed")
		}
		scanQuotesAndFlow(line, lineNo, &flow, errors)
		scanReserved(line, lineNo, errors)

		if strings.HasPrefix(trimmed, "|") || strings.HasPrefix(trimmed, ">") {
			addCandidate(errors, lineNo, 5, "anchors, aliases, tags, and block scalars are not allowed")
		}
		if item, ok := strings.CutPrefix(trimmed, "-"); ok && firstIsSpace(item) {
			item = strings.TrimLeftFunc(item, unicode.IsSpace)
			if strings.HasPrefix(item, "|") || strings.HasPrefix(item, ">") {
				addCandidate(errors, lineNo, 5, "anchors, aliases, tags, and block scalars are not allowed")
			}
		}

		if key, value, ok := mappingKeyValue(line); ok {
			if strings.TrimSpace(key) == "<<" {
				addCandidate(errors, lineNo, 8, "YAML merge key `<<` is not allowed")
			}
			value = strings.TrimSpace(stripComment(value))
			if strings.HasPrefix(value, "|") || strings.HasPrefix(value, ">") {
				addCandidate(errors, lineNo, 5, "anchors, aliases, tags, and block scalars are not allowed")
			}
			if startsListItem(value) {
				addCandidate(errors, lineNo, 18, "list item in a value position")
			}
			if containsPlainColonSpace(value) {
				scalar := strings.Trim(value, "\"'")
				addCandidate(errors, lineNo, 17, fmt.Sprintf("unquoted `: ` inside a value: %q", scalar))
			}
		}
	}
	if len(flow) > 0 {
		addCandidate(errors, flow[0].line, 15, "unterminated flow collection")
	}
}

func scanQuotesAndFlow(line string, lineNo int, flow *[]flowOpen, errors *[]candidate) {
	var quote rune
	escaped := false
	var previous rune
	itemStart := 0
	for offset := 0; offset < len(line); {
		r, size := runeAt(line, offset)
		nextOffset := offset + size
		if quote != 0 {
			if quote == '"' && escaped {
				if r == 'u' {
					addCandidate(errors, lineNo, 9, "Unicode escape \\u is not supported")
				} else if r != 'n' && r != 't' && r != '"' && r != '\\' && r != '/' {
					addCandidate(errors, lineNo, 10, fmt.Sprintf("unsupported escape \\%c", r))
				}
				escaped = false
			} else if quote == '"' && r == '\\' {
				escaped = true
			} else if quote == '\'' && r == '\'' {
				if nextOffset < len(line) && line[nextOffset] == '\'' {
					nextOffset++
				} else {
					quote = 0
					quoteTail(line, nextOffset, lineNo, errors)
				}
			} else if quote == '"' && r == '"' {
				quote = 0
				quoteTail(line, nextOffset, lineNo, errors)
			}
			previous = r
			offset = nextOffset
			continue
		}

		if r == '#' && (offset == 0 || unicode.IsSpace(previous)) {
			break
		}
		if r == '"' || r == '\'' {
			quote = r
		} else if r == '[' || r == '{' {
			if r == '[' && len(*flow) > 0 && previous != 0 && previous != '[' && previous != '{' && previous != ',' && previous != ':' && !unicode.IsSpace(previous) {
				tokenEnd := strings.IndexByte(line[offset:], ']')
				if tokenEnd < 0 {
					tokenEnd = len(line) - offset
				} else {
					tokenEnd += size
				}
				token := strings.TrimSpace(line[itemStart : offset+tokenEnd])
				addCandidate(errors, lineNo, 13, fmt.Sprintf("quote %q: brackets inside a flow collection", token))
			}
			*flow = append(*flow, flowOpen{char: byte(r), line: lineNo})
			if len(*flow) > maxFlowDepth {
				addCandidate(errors, lineNo, 7, "maximum nesting depth of 64 collections exceeded")
			}
			itemStart = nextOffset
		} else if r == ']' || r == '}' {
			if len(*flow) > 0 && matchingFlow((*flow)[len(*flow)-1].char, byte(r)) {
				*flow = (*flow)[:len(*flow)-1]
				if len(*flow) == 0 {
					tail := strings.TrimSpace(line[nextOffset:])
					if tail != "" && !strings.HasPrefix(tail, "#") {
						addCandidate(errors, lineNo, 16, "trailing text after flow collection")
					}
				}
			} else if len(*flow) > 0 {
				addCandidate(errors, lineNo, 14, fmt.Sprintf("malformed flow collection near %s", strings.TrimSpace(line[offset:])))
			}
		} else if r == ',' && len(*flow) > 0 {
			rest := strings.TrimLeftFunc(line[nextOffset:], unicode.IsSpace)
			before := strings.TrimRightFunc(line[:offset], unicode.IsSpace)
			if strings.HasSuffix(before, ",") || strings.HasSuffix(before, "[") || strings.HasSuffix(before, "{") {
				start := firstFlowOpen(line)
				addCandidate(errors, lineNo, 14, fmt.Sprintf("malformed flow collection near %s", strings.TrimSpace(line[start:])))
			}
			itemStart = nextOffset + len(line[nextOffset:]) - len(rest)
		}
		previous = r
		offset = nextOffset
	}
	if quote != 0 {
		addCandidate(errors, lineNo, 11, "unterminated quoted string")
	}
}

func runeAt(value string, offset int) (rune, int) {
	r, size := utf8.DecodeRuneInString(value[offset:])
	return r, size
}

func quoteTail(line string, offset, lineNo int, errors *[]candidate) {
	tail := strings.TrimLeftFunc(line[offset:], unicode.IsSpace)
	if tail == "" || strings.HasPrefix(tail, "#") || strings.HasPrefix(tail, ":") || strings.HasPrefix(tail, ",") || strings.HasPrefix(tail, "]") || strings.HasPrefix(tail, "}") {
		return
	}
	addCandidate(errors, lineNo, 12, "text after closing quote")
}

func scanReserved(line string, lineNo int, errors *[]candidate) {
	visible := stripComment(line)
	var quote rune
	escaped := false
	for index := 0; index < len(visible); {
		r, size := runeAt(visible, index)
		next := index + size
		if quote != 0 {
			if quote == '"' && escaped {
				escaped = false
			} else if quote == '"' && r == '\\' {
				escaped = true
			} else if r == quote {
				if quote == '\'' && next < len(visible) && visible[next] == '\'' {
					next++
				} else {
					quote = 0
				}
			}
			index = next
			continue
		}
		if r == '"' || r == '\'' {
			quote = r
			index = next
			continue
		}
		if r == '&' || r == '*' || r == '!' {
			prefix := strings.TrimRightFunc(visible[:index], unicode.IsSpace)
			tokenStart := prefix == "" || strings.HasSuffix(prefix, ":") || strings.HasSuffix(prefix, "[") || strings.HasSuffix(prefix, "{") || strings.HasSuffix(prefix, ",") || strings.HasSuffix(prefix, "-")
			if tokenStart && next < len(visible) && !isASCIIWhitespace(visible[next]) {
				if r == '!' {
					addCandidate(errors, lineNo, 6, "anchors, aliases, and tags are not allowed")
				} else {
					addCandidate(errors, lineNo, 6, "anchors, aliases, and block scalars are not allowed")
				}
				return
			}
		}
		index = next
	}
}

func stripComment(line string) string {
	var quote rune
	escaped := false
	for offset := 0; offset < len(line); {
		r, size := runeAt(line, offset)
		nextOffset := offset + size
		if quote != 0 {
			if quote == '"' && escaped {
				escaped = false
			} else if quote == '"' && r == '\\' {
				escaped = true
			} else if r == quote {
				if quote == '\'' && nextOffset < len(line) && line[nextOffset] == '\'' {
					nextOffset++
				} else {
					quote = 0
				}
			}
		} else if r == '"' || r == '\'' {
			quote = r
		} else if r == '#' && (offset == 0 || previousRuneIsSpace(line, offset)) {
			return line[:offset]
		}
		offset = nextOffset
	}
	return line
}

func mappingKeyValue(line string) (key, value string, ok bool) {
	visible := strings.TrimSpace(stripComment(line))
	if item, found := strings.CutPrefix(visible, "- "); found {
		visible = item
	}
	colon := plainColon(visible)
	if colon < 0 {
		return "", "", false
	}
	return visible[:colon], strings.TrimLeftFunc(visible[colon+1:], unicode.IsSpace), true
}

func plainColon(value string) int {
	var quote rune
	depth := 0
	escaped := false
	for offset := 0; offset < len(value); {
		r, size := runeAt(value, offset)
		nextOffset := offset + size
		if quote != 0 {
			if quote == '"' && escaped {
				escaped = false
			} else if quote == '"' && r == '\\' {
				escaped = true
			} else if r == quote {
				if quote == '\'' && nextOffset < len(value) && value[nextOffset] == '\'' {
					nextOffset++
				} else {
					quote = 0
				}
			}
		} else if r == '"' || r == '\'' {
			quote = r
		} else if r == '[' || r == '{' {
			depth++
		} else if r == ']' || r == '}' {
			if depth > 0 {
				depth--
			}
		} else if r == ':' && depth == 0 {
			return offset
		}
		offset = nextOffset
	}
	return -1
}

func containsPlainColonSpace(value string) bool {
	var quote rune
	depth := 0
	escaped := false
	for offset := 0; offset < len(value); {
		r, size := runeAt(value, offset)
		nextOffset := offset + size
		if quote != 0 {
			if quote == '"' && escaped {
				escaped = false
			} else if quote == '"' && r == '\\' {
				escaped = true
			} else if r == quote {
				if quote == '\'' && nextOffset < len(value) && value[nextOffset] == '\'' {
					nextOffset++
				} else {
					quote = 0
				}
			}
		} else if r == '"' || r == '\'' {
			quote = r
		} else if r == '[' || r == '{' {
			depth++
		} else if r == ']' || r == '}' {
			if depth > 0 {
				depth--
			}
		} else if r == ':' && depth == 0 && nextOffset < len(value) {
			next, _ := runeAt(value, nextOffset)
			if unicode.IsSpace(next) {
				return true
			}
		}
		offset = nextOffset
	}
	return false
}

func startsListItem(value string) bool {
	rest, ok := strings.CutPrefix(value, "-")
	return ok && firstIsSpace(rest)
}

func firstIsSpace(value string) bool {
	if value == "" {
		return false
	}
	r, _ := runeAt(value, 0)
	return unicode.IsSpace(r)
}

func matchingFlow(open, close byte) bool {
	return (open == '[' && close == ']') || (open == '{' && close == '}')
}

func firstFlowOpen(line string) int {
	index := strings.IndexAny(line, "[{")
	if index < 0 {
		return 0
	}
	return index + 1
}

func isASCIIWhitespace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r' || value == '\v' || value == '\f'
}

func previousRuneIsSpace(value string, offset int) bool {
	if offset == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(value[:offset])
	return unicode.IsSpace(r)
}
