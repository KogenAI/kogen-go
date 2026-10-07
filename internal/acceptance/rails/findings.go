package rails

import (
	"path/filepath"
	"strconv"
	"strings"

	"kogen-go/internal/findings"
)

// ParseFindings recognizes Minitest failures and RuboCop-style Ruby lint
// diagnostics. It is retained as the default RuboCop-compatible entry point.
func ParseFindings(output []byte, workdir string) []findings.Finding {
	return ParseFindingsForTool(output, "rubocop", workdir)
}

// ParseFindingsForTool recognizes adapter-specific lines and gives lint rules
// a stable tool/rule identity. tool should be standard or rubocop according to
// the selected formatter; the Minitest parser is used for either.
func ParseFindingsForTool(output []byte, tool, workdir string) []findings.Finding {
	text := strings.ToValidUTF8(string(output), "�")
	lines := strings.Split(text, "\n")
	result := parseMinitest(lines, workdir)
	for _, line := range lines {
		if finding, ok := parseRubyLint(line, tool, workdir); ok {
			result = append(result, finding)
		}
	}
	return result
}

func parseMinitest(lines []string, workdir string) []findings.Finding {
	result := make([]findings.Finding, 0)
	header := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, ") Failure:") || strings.Contains(trimmed, ") Error:") {
			header = trimmed
			continue
		}
		if header == "" {
			continue
		}
		if symbol, locationPath, lineNumber, ok := minitestLocation(trimmed); ok {
			lineValue := lineNumber
			result = append(result, findings.Finding{
				Path: normalizePath(locationPath, workdir), Rule: "minitest/failure",
				Symbol: symbol, Message: header, Severity: "error", Line: &lineValue,
			})
			header = ""
		} else if strings.HasPrefix(trimmed, "Finished in ") {
			header = ""
		}
	}
	return result
}

func minitestLocation(line string) (symbol, file string, lineNumber uint32, ok bool) {
	marker := strings.Index(line, " [")
	if marker < 0 || !strings.HasSuffix(line, "]:") {
		return "", "", 0, false
	}
	symbol = line[:marker]
	if !strings.Contains(symbol, "#test_") {
		return "", "", 0, false
	}
	location := line[marker+2 : len(line)-2]
	colon := strings.LastIndexByte(location, ':')
	if colon <= 0 {
		return "", "", 0, false
	}
	parsed, err := strconv.ParseUint(location[colon+1:], 10, 32)
	if err != nil {
		return "", "", 0, false
	}
	return symbol, location[:colon], uint32(parsed), true
}

func parseRubyLint(line, tool, workdir string) (findings.Finding, bool) {
	line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
	if line == "" {
		return findings.Finding{}, false
	}
	parts := strings.SplitN(line, ":", 4)
	if len(parts) < 3 {
		return findings.Finding{}, false
	}
	lineNumber, err := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 32)
	if err != nil {
		return findings.Finding{}, false
	}
	var column *uint32
	var detail string
	columnNumber, columnErr := strconv.ParseUint(strings.TrimSpace(parts[2]), 10, 32)
	if columnErr == nil {
		value := uint32(columnNumber)
		column = &value
		if len(parts) < 4 {
			return findings.Finding{}, false
		}
		detail = strings.TrimSpace(parts[3])
	} else {
		detail = strings.TrimSpace(parts[2])
		if len(parts) == 4 {
			detail += ":" + parts[3]
		}
	}
	severity := "warning"
	if severityText, rest, ok := strings.Cut(detail, ":"); ok {
		switch strings.TrimSpace(severityText) {
		case "C", "W":
			detail = strings.TrimSpace(rest)
		case "E", "F":
			severity = "error"
			detail = strings.TrimSpace(rest)
		}
	}
	detail = strings.TrimPrefix(detail, "[Correctable] ")
	detail = strings.TrimPrefix(detail, "[Corrected] ")
	rule, message, ok := strings.Cut(detail, ": ")
	if !ok || rule == "" || strings.ContainsAny(rule, " \t") {
		return findings.Finding{}, false
	}
	if tool == "" {
		tool = "rubocop"
	}
	lineValue := uint32(lineNumber)
	return findings.Finding{
		Path: normalizePath(parts[0], workdir), Rule: tool + "/" + rule,
		Symbol: "", Message: message, Severity: severity,
		Line: &lineValue, Column: column,
	}, true
}

func normalizePath(value, workdir string) string {
	if relative, ok := strings.CutPrefix(value, "$WORKDIR/"); ok {
		return filepath.ToSlash(relative)
	}
	pathValue := filepath.Clean(value)
	if workdir != "" {
		if relative, err := filepath.Rel(workdir, pathValue); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(relative)
		}
	}
	if filepath.IsAbs(pathValue) {
		parts := strings.Split(filepath.ToSlash(pathValue), "/")
		for index, component := range parts {
			switch component {
			case "test", "app", "config", "lib":
				return strings.Join(parts[index:], "/")
			}
		}
	}
	return filepath.ToSlash(pathValue)
}
