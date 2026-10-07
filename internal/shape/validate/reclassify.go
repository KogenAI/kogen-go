package validate

import (
	"bytes"
	"fmt"
	"strings"

	"kogen-go/internal/intent"
)

// Reclassify reconciles Verify kinds with their observed base results. It
// preserves the original Request bytes and line endings, groups warnings by
// direction, and reports whether at least one change item is fully red.
func Reclassify(slug string, source []byte, baseResults map[string]bool) ([]byte, []Warning, bool, *Failure) {
	parsed, failure := parseAndLint(slug, source)
	if failure != nil {
		return nil, nil, false, failure
	}
	prefixEnd := headingStart(source, "Request")
	prefix := bytes.Clone(source[:prefixEnd])
	lines := splitSourceLines(prefix)
	verifyByLine := make(map[int]intent.VerifyItem, len(parsed.Verify))
	for _, item := range parsed.Verify {
		verifyByLine[item.Line] = item
	}
	var failedKeep, passedChange []string
	lineNumber := 0
	for index, line := range lines {
		lineNumber++
		item, ok := verifyByLine[lineNumber]
		if !ok {
			continue
		}
		passed := baseResults[item.ID]
		if passed == item.IsKeep() {
			continue
		}
		updated, ok := rewriteVerifyKind(line, passed)
		if !ok {
			return nil, nil, false, &Failure{Reason: "intent_parse_failed", Detail: fmt.Sprintf("could not reclassify Verify entry %s", item.ID)}
		}
		lines[index] = updated
		if passed {
			passedChange = append(passedChange, item.ID)
		} else {
			failedKeep = append(failedKeep, item.ID)
		}
	}
	newPrefix := bytes.Join(lines, nil)
	updated := appendRequest(newPrefix, requestBytes(source))
	if _, failure := parseAndLintAllowNoChange(slug, updated); failure != nil {
		return nil, nil, false, failure
	}
	warnings := make([]Warning, 0, 2)
	if len(failedKeep) != 0 {
		warnings = append(warnings, reclassifiedWarning(failedKeep, "test"))
	}
	if len(passedChange) != 0 {
		warnings = append(warnings, reclassifiedWarning(passedChange, "test keep"))
	}
	finalIntent, parseErr := intent.Parse(slug, updated)
	if parseErr != nil {
		return nil, nil, false, &Failure{Reason: "intent_parse_failed", Detail: parseErr.Error()}
	}
	redChange := false
	for _, item := range finalIntent.Verify {
		if item.IsChange() && !baseResults[item.ID] {
			redChange = true
			break
		}
	}
	return updated, warnings, redChange, nil
}

func parseAndLintAllowNoChange(slug string, source []byte) (*intent.Intent, *Failure) {
	parsed, err := intent.Parse(slug, source)
	if err != nil {
		return nil, &Failure{Reason: "intent_parse_failed", Detail: err.Error()}
	}
	var errorsFound []string
	for _, issue := range parsed.LintForShaping() {
		if issue.Severity != intent.LintError || issue.Rule == "no_change_item" {
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

func splitSourceLines(source []byte) [][]byte {
	if len(source) == 0 {
		return nil
	}
	var lines [][]byte
	for start := 0; start < len(source); {
		relativeEnd := bytes.IndexByte(source[start:], '\n')
		if relativeEnd < 0 {
			lines = append(lines, bytes.Clone(source[start:]))
			break
		}
		end := start + relativeEnd + 1
		lines = append(lines, bytes.Clone(source[start:end]))
		start = end
	}
	return lines
}

func rewriteVerifyKind(line []byte, makeKeep bool) ([]byte, bool) {
	ending := []byte{}
	body := line
	if bytes.HasSuffix(body, []byte{'\n'}) {
		ending = []byte{'\n'}
		body = body[:len(body)-1]
	}
	if bytes.HasSuffix(body, []byte{'\r'}) {
		ending = append([]byte{'\r'}, ending...)
		body = body[:len(body)-1]
	}
	colon := bytes.IndexByte(body, ':')
	if colon < 0 {
		return nil, false
	}
	value := body[colon+1:]
	leading := len(value) - len(bytes.TrimLeft(value, " \t"))
	content := value[leading:]
	trailing := len(content) - len(bytes.TrimRight(content, " \t"))
	core := content[:len(content)-trailing]
	if !bytes.HasPrefix(core, []byte("test")) || (len(core) > 4 && core[4] != ' ' && core[4] != '\t') {
		return nil, false
	}
	rest := core[len("test"):]
	restTrimmed := bytes.TrimLeft(rest, " \t")
	secondEnd := bytes.IndexAny(restTrimmed, " \t")
	second := restTrimmed
	modifiers := []byte{}
	if secondEnd >= 0 {
		second = restTrimmed[:secondEnd]
		modifiers = bytes.TrimLeft(restTrimmed[secondEnd:], " \t")
	}
	isKeep := bytes.Equal(second, []byte("keep"))
	if makeKeep {
		if isKeep {
			return bytes.Clone(line), true
		}
		if len(restTrimmed) == 0 {
			core = []byte("test keep")
		} else {
			core = append([]byte("test keep "), restTrimmed...)
		}
	} else if isKeep {
		if len(modifiers) == 0 {
			core = []byte("test")
		} else {
			core = append([]byte("test "), modifiers...)
		}
	} else {
		return bytes.Clone(line), true
	}
	updated := make([]byte, 0, len(body)+len(ending)+8)
	updated = append(updated, body[:colon+1]...)
	updated = append(updated, value[:leading]...)
	updated = append(updated, core...)
	updated = append(updated, content[len(content)-trailing:]...)
	updated = append(updated, ending...)
	return updated, true
}

func requestBytes(source []byte) []byte {
	start := headingStart(source, "Request")
	if start == len(source) {
		return nil
	}
	lineEnd := bytes.IndexByte(source[start:], '\n')
	if lineEnd < 0 {
		return nil
	}
	return source[start+lineEnd+1:]
}

func appendRequest(prefix, request []byte) []byte {
	var output bytes.Buffer
	output.Grow(len(prefix) + len(request) + 16)
	output.Write(prefix)
	if bytes.HasSuffix(prefix, []byte("\n\n")) || bytes.HasSuffix(prefix, []byte("\r\n\r\n")) || bytes.HasSuffix(prefix, []byte("\n\r\n")) {
		// Normalize already left the exact Request separator in place.
	} else if bytes.HasSuffix(prefix, []byte{'\n'}) {
		output.WriteByte('\n')
	} else {
		output.WriteString("\n\n")
	}
	output.WriteString("## Request\n")
	output.Write(request)
	return output.Bytes()
}

func reclassifiedWarning(itemIDs []string, kind string) Warning {
	verb := "were"
	if len(itemIDs) == 1 {
		verb = "was"
	}
	return Warning{
		Code:    "shape_reclassified",
		ItemIDs: append([]string(nil), itemIDs...),
		Message: strings.Join(itemIDs, ", ") + " " + verb + " reclassified as " + kind,
	}
}
