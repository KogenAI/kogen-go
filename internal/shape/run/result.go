package run

import (
	"errors"
	"fmt"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/shape/validate"
)

// Result is the completed public Shape result. It contains output paths and
// call summaries only; it carries no approval or commit action.
type Result struct {
	IntentPath     string
	AcceptancePath string
	Rounds         uint64
	Warnings       []validate.Warning
	Calls          []ModelCall
	TranscriptPath string
}

// Text renders the unchanged `intent shape` success output. The accounting
// receipt remains in the private scratch directory and adds no output line.
func (r Result) Text(slug string) string {
	var output strings.Builder
	fmt.Fprintf(&output, "Intent: %s\n", r.IntentPath)
	fmt.Fprintf(&output, "Acceptance test: %s\n", r.AcceptancePath)
	fmt.Fprintf(&output, "Validated after %d round(s).\n", r.Rounds)
	if len(r.Warnings) != 0 {
		output.WriteString("Warnings\n")
		for _, warning := range r.Warnings {
			ids := "-"
			if len(warning.ItemIDs) != 0 {
				ids = strings.Join(warning.ItemIDs, ", ")
			}
			fmt.Fprintf(&output, "  - %s: %s — %s\n", warning.Code, ids, warning.Message)
		}
	}
	for _, call := range r.Calls {
		fmt.Fprintf(&output, "shape %s/%s input=%d cached=%d output=%d reasoning=%d wall_ms=%d\n",
			call.Settings.Model, call.Settings.Effort,
			tokenValue(call.Usage, func(usage *contract.TokenUsage) *int64 { return usage.Input }),
			tokenValue(call.Usage, func(usage *contract.TokenUsage) *int64 { return usage.CachedInput }),
			tokenValue(call.Usage, func(usage *contract.TokenUsage) *int64 { return usage.Output }),
			tokenValue(call.Usage, func(usage *contract.TokenUsage) *int64 { return usage.Reasoning }), call.WallMS)
	}
	fmt.Fprintf(&output, "Transcript: %s\n", r.TranscriptPath)
	fmt.Fprintf(&output, "Next: kogen intent approve %s\n", slug)
	return output.String()
}

// FormatError renders a public Shape error line and indents any following
// diagnostic lines according to the command contract.
func FormatError(err error) string {
	if err == nil {
		return ""
	}
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure == nil {
		return "controller/internal_error: " + oneLine(err.Error())
	}
	prefix := string(failure.Class) + "/" + string(failure.Reason)
	message := failure.Message
	if message == "" {
		message = err.Error()
	}
	if strings.HasPrefix(message, prefix) {
		message = strings.TrimPrefix(message, prefix)
		message = strings.TrimPrefix(message, ": ")
	}
	lines := strings.Split(strings.ReplaceAll(message, "\r\n", "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return prefix
	}
	var output strings.Builder
	output.WriteString(prefix)
	if lines[0] != "" {
		output.WriteString(": ")
		output.WriteString(lines[0])
	}
	for _, line := range lines[1:] {
		output.WriteString("\n  ")
		output.WriteString(line)
	}
	if failure.Class == "provider" {
		if failure.Reason == "login" || failure.Reason == "usage_limit" {
			output.WriteString("\nshape/provider_failed: shaping stopped on a provider error that retrying cannot fix; no Intent was written. Fix the account, then run kogen intent shape again.")
		} else {
			output.WriteString("\nshape/provider_failed: shaping stopped on a provider error after its retries; no Intent was written. Run kogen intent shape again, or write the Intent yourself.")
		}
	}
	return output.String()
}

func tokenValue(usage *contract.TokenUsage, selectValue func(*contract.TokenUsage) *int64) int64 {
	if usage == nil {
		return 0
	}
	value := selectValue(usage)
	if value == nil {
		return 0
	}
	return *value
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
