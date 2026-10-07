package rails

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"kogen-go/internal/process"
)

func TestRailsFixtureDetectionAndExplicitOverride(t *testing.T) {
	fixture := filepath.Join("testdata", "rails")
	if !Detected(fixture) {
		t.Fatal("versioned Rails fixture was not detected")
	}
	if !Selected(fixture, nil) {
		t.Fatal("automatic selection did not select Rails")
	}
	command := "command"
	if Selected(fixture, &command) {
		t.Fatal("explicit non-Rails adapter did not override detection")
	}
	rails := "rails"
	if !Selected(t.TempDir(), &rails) {
		t.Fatal("explicit Rails adapter did not override missing marker files")
	}
}

func TestRailsDetectionRequiresBothMarkerFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Gemfile"), []byte("source 'https://rubygems.org'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if Detected(root) {
		t.Fatal("Gemfile alone selected Rails")
	}
	if err := os.WriteFile(filepath.Join(root, "config", "application.rb"), []byte("module Fixture; end\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !Detected(root) {
		t.Fatal("both Rails marker files did not select Rails")
	}
}

func TestRailsPathsCommandsSetupAndGateFiles(t *testing.T) {
	source, err := SourcePath("greet")
	if err != nil || source != ".kogen/acceptance/greet_test.rb" {
		t.Fatalf("source path = %q, %v", source, err)
	}
	candidate, err := CandidatePath("greet")
	if err != nil || candidate != "test/acceptance/greet_test.rb" {
		t.Fatalf("candidate path = %q, %v", candidate, err)
	}
	for _, slug := range []string{"../escape", "x", "two--words", "UPPER"} {
		if _, err := SourcePath(slug); err == nil {
			t.Errorf("unsafe slug %q accepted", slug)
		}
	}
	if got := RunnerCommand(); !reflect.DeepEqual(got, []string{"bundle", "exec", "rails", "test", "{path}"}) {
		t.Fatalf("runner command = %#v", got)
	}
	if got := AcceptanceCheck(); !reflect.DeepEqual(got, []string{"ruby", "-c", "{path}"}) {
		t.Fatalf("syntax command = %#v", got)
	}
	if got := SetupCommand(); !reflect.DeepEqual(got, []string{"bundle", "install", "--local"}) {
		t.Fatalf("setup command = %#v", got)
	}
	if got := SetupSeeds(); !reflect.DeepEqual(got, []string{"vendor/cache"}) {
		t.Fatalf("setup seeds = %#v", got)
	}
	if got := GateFiles(); !reflect.DeepEqual(got, []string{"Gemfile", "Gemfile.lock", "bin/rails", ".standard.yml", ".rubocop.yml"}) {
		t.Fatalf("gate files = %#v", got)
	}
	got := ChildEnvironment("/candidate/vendor/cache")
	want := process.Environment{"BUNDLE_PATH": "/candidate/vendor/cache", "RAILS_ENV": "test"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("child environment = %#v", got)
	}
}

func TestRailsFormatterSelectionReadsGemDeclarations(t *testing.T) {
	gemfile, err := os.ReadFile(filepath.Join("testdata", "rails", "Gemfile"))
	if err != nil {
		t.Fatal(err)
	}
	if got := Formatter(string(gemfile)); !reflect.DeepEqual(got, []string{"bundle", "exec", "standardrb", "-a"}) {
		t.Fatalf("fixture formatter = %#v", got)
	}
	for _, test := range []struct {
		name string
		src  string
		want []string
	}{
		{name: "rubocop", src: "gem(\"rubocop\", require: false)", want: []string{"bundle", "exec", "rubocop", "-a"}},
		{name: "standard takes precedence", src: "gem 'rubocop'\ngem 'standard'", want: []string{"bundle", "exec", "standardrb", "-a"}},
		{name: "comments and quoted text ignored", src: "# gem 'standard'\nmessage = \"gem 'rubocop'\"", want: nil},
		{name: "nearby gem name ignored", src: "gem 'standardrb'", want: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := Formatter(test.src); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("formatter = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestRailsFindingsParseMinitestAndRubyLint(t *testing.T) {
	output := []byte("" +
		"  1) Failure:\n" +
		"HelloTest#test_legacy [test/hello_test.rb:12]:\n" +
		"Expected value\n" +
		"app/models/hello.rb:4:7: C: [Correctable] Style/FrozenStringLiteralComment: Missing frozen string literal comment.\n" +
		"$WORKDIR/app/models/hello.rb:9: Layout/LineLength: Line is too long.\n")
	parsed := ParseFindingsForTool(output, "standard", "/tmp/work")
	if len(parsed) != 3 {
		t.Fatalf("findings = %#v", parsed)
	}
	if parsed[0].Path != "test/hello_test.rb" || parsed[0].Rule != "minitest/failure" || parsed[0].Symbol != "HelloTest#test_legacy" || parsed[0].Message != "1) Failure:" || parsed[0].Line == nil || *parsed[0].Line != 12 {
		t.Fatalf("Minitest finding = %#v", parsed[0])
	}
	if parsed[1].Path != "app/models/hello.rb" || parsed[1].Rule != "standard/Style/FrozenStringLiteralComment" || parsed[1].Symbol != "" || parsed[1].Message != "Missing frozen string literal comment." || parsed[1].Line == nil || *parsed[1].Line != 4 || parsed[1].Column == nil || *parsed[1].Column != 7 {
		t.Fatalf("Ruby lint finding = %#v", parsed[1])
	}
	if parsed[2].Path != "app/models/hello.rb" || parsed[2].Rule != "standard/Layout/LineLength" || parsed[2].Message != "Line is too long." || parsed[2].Line == nil || *parsed[2].Line != 9 || parsed[2].Column != nil {
		t.Fatalf("standard-style finding = %#v", parsed[2])
	}
}
