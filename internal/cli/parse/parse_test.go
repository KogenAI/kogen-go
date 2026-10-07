package parse

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"kogen-go/internal/cli/render"
)

func TestMovedFormsPrecedeTreeAndOptionParsing(t *testing.T) {
	got := Parse([]string{"build", "show", "--task-file=x"}, "/repo")
	if got.Kind != ResultMoved || got.Message != "kogen status <slug>" {
		t.Fatalf("moved prefix should win over flags: %#v", got)
	}

	got = Parse([]string{"status", "--", "--task-file"}, "/repo")
	if got.Kind != ResultMoved || got.Message != "kogen intent shape <slug> <file>" {
		t.Fatalf("moved flags scan the whole argv, including after --: %#v", got)
	}
}

func TestEveryFrozenMovedForm(t *testing.T) {
	tests := []struct {
		args    []string
		message string
	}{
		{args: []string{"build", "show", "greet"}, message: "kogen status <slug>"},
		{args: []string{"build", "greet"}, message: "kogen queue start (approved Intents build from the queue)"},
		{args: []string{"report", "greet"}, message: "kogen status <slug>"},
		{args: []string{"approve", "greet"}, message: "kogen intent approve <slug> <hash>"},
		{args: []string{"reconcile"}, message: "kogen status (crash recovery is automatic in status and queue start)"},
		{args: []string{"intent", "check", "greet"}, message: "kogen intent approve <slug> (prints the review card and check results)"},
		{args: []string{"intent", "close", "greet"}, message: "kogen intent remove <slug>"},
		{args: []string{"--version"}, message: "kogen version"},
		{args: []string{"status", "--task-file=request.md"}, message: "kogen intent shape <slug> <file>"},
		{args: []string{"status", "--yes"}, message: "kogen intent approve <slug> <hash>"},
		{args: []string{"status", "--borrow"}, message: "kogen provider login chatgpt for a Kogen-owned login"},
		{args: []string{"status", "--recipe"}, message: "build.recipe in .kogen/project.yaml"},
		{args: []string{"status", "--model"}, message: "build.roles.builder.model in .kogen/project.yaml"},
		{args: []string{"status", "--effort"}, message: "build.roles.builder.effort in .kogen/project.yaml"},
		{args: []string{"status", "--as=default"}, message: "kogen provider use chatgpt --as <label> --project <checkout>"},
	}
	for _, test := range tests {
		got := Parse(test.args, "/repo")
		if got.Kind != ResultMoved || got.Message != test.message {
			t.Errorf("Parse(%q) = %#v, want moved message %q", test.args, got, test.message)
		}
	}

	got := Parse([]string{"provider", "login", "grok", "--as", "default"}, "/repo")
	if got.Kind != ResultUsage || got.Message != "kogen provider login: unknown option '--as'" {
		t.Errorf("--as inside provider commands must be parsed normally: %#v", got)
	}
}

func TestTreeSelectionPrecedesOptionParsing(t *testing.T) {
	tests := []struct {
		args    []string
		kind    ResultKind
		page    render.HelpPage
		message string
	}{
		{args: nil, kind: ResultHelp, page: render.HelpTop},
		{args: []string{"help"}, kind: ResultHelp, page: render.HelpTop},
		{args: []string{"intent"}, kind: ResultHelp, page: render.HelpIntent},
		{args: []string{"provider"}, kind: ResultHelp, page: render.HelpProvider},
		{args: []string{"--project", "x", "status"}, kind: ResultUsage, page: render.HelpTop, message: "kogen: unknown command '--project'"},
		{args: []string{"intent", "--project", "x"}, kind: ResultUsage, page: render.HelpIntent, message: "kogen intent: unknown command '--project'"},
		{args: []string{"help", "status", "--help"}, kind: ResultUsage, page: render.HelpTop, message: "kogen help: unexpected argument 'status'"},
		{args: []string{"bogus"}, kind: ResultUsage, page: render.HelpTop, message: "kogen: unknown command 'bogus'"},
		{args: []string{"queue", "bogus"}, kind: ResultUsage, page: render.HelpQueue, message: "kogen queue: unknown command 'bogus'"},
		{args: []string{"--help"}, kind: ResultUsage, page: render.HelpTop, message: "kogen: unknown command '--help'"},
		{args: []string{"status", "--help"}, kind: ResultUsage, page: render.HelpStatus, message: "kogen status: unknown option '--help'"},
	}
	for _, test := range tests {
		got := Parse(test.args, "/repo")
		if got.Kind != test.kind || got.Page != test.page || got.Message != test.message {
			t.Errorf("Parse(%q) = %#v, want kind=%v page=%q message=%q", test.args, got, test.kind, test.page, test.message)
		}
	}
}

func TestOptionErrorsPrecedePositionalsInFixedOrder(t *testing.T) {
	tests := []struct {
		args    []string
		message string
	}{
		{args: []string{"status", "--json=yes", "--unknown"}, message: "kogen status: --json takes no value"},
		{args: []string{"intent", "shape", "--unknown"}, message: "kogen intent shape: unknown option '--unknown'"},
		{args: []string{"status", "-x"}, message: "kogen status: unknown option '-x'"},
		{args: []string{"intent", "shape", "--json"}, message: "kogen intent shape: unknown option '--json'"},
		{args: []string{"intent", "shape", "--project"}, message: "kogen intent shape: --project needs a value"},
		{args: []string{"provider", "login", "chatgpt", "--as"}, message: "kogen provider login: unknown option '--as'"},
	}
	for _, test := range tests {
		got := Parse(test.args, "/repo")
		if got.Kind != ResultUsage || got.Message != test.message {
			t.Errorf("Parse(%q) = %#v, want usage %q", test.args, got, test.message)
		}
	}

	got := Parse([]string{"intent", "shape", "greet", "--project"}, "/repo")
	if got.Kind != ResultUsage || got.Message != "kogen intent shape: --project needs a value" {
		t.Fatalf("option value errors must precede missing positionals: %#v", got)
	}
}

func TestPositionalsAndDoubleDash(t *testing.T) {
	tests := []struct {
		args    []string
		message string
	}{
		{args: []string{"intent", "shape"}, message: "kogen intent shape: missing <slug>"},
		{args: []string{"intent", "shape", "greet"}, message: "kogen intent shape: missing <file|->"},
		{args: []string{"provider", "login"}, message: "kogen provider login: missing <provider>"},
		{args: []string{"status", "greet", "extra"}, message: "kogen status: unexpected argument 'extra'"},
		{args: []string{"intent", "shape", "greet", "-", "extra"}, message: "kogen intent shape: unexpected argument 'extra'"},
	}
	for _, test := range tests {
		got := Parse(test.args, "/repo")
		if got.Kind != ResultUsage || got.Message != test.message {
			t.Errorf("Parse(%q) = %#v, want usage %q", test.args, got, test.message)
		}
	}

	got := Parse([]string{"status", "--", "--json"}, "/repo")
	if got.Kind != ResultCommand || got.Command.Route != RouteStatus || got.Command.Slug == nil || *got.Command.Slug != "--json" || got.Command.JSON {
		t.Fatalf("-- should make a flag-looking token positional: %#v", got)
	}
	got = Parse([]string{"intent", "approve", "--", "--by"}, "/repo")
	if got.Kind != ResultCommand || got.Command.Slug == nil || *got.Command.Slug != "--by" {
		t.Fatalf("-- should make a dash-prefixed slug positional: %#v", got)
	}
}

func TestRepeatedOptionsAndPathExpansion(t *testing.T) {
	got := Parse([]string{"status", "slug", "--project", "first", "--project=second", "--origin", "origin", "--base", "main", "--json"}, "/checkout")
	if got.Kind != ResultCommand {
		t.Fatalf("expected command, got %#v", got)
	}
	command := got.Command
	if command.Project.Project == nil || *command.Project.Project != "/checkout/second" {
		t.Errorf("last --project should win and resolve against cwd: %#v", command.Project.Project)
	}
	if command.Project.Origin == nil || *command.Project.Origin != "/checkout/origin" {
		t.Errorf("--origin should resolve against cwd: %#v", command.Project.Origin)
	}
	if command.Project.Base == nil || *command.Project.Base != "main" || !command.JSON {
		t.Errorf("base and boolean options should be preserved: %#v", command)
	}
	got = Parse([]string{"status", "--project", "nested/../path"}, "/checkout")
	if got.Kind != ResultCommand || got.Command.Project.Project == nil || *got.Command.Project.Project != "/checkout/nested/../path" {
		t.Errorf("path expansion should not canonicalize caller spelling: %#v", got)
	}

	got = Parse([]string{"provider", "use", "grok", "--as", "default"}, "/checkout")
	if got.Kind != ResultCommand || got.Command.Project.Project != nil {
		t.Errorf("provider use has no default project: %#v", got)
	}
	got = Parse([]string{"provider", "use", "grok", "--as", "default", "--project", "child"}, "/checkout")
	if got.Kind != ResultCommand || got.Command.Project.Project == nil || *got.Command.Project.Project != "/checkout/child" {
		t.Errorf("provider use project should resolve only when supplied: %#v", got)
	}
}

func TestValueValidationAndProviderNames(t *testing.T) {
	tests := []struct {
		args    []string
		message string
	}{
		{args: []string{"provider", "login", "claude"}, message: "kogen provider login: unknown provider 'claude' (supported: chatgpt, grok)"},
		{args: []string{"provider", "use", "claude"}, message: "kogen provider use: unknown provider 'claude' (supported: chatgpt, grok)"},
		{args: []string{"provider", "use", "grok"}, message: "kogen provider use: missing --as <label>"},
		{args: []string{"intent", "approve", "greet", "ABCDEF"}, message: "kogen intent approve: <hash> must be 6 to 64 lowercase hex characters"},
		{args: []string{"status", "--watch", "--json"}, message: "kogen status: --watch and --json can't be combined"},
	}
	for _, test := range tests {
		got := Parse(test.args, "/repo")
		if got.Kind != ResultUsage || got.Message != test.message {
			t.Errorf("Parse(%q) = %#v, want usage %q", test.args, got, test.message)
		}
	}

	for _, provider := range []string{"chatgpt", "grok"} {
		got := Parse([]string{"provider", "login", provider}, "/repo")
		if got.Kind != ResultCommand || got.Command.Provider != provider {
			t.Errorf("provider %q should be admitted: %#v", provider, got)
		}
	}
	got := Parse([]string{"status", "UPPER"}, "/repo")
	if got.Kind != ResultCommand {
		t.Errorf("slug shape is a handler check, not a parser check: %#v", got)
	}
}

func TestTargetHelpPagesMatchFrozenDraftBytes(t *testing.T) {
	pages := map[render.HelpPage]string{
		render.HelpTop:            "2d83950e020890041258878e2579f86cadbd53e477355cb6d2836982923ef012",
		render.HelpStatus:         "3c567f52f3f32d9590cc356c21878abcf8f0eb6749a6aeebbc291a88efe534b9",
		render.HelpIntent:         "de597987f0e63a3f1c068754662f1efe2072fed703f541992706c8996841aaee",
		render.HelpIntentShape:    "2d61825da99ebd953a74ba39f48d640704affaaa29b2312c39d7a96cbeab1f29",
		render.HelpIntentApprove:  "d77f824d32fb21ab6966235c498d99156b18a29be637f93aacfeca88d01e3367",
		render.HelpIntentRemove:   "a4ae9a65d1a9f81353988860f1223503f8a8ec91d562e7c654decd0933cb2651",
		render.HelpQueue:          "2768657f1280471228380daea5bb9f1621c4b3e7bff07ea5079595241d264d1c",
		render.HelpQueueStart:     "56c1732a5dd0d4730ab41b1d00dc217cfcb14d21ffe0db74994b86c4daa7dd4f",
		render.HelpQueueStop:      "11be1148839138bfe0cc921afd73c42c3ef607ad846415ab201e8414ab236705",
		render.HelpProvider:       "61a2924088cd49a2437c21913cfcd540b1224f8f85863e3e132a75c9e5740236",
		render.HelpProviderList:   "8d2e5e31b1e443a7851438a2371b6a34007aa9a0125d4b8c82eb17082b422dc6",
		render.HelpProviderLogin:  "d958e441024ac084db854a64583990fba92894e9a0d07e4d896075207f275ed2",
		render.HelpProviderLogout: "1fb492f4cbd3093052bc9a0ce503e2ebb7567b01d8a59dbf889d94bd7d4fbc24",
		render.HelpProviderUse:    "15924a12dd665162cccb9fb98d2c61d4f7f153ebc022a860569650cbbc26dc20",
		render.HelpVersion:        "31d6dafe6cb914e20dde1cabe7d0a80e6fee1b66b197219a1ee8faa573ed3412",
	}
	for page, want := range pages {
		digest := sha256.Sum256([]byte(page.Contents()))
		if got := hex.EncodeToString(digest[:]); got != want {
			t.Errorf("help page %q SHA-256 = %s, want %s", page, got, want)
		}
	}
}

func TestUsageRendererKeepsTheExactPageAndNewlines(t *testing.T) {
	message := "kogen: unknown command 'bogus'"
	got := render.UsageError{Message: message, Page: render.HelpTop}.Render()
	if got != message+"\n\n"+render.HelpTop.Contents() {
		t.Fatalf("usage rendering changed the fixed page bytes: %q", got)
	}
	if moved := render.Moved("kogen version"); moved != "kogen: moved: use kogen version\n" {
		t.Fatalf("moved rendering = %q", moved)
	}
}
