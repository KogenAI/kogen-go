// Package prompts contains the stable, generic prompt and exact message
// templates used while shaping an Intent.
package prompts

import (
	_ "embed"
	"sort"
	"strings"
)

// ShaperRoleMarker is also the first sentence of the shaper's system prompt.
// Keep it stable: it identifies the shaper role in the provider conversation.
const ShaperRoleMarker = "You are Kogen Intent shaper."

// The generic system instructions are an immutable, cacheable prefix. Run-
// specific values belong in the first user message built by InitialMessage.
//
//go:embed shaper-system.md
var shaperSystemPrompt string

// SystemPrompt returns the generic shaper instructions. The returned string is
// immutable; callers append run data in a user message instead.
func SystemPrompt() string { return shaperSystemPrompt }

// RequiredPath records whether a required output path can be read for repair
// framing. A false Readable value includes both missing and unreadable files.
type RequiredPath struct {
	Path     string
	Readable bool
}

// InitialMessage returns the exact first user message for one Shape run.
// Domains and gate paths are byte-sorted without changing the caller's slices.
// Request bytes are inserted verbatim, including CRLFs and final newlines.
func InitialMessage(slug string, domains, gatePaths []string, request []byte, intentPath, acceptancePath string) string {
	domains = append([]string(nil), domains...)
	gatePaths = append([]string(nil), gatePaths...)
	sort.Strings(domains)
	sort.Strings(gatePaths)
	quotedGates := make([]string, len(gatePaths))
	for i, path := range gatePaths {
		quotedGates[i] = "`" + path + "`"
	}

	var message strings.Builder
	message.WriteString("Slug: ")
	message.WriteString(slug)
	message.WriteString("\n\nConfigured project domains: ")
	message.WriteString(strings.Join(domains, ", "))
	message.WriteString(". Use only these names in the Intent and Verify lines.\n\nEffective gate paths: ")
	message.WriteString(strings.Join(quotedGates, ", "))
	message.WriteString(". Set `changes_gate: true` only when the task or planned changes require modifying one of these paths. Omit it for unrelated changes; running or inspecting checks alone does not count.\n\nTask statement:\n")
	message.Write(request)
	message.WriteString("\n\nWrite the Intent to `")
	message.WriteString(intentPath)
	message.WriteString("` and its acceptance test to `")
	message.WriteString(acceptancePath)
	message.WriteString("`.")
	return message.String()
}

// ValidationFeedback formats the candidate failure appended to a repair
// request. Detail is kept byte-for-byte as provided by the validator.
func ValidationFeedback(reason, detail string) string {
	return "candidate/" + reason + ": " + detail
}

// RepairMessage returns the exact validation repair template. The validator's
// feedback is appended unchanged after the template's final blank line.
func RepairMessage(intentPath, acceptancePath RequiredPath, feedback string) string {
	return "Validation failed. Repair the generated files in this conversation. The required paths and their current state are:\n" +
		pathState(intentPath) + "\n" + pathState(acceptancePath) +
		"\nBoth exact paths must exist after this pass. Every missing path must be written now. Do not delete required files. The available tools can read, search, and write files; they cannot remove them. Preserve present content unless the failure below requires a focused correction.\n\nExact failure output:\n\n" + feedback
}

// FinishGuardMessage asks for another turn when a pass ends before both
// required files exist. It does not imply a validation repair.
func FinishGuardMessage(missingPaths []string) string {
	return "Both files must exist before you finish. Missing: " + strings.Join(missingPaths, ", ") + "."
}

// FallbackMessage starts a fresh conversation from the exact initial message
// and adds the last failure without changing either value.
func FallbackMessage(initial, feedback string) string {
	return initial + "\n\nLast validation failure:\n\n" + feedback
}

func pathState(path RequiredPath) string {
	status := "missing or unreadable. Write it during this repair pass at this exact path."
	if path.Readable {
		status = "present on disk. Keep it in place; change it only if the failure below requires a correction."
	}
	return "- `" + path.Path + "`: " + status
}
