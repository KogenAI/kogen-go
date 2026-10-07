package intent

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// LintSeverity distinguishes blocking structural problems from non-blocking
// style suggestions. Style findings are rendered as lint_<rule> card warnings.
type LintSeverity string

const (
	LintError LintSeverity = "error"
	LintStyle LintSeverity = "style"
)

// LintIssue is one structural or style finding. Line is nil for findings that
// describe the Intent as a whole rather than one source line.
type LintIssue struct {
	Rule     string       `json:"rule"`
	Severity LintSeverity `json:"severity"`
	Line     *int         `json:"line,omitempty"`
	Message  string       `json:"message"`
}

// WarningCode returns the shape-warning code for a style finding. Structural
// errors do not become warnings and return an empty string.
func (issue LintIssue) WarningCode() string {
	if issue.Severity != LintStyle {
		return ""
	}
	return "lint_" + issue.Rule
}

// LintOptions supplies the shaping-only context needed for gate-path checks.
// Ordinary approval lint leaves these fields empty and does not report a gate
// path as undeclared.
type LintOptions struct {
	Shaping   bool
	Test      []byte
	GatePaths []string
}

// Lint returns structural errors and non-blocking style findings suitable for
// approval. The opaque Request section is not inspected.
func (intent *Intent) Lint() []LintIssue {
	return intent.LintWithOptions(LintOptions{})
}

// LintForShaping returns the same findings as Lint plus shaping-only style
// requirements such as the Notes Approach line. Gate paths can be supplied
// through LintWithOptions.
func (intent *Intent) LintForShaping() []LintIssue {
	return intent.LintWithOptions(LintOptions{Shaping: true})
}

// LintWithOptions returns structural and style findings for the supplied
// context. Style findings never change severity based on mode; callers may
// repair them during shaping and present remaining findings as warnings.
func (intent *Intent) LintWithOptions(options LintOptions) []LintIssue {
	if intent == nil {
		return nil
	}

	var issues []LintIssue
	if strings.TrimSpace(intent.Brief) == "" {
		issues = append(issues, errorIssue("missing_brief", nil, "write the Brief as prose"))
	}
	if line := firstBriefLine(intent.BriefLines, isList); line != nil {
		issues = append(issues, errorIssue("list_in_brief", line, "the Brief cannot contain lists"))
	}
	if line := firstBriefLine(intent.BriefLines, isHeading); line != nil {
		issues = append(issues, errorIssue("heading_in_brief", line, "the Brief cannot contain headings"))
	}
	if line := firstBriefLine(intent.BriefLines, isCodeFence); line != nil {
		issues = append(issues, errorIssue("code_block_in_brief", line, "the Brief cannot contain code blocks"))
	}
	if intent.Frontmatter.Size != "small" && intent.Frontmatter.Size != "medium" && intent.Frontmatter.Size != "large" {
		issues = append(issues, errorIssue("unknown_size", nil, "size must be small, medium, or large"))
	}
	if len(intent.Acceptance) == 0 {
		issues = append(issues, errorIssue("acceptance_count", nil, "Acceptance needs at least one item"))
	}
	if strings.TrimSpace(intent.Frontmatter.Title) == "" {
		issues = append(issues, errorIssue("missing_title", nil, "title is required"))
	}
	if len(intent.Frontmatter.Domains) < 1 || len(intent.Frontmatter.Domains) > 4 {
		issues = append(issues, errorIssue("domain_count", nil, "declare between one and four domains"))
	}

	issues = append(issues, acceptanceIDIssues(intent)...)
	issues = append(issues, verifyIssues(intent)...)
	if !hasChangeItem(intent.Verify) {
		issues = append(issues, errorIssue("no_change_item", nil, "at least one Acceptance item must be a change item (test)"))
	}
	if line := firstOpenQuestion(intent); line != nil {
		issues = append(issues, errorIssue("open_question", line, "remove TBD, TODO, FIXME, or unresolved question markers"))
	}

	issues = append(issues, styleFindings(intent, options.Shaping)...)
	if options.Shaping && !intent.Frontmatter.ChangesGate {
		for _, path := range options.GatePaths {
			if path != "" && (strings.Contains(intent.Notes, path) || bytes.Contains(options.Test, []byte(path))) {
				issues = append(issues, errorIssue("undeclared_gate_path", nil, fmt.Sprintf("Gate-path edit requires `changes_gate: true`; matched path %s.", path)))
				break
			}
		}
	}
	return issues
}

// LintWarning is the card representation of one non-blocking style finding.
type LintWarning struct {
	Code    string   `json:"code"`
	ItemIDs []string `json:"item_ids"`
	Message string   `json:"message"`
}

// StyleWarnings converts approval-mode style findings into the warning shape
// used by shape-warnings.json and approval cards.
func (intent *Intent) StyleWarnings() []LintWarning {
	if intent == nil {
		return nil
	}
	var warnings []LintWarning
	for _, issue := range intent.Lint() {
		code := issue.WarningCode()
		if code == "" {
			continue
		}
		itemIDs := make([]string, 0, 1)
		if issue.Line != nil {
			for _, item := range intent.Acceptance {
				if item.Line == *issue.Line {
					itemIDs = append(itemIDs, item.ID)
					break
				}
			}
		}
		warnings = append(warnings, LintWarning{Code: code, ItemIDs: itemIDs, Message: issue.Message})
	}
	return warnings
}

func firstBriefLine(lines []TextLine, predicate func(string) bool) *int {
	for _, line := range lines {
		if predicate(line.Text) {
			return intPointer(line.Line)
		}
	}
	return nil
}

func isList(text string) bool {
	text = strings.TrimLeftFunc(text, unicode.IsSpace)
	runes := []rune(text)
	if len(runes) >= 2 && (runes[0] == '-' || runes[0] == '*' || runes[0] == '+') && unicode.IsSpace(runes[1]) {
		return true
	}
	digits := 0
	for digits < len(runes) && runes[digits] >= '0' && runes[digits] <= '9' {
		digits++
	}
	return digits > 0 && digits+1 < len(runes) && (runes[digits] == '.' || runes[digits] == ')') && unicode.IsSpace(runes[digits+1])
}

func isHeading(text string) bool {
	text = strings.TrimLeftFunc(text, unicode.IsSpace)
	hashes := 0
	for hashes < len(text) && text[hashes] == '#' {
		hashes++
	}
	if hashes < 1 || hashes > 6 || hashes >= len(text) {
		return false
	}
	character, _ := utf8.DecodeRuneInString(text[hashes:])
	return unicode.IsSpace(character)
}

func isCodeFence(text string) bool {
	text = strings.TrimLeftFunc(text, unicode.IsSpace)
	return strings.HasPrefix(text, "```") || strings.HasPrefix(text, "~~~")
}

func acceptanceIDIssues(intent *Intent) []LintIssue {
	var issues []LintIssue
	seen := make(map[string]struct{}, len(intent.Acceptance))
	for _, item := range intent.Acceptance {
		if _, exists := seen[item.ID]; exists {
			issues = append(issues, errorIssue("duplicate_id", intPointer(item.Line), "Acceptance ids must be unique"))
			break
		}
		seen[item.ID] = struct{}{}
	}
	for index, item := range intent.Acceptance {
		if item.ID != fmt.Sprintf("A%d", index+1) {
			issues = append(issues, errorIssue("sequential_ids", nil, "Acceptance ids must be A1 through An in order"))
			break
		}
	}
	return issues
}

func verifyIssues(intent *Intent) []LintIssue {
	var issues []LintIssue
	for _, acceptance := range intent.Acceptance {
		if intent.VerifyFor(acceptance.ID) == nil {
			issues = append(issues, errorIssue("missing_verify", nil, "every Acceptance item needs a Verify kind"))
			break
		}
	}
	for _, item := range intent.Verify {
		if len(item.Words) == 0 {
			issues = append(issues, errorIssue("invalid_verify", intPointer(item.Line), `unknown Verify word ""`))
			continue
		}
		kind := item.Words[0]
		if kind == "example" || kind == "check" {
			issues = append(issues, errorIssue("unsupported_verify_kind", intPointer(item.Line), kind+" is not supported in core v1"))
			continue
		}
		if kind != "test" {
			issues = append(issues, errorIssue("invalid_verify", intPointer(item.Line), fmt.Sprintf("unknown Verify word %q", kind)))
			continue
		}
		modifierIndex := 1
		if len(item.Words) > 1 && item.Words[1] == "keep" {
			modifierIndex = 2
		}
		for _, modifier := range item.Words[modifierIndex:] {
			valid := modifier == "integration"
			if value, ok := strings.CutPrefix(modifier, "domain="); ok && value != "" {
				valid = true
			}
			if value, ok := strings.CutPrefix(modifier, "after="); ok && value != "" {
				valid = true
			}
			if !valid {
				issues = append(issues, errorIssue("invalid_verify", intPointer(item.Line), fmt.Sprintf("unknown Verify word %q", modifier)))
				break
			}
		}
	}
	return issues
}

func hasChangeItem(items []VerifyItem) bool {
	for _, item := range items {
		if item.IsChange() {
			return true
		}
	}
	return false
}

func firstOpenQuestion(intent *Intent) *int {
	for _, line := range intent.BriefLines {
		if containsOpenMarker(line.Text) {
			return intPointer(line.Line)
		}
	}
	for _, item := range intent.Acceptance {
		if containsOpenMarker(item.Text) {
			return intPointer(item.Line)
		}
	}
	for _, line := range intent.NotesLines {
		if containsOpenMarker(line.Text) {
			return intPointer(line.Line)
		}
	}
	return nil
}

func containsOpenMarker(text string) bool {
	upper := asciiUpper(text)
	for _, marker := range []string{"TBD", "TODO", "FIXME"} {
		if containsWord(upper, marker) {
			return true
		}
	}
	if strings.Contains(upper, "??") {
		return true
	}
	for offset := 0; ; {
		index := strings.IndexByte(upper[offset:], '[')
		if index < 0 {
			return false
		}
		start := offset + index + 1
		tail := strings.TrimLeft(upper[start:], " \t\r\n\v\f")
		if strings.HasPrefix(tail, "NEEDS CLARIFICATION") {
			return true
		}
		offset = start
	}
}

func containsWord(text, word string) bool {
	for offset := 0; offset <= len(text)-len(word); {
		index := strings.Index(text[offset:], word)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(word)
		if !asciiWordBoundaryBefore(text, start) || !asciiWordBoundaryAfter(text, end) {
			offset = start + 1
			continue
		}
		return true
	}
	return false
}

func asciiWordBoundaryBefore(text string, offset int) bool {
	if offset == 0 {
		return true
	}
	previous, _ := utf8.DecodeLastRuneInString(text[:offset])
	return !asciiWordRune(previous)
}

func asciiWordBoundaryAfter(text string, offset int) bool {
	if offset >= len(text) {
		return true
	}
	current, _ := utf8.DecodeRuneInString(text[offset:])
	return !asciiWordRune(current)
}

func asciiWordRune(r rune) bool {
	return r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

func errorIssue(rule string, line *int, message string) LintIssue {
	return LintIssue{Rule: rule, Severity: LintError, Line: line, Message: message}
}

func intPointer(value int) *int { return &value }
