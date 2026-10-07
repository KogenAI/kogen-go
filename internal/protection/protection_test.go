package protection

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/intent"
	"kogen-go/internal/process"
	"kogen-go/internal/project"
	"kogen-go/internal/safefs"
	"kogen-go/internal/testkit"
)

func TestCompileGlobSpecForms(t *testing.T) {
	if got := digest([]byte("kogen:absent")); got != AbsentSHA256 {
		t.Fatalf("absent sentinel digest = %s", got)
	}
	cases := []struct {
		pattern string
		match   []string
		reject  []string
	}{
		{"**/*.go", []string{"main.go", ".hidden/main.go", "src/deep/main.go"}, []string{"main.txt"}},
		{"src/*", []string{"src/.hidden"}, []string{"src/deep/file.go"}},
		{"src/**", []string{"src/file.go", "src/deep/file.go"}, []string{"other/file.go"}},
		{"docs/{a,b}.md", []string{"docs/a.md", "docs/b.md"}, []string{"docs/c.md"}},
		{"file[1-3]?.txt", []string{"file2a.txt", "file3_.txt"}, []string{"file4a.txt", "file2/a.txt"}},
		{"src/", []string{"src/file.go", "src/deep/file.go"}, []string{"src", "src-other/file.go"}},
	}
	for _, test := range cases {
		t.Run(test.pattern, func(t *testing.T) {
			glob, err := CompileGlob(test.pattern)
			if err != nil {
				t.Fatalf("CompileGlob(%q): %v", test.pattern, err)
			}
			for _, candidate := range test.match {
				if !glob.Match(candidate) {
					t.Errorf("%q did not match %q", test.pattern, candidate)
				}
			}
			for _, candidate := range test.reject {
				if glob.Match(candidate) {
					t.Errorf("%q unexpectedly matched %q", test.pattern, candidate)
				}
			}
		})
	}
	for _, pattern := range []string{"../secret", ".git/config", "bad[", "bad{brace", "bad\\path"} {
		if _, err := CompileGlob(pattern); err == nil {
			t.Errorf("CompileGlob(%q) unexpectedly succeeded", pattern)
		}
	}
}

func TestBuildManifestUsesBaseGlobsGateInferenceOwnBytesAndStaleCheck(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	configBytes := []byte(strings.Join([]string{
		"name: demo",
		"checks:",
		"  - name: lint",
		"    argv: [sh, ./scripts/lint.sh]",
		"    timeout_ms: 1000",
		"acceptance_checks:",
		"  - name: acceptance-format",
		"    argv: [make, test]",
		"    timeout_ms: 1000",
		"fix:",
		"  - name: fix",
		"    argv: [python3, ./tools/fix.py]",
		"    timeout_ms: 1000",
		"protected_paths:",
		"  - .private/**",
		`  - "docs/{a,b}.md"`,
		`  - "src/[a-c].go"`,
		"  - absent.txt",
		"gate_paths:",
		"  - gate/build.sh",
		"acceptance:",
		"  run: [ruby, ./scripts/acceptance.rb]",
	}, "\n"))
	writeProtectedFixtureFile(t, fixture.Checkout, ".kogen/project.yaml", configBytes, 0o644)
	files := map[string]string{
		".private/.secret":           "secret\n",
		"docs/a.md":                  "a\n",
		"docs/b.md":                  "b\n",
		"src/b.go":                   "package b\n",
		"scripts/lint.sh":            "#!/bin/sh\nexit 0\n",
		"scripts/acceptance.rb":      "puts 'ok'\n",
		"tools/fix.py":               "print('fix')\n",
		"gate/build.sh":              "#!/bin/sh\nexit 0\n",
		"Makefile":                   "test:\n\ttrue\n",
		"GNUmakefile":                "all:\n\ttrue\n",
		"unrelated.txt":              "unrelated\n",
		".kogen/acceptance/demo.exs": "source acceptance\n",
	}
	for name, contents := range files {
		writeProtectedFixtureFile(t, fixture.Checkout, name, []byte(contents), 0o644)
	}
	fixture.Run(t, "add", "--all")
	fixture.Run(t, "commit", "--quiet", "-m", "protection fixture")
	base := contract.ObjectID(strings.TrimSpace(string(fixture.Run(t, "rev-parse", "HEAD"))))

	config, err := project.ParseConfig(".kogen/project.yaml", configBytes)
	if err != nil {
		t.Fatalf("parse fixture project config: %v", err)
	}
	approvedIntentBytes := []byte("---\ntitle: Demo\nsize: small\ndomains: [app]\n---\nBrief.\n## Acceptance\n- A1: accepted\n## Verify\n- A1: test\n")
	approvedIntent, err := intent.Parse("demo", approvedIntentBytes)
	if err != nil {
		t.Fatalf("parse fixture Intent: %v", err)
	}
	policy := protectionTestPolicy(fixture)
	git := gitio.NewWorkspace(process.Supervisor{})
	options := BuildOptions{
		BaseCommit: base, CheckoutRoot: fixture.Checkout, Config: config, Intent: approvedIntent,
		CandidatePath: "test/acceptance/demo_test.rb", CandidateBytes: []byte("approved candidate test\n"),
		RemovedSources: []string{".kogen/acceptance/demo.exs"},
	}
	result, err := BuildManifest(context.Background(), git, policy, options)
	if err != nil {
		t.Fatalf("build protection manifest: %v", err)
	}
	wantPaths := []string{
		".kogen/intents/demo/intent.md", ".kogen/project.yaml", ".private/.secret",
		"docs/a.md", "docs/b.md", "src/b.go", "absent.txt", "gate/build.sh",
		"scripts/lint.sh", "scripts/acceptance.rb", "tools/fix.py", "Makefile", "GNUmakefile",
		"test/acceptance/demo_test.rb",
	}
	for _, name := range wantPaths {
		if _, ok := result.Manifest[name]; !ok {
			t.Errorf("manifest omitted %q", name)
		}
	}
	if _, ok := result.Manifest["unrelated.txt"]; ok {
		t.Error("manifest included an unrelated path")
	}
	if result.Manifest["absent.txt"].SHA256 != AbsentSHA256 || result.Manifest["absent.txt"].Present {
		t.Errorf("literal missing path entry = %#v", result.Manifest["absent.txt"])
	}
	if got := string(result.Manifest[".private/.secret"].Bytes); got != "secret\n" {
		t.Errorf("dotfile glob selected bytes %q", got)
	}
	if got := string(result.Manifest[".kogen/intents/demo/intent.md"].Bytes); got != string(approvedIntentBytes) {
		t.Errorf("Intent own bytes = %q", got)
	}
	if _, exists := result.Manifest[".kogen/acceptance/demo.exs"]; exists {
		t.Error("removed source acceptance copy appeared in manifest")
	}

	writeProtectedFixtureFile(t, fixture.Checkout, "scripts/lint.sh", []byte("changed in checkout\n"), 0o644)
	stale, err := BuildManifest(context.Background(), git, policy, options)
	var behind *CheckoutBehindBaseError
	if !errors.Is(err, ErrCheckoutBehindBase) || !errors.As(err, &behind) {
		t.Fatalf("stale checkout error = %v, want CheckoutBehindBaseError", err)
	}
	if stale == nil || len(stale.Behind) != 1 || stale.Behind[0] != "scripts/lint.sh" || len(behind.Paths) != 1 || behind.Paths[0] != "scripts/lint.sh" {
		t.Fatalf("stale paths result=%#v error=%#v", stale, behind)
	}
}

func TestProtectorRestoresFilesAbsentPathsSourcesAndSymlinkLeaves(t *testing.T) {
	rootPath, outside := t.TempDir(), t.TempDir()
	outsideTarget := filepath.Join(outside, "target")
	if err := os.WriteFile(outsideTarget, []byte("outside remains unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "approved.txt"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideTarget, filepath.Join(rootPath, "approved.txt.link")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rootPath, "absent", "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "absent", "nested", "new.txt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "source.exs"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		"approved.txt":      presentEntry([]byte("approved"), modeRegular),
		"approved.txt.link": presentEntry([]byte("inside/target"), modeSymlink),
		"absent":            absentEntry(),
	}
	protector, err := NewProtector(manifest, []string{"source.exs"})
	if err != nil {
		t.Fatal(err)
	}
	root, err := safefs.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	restored, err := protector.RestoreAfterBatch(root)
	if err != nil {
		t.Fatalf("restore after batch: %v", err)
	}
	if len(restored) != 4 {
		t.Fatalf("restored paths = %#v, want file, link, absent tree, and source", restored)
	}
	if got, err := os.ReadFile(filepath.Join(rootPath, "approved.txt")); err != nil || string(got) != "approved" {
		t.Errorf("approved bytes = %q, %v", got, err)
	}
	linkInfo, err := os.Lstat(filepath.Join(rootPath, "approved.txt.link"))
	if err != nil || linkInfo.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("restored link mode = %v, %v", linkInfo, err)
	}
	if got, err := os.Readlink(filepath.Join(rootPath, "approved.txt.link")); err != nil || got != "inside/target" {
		t.Errorf("restored link target = %q, %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(rootPath, "absent")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("absent protected directory remains: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(rootPath, "source.exs")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("source acceptance copy remains: %v", err)
	}
	if got, err := os.ReadFile(outsideTarget); err != nil || string(got) != "outside remains unchanged" {
		t.Errorf("outside symlink target changed: %q, %v", got, err)
	}
	findings, err := protector.Guard(root)
	if err != nil || len(findings) != 0 {
		t.Errorf("guard after restore = %#v, %v", findings, err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "approved.txt"), []byte("changed again"), 0o600); err != nil {
		t.Fatal(err)
	}
	findings, err = protector.Guard(root)
	if err != nil || len(findings) != 1 || findings[0].Path != "approved.txt" {
		t.Errorf("guard reports changed bytes = %#v, %v", findings, err)
	}
}

func TestManifestGlobsDoNotUseGitIgnoreEligibility(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	configBytes := []byte("name: demo\nchecks: []\nprotected_paths: [protected/**]\n")
	writeProtectedFixtureFile(t, fixture.Checkout, ".kogen/project.yaml", configBytes, 0o644)
	writeProtectedFixtureFile(t, fixture.Checkout, ".gitignore", []byte("protected/\n"), 0o644)
	fixture.Run(t, "add", "--all")
	fixture.Run(t, "commit", "--quiet", "-m", "glob ignore fixture")
	base := contract.ObjectID(strings.TrimSpace(string(fixture.Run(t, "rev-parse", "HEAD"))))
	writeProtectedFixtureFile(t, fixture.Checkout, "protected/ignored.txt", []byte("ignored by Git\n"), 0o644)
	fixture.Run(t, "check-ignore", "--", "protected/ignored.txt")
	config, err := project.ParseConfig(".kogen/project.yaml", configBytes)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := intent.Parse("demo", []byte("---\ntitle: Demo\nsize: small\ndomains: [app]\n---\nBrief.\n## Acceptance\n- A1: accepted\n## Verify\n- A1: test\n"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := BuildManifest(context.Background(), gitio.NewWorkspace(process.Supervisor{}), protectionTestPolicy(fixture), BuildOptions{
		BaseCommit: base, CheckoutRoot: fixture.Checkout, Config: config, Intent: parsed,
		CandidatePath: "test/acceptance/demo_test.rb", CandidateBytes: []byte("approved"),
	})
	var behind *CheckoutBehindBaseError
	if !errors.As(err, &behind) || result == nil {
		t.Fatalf("ignored untracked protected path error = %v, result = %#v", err, result)
	}
	entry, ok := result.Manifest["protected/ignored.txt"]
	if !ok || entry.Present || entry.SHA256 != AbsentSHA256 {
		t.Fatalf("ignored protected path entry = %#v, present=%t", entry, ok)
	}
	if len(behind.Paths) != 1 || behind.Paths[0] != "protected/ignored.txt" {
		t.Fatalf("stale paths = %#v", behind.Paths)
	}
}

func TestChangesGateDropsGatePathsAndPrograms(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	configBytes := []byte("name: demo\nchecks:\n  - name: lint\n    argv: [sh, gate.sh]\n    timeout_ms: 1000\nprotected_paths: [src/**]\ngate_paths: [explicit-gate.sh]\n")
	writeProtectedFixtureFile(t, fixture.Checkout, ".kogen/project.yaml", configBytes, 0o644)
	writeProtectedFixtureFile(t, fixture.Checkout, "gate.sh", []byte("gate\n"), 0o644)
	writeProtectedFixtureFile(t, fixture.Checkout, "explicit-gate.sh", []byte("explicit gate\n"), 0o644)
	writeProtectedFixtureFile(t, fixture.Checkout, "src/app.go", []byte("package app\n"), 0o644)
	fixture.Run(t, "add", "--all")
	fixture.Run(t, "commit", "--quiet", "-m", "changes gate fixture")
	base := contract.ObjectID(strings.TrimSpace(string(fixture.Run(t, "rev-parse", "HEAD"))))
	config, err := project.ParseConfig(".kogen/project.yaml", configBytes)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := intent.Parse("demo", []byte("---\ntitle: Demo\nsize: small\ndomains: [app]\nchanges_gate: true\n---\nBrief.\n## Acceptance\n- A1: accepted\n## Verify\n- A1: test\n"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := BuildManifest(context.Background(), gitio.NewWorkspace(process.Supervisor{}), protectionTestPolicy(fixture), BuildOptions{
		BaseCommit: base, CheckoutRoot: fixture.Checkout, Config: config, Intent: parsed,
		CandidatePath: "test/acceptance/demo_test.rb", CandidateBytes: []byte("approved"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Manifest["src/app.go"]; !ok {
		t.Error("changes_gate suppressed protected_paths")
	}
	for _, name := range []string{".kogen/project.yaml", "gate.sh", "explicit-gate.sh"} {
		if _, ok := result.Manifest[name]; ok {
			t.Errorf("changes_gate retained gate path %q", name)
		}
	}
}

func protectionTestPolicy(fixture *testkit.GitFixture) contract.GitPolicy {
	env := make(process.Environment)
	for _, pair := range fixture.Environment() {
		key, value, ok := strings.Cut(pair, "=")
		if ok {
			env[key] = value
		}
	}
	return gitio.WorkspacePolicy(fixture.Checkout, env)
}

func writeProtectedFixtureFile(t *testing.T, root, name string, content []byte, mode fs.FileMode) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, content, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(full, mode); err != nil {
		t.Fatal(err)
	}
}
