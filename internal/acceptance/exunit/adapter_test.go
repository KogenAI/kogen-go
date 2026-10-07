package exunit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"kogen-go/internal/acceptance"
	"kogen-go/internal/contract"
	"kogen-go/internal/process"
)

func TestPathsAndSeedDirectories(t *testing.T) {
	source, err := SourcePath("greet")
	if err != nil || source != ".kogen/acceptance/greet_test.exs" {
		t.Fatalf("SourcePath = %q, %v", source, err)
	}
	candidate, err := CandidatePath("greet")
	if err != nil || candidate != "test/acceptance/greet_test.exs" {
		t.Fatalf("CandidatePath = %q, %v", candidate, err)
	}
	if got := SetupSeeds(); !reflect.DeepEqual(got, []string{"deps", "_build"}) {
		t.Fatalf("SetupSeeds = %#v", got)
	}
	for _, slug := range []string{"../greet", "x", "Upper", "a/b", "greet_test"} {
		if _, err := SourcePath(slug); !errors.Is(err, ErrInvalidSlug) {
			t.Errorf("SourcePath(%q) error = %v", slug, err)
		}
	}
}

func TestWriteFormatterPublishesPrivateRunArtifact(t *testing.T) {
	workspace, runDir := t.TempDir(), t.TempDir()
	path, err := WriteFormatter(runDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(runDir, FormatterName) {
		t.Fatalf("formatter path = %q", path)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != FormatterSource() {
		t.Fatalf("formatter contents = %q, %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(workspace, FormatterName)); !os.IsNotExist(err) {
		t.Fatalf("formatter appeared in workspace: %v", err)
	}
	if got, err := WriteFormatter(runDir, nil); err != nil || got != path {
		t.Fatalf("idempotent WriteFormatter = %q, %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("formatter mode = %o", info.Mode().Perm())
	}
}

func TestWriteFormatterRejectsUnexpectedExistingLeaf(t *testing.T) {
	for _, test := range []struct {
		name string
		make func(string) error
	}{
		{name: "different file", make: func(path string) error { return os.WriteFile(path, []byte("foreign"), 0o600) }},
		{name: "symlink", make: func(path string) error { return os.Symlink("target", path) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			runDir := t.TempDir()
			if err := test.make(filepath.Join(runDir, FormatterName)); err != nil {
				t.Fatal(err)
			}
			if _, err := WriteFormatter(runDir, nil); !errors.Is(err, ErrFormatterExists) {
				t.Fatalf("WriteFormatter error = %v", err)
			}
		})
	}
}

func TestRunnerCommandAndFormatterSelection(t *testing.T) {
	runDir := t.TempDir()
	path, err := WriteFormatter(runDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	command, err := RunnerCommand(path, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mise", "exec", "--", "elixir", "-e", "Code.require_file(" + elixirString(path) + ")", "-S", "mix", "test", "--formatter", "KogenLedgerFormatter", "--formatter", "ExUnit.CLIFormatter", "{path}"}
	if !reflect.DeepEqual(command, want) {
		t.Fatalf("RunnerCommand = %#v, want %#v", command, want)
	}
	quoted := elixirString(`/run dir/a"b\c#{x}.ex`)
	if quoted != `"/run dir/a\"b\\c\#{x}.ex"` {
		t.Fatalf("Elixir string = %q", quoted)
	}
	checks := []contract.CheckSpec{
		{Name: "lint", Program: "mix", Args: []string{"credo"}},
		{Name: "format", Program: "mise", Args: []string{"exec", "--", "mix", "format", "--check-formatted", "--dot-formatter", ".formatter.exs"}},
		{Name: "format later", Program: "mix", Args: []string{"format", "--check-formatted"}},
	}
	if got := Formatter(checks, "test/hello_test.exs"); !reflect.DeepEqual(got, []string{"mise", "exec", "--", "mix", "format", "--dot-formatter", ".formatter.exs"}) {
		t.Fatalf("derived formatter = %#v", got)
	}
	if got := Formatter(nil, "lib/hello.ex"); !reflect.DeepEqual(got, []string{"mix", "format", "lib/hello.ex"}) {
		t.Fatalf("default formatter = %#v", got)
	}
	if got := Formatter(nil, "README.md"); got != nil {
		t.Fatalf("non-Elixir formatter = %#v", got)
	}
}

func TestUnavailableDetectionUsesFirstTwentyLinesAndWholeWords(t *testing.T) {
	for _, log := range [][]byte{
		[]byte("/usr/bin/env: erl: No such file or directory\n"),
		[]byte("elixir: command not found\n"),
		[]byte("mise ERROR: could not find mix executable\n"),
		[]byte("Can't find 'erl' to start Erlang\n"),
	} {
		if !Unavailable(log) {
			t.Errorf("Unavailable(%q) = false", log)
		}
	}
	later := strings.Repeat("ordinary output\n", 20) + "mix: command not found\n"
	if Unavailable([]byte(later)) {
		t.Fatal("runtime absence after line 20 was treated as unavailable")
	}
	for _, line := range []string{"mix test failed because test is missing", "mixing is not found", "elixirize not found"} {
		if Unavailable([]byte(line)) {
			t.Errorf("ordinary diagnostic %q was treated as missing runtime", line)
		}
	}
}

func TestParseFindingsFromExUnitElixirCredoAndMix(t *testing.T) {
	output := strings.Join([]string{
		"  1) test legacy is broken (HelloTest)",
		"     test/hello_test.exs:3",
		"     Assertion with == failed",
		"** (CompileError) lib/hello.ex:9: undefined function Hello.foo/0",
		"┃ [W] ↗ Credo.Check.Readability.ModuleDoc: Add a moduledoc.",
		"┃ lib/hello.ex:4:2",
		"The following files are not formatted:",
		"  * test/hello_test.exs",
	}, "\n")
	got := ParseFindings([]byte(output), "/tmp/project")
	if len(got) != 4 {
		t.Fatalf("findings = %#v", got)
	}
	if got[0].Path != "test/hello_test.exs" || got[0].Rule != "exunit/assertion" || got[0].Symbol != "legacy is broken" || got[0].Line == nil || *got[0].Line != 3 {
		t.Fatalf("ExUnit finding = %#v", got[0])
	}
	if got[1].Path != "lib/hello.ex" || got[1].Rule != "compile/undefined" {
		t.Fatalf("compiler finding = %#v", got[1])
	}
	if got[2].Path != "lib/hello.ex" || got[2].Rule != "credo/Readability.ModuleDoc" || got[2].Column == nil || *got[2].Column != 2 {
		t.Fatalf("Credo finding = %#v", got[2])
	}
	if got[3].Path != "test/hello_test.exs" || got[3].Rule != "format/unformatted" {
		t.Fatalf("format finding = %#v", got[3])
	}
}

func TestRunnerUsesExternalFormatterAndMapsMissingRuntime(t *testing.T) {
	workdir, runDir := t.TempDir(), t.TempDir()
	processes := &processStub{
		result: contract.ProcessResult{ExitStatus: intPointer(1)},
		log:    []byte("/usr/bin/env: erl: No such file or directory\n"),
	}
	runner := &Runner{Processes: processes, Trees: stableTree{}}
	result, err := runner.Run(context.Background(), Request{
		Slug: "greet", Workdir: workdir, RunDir: runDir, UseMise: true,
		Environment: process.Environment{"PATH": "/bin"}, ExpectedItems: []string{"A1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Process.Unavailable || !containsFailure(result.Failures, acceptance.FailureToolMissing) || containsFailure(result.Failures, acceptance.FailureAcceptanceCompileFailed) {
		t.Fatalf("missing runtime result = %#v", result)
	}
	if !strings.Contains(processes.spec.Executable, "mise") || processes.spec.Args[len(processes.spec.Args)-1] != filepath.Join(workdir, CandidateDir, "greet_test.exs") {
		t.Fatalf("invocation = %#v", processes.spec)
	}
	if _, err := os.Stat(filepath.Join(runDir, FormatterName)); err != nil {
		t.Fatalf("formatter was not stored in run directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workdir, FormatterName)); !os.IsNotExist(err) {
		t.Fatalf("formatter was written into workspace: %v", err)
	}
}

func TestRunnerRejectsFormatterDirectoryInsideWorkspace(t *testing.T) {
	workdir := t.TempDir()
	processes := &processStub{result: contract.ProcessResult{ExitStatus: intPointer(0)}}
	runner := &Runner{Processes: processes, Trees: stableTree{}}
	_, err := runner.Run(context.Background(), Request{
		Slug: "greet", Workdir: workdir, RunDir: filepath.Join(workdir, "run"),
		Environment: process.Environment{"PATH": "/bin"},
	})
	if !errors.Is(err, ErrRunDirInWorkspace) {
		t.Fatalf("Runner error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(workdir, "run", FormatterName)); !os.IsNotExist(err) {
		t.Fatalf("formatter was written into workspace: %v", err)
	}
	if processes.spec.Executable != "" {
		t.Fatal("process ran despite the workspace-local run directory")
	}
}

type processStub struct {
	result contract.ProcessResult
	log    []byte
	spec   contract.ProcessSpec
}

func (p *processStub) Run(_ context.Context, spec contract.ProcessSpec) (contract.ProcessResult, error) {
	p.spec = spec
	for _, entry := range spec.Env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key == "KOGEN_LEDGER_REPORT" {
			if err := os.WriteFile(value, nil, 0o600); err != nil {
				return p.result, err
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(spec.LogPath), 0o700); err != nil {
		return p.result, err
	}
	if err := os.WriteFile(spec.LogPath, p.log, 0o600); err != nil {
		return p.result, err
	}
	p.result.LogPath = spec.LogPath
	return p.result, nil
}

type stableTree struct{}

func (stableTree) Snapshot(context.Context, string) (string, error) { return "same", nil }

func containsFailure(failures []acceptance.Failure, kind acceptance.FailureKind) bool {
	for _, failure := range failures {
		if failure.Kind == kind {
			return true
		}
	}
	return false
}

func intPointer(value int) *int { return &value }
