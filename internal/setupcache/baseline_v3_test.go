package setupcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"kogen-go/internal/approval/prepare"
	"kogen-go/internal/contract"
	"kogen-go/internal/intent"
	"kogen-go/internal/process"
)

// This vector pins the current Go serialization for review. The shared v1.3
// cross-language fixture is still pending: spec/02-formats.md §2.9 requires
// sorted JSON object keys and describes child_env as an object, while the
// current BaselineKey document uses struct field order and a KEY=value array.
func TestBaselineV3LocalSerializationVector(t *testing.T) {
	check := contract.CheckSpec{
		Name: "format", Adapter: "command", Program: "mix",
		Args: []string{"format", "--check"}, Timeout: 12500 * time.Millisecond,
	}
	key, err := prepare.NewBaselineKey(
		strings.Repeat("1", 40), strings.Repeat("a", 64), []contract.CheckSpec{check},
		process.Environment{"PATH": "/tool/bin", "LANG": "C.UTF-8"},
		map[string]string{"otp": "27.0", "elixir": "1.18.2"}, true,
		"darwin", "arm64", "command-v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	const fixture = `{"v":3,"checked_base_tree":"1111111111111111111111111111111111111111","setup_key":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","checks":[{"name":"format","adapter":"command","program":"mix","args":["format","--check"],"timeout_ms":12500}],"child_env":["LANG=C.UTF-8","PATH=/tool/bin"],"toolchain":{"elixir":"1.18.2","otp":"27.0"},"os":"darwin","arch":"arm64","adapter_version":"command-v1"}`
	digest := sha256.Sum256([]byte(fixture))
	if got, want := key.Digest, hex.EncodeToString(digest[:]); got != want {
		t.Fatalf("v3 local fixture digest = %s, want %s", got, want)
	}
	if key.Version != 3 || key.CheckedBaseTree != strings.Repeat("1", 40) || !key.Cacheable {
		t.Fatalf("v3 local fixture identity = %#v", key)
	}
}

func TestBaselineV3SourceOnlyTreeChangesReuseSetupButRefreshBaseline(t *testing.T) {
	cache := openTestCache(t)
	defer cache.Close()

	type base struct {
		tree   string
		source string
		status contract.CheckStatus
	}
	bases := []base{
		{tree: strings.Repeat("1", 40), source: "defect\n", status: contract.CheckRed},
		{tree: strings.Repeat("2", 40), source: "fixed\n", status: contract.CheckGreen},
		{tree: strings.Repeat("3", 40), source: "defect reintroduced\n", status: contract.CheckRed},
	}
	workspaces := make([]string, len(bases))
	for index, current := range bases {
		workspace := t.TempDir()
		writeTestFile(t, workspace, "mix.lock", "same dependencies\n", 0o644)
		writeTestFile(t, workspace, "lib/source.go", current.source, 0o644)
		workspaces[index] = workspace
	}
	request := setupRequest(workspaces[0])
	request.Inputs = []string{"mix.lock"}
	request.Outputs = []string{"deps"}
	setupCalls := 0
	setupResults := make([]prepare.SetupResult, len(bases))
	for index, current := range bases {
		request.Workspace = workspaces[index]
		request.BaseTree = current.tree
		result, err := cache.Run(context.Background(), request, func(context.Context) error {
			setupCalls++
			writeTestFile(t, request.Workspace, "deps/ready", "cached setup product\n", 0o644)
			return nil
		})
		if err != nil {
			t.Fatalf("setup for base %d: %v", index+1, err)
		}
		setupResults[index] = result
		if index == 0 && result.Reused {
			t.Fatal("first setup unexpectedly reused a cache entry")
		}
		if index != 0 && !result.Reused {
			t.Fatalf("source-only base %d missed setup cache", index+1)
		}
		if index != 0 && result.Key != setupResults[0].Key {
			t.Fatalf("setup key changed for source-only base %d: %s != %s", index+1, result.Key, setupResults[0].Key)
		}
		assertTestFile(t, request.Workspace, "deps/ready", "cached setup product\n")
	}
	if setupCalls != 1 {
		t.Fatalf("setup ran %d times, want one run and two source-only hits", setupCalls)
	}

	check := contract.CheckSpec{Name: "format", Adapter: "command", Program: "mix", Args: []string{"format", "--check"}, Timeout: 10 * time.Second}
	baselineCalls := 0
	baselineRows := make([]prepare.BaselineResult, len(bases))
	for index, current := range bases {
		key := baselineKey(t, current.tree, setupResults[index].Key, check, process.Environment{"PATH": "/tool/bin"}, true)
		baseline, err := cache.GetOrCompute(context.Background(), key, func(context.Context) ([]prepare.BaselineRow, error) {
			baselineCalls++
			return []prepare.BaselineRow{{Name: check.Name, Status: current.status}}, nil
		})
		if err != nil {
			t.Fatalf("baseline for base %d: %v", index+1, err)
		}
		if baseline.Reused {
			t.Fatalf("source-only base %d reused another tree's baseline", index+1)
		}
		if len(baseline.Rows) != 1 || baseline.Rows[0].Status != current.status {
			t.Fatalf("baseline for base %d = %#v, want %s", index+1, baseline.Rows, current.status)
		}
		baselineRows[index] = baseline
	}
	if baselineCalls != len(bases) {
		t.Fatalf("baseline ran %d times, want one run per exact tree", baselineCalls)
	}
	if baselineRows[1].Rows[0].Status != contract.CheckGreen || baselineRows[2].Rows[0].Status != contract.CheckRed {
		t.Fatalf("green replacement baseline excused reintroduced defect: replacement=%#v reintroduced=%#v", baselineRows[1].Rows, baselineRows[2].Rows)
	}

	key := baselineKey(t, bases[2].tree, setupResults[2].Key, check, process.Environment{"PATH": "/tool/bin"}, true)
	hit, err := cache.GetOrCompute(context.Background(), key, func(context.Context) ([]prepare.BaselineRow, error) {
		return nil, errors.New("same-tree baseline should be reused")
	})
	if err != nil || !hit.Reused || hit.Rows[0].Status != contract.CheckRed || baselineCalls != len(bases) {
		t.Fatalf("same-tree baseline = %#v, err=%v, computes=%d", hit, err, baselineCalls)
	}

	intentBytes := []byte("---\ntitle: Fixture\nsize: small\ndomains: [app]\n---\nKeep the source check honest.\n\n## Acceptance\n- A1: identify a reintroduced defect\n\n## Verify\n- A1: test\n")
	parsed, err := intent.Parse("baseline-fixture", intentBytes)
	if err != nil {
		t.Fatal(err)
	}
	approvalHash := intent.ApprovalSHA256(intentBytes, []byte("acceptance bytes\n"))
	cardBefore := prepare.RenderCard(parsed, "main", strings.Repeat("4", 40), approvalHash, "Fixture <fixture@example.invalid>", nil, baselineRows[2].Rows)
	cardAfter := prepare.RenderCard(parsed, "main", strings.Repeat("4", 40), approvalHash, "Fixture <fixture@example.invalid>", nil, hit.Rows)
	if cardBefore != cardAfter || !strings.Contains(cardAfter, approvalHash) {
		t.Fatalf("same-tree card/hash changed across baseline hit: hash=%s before=%q after=%q", approvalHash, cardBefore, cardAfter)
	}
}

func TestBaselineV3FullContextUnknownAndLegacyIdentitiesMiss(t *testing.T) {
	cache := openTestCache(t)
	defer cache.Close()

	check := contract.CheckSpec{Name: "unit", Adapter: "command", Program: "go", Args: []string{"test", "./..."}, Timeout: time.Second}
	environment := process.Environment{"PATH": "/tool/bin", "LANG": "C.UTF-8"}
	toolchain := map[string]string{"go": "go1.27.1", "shell": "sh-5"}
	tree := strings.Repeat("a", 40)
	setupKey := strings.Repeat("b", 64)
	makeKey := func(tree, setupKey string, checks []contract.CheckSpec, env process.Environment, tools map[string]string, known bool, osName, arch, adapter string) prepare.BaselineKey {
		t.Helper()
		key, err := prepare.NewBaselineKey(tree, setupKey, checks, env, tools, known, osName, arch, adapter)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	base := makeKey(tree, setupKey, []contract.CheckSpec{check}, environment, toolchain, true, runtime.GOOS, runtime.GOARCH, "adapter-v1")
	rowsFor := func(checks []contract.CheckSpec, status contract.CheckStatus) []prepare.BaselineRow {
		rows := make([]prepare.BaselineRow, len(checks))
		for index, spec := range checks {
			rows[index] = prepare.BaselineRow{Name: spec.Name, Status: status}
		}
		return rows
	}
	first, err := cache.GetOrCompute(context.Background(), base, func(context.Context) ([]prepare.BaselineRow, error) {
		return rowsFor([]contract.CheckSpec{check}, contract.CheckGreen), nil
	})
	if err != nil || first.Reused {
		t.Fatalf("initial baseline = %#v, err=%v", first, err)
	}

	longer := check
	longer.Timeout += time.Second
	changedCheck := check
	changedCheck.Program = "go-alt"
	reversed := []contract.CheckSpec{
		{Name: "first", Adapter: "command", Program: "go", Args: []string{"test"}, Timeout: time.Second},
		{Name: "second", Adapter: "command", Program: "go", Args: []string{"vet"}, Timeout: time.Second},
	}
	changedEnv := process.Environment{"PATH": "/other/tool/bin", "LANG": "C.UTF-8"}
	changedToolchain := map[string]string{"go": "go1.28.0", "shell": "sh-5"}
	variants := []struct {
		name   string
		key    prepare.BaselineKey
		checks []contract.CheckSpec
	}{
		{name: "checked base tree", key: makeKey(strings.Repeat("c", 40), setupKey, []contract.CheckSpec{check}, environment, toolchain, true, runtime.GOOS, runtime.GOARCH, "adapter-v1"), checks: []contract.CheckSpec{check}},
		{name: "setup identity", key: makeKey(tree, strings.Repeat("d", 64), []contract.CheckSpec{check}, environment, toolchain, true, runtime.GOOS, runtime.GOARCH, "adapter-v1"), checks: []contract.CheckSpec{check}},
		{name: "check definition", key: makeKey(tree, setupKey, []contract.CheckSpec{changedCheck}, environment, toolchain, true, runtime.GOOS, runtime.GOARCH, "adapter-v1"), checks: []contract.CheckSpec{changedCheck}},
		{name: "ordered checks", key: makeKey(tree, setupKey, reversed, environment, toolchain, true, runtime.GOOS, runtime.GOARCH, "adapter-v1"), checks: reversed},
		{name: "check deadline", key: makeKey(tree, setupKey, []contract.CheckSpec{longer}, environment, toolchain, true, runtime.GOOS, runtime.GOARCH, "adapter-v1"), checks: []contract.CheckSpec{longer}},
		{name: "child environment", key: makeKey(tree, setupKey, []contract.CheckSpec{check}, changedEnv, toolchain, true, runtime.GOOS, runtime.GOARCH, "adapter-v1"), checks: []contract.CheckSpec{check}},
		{name: "toolchain", key: makeKey(tree, setupKey, []contract.CheckSpec{check}, environment, changedToolchain, true, runtime.GOOS, runtime.GOARCH, "adapter-v1"), checks: []contract.CheckSpec{check}},
		{name: "platform", key: makeKey(tree, setupKey, []contract.CheckSpec{check}, environment, toolchain, true, "linux", "arm64", "adapter-v1"), checks: []contract.CheckSpec{check}},
		{name: "adapter", key: makeKey(tree, setupKey, []contract.CheckSpec{check}, environment, toolchain, true, runtime.GOOS, runtime.GOARCH, "adapter-v2"), checks: []contract.CheckSpec{check}},
	}
	computeCalls := 0
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			result, err := cache.GetOrCompute(context.Background(), variant.key, func(context.Context) ([]prepare.BaselineRow, error) {
				computeCalls++
				return rowsFor(variant.checks, contract.CheckRed), nil
			})
			if err != nil || result.Reused || len(result.Rows) != len(variant.checks) {
				t.Fatalf("changed-context baseline = %#v, err=%v", result, err)
			}
		})
	}
	if computeCalls != len(variants) {
		t.Fatalf("changed-context checks ran %d times, want %d", computeCalls, len(variants))
	}

	unknown := makeKey(tree, setupKey, []contract.CheckSpec{check}, environment, nil, false, runtime.GOOS, runtime.GOARCH, "adapter-v1")
	for index := 0; index < 2; index++ {
		result, err := cache.GetOrCompute(context.Background(), unknown, func(context.Context) ([]prepare.BaselineRow, error) {
			computeCalls++
			return rowsFor([]contract.CheckSpec{check}, contract.CheckGreen), nil
		})
		if err != nil || result.Reused {
			t.Fatalf("unknown identity run %d = %#v, err=%v", index+1, result, err)
		}
	}
	legacy := base
	legacy.Version = 2
	for index := 0; index < 2; index++ {
		result, err := cache.GetOrCompute(context.Background(), legacy, func(context.Context) ([]prepare.BaselineRow, error) {
			computeCalls++
			return rowsFor([]contract.CheckSpec{check}, contract.CheckGreen), nil
		})
		if err != nil || result.Reused {
			t.Fatalf("legacy identity run %d = %#v, err=%v", index+1, result, err)
		}
	}
	if want := len(variants) + 4; computeCalls != want {
		t.Fatalf("changed, unknown and legacy computations = %d, want %d", computeCalls, want)
	}
}
