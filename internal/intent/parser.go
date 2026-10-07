// Package intent parses the byte-oriented Intent format used by Kogen.
package intent

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ParseError is the first structural Intent error. Line numbers are one-based.
type ParseError struct {
	Line    int
	Message string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("line %d: %s", e.Line, e.Message)
}

func parseError(line int, message string) *ParseError {
	return &ParseError{Line: line, Message: message}
}

// TextLine keeps the original logical line and its one-based source line.
// CRLF is normalized to LF for parsed sections, as required by the Intent
// grammar. Request is the exception and remains byte-for-byte unchanged.
type TextLine struct {
	Line int
	Text string
}

// AcceptanceItem is one parsed Acceptance row.
type AcceptanceItem struct {
	ID   string
	Text string
	Line int
}

// VerifyItem is one parsed Verify row. Words are retained even when a word is
// not supported, so lint can report the specific unsupported kind/modifier.
type VerifyItem struct {
	ID    string
	Words []string
	Line  int
}

func (item VerifyItem) Kind() string {
	if len(item.Words) == 0 {
		return ""
	}
	return item.Words[0]
}

func (item VerifyItem) IsKeep() bool {
	return len(item.Words) > 1 && item.Words[1] == "keep"
}

func (item VerifyItem) IsChange() bool {
	return item.Kind() == "test" && !item.IsKeep()
}

func (item VerifyItem) IsIntegration() bool {
	for _, word := range item.Words[1:] {
		if word == "integration" {
			return true
		}
	}
	return false
}

// Domain returns the last domain= modifier, matching the Intent contract.
func (item VerifyItem) Domain() string {
	domain := ""
	for _, word := range item.Words[1:] {
		if value, ok := strings.CutPrefix(word, "domain="); ok {
			domain = value
		}
	}
	return domain
}

// AfterIDs returns every after= modifier in source order.
func (item VerifyItem) AfterIDs() []string {
	var ids []string
	for _, word := range item.Words[1:] {
		if value, ok := strings.CutPrefix(word, "after="); ok {
			ids = append(ids, value)
		}
	}
	return ids
}

// Intent contains the parsed structural sections and the exact source bytes.
// Request is a string to retain arbitrary bytes without UTF-8 conversion; use
// []byte(Request) when passing it to a byte-oriented consumer. HasRequest
// distinguishes an absent Request section from an empty one.
type Intent struct {
	Slug        string
	Frontmatter Frontmatter
	Brief       string
	BriefLines  []TextLine
	Acceptance  []AcceptanceItem
	Verify      []VerifyItem
	Notes       string
	NotesLines  []TextLine
	Request     string
	HasRequest  bool
	rawBytes    []byte
}

// RawBytes returns a copy of the exact Intent source, including original line
// endings and any opaque bytes in Request.
func (intent *Intent) RawBytes() []byte {
	if intent == nil {
		return nil
	}
	return bytes.Clone(intent.rawBytes)
}

// VerifyFor returns the first Verify row with id, or nil when absent.
func (intent *Intent) VerifyFor(id string) *VerifyItem {
	for index := range intent.Verify {
		if intent.Verify[index].ID == id {
			return &intent.Verify[index]
		}
	}
	return nil
}

type sourceLine struct {
	text  []byte // excludes LF, includes a possible CR
	line  int
	start int
	end   int // includes LF when present
}

func (line sourceLine) logical() []byte {
	return bytes.TrimSuffix(line.text, []byte{'\r'})
}

type section uint8

const (
	sectionBrief section = iota
	sectionAcceptance
	sectionVerify
	sectionNotes
)

// Parse validates and parses an Intent. Bytes after a recognized ## Request
// heading are not decoded or normalized; all bytes from the end of that line
// through EOF are retained exactly.
func Parse(slug string, source []byte) (*Intent, error) {
	if !validSlug(slug) {
		return nil, parseError(1, "invalid slug")
	}
	lines := sourceLines(source)
	if len(lines) == 0 {
		return nil, parseError(1, "frontmatter must start with `---`")
	}
	if !utf8.Valid(lines[0].logical()) {
		return nil, parseError(1, "Intent is not valid UTF-8")
	}
	if !bytes.Equal(lines[0].logical(), []byte("---")) {
		return nil, parseError(1, "frontmatter must start with `---`")
	}

	closing := -1
	for index := 1; index < len(lines); index++ {
		if bytes.Equal(lines[index].logical(), []byte("---")) {
			closing = index
			break
		}
	}
	if closing < 0 {
		return nil, parseError(len(lines)+1, "frontmatter is missing its closing `---`")
	}
	frontmatterStart := lines[0].end
	frontmatterEnd := lines[closing].start
	frontmatter, err := parseFrontmatter(source[frontmatterStart:frontmatterEnd])
	if err != nil {
		return nil, err
	}

	intent := &Intent{Slug: slug, Frontmatter: frontmatter, rawBytes: bytes.Clone(source)}
	current := sectionBrief
	seen := make(map[section]bool)
	var brief, notes []TextLine
	var acceptanceRows, verifyRows []TextLine

	for _, line := range lines[closing+1:] {
		logical := line.logical()
		if !utf8.Valid(logical) {
			return nil, parseError(line.line, "Intent is not valid UTF-8")
		}
		text := string(logical)
		heading, next, known := knownHeading(strings.TrimSpace(text))
		if known {
			if seen[next] {
				return nil, parseError(line.line+1, "duplicate "+heading+" section")
			}
			seen[next] = true
			if next == sectionBrief { // Request is opaque after its marker.
				intent.HasRequest = true
				intent.Request = string(source[line.end:])
				break
			}
			current = next
			continue
		}
		if name, unknown := unknownHeading(strings.TrimSpace(text)); unknown && current != sectionBrief {
			return nil, parseError(line.line+1, fmt.Sprintf("unknown Intent section %q", name))
		}
		row := TextLine{Line: line.line, Text: text}
		switch current {
		case sectionBrief:
			brief = append(brief, row)
		case sectionAcceptance:
			acceptanceRows = append(acceptanceRows, row)
		case sectionVerify:
			verifyRows = append(verifyRows, row)
		case sectionNotes:
			notes = append(notes, row)
		}
	}

	acceptance, err := parseAcceptance(acceptanceRows)
	if err != nil {
		return nil, err
	}
	verify, err := parseVerify(verifyRows)
	if err != nil {
		return nil, err
	}
	if err := validateVerifyTargets(acceptance, verify); err != nil {
		return nil, err
	}
	intent.BriefLines = brief
	intent.Brief = trimTextLines(brief)
	intent.Acceptance = acceptance
	intent.Verify = verify
	intent.NotesLines = notes
	intent.Notes = trimTextLines(notes)
	return intent, nil
}

func sourceLines(source []byte) []sourceLine {
	var lines []sourceLine
	for start, number := 0, 1; start < len(source); number++ {
		end := bytes.IndexByte(source[start:], '\n')
		if end < 0 {
			lines = append(lines, sourceLine{text: source[start:], line: number, start: start, end: len(source)})
			break
		}
		end += start
		lines = append(lines, sourceLine{text: source[start:end], line: number, start: start, end: end + 1})
		start = end + 1
	}
	return lines
}

func knownHeading(value string) (string, section, bool) {
	switch value {
	case "## Acceptance":
		return "Acceptance", sectionAcceptance, true
	case "## Verify":
		return "Verify", sectionVerify, true
	case "## Notes":
		return "Notes", sectionNotes, true
	case "## Request":
		return "Request", sectionBrief, true
	default:
		return "", sectionBrief, false
	}
}

func unknownHeading(value string) (string, bool) {
	name, ok := strings.CutPrefix(value, "## ")
	return name, ok && name != ""
}

func parseAcceptance(rows []TextLine) ([]AcceptanceItem, error) {
	items := make([]AcceptanceItem, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.Text) == "" {
			continue
		}
		id, text, ok := parseIDEntry(row.Text)
		if !ok {
			return nil, parseError(row.Line, "Acceptance entries use `- A<n>: one sentence` on one line")
		}
		items = append(items, AcceptanceItem{ID: id, Text: text, Line: row.Line})
	}
	return items, nil
}

func parseVerify(rows []TextLine) ([]VerifyItem, error) {
	items := make([]VerifyItem, 0, len(rows))
	seen := make(map[string]bool)
	for _, row := range rows {
		if strings.TrimSpace(row.Text) == "" {
			continue
		}
		id, text, ok := parseIDEntry(row.Text)
		if !ok {
			return nil, parseError(row.Line, "Verify entries use `- A<n>: test` or `- A<n>: test keep`")
		}
		if seen[id] {
			return nil, parseError(row.Line, "duplicate Verify entry for "+id)
		}
		seen[id] = true
		items = append(items, VerifyItem{ID: id, Words: strings.Fields(text), Line: row.Line})
	}
	return items, nil
}

func parseIDEntry(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "-") {
		return "", "", false
	}
	line = line[1:]
	space := 0
	for space < len(line) && isYAMLWhitespace(line[space]) {
		space++
	}
	if space == 0 || space == len(line) || line[space] != 'A' {
		return "", "", false
	}
	start := space + 1
	end := start
	for end < len(line) && line[end] >= '0' && line[end] <= '9' {
		end++
	}
	if end == start || end >= len(line) || line[end] != ':' {
		return "", "", false
	}
	text := strings.TrimSpace(line[end+1:])
	return "A" + line[start:end], text, true
}

func isYAMLWhitespace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n' || value == '\v' || value == '\f'
}

func validateVerifyTargets(acceptance []AcceptanceItem, verify []VerifyItem) error {
	accepted := make(map[string]bool, len(acceptance))
	for _, item := range acceptance {
		accepted[item.ID] = true
	}
	for _, item := range verify {
		if !accepted[item.ID] {
			return parseError(item.Line, "Verify entry "+item.ID+" has no Acceptance item")
		}
	}
	return nil
}

func trimTextLines(lines []TextLine) string {
	parts := make([]string, len(lines))
	for index, line := range lines {
		parts[index] = line.Text
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func validSlug(slug string) bool {
	if len(slug) < 3 || len(slug) > 48 || slug[0] == '-' || slug[len(slug)-1] == '-' {
		return false
	}
	previousDash := false
	for _, character := range []byte(slug) {
		if character == '-' {
			if previousDash {
				return false
			}
			previousDash = true
			continue
		}
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
		previousDash = false
	}
	return true
}
