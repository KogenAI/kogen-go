package findings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"kogen-go/internal/contract"
)

const (
	maxFeedbackFindingsPerTool = 10
	maxFeedbackFindingsTotal   = 20
	maxFeedbackMessageRunes    = 200
	maxRawTailLines            = 8
	maxRawTailRunes            = 600
)

// FeedbackCheck contains one verification result and its complete raw log.
// The caller supplies Excused from the gate comparison; this package formats
// that decision but does not decide whether the candidate may land.
type FeedbackCheck struct {
	Name         string
	Program      string
	Status       contract.CheckStatus
	ExitStatus   *int
	Timeout      time.Duration
	Findings     []Finding
	ChangedPaths []string
	LogPath      string
	RawLog       []byte
	Excused      bool
}

// FeedbackFix contains the observed result and log for one configured fix.
type FeedbackFix struct {
	Name        string
	ExitStatus  *int
	TimedOut    bool
	Unavailable bool
	LogPath     string
	RawLog      []byte
}

// FeedbackAcceptance is the observed result for one approved acceptance item.
type FeedbackAcceptance struct {
	ID     string
	Status string
	Passed bool
}

// FeedbackInput contains only observations needed to render the gate message
// and the complete machine-readable finding artifact.
type FeedbackInput struct {
	RunID      string
	Checks     []FeedbackCheck
	Baseline   []BaselineRow
	Fixes      []FeedbackFix
	Acceptance []FeedbackAcceptance
	RunDir     string
	Home       string
}

// GateFeedback is the bounded builder-facing message plus a full findings
// artifact ready for publication under the run directory.
type GateFeedback struct {
	Text         string
	ArtifactPath string
	Artifact     []byte
}

type gateFindingRecord struct {
	Check string `json:"check"`
	Finding
}

// RenderGateFeedback renders the bounded §3.7.3 message and constructs the
// complete JSON finding artifact. The artifact includes all parsed findings,
// including findings omitted from the bounded message.
func RenderGateFeedback(input FeedbackInput) (GateFeedback, error) {
	if !validRunID(input.RunID) {
		return GateFeedback{}, fmt.Errorf("invalid gate finding artifact ID %q", input.RunID)
	}
	artifact := make([]gateFindingRecord, 0)
	for _, check := range input.Checks {
		for _, finding := range check.Findings {
			artifact = append(artifact, gateFindingRecord{Check: check.Name, Finding: finding})
		}
	}
	artifactBytes, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return GateFeedback{}, fmt.Errorf("encode gate findings: %w", err)
	}
	artifactBytes = append(artifactBytes, '\n')

	lines := make([]string, 0)
	for _, fix := range input.Fixes {
		if fix.Passed() {
			continue
		}
		lines = append(lines, fmt.Sprintf("fix/%s: %s", fix.Name, fixFailureStatus(fix)))
		lines = append(lines, "raw log: "+fix.LogPath)
		appendRawTail(&lines, fix.Name, fix.RawLog, input.RunDir, input.Home)
	}

	warnings := 0
	for _, check := range input.Checks {
		if check.Excused {
			warnings++
		}
	}
	baseByName := make(map[string]BaselineRow, len(input.Baseline))
	for _, row := range input.Baseline {
		if _, exists := baseByName[row.Name]; !exists {
			baseByName[row.Name] = row
		}
	}

	errorsCount := 0
	totalFindings := 0
	toolCounts := make(map[string]int)
	omittedFindings := make(map[string]int)
	for _, check := range input.Checks {
		if check.Status == contract.CheckGreen || check.Excused {
			continue
		}
		errorsCount++
		appendCheckDetails(
			&lines,
			check,
			baseByName,
			&totalFindings,
			toolCounts,
			omittedFindings,
		)
		lines = append(lines, "raw log: "+check.LogPath)
		appendRawTail(&lines, check.Name, check.RawLog, input.RunDir, input.Home)
	}

	for _, tool := range sortedCounts(omittedFindings) {
		lines = append(lines, fmt.Sprintf("… %d more %s findings", omittedFindings[tool], tool))
	}

	failedAcceptance := 0
	for _, item := range input.Acceptance {
		if item.Passed {
			continue
		}
		failedAcceptance++
		status := item.Status
		if status == "" {
			status = "failed"
		}
		lines = append(lines, fmt.Sprintf("acceptance %s: %s", item.ID, status))
	}
	if warnings > 0 {
		for _, check := range input.Checks {
			if check.Excused {
				lines = append(lines, fmt.Sprintf("Base-red warning: check %q still has only findings recorded at approval.", check.Name))
			}
		}
	}

	checkStatuses := make([]string, 0, len(input.Checks))
	for _, check := range input.Checks {
		checkStatuses = append(checkStatuses, fmt.Sprintf("%s=%s", check.Name, check.Status))
	}
	counts := make([]string, 0, len(toolCounts))
	for _, tool := range sortedCounts(toolCounts) {
		counts = append(counts, fmt.Sprintf("%s %d", tool, toolCounts[tool]))
	}
	passed := len(input.Acceptance) - failedAcceptance
	lines = append(lines, fmt.Sprintf(
		"gate: %d errors, %d warnings (%s); checks %s; acceptance %d/%d",
		errorsCount,
		warnings,
		strings.Join(counts, ", "),
		strings.Join(checkStatuses, ", "),
		passed,
		len(input.Acceptance),
	))

	return GateFeedback{
		Text:         strings.Join(lines, "\n"),
		ArtifactPath: "gate-findings-" + input.RunID + ".json",
		Artifact:     artifactBytes,
	}, nil
}

// Publish writes the full artifact through the shared rooted filesystem port.
// Create-only publication prevents a reused ID from replacing prior evidence.
func (feedback GateFeedback) Publish(root contract.RootedFS) error {
	if root == nil {
		return errors.New("publish gate findings: nil rooted filesystem")
	}
	if feedback.ArtifactPath == "" || !fs.ValidPath(feedback.ArtifactPath) {
		return errors.New("publish gate findings: invalid artifact path")
	}
	if err := root.Publish(feedback.ArtifactPath, feedback.Artifact, 0o600, contract.PublicationCreateOnly); err != nil {
		return fmt.Errorf("publish gate findings: %w", err)
	}
	return nil
}

func (fix FeedbackFix) Passed() bool {
	return !fix.TimedOut && !fix.Unavailable && fix.ExitStatus != nil && *fix.ExitStatus == 0
}

func appendCheckDetails(
	lines *[]string,
	check FeedbackCheck,
	baseByName map[string]BaselineRow,
	totalFindings *int,
	toolCounts map[string]int,
	omittedFindings map[string]int,
) {
	switch check.Status {
	case contract.CheckGreen:
		return
	case contract.CheckMutating:
		if len(check.ChangedPaths) == 0 {
			*lines = append(*lines, fmt.Sprintf("check %s: Mutating", check.Name))
		} else {
			*lines = append(*lines, fmt.Sprintf("check %s: Mutating; changed paths: %s", check.Name, strings.Join(check.ChangedPaths, ", ")))
		}
	case contract.CheckTimeout:
		*lines = append(*lines, fmt.Sprintf("check %s: timed out after %g s", check.Name, check.Timeout.Seconds()))
	case contract.CheckUnavailable:
		if base, found := baseByName[check.Name]; found && base.Status == contract.CheckGreen {
			*lines = append(*lines, fmt.Sprintf("%s is not available, but it ran on the base", check.Program))
		} else {
			*lines = append(*lines, fmt.Sprintf("check %s: Unavailable", check.Name))
		}
	case contract.CheckRed:
		*lines = append(*lines, fmt.Sprintf("check %s: Red", check.Name))
		for _, finding := range check.Findings {
			tool := findingTool(finding.Rule)
			if *totalFindings >= maxFeedbackFindingsTotal || toolCounts[tool] >= maxFeedbackFindingsPerTool {
				omittedFindings[tool]++
				continue
			}
			(*totalFindings)++
			toolCounts[tool]++
			lineNo, column := uint32(0), uint32(1)
			if finding.Line != nil {
				lineNo = *finding.Line
			}
			if finding.Column != nil {
				column = *finding.Column
			}
			message := truncateRunes(finding.Message, maxFeedbackMessageRunes)
			symbol := ""
			if finding.Symbol != "" {
				symbol = " " + finding.Symbol + ":"
			}
			*lines = append(*lines, fmt.Sprintf("%s:%d:%d: error: [%s]%s %s", finding.Path, lineNo, column, finding.Rule, symbol, message))
		}
	default:
		*lines = append(*lines, fmt.Sprintf("check %s: %s", check.Name, check.Status))
	}
}

func appendRawTail(lines *[]string, step string, rawLog []byte, runDir, home string) {
	text := string(bytes.ToValidUTF8(rawLog, []byte("�")))
	if text == "" {
		return
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	allLines := strings.Split(text, "\n")
	if strings.HasSuffix(text, "\n") {
		allLines = allLines[:len(allLines)-1]
	}
	if len(allLines) > maxRawTailLines {
		allLines = allLines[len(allLines)-maxRawTailLines:]
	}
	tail := strings.Join(allLines, "\n")
	if runDir != "" {
		tail = strings.ReplaceAll(tail, runDir, "$TMPDIR")
	}
	if home != "" {
		tail = strings.ReplaceAll(tail, home, "$HOME")
	}
	tail = truncateRunes(tail, maxRawTailRunes)
	if tail != "" {
		*lines = append(*lines, fmt.Sprintf("raw tail (first failed step %s):\n%s", step, tail))
	}
}

func findingTool(rule string) string {
	if tool, _, ok := strings.Cut(rule, "/"); ok {
		return tool
	}
	return rule
}

func sortedCounts(counts map[string]int) []string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func truncateRunes(value string, maximum int) string {
	if maximum <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= maximum {
		return value
	}
	runes := []rune(value)
	return string(runes[:maximum])
}

func validRunID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	for index, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		if index > 0 && (r == '-' || r == '_' || r == '.') {
			continue
		}
		return false
	}
	return true
}

func fixFailureStatus(fix FeedbackFix) string {
	if fix.TimedOut {
		return "timed out"
	}
	if fix.Unavailable {
		return "unavailable"
	}
	if fix.ExitStatus == nil {
		return "exit -1"
	}
	return fmt.Sprintf("exit %d", *fix.ExitStatus)
}
