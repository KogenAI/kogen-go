package command

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"kogen-go/internal/acceptance"
	"kogen-go/internal/contract"
	"kogen-go/internal/process"
)

type processStub struct {
	result       contract.ProcessResult
	report       []byte
	writeReport  bool
	request      contract.ProcessSpec
	beforeReturn func(contract.ProcessSpec) error
}

func (p *processStub) Run(_ context.Context, spec contract.ProcessSpec) (contract.ProcessResult, error) {
	p.request = spec
	if p.beforeReturn != nil {
		if err := p.beforeReturn(spec); err != nil {
			return p.result, err
		}
	}
	if p.writeReport {
		path := environmentValue(spec.Env, "KOGEN_LEDGER_REPORT")
		if err := os.WriteFile(path, p.report, 0o600); err != nil {
			return p.result, err
		}
	}
	return p.result, nil
}

type treeStub struct {
	values []string
	index  int
}

func (t *treeStub) Snapshot(context.Context, string) (string, error) {
	if t.index >= len(t.values) {
		return "", errors.New("no tree snapshot scripted")
	}
	value := t.values[t.index]
	t.index++
	return value, nil
}

func TestConfigResolvesCommandAdapterPaths(t *testing.T) {
	config := Config{Extension: ".t.sh", CandidateDir: "test/acceptance", Run: []string{"sh", "run", "{path}"}}
	source, err := config.SourcePath("greet")
	if err != nil || source != ".kogen/acceptance/greet.t.sh" {
		t.Fatalf("source path = %q, %v", source, err)
	}
	candidate, err := config.CandidatePath("greet")
	if err != nil || candidate != "test/acceptance/greet.t.sh" {
		t.Fatalf("candidate path = %q, %v", candidate, err)
	}
	for _, invalid := range []Config{
		{Extension: "../escape", CandidateDir: "test/acceptance", Run: []string{"sh"}},
		{Extension: ".t.sh", CandidateDir: "../escape", Run: []string{"sh"}},
		{Extension: ".t.sh", CandidateDir: "test/acceptance", Run: []string{""}},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("invalid config accepted: %#v", invalid)
		}
	}
}

func TestRunnerSetsCommandEnvironmentAndAddsSuiteFailureForNonzeroAllGreen(t *testing.T) {
	workdir := t.TempDir()
	runDir := t.TempDir()
	process := &processStub{
		result:      contract.ProcessResult{ExitStatus: intPointer(1)},
		report:      []byte("{\"tag\":\"greet/A1\",\"test\":\"first\",\"status\":\"passed\"}\n{\"tag\":\"greet/A2\",\"test\":\"second\",\"status\":\"passed\"}\n"),
		writeReport: true,
	}
	runner := &Runner{Processes: process, Trees: &treeStub{values: []string{"tree", "tree"}}}
	result, err := runner.Run(context.Background(), Request{
		Config: Config{Extension: ".t.sh", CandidateDir: "test/acceptance", Run: []string{"sh", "test {path}", "{path}"}, Timeout: 4 * time.Second},
		Slug:   "greet", Workdir: workdir, RunDir: runDir,
		Environment:   processEnv("KOGEN_LEDGER_REPORT", "wrong", "KOGEN_INTENT_SLUG", "wrong", "PATH", "/bin"),
		ExpectedItems: []string{"A1", "A2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ItemPass["A1"] || !result.ItemPass["A2"] || !hasFailure(result, acceptance.FailureSuite) {
		t.Fatalf("result = %#v", result)
	}
	if process.request.Executable != "sh" || !reflect.DeepEqual(process.request.Args, []string{"test " + filepath.Join(workdir, "test/acceptance/greet.t.sh"), filepath.Join(workdir, "test/acceptance/greet.t.sh")}) {
		t.Fatalf("command argv = %q %q", process.request.Executable, process.request.Args)
	}
	if process.request.Timeout != 4*time.Second || process.request.Dir != workdir {
		t.Fatalf("process execution context = %#v", process.request)
	}
	if got := environmentValue(process.request.Env, "KOGEN_LEDGER_REPORT"); got != filepath.Join(runDir, "reports", "ledger.jsonl") {
		t.Fatalf("ledger report environment = %q", got)
	}
	if got := environmentValue(process.request.Env, "KOGEN_INTENT_SLUG"); got != "greet" {
		t.Fatalf("slug environment = %q", got)
	}
	if got := environmentValue(process.request.Env, "PATH"); got != "/bin" {
		t.Fatalf("child PATH = %q", got)
	}
	if _, err := os.Stat(process.request.LogPath); !os.IsNotExist(err) {
		t.Fatalf("stub unexpectedly created process log: %v", err)
	}
}

func TestRunnerReportsMutationTimeoutAndEmptyUnavailableResults(t *testing.T) {
	t.Run("tree mutation and timeout", func(t *testing.T) {
		workdir, runDir := t.TempDir(), t.TempDir()
		process := &processStub{result: contract.ProcessResult{ExitStatus: intPointer(124), TimedOut: true}, report: []byte("{\"tag\":\"greet/A1\",\"test\":\"first\",\"status\":\"passed\"}\n"), writeReport: true}
		runner := &Runner{Processes: process, Trees: &treeStub{values: []string{"before", "after"}}}
		result, err := runner.Run(context.Background(), baseRequest(workdir, runDir))
		if err != nil {
			t.Fatal(err)
		}
		if !hasFailure(result, acceptance.FailureAcceptanceTimeout) || !hasFailure(result, acceptance.FailureTreeMutated) {
			t.Fatalf("failures = %#v", result.Failures)
		}
	})

	t.Run("empty missing runner report", func(t *testing.T) {
		workdir, runDir := t.TempDir(), t.TempDir()
		process := &processStub{result: contract.ProcessResult{ExitStatus: intPointer(127), Unavailable: true}}
		runner := &Runner{Processes: process, Trees: &treeStub{values: []string{"same", "same"}}}
		result, err := runner.Run(context.Background(), baseRequest(workdir, runDir))
		if err != nil {
			t.Fatal(err)
		}
		if !hasFailure(result, acceptance.FailureToolMissing) || hasFailure(result, acceptance.FailureNoTaggedTests) {
			t.Fatalf("failures = %#v", result.Failures)
		}
	})

	t.Run("empty nonzero report is compile failure", func(t *testing.T) {
		workdir, runDir := t.TempDir(), t.TempDir()
		process := &processStub{result: contract.ProcessResult{ExitStatus: intPointer(2)}, report: []byte{}, writeReport: true}
		runner := &Runner{Processes: process, Trees: &treeStub{values: []string{"same", "same"}}}
		result, err := runner.Run(context.Background(), baseRequest(workdir, runDir))
		if err != nil {
			t.Fatal(err)
		}
		if !hasFailure(result, acceptance.FailureAcceptanceCompileFailed) {
			t.Fatalf("failures = %#v", result.Failures)
		}
	})
}

func TestRunnerRejectsReportEscapeBeforeInvokingProcess(t *testing.T) {
	workdir, runDir := t.TempDir(), t.TempDir()
	process := &processStub{result: contract.ProcessResult{ExitStatus: intPointer(0)}}
	runner := &Runner{Processes: process, Trees: &treeStub{values: []string{"tree", "tree"}}}
	request := baseRequest(workdir, runDir)
	request.ReportPath = filepath.Join(filepath.Dir(runDir), "outside.jsonl")
	if _, err := runner.Run(context.Background(), request); err == nil || !strings.Contains(err.Error(), "under run_dir") {
		t.Fatalf("report escape error = %v", err)
	}
	if process.request.Executable != "" {
		t.Fatal("process ran before report path validation")
	}
}

func baseRequest(workdir, runDir string) Request {
	return Request{
		Config:        Config{Extension: ".t.sh", CandidateDir: "test/acceptance", Run: []string{"sh", "{path}"}},
		Slug:          "greet",
		Workdir:       workdir,
		RunDir:        runDir,
		Environment:   process.Environment{"PATH": "/bin"},
		ExpectedItems: []string{"A1"},
	}
}

func processEnv(pairs ...string) process.Environment {
	environment := make(process.Environment, len(pairs)/2)
	for index := 0; index+1 < len(pairs); index += 2 {
		environment[pairs[index]] = pairs[index+1]
	}
	return environment
}

func environmentValue(environment []string, key string) string {
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if found && name == key {
			return value
		}
	}
	return ""
}

func hasFailure(result acceptance.Result, kind acceptance.FailureKind) bool {
	for _, failure := range result.Failures {
		if failure.Kind == kind {
			return true
		}
	}
	return false
}

func intPointer(value int) *int { return &value }
