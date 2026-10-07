package prepare

import (
	"fmt"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/intent"
)

// RenderCard renders the human decision card from immutable source and
// baseline observations. It never embeds the Request section.
func RenderCard(parsed *intent.Intent, base string, baseCommit, approvalHash, approver string, warnings []Warning, baseline []BaselineRow) string {
	if parsed == nil || len(approvalHash) != 64 {
		return ""
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Intent: %s — %s\nSHA-256: %s\nApprover: %s\nBase: %s at %s\n\nBrief\n", parsed.Slug, parsed.Frontmatter.Title, approvalHash, approver, base, baseCommit)
	brief := parsed.BriefLines
	start, end := 0, len(brief)
	for start < end && strings.TrimSpace(brief[start].Text) == "" {
		start++
	}
	for end > start && strings.TrimSpace(brief[end-1].Text) == "" {
		end--
	}
	for _, line := range brief[start:end] {
		text := strings.TrimRight(line.Text, " \t")
		if strings.TrimSpace(text) == "" {
			out.WriteByte('\n')
		} else {
			out.WriteString("  ")
			out.WriteString(text)
			out.WriteByte('\n')
		}
	}
	out.WriteString("\nAcceptance\n")
	for _, item := range parsed.Acceptance {
		kind := "test"
		if verify := parsed.VerifyFor(item.ID); verify != nil && verify.IsKeep() {
			kind = "test keep"
		}
		fmt.Fprintf(&out, "  - [%s] %s (%s)\n", item.ID, item.Text, kind)
	}
	warningText := renderWarnings(warnings, baseline)
	if warningText != "" {
		out.WriteByte('\n')
		out.WriteString(warningText)
		out.WriteByte('\n')
	} else {
		out.WriteByte('\n')
	}
	fmt.Fprintf(&out, "Approve with:\n  kogen intent approve %s %s\n", parsed.Slug, approvalHash[:8])
	return out.String()
}

func renderWarnings(warnings []Warning, baseline []BaselineRow) string {
	var out strings.Builder
	other := make([]BaselineRow, 0)
	red := false
	for _, row := range baseline {
		switch row.Status {
		case contract.CheckRed:
			red = true
		case contract.CheckUnavailable, contract.CheckTimeout, contract.CheckMutating:
			other = append(other, row)
		}
	}
	if len(warnings) != 0 || len(other) != 0 {
		out.WriteString("Warnings\n")
		for _, warning := range warnings {
			ids := "-"
			if len(warning.ItemIDs) != 0 {
				ids = strings.Join(warning.ItemIDs, ", ")
			}
			fmt.Fprintf(&out, "  - %s: %s — %s\n", warning.Code, ids, warning.Message)
		}
		for _, row := range other {
			fmt.Fprintf(&out, "  - baseline_%s: %s — configured check did not pass on the base\n", row.Status, row.Name)
		}
	}
	if red {
		if out.Len() != 0 {
			out.WriteByte('\n')
		}
		out.WriteString("Warning: configured checks are already red on the base:\n")
		for _, row := range baseline {
			if row.Status != contract.CheckRed {
				continue
			}
			limit := len(row.Findings)
			if limit > 5 {
				limit = 5
			}
			for _, finding := range row.Findings[:limit] {
				line := uint32(0)
				if finding.Line != nil {
					line = *finding.Line
				}
				detail := fmt.Sprintf("%s:%d: %s", finding.Path, line, finding.Message)
				if finding.Symbol != "" {
					detail = fmt.Sprintf("%s:%d: %s: %s", finding.Path, line, finding.Symbol, finding.Message)
				}
				fmt.Fprintf(&out, "  - %s: [%s] %s\n", row.Name, finding.Rule, detail)
			}
		}
		out.WriteString("Hint: fix the base first, or scope the check, e.g. a changed-files format argv.\n")
	}
	return strings.TrimSuffix(out.String(), "\n")
}
