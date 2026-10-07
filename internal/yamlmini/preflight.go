package yamlmini

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	maxDocumentBytes = 1 << 20
	maxFlowDepth     = 64
)

// Issue describes the first input error. Line is zero for document-wide errors
// such as an oversized document or an empty document.
type Issue struct {
	Line    int
	Message string
}

func (issue *Issue) Error() string {
	if issue.Line > 0 {
		return fmt.Sprintf("line %d: %s", issue.Line, issue.Message)
	}
	return issue.Message
}

type candidate struct {
	line    int
	order   int
	message string
}

type lineCheck func(lines []string, errors *[]candidate)

// preflight applies the byte-level and lexical checks before callers parse YAML.
// Additional structural checks share the same candidate list so the selected
// error is always the earliest line, then the lowest catalog order.
func preflight(source []byte, additional ...lineCheck) (string, *Issue) {
	if len(source) > maxDocumentBytes {
		return "", &Issue{Message: "document exceeds the maximum size of 1048576 bytes"}
	}
	if bytes.HasPrefix(source, []byte{0xef, 0xbb, 0xbf}) {
		return "", &Issue{Line: 1, Message: "leading UTF-8 BOM is not allowed"}
	}
	if offset, ok := invalidUTF8Offset(source); !ok {
		line := bytes.Count(source[:offset], []byte{'\n'}) + 1
		return "", &Issue{Line: line, Message: "document is not valid UTF-8"}
	}

	text := string(source)
	lines := strings.Split(text, "\n")
	var errors []candidate
	scanLines(lines, &errors)
	for _, check := range additional {
		check(lines, &errors)
	}
	if emptyDocument(lines) {
		addCandidate(&errors, 0, 23, "empty document")
	}
	if first, ok := firstCandidate(errors); ok {
		return "", &Issue{Line: first.line, Message: first.message}
	}
	return text, nil
}

func invalidUTF8Offset(source []byte) (int, bool) {
	for offset := 0; offset < len(source); {
		r, size := utf8.DecodeRune(source[offset:])
		if r == utf8.RuneError && size == 1 {
			return offset, false
		}
		offset += size
	}
	return 0, true
}

func addCandidate(errors *[]candidate, line, order int, message string) {
	*errors = append(*errors, candidate{line: line, order: order, message: message})
}

func firstCandidate(errors []candidate) (candidate, bool) {
	if len(errors) == 0 {
		return candidate{}, false
	}
	first := errors[0]
	for _, current := range errors[1:] {
		if current.line < first.line || (current.line == first.line && current.order < first.order) {
			first = current
		}
	}
	return first, true
}

func emptyDocument(lines []string) bool {
	for _, source := range lines {
		line := strings.TrimSuffix(source, "\r")
		if strings.TrimSpace(stripComment(line)) != "" {
			return false
		}
	}
	return true
}
