package diagnostic

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"kogen-go/internal/acceptance"
	acceptancecommand "kogen-go/internal/acceptance/command"
	"kogen-go/internal/approval/prepare"
	"kogen-go/internal/auth/accounts"
	"kogen-go/internal/auth/vault"
	"kogen-go/internal/build/ladder"
	"kogen-go/internal/build/recipe"
	"kogen-go/internal/build/repair"
	buildselect "kogen-go/internal/build/select"
	"kogen-go/internal/contract"
	"kogen-go/internal/gate"
	"kogen-go/internal/gitio"
	"kogen-go/internal/process"
	"kogen-go/internal/setupcache"
	"kogen-go/internal/testkit"
)

func TestCompareReportsEveryDivergenceAndCannotCountTowardG(t *testing.T) {
	report, err := Compare(Accounts,
		json.RawMessage(`{"a":1,"gone":{"x":true},"nested":[1,2],"same":null}`),
		json.RawMessage(`{"a":2,"extra":"x","nested":[1,3,4],"same":null}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Class != "D" || report.CountsTowardG || report.Match || len(report.Divergences) != 5 {
		t.Fatalf("comparison = %#v", report)
	}
	wantPaths := []string{"/a", "/extra", "/gone", "/nested/1", "/nested/2"}
	for index, want := range wantPaths {
		if report.Divergences[index].Path != want {
			t.Fatalf("divergence paths = %#v, want %#v", report.Divergences, wantPaths)
		}
	}
	if report.Divergences[1].ExpectedPresent || !report.Divergences[1].ActualPresent {
		t.Fatalf("additional field presence = %#v", report.Divergences[1])
	}
	if !report.Divergences[2].ExpectedPresent || report.Divergences[2].ActualPresent {
		t.Fatalf("missing field presence = %#v", report.Divergences[2])
	}
}

func TestFullObservationValidationRejectsUnknownAndProjectedSlices(t *testing.T) {
	if _, err := Compare("resilience", json.RawMessage(`{}`), json.RawMessage(`{}`)); !errors.Is(err, ErrUnknownSlice) {
		t.Fatalf("unknown slice error = %v", err)
	}
	if _, err := MarshalObservation(Gate, map[string]any{"last": "no_seam"}); err == nil {
		t.Fatal("projected observation was accepted")
	}
	if _, err := MarshalObservation(Gate, []string{"not", "an", "object"}); err == nil {
		t.Fatal("non-object observation was accepted")
	}
}

func TestObserveGateReadsProductionReportAndRetainsObservationalAdvice(t *testing.T) {
	report := runGateFixture(t)
	before := report.Counts()
	report.RecordAuditAdvice([]gate.AuditAdvice{{ID: "A1", Verdict: "contradicts", Reason: "diagnostic advice"}})
	observation, err := ObserveGate(report)
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts() != before || observation.Counts != (GateCounts{Passed: before.Passed, Total: before.Total}) {
		t.Fatalf("audit changed or obscured the production count: before=%+v observation=%+v after=%+v", before, observation.Counts, report.Counts())
	}
	if observation.Verdict != gate.VerdictGreen || !observation.Verified || !observation.Landable || observation.Receipt == nil {
		t.Fatalf("gate observation lost the production receipt: %#v", observation)
	}
	if len(observation.AuditAdvice) != 1 || observation.AuditAdvice[0].Verdict != "contradicts" {
		t.Fatalf("gate advice was not observed: %#v", observation.AuditAdvice)
	}
	encoded, err := MarshalObservation(Gate, observation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Compare(Gate, encoded, encoded); err != nil {
		t.Fatal(err)
	}
}

func TestObserveOrchestrationMapsOutcomeAndProductionSelection(t *testing.T) {
	parsed, err := recipe.Parse("ladder")
	if err != nil {
		t.Fatal(err)
	}
	outcome := ladder.Outcome{
		Status: ladder.StatusFailed, Reason: "best_candidate",
		Build: recipe.Build{Recipe: parsed},
		Candidates: []buildselect.Candidate{{
			Rung: "R1", Rank: 1, AttemptOrder: 1, Diff: []byte("+candidate\n"),
			SnapshotReason: repair.ReasonRepairCap,
		}},
	}
	selected, err := buildselect.Select(outcome.Candidates)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := ObserveOrchestration(outcome, &selected)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Status != outcome.Status || observation.Reason != outcome.Reason || observation.Selection == nil {
		t.Fatalf("orchestration observation = %#v", observation)
	}
	if observation.Selection.AuditMode != buildselect.AuditModeObservational || observation.Selection.Demoted || len(observation.Selection.AdvisoryItems) != 0 {
		t.Fatalf("production selector observation is not observational: %#v", observation.Selection)
	}
	if len(observation.Candidates) != 1 || string(observation.Candidates[0].Diff) != "+candidate\n" {
		t.Fatalf("candidate observation = %#v", observation.Candidates)
	}
}

func TestObserveAccountsUsesProductionStoreResolverAndList(t *testing.T) {
	store, err := accounts.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Write(accounts.File{
		ChatGPT:   accounts.ProviderAccounts{Default: "work"},
		Selection: accounts.Selection{Default: accounts.ChatGPT},
	}); err != nil {
		t.Fatal(err)
	}
	email := "fixture@example.invalid"
	profiles := vault.Profiles{ChatGPT: map[string]vault.ChatGPTProfile{
		"work": {Email: &email, SignedIn: true, ExpiresAt: 1735689600},
	}}
	observation, err := ObserveAccounts(store, profiles, accounts.ResolveInput{})
	if err != nil {
		t.Fatal(err)
	}
	if observation.Resolved == nil || observation.Resolved.Provider != accounts.ChatGPT || observation.Resolved.Label != "work" || observation.Resolved.AccountSource != "default" {
		t.Fatalf("resolved account = %#v", observation.Resolved)
	}
	if !strings.Contains(observation.ListOutput, "chatgpt:work (default) signed in fixture@example.invalid") {
		t.Fatalf("list output = %q", observation.ListOutput)
	}
	if strings.Contains(observation.AccountsYAML, "access_token") || strings.Contains(observation.AccountsYAML, "fixture-secret") {
		t.Fatal("account observation contains credential material")
	}
}

func TestObserveSetupCacheKeepsSetupAndBaselineResultsIndependent(t *testing.T) {
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	if err := os.Mkdir(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	cache, err := setupcache.Open(cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	workspace := t.TempDir()
	baseTree := strings.Repeat("a", 40)
	request := prepare.SetupRequest{
		BaseTree: baseTree, Workspace: workspace, OS: runtime.GOOS, Arch: runtime.GOARCH,
		Toolchain: map[string]string{"go": "1.27.1"}, ToolchainKnown: true,
		Outputs: []string{"result.txt"},
	}
	firstSetup, err := cache.Run(context.Background(), request, func(context.Context) error {
		return os.WriteFile(filepath.Join(workspace, "result.txt"), []byte("prepared"), 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	secondSetup, err := cache.Run(context.Background(), request, func(context.Context) error {
		return errors.New("setup callback ran despite a cache hit")
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := prepare.NewBaselineKey(baseTree, firstSetup.Key, nil, process.Environment{}, request.Toolchain, true, runtime.GOOS, runtime.GOARCH, "diagnostic-test")
	if err != nil {
		t.Fatal(err)
	}
	computeCalls := 0
	baseline, err := cache.GetOrCompute(context.Background(), key, func(context.Context) ([]prepare.BaselineRow, error) {
		computeCalls++
		return []prepare.BaselineRow{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	baselineHit, err := cache.GetOrCompute(context.Background(), key, func(context.Context) ([]prepare.BaselineRow, error) {
		computeCalls++
		return []prepare.BaselineRow{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	changedTreeKey, err := prepare.NewBaselineKey(strings.Repeat("b", 40), firstSetup.Key, nil, process.Environment{}, request.Toolchain, true, runtime.GOOS, runtime.GOARCH, "diagnostic-test")
	if err != nil {
		t.Fatal(err)
	}
	changedTree, err := cache.GetOrCompute(context.Background(), changedTreeKey, func(context.Context) ([]prepare.BaselineRow, error) {
		computeCalls++
		return []prepare.BaselineRow{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if firstSetup.Reused || !secondSetup.Reused || baseline.Reused || !baselineHit.Reused || changedTree.Reused || computeCalls != 2 {
		t.Fatalf("setup/baseline effects: first=%+v second=%+v initial=%+v hit=%+v changed-tree=%+v calls=%d", firstSetup, secondSetup, baseline, baselineHit, changedTree, computeCalls)
	}
	observation := ObserveSetupCache(secondSetup, changedTreeKey, changedTree)
	if !observation.SetupReused || observation.BaselineReused || observation.CheckedBaseTree != strings.Repeat("b", 40) {
		t.Fatalf("independent setup and baseline observation = %#v", observation)
	}
}

type testProcessRunner struct{}

func (testProcessRunner) Run(context.Context, contract.ProcessSpec) (contract.ProcessResult, error) {
	status := 0
	return contract.ProcessResult{ExitStatus: &status}, nil
}

type testAcceptanceRunner struct{ result acceptance.Result }

func (r testAcceptanceRunner) Run(context.Context, gate.AcceptanceExecution) (acceptance.Result, error) {
	return r.result, nil
}

func runGateFixture(t *testing.T) *gate.GateReport {
	t.Helper()
	fixture := testkit.NewGitFixture(t)
	writeFixture(t, filepath.Join(fixture.Checkout, ".kogen/acceptance/greet.sh"), []byte("approved acceptance test\n"), 0o644)
	writeFixture(t, filepath.Join(fixture.Checkout, ".kogen/intents/greet/intent.md"), []byte("approved intent\n"), 0o644)
	writeFixture(t, filepath.Join(fixture.Checkout, "lib/app.txt"), []byte("base\n"), 0o644)
	fixture.Run(t, "add", ".")
	fixture.Run(t, "commit", "--quiet", "--message", "gate fixture")
	fixture.Run(t, "push", "--quiet", "origin", "main")
	base := filepath.Join(fixture.Root, "base")
	candidate := filepath.Join(fixture.Root, "candidate")
	fixture.RunIn(t, fixture.Root, "clone", "--quiet", fixture.Origin, base)
	fixture.RunIn(t, fixture.Root, "clone", "--quiet", fixture.Origin, candidate)
	git := gitio.NewWorkspace(process.Supervisor{})
	env := make(process.Environment)
	for _, item := range fixture.Environment() {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	policy := gitio.WorkspacePolicy(candidate, env)
	commit := strings.TrimSpace(string(fixture.RunIn(t, candidate, "rev-parse", "HEAD")))
	metadata, err := gitio.LoadBaseMetadata(context.Background(), git, policy, contract.ObjectID(commit))
	if err != nil {
		t.Fatal(err)
	}
	trees := acceptance.CandidateTree{Git: git, Policy: policy, Base: metadata}
	baseTree, err := trees.Snapshot(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(fixture.Root, "run")
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	status := 0
	request := gate.Request{
		Processes: testProcessRunner{}, AcceptanceRunner: testAcceptanceRunner{result: acceptance.Result{
			Process: contract.ProcessResult{ExitStatus: &status}, ItemPass: map[string]bool{"A1": true},
		}},
		Trees: trees, BaseWorkspace: base, CandidateWorkspace: candidate, RunDir: runDir,
		ExpectedBaseTree: baseTree, ApprovalSHA256: strings.Repeat("a", 64),
		HomeDir: filepath.Dir(runDir), TempDir: filepath.Dir(runDir),
		Acceptance: gate.AcceptancePlan{
			Request: acceptancecommand.Request{
				Config: acceptancecommand.Config{Extension: ".sh", CandidateDir: "test/acceptance", Run: []string{"sh", "{path}"}, Timeout: time.Second},
				Slug:   "greet", ExpectedItems: []string{"A1"},
			},
			ApprovedBytes: []byte("approved acceptance test\n"), ChangeItems: []string{"A1"},
		},
	}
	report, err := gate.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func writeFixture(t *testing.T, path string, contents []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
