package intent

import (
	"fmt"
	"strings"
	"testing"
)

func TestLintStructuralErrors(t *testing.T) {
	tests := []struct {
		name        string
		intent      *Intent
		wantRule    string
		wantLine    int
		wantMessage string
	}{
		{
			name:        "missing brief",
			intent:      parseLintFixture(t, "", "- A1: item", "- A1: test", ""),
			wantRule:    "missing_brief",
			wantMessage: "write the Brief as prose",
		},
		{
			name:        "list in brief",
			intent:      parseLintFixture(t, "Change the greeting.\n- include the name", "- A1: item", "- A1: test", ""),
			wantRule:    "list_in_brief",
			wantLine:    7,
			wantMessage: "the Brief cannot contain lists",
		},
		{
			name:        "heading in brief",
			intent:      parseLintFixture(t, "Change the greeting.\n## Context", "- A1: item", "- A1: test", ""),
			wantRule:    "heading_in_brief",
			wantLine:    7,
			wantMessage: "the Brief cannot contain headings",
		},
		{
			name:        "code block in brief",
			intent:      parseLintFixture(t, "Change the greeting.\n```go", "- A1: item", "- A1: test", ""),
			wantRule:    "code_block_in_brief",
			wantLine:    7,
			wantMessage: "the Brief cannot contain code blocks",
		},
		{
			name:        "unknown size",
			intent:      parseLintFixtureOptions(t, "Intent", "huge", "[app]", "Change the greeting.", "- A1: item", "- A1: test", ""),
			wantRule:    "unknown_size",
			wantMessage: "size must be small, medium, or large",
		},
		{
			name:        "empty acceptance",
			intent:      parseLintFixture(t, "Change the greeting.", "", "", ""),
			wantRule:    "acceptance_count",
			wantMessage: "Acceptance needs at least one item",
		},
		{
			name:        "missing title",
			intent:      parseLintFixtureOptions(t, "", "small", "[app]", "Change the greeting.", "- A1: item", "- A1: test", ""),
			wantRule:    "missing_title",
			wantMessage: "title is required",
		},
		{
			name:        "domain count",
			intent:      parseLintFixtureOptions(t, "Intent", "small", "[]", "Change the greeting.", "- A1: item", "- A1: test", ""),
			wantRule:    "domain_count",
			wantMessage: "declare between one and four domains",
		},
		{
			name:        "duplicate id",
			intent:      parseLintFixture(t, "Change the greeting.", "- A1: first\n- A1: second", "- A1: test", ""),
			wantRule:    "duplicate_id",
			wantLine:    10,
			wantMessage: "Acceptance ids must be unique",
		},
		{
			name:        "sequential ids",
			intent:      parseLintFixture(t, "Change the greeting.", "- A1: first\n- A3: second", "- A1: test\n- A3: test", ""),
			wantRule:    "sequential_ids",
			wantMessage: "Acceptance ids must be A1 through An in order",
		},
		{
			name:        "missing verify",
			intent:      parseLintFixture(t, "Change the greeting.", "- A1: first\n- A2: second", "- A1: test", ""),
			wantRule:    "missing_verify",
			wantMessage: "every Acceptance item needs a Verify kind",
		},
		{
			name:        "invalid verify kind",
			intent:      parseLintFixture(t, "Change the greeting.", "- A1: item", "- A1: manual", ""),
			wantRule:    "invalid_verify",
			wantLine:    12,
			wantMessage: `unknown Verify word "manual"`,
		},
		{
			name:        "unsupported verify kind",
			intent:      parseLintFixture(t, "Change the greeting.", "- A1: item", "- A1: check", ""),
			wantRule:    "unsupported_verify_kind",
			wantLine:    12,
			wantMessage: "check is not supported in core v1",
		},
		{
			name:        "no change item",
			intent:      parseLintFixture(t, "Change the greeting.", "- A1: item", "- A1: test keep", ""),
			wantRule:    "no_change_item",
			wantMessage: "at least one Acceptance item must be a change item (test)",
		},
		{
			name:        "open question",
			intent:      parseLintFixture(t, "Change the greeting. [ needs clarification on punctuation ]", "- A1: item", "- A1: test", ""),
			wantRule:    "open_question",
			wantLine:    6,
			wantMessage: "remove TBD, TODO, FIXME, or unresolved question markers",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			issue := findLintIssue(test.intent.Lint(), test.wantRule)
			if issue == nil {
				t.Fatalf("Lint() missing %q", test.wantRule)
			}
			if issue.Severity != LintError || issue.Message != test.wantMessage {
				t.Fatalf("issue = %#v", issue)
			}
			if test.wantLine == 0 && issue.Line != nil {
				t.Fatalf("line = %d, want none", *issue.Line)
			}
			if test.wantLine != 0 && (issue.Line == nil || *issue.Line != test.wantLine) {
				t.Fatalf("line = %v, want %d", issue.Line, test.wantLine)
			}
		})
	}
}

func TestLintRequestIsOpaqueAndExempt(t *testing.T) {
	source := []byte(lintFixture("Change the greeting.", "- A1: item", "- A1: test", ""))
	source = append(source, []byte("\n## Request\nensure robust ??? TODO and/or note that this remains raw\r\n")...)
	intent, err := Parse("greet", source)
	if err != nil {
		t.Fatal(err)
	}
	if got := intent.Lint(); len(got) != 0 {
		t.Fatalf("Lint() = %#v, want no findings", got)
	}
}

func TestLintUsesEveryNormativeBannedWordAndPhrase(t *testing.T) {
	terms := append(append([]string(nil), normativeLint.BannedWords...), normativeLint.BannedPhrases...)
	for _, term := range terms {
		t.Run(strings.ReplaceAll(term, " ", "_"), func(t *testing.T) {
			intent := parseLintFixture(t, "The phrase "+term+" appears here.", "- A1: item", "- A1: test", "")
			issues := intent.Lint()
			var matches []LintIssue
			for _, issue := range issues {
				if issue.Rule == "banned_phrase" {
					matches = append(matches, issue)
				}
			}
			wantMessage := fmt.Sprintf("Brief contains banned phrase %q", term)
			found := false
			for _, issue := range matches {
				if issue.Severity != LintStyle {
					t.Fatalf("banned finding severity for %q = %q", term, issue.Severity)
				}
				if issue.Message == wantMessage {
					found = true
				}
			}
			if !found {
				t.Fatalf("banned findings for %q = %#v", term, matches)
			}
		})
	}
}

func TestLintStyleExemptionsAndWarnings(t *testing.T) {
	t.Run("inline code and word boundaries", func(t *testing.T) {
		intent := parseLintFixture(t, "The value `ensure` is literal; ensurement is a separate identifier.", "- A1: item", "- A1: test", "")
		for _, issue := range intent.Lint() {
			if issue.Rule == "banned_phrase" {
				t.Fatalf("unexpected banned phrase: %#v", issue)
			}
		}
	})

	t.Run("brief and acceptance hedges", func(t *testing.T) {
		intent := parseLintFixture(t, "The output should match the expected line.", "- A1: the output may match the expected line", "- A1: test", "")
		issues := intent.Lint()
		if got := countLintRule(issues, "hedge"); got != 2 {
			t.Fatalf("hedge findings = %d, want brief and A1", got)
		}
	})

	t.Run("notes phrase hedge and sentence exemptions", func(t *testing.T) {
		intent := parseLintFixture(t, "Change the greeting.", "- A1: item", "- A1: test", "Approach: this should ensure robust code in the right place. This sentence has more than thirty words and is only notes text so it stays exempt from the phrase hedge and sentence checks while retaining the original Notes for the approval card.")
		for _, issue := range intent.Lint() {
			if issue.Rule == "banned_phrase" || issue.Rule == "hedge" || issue.Rule == "sentence_too_long" {
				t.Fatalf("Notes produced exempt style finding: %#v", issue)
			}
		}
	})

	t.Run("size and item warnings", func(t *testing.T) {
		longItem := "- A1: " + strings.Repeat("word ", 25) + "last"
		intent := parseLintFixtureOptions(t, strings.Repeat("界", 73), "small", "[app]", "The output changes.", longItem, "- A1: test", "")
		warnings := intent.StyleWarnings()
		if len(warnings) != 2 {
			t.Fatalf("warnings = %#v, want title and item warnings", warnings)
		}
		if warnings[0].Code != "lint_title_too_long" || len(warnings[0].ItemIDs) != 0 {
			t.Fatalf("title warning = %#v", warnings[0])
		}
		if warnings[1].Code != "lint_item_too_long" || len(warnings[1].ItemIDs) != 1 || warnings[1].ItemIDs[0] != "A1" || warnings[1].Message != "A1 exceeds 25 words" {
			t.Fatalf("item warning = %#v", warnings[1])
		}
	})

	t.Run("tier thresholds are inclusive", func(t *testing.T) {
		brief := strings.TrimSpace(strings.Repeat("word ", normativeLint.Tiers["small"].BriefWords))
		intent := parseLintFixture(t, brief, "- A1: item", "- A1: test", strings.TrimSpace(strings.Repeat("note ", normativeLint.Tiers["small"].NotesWords)))
		if got := countLintRule(intent.Lint(), "brief_too_long") + countLintRule(intent.Lint(), "notes_too_long"); got != 0 {
			t.Fatalf("at-limit text produced %d length findings", got)
		}
	})

	t.Run("code block limit counts content lines", func(t *testing.T) {
		code := "```\n" + strings.Repeat("line\n", normativeLint.Limits.NotesCodeBlockLines+1) + "```"
		intent := parseLintFixture(t, "Change the greeting.", "- A1: item", "- A1: test", code)
		issue := findLintIssue(intent.Lint(), "long_code_block")
		if issue == nil || issue.Severity != LintStyle || issue.Message != "Notes code blocks must contain at most 15 lines" {
			t.Fatalf("long code block issue = %#v", issue)
		}
	})
}

func TestLintShapingRulesAndGatePaths(t *testing.T) {
	t.Run("missing approach only while shaping", func(t *testing.T) {
		intent := parseLintFixture(t, "Change the greeting.", "- A1: item", "- A1: test", "plain notes")
		if got := countLintRule(intent.Lint(), "missing_approach"); got != 0 {
			t.Fatalf("approval missing_approach findings = %d", got)
		}
		issue := findLintIssue(intent.LintForShaping(), "missing_approach")
		if issue == nil || issue.Severity != LintStyle || issue.WarningCode() != "lint_missing_approach" {
			t.Fatalf("shaping missing_approach = %#v", issue)
		}
	})

	t.Run("normalizes a valid action approach", func(t *testing.T) {
		got, changed := NormalizeNotes("  Replace the parser path with one focused helper and retain stable diagnostics.")
		if !changed || got != "Approach: Replace the parser path with one focused helper and retain stable diagnostics." {
			t.Fatalf("NormalizeNotes() = %q, %t", got, changed)
		}
		if _, changed := NormalizeNotes("write only five words here"); changed {
			t.Fatal("short Notes unexpectedly normalized")
		}
	})

	t.Run("checks declared gate paths only in shaping mode", func(t *testing.T) {
		intent := parseLintFixture(t, "Change the greeting.", "- A1: item", "- A1: test", "Edit config/build.yaml")
		options := LintOptions{Shaping: true, GatePaths: []string{"config/build.yaml"}}
		issue := findLintIssue(intent.LintWithOptions(options), "undeclared_gate_path")
		if issue == nil || issue.Severity != LintError || issue.Message != "Gate-path edit requires `changes_gate: true`; matched path config/build.yaml." {
			t.Fatalf("gate-path issue = %#v", issue)
		}
		if issue := findLintIssue(intent.Lint(), "undeclared_gate_path"); issue != nil {
			t.Fatalf("approval lint unexpectedly checked gate paths: %#v", issue)
		}
	})
}

func parseLintFixture(t *testing.T, brief, acceptance, verify, notes string) *Intent {
	t.Helper()
	return parseLintFixtureOptions(t, "A concise title", "small", "[app]", brief, acceptance, verify, notes)
}

func parseLintFixtureOptions(t *testing.T, title, size, domains, brief, acceptance, verify, notes string) *Intent {
	t.Helper()
	parsed, err := Parse("greet", []byte(lintFixtureOptions(title, size, domains, brief, acceptance, verify, notes)))
	if err != nil {
		t.Fatalf("Parse fixture: %v", err)
	}
	return parsed
}

func lintFixture(brief, acceptance, verify, notes string) string {
	return lintFixtureOptions("A concise title", "small", "[app]", brief, acceptance, verify, notes)
}

func lintFixtureOptions(title, size, domains, brief, acceptance, verify, notes string) string {
	if title == "" {
		title = `""`
	}
	var source strings.Builder
	fmt.Fprintf(&source, "---\ntitle: %s\nsize: %s\ndomains: %s\n---\n", title, size, domains)
	if brief != "" {
		source.WriteString(brief)
	}
	source.WriteString("\n\n## Acceptance\n")
	if acceptance != "" {
		source.WriteString(acceptance)
		source.WriteByte('\n')
	}
	source.WriteString("\n## Verify\n")
	if verify != "" {
		source.WriteString(verify)
		source.WriteByte('\n')
	}
	if notes != "" {
		source.WriteString("\n## Notes\n")
		source.WriteString(notes)
		source.WriteByte('\n')
	}
	source.WriteString("\n## Request\nensure robust ??? TODO and/or note that this is outside lint\n")
	return source.String()
}

func findLintIssue(issues []LintIssue, rule string) *LintIssue {
	for index := range issues {
		if issues[index].Rule == rule {
			return &issues[index]
		}
	}
	return nil
}

func countLintRule(issues []LintIssue, rule string) int {
	count := 0
	for _, issue := range issues {
		if issue.Rule == rule {
			count++
		}
	}
	return count
}
