package setupcache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"kogen-go/internal/approval/prepare"
	"kogen-go/internal/contract"
	"kogen-go/internal/process"
)

func TestSetupV2ReusesInputsAcrossSourceTreeChangesAndRestoresCOWProducts(t *testing.T) {
	cache := openTestCache(t)
	defer cache.Close()

	firstWorkspace := t.TempDir()
	writeTestFile(t, firstWorkspace, "mix.exs", "same lock and setup input\n", 0o644)
	request := setupRequest(firstWorkspace)
	request.Outputs = []string{"deps", "generated/manifest.json"}
	setupCalls := 0
	first, err := cache.Run(context.Background(), request, func(context.Context) error {
		setupCalls++
		writeTestFile(t, firstWorkspace, "deps/lib/answer.txt", "cached product\n", 0o644)
		writeTestFile(t, firstWorkspace, "generated/manifest.json", "cached manifest\n", 0o600)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Reused || setupCalls != 1 || !sha256Identity.MatchString(first.Key) {
		t.Fatalf("first setup result = %#v, setup calls=%d", first, setupCalls)
	}

	secondWorkspace := t.TempDir()
	writeTestFile(t, secondWorkspace, "mix.exs", "same lock and setup input\n", 0o644)
	request.Workspace = secondWorkspace
	request.BaseTree = strings.Repeat("c", 40) // Source-only tree changes are absent from the v2 setup key.
	second, err := cache.Run(context.Background(), request, func(context.Context) error {
		setupCalls++
		return errors.New("setup must be skipped on a cache hit")
	})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Reused || second.Key != first.Key || setupCalls != 1 {
		t.Fatalf("second setup result = %#v, setup calls=%d", second, setupCalls)
	}
	assertTestFile(t, secondWorkspace, "deps/lib/answer.txt", "cached product\n")
	assertTestFile(t, secondWorkspace, "generated/manifest.json", "cached manifest\n")

	// The restored product is an independent COW/byte copy; changing it cannot
	// corrupt the stored cache product or another restored workspace.
	writeTestFile(t, secondWorkspace, "deps/lib/answer.txt", "local mutation\n", 0o644)
	thirdWorkspace := t.TempDir()
	writeTestFile(t, thirdWorkspace, "mix.exs", "same lock and setup input\n", 0o644)
	request.Workspace = thirdWorkspace
	third, err := cache.Run(context.Background(), request, func(context.Context) error {
		setupCalls++
		return errors.New("setup must be skipped on the second cache hit")
	})
	if err != nil {
		t.Fatal(err)
	}
	if !third.Reused || setupCalls != 1 {
		t.Fatalf("third setup result = %#v, setup calls=%d", third, setupCalls)
	}
	assertTestFile(t, thirdWorkspace, "deps/lib/answer.txt", "cached product\n")
	assertTestFile(t, thirdWorkspace, "generated/manifest.json", "cached manifest\n")
}

func TestSetupV2BindsInputsAndUsesThreeEntryLRU(t *testing.T) {
	cache := openTestCache(t)
	defer cache.Close()

	keys := make([]string, 0, 4)
	for index := 0; index < 4; index++ {
		workspaceDirectory := t.TempDir()
		writeTestFile(t, workspaceDirectory, "mix.exs", string(rune('a'+index)), 0o644)
		request := setupRequest(workspaceDirectory)
		result, err := cache.Run(context.Background(), request, func(context.Context) error {
			writeTestFile(t, workspaceDirectory, "deps/pkg.txt", string(rune('a'+index)), 0o644)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Reused {
			t.Fatalf("unique input %d unexpectedly hit", index)
		}
		keys = append(keys, result.Key)
	}
	if len(cache.readSetupIndex()) != setupLRULimit {
		t.Fatalf("setup LRU has %d keys, want %d", len(cache.readSetupIndex()), setupLRULimit)
	}
	if err := cache.Restore(context.Background(), keys[0], t.TempDir()); err == nil {
		t.Fatal("oldest setup entry was not evicted")
	}

	unknownWorkspace := t.TempDir()
	writeTestFile(t, unknownWorkspace, "mix.exs", "stable\n", 0o644)
	unknown := setupRequest(unknownWorkspace)
	unknown.ToolchainKnown = false
	first, err := cache.Run(context.Background(), unknown, func(context.Context) error {
		writeTestFile(t, unknownWorkspace, "deps/pkg.txt", "first\n", 0o644)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := cache.Run(context.Background(), unknown, func(context.Context) error {
		writeTestFile(t, unknownWorkspace, "deps/pkg.txt", "second\n", 0o644)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Reused || second.Reused || first.Key == second.Key || !volatileKey.MatchString(first.Key) {
		t.Fatalf("unknown setup identity was reused: first=%#v second=%#v", first, second)
	}
	if err := cache.Restore(context.Background(), first.Key, unknownWorkspace); err != nil {
		t.Fatalf("unknown identity did not retain same-run setup products: %v", err)
	}
	assertTestFile(t, unknownWorkspace, "deps/pkg.txt", "first\n")
}

func TestSetupV2CanonicalEnvironmentUsesFilteredSortedIdentity(t *testing.T) {
	workspaceDirectory := t.TempDir()
	writeTestFile(t, workspaceDirectory, "mix.exs", "stable\n", 0o644)
	writeTestFile(t, workspaceDirectory, "apps/demo/config.exs", "stable config\n", 0o644)
	request := setupRequest(workspaceDirectory)
	request.Inputs = []string{"mix.exs", "apps/demo"}
	request.Outputs = []string{"deps", "_build"}
	first, cacheable, _, err := setupIdentity(request)
	if err != nil || !cacheable {
		t.Fatalf("setup identity = %q, cacheable=%t, err=%v", first, cacheable, err)
	}

	filtered := setupRequest(workspaceDirectory)
	filtered.ChildEnv["MISE_STATE_DIR"] = "/private/state-a"
	filtered.KeyEnvironment["MISE_STATE_DIR"] = "/private/state-a"
	filtered.Checks[0].Env = append(filtered.Checks[0].Env, "MISE_STATE_DIR=/private/state-a")
	filtered.Checks[0].Env = append(filtered.Checks[0].Env, "MISE_CACHE_DIR=/private/cache-a")
	filtered.Checks[0].Env = append(filtered.Checks[0].Env, "MISE_TRUSTED_CONFIG_PATHS=/private/trusted-a")
	filtered.Inputs = []string{"apps/demo", "mix.exs"}
	filtered.Outputs = []string{"_build", "deps"}
	second, cacheable, _, err := setupIdentity(filtered)
	if err != nil || !cacheable || second != first {
		t.Fatalf("MISE-only environment change altered setup key: first=%q second=%q cacheable=%t err=%v", first, second, cacheable, err)
	}

	changed := setupRequest(workspaceDirectory)
	changed.KeyEnvironment["PATH"] = "/other/bin"
	changed.Checks[0].Env = []string{"PATH=/other/bin"}
	third, cacheable, _, err := setupIdentity(changed)
	if err != nil || !cacheable || third == first {
		t.Fatalf("PATH environment change did not alter setup key: first=%q third=%q cacheable=%t err=%v", first, third, cacheable, err)
	}
}

func TestBaselineV3SeparatesExactTreeAndFullContextAndRetainsRows(t *testing.T) {
	cache := openTestCache(t)
	defer cache.Close()

	check := contract.CheckSpec{Name: "format", Adapter: "command", Program: "mix", Args: []string{"format", "--check"}, Timeout: 10 * time.Second}
	key := baselineKey(t, strings.Repeat("a", 40), strings.Repeat("b", 64), check, process.Environment{"PATH": "/tool/bin"}, true)
	line := uint32(17)
	computed := 0
	compute := func(context.Context) ([]prepare.BaselineRow, error) {
		computed++
		return []prepare.BaselineRow{{Name: "format", Status: contract.CheckRed, ExitStatus: intPointer(1), Findings: []prepare.BaselineFinding{{Path: "lib/a.ex", Rule: "format", Message: "needs formatting", Line: &line}}}}, nil
	}
	first, err := cache.GetOrCompute(context.Background(), key, compute)
	if err != nil || first.Reused || computed != 1 {
		t.Fatalf("first baseline = %#v, err=%v, computed=%d", first, err, computed)
	}
	line = 99
	second, err := cache.GetOrCompute(context.Background(), key, compute)
	if err != nil || !second.Reused || computed != 1 {
		t.Fatalf("second baseline = %#v, err=%v, computed=%d", second, err, computed)
	}
	if second.Rows[0].Status != contract.CheckRed || second.Rows[0].Findings[0].Line == nil || *second.Rows[0].Findings[0].Line != 17 {
		t.Fatalf("cached baseline row lost its observation: %#v", second.Rows)
	}

	changedTree := baselineKey(t, strings.Repeat("c", 40), strings.Repeat("b", 64), check, process.Environment{"PATH": "/tool/bin"}, true)
	changed, err := cache.GetOrCompute(context.Background(), changedTree, func(context.Context) ([]prepare.BaselineRow, error) {
		computed++
		return []prepare.BaselineRow{{Name: "format", Status: contract.CheckGreen}}, nil
	})
	if err != nil || changed.Reused || changed.Rows[0].Status != contract.CheckGreen || computed != 2 {
		t.Fatalf("changed-tree baseline = %#v, err=%v, computed=%d", changed, err, computed)
	}
	changedContext := baselineKey(t, strings.Repeat("a", 40), strings.Repeat("b", 64), contract.CheckSpec{Name: "format", Adapter: "command", Program: "mix", Args: []string{"format", "--check"}, Timeout: 11 * time.Second}, process.Environment{"PATH": "/tool/bin"}, true)
	deadlineMiss, err := cache.GetOrCompute(context.Background(), changedContext, func(context.Context) ([]prepare.BaselineRow, error) {
		computed++
		return []prepare.BaselineRow{{Name: "format", Status: contract.CheckGreen}}, nil
	})
	if err != nil || deadlineMiss.Reused || computed != 3 {
		t.Fatalf("changed-deadline baseline = %#v, err=%v, computed=%d", deadlineMiss, err, computed)
	}
}

func TestBaselineV3UnknownAndLegacyIdentitiesAlwaysCompute(t *testing.T) {
	cache := openTestCache(t)
	defer cache.Close()

	check := contract.CheckSpec{Name: "test", Adapter: "command", Program: "go", Args: []string{"test", "./..."}, Timeout: time.Second}
	unknown := baselineKey(t, strings.Repeat("a", 40), strings.Repeat("b", 64), check, process.Environment{"PATH": "/tool/bin"}, false)
	calls := 0
	compute := func(context.Context) ([]prepare.BaselineRow, error) {
		calls++
		return []prepare.BaselineRow{{Name: "test", Status: contract.CheckGreen}}, nil
	}
	for index := 0; index < 2; index++ {
		result, err := cache.GetOrCompute(context.Background(), unknown, compute)
		if err != nil || result.Reused {
			t.Fatalf("unknown baseline result = %#v, err=%v", result, err)
		}
	}
	legacy := baselineKey(t, strings.Repeat("d", 40), strings.Repeat("b", 64), check, process.Environment{"PATH": "/tool/bin"}, true)
	legacy.Version = 2
	for index := 0; index < 2; index++ {
		result, err := cache.GetOrCompute(context.Background(), legacy, compute)
		if err != nil || result.Reused {
			t.Fatalf("legacy baseline result = %#v, err=%v", result, err)
		}
	}
	if calls != 4 {
		t.Fatalf("compute called %d times, want four misses", calls)
	}
}

func TestBaselineV3DigestBindsEnvironmentToolchainPlatformAndAdapter(t *testing.T) {
	check := contract.CheckSpec{Name: "compile", Adapter: "command", Program: "go", Args: []string{"test", "./..."}, Timeout: time.Second}
	base := baselineKey(t, strings.Repeat("a", 40), strings.Repeat("b", 64), check, process.Environment{"PATH": "/tool/bin"}, true)
	variants := []prepare.BaselineKey{}
	variants = append(variants, baselineKey(t, strings.Repeat("a", 40), strings.Repeat("b", 64), check, process.Environment{"PATH": "/other/bin"}, true))
	toolchain, err := prepare.NewBaselineKey(base.CheckedBaseTree, base.SetupKey, []contract.CheckSpec{check}, process.Environment{"PATH": "/tool/bin"}, map[string]string{"go": "go1.28.0"}, true, runtime.GOOS, runtime.GOARCH, "adapter-v1")
	if err != nil {
		t.Fatal(err)
	}
	variants = append(variants, toolchain)
	platform, err := prepare.NewBaselineKey(base.CheckedBaseTree, base.SetupKey, []contract.CheckSpec{check}, process.Environment{"PATH": "/tool/bin"}, map[string]string{"go": "go1.27.1"}, true, "linux", "arm64", "adapter-v1")
	if err != nil {
		t.Fatal(err)
	}
	variants = append(variants, platform)
	adapter, err := prepare.NewBaselineKey(base.CheckedBaseTree, base.SetupKey, []contract.CheckSpec{check}, process.Environment{"PATH": "/tool/bin"}, map[string]string{"go": "go1.27.1"}, true, runtime.GOOS, runtime.GOARCH, "adapter-v2")
	if err != nil {
		t.Fatal(err)
	}
	variants = append(variants, adapter)
	for index, variant := range variants {
		if variant.Digest == base.Digest || !variant.Cacheable {
			t.Fatalf("context variant %d did not produce a distinct cacheable identity: %#v", index, variant)
		}
	}
}

func setupRequest(workspaceDirectory string) prepare.SetupRequest {
	return prepare.SetupRequest{
		BaseTree: strings.Repeat("a", 40), Workspace: workspaceDirectory,
		OS: runtime.GOOS, Arch: runtime.GOARCH,
		Toolchain: map[string]string{"elixir": "1.18.2"}, ToolchainKnown: true,
		Inputs: []string{"mix.exs"}, Outputs: []string{"deps"},
		Checks:   []contract.CheckSpec{{Name: "deps", Adapter: "command", Program: "mix", Args: []string{"deps.get"}, Env: []string{"PATH=/tool/bin"}, Timeout: time.Minute}},
		ChildEnv: process.Environment{"PATH": "/tool/bin"}, KeyEnvironment: process.Environment{"PATH": "/tool/bin"},
	}
}

func openTestCache(t *testing.T) *Cache {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	cache, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	return cache
}

func baselineKey(t *testing.T, tree, setup string, check contract.CheckSpec, environment process.Environment, toolchainKnown bool) prepare.BaselineKey {
	t.Helper()
	key, err := prepare.NewBaselineKey(tree, setup, []contract.CheckSpec{check}, environment, map[string]string{"go": "go1.27.1"}, toolchainKnown, runtime.GOOS, runtime.GOARCH, "adapter-v1")
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func writeTestFile(t *testing.T, root, relative, contents string, mode os.FileMode) {
	t.Helper()
	name := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func assertTestFile(t *testing.T, root, relative, expected string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != expected {
		t.Fatalf("%s = %q, want %q", relative, data, expected)
	}
}

func intPointer(value int) *int { return &value }
