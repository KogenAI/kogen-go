package exunit

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"

	"kogen-go/internal/findings"
)

// ParseFindings parses ExUnit failures, Elixir compiler diagnostics, Credo
// warnings and Mix formatter output. It preserves stable path/rule/symbol
// identities for baseline comparison.
func ParseFindings(output []byte, workdir string) []findings.Finding {
	text := string(bytes.ToValidUTF8(output, []byte("�")))
	lines := strings.Split(text, "\n")
	for index := range lines {
		lines[index] = strings.TrimSuffix(lines[index], "\r")
	}
	result := parseTestFailures(lines, workdir)
	for _, line := range lines {
		if finding, ok := compilerFinding(line, workdir); ok {
			result = append(result, finding)
		}
	}
	result = append(result, parseCredo(lines, workdir)...)
	result = append(result, parseFormat(lines, workdir)...)
	return result
}

func parseTestFailures(lines []string, workdir string) []findings.Finding {
	var result []findings.Finding
	var name string
	var block []string
	flush := func() {
		if name == "" {
			return
		}
		if finding, ok := testFinding(name, block, workdir); ok {
			result = append(result, finding)
		}
		name = ""
		block = nil
	}
	for _, line := range lines {
		if next := failureName(line); next != "" {
			flush()
			name, block = next, []string{line}
			continue
		}
		if name != "" {
			if strings.HasPrefix(strings.TrimSpace(cleanCLILine(line)), "Finished in ") {
				flush()
			} else {
				block = append(block, line)
			}
		}
	}
	flush()
	return result
}

func failureName(line string) string {
	clean := cleanCLILine(line)
	_, remainder, ok := strings.Cut(clean, ") test ")
	if !ok {
		return ""
	}
	end := strings.LastIndex(remainder, " (")
	if end < 0 {
		return ""
	}
	name := strings.TrimPrefix(remainder[:end], "test ")
	return strings.TrimSpace(name)
}

func testFinding(name string, block []string, workdir string) (findings.Finding, bool) {
	var path string
	var line, column *uint32
	for _, raw := range block {
		if parsedPath, parsedLine, parsedColumn, ok := parseLocation(cleanCLILine(raw)); ok && isElixirPath(parsedPath) {
			path = normalizePath(parsedPath, workdir)
			line = uint32Pointer(parsedLine)
			column = parsedColumn
			break
		}
	}
	assertion, environmental := false, false
	var message string
	for _, raw := range block {
		clean := cleanCLILine(raw)
		if strings.Contains(clean, "Assertion") || strings.Contains(clean, "match (=) failed") || strings.Contains(clean, "Expected truthy") {
			assertion = true
		}
		lower := strings.ToLower(clean)
		if strings.Contains(lower, "no such file or directory") || strings.Contains(lower, "permission denied") || strings.Contains(lower, "operation not permitted") || strings.Contains(lower, "command not found") {
			environmental = true
		}
		if message == "" && (strings.Contains(clean, "Assertion") || strings.Contains(clean, "match (=) failed") || strings.HasPrefix(clean, "** (") || strings.Contains(clean, "Expected truthy")) {
			message = clean
		}
	}
	if message == "" {
		message = "test failed"
	}
	rule := "exunit/failure"
	if assertion {
		rule = "exunit/assertion"
	}
	if environmental {
		rule = "exunit/environment"
	}
	return findings.Finding{Path: path, Rule: rule, Symbol: name, Message: message, Severity: "error", Line: line, Column: column}, true
}

func compilerFinding(raw, workdir string) (findings.Finding, bool) {
	line := cleanCLILine(raw)
	markers := []string{"(CompileError) ", "(SyntaxError) ", "(TokenMissingError) ", "(CompileWarning) "}
	markerAt, markerLen := -1, 0
	for _, marker := range markers {
		if index := strings.Index(line, marker); index >= 0 {
			markerAt, markerLen = index, len(marker)
			break
		}
	}
	if markerAt < 0 {
		return findings.Finding{}, false
	}
	diagnostic := strings.TrimSpace(line[markerAt+markerLen:])
	pathEnd := strings.IndexAny(diagnostic, " \t")
	location := diagnostic
	if pathEnd >= 0 {
		location = diagnostic[:pathEnd]
	}
	path, lineNumber, column, ok := parseLocation(location)
	if !ok {
		return findings.Finding{}, false
	}
	detail := strings.TrimSpace(strings.TrimPrefix(diagnostic[len(location):], ":"))
	lower := strings.ToLower(diagnostic)
	kind := "compile_error"
	switch {
	case strings.Contains(lower, "undefined or private") || strings.Contains(lower, "undefined function"):
		kind = "undefined"
	case strings.Contains(lower, "deprecated"):
		kind = "deprecated"
	case strings.Contains(lower, "unused"):
		kind = "unused"
	}
	if detail == "" {
		detail = diagnostic
	}
	return findings.Finding{
		Path: normalizePath(path, workdir), Rule: "compile/" + kind,
		Message: detail, Severity: "error", Line: uint32Pointer(lineNumber), Column: column,
	}, true
}

func parseCredo(lines []string, workdir string) []findings.Finding {
	var result []findings.Finding
	var severity string
	var messages []string
	flush := func() {
		if severity == "" {
			return
		}
		joined := strings.Join(messages, " ")
		var path string
		var line, column *uint32
		for _, message := range messages {
			if parsedPath, parsedLine, parsedColumn, ok := parseLocation(message); ok && isElixirPath(parsedPath) {
				path = normalizePath(parsedPath, workdir)
				line, column = uint32Pointer(parsedLine), parsedColumn
				break
			}
		}
		result = append(result, findings.Finding{
			Path: path, Rule: "credo/" + credoRule(joined), Message: strings.Join(strings.Fields(joined), " "),
			Severity: credoSeverity(severity), Line: line, Column: column,
		})
		severity, messages = "", nil
	}
	for _, raw := range lines {
		line := cleanCLILine(raw)
		if nextSeverity, firstMessage, ok := credoHeader(line); ok {
			flush()
			severity, messages = nextSeverity, []string{firstMessage}
			continue
		}
		if severity == "" || line == "" {
			continue
		}
		messages = append(messages, line)
		if path, _, _, ok := parseLocation(line); ok && isElixirPath(path) {
			flush()
		}
	}
	flush()
	return result
}

func credoHeader(line string) (severity, message string, ok bool) {
	if !strings.HasPrefix(line, "[") {
		return "", "", false
	}
	end := strings.IndexByte(line, ']')
	if end < 0 {
		return "", "", false
	}
	severity = line[1:end]
	if severity != "F" && severity != "W" && severity != "C" && severity != "R" && severity != "D" {
		return "", "", false
	}
	message = strings.TrimSpace(line[end+1:])
	message = strings.TrimLeft(message, "↗↘→ ")
	return severity, strings.TrimSpace(message), true
}

func credoSeverity(value string) string {
	if value == "F" || value == "W" {
		return "warning"
	}
	return "note"
}

func credoRule(message string) string {
	for _, prefix := range []string{"Credo.Check.", "Warning."} {
		if index := strings.Index(message, prefix); index >= 0 {
			start := index + len(prefix)
			end := start
			for end < len(message) {
				ch := message[end]
				if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '.' {
					end++
				} else {
					break
				}
			}
			if end > start {
				return message[start:end]
			}
			return "unknown"
		}
	}
	if strings.Contains(message, "spans ") {
		return "ModuleSize"
	}
	if strings.Contains(message, "File has ") {
		return "FileSize"
	}
	return "unknown"
}

func parseFormat(lines []string, workdir string) []findings.Finding {
	var result []findings.Finding
	listing := false
	for _, raw := range lines {
		line := cleanCLILine(raw)
		if strings.Contains(line, "The following files are not formatted:") {
			listing = true
			continue
		}
		if listing {
			path, ok := strings.CutPrefix(line, "* ")
			if !ok {
				path, ok = strings.CutPrefix(line, "- ")
			}
			if ok {
				result = append(result, formatFinding(path, workdir))
			} else if line != "" {
				listing = false
			}
		}
		if strings.Contains(line, "mix format failed") {
			for _, field := range strings.Fields(line) {
				field = strings.Trim(field, "*`'\"()[]{}:,;")
				if isElixirPath(field) {
					result = append(result, formatFinding(field, workdir))
				}
			}
		}
	}
	return result
}

func formatFinding(path, workdir string) findings.Finding {
	return findings.Finding{Path: normalizePath(strings.TrimSpace(path), workdir), Rule: "format/unformatted", Message: "file is not formatted", Severity: "error"}
}

func parseLocation(value string) (string, uint32, *uint32, bool) {
	value = strings.TrimSpace(strings.TrimSuffix(value, ":"))
	last := strings.LastIndexByte(value, ':')
	if last <= 0 || last == len(value)-1 {
		return "", 0, nil, false
	}
	lastNumber, err := parsePosition(value[last+1:])
	if err != nil {
		return "", 0, nil, false
	}
	previous := strings.LastIndexByte(value[:last], ':')
	if previous > 0 {
		if line, lineErr := parsePosition(value[previous+1 : last]); lineErr == nil {
			return value[:previous], line, uint32Pointer(lastNumber), true
		}
	}
	return value[:last], lastNumber, nil, true
}

func parsePosition(value string) (uint32, error) {
	var position uint64
	if value == "" {
		return 0, fmt.Errorf("empty line number")
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("invalid line number")
		}
		position = position*10 + uint64(ch-'0')
		if position > uint64(^uint32(0)) {
			return 0, fmt.Errorf("line number overflows")
		}
	}
	return uint32(position), nil
}

func cleanCLILine(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimLeft(line, "┃│└─")
	return strings.TrimSpace(line)
}

func normalizePath(value, workdir string) string {
	if relative, ok := strings.CutPrefix(value, "$WORKDIR/"); ok {
		return filepath.ToSlash(relative)
	}
	path := filepath.Clean(value)
	if workdir != "" {
		if relative, err := filepath.Rel(workdir, path); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return filepath.ToSlash(relative)
		}
	}
	if filepath.IsAbs(path) {
		parts := strings.Split(filepath.ToSlash(path), "/")
		for index, part := range parts {
			if part == "lib" || part == "test" || part == "src" {
				return strings.Join(parts[index:], "/")
			}
		}
	}
	return filepath.ToSlash(path)
}

func isElixirPath(value string) bool {
	ext := filepath.Ext(value)
	return ext == ".ex" || ext == ".exs"
}

func uint32Pointer(value uint32) *uint32 { return &value }
