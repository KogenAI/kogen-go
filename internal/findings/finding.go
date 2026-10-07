// Package findings parses check output and renders findings for approval cards
// and Build feedback.
package findings

import (
	"bytes"
	"regexp"
	"strings"

	"kogen-go/internal/contract"
)

// Finding retains presentation fields while exposing a stable identity for
// baseline comparison. Position and message do not participate in Identity.
type Finding struct {
	Path     string  `json:"path"`
	Rule     string  `json:"rule"`
	Symbol   string  `json:"symbol"`
	Message  string  `json:"message"`
	Severity string  `json:"severity"`
	Line     *uint32 `json:"line,omitempty"`
	Column   *uint32 `json:"column,omitempty"`
}

var gnuSeverity = regexp.MustCompile(`:[ \t]*(error|warning|note): \[([^\]]+)\] (.*)$`)

// ParseGNU extracts the GNU-format lines from check output. Invalid UTF-8 is
// replaced, and unrecognized lines are ignored. Test symbols are parsed only
// for test-failure tools; other tools retain everything after the rule as the
// message.
func ParseGNU(output []byte) []Finding {
	text := string(bytes.ToValidUTF8(output, []byte("�")))
	lines := strings.Split(text, "\n")
	findings := make([]Finding, 0)
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if finding, ok := parseGNULine(line); ok {
			findings = append(findings, finding)
		}
	}
	return findings
}

func parseGNULine(line string) (Finding, bool) {
	parts := gnuSeverity.FindStringSubmatchIndex(line)
	if parts == nil {
		return Finding{}, false
	}
	path, lineNo, column, ok := parseLocation(line[:parts[0]])
	if !ok {
		return Finding{}, false
	}
	finding := Finding{
		Path:     path,
		Rule:     line[parts[4]:parts[5]],
		Message:  line[parts[6]:parts[7]],
		Severity: line[parts[2]:parts[3]],
		Line:     &lineNo,
		Column:   column,
	}
	if isTestRule(finding.Rule) {
		if symbol, message, found := strings.Cut(finding.Message, ": "); found {
			finding.Symbol = symbol
			finding.Message = message
		}
	}
	return finding, true
}

func parseLocation(location string) (string, uint32, *uint32, bool) {
	last := strings.LastIndexByte(location, ':')
	if last <= 0 || last == len(location)-1 {
		return "", 0, nil, false
	}
	lastPosition, ok := parsePosition(location[last+1:])
	if !ok {
		return "", 0, nil, false
	}
	previous := strings.LastIndexByte(location[:last], ':')
	if previous > 0 {
		if lineNo, ok := parsePosition(location[previous+1 : last]); ok {
			column := lastPosition
			return location[:previous], lineNo, &column, true
		}
	}
	return location[:last], lastPosition, nil, true
}

func parsePosition(value string) (uint32, bool) {
	var n uint64
	if value == "" {
		return 0, false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + uint64(r-'0')
		if n > uint64(^uint32(0)) {
			return 0, false
		}
	}
	return uint32(n), true
}

func isTestRule(rule string) bool {
	tool, _, _ := strings.Cut(rule, "/")
	switch tool {
	case "test", "kt", "exunit", "minitest", "rails", "acceptance":
		return true
	default:
		return false
	}
}

// Identity returns the stable GNU finding identity. Line, column, severity and
// message intentionally do not affect equality.
func (f Finding) Identity() contract.FindingIdentity {
	return contract.FindingIdentity{Path: f.Path, Rule: f.Rule, Symbol: f.Symbol}
}

// AcceptanceIdentity builds the identity for one failed approved item. The
// item ID is carried in Symbol so separate acceptance items in one file remain
// distinct.
func AcceptanceIdentity(candidatePath, itemID string) contract.FindingIdentity {
	return contract.FindingIdentity{Path: candidatePath, Rule: "acceptance", Symbol: itemID}
}

// Identities returns the stable identities in input order.
func Identities(findings []Finding) []contract.FindingIdentity {
	identities := make([]contract.FindingIdentity, len(findings))
	for i, finding := range findings {
		identities[i] = finding.Identity()
	}
	return identities
}
