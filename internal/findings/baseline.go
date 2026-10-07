package findings

import (
	"fmt"
	"strings"

	"kogen-go/internal/contract"
)

// BaselineRow is one approval-time check result used by card warnings and
// base-relative verification.
type BaselineRow struct {
	Name     string
	Status   contract.CheckStatus
	ExitCode *int
	Findings []Finding
}

// HasRedBaseline reports whether any configured check was red on the base.
func HasRedBaseline(rows []BaselineRow) bool {
	for _, row := range rows {
		if row.Status == contract.CheckRed {
			return true
		}
	}
	return false
}

// BaseRedWarningLines formats the finding rows shown on an approval card.
// Each red check contributes at most five findings, matching the card contract.
func BaseRedWarningLines(rows []BaselineRow) []string {
	lines := make([]string, 0)
	for _, row := range rows {
		if row.Status != contract.CheckRed {
			continue
		}
		limit := len(row.Findings)
		if limit > 5 {
			limit = 5
		}
		for _, finding := range row.Findings[:limit] {
			lineNo := uint32(0)
			if finding.Line != nil {
				lineNo = *finding.Line
			}
			detail := fmt.Sprintf("%s:%d: %s", finding.Path, lineNo, finding.Message)
			if finding.Symbol != "" {
				detail = fmt.Sprintf("%s:%d: %s: %s", finding.Path, lineNo, finding.Symbol, finding.Message)
			}
			lines = append(lines, fmt.Sprintf("  - %s: [%s] %s", row.Name, finding.Rule, detail))
		}
	}
	return lines
}

// BaselineStatusWarningLines formats non-red checks that were unavailable,
// timed out, or mutated the checked base.
func BaselineStatusWarningLines(rows []BaselineRow) []string {
	lines := make([]string, 0)
	for _, row := range rows {
		switch row.Status {
		case contract.CheckUnavailable, contract.CheckTimeout, contract.CheckMutating:
			lines = append(lines, fmt.Sprintf("  - baseline_%s: %s — configured check did not pass on the base", row.Status, row.Name))
		}
	}
	return lines
}

// BaseRedWarningBlock renders the fixed warning block on an approval card.
// An empty string means there are no red baseline checks.
func BaseRedWarningBlock(rows []BaselineRow) string {
	if !HasRedBaseline(rows) {
		return ""
	}
	lines := []string{"Warning: configured checks are already red on the base:"}
	lines = append(lines, BaseRedWarningLines(rows)...)
	lines = append(lines, "Hint: fix the base first, or scope the check, e.g. a changed-files format argv.")
	return joinLines(lines)
}

// IsExcused reports whether a current check result is excused by its approved
// baseline. If both results have finding identities, the current identities
// must be a subset of the baseline identities. If either side has none, equal
// exit statuses are required. Green baselines and status changes never excuse.
func IsExcused(
	baselineStatus, currentStatus contract.CheckStatus,
	baselineExit, currentExit *int,
	baselineFindings, currentFindings []contract.FindingIdentity,
) bool {
	if baselineStatus == contract.CheckGreen || baselineStatus != currentStatus {
		return false
	}
	baseIDs := identitySet(baselineFindings)
	currentIDs := identitySet(currentFindings)
	if len(baseIDs) != 0 && len(currentIDs) != 0 {
		for identity := range currentIDs {
			if _, found := baseIDs[identity]; !found {
				return false
			}
		}
		return true
	}
	return sameExit(baselineExit, currentExit)
}

type identity struct{ path, rule, symbol string }

func identitySet(findings []contract.FindingIdentity) map[identity]struct{} {
	set := make(map[identity]struct{}, len(findings))
	for _, finding := range findings {
		set[identity{finding.Path, finding.Rule, finding.Symbol}] = struct{}{}
	}
	return set
}

func sameExit(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func joinLines(lines []string) string {
	return strings.Join(lines, "\n")
}
