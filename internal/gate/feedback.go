package gate

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/findings"
)

// Feedback renders the controller's bounded, deterministic repair message.
// It includes only blocking evidence and base-red warnings; auditor advice is
// deliberately kept separate.
func (r *GateReport) Feedback() string {
	if r == nil {
		return "gate: 1 errors, 0 warnings; checks ; acceptance 0/0"
	}
	var lines []string
	toolCounts := make(map[string]int)
	shownByTool := make(map[string]int)
	seenFindings := 0
	errorCount, warningCount := 0, 0
	firstFailedName := ""
	var firstFailedTail []byte
	var firstFailedLog string
	setFirst := func(name, log string, tail []byte) {
		if firstFailedName == "" {
			firstFailedName, firstFailedLog, firstFailedTail = name, log, tail
		}
	}

	for _, fix := range r.fixes {
		if fix.TimedOut || fix.Unavailable || fix.ExitStatus == nil || *fix.ExitStatus != 0 {
			errorCount++
			lines = append(lines, fmt.Sprintf("fix/%s: %s", fix.Spec.Name, fixStatus(fix)))
			setFirst(fix.Spec.Name, fix.LogPath, fix.OutputTail)
			if fix.LogPath != "" {
				lines = append(lines, "raw log: "+redactPath(fix.LogPath, r.homeDir, r.tempDir))
			}
		}
	}

	for _, check := range r.checks {
		if check.Excused {
			warningCount++
			lines = append(lines, fmt.Sprintf("Base-red warning: check %q still has only findings recorded at approval.", check.Spec.Name))
			continue
		}
		if check.Status == contract.CheckGreen {
			continue
		}
		blockingFindings := 0
		for _, finding := range check.Findings {
			if finding.Severity == "error" {
				blockingFindings++
			} else if finding.Severity == "warning" {
				warningCount++
			}
			tool := findingTool(finding.Rule)
			toolCounts[tool]++
		}
		if blockingFindings == 0 {
			errorCount++
		} else {
			errorCount += blockingFindings
		}
		setFirst(check.Spec.Name, check.LogPath, check.OutputTail)
		switch check.Status {
		case contract.CheckUnavailable:
			if check.BaseStatus == contract.CheckGreen {
				lines = append(lines, check.Spec.Program+" is not available, but it ran on the base")
			} else if blockingFindings == 0 {
				lines = append(lines, "check "+check.Spec.Name+": unavailable")
			}
		case contract.CheckTimeout:
			seconds := strconv.FormatFloat(check.Spec.Timeout.Seconds(), 'f', -1, 64)
			lines = append(lines, "timed out after "+seconds+" s")
		case contract.CheckMutating:
			if len(check.ChangedPaths) != 0 {
				lines = append(lines, "mutating: "+strings.Join(check.ChangedPaths, ", "))
			} else if blockingFindings == 0 {
				lines = append(lines, "check "+check.Spec.Name+": mutating")
			}
		case contract.CheckRed:
			if blockingFindings == 0 {
				lines = append(lines, "check "+check.Spec.Name+": red")
			}
		}
		for _, finding := range check.Findings {
			if seenFindings >= 20 {
				break
			}
			tool := findingTool(finding.Rule)
			if shownByTool[tool] >= 10 {
				continue
			}
			lines = append(lines, redactPath(formatFinding(finding), r.homeDir, r.tempDir))
			shownByTool[tool]++
			seenFindings++
		}
		if check.LogPath != "" {
			lines = append(lines, "raw log: "+redactPath(check.LogPath, r.homeDir, r.tempDir))
		}
	}

	for tool, total := range toolCounts {
		if shownByTool[tool] < total {
			lines = append(lines, fmt.Sprintf("… %d more %s findings", total-shownByTool[tool], tool))
		}
	}

	for _, item := range r.approvedItems {
		if r.effectivePass[item] {
			continue
		}
		errorCount++
		lines = append(lines, "acceptance "+item+": failed")
	}
	for _, failure := range r.initial.Failures {
		errorCount++
		lines = append(lines, "acceptance: "+string(failure.Kind))
	}
	if (len(r.initial.Failures) != 0 || len(failedItems(r.approvedItems, r.initial.ItemPass)) != 0) && r.initial.Process.LogPath != "" {
		log := r.initial.Process.LogPath
		setFirst("acceptance", log, r.initial.Process.OutputTail)
		lines = append(lines, "raw log: "+redactPath(log, r.homeDir, r.tempDir))
	}
	for _, finding := range r.protection {
		errorCount++
		lines = append(lines, "protected/"+finding.Path)
	}
	if r.flake != nil && r.flake.StoreError != "" {
		warningCount++
		lines = append(lines, "flake evidence could not be stored; base-red failures remain blocking")
	}

	if firstFailedName != "" {
		lines = append(lines, "raw tail (first failed step "+firstFailedName+"):")
		tail := formatTail(firstFailedTail, r.homeDir, r.tempDir)
		if tail != "" {
			lines = append(lines, tail)
		}
		if firstFailedLog != "" && !hasRawLog(lines, redactPath(firstFailedLog, r.homeDir, r.tempDir)) {
			lines = append(lines, "raw log: "+redactPath(firstFailedLog, r.homeDir, r.tempDir))
		}
	}

	checkStatuses := make([]string, len(r.checks))
	for i, check := range r.checks {
		checkStatuses[i] = check.Spec.Name + "=" + string(check.Status)
	}
	toolNames := make([]string, 0, len(toolCounts))
	for tool := range toolCounts {
		toolNames = append(toolNames, tool)
	}
	sort.Strings(toolNames)
	counts := make([]string, 0, len(toolNames))
	for _, tool := range toolNames {
		counts = append(counts, tool+"="+strconv.Itoa(toolCounts[tool]))
	}
	countText := ""
	if len(counts) != 0 {
		countText = " (" + strings.Join(counts, ", ") + ")"
	}
	lines = append(lines, fmt.Sprintf("gate: %d errors, %d warnings%s; checks %s; acceptance %d/%d", errorCount, warningCount, countText, strings.Join(checkStatuses, ", "), r.passedCount, len(r.approvedItems)))
	return strings.Join(lines, "\n")
}

func findingTool(rule string) string {
	tool, _, found := strings.Cut(rule, "/")
	if !found || tool == "" {
		return "check"
	}
	return tool
}

func formatFinding(finding findings.Finding) string {
	line, column := uint32(0), uint32(0)
	if finding.Line != nil {
		line = *finding.Line
	}
	if finding.Column != nil {
		column = *finding.Column
	}
	severity := finding.Severity
	if severity == "" {
		severity = "error"
	}
	message := clipRunes(finding.Message, 200)
	text := fmt.Sprintf("%s:%d:%d: %s: [%s]", finding.Path, line, column, severity, finding.Rule)
	if finding.Symbol != "" {
		text += " " + finding.Symbol + ":"
	}
	if message != "" {
		text += " " + message
	}
	return text
}

func fixStatus(fix FixObservation) string {
	switch {
	case fix.TimedOut:
		return "timeout"
	case fix.Unavailable:
		return "unavailable"
	case fix.ExitStatus == nil:
		return "unavailable"
	default:
		return fmt.Sprintf("exit %d", *fix.ExitStatus)
	}
}

func redactPath(value, homeDir, tempDir string) string {
	if tempDir != "" {
		value = strings.ReplaceAll(value, tempDir, "$TMPDIR")
	}
	if homeDir != "" {
		value = strings.ReplaceAll(value, homeDir, "$HOME")
	}
	return value
}

func formatTail(raw []byte, homeDir, tempDir string) string {
	text := strings.TrimSpace(redactPath(string(raw), homeDir, tempDir))
	if text == "" {
		return ""
	}
	rows := strings.Split(text, "\n")
	if len(rows) > 8 {
		rows = rows[len(rows)-8:]
	}
	text = strings.Join(rows, "\n")
	return clipRunes(text, 600)
}

func clipRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func hasRawLog(lines []string, value string) bool {
	for _, line := range lines {
		if line == "raw log: "+value {
			return true
		}
	}
	return false
}
