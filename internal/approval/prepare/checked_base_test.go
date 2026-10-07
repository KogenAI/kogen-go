package prepare

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kogen-go/internal/contract"
	"kogen-go/internal/process"
	"kogen-go/internal/project"
	"kogen-go/internal/protection"
	"kogen-go/internal/safefs"
	"kogen-go/internal/yamlmini"
)

func TestPrepareChecksDirtyCheckoutAgainstExactBaseAndTracesBaseTreeCacheKey(t *testing.T) {
	checkout := t.TempDir()
	intentSource := []byte("---\ntitle: Check a source file\nsize: small\ndomains: [app]\n---\nKeep the source check honest.\n\n## Acceptance\n- A1: identify a reintroduced defect\n\n## Verify\n- A1: test\n")
	acceptanceSource := []byte("#!/bin/sh\nexit 0\n")
	writeFile(t, checkout, ".kogen/intents/baseline-fixture/intent.md", intentSource)
	writeFile(t, checkout, ".kogen/acceptance/baseline-fixture.t.sh", acceptanceSource)
	writeFile(t, checkout, "src/example.go", []byte("dirty checkout contains a local fix\n"))

	type baseFixture struct {
		commit contract.ObjectID
		tree   contract.ObjectID
		source string
		status contract.CheckStatus
	}
	bases := []baseFixture{
		{commit: contract.ObjectID(strings.Repeat("a", 40)), tree: contract.ObjectID(strings.Repeat("1", 40)), source: "defect in exact base\n", status: contract.CheckRed},
		{commit: contract.ObjectID(strings.Repeat("b", 40)), tree: contract.ObjectID(strings.Repeat("2", 40)), source: "fixed in exact base\n", status: contract.CheckGreen},
		{commit: contract.ObjectID(strings.Repeat("c", 40)), tree: contract.ObjectID(strings.Repeat("3", 40)), source: "defect reintroduced in exact base\n", status: contract.CheckRed},
	}
	scratchByTree := make(map[contract.ObjectID]string, len(bases))
	treeByScratch := make(map[string]contract.ObjectID, len(bases))
	sourceByTree := make(map[contract.ObjectID]string, len(bases))
	baseByTree := make(map[contract.ObjectID]baseFixture, len(bases))
	for _, base := range bases {
		directory := t.TempDir()
		writeFile(t, directory, "src/example.go", []byte(base.source))
		scratchByTree[base.tree] = directory
		treeByScratch[directory] = base.tree
		sourceByTree[base.tree] = base.source
		baseByTree[base.tree] = base
	}
	dirtyTree := contract.ObjectID(strings.Repeat("f", 40))

	request := Request{
		Project: &project.Resolution{
			Checkout: checkout, Origin: checkout, Base: "main",
			Config: &project.Config{Raw: yamlmini.Mapping{
				"checks": yamlmini.Sequence{yamlmini.Mapping{
					"name": "source-policy", "argv": yamlmini.Sequence{"source-check"}, "timeout_ms": "10000",
				}},
			}},
		},
		Slug: "baseline-fixture", By: "Fixture <fixture@example.invalid>", RunDir: filepath.Join(checkout, "approval-run"),
		AcceptanceSourcePath:    ".kogen/acceptance/baseline-fixture.t.sh",
		AcceptanceCandidatePath: "test/acceptance/baseline-fixture.t.sh",
		BaseEnvironment:         process.Environment{"PATH": filepath.Join(checkout, "no-bin")},
		AdapterVersion:          "command-v1", Toolchain: map[string]string{"go": "go1.27.1"}, ToolchainKnown: true,
	}
	if err := os.Mkdir(request.RunDir, 0o700); err != nil {
		t.Fatal(err)
	}

	baseline := &checkedBaseBaselineCache{rows: make(map[string][]BaselineRow)}
	setup := &checkedBaseSetupCache{}
	var scratchRequests []ScratchRequest
	var checkCalls int
	var currentBase contract.ObjectID
	checker := checkFunc(func(_ context.Context, directory string, spec contract.CheckSpec) (contract.CheckResult, error) {
		checkCalls++
		tree, ok := treeByScratch[directory]
		if !ok {
			return contract.CheckResult{}, fmt.Errorf("baseline check ran outside exact-base scratch: %s", directory)
		}
		source, err := os.ReadFile(filepath.Join(directory, "src", "example.go"))
		if err != nil {
			return contract.CheckResult{}, err
		}
		if string(source) != sourceByTree[tree] || tree != currentBase {
			return contract.CheckResult{}, fmt.Errorf("checked %q from tree %s while resolved base is %s", source, tree, currentBase)
		}
		base := baseByTree[tree]
		exit := 0
		output := []byte(nil)
		if base.status == contract.CheckRed {
			exit = 1
			output = []byte("src/example.go:1:1: error: [go/vet] example.go: defect present\n")
		}
		return contract.CheckResult{
			Name: spec.Name, Status: base.status, ExitStatus: &exit,
			TreeBefore: string(tree), TreeAfter: string(tree), OutputTail: output,
		}, nil
	})
	deps := Dependencies{
		Git: fakeGit{}, Policy: func(directory string) contract.GitPolicy { return contract.GitPolicy{WorkingDirectory: directory} },
		Roots: safefs.Opener{},
		Processes: processFunc(func(context.Context, contract.ProcessSpec) (contract.ProcessResult, error) {
			return contract.ProcessResult{}, fmt.Errorf("unexpected process invocation")
		}),
		Checks: checker,
		Trees: treeFunc(func(_ context.Context, directory string) (string, error) {
			if tree, ok := treeByScratch[directory]; ok {
				return string(tree), nil
			}
			if directory == checkout {
				return string(dirtyTree), nil
			}
			return "", fmt.Errorf("unexpected tree snapshot: %s", directory)
		}),
		Scratch: scratchFunc(func(_ context.Context, scratch ScratchRequest) (ScratchWorkspace, error) {
			scratchRequests = append(scratchRequests, scratch)
			base, ok := baseByTree[scratch.BaseTree]
			if !ok || scratch.BaseCommit != base.commit || scratch.Origin != checkout || scratch.RunDir != request.RunDir {
				return nil, fmt.Errorf("scratch request is not bound to the resolved base: %#v", scratch)
			}
			return &scratchWorkspace{dir: scratchByTree[scratch.BaseTree], tree: scratch.BaseTree}, nil
		}),
		Setup: setup, Baselines: baseline,
		Manifest: manifestFunc(func(_ context.Context, _ contract.GitPort, _ contract.GitPolicy, _ protection.BuildOptions) (*protection.BuildResult, error) {
			return &protection.BuildResult{Manifest: protection.Manifest{}}, nil
		}),
	}

	prepareAt := func(base baseFixture, hash string) *Prepared {
		t.Helper()
		currentBase = base.tree
		deps.Git = fakeGit{commit: base.commit, tree: base.tree}
		request.HashPrefix = hash
		prepared, err := Prepare(context.Background(), request, deps)
		if err != nil {
			t.Fatalf("Prepare for base tree %s: %v", base.tree, err)
		}
		return prepared
	}

	redBeforeChange := prepareAt(bases[0], "")
	greenReplacement := prepareAt(bases[1], "")
	redAfterReintroducedDefect := prepareAt(bases[2], "")
	redCardHit := prepareAt(bases[2], "")
	approvalHit := prepareAt(bases[2], redAfterReintroducedDefect.ApprovalSHA256)

	if redBeforeChange.BaseTree != bases[0].tree || len(redBeforeChange.CheckBaseline) != 1 || redBeforeChange.CheckBaseline[0].Status != contract.CheckRed {
		t.Fatalf("first approval trace = %#v", redBeforeChange)
	}
	if greenReplacement.BaseTree != bases[1].tree || greenReplacement.BaselineReused || greenReplacement.CheckBaseline[0].Status != contract.CheckGreen {
		t.Fatalf("source-only green replacement reused another tree's baseline: %#v", greenReplacement)
	}
	if redAfterReintroducedDefect.BaseTree != bases[2].tree || redAfterReintroducedDefect.BaselineReused || redAfterReintroducedDefect.CheckBaseline[0].Status != contract.CheckRed {
		t.Fatalf("reintroduced defect was excused by a green baseline: %#v", redAfterReintroducedDefect)
	}
	if redCardHit.BaseTree != bases[2].tree || !redCardHit.BaselineReused || redCardHit.Card != redAfterReintroducedDefect.Card || redCardHit.ApprovalSHA256 != redAfterReintroducedDefect.ApprovalSHA256 {
		t.Fatalf("same-tree card cache trace changed: first=%#v hit=%#v", redAfterReintroducedDefect, redCardHit)
	}
	if approvalHit.IsCard || approvalHit.BaseTree != bases[2].tree || !approvalHit.BaselineReused || approvalHit.ApprovalSHA256 != redAfterReintroducedDefect.ApprovalSHA256 {
		t.Fatalf("hash approval did not reuse the exact-tree baseline: %#v", approvalHit)
	}
	if strings.Contains(redAfterReintroducedDefect.Card, "dirty checkout contains a local fix") {
		t.Fatal("approval card or baseline was derived from dirty checkout bytes")
	}
	if baseline.computes != 3 || checkCalls != 3 {
		t.Fatalf("baseline computations/check calls = %d/%d, want one per exact base tree", baseline.computes, checkCalls)
	}
	if len(baseline.keys) != 5 || len(scratchRequests) != 5 {
		t.Fatalf("baseline/scratch trace lengths = %d/%d, want five calls", len(baseline.keys), len(scratchRequests))
	}
	for index, expected := range []contract.ObjectID{bases[0].tree, bases[1].tree, bases[2].tree, bases[2].tree, bases[2].tree} {
		if got := baseline.keys[index].CheckedBaseTree; got != string(expected) {
			t.Errorf("approve trace %d baseTree = %s, want %s", index+1, got, expected)
		}
		if got := scratchRequests[index].BaseTree; got != expected {
			t.Errorf("scratch trace %d tree = %s, want %s", index+1, got, expected)
		}
		if got := setup.requests[index].BaseTree; got != string(expected) {
			t.Errorf("setup trace %d tree = %s, want %s", index+1, got, expected)
		}
	}
	for index := 1; index < len(baseline.keys); index++ {
		if baseline.keys[index].SetupKey != baseline.keys[0].SetupKey {
			t.Fatalf("setup identity changed in baseline trace %d", index+1)
		}
	}
	if baseline.keys[0].Digest == baseline.keys[1].Digest || baseline.keys[1].Digest == baseline.keys[2].Digest {
		t.Fatal("approval baseline digest did not follow exact baseTree changes")
	}
}

type checkedBaseBaselineCache struct {
	rows     map[string][]BaselineRow
	keys     []BaselineKey
	computes int
}

func (c *checkedBaseBaselineCache) GetOrCompute(ctx context.Context, key BaselineKey, compute func(context.Context) ([]BaselineRow, error)) (BaselineResult, error) {
	c.keys = append(c.keys, key)
	if rows, ok := c.rows[key.Digest]; ok {
		return BaselineResult{Rows: cloneBaseline(rows), Reused: true}, nil
	}
	rows, err := compute(ctx)
	if err != nil {
		return BaselineResult{}, err
	}
	c.computes++
	c.rows[key.Digest] = cloneBaseline(rows)
	return BaselineResult{Rows: cloneBaseline(rows)}, nil
}

type checkedBaseSetupCache struct {
	requests []SetupRequest
}

func (c *checkedBaseSetupCache) Run(ctx context.Context, request SetupRequest, run func(context.Context) error) (SetupResult, error) {
	c.requests = append(c.requests, request)
	if err := run(ctx); err != nil {
		return SetupResult{}, err
	}
	return SetupResult{Key: strings.Repeat("a", 64), Reused: len(c.requests) > 1}, nil
}

func (*checkedBaseSetupCache) Restore(context.Context, string, string) error { return nil }

var _ SetupCachePort = (*checkedBaseSetupCache)(nil)
var _ BaselineV3Port = (*checkedBaseBaselineCache)(nil)
