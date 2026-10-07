package validate

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"kogen-go/internal/intent"
)

// Normalize removes a model-written Request section, applies the Notes
// Approach-prefix rule, then appends request verbatim. Request bytes are
// opaque: CRLF, invalid UTF-8, NUL, and a missing final newline are retained.
func Normalize(generated, request []byte) ([]byte, *Failure) {
	if !utf8.Valid(generated) {
		return nil, &Failure{Reason: "intent_parse_failed", Detail: "Intent is not valid UTF-8"}
	}
	prefix := generated[:headingStart(generated, "Request")]
	prefix = normalizeNotes(prefix)
	var output bytes.Buffer
	output.Grow(len(prefix) + len(request) + len("\n\n## Request\n"))
	output.Write(prefix)
	if bytes.HasSuffix(prefix, []byte{'\n'}) {
		output.WriteByte('\n')
	} else {
		output.WriteString("\n\n")
	}
	output.WriteString("## Request\n")
	output.Write(request)
	return output.Bytes(), nil
}

func headingStart(source []byte, section string) int {
	offset := 0
	for offset < len(source) {
		end := bytes.IndexByte(source[offset:], '\n')
		if end < 0 {
			end = len(source)
		} else {
			end += offset + 1
		}
		line := source[offset:end]
		line = bytes.TrimSuffix(line, []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if strings.TrimSpace(string(line)) == "## "+section {
			return offset
		}
		offset = end
	}
	return len(source)
}

func normalizeNotes(prefix []byte) []byte {
	start, end, ok := notesRange(prefix)
	if !ok {
		return bytes.Clone(prefix)
	}
	body := string(prefix[start:end])
	leading := len(body) - len(strings.TrimLeftFunc(body, unicode.IsSpace))
	normalized, changed := intent.NormalizeNotes(body[leading:])
	if !changed {
		return bytes.Clone(prefix)
	}
	out := make([]byte, 0, len(prefix)+len(normalized)-len(body)+leading)
	out = append(out, prefix[:start]...)
	out = append(out, body[:leading]...)
	out = append(out, normalized...)
	out = append(out, prefix[end:]...)
	return out
}

func notesRange(source []byte) (int, int, bool) {
	offset := 0
	bodyStart := -1
	for offset < len(source) {
		end := bytes.IndexByte(source[offset:], '\n')
		if end < 0 {
			end = len(source)
		} else {
			end += offset + 1
		}
		line := source[offset:end]
		logical := bytes.TrimSuffix(bytes.TrimSuffix(line, []byte{'\n'}), []byte{'\r'})
		trimmed := strings.TrimSpace(string(logical))
		if trimmed == "## Notes" {
			bodyStart = end
		} else if bodyStart >= 0 && isKnownHeading(trimmed) {
			return bodyStart, offset, true
		}
		offset = end
	}
	if bodyStart >= 0 {
		return bodyStart, len(source), true
	}
	return 0, 0, false
}

func isKnownHeading(line string) bool {
	switch line {
	case "## Acceptance", "## Verify", "## Notes", "## Request":
		return true
	default:
		return false
	}
}

func parseAndLint(slug string, source []byte) (*intent.Intent, *Failure) {
	parsed, err := intent.Parse(slug, source)
	if err != nil {
		return nil, &Failure{Reason: "intent_parse_failed", Detail: err.Error()}
	}
	issues := parsed.LintForShaping()
	var errorsFound []string
	for _, issue := range issues {
		if issue.Severity != intent.LintError {
			continue
		}
		message := issue.Message
		if issue.Line != nil {
			message = fmt.Sprintf("line %d: %s", *issue.Line, issue.Message)
		}
		errorsFound = append(errorsFound, issue.Rule+": "+message)
	}
	if len(errorsFound) != 0 {
		return nil, &Failure{Reason: "intent_lint_failed", Detail: strings.Join(errorsFound, "\n")}
	}
	return parsed, nil
}

func styleIssues(parsed *intent.Intent) []intent.LintIssue {
	var styles []intent.LintIssue
	for _, issue := range parsed.LintForShaping() {
		if issue.Severity == intent.LintStyle {
			styles = append(styles, issue)
		}
	}
	return styles
}

func styleWarning(parsed *intent.Intent, issue intent.LintIssue) Warning {
	ids := []string{}
	if issue.Line != nil {
		for _, item := range parsed.Acceptance {
			if item.Line == *issue.Line {
				ids = append(ids, item.ID)
				break
			}
		}
	}
	return Warning{Code: "lint_" + issue.Rule, ItemIDs: ids, Message: issue.Message}
}

func undeclaredGatePath(parsed *intent.Intent, test []byte, gatePaths []string) string {
	if parsed.Frontmatter.ChangesGate {
		return ""
	}
	for _, gatePath := range gatePaths {
		if gatePath != "" && (strings.Contains(parsed.Notes, gatePath) || bytes.Contains(test, []byte(gatePath))) {
			return gatePath
		}
	}
	return ""
}
