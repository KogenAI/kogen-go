package render

import "kogen-go/internal/cli/data"

// HelpPage is a fixed public help page. There is intentionally no command-topic
// page for "help"; kogen help prints HelpTop.
type HelpPage string

const (
	HelpTop            HelpPage = "top"
	HelpStatus         HelpPage = "status"
	HelpIntent         HelpPage = "intent"
	HelpIntentShape    HelpPage = "intent-shape"
	HelpIntentApprove  HelpPage = "intent-approve"
	HelpIntentRemove   HelpPage = "intent-remove"
	HelpQueue          HelpPage = "queue"
	HelpQueueStart     HelpPage = "queue-start"
	HelpQueueStop      HelpPage = "queue-stop"
	HelpProvider       HelpPage = "provider"
	HelpProviderList   HelpPage = "provider-list"
	HelpProviderLogin  HelpPage = "provider-login"
	HelpProviderLogout HelpPage = "provider-logout"
	HelpProviderUse    HelpPage = "provider-use"
	HelpVersion        HelpPage = "version"
)

var helpFiles = map[HelpPage]string{
	HelpTop:            "kogen.txt",
	HelpStatus:         "kogen-status.txt",
	HelpIntent:         "kogen-intent.txt",
	HelpIntentShape:    "kogen-intent-shape.txt",
	HelpIntentApprove:  "kogen-intent-approve.txt",
	HelpIntentRemove:   "kogen-intent-remove.txt",
	HelpQueue:          "kogen-queue.txt",
	HelpQueueStart:     "kogen-queue-start.txt",
	HelpQueueStop:      "kogen-queue-stop.txt",
	HelpProvider:       "kogen-provider.txt",
	HelpProviderList:   "kogen-provider-list.txt",
	HelpProviderLogin:  "kogen-provider-login.txt",
	HelpProviderLogout: "kogen-provider-logout.txt",
	HelpProviderUse:    "kogen-provider-use.txt",
	HelpVersion:        "kogen-version.txt",
}

// The primary embedded help tree retains older page bytes. These target-draft
// pages replace the changed entries; the remaining primary pages are byte-
// identical to the target corpus and are read from internal/cli/data.
var currentHelpPages = map[HelpPage]string{
	HelpTop: `Commands:
  status     Show the queue, Builds and Intents
  intent     Shape, approve or remove an Intent
  queue      Build approved Intents one at a time
  provider   Manage Kogen's provider logins
  version    Show the Kogen version
  help       List commands

Intent shaping defaults to gpt-6.1-sol at high effort; an explicit build.roles.shaper
in project or machine config overrides this default.

Run kogen <command> to see its subcommands and options.
`,
	HelpStatus: `Usage: kogen status [<slug>] [options]

Shows the queue, then Intents by state. With <slug>, shows that Intent and its latest Build.
Builds whose process died are marked crashed first. Agent roles, activity and elapsed
time are shown for this project (with <slug>: its latest Build).

Options:
  --watch               Print again on every change; return when the queue is idle
  --json                JSON Lines for Intents and agents (with <slug>: the Build report)
  --project <checkout>  Project checkout (default: current directory)
  --origin <repo>       Git repository holding Intent state and the target branch
  --base <branch>       Target branch (project setting, origin HEAD, then current branch)
`,
	HelpIntent: `Usage: kogen intent <command> <slug> [arguments] [options]

Commands:
  shape <slug> <file|->     Shape an Intent from a request file (- reads stdin)
  approve <slug> [<hash>]   Show the review card, or approve and queue the Intent
  remove <slug>            Remove an Intent and its approval in one commit
`,
	HelpIntentShape: `Usage: kogen intent shape <slug> <file|-> [options]

Shapes .kogen/intents/<slug>/intent.md and its acceptance test from the request in <file>
(- reads stdin). Waits until the shaper finishes, with no time limit. Never approves.
Shaping defaults to gpt-6.1-sol at high effort. An explicit build.roles.shaper in
project or machine config overrides this default.

Options:
  --project <checkout>  Project checkout (default: current directory)
  --origin <repo>       Git repository holding Intent state and the target branch
  --base <branch>       Target branch (project setting, origin HEAD, then current branch)
`,
	HelpProvider: `Usage: kogen provider <command> [options]

Commands:
  list               List saved accounts and the default
  login chatgpt      Sign in with a ChatGPT account
  login grok         Sign in with a Grok subscription
  logout chatgpt     Sign out of a ChatGPT account
  logout grok        Sign out of a Grok subscription
  use chatgpt        Choose the default account, or one project's account
  use grok           Choose the default account, or one project's account

Logins belong to this machine, never to a repo.
`,
	HelpProviderLogin: `Usage: kogen provider login <provider>

Supported providers: chatgpt, grok.
`,
	HelpProviderLogout: `Usage: kogen provider logout <provider>

Supported providers: chatgpt, grok.
`,
	HelpProviderUse: `Usage: kogen provider use <provider> --as <label> [--project <checkout>]

Supported providers: chatgpt, grok.
Without --project: makes <label> the provider and account default on this machine.
With --project: that project uses this provider and account; other projects keep their choice.
Choices live in ~/.kogen/accounts.yaml on this machine, never in a repo.

Options:
  --as <label>          Account label (required)
  --project <checkout>  The project that uses this account
`,
	HelpQueue: `Usage: kogen queue <command> [options]

Commands:
  start     Build approved Intents one at a time, oldest approval first
  stop      Stop the running queue after its current Build
`,
}

// Contents returns the exact help page bytes as a string, including the final
// newline. Unknown pages return an empty string.
func (page HelpPage) Contents() string {
	if contents, ok := currentHelpPages[page]; ok {
		return contents
	}
	name, ok := helpFiles[page]
	if !ok {
		return ""
	}
	contents, err := data.ReadFile("help/" + name)
	if err != nil {
		panic("read embedded help page " + name + ": " + err.Error())
	}
	return string(contents)
}
