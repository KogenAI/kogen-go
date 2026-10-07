package yamlmini

import (
	"strings"
	"unicode"
)

type flowParser struct {
	text      string
	index     int
	firstLine int
}

func (p *flowParser) line() int {
	if p.index <= 0 {
		return p.firstLine
	}
	return p.firstLine + strings.Count(p.text[:p.index], "\n")
}

func (p *flowParser) parseValue() (Value, *syntaxError) {
	p.skipSpace()
	if p.index >= len(p.text) {
		return nil, syntax(p.line(), 15, "unterminated flow collection")
	}
	switch p.text[p.index] {
	case '[':
		return p.parseSequence()
	case '{':
		return p.parseMapping()
	case '\'', '"':
		return p.parseQuoted()
	case ',', ']', '}':
		return nil, p.malformed()
	default:
		return p.parsePlain()
	}
}

func (p *flowParser) parseSequence() (Sequence, *syntaxError) {
	p.index++ // [
	result := make(Sequence, 0)
	p.skipSpace()
	if p.take(']') {
		return result, nil
	}
	for {
		var value Value
		var err *syntaxError
		if p.implicitFlowPairAhead() {
			value, err = p.parseImplicitMapping()
		} else {
			value, err = p.parseValue()
		}
		if err != nil {
			return nil, err
		}
		result = append(result, value)
		p.skipSpace()
		if p.take(']') {
			return result, nil
		}
		if !p.take(',') {
			return nil, p.malformed()
		}
		p.skipSpace()
		if p.take(']') { // YAML permits a trailing flow comma.
			return result, nil
		}
	}
}

func (p *flowParser) parseMapping() (Mapping, *syntaxError) {
	p.index++ // {
	result := make(Mapping)
	p.skipSpace()
	if p.take('}') {
		return result, nil
	}
	for {
		key, keyLine, err := p.parseKey()
		if err != nil {
			return nil, err
		}
		if key == "<<" {
			return nil, syntax(keyLine, 8, "YAML merge key `<<` is not allowed")
		}
		p.skipSpace()
		if !p.take(':') {
			return nil, p.malformed()
		}
		p.skipSpace()
		if p.index >= len(p.text) || p.text[p.index] == ',' || p.text[p.index] == '}' {
			return nil, syntax(keyLine, 22, "mapping key has no value")
		}
		value, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		if _, exists := result[key]; exists {
			return nil, syntax(keyLine, 20, "duplicate key \""+key+"\"")
		}
		result[key] = value
		p.skipSpace()
		if p.take('}') {
			return result, nil
		}
		if !p.take(',') {
			return nil, p.malformed()
		}
		p.skipSpace()
		if p.take('}') { // YAML permits a trailing flow comma.
			return result, nil
		}
	}
}

func (p *flowParser) parseImplicitMapping() (Mapping, *syntaxError) {
	result := make(Mapping)
	for {
		key, keyLine, err := p.parseKey()
		if err != nil {
			return nil, err
		}
		if key == "<<" {
			return nil, syntax(keyLine, 8, "YAML merge key `<<` is not allowed")
		}
		p.skipSpace()
		if !p.take(':') {
			return nil, p.malformed()
		}
		p.skipSpace()
		if p.index >= len(p.text) || p.text[p.index] == ',' || p.text[p.index] == ']' || p.text[p.index] == '}' {
			return nil, syntax(keyLine, 22, "mapping key has no value")
		}
		value, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		if _, exists := result[key]; exists {
			return nil, syntax(keyLine, 20, "duplicate key \""+key+"\"")
		}
		result[key] = value
		p.skipSpace()
		comma := p.index
		if !p.take(',') {
			return result, nil
		}
		p.skipSpace()
		if !p.implicitFlowPairAhead() {
			p.index = comma
			return result, nil
		}
	}
}

func (p *flowParser) implicitFlowPairAhead() bool {
	p.skipSpace()
	start := p.index
	if start >= len(p.text) || p.text[start] == '[' || p.text[start] == '{' {
		return false
	}
	var quote byte
	escaped := false
	for index := p.index; index < len(p.text); index++ {
		ch := p.text[index]
		if quote != 0 {
			if quote == '"' && escaped {
				escaped = false
			} else if quote == '"' && ch == '\\' {
				escaped = true
			} else if quote == ch {
				if quote == '\'' && index+1 < len(p.text) && p.text[index+1] == '\'' {
					index++
				} else {
					quote = 0
				}
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if ch == ',' || ch == ']' || ch == '}' {
			return false
		}
		if ch == '[' || ch == '{' {
			return false
		}
		if ch == ':' && index > start && index+1 < len(p.text) && unicode.IsSpace(rune(p.text[index+1])) {
			return true
		}
	}
	return false
}

func (p *flowParser) parseKey() (string, int, *syntaxError) {
	p.skipSpace()
	line := p.line()
	if p.index >= len(p.text) {
		return "", line, syntax(line, 15, "unterminated flow collection")
	}
	if p.text[p.index] == '\'' || p.text[p.index] == '"' {
		value, err := p.parseQuoted()
		if err != nil {
			return "", line, err
		}
		return value, line, nil
	}
	start := p.index
	for p.index < len(p.text) {
		ch := p.text[p.index]
		if ch == ':' || ch == ',' || ch == '}' || ch == ']' || ch == '\n' {
			break
		}
		p.index++
	}
	key := strings.TrimSpace(p.text[start:p.index])
	if key == "" {
		return "", line, p.malformed()
	}
	return key, line, nil
}

func (p *flowParser) parseQuoted() (string, *syntaxError) {
	start := p.index
	line := p.line()
	quote := p.text[p.index]
	p.index++
	for p.index < len(p.text) {
		ch := p.text[p.index]
		if ch == '\n' {
			return "", syntax(line, 11, "unterminated quoted string")
		}
		if quote == '"' && ch == '\\' {
			p.index += 2
			continue
		}
		if ch == quote {
			if quote == '\'' && p.index+1 < len(p.text) && p.text[p.index+1] == '\'' {
				p.index += 2
				continue
			}
			p.index++
			return decodeQuoted(p.text[start:p.index], line)
		}
		p.index++
	}
	return "", syntax(line, 11, "unterminated quoted string")
}

func (p *flowParser) parsePlain() (string, *syntaxError) {
	start := p.index
	for p.index < len(p.text) {
		switch p.text[p.index] {
		case ',', ']', '}':
			goto done
		}
		p.index++
	}
done:
	value := strings.TrimSpace(p.text[start:p.index])
	if value == "" {
		return "", p.malformed()
	}
	if containsPlainColonSpace(value) {
		return "", syntax(p.firstLine+strings.Count(p.text[:start], "\n"), 17, "unquoted `: ` inside a value")
	}
	return strings.Join(strings.Fields(value), " "), nil
}

func (p *flowParser) skipSpace() {
	for p.index < len(p.text) {
		r, size := runeAt(p.text, p.index)
		if !unicode.IsSpace(r) {
			return
		}
		p.index += size
	}
}

func (p *flowParser) take(ch byte) bool {
	if p.index < len(p.text) && p.text[p.index] == ch {
		p.index++
		return true
	}
	return false
}

func (p *flowParser) malformed() *syntaxError {
	near := strings.TrimSpace(p.text[p.index:])
	if near == "" {
		near = "<end>"
	}
	return syntax(p.line(), 14, "malformed flow collection near "+near)
}
