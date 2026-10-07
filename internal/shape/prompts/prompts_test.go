package prompts

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kogen-go/internal/intent"
)

func TestSystemPromptContainsCompleteIntentGrammar(t *testing.T) {
	prompt := SystemPrompt()
	if !strings.HasPrefix(prompt, ShaperRoleMarker) {
		t.Fatalf("system prompt must start with role marker %q", ShaperRoleMarker)
	}
	for _, required := range []string{
		"title: Show organization-local ticket numbers",
		"size: small",
		"domains: [app]",
		"required keys",
		"body title never replaces the YAML `title`",
		"`changes_gate` (boolean)",
		"`limits` (list of strings)",
		"`blocks_on` (list of slugs)",
		"`priority` (integer)",
		"`assumptions` and `shared_contracts`",
		"`source` (string)",
		"## Acceptance",
		"## Verify",
		"## Notes",
		"`test keep` checks behavior that already passes on the unchanged checkout",
		"`test` is a change check expected to be red on the unchanged checkout",
		"Kogen appends the original Request verbatim",
		"or change, paraphrase, trim, or normalize the Request bytes",
		"exact second path supplied by the user",
	} {
		if !strings.Contains(prompt, required) {
			t.Errorf("system prompt is missing %q", required)
		}
	}
	if strings.Contains(prompt, "## Brief\n") || strings.Contains(prompt, "## Request\n") {
		t.Fatal("system example must not add a Brief or controller-owned Request heading")
	}
}

func TestInitialMessageMatchesTemplateAndPreservesRequestBytes(t *testing.T) {
	domains := []string{"infra", "app"}
	gatePaths := []string{"mix.exs", ".kogen/gate.yaml"}
	request := []byte("Keep CRLF here.\r\nKeep UTF-8 here: \xc3\xa9\n")
	message := InitialMessage(
		"ticket-numbers",
		domains,
		gatePaths,
		request,
		".kogen/intents/ticket-numbers/intent.md",
		".kogen/acceptance/ticket-numbers_test.exs",
	)
	want := "Slug: ticket-numbers\n\n" +
		"Configured project domains: app, infra. Use only these names in the Intent and Verify lines.\n\n" +
		"Effective gate paths: `.kogen/gate.yaml`, `mix.exs`. Set `changes_gate: true` only when the task or planned changes require modifying one of these paths. Omit it for unrelated changes; running or inspecting checks alone does not count.\n\n" +
		"Task statement:\n" + string(request) +
		"\n\nWrite the Intent to `.kogen/intents/ticket-numbers/intent.md` and its acceptance test to `.kogen/acceptance/ticket-numbers_test.exs`."
	if message != want {
		t.Fatalf("initial message differs from the exact template\nwant: %q\n got: %q", want, message)
	}
	if !bytes.Contains([]byte(message), request) {
		t.Fatal("the request bytes were normalized or changed")
	}
	if domains[0] != "infra" || domains[1] != "app" || gatePaths[0] != "mix.exs" || gatePaths[1] != ".kogen/gate.yaml" {
		t.Fatal("InitialMessage changed caller-owned input slices")
	}
}

func TestRepairMessageAndFinishGuardUseExactFeedback(t *testing.T) {
	detail := "line 2: frontmatter is missing required key `size`"
	feedback := ValidationFeedback("intent_parse_failed", detail)
	if got, want := feedback, "candidate/intent_parse_failed: "+detail; got != want {
		t.Fatalf("validation feedback = %q, want %q", got, want)
	}
	repair := RepairMessage(
		RequiredPath{Path: ".kogen/intents/demo/intent.md", Readable: true},
		RequiredPath{Path: ".kogen/acceptance/demo_test.exs"},
		feedback,
	)
	wantRepair := "Validation failed. Repair the generated files in this conversation. The required paths and their current state are:\n" +
		"- `.kogen/intents/demo/intent.md`: present on disk. Keep it in place; change it only if the failure below requires a correction.\n" +
		"- `.kogen/acceptance/demo_test.exs`: missing or unreadable. Write it during this repair pass at this exact path.\n" +
		"Both exact paths must exist after this pass. Every missing path must be written now. Do not delete required files. The available tools can read, search, and write files; they cannot remove them. Preserve present content unless the failure below requires a focused correction.\n\n" +
		"Exact failure output:\n\n" + feedback
	if repair != wantRepair {
		t.Fatalf("repair message differs from the exact template\nwant: %q\n got: %q", wantRepair, repair)
	}
	if got, want := FinishGuardMessage([]string{".kogen/acceptance/demo_test.exs"}), "Both files must exist before you finish. Missing: .kogen/acceptance/demo_test.exs."; got != want {
		t.Fatalf("finish guard = %q, want %q", got, want)
	}
}

func TestRejectedBenchmarkSourcesKeepExactMissingKeyFeedback(t *testing.T) {
	cases := []struct {
		file       string
		slug       string
		missingKey string
	}{
		{file: "syn-06-rejected-intent.md", slug: "syn-06-migration-ticket-numbers", missingKey: "title"},
		{file: "syn-20-rejected-intent.md", slug: "syn-20-email-invite-flow", missingKey: "size"},
	}
	for _, test := range cases {
		t.Run(test.slug, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("testdata", test.file))
			if err != nil {
				t.Fatal(err)
			}
			_, err = intent.Parse(test.slug, source)
			parseError, ok := err.(*intent.ParseError)
			if !ok {
				t.Fatalf("fixture must remain rejected by the Intent parser, got %v", err)
			}
			wantDetail := "line 2: frontmatter is missing required key `" + test.missingKey + "`"
			if parseError.Error() != wantDetail {
				t.Fatalf("parser feedback = %q, want %q", parseError.Error(), wantDetail)
			}

			feedback := ValidationFeedback("intent_parse_failed", parseError.Error())
			repair := RepairMessage(RequiredPath{Path: "intent.md"}, RequiredPath{Path: "acceptance_test"}, feedback)
			if !strings.HasSuffix(repair, "Exact failure output:\n\n"+feedback) {
				t.Fatalf("repair did not preserve exact parser feedback: %q", repair)
			}
			if !strings.Contains(SystemPrompt(), "Include all three required keys") || !strings.Contains(SystemPrompt(), "body title never replaces the YAML `title`") {
				t.Fatal("shaper context does not explain the rejected required frontmatter")
			}
		})
	}
}

func TestFallbackMessageKeepsInitialTemplateAndFailure(t *testing.T) {
	initial := "Slug: demo\n\nTask statement:\nraw request"
	feedback := "candidate/intent_parse_failed: missing title"
	want := initial + "\n\nLast validation failure:\n\n" + feedback
	if got := FallbackMessage(initial, feedback); got != want {
		t.Fatalf("fallback message = %q, want %q", got, want)
	}
}
