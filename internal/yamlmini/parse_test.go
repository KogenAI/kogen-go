package yamlmini

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseBlockMapsSequencesAndStringScalars(t *testing.T) {
	source := []byte(`# project settings
name: 123
enabled: true
empty: ""
build:
  env:
    GOOS: darwin
  checks:
    - name: unit
      argv: [go, test, ./...]
      timeout_ms: 120000
    - name: lint
      argv:
        - go
        - vet
        - ./...
`)
	got, err := Parse(source)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := Mapping{
		"name":    "123",
		"enabled": "true",
		"empty":   "",
		"build": Mapping{
			"env": Mapping{"GOOS": "darwin"},
			"checks": Sequence{
				Mapping{
					"name":       "unit",
					"argv":       Sequence{"go", "test", "./..."},
					"timeout_ms": "120000",
				},
				Mapping{
					"name": "lint",
					"argv": Sequence{"go", "vet", "./..."},
				},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse() = %#v, want %#v", got, want)
	}
}

func TestParseMultilineFlowCollectionsCommentsAndQuotes(t *testing.T) {
	source := []byte("values: [one,\n  {two: \"line\\nvalue\", three: ['it''s', four]},\n] # comment\n" +
		"mapping: {alpha: x,\n  beta: [y, z],\n}\n" +
		"implicit: [first: value, second: [a, b], next]\n")
	got, err := Parse(source)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := Mapping{
		"values": Sequence{
			"one",
			Mapping{
				"two":   "line\nvalue",
				"three": Sequence{"it's", "four"},
			},
		},
		"mapping": Mapping{
			"alpha": "x",
			"beta":  Sequence{"y", "z"},
		},
		"implicit": Sequence{
			Mapping{"first": "value", "second": Sequence{"a", "b"}},
			"next",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse() = %#v, want %#v", got, want)
	}
}

func TestParseQuotedAndPlainScalarsStayStrings(t *testing.T) {
	source := []byte("- \"say \\\"hello\\\" # inside\"\n" +
		"- 'it''s # quoted'\n" +
		"- plain#attached\n" +
		"- \"slash\\/ tab\\t newline\\n\" # trailing comment\n")
	got, err := Parse(source)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := Sequence{"say \"hello\" # inside", "it's # quoted", "plain#attached", "slash/ tab\t newline\n"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse() = %#v, want %#v", got, want)
	}
}

func TestParseRejectsStructuralErrorsWithFirstLine(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		line    int
		message string
	}{
		{"duplicate block key", "name: one\nname: two\n", 2, `duplicate key "name"`},
		{"duplicate nested key", "outer:\n  name: one\n  name: two\n", 3, `duplicate key "name"`},
		{"duplicate flow key", "value: {name: one, name: two}\n", 1, `duplicate key "name"`},
		{"duplicate multiline flow key", "value: {name: one,\n  name: two}\n", 2, `duplicate key "name"`},
		{"empty mapping value", "value:\n", 1, "mapping key has no value"},
		{"empty sequence item", "values:\n  -\n", 2, "list item has no value"},
		{"indentless sequence", "values:\n- one\n", 2, "unexpected indentation"},
		{"root indentation", "  name: one\n", 1, "unexpected indentation"},
		{"unexpected nested indentation", "name: one\n  other: two\n", 2, "unexpected indentation"},
		{"bad nested indentation", "outer:\n  inner:\n    first: one\n   second: two\n", 4, "unexpected indentation"},
		{"malformed flow", "values: [one,, two]\n", 1, "malformed flow collection"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.source))
			issue, ok := err.(*Issue)
			if !ok {
				t.Fatalf("Parse() error = %v, want *Issue", err)
			}
			if issue.Line != tt.line || !strings.Contains(issue.Message, tt.message) {
				t.Fatalf("Parse() issue = {line:%d message:%q}, want line %d containing %q", issue.Line, issue.Message, tt.line, tt.message)
			}
		})
	}
}

func TestParseRejectsForbiddenYAMLFeatures(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		line    int
		message string
	}{
		{"directive", "%YAML 1.2\nname: one\n", 1, "directives and document markers"},
		{"document marker", "---\nname: one\n", 1, "directives and document markers"},
		{"anchor", "name: &base one\n", 1, "anchors, aliases"},
		{"alias", "name: *base\n", 1, "anchors, aliases"},
		{"tag", "name: !tag one\n", 1, "anchors, aliases"},
		{"block scalar", "name: |\n  body\n", 1, "block scalars are not allowed"},
		{"merge key", "value: {<<: {a: b}}\n", 1, "YAML merge key"},
		{"plain colon space", "value: one: two\n", 1, "unquoted `: ` inside a value"},
		{"list in value position", "value: - one\n", 1, "list item in a value position"},
		{"unicode escape", `name: "\u0074"` + "\n", 1, "Unicode escape \\u"},
		{"unsupported escape", `name: "\x"` + "\n", 1, "unsupported escape \\x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.source))
			issue, ok := err.(*Issue)
			if !ok {
				t.Fatalf("Parse() error = %v, want *Issue", err)
			}
			if issue.Line != tt.line || !strings.Contains(issue.Message, tt.message) {
				t.Fatalf("Parse() issue = {line:%d message:%q}, want line %d containing %q", issue.Line, issue.Message, tt.line, tt.message)
			}
		})
	}
}
