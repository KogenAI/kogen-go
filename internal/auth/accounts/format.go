package accounts

import (
	"fmt"
	"sort"
	"strings"

	"kogen-go/internal/auth/vault"
)

// FormatList renders saved profile rows in the CLI's fixed provider/label
// order. Project-specific selections are resolved during a run and therefore
// do not change the machine-default marker shown here.
func FormatList(file File, profiles vault.Profiles) string {
	chatgptLabels := sortedChatGPTLabels(profiles.ChatGPT)
	grokLabels := sortedGrokLabels(profiles.Grok)
	if len(chatgptLabels)+len(grokLabels) == 0 {
		return "chatgpt: not signed in\ngrok: not signed in\n"
	}

	selectedProvider := file.Selection.Default
	if selectedProvider == "" {
		selectedProvider = ChatGPT
	}
	var out strings.Builder
	for _, label := range chatgptLabels {
		profile := profiles.ChatGPT[label]
		writeProfileLine(&out, ChatGPT, label, file.ChatGPT.Default == label && selectedProvider == ChatGPT, profile.SignedIn, profile.Email, profile.ExpiresAt)
	}
	for _, label := range grokLabels {
		profile := profiles.Grok[label]
		writeProfileLine(&out, Grok, label, file.Grok.Default == label && selectedProvider == Grok, profile.SignedIn, profile.Email, profile.ExpiresAt)
	}
	return out.String()
}

// ChatGPTPlanNotice is emitted on stderr after the first successful ChatGPT
// login, before the final signed-in result line.
func ChatGPTPlanNotice() string { return "You're using your ChatGPT plan\n" }

// FormatChatGPTSignedIn returns the stable final ChatGPT login line.
func FormatChatGPTSignedIn(email *string) string {
	return formatSignedIn(ChatGPT, email)
}

// FormatGrokSignedIn returns the stable final Grok login line.
func FormatGrokSignedIn(email *string) string {
	return formatSignedIn(Grok, email)
}

// FormatChatGPTSignedOut reports whether remote token revocation was confirmed.
func FormatChatGPTSignedOut(remoteRevoked bool) string {
	if remoteRevoked {
		return "chatgpt:default signed out\n"
	}
	return "chatgpt:default signed out locally; remote revocation was not confirmed. You can disconnect Kogen in ChatGPT Settings if needed.\n"
}

// FormatGrokSignedOut reports local logout; Grok tokens are not remotely
// revoked by Kogen.
func FormatGrokSignedOut() string { return "grok:default signed out locally\n" }

// FormatUse renders the confirmation for a machine-default or project choice.
func FormatUse(provider Provider, label, canonicalProject string) string {
	if canonicalProject == "" {
		return fmt.Sprintf("%s:%s is the default account\n", provider, label)
	}
	return fmt.Sprintf("%s:%s is the account for %s\n", provider, label, canonicalProject)
}

// DeprecatedAccountWarning is the v1.3 stderr warning for committed project
// account fields. It is emitted by Shape and Build routes only.
func DeprecatedAccountWarning() string {
	return "kogen: moved: account in .kogen/project.yaml; use kogen provider use chatgpt --as <label> --project <checkout>\n"
}

func formatSignedIn(provider Provider, email *string) string {
	line := fmt.Sprintf("%s:default signed in", provider)
	if email != nil && *email != "" {
		line += " (" + singleLine(*email) + ")"
	}
	return line + "\n"
}

func writeProfileLine(out *strings.Builder, provider Provider, label string, selectedDefault, signedIn bool, email *string, expiresAt int64) {
	out.WriteString(string(provider))
	out.WriteByte(':')
	out.WriteString(label)
	if selectedDefault {
		out.WriteString(" (default)")
	}
	if signedIn {
		out.WriteString(" signed in")
	} else {
		out.WriteString(" signed out")
	}
	if email != nil && *email != "" {
		out.WriteByte(' ')
		out.WriteString(singleLine(*email))
	}
	if expiresAt != 0 {
		out.WriteString(" expires=")
		out.WriteString(fmt.Sprintf("%d", expiresAt))
	}
	out.WriteByte('\n')
}

func sortLabels(labels []string) { sort.Strings(labels) }

func singleLine(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

func sortedChatGPTLabels(profiles map[string]vault.ChatGPTProfile) []string {
	labels := make([]string, 0, len(profiles))
	for label := range profiles {
		if ValidLabel(label) {
			labels = append(labels, label)
		}
	}
	sortLabels(labels)
	return labels
}

func sortedGrokLabels(profiles map[string]vault.GrokProfile) []string {
	labels := make([]string, 0, len(profiles))
	for label := range profiles {
		if ValidLabel(label) {
			labels = append(labels, label)
		}
	}
	sortLabels(labels)
	return labels
}
