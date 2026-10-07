package yamlmini

import (
	"bytes"
	"strings"
	"testing"
)

func TestPreflightByteContract(t *testing.T) {
	tests := []struct {
		name    string
		source  []byte
		line    int
		message string
	}{
		{
			name:    "size limit is inclusive",
			source:  bytes.Repeat([]byte{'x'}, maxDocumentBytes),
			message: "",
		},
		{
			name:    "oversized takes precedence over BOM",
			source:  append([]byte{0xef, 0xbb, 0xbf}, bytes.Repeat([]byte{'x'}, maxDocumentBytes)...),
			message: "document exceeds the maximum size of 1048576 bytes",
		},
		{
			name:    "leading BOM",
			source:  []byte("\xef\xbb\xbfname: value\n"),
			line:    1,
			message: "leading UTF-8 BOM is not allowed",
		},
		{
			name:    "invalid UTF-8 reports its physical line",
			source:  []byte{'a', ':', ' ', 'o', 'k', '\n', 'b', ':', ' ', 0xff},
			line:    2,
			message: "document is not valid UTF-8",
		},
		{
			name:    "empty document",
			source:  nil,
			message: "empty document",
		},
		{
			name:    "comment-only document is empty",
			source:  []byte(" # comment\n\t# also a comment\n"),
			message: "empty document",
		},
		{
			name:    "tab in a comment is rejected",
			source:  []byte("name: value\n#\tcomment\n"),
			line:    2,
			message: "tab character: indent with spaces",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, issue := preflight(tt.source)
			if tt.message == "" {
				if issue != nil {
					t.Fatalf("preflight() error = %v", issue)
				}
				return
			}
			if issue == nil {
				t.Fatal("preflight() error = nil, want an issue")
			}
			if issue.Line != tt.line || !strings.Contains(issue.Message, tt.message) {
				t.Fatalf("preflight() issue = {line:%d message:%q}, want line %d containing %q", issue.Line, issue.Message, tt.line, tt.message)
			}
		})
	}
}

func TestLexicalRejectionCorpus(t *testing.T) {
	deep := "name: kt\nchecks: []\nx: " + strings.Repeat("[", 65) + strings.Repeat("]", 65) + "\n"
	tests := []struct {
		name    string
		source  string
		line    int
		message string
	}{
		{"tab", "name: kt\nchecks:\n\t- name: x\n", 3, "tab character"},
		{"directive", "%YAML 1.2\nname: kt\n", 1, "directives and document markers"},
		{"document marker", "---\nname: kt\n", 1, "directives and document markers"},
		{"block scalar", "name: kt\nchecks: []\nbase: |\n", 3, "block scalars are not allowed"},
		{"anchor", "name: kt\nchecks: []\nbase: &b main\n", 3, "anchors, aliases"},
		{"alias", "name: kt\nchecks: []\nbase: *b\n", 3, "anchors, aliases"},
		{"tag", "name: kt\nchecks: []\nbase: !tag value\n", 3, "anchors, aliases"},
		{"depth", deep, 3, "maximum nesting depth of 64"},
		{"merge key", "name: kt\nchecks: []\n<<: {a: b}\n", 3, "YAML merge key"},
		{"unicode escape", "name: \"k\\u0074\"\nchecks: []\n", 1, "Unicode escape \\u"},
		{"unsupported escape", "name: \"k\\x\"\nchecks: []\n", 1, "unsupported escape \\x"},
		{"unterminated quote", "name: kt\nchecks: []\nbase: \"main\n", 3, "unterminated quoted string"},
		{"quote tail", "name: kt\nchecks: []\nbase: \"main\" x\n", 3, "text after closing quote"},
		{"bracket in flow item", "name: kt\nchecks: []\nprotected_paths: [a[1]]\n", 3, "brackets inside a flow collection"},
		{"malformed flow", "name: kt\nchecks: []\nprotected_paths: [a,, b]\n", 3, "malformed flow collection"},
		{"unclosed flow", "name: kt\nchecks: []\nprotected_paths: [a, b\n", 3, "unterminated flow collection"},
		{"flow tail", "name: kt\nchecks: []\nprotected_paths: [a] b\n", 3, "trailing text after flow collection"},
		{"plain colon-space", "name: kt\nchecks: []\nbase: a: b\n", 3, "unquoted `: ` inside a value"},
		{"list item as value", "name: kt\nchecks: []\nbase: - main\n", 3, "list item in a value position"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, issue := preflight([]byte(tt.source))
			if issue == nil {
				t.Fatalf("preflight(%q) error = nil", tt.source)
			}
			if issue.Line != tt.line || !strings.Contains(issue.Message, tt.message) {
				t.Fatalf("preflight() issue = {line:%d message:%q}, want line %d containing %q", issue.Line, issue.Message, tt.line, tt.message)
			}
		})
	}
}

func TestFirstErrorOrderingIsLineThenCatalogOrder(t *testing.T) {
	// On the same line, the reserved anchor has catalog order 6 and wins over
	// the Unicode escape at order 9. A later tab cannot outrank either one.
	source := "name: &anchor \"bad\\u\"\n\tvalue: later\n"
	_, issue := preflight([]byte(source))
	if issue == nil {
		t.Fatal("preflight() error = nil")
	}
	if issue.Line != 1 || issue.Message != "anchors, aliases, and block scalars are not allowed" {
		t.Fatalf("first issue = {line:%d message:%q}", issue.Line, issue.Message)
	}

	// A structural callback can contribute an earlier catalog item without
	// changing the candidate ordering used by the lexical scanner.
	_, issue = preflight([]byte("name: &anchor value\n"), func(_ []string, errors *[]candidate) {
		addCandidate(errors, 1, 19, "unexpected indentation")
	})
	if issue == nil || issue.Message != "anchors, aliases, and block scalars are not allowed" {
		t.Fatalf("first issue after structural checks = %v", issue)
	}
}

func TestQuotesEscapesAndComments(t *testing.T) {
	source := "name: \"say \\\"# inside quote\\\"\" # outside quote\n" +
		"single: 'it''s # still quoted' # outside quote\n" +
		"reserved: \"text: &not-anchor *not-alias !not-tag\"\n" +
		"escapes: \"\\n\\t\\\"\\\\\\/\"\n" +
		"plain: value # &alias [comment is ignored\n"
	if _, issue := preflight([]byte(source)); issue != nil {
		t.Fatalf("preflight(valid quoted source) error = %v", issue)
	}

	if got := stripComment("value: \"# quoted\" # comment"); got != "value: \"# quoted\" " {
		t.Fatalf("stripComment() = %q", got)
	}
	if got := stripComment("value: plain#not-comment"); got != "value: plain#not-comment" {
		t.Fatalf("stripComment() removed an inline # without preceding whitespace: %q", got)
	}
}

func TestPreflightPreservesUTF8Text(t *testing.T) {
	source := []byte("name: café\n")
	text, issue := preflight(source)
	if issue != nil {
		t.Fatalf("preflight(valid UTF-8) error = %v", issue)
	}
	if text != string(source) {
		t.Fatalf("preflight() text = %q, want exact source %q", text, source)
	}
}
