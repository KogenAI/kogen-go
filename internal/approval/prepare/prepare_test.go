package prepare

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/intent"
	"kogen-go/internal/process"
	"kogen-go/internal/project"
	"kogen-go/internal/protection"
	"kogen-go/internal/safefs"
	"kogen-go/internal/yamlmini"
)

func TestPrepareHashMismatchStopsBeforeAnyOtherPort(t *testing.T) {
	root := t.TempDir()
	intentSource := []byte("---\ntitle: Greeting\nsize: small\ndomains: [app]\n---\nUpdate the greeting.\n\n## Acceptance\n- A1: update the greeting\n\n## Verify\n- A1: test\n")
	writeFile(t, root, ".kogen/intents/greeting/intent.md", intentSource)
	acceptanceSource := []byte("#!/bin/sh\nexit 0\n")
	writeFile(t, root, ".kogen/acceptance/greeting.t.sh", acceptanceSource)
	wrongPrefix := []byte(intent.ApprovalSHA256(intentSource, acceptanceSource)[:6])
	if wrongPrefix[0] == '0' {
		wrongPrefix[0] = '1'
	} else {
		wrongPrefix[0] = '0'
	}
	request := Request{
		Project: &project.Resolution{Checkout: root, Origin: root, Base: "main"},
		Slug:    "greeting", HashPrefix: string(wrongPrefix), RunDir: filepath.Join(root, "run"),
		AcceptanceSourcePath:    ".kogen/acceptance/greeting.t.sh",
		AcceptanceCandidatePath: "test/acceptance/greeting.t.sh",
	}
	_, err := Prepare(context.Background(), request, Dependencies{Roots: safefs.Opener{}})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Reason != "hash_mismatch" || failure.Exit != 1 {
		t.Fatalf("Prepare error = %v, want hash_mismatch exit 1", err)
	}
}

func TestBaselineKeyBindsExactTreeChecksAndEnvironmentWithoutLoggingValues(t *testing.T) {
	check := contract.CheckSpec{Name: "unit", Adapter: "command", Program: "go", Args: []string{"test", "./..."}, Timeout: 10 * time.Second}
	setup := strings.Repeat("a", 64)
	base := strings.Repeat("b", 40)
	first, err := NewBaselineKey(base, setup, []contract.CheckSpec{check}, process.Environment{"PATH": "/tool/bin", "SECRET": "do-not-log"}, map[string]string{"go": "go1.27.1"}, true, "darwin", "arm64", "adapter-1")
	if err != nil {
		t.Fatal(err)
	}
	if !first.Cacheable || first.Version != 3 {
		t.Fatalf("key = %#v, want cacheable v3 identity", first)
	}
	changedTree, err := NewBaselineKey(strings.Repeat("c", 40), setup, []contract.CheckSpec{check}, process.Environment{"PATH": "/tool/bin", "SECRET": "do-not-log"}, map[string]string{"go": "go1.27.1"}, true, "darwin", "arm64", "adapter-1")
	if err != nil {
		t.Fatal(err)
	}
	changedDeadline := check
	changedDeadline.Timeout += time.Millisecond
	changedChecks, err := NewBaselineKey(base, setup, []contract.CheckSpec{changedDeadline}, process.Environment{"PATH": "/tool/bin", "SECRET": "do-not-log"}, map[string]string{"go": "go1.27.1"}, true, "darwin", "arm64", "adapter-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest == changedTree.Digest || first.Digest == changedChecks.Digest {
		t.Fatal("baseline digest did not change with checked tree or check deadline")
	}
	if strings.Contains(first.String(), "do-not-log") || strings.Contains(first.GoString(), "/tool/bin") {
		t.Fatal("baseline identity diagnostic leaked environment values")
	}
	unknown, err := NewBaselineKey(base, setup, []contract.CheckSpec{check}, process.Environment{}, nil, false, "darwin", "arm64", "adapter-1")
	if err != nil {
		t.Fatal(err)
	}
	if unknown.Cacheable {
		t.Fatal("unknown toolchain identity must disable baseline reuse")
	}
}

func TestRenderCardKeepsVerifySemanticsAndOmitsRequest(t *testing.T) {
	source := []byte("---\ntitle: Greeting\nsize: small\ndomains: [app]\n---\nUpdate the greeting.\n\n## Acceptance\n- A1: update greeting output\n- A2: preserve the existing warning\n\n## Verify\n- A1: test\n- A2: test keep\n\n## Request\nprivate request body\n")
	parsed, err := intent.Parse("greeting", source)
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("a", 64)
	card := RenderCard(parsed, "main", strings.Repeat("b", 40), hash, "Kogen Test <test@kogen.invalid>", nil, nil)
	if !strings.Contains(card, "[A1] update greeting output (test)") || !strings.Contains(card, "[A2] preserve the existing warning (test keep)") {
		t.Fatalf("card lost Verify change/keep semantics: %s", card)
	}
	if strings.Contains(card, "private request body") {
		t.Fatal("card included raw Request content")
	}
	if !strings.HasSuffix(card, "kogen intent approve greeting aaaaaaaa\n") {
		t.Fatalf("card approval command missing: %s", card)
	}
}

func TestStageAndCheckRestoresCandidateAfterRedCheck(t *testing.T) {
	workspace := t.TempDir()
	root, err := safefs.OpenRoot(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	adapter := checkFunc(func(_ context.Context, workdir string, spec contract.CheckSpec) (contract.CheckResult, error) {
		candidate := filepath.Join(workdir, "test", "acceptance", "greeting.t.sh")
		data, err := os.ReadFile(candidate)
		if err != nil || string(data) != "approved bytes" {
			t.Fatalf("staged candidate = %q, err=%v", data, err)
		}
		exit := 1
		return contract.CheckResult{Name: spec.Name, Status: contract.CheckRed, ExitStatus: &exit}, nil
	})
	err = stageAndCheck(context.Background(), Dependencies{Checks: adapter}, root, workspace, "test/acceptance/greeting.t.sh", []byte("approved bytes"), []contract.CheckSpec{{Name: "syntax", Program: "sh", Timeout: time.Second}})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Reason != "acceptance_check_failed" {
		t.Fatalf("stageAndCheck error = %v, want acceptance_check_failed", err)
	}
	if _, err := root.Lstat("test/acceptance/greeting.t.sh"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged candidate remains after failure: %v", err)
	}
	if _, err := root.Lstat("test"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new empty parents remain after restore: %v", err)
	}
}

func TestPrepareRunsSetupBaselineAcceptanceOnResolvedBase(t *testing.T) {
	checkout := t.TempDir()
	writeFile(t, checkout, ".kogen/intents/greeting/intent.md", []byte("---\ntitle: Greeting\nsize: small\ndomains: [app]\n---\nUpdate the greeting.\n\n## Acceptance\n- A1: update greeting\n\n## Verify\n- A1: test\n"))
	writeFile(t, checkout, ".kogen/acceptance/greeting.t.sh", []byte("#!/bin/sh\nexit 0\n"))
	writeFile(t, checkout, "mix.exs", []byte("project(:sample, [])\n"))
	runDir := filepath.Join(checkout, "approval-run")
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	scratchDir := filepath.Join(checkout, "scratch-base")
	if err := os.Mkdir(scratchDir, 0o700); err != nil {
		t.Fatal(err)
	}
	baseCommit := contract.ObjectID(strings.Repeat("1", 40))
	baseTree := contract.ObjectID(strings.Repeat("2", 40))
	git := fakeGit{commit: baseCommit, tree: baseTree}
	checkCalls := 0
	checker := checkFunc(func(_ context.Context, workdir string, spec contract.CheckSpec) (contract.CheckResult, error) {
		checkCalls++
		if spec.Name == "syntax" {
			candidate := filepath.Join(workdir, "test", "acceptance", "greeting.t.sh")
			if data, err := os.ReadFile(candidate); err != nil || string(data) == "" {
				return contract.CheckResult{}, fmt.Errorf("staged candidate unavailable: %w", err)
			}
		}
		zero := 0
		return contract.CheckResult{Name: spec.Name, Status: contract.CheckGreen, ExitStatus: &zero, TreeBefore: string(baseTree), TreeAfter: string(baseTree)}, nil
	})
	baseline := &fakeBaseline{}
	setup := &fakeSetup{}
	request := Request{
		Project: &project.Resolution{
			Checkout: checkout, Origin: checkout, Base: "main",
			Config: &project.Config{Raw: yamlmini.Mapping{
				"checks":            yamlmini.Sequence{yamlmini.Mapping{"name": "unit", "argv": yamlmini.Sequence{"true"}, "timeout_ms": "10000"}},
				"acceptance_checks": yamlmini.Sequence{yamlmini.Mapping{"name": "syntax", "argv": yamlmini.Sequence{"sh", "-n", "{path}"}, "timeout_ms": "10000"}},
			}},
		},
		Slug: "greeting", By: "Kogen Test", RunDir: runDir,
		AcceptanceSourcePath:    ".kogen/acceptance/greeting.t.sh",
		AcceptanceCandidatePath: "test/acceptance/greeting.t.sh",
		BaseEnvironment:         process.Environment{"PATH": filepath.Join(checkout, "no-bin")},
		AdapterVersion:          "command-v1", Toolchain: map[string]string{"shell": "test-shell"}, ToolchainKnown: true,
	}
	deps := Dependencies{
		Git: git, Policy: func(directory string) contract.GitPolicy { return contract.GitPolicy{WorkingDirectory: directory} },
		Roots: safefs.Opener{}, Processes: processFunc(func(context.Context, contract.ProcessSpec) (contract.ProcessResult, error) {
			return contract.ProcessResult{}, errors.New("unexpected process invocation")
		}),
		Checks: checker, Trees: treeFunc(func(context.Context, string) (string, error) { return string(baseTree), nil }),
		Scratch: scratchFunc(func(_ context.Context, request ScratchRequest) (ScratchWorkspace, error) {
			if request.BaseCommit != baseCommit || request.BaseTree != baseTree {
				t.Fatalf("scratch request = %#v, want exact resolved base", request)
			}
			return &scratchWorkspace{dir: scratchDir, tree: baseTree}, nil
		}),
		Setup: setup, Baselines: baseline, Manifest: manifestFunc(func(_ context.Context, _ contract.GitPort, _ contract.GitPolicy, _ protection.BuildOptions) (*protection.BuildResult, error) {
			return &protection.BuildResult{Manifest: protection.Manifest{}}, nil
		}),
	}
	prepared, err := Prepare(context.Background(), request, deps)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.IsCard || prepared.BaseTree != baseTree || !strings.Contains(prepared.Card, "SHA-256: "+prepared.ApprovalSHA256) {
		t.Fatalf("prepared result is not bound to base/card: %#v", prepared)
	}
	if setup.calls != 1 || baseline.calls != 1 || checkCalls != 2 {
		t.Fatalf("setup/baseline/check calls = %d/%d/%d, want 1/1/2", setup.calls, baseline.calls, checkCalls)
	}
	if baseline.key.Version != 3 || baseline.key.CheckedBaseTree != string(baseTree) || !baseline.key.Cacheable {
		t.Fatalf("baseline key = %#v, want cacheable v3 bound to exact tree", baseline.key)
	}
	if _, err := os.Lstat(filepath.Join(scratchDir, "test", "acceptance", "greeting.t.sh")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("candidate test was not removed after checks: %v", err)
	}
}

type checkFunc func(context.Context, string, contract.CheckSpec) (contract.CheckResult, error)

func (f checkFunc) Run(ctx context.Context, dir string, spec contract.CheckSpec) (contract.CheckResult, error) {
	return f(ctx, dir, spec)
}

type processFunc func(context.Context, contract.ProcessSpec) (contract.ProcessResult, error)

func (f processFunc) Run(ctx context.Context, spec contract.ProcessSpec) (contract.ProcessResult, error) {
	return f(ctx, spec)
}

type fakeGit struct {
	commit contract.ObjectID
	tree   contract.ObjectID
}

func (g fakeGit) Exec(_ context.Context, args []string, _ []byte, _ contract.GitPolicy) (contract.GitResult, error) {
	zero := 0
	result := contract.GitResult{Process: contract.ProcessResult{ExitStatus: &zero}}
	if len(args) == 2 && args[0] == "rev-parse" && args[1] == "--show-object-format" {
		result.Stdout = []byte("sha1\n")
		return result, nil
	}
	if len(args) == 5 && args[0] == "rev-parse" && args[1] == "--verify" {
		switch {
		case strings.HasSuffix(args[4], "^{commit}"):
			result.Stdout = []byte(g.commit + "\n")
		case strings.HasSuffix(args[4], "^{tree}"):
			result.Stdout = []byte(g.tree + "\n")
		default:
			return contract.GitResult{}, fmt.Errorf("unexpected revision %q", args[4])
		}
		return result, nil
	}
	return contract.GitResult{}, fmt.Errorf("unexpected git command %q", args)
}

type treeFunc func(context.Context, string) (string, error)

func (f treeFunc) Snapshot(ctx context.Context, directory string) (string, error) {
	return f(ctx, directory)
}

type scratchFunc func(context.Context, ScratchRequest) (ScratchWorkspace, error)

func (f scratchFunc) OpenExactBase(ctx context.Context, request ScratchRequest) (ScratchWorkspace, error) {
	return f(ctx, request)
}

type scratchWorkspace struct {
	dir  string
	tree contract.ObjectID
}

func (s *scratchWorkspace) Directory() string                 { return s.dir }
func (s *scratchWorkspace) Tree() contract.ObjectID           { return s.tree }
func (s *scratchWorkspace) ResetToBase(context.Context) error { return nil }
func (s *scratchWorkspace) Close() error                      { return nil }

type fakeSetup struct{ calls int }

func (s *fakeSetup) Run(ctx context.Context, _ SetupRequest, run func(context.Context) error) (SetupResult, error) {
	s.calls++
	if err := run(ctx); err != nil {
		return SetupResult{}, err
	}
	return SetupResult{Key: strings.Repeat("a", 64)}, nil
}

func (*fakeSetup) Restore(context.Context, string, string) error { return nil }

type fakeBaseline struct {
	calls int
	key   BaselineKey
}

func (b *fakeBaseline) GetOrCompute(ctx context.Context, key BaselineKey, compute func(context.Context) ([]BaselineRow, error)) (BaselineResult, error) {
	b.calls++
	b.key = key
	rows, err := compute(ctx)
	return BaselineResult{Rows: rows}, err
}

type manifestFunc func(context.Context, contract.GitPort, contract.GitPolicy, protection.BuildOptions) (*protection.BuildResult, error)

func (f manifestFunc) Build(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, options protection.BuildOptions) (*protection.BuildResult, error) {
	return f(ctx, git, policy, options)
}

func writeFile(t *testing.T, root, name string, content []byte) {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, content, 0o600); err != nil {
		t.Fatal(err)
	}
}
