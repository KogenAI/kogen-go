package intent

import (
	"bytes"
	"errors"
	"testing"
)

const validIntent = "---\ntitle: Greet Almir by name\nsize: small\ndomains: [app]\n---\nChange the greeting.\n\n## Acceptance\n- A1: greeting names Almir\n\n## Verify\n- A1: test\n"

func TestParsePreservesRequestAndWholeSourceBytes(t *testing.T) {
	request := []byte("## Request\r\nPlease retain CRLF.\r\n")
	request = append(request, 0xff, 0x00, '\n', 0xc3)
	source := append([]byte(validIntent), request...)
	wantSource := bytes.Clone(source)

	parsed, err := Parse("greet", source)
	if err != nil {
		t.Fatal(err)
	}
	wantRequest := request[len("## Request\r\n"):]
	if !parsed.HasRequest || !bytes.Equal([]byte(parsed.Request), wantRequest) {
		t.Fatalf("Request = %q, want raw %q", parsed.Request, wantRequest)
	}
	if !bytes.Equal(parsed.RawBytes(), source) {
		t.Fatal("RawBytes changed source bytes")
	}
	source[0] = 'x'
	if !bytes.Equal(parsed.RawBytes(), wantSource) {
		t.Fatal("RawBytes aliases caller storage")
	}
}

func TestParseNormalizesCRLFOnlyInStructuredSections(t *testing.T) {
	source := []byte("---\r\ntitle: T\r\nsize: small\r\ndomains: [app]\r\n---\r\nBrief one.\r\nBrief two.\r\n## Acceptance\r\n- A1: item\r\n## Verify\r\n- A1: test\r\n")
	parsed, err := Parse("greet", source)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Brief != "Brief one.\nBrief two." {
		t.Fatalf("Brief = %q", parsed.Brief)
	}
	if parsed.BriefLines[0].Text != "Brief one." || parsed.BriefLines[1].Text != "Brief two." {
		t.Fatalf("BriefLines retained CR: %#v", parsed.BriefLines)
	}
	if got := parsed.Acceptance[0]; got.Line != 9 || got.ID != "A1" || got.Text != "item" {
		t.Fatalf("Acceptance = %#v", got)
	}
}

func TestInvalidUTF8IsRejectedBeforeRequestButOpaqueAfterIt(t *testing.T) {
	before := []byte(validIntent + "bad ")
	before = append(before, 0xff)
	if _, err := Parse("greet", before); err == nil {
		t.Fatal("invalid UTF-8 before Request was accepted")
	} else {
		var issue *ParseError
		if !errors.As(err, &issue) || issue.Line != 13 || issue.Message != "Intent is not valid UTF-8" {
			t.Fatalf("parse error = %#v", err)
		}
	}

	after := append([]byte(validIntent+"## Request\n"), 0xff)
	if _, err := Parse("greet", after); err != nil {
		t.Fatalf("invalid UTF-8 in Request was rejected: %v", err)
	}
}

func TestParseFrontmatterBoundaryAndLineNumbers(t *testing.T) {
	tests := []struct {
		name, source, message string
		line                  int
	}{
		{"missing opening", "title: T\n", "frontmatter must start with `---`", 1},
		{"missing closing", "---\ntitle: T\nsize: small\n", "frontmatter is missing its closing `---`", 4},
		{"non-map", "---\n- item\n---\n", "frontmatter must be a YAML map", 2},
		{"unknown field", "---\ntitle: T\nsize: small\ndomains: []\nowner: me\n---\n", `unknown frontmatter key "owner"`, 5},
		{"missing required", "---\nsize: small\ndomains: []\n---\n", "frontmatter is missing required key `title`", 2},
		{"wrong type", "---\ntitle: T\nsize: small\ndomains: app\n---\n", "frontmatter `domains` must be a list", 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse("greet", []byte(test.source))
			var issue *ParseError
			if !errors.As(err, &issue) || issue.Line != test.line || issue.Message != test.message {
				t.Fatalf("Parse error = %#v; want line %d, %q", err, test.line, test.message)
			}
		})
	}
}

func TestSectionGrammarAndRequestBoundary(t *testing.T) {
	t.Run("all sections in any order", func(t *testing.T) {
		source := "---\ntitle: T\nsize: small\ndomains: [app]\n---\nBrief.\n## Notes\nApproach: do it.\n## Verify\n- A1: test keep integration domain=old after=A2 domain=app\n## Acceptance\n- A1: kept item\n## Request\nraw\r\n"
		parsed, err := Parse("greet", []byte(source))
		if err != nil {
			t.Fatal(err)
		}
		verify := parsed.VerifyFor("A1")
		if verify == nil || !verify.IsKeep() || verify.IsChange() || !verify.IsIntegration() || verify.Domain() != "app" {
			t.Fatalf("Verify item = %#v", verify)
		}
		if got := verify.AfterIDs(); len(got) != 1 || got[0] != "A2" {
			t.Fatalf("AfterIDs = %#v", got)
		}
		if !parsed.HasRequest || parsed.Request != "raw\r\n" {
			t.Fatalf("Request = %q, has=%t", parsed.Request, parsed.HasRequest)
		}
	})

	tests := []struct {
		name, suffix, message string
		line                  int
	}{
		{"duplicate Acceptance", "## Acceptance\n- A1: item\n## Acceptance\n", "duplicate Acceptance section", 10},
		{"duplicate Verify row", "## Acceptance\n- A1: item\n## Verify\n- A1: test\n- A1: test keep\n", "duplicate Verify entry for A1", 11},
		{"unknown section after known", "## Acceptance\n- A1: item\n## Extra\n", `unknown Intent section "Extra"`, 10},
		{"malformed acceptance row", "## Acceptance\nA1: item\n", "Acceptance entries use `- A<n>: one sentence` on one line", 8},
		{"malformed verify row", "## Acceptance\n- A1: item\n## Verify\n- A1 item\n", "Verify entries use `- A<n>: test` or `- A<n>: test keep`", 10},
		{"verify target absent", "## Acceptance\n- A1: item\n## Verify\n- A2: test\n", "Verify entry A2 has no Acceptance item", 10},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prefix := "---\ntitle: T\nsize: small\ndomains: [app]\n---\nBrief.\n"
			_, err := Parse("greet", []byte(prefix+test.suffix))
			var issue *ParseError
			if !errors.As(err, &issue) || issue.Line != test.line || issue.Message != test.message {
				t.Fatalf("Parse error = %#v; want line %d, %q", err, test.line, test.message)
			}
		})
	}
}

func TestUnknownBriefHeadingIsTextAndUnknownVerifyWordsRemainForLint(t *testing.T) {
	source := "---\ntitle: T\nsize: small\ndomains: [app]\n---\nBrief.\n## Background\ntext\n## Acceptance\n- A1: item\n## Verify\n- A1: check strange\n"
	parsed, err := Parse("greet", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Brief != "Brief.\n## Background\ntext" {
		t.Fatalf("Brief = %q", parsed.Brief)
	}
	if got := parsed.Verify[0].Words; len(got) != 2 || got[0] != "check" || got[1] != "strange" {
		t.Fatalf("Verify words = %#v", got)
	}
}

func TestSlugGrammar(t *testing.T) {
	for _, slug := range []string{"ab", "-abc", "abc-", "a--b", "Abc", "a_b", "ébc", string(bytes.Repeat([]byte{'a'}, 49))} {
		if _, err := Parse(slug, []byte(validIntent)); err == nil {
			t.Errorf("Parse accepted invalid slug %q", slug)
		}
	}
}
