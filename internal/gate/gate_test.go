package gate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kogen-go/internal/acceptance"
	acceptancecommand "kogen-go/internal/acceptance/command"
	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/process"
	"kogen-go/internal/safefs"
	"kogen-go/internal/testkit"
)

func TestRunAppliesFixesBeforeChecksAndReceiptBindsPostFixTree(t *testing.T) {
	fixture := newGateFixture(t)
	var order []string
	processes := &processStub{run: func(spec contract.ProcessSpec) (int, bool, string) {
		order = append(order, filepath.Base(spec.Dir)+":"+spec.Executable)
		if spec.Executable == "format" {
			writeFixtureFile(t, filepath.Join(spec.Dir, "lib/fixed.txt"), []byte("fixed\n"), 0o644)
		}
		return 0, false, ""
	}}
	acceptanceRunner := &acceptanceStub{results: []acceptance.Result{acceptanceResult(t, fixture.candidate, map[string]bool{"A1": true}, nil, 0)}}
	request := fixture.request(processes, acceptanceRunner)
	request.Fixes = []contract.CheckSpec{checkSpec("format")}
	request.Checks = []contract.CheckSpec{checkSpec("lint")}

	report, err := Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict() != VerdictGreen || !report.IsVerified() || !report.IsLandable() {
		t.Fatalf("gate result = %s verified=%t landable=%t", report.Verdict(), report.IsVerified(), report.IsLandable())
	}
	if want := []string{"candidate:format", "base:lint", "candidate:lint"}; !equalStrings(order, want) {
		t.Fatalf("process order = %v, want %v", order, want)
	}
	receipt, ok := report.Receipt()
	if !ok {
		t.Fatal("green gate did not return a receipt")
	}
	currentTree, err := fixture.trees.Snapshot(context.Background(), fixture.candidate)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.CandidateTree != currentTree {
		t.Fatalf("receipt tree = %s, final tree = %s", receipt.CandidateTree, currentTree)
	}
	if _, err := os.Stat(filepath.Join(fixture.candidate, "lib/fixed.txt")); err != nil {
		t.Fatalf("fix output missing: %v", err)
	}
	beforeCounts := report.Counts()
	report.RecordAuditAdvice([]AuditAdvice{{ID: "A999", Verdict: "contradicts", Reason: "advice only"}})
	afterCounts := report.Counts()
	receiptAfterAdvice, ok := report.Receipt()
	if !ok || receiptAfterAdvice.CandidateTree != receipt.CandidateTree || beforeCounts != afterCounts || !report.IsLandable() {
		t.Fatal("auditor advice changed the receipt, eligibility, or acceptance counts")
	}
	if len(report.AuditAdvice()) != 1 {
		t.Fatal("observational advice was not retained")
	}
}

func TestWorkspaceSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writeFixtureFile(t, filepath.Join(dir, "one/two.txt"), []byte("first\n"), 0o644)
	root, err := safefs.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	snapshot, err := captureWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Remove("one/two.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "one/extra.txt"), []byte("extra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	current, err := captureWorkspaceAt(safefs.Opener{}, dir, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Restore(root, current); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "one/two.txt")); err != nil || string(got) != "first\n" {
		t.Fatalf("restored file=%q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "one/extra.txt")); !os.IsNotExist(err) {
		t.Fatalf("extra file survived restore: %v", err)
	}
}

func TestBaseRelativeMutatingCheckIsRolledBackAndCanBeExcused(t *testing.T) {
	fixture := newGateFixture(t)
	processes := &processStub{run: func(spec contract.ProcessSpec) (int, bool, string) {
		if spec.Executable == "touchy" {
			writeFixtureFile(t, filepath.Join(spec.Dir, "lib/transient.txt"), []byte("noise\n"), 0o644)
		}
		return 0, false, ""
	}}
	acceptanceRunner := &acceptanceStub{results: []acceptance.Result{acceptanceResult(t, fixture.candidate, map[string]bool{"A1": true}, nil, 0)}}
	request := fixture.request(processes, acceptanceRunner)
	request.Checks = []contract.CheckSpec{checkSpec("touchy")}

	report, err := Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	checks := report.Checks()
	if len(checks) != 1 || checks[0].Status != contract.CheckMutating || !checks[0].Excused {
		t.Fatalf("mutating check observation = %+v", checks)
	}
	if report.Verdict() != VerdictGreen || !report.IsLandable() {
		t.Fatalf("same base mutation was not excused: %s", report.Verdict())
	}
	for _, workspace := range []string{fixture.base, fixture.candidate} {
		if _, err := os.Stat(filepath.Join(workspace, "lib/transient.txt")); !os.IsNotExist(err) {
			t.Fatalf("mutating check was not rolled back in %s: %v", workspace, err)
		}
	}
	if got := checks[0].ChangedPaths; !containsString(got, "lib/transient.txt") {
		t.Fatalf("changed path was not retained in evidence: %v", got)
	}
}

func TestNewFindingIsNotExcusedAndAuditCannotChangeGate(t *testing.T) {
	fixture := newGateFixture(t)
	processes := &processStub{run: func(spec contract.ProcessSpec) (int, bool, string) {
		if spec.Dir == fixture.base {
			return 2, false, "lib/old.txt:1:1: error: [lint/todo] old.txt: TODO found\n"
		}
		return 2, false, "lib/new.txt:1:1: error: [lint/todo] new.txt: TODO found\n"
	}}
	acceptanceRunner := &acceptanceStub{results: []acceptance.Result{acceptanceResult(t, fixture.candidate, map[string]bool{"A1": true}, nil, 0)}}
	request := fixture.request(processes, acceptanceRunner)
	request.Checks = []contract.CheckSpec{checkSpec("lint")}
	request.Baseline = &CheckBaseline{Tree: fixture.baseTree, Checks: []BaselineCheck{{
		Name: "lint", Status: contract.CheckRed, ExitStatus: intPointer(2),
		Findings: []contract.FindingIdentity{{Path: "lib/old.txt", Rule: "lint/todo"}},
	}}}

	report, err := Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict() != VerdictUnverified || report.IsVerified() || report.IsLandable() {
		t.Fatalf("new base-relative identity was excused: %s", report.Verdict())
	}
	checks := report.Checks()
	if len(checks) != 1 || checks[0].Excused || len(checks[0].Findings) != 1 || checks[0].Findings[0].Symbol != "" {
		t.Fatalf("candidate finding identity = %+v", checks)
	}
	counts := report.Counts()
	report.RecordAuditAdvice([]AuditAdvice{{ID: "A1", Verdict: "over_strict", Reason: "suggestion"}, {ID: "A1", Verdict: "contradicts"}})
	if report.Verdict() != VerdictUnverified || report.IsVerified() || report.IsLandable() || report.Counts() != counts {
		t.Fatal("auditor advice changed red eligibility or counts")
	}
	if got := report.Feedback(); !strings.Contains(got, "gate: 1 errors, 0 warnings") || !strings.Contains(got, "lib/new.txt:1:1: error: [lint/todo] new.txt: TODO found") {
		t.Fatalf("unexpected gate feedback:\n%s", got)
	}
}

func TestFlakeEvidenceUsesOneSeedCapsExcusesAndPersistsBeforeEligibility(t *testing.T) {
	fixture := newGateFixture(t)
	processes := &processStub{run: func(spec contract.ProcessSpec) (int, bool, string) { return 0, false, "" }}
	acceptanceRunner := &acceptanceStub{results: []acceptance.Result{
		acceptanceResult(t, fixture.candidate, map[string]bool{"A1": false, "A2": false, "A3": false}, nil, 1),
		acceptanceResult(t, fixture.candidate, map[string]bool{"A1": true, "A2": true, "A3": true}, nil, 0),
		acceptanceResult(t, fixture.base, map[string]bool{"A1": false, "A2": false, "A3": false}, nil, 1),
	}}
	request := fixture.request(processes, acceptanceRunner)
	request.Acceptance.Request.ExpectedItems = []string{"A1", "A2", "A3"}
	request.Acceptance.ChangeItems = []string{"A1"}

	report, err := Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict() != VerdictUnverified || report.IsVerified() {
		t.Fatalf("more than two base-red failures were excused: %s", report.Verdict())
	}
	if counts := report.Counts(); counts != (AcceptanceCounts{Passed: 2, Total: 3}) {
		t.Fatalf("effective acceptance count = %+v", counts)
	}
	if len(acceptanceRunner.executions) != 3 {
		t.Fatalf("acceptance attempts = %d, want initial, retry, base", len(acceptanceRunner.executions))
	}
	seed := acceptanceRunner.executions[0].Seed
	if seed == "" || acceptanceRunner.executions[1].Seed != seed || acceptanceRunner.executions[2].Seed != seed {
		t.Fatalf("retry/base did not reuse initial seed: %+v", acceptanceRunner.executions)
	}
	flake, ok := report.Flake()
	if !ok || !flake.Persisted || !equalStrings(flake.ExcusedIDs, []string{"A1", "A2"}) {
		t.Fatalf("flake evidence = %+v, ok=%t", flake, ok)
	}
	data, err := os.ReadFile(filepath.Join(fixture.runDir, "flake-evidence.json"))
	if err != nil {
		t.Fatalf("flake evidence was not durably published: %v", err)
	}
	var persisted FlakeEvidence
	if err := json.Unmarshal(data, &persisted); err != nil || persisted.Seed != seed || !equalStrings(persisted.ExcusedIDs, []string{"A1", "A2"}) {
		t.Fatalf("stored flake evidence = %+v, error=%v", persisted, err)
	}
}

func TestSuiteFailureNeverCreatesReceiptOrRunsFlakeRetry(t *testing.T) {
	fixture := newGateFixture(t)
	processes := &processStub{run: func(spec contract.ProcessSpec) (int, bool, string) { return 0, false, "" }}
	acceptanceRunner := &acceptanceStub{results: []acceptance.Result{
		acceptanceResult(t, fixture.candidate, map[string]bool{"A1": true}, []acceptance.Failure{{Kind: acceptance.FailureSuite}}, 0),
	}}
	request := fixture.request(processes, acceptanceRunner)
	report, err := Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict() != VerdictUnverified || report.IsVerified() || len(acceptanceRunner.executions) != 1 {
		t.Fatalf("suite failure gate = %s, receipt=%t, attempts=%d", report.Verdict(), report.IsVerified(), len(acceptanceRunner.executions))
	}
}

func TestFlakeEvidencePublicationFailureLeavesBaseRedItemsBlocking(t *testing.T) {
	fixture := newGateFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.runDir, "flake-evidence.json"), []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	processes := &processStub{run: func(spec contract.ProcessSpec) (int, bool, string) { return 0, false, "" }}
	acceptanceRunner := &acceptanceStub{results: []acceptance.Result{
		acceptanceResult(t, fixture.candidate, map[string]bool{"A1": false}, nil, 1),
		acceptanceResult(t, fixture.candidate, map[string]bool{"A1": true}, nil, 0),
		acceptanceResult(t, fixture.base, map[string]bool{"A1": false}, nil, 1),
	}}
	request := fixture.request(processes, acceptanceRunner)
	report, err := Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	flake, ok := report.Flake()
	if !ok || flake.Persisted || flake.StoreError == "" || report.Counts().Passed != 0 || report.IsVerified() {
		t.Fatalf("failed evidence publication changed eligibility: flake=%+v counts=%+v", flake, report.Counts())
	}
}

type gateFixture struct {
	t         *testing.T
	base      string
	candidate string
	runDir    string
	baseTree  string
	git       *gitio.Runner
	policy    contract.GitPolicy
	trees     acceptance.CandidateTree
}

func newGateFixture(t *testing.T) *gateFixture {
	t.Helper()
	gitFixture := testkit.NewGitFixture(t)
	approved := []byte("approved acceptance test\n")
	writeFixtureFile(t, filepath.Join(gitFixture.Checkout, ".kogen/acceptance/greet.sh"), approved, 0o644)
	writeFixtureFile(t, filepath.Join(gitFixture.Checkout, ".kogen/intents/greet/intent.md"), []byte("approved intent\n"), 0o644)
	writeFixtureFile(t, filepath.Join(gitFixture.Checkout, "lib/app.txt"), []byte("base\n"), 0o644)
	gitFixture.Run(t, "add", ".")
	gitFixture.Run(t, "commit", "--quiet", "--message", "add gate fixture")
	gitFixture.Run(t, "push", "--quiet", "origin", "main")
	base := filepath.Join(gitFixture.Root, "base")
	candidate := filepath.Join(gitFixture.Root, "candidate")
	gitFixture.RunIn(t, gitFixture.Root, "clone", "--quiet", gitFixture.Origin, base)
	gitFixture.RunIn(t, gitFixture.Root, "clone", "--quiet", gitFixture.Origin, candidate)

	git := gitio.NewWorkspace(process.Supervisor{})
	env := environmentMap(gitFixture.Environment())
	policy := gitio.WorkspacePolicy(candidate, env)
	commit := strings.TrimSpace(string(gitFixture.RunIn(t, candidate, "rev-parse", "HEAD")))
	metadata, err := gitio.LoadBaseMetadata(context.Background(), git, policy, contract.ObjectID(commit))
	if err != nil {
		t.Fatalf("load immutable gate base: %v", err)
	}
	trees := acceptance.CandidateTree{Git: git, Policy: policy, Base: metadata}
	actualBaseTree, err := trees.Snapshot(context.Background(), base)
	if err != nil {
		t.Fatalf("snapshot immutable gate base: %v", err)
	}
	runDir := filepath.Join(gitFixture.Root, "run")
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return &gateFixture{t: t, base: base, candidate: candidate, runDir: runDir, baseTree: actualBaseTree, git: git, policy: policy, trees: trees}
}

func (f *gateFixture) request(processes contract.ProcessRunner, runner AcceptanceRunner) Request {
	return Request{
		Processes: processes, AcceptanceRunner: runner, Trees: f.trees, BaseWorkspace: f.base,
		CandidateWorkspace: f.candidate, RunDir: f.runDir, ExpectedBaseTree: f.baseTree,
		ApprovalSHA256: strings.Repeat("a", 64), HomeDir: filepath.Dir(f.runDir), TempDir: filepath.Dir(f.runDir),
		Acceptance: AcceptancePlan{Request: acceptancecommand.Request{
			Config: acceptancecommand.Config{Extension: ".sh", CandidateDir: "test/acceptance", Run: []string{"sh", "{path}"}, Timeout: time.Second},
			Slug:   "greet", ExpectedItems: []string{"A1"},
		}, ApprovedBytes: []byte("approved acceptance test\n"), ChangeItems: []string{"A1"}},
	}
}

type processStub struct {
	mu    sync.Mutex
	calls []contract.ProcessSpec
	run   func(contract.ProcessSpec) (int, bool, string)
}

func (s *processStub) Run(_ context.Context, spec contract.ProcessSpec) (contract.ProcessResult, error) {
	s.mu.Lock()
	s.calls = append(s.calls, spec)
	s.mu.Unlock()
	exit, unavailable, output := s.run(spec)
	if err := os.MkdirAll(filepath.Dir(spec.LogPath), 0o700); err != nil {
		return contract.ProcessResult{}, err
	}
	if err := os.WriteFile(spec.LogPath, []byte(output), 0o600); err != nil {
		return contract.ProcessResult{}, err
	}
	return contract.ProcessResult{ExitStatus: intPointer(exit), Unavailable: unavailable, OutputTail: []byte(output), LogPath: spec.LogPath}, nil
}

type acceptanceStub struct {
	mu         sync.Mutex
	results    []acceptance.Result
	executions []AcceptanceExecution
}

func (s *acceptanceStub) Run(_ context.Context, execution AcceptanceExecution) (acceptance.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executions = append(s.executions, execution)
	if len(s.results) == 0 {
		return acceptance.Result{}, os.ErrInvalid
	}
	result := cloneAcceptance(s.results[0])
	s.results = s.results[1:]
	return result, nil
}

func acceptanceResult(t *testing.T, workdir string, itemPass map[string]bool, failures []acceptance.Failure, exit int) acceptance.Result {
	t.Helper()
	return acceptance.Result{
		Process:  contract.ProcessResult{ExitStatus: intPointer(exit), LogPath: filepath.Join(workdir, "acceptance.log")},
		ItemPass: cloneBoolMap(itemPass), Failures: append([]acceptance.Failure(nil), failures...),
	}
}

func checkSpec(name string) contract.CheckSpec {
	return contract.CheckSpec{Name: name, Program: name, Env: []string{"PATH=" + os.Getenv("PATH")}, Timeout: 5 * time.Second}
}

func writeFixtureFile(t *testing.T, name string, contents []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, contents, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, mode); err != nil {
		t.Fatal(err)
	}
}

func environmentMap(entries []string) process.Environment {
	result := make(process.Environment, len(entries))
	for _, entry := range entries {
		key, value, found := strings.Cut(entry, "=")
		if found {
			result[key] = value
		}
	}
	return result
}

func intPointer(value int) *int { return &value }

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func containsString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

var _ contract.ProcessRunner = (*processStub)(nil)
var _ AcceptanceRunner = (*acceptanceStub)(nil)
