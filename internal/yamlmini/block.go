package yamlmini

import (
	"strings"
	"unicode"
)

type blockLine struct {
	number int
	indent int
	body   string
	raw    string
}

type blockParser struct {
	lines []blockLine
	index int
}

func newBlockParser(source []string) *blockParser {
	parser := &blockParser{lines: make([]blockLine, len(source))}
	for index, raw := range source {
		raw = strings.TrimSuffix(raw, "\r")
		visible := strings.TrimRight(stripComment(raw), " \r")
		indent := 0
		for indent < len(visible) && visible[indent] == ' ' {
			indent++
		}
		parser.lines[index] = blockLine{
			number: index + 1,
			indent: indent,
			body:   strings.TrimSpace(visible[indent:]),
			raw:    raw,
		}
	}
	return parser
}

func (p *blockParser) document() (Value, *syntaxError) {
	index := p.nextContent(0)
	if index == len(p.lines) {
		return nil, nil
	}
	if p.lines[index].indent != 0 {
		return nil, syntax(p.lines[index].number, 19, "unexpected indentation")
	}
	p.index = index
	value, err := p.parseBlock(0)
	if err != nil {
		return nil, err
	}
	if index := p.nextContent(p.index); index < len(p.lines) {
		line := p.lines[index]
		return nil, syntax(line.number, 19, "unexpected indentation")
	}
	return value, nil
}

func (p *blockParser) parseBlock(indent int) (Value, *syntaxError) {
	index := p.nextContent(p.index)
	if index >= len(p.lines) {
		return nil, syntax(0, 22, "mapping key has no value")
	}
	line := p.lines[index]
	if line.indent != indent {
		return nil, syntax(line.number, 19, "unexpected indentation")
	}
	p.index = index
	if isSequenceLine(line.body) {
		return p.parseSequence(indent)
	}
	if _, _, ok := splitBlockMapping(line.body); ok {
		return p.parseMapping(indent)
	}
	p.index = index + 1
	value, err := p.parseInline(line.body, index)
	if err != nil {
		return nil, err
	}
	return value, nil
}

func (p *blockParser) parseMapping(indent int) (Mapping, *syntaxError) {
	result := make(Mapping)
	for {
		index := p.nextContent(p.index)
		if index >= len(p.lines) || p.lines[index].indent < indent {
			p.index = index
			return result, nil
		}
		line := p.lines[index]
		if line.indent > indent {
			return nil, syntax(line.number, 19, "unexpected indentation")
		}
		keySource, valueSource, ok := splitBlockMapping(line.body)
		if !ok {
			if len(result) == 0 {
				return nil, syntax(line.number, 14, "malformed block mapping")
			}
			p.index = index
			return result, nil
		}
		key, err := parseBlockKey(keySource, line.number)
		if err != nil {
			return nil, err
		}
		if _, exists := result[key]; exists {
			return nil, syntax(line.number, 20, "duplicate key \""+key+"\"")
		}
		p.index = index + 1
		value, err := p.parseMapValue(valueSource, line.number, indent, index)
		if err != nil {
			return nil, err
		}
		result[key] = value
	}
}

func (p *blockParser) parseSequence(indent int) (Sequence, *syntaxError) {
	result := make(Sequence, 0)
	for {
		index := p.nextContent(p.index)
		if index >= len(p.lines) || p.lines[index].indent < indent {
			p.index = index
			return result, nil
		}
		line := p.lines[index]
		if line.indent > indent {
			return nil, syntax(line.number, 19, "unexpected indentation")
		}
		if !isSequenceLine(line.body) {
			p.index = index
			return result, nil
		}
		tail := strings.TrimSpace(line.body[1:])
		if tail == "" {
			return nil, syntax(line.number, 23, "list item has no value")
		}
		if keySource, _, ok := splitBlockMapping(tail); ok {
			key, err := parseBlockKey(keySource, line.number)
			if err != nil {
				return nil, err
			}
			mapping := make(Mapping)
			p.index = index + 1
			valueSource := mappingValue(tail)
			value, err := p.parseMapValue(valueSource, line.number, indent+2, index)
			if err != nil {
				return nil, err
			}
			mapping[key] = value
			if _, err := p.parseMappingRemainder(mapping, indent+2); err != nil {
				return nil, err
			}
			result = append(result, mapping)
			continue
		}
		p.index = index + 1
		value, err := p.parseInline(tail, index)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
}

func (p *blockParser) parseMappingRemainder(result Mapping, indent int) (Mapping, *syntaxError) {
	for {
		index := p.nextContent(p.index)
		if index >= len(p.lines) || p.lines[index].indent < indent {
			p.index = index
			return result, nil
		}
		line := p.lines[index]
		if line.indent > indent {
			return nil, syntax(line.number, 19, "unexpected indentation")
		}
		if isSequenceLine(line.body) {
			p.index = index
			return result, nil
		}
		keySource, valueSource, ok := splitBlockMapping(line.body)
		if !ok {
			return nil, syntax(line.number, 19, "unexpected indentation")
		}
		key, err := parseBlockKey(keySource, line.number)
		if err != nil {
			return nil, err
		}
		if _, exists := result[key]; exists {
			return nil, syntax(line.number, 20, "duplicate key \""+key+"\"")
		}
		p.index = index + 1
		value, err := p.parseMapValue(valueSource, line.number, indent, index)
		if err != nil {
			return nil, err
		}
		result[key] = value
	}
}

func (p *blockParser) parseMapValue(source string, lineNumber, indent, sourceIndex int) (Value, *syntaxError) {
	source = strings.TrimSpace(source)
	if source != "" {
		if startsListItem(source) {
			return nil, syntax(lineNumber, 18, "list item in a value position")
		}
		if containsPlainColonSpace(source) {
			return nil, syntax(lineNumber, 17, "unquoted `: ` inside a value")
		}
		return p.parseInline(source, sourceIndex)
	}
	next := p.nextContent(p.index)
	if next < len(p.lines) {
		child := p.lines[next]
		if child.indent == indent && isSequenceLine(child.body) {
			return nil, syntax(child.number, 19, "unexpected indentation")
		}
		if child.indent > indent {
			p.index = next
			return p.parseBlock(child.indent)
		}
	}
	return nil, syntax(lineNumber, 22, "mapping key has no value")
}

func (p *blockParser) parseInline(source string, sourceIndex int) (Value, *syntaxError) {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil, syntax(p.lineNumber(sourceIndex), 22, "mapping key has no value")
	}
	if source[0] == '[' || source[0] == '{' {
		text := p.flowText(source, sourceIndex)
		flow := flowParser{text: text, firstLine: p.lineNumber(sourceIndex)}
		value, err := flow.parseValue()
		if err != nil {
			return nil, err
		}
		lineEnd := strings.IndexByte(flow.text[flow.index:], '\n')
		tail := flow.text[flow.index:]
		if lineEnd >= 0 {
			tail = tail[:lineEnd]
		}
		if strings.TrimSpace(tail) != "" {
			return nil, syntax(flow.line(), 16, "trailing text after flow collection")
		}
		consumed := strings.Count(flow.text[:flow.index], "\n")
		p.index = sourceIndex + consumed + 1
		return value, nil
	}
	if source[0] == '\'' || source[0] == '"' {
		return decodeQuoted(source, p.lineNumber(sourceIndex))
	}
	if containsPlainColonSpace(source) {
		return nil, syntax(p.lineNumber(sourceIndex), 17, "unquoted `: ` inside a value")
	}
	return source, nil
}

func (p *blockParser) flowText(source string, sourceIndex int) string {
	var text strings.Builder
	text.WriteString(source)
	for index := sourceIndex + 1; index < len(p.lines); index++ {
		text.WriteByte('\n')
		text.WriteString(strings.TrimSuffix(stripComment(p.lines[index].raw), "\r"))
	}
	return text.String()
}

func (p *blockParser) nextContent(index int) int {
	for index < len(p.lines) && p.lines[index].body == "" {
		index++
	}
	return index
}

func (p *blockParser) lineNumber(index int) int {
	if index < 0 || index >= len(p.lines) {
		return 0
	}
	return p.lines[index].number
}

func splitBlockMapping(source string) (key, value string, ok bool) {
	var quote rune
	depth := 0
	escaped := false
	for offset := 0; offset < len(source); {
		r, size := runeAt(source, offset)
		next := offset + size
		if quote != 0 {
			if quote == '"' && escaped {
				escaped = false
			} else if quote == '"' && r == '\\' {
				escaped = true
			} else if quote == r {
				if quote == '\'' && next < len(source) && source[next] == '\'' {
					next++
				} else {
					quote = 0
				}
			}
		} else if r == '\'' || r == '"' {
			quote = r
		} else if r == '[' || r == '{' {
			depth++
		} else if r == ']' || r == '}' {
			depth--
		} else if r == ':' && depth == 0 {
			if next == len(source) || unicode.IsSpace(rune(source[next])) {
				return strings.TrimSpace(source[:offset]), strings.TrimSpace(source[next:]), true
			}
		}
		offset = next
	}
	return "", "", false
}

func mappingValue(source string) string {
	_, value, ok := splitBlockMapping(source)
	if !ok {
		return ""
	}
	return value
}

func parseBlockKey(source string, line int) (string, *syntaxError) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", syntax(line, 22, "mapping key has no value")
	}
	if source[0] == '\'' || source[0] == '"' {
		return decodeQuoted(source, line)
	}
	if strings.ContainsAny(source, "[]{}") {
		return "", syntax(line, 14, "malformed block mapping")
	}
	return source, nil
}

func isSequenceLine(source string) bool {
	return source == "-" || (strings.HasPrefix(source, "-") && len(source) > 1 && unicode.IsSpace(rune(source[1])))
}
