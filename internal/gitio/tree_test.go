package gitio

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kogen-go/internal/contract"
	"kogen-go/internal/process"
	"kogen-go/internal/testkit"
)

type observedTreeEntry struct {
	mode   string
	object string
	kind   string
}

func TestCandidateTreeUsesNativeNestedIgnoreRulesAndBaseTrackedPaths(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	runner, policy := treeTestRunner(fixture, fixture.Checkout, map[string]string{
		"GIT_DIR":              fixture.Origin,
		"GIT_INDEX_FILE":       filepath.Join(fixture.Root, "poison-index"),
		"GIT_OBJECT_DIRECTORY": filepath.Join(fixture.Root, "poison-objects"),
	})
	writeTreeFile(t, fixture.Checkout, ".gitignore", []byte(".hidden\ntracked.log\nbuild/\n!build/\nbuild/*\n!build/keep.txt\n"), 0o644)
	writeTreeFile(t, fixture.Checkout, "tracked.log", []byte("base tracked bytes\n"), 0o644)
	if err := os.MkdirAll(filepath.Join(fixture.Checkout, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTreeFile(t, fixture.Checkout, "nested/.gitignore", []byte("*.cache\n!keep.cache\n"), 0o644)
	fixture.Run(t, "add", "--all", "-f")
	fixture.Run(t, "commit", "--quiet", "-m", "candidate tree base")
	base := fixtureHead(t, fixture)
	metadata, err := LoadBaseMetadata(context.Background(), runner, policy, base)
	if err != nil {
		t.Fatalf("load base metadata: %v", err)
	}

	writeTreeFile(t, fixture.Checkout, "tracked.log", []byte("tracked despite new ignore rule\n"), 0o644)
	writeTreeFile(t, fixture.Checkout, ".hidden", []byte("ignored dotfile\n"), 0o644)
	writeTreeFile(t, fixture.Checkout, ".visible", []byte("included dotfile\n"), 0o644)
	writeTreeFile(t, fixture.Checkout, "build/drop.bin", []byte("ignored child\n"), 0o644)
	writeTreeFile(t, fixture.Checkout, "build/keep.txt", []byte("negated child\n"), 0o644)
	writeTreeFile(t, fixture.Checkout, "nested/drop.cache", []byte("nested ignored\n"), 0o644)
	writeTreeFile(t, fixture.Checkout, "nested/keep.cache", []byte("nested negation\n"), 0o644)
	writeTreeFile(t, fixture.Checkout, "info-excluded.txt", []byte("info exclude must not hide this\n"), 0o644)
	writeTreeFile(t, fixture.Checkout, "global-excluded.txt", []byte("global exclude must not hide this\n"), 0o644)
	filterMarker := filepath.Join(fixture.Root, "candidate-filter-ran")
	fsmonitorMarker := filepath.Join(fixture.Root, "candidate-fsmonitor-ran")
	filter := writeHelper(t, fixture.Root, "candidate-filter", "printf ran > "+shellQuote(filterMarker)+"\ncat\n")
	fsmonitor := writeHelper(t, fixture.Root, "candidate-fsmonitor", "printf ran > "+shellQuote(fsmonitorMarker)+"\nprintf 'dirty\\n'\n")
	writeTreeFile(t, fixture.Checkout, ".gitattributes", []byte("tracked.log filter=unsafe\n"), 0o644)
	fixture.Run(t, "config", "--local", "filter.unsafe.clean", filter)
	fixture.Run(t, "config", "--local", "filter.unsafe.required", "true")
	fixture.Run(t, "config", "--local", "core.fsmonitor", fsmonitor)
	if err := os.WriteFile(filepath.Join(fixture.Checkout, ".git", "info", "exclude"), []byte("info-excluded.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	globalExcludes := filepath.Join(fixture.Root, "global-excludes")
	if err := os.WriteFile(globalExcludes, []byte("global-excluded.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.Run(t, "config", "--local", "core.excludesFile", globalExcludes)

	tree, err := SnapshotCandidateTree(context.Background(), runner, policy, metadata)
	if err != nil {
		t.Fatalf("snapshot candidate tree: %v", err)
	}
	entries := readTreeEntries(t, runner, policy, tree)
	for _, path := range []string{
		".gitignore", ".visible", "tracked.log", "build/keep.txt", "nested/.gitignore",
		"nested/keep.cache", "info-excluded.txt", "global-excluded.txt",
	} {
		if _, ok := entries[path]; !ok {
			t.Errorf("candidate tree omitted %q", path)
		}
	}
	for _, path := range []string{".hidden", "build/drop.bin", "nested/drop.cache"} {
		if _, ok := entries[path]; ok {
			t.Errorf("candidate tree retained ignored path %q", path)
		}
	}
	if got := string(readTreeBlob(t, runner, policy, entries["tracked.log"].object)); got != "tracked despite new ignore rule\n" {
		t.Errorf("tracked ignored bytes = %q", got)
	}
	for _, marker := range []string{filterMarker, fsmonitorMarker} {
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Errorf("workspace helper ran while capturing tree: %s (stat error %v)", marker, err)
		}
	}
}

func TestCandidateTreeIgnoresBuilderHeadAndIndexAndPreservesGitModes(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	runner, policy := treeTestRunner(fixture, fixture.Checkout, nil)
	writeTreeFile(t, fixture.Checkout, ".gitignore", []byte("tracked.log\n"), 0o644)
	writeTreeFile(t, fixture.Checkout, "tracked.log", []byte("base tracked\n"), 0o644)
	writeTreeFile(t, fixture.Checkout, "deleted.txt", []byte("delete me\n"), 0o644)
	writeTreeFile(t, fixture.Checkout, "old name.txt", []byte("rename me\n"), 0o644)
	writeTreeFile(t, fixture.Checkout, "run.sh", []byte("#!/bin/sh\r\nexit 0\r\n"), 0o644)
	fixture.Run(t, "add", "--all", "-f")
	fixture.Run(t, "commit", "--quiet", "-m", "candidate tree base")
	base := fixtureHead(t, fixture)
	metadata, err := LoadBaseMetadata(context.Background(), runner, policy, base)
	if err != nil {
		t.Fatalf("load base metadata: %v", err)
	}

	writeTreeFile(t, fixture.Checkout, "tracked.log", []byte("staged bytes\n"), 0o644)
	fixture.Run(t, "rm", "--cached", "--quiet", "tracked.log")
	fixture.Run(t, "commit", "--quiet", "-m", "builder moved HEAD")
	fixture.Run(t, "add", "--force", "--", "tracked.log")
	writeTreeFile(t, fixture.Checkout, "tracked.log", []byte("working tree bytes\r\n\x00"), 0o644)
	if err := os.Remove(filepath.Join(fixture.Checkout, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(fixture.Checkout, "old name.txt"), filepath.Join(fixture.Checkout, "new name.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(fixture.Checkout, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("run.sh", filepath.Join(fixture.Checkout, "run-link")); err != nil {
		t.Fatal(err)
	}

	tree, err := SnapshotCandidateTree(context.Background(), runner, policy, metadata)
	if err != nil {
		t.Fatalf("snapshot candidate tree: %v", err)
	}
	entries := readTreeEntries(t, runner, policy, tree)
	for _, path := range []string{"deleted.txt", "old name.txt"} {
		if _, ok := entries[path]; ok {
			t.Errorf("candidate tree retained deleted/renamed source %q", path)
		}
	}
	for _, path := range []string{"new name.txt", "tracked.log", "run.sh", "run-link"} {
		if _, ok := entries[path]; !ok {
			t.Errorf("candidate tree omitted %q", path)
		}
	}
	if got, want := entries["run.sh"].mode, "100755"; got != want {
		t.Errorf("executable mode = %q, want %q", got, want)
	}
	if got, want := entries["run-link"].mode, "120000"; got != want {
		t.Errorf("symlink mode = %q, want %q", got, want)
	}
	if got, want := string(readTreeBlob(t, runner, policy, entries["run-link"].object)), "run.sh"; got != want {
		t.Errorf("symlink blob = %q, want target %q", got, want)
	}
	if got, want := readTreeBlob(t, runner, policy, entries["tracked.log"].object), []byte("working tree bytes\r\n\x00"); string(got) != string(want) {
		t.Errorf("candidate used staged or filtered bytes: got %q, want %q", got, want)
	}
}

func TestWriteExactTreeIncludesIgnoredApprovalBytesAndPreservesModes(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	runner, policy := treeTestRunner(fixture, fixture.Checkout, nil)
	writeTreeFile(t, fixture.Checkout, ".gitignore", []byte(".env\n"), 0o644)
	fixture.Run(t, "add", ".gitignore")
	fixture.Run(t, "commit", "--quiet", "-m", "ignore draft files")
	files := []TreeFile{
		{Path: ".env", Mode: GitModeRegular, Bytes: []byte("approved secret bytes\r\n")},
		{Path: "script.sh", Mode: GitModeExecutable, Bytes: []byte("#!/bin/sh\r\nexit 0\r\n")},
		{Path: "link", Mode: GitModeSymlink, Bytes: []byte("script.sh")},
		{Path: "tab\tand\nnewline.txt", Mode: GitModeRegular, Bytes: []byte{0, 0xff, '\r', '\n'}},
	}
	tree, err := WriteExactTree(context.Background(), runner, policy, files)
	if err != nil {
		t.Fatalf("write exact tree: %v", err)
	}
	entries := readTreeEntries(t, runner, policy, tree)
	if _, ok := entries[".env"]; !ok {
		t.Fatal("exact tree omitted an ignored approved path")
	}
	if got := readTreeBlob(t, runner, policy, entries[".env"].object); string(got) != string(files[0].Bytes) {
		t.Fatalf("approved blob bytes = %q, want exact bytes %q", got, files[0].Bytes)
	}
	if got := entries["script.sh"].mode; got != "100755" {
		t.Fatalf("script mode = %q, want 100755", got)
	}
	if got := entries["link"].mode; got != "120000" {
		t.Fatalf("symlink mode = %q, want 120000", got)
	}
	if entry, ok := entries[files[3].Path]; !ok {
		t.Fatal("exact tree lost a path containing tab/newline bytes")
	} else if got := readTreeBlob(t, runner, policy, entry.object); string(got) != string(files[3].Bytes) {
		t.Fatalf("binary blob bytes = %v, want %v", got, files[3].Bytes)
	}
}

func TestCandidateTreeRejectsReplacedGitDirectory(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	runner, policy := treeTestRunner(fixture, fixture.Checkout, nil)
	base := fixtureHead(t, fixture)
	metadata, err := LoadBaseMetadata(context.Background(), runner, policy, base)
	if err != nil {
		t.Fatalf("load base metadata: %v", err)
	}
	outside := t.TempDir()
	gitDir := filepath.Join(fixture.Checkout, ".git")
	if err := os.Rename(gitDir, filepath.Join(fixture.Checkout, ".git.saved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, gitDir); err != nil {
		t.Fatal(err)
	}
	if _, err := SnapshotCandidateTree(context.Background(), runner, policy, metadata); err == nil {
		t.Fatal("candidate tree accepted a replaced .git symlink")
	}
}

func TestCandidateTreeSupportsSHA256Repository(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	root := filepath.Join(fixture.Root, "sha256")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.RunIn(t, root, "init", "--quiet", "--object-format=sha256", "--initial-branch=main")
	fixture.RunIn(t, root, "config", "user.name", "SHA256 Candidate Fixture")
	fixture.RunIn(t, root, "config", "user.email", "sha256-candidate@example.invalid")
	fixture.RunIn(t, root, "config", "commit.gpgsign", "false")
	writeTreeFile(t, root, "tracked.txt", []byte("base\n"), 0o644)
	fixture.RunIn(t, root, "add", "tracked.txt")
	fixture.RunIn(t, root, "commit", "--quiet", "-m", "base")
	runner, policy := treeTestRunner(fixture, root, nil)
	base := contract.ObjectID(strings.TrimSpace(string(fixture.RunIn(t, root, "rev-parse", "HEAD"))))
	writeTreeFile(t, root, "untracked.txt", []byte("candidate\n"), 0o644)
	tree, err := BuildCandidateTree(context.Background(), runner, policy, base)
	if err != nil {
		t.Fatalf("build SHA-256 candidate tree: %v", err)
	}
	if len(tree) != 64 {
		t.Fatalf("candidate tree id length = %d, want 64", len(tree))
	}
	if _, ok := readTreeEntries(t, runner, policy, tree)["untracked.txt"]; !ok {
		t.Fatal("SHA-256 candidate tree omitted untracked file")
	}
}

func treeTestRunner(fixture *testkit.GitFixture, directory string, overrides map[string]string) (*Runner, contract.GitPolicy) {
	env := environmentMap(fixture.Environment())
	for key, value := range overrides {
		env[key] = value
	}
	return NewWorkspace(process.Supervisor{}), WorkspacePolicy(directory, env)
}

func fixtureHead(t *testing.T, fixture *testkit.GitFixture) contract.ObjectID {
	t.Helper()
	return contract.ObjectID(strings.TrimSpace(string(fixture.Run(t, "rev-parse", "HEAD"))))
}

func writeTreeFile(t *testing.T, root, name string, contents []byte, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func readTreeEntries(t *testing.T, runner *Runner, policy contract.GitPolicy, tree contract.ObjectID) map[string]observedTreeEntry {
	t.Helper()
	var err error
	policy, _, err = workspaceGitPolicy(policy)
	if err != nil {
		t.Fatalf("prepare tree inspection policy: %v", err)
	}
	result, err := runner.Exec(context.Background(), []string{"ls-tree", "-r", "-z", "--full-tree", string(tree)}, nil, policy)
	if err != nil {
		t.Fatalf("list tree: %v", err)
	}
	if err := requireSuccess("list tree", result); err != nil {
		t.Fatal(err)
	}
	entries := make(map[string]observedTreeEntry)
	for _, record := range bytesSplitNUL(result.Stdout) {
		metadata, name, ok := strings.Cut(string(record), "\t")
		if !ok {
			t.Fatalf("malformed ls-tree record %q", record)
		}
		fields := strings.Fields(metadata)
		if len(fields) != 3 {
			t.Fatalf("malformed ls-tree metadata %q", metadata)
		}
		entries[name] = observedTreeEntry{mode: fields[0], kind: fields[1], object: fields[2]}
	}
	return entries
}

func readTreeBlob(t *testing.T, runner *Runner, policy contract.GitPolicy, object string) []byte {
	t.Helper()
	var err error
	policy, _, err = workspaceGitPolicy(policy)
	if err != nil {
		t.Fatalf("prepare blob inspection policy: %v", err)
	}
	result, err := runner.Exec(context.Background(), []string{"cat-file", "blob", object}, nil, policy)
	if err != nil {
		t.Fatalf("read blob %s: %v", object, err)
	}
	if err := requireSuccess("read tree blob", result); err != nil {
		t.Fatal(err)
	}
	return result.Stdout
}

func bytesSplitNUL(input []byte) [][]byte {
	if len(input) == 0 {
		return nil
	}
	if input[len(input)-1] != 0 {
		return [][]byte{input}
	}
	return bytes.Split(input[:len(input)-1], []byte{0})
}
