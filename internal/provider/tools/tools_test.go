package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kogen-go/internal/contract"
	"kogen-go/internal/safefs"
)

func TestFileToolAllowListsAndArgumentDefaults(t *testing.T) {
	if got, want := AllowedFileTools(RoleBuilderDirect), []string{ToolEdit, ToolRead, ToolSearch, ToolWrite}; !equalStrings(got, want) {
		t.Fatalf("builder direct tools = %v, want %v", got, want)
	}
	if got, want := AllowedFileTools(RoleShaper), []string{ToolRead, ToolSearch, ToolWrite}; !equalStrings(got, want) {
		t.Fatalf("shaper tools = %v, want %v", got, want)
	}
	for _, role := range []ToolRole{RoleBuilderShell, RolePlanner, RoleAuditor, "unknown"} {
		if got := AllowedFileTools(role); len(got) != 0 {
			t.Errorf("role %q has file tools %v", role, got)
		}
	}

	read, err := ParseFileArguments(ToolRead, json.RawMessage(`{"path":"src/a.txt","timeout_ms":5}`))
	if err != nil || read.Offset != 1 || read.Limit != 200 || read.Path != "src/a.txt" {
		t.Fatalf("read defaults = %#v, %v", read, err)
	}
	search, err := ParseFileArguments(ToolSearch, json.RawMessage(`{"pattern":"word"}`))
	if err != nil || search.Path != "." || search.Pattern != "word" {
		t.Fatalf("search defaults = %#v, %v", search, err)
	}
	if _, err := ParseFileArguments(ToolRead, json.RawMessage(`{"path":"a","limit":401}`)); err == nil || err.Error() != invalidLimitText {
		t.Fatalf("limit error = %v, want %q", err, invalidLimitText)
	}
	if _, err := ParseFileArguments(ToolRead, json.RawMessage(`{"path":"a","offset":0}`)); err == nil || err.Error() != invalidArgumentsText {
		t.Fatalf("offset error = %v, want %q", err, invalidArgumentsText)
	}
	for _, raw := range []string{`[]`, `null`, `{"path":1}`, `{"path":"a","limit":1.5}`} {
		if _, err := ParseFileArguments(ToolRead, json.RawMessage(raw)); err == nil {
			t.Errorf("ParseFileArguments(%s) unexpectedly succeeded", raw)
		}
	}
	if _, err := ParseFileArguments(ToolSearch, json.RawMessage(`{"pattern":"x","path":null}`)); err == nil {
		t.Fatal("null search path unexpectedly succeeded")
	}
}

func TestReadLineRangeExactResultsAndErrors(t *testing.T) {
	ctx := newToolContext(t, RoleBuilderDirect)
	writeFixture(t, ctx.Workspace, "lib/lines.txt", "one\r\ntwo\nthree\n", 0o644)

	got, err := ctx.Execute(context.Background(), ToolRead, json.RawMessage(`{"path":"lib/lines.txt","limit":2}`))
	if err != nil || got != "lib/lines.txt:\n1: one\n2: two\n\n[continue with offset=3]" {
		t.Fatalf("read result = %q, %v", got, err)
	}
	got, err = ctx.Execute(context.Background(), ToolRead, json.RawMessage(`{"path":"lib/lines.txt","offset":3,"limit":1}`))
	if err != nil || got != "lib/lines.txt:\n3: three\n" {
		t.Fatalf("offset result = %q, %v", got, err)
	}
	got, err = ctx.Execute(context.Background(), ToolRead, json.RawMessage(`{"path":"lib/missing.txt"}`))
	if got != "" || err == nil || err.Error() != missingFileText {
		t.Fatalf("missing read = %q, %v", got, err)
	}
	writeFixture(t, ctx.Workspace, "lib/binary.dat", "a\x00b", 0o644)
	got, err = ctx.Execute(context.Background(), ToolRead, json.RawMessage(`{"path":"lib/binary.dat"}`))
	if got != "" || err == nil || err.Error() != binaryFileText {
		t.Fatalf("binary read = %q, %v", got, err)
	}
}

func TestRootedSymlinkPolicyAndShaperWriteScope(t *testing.T) {
	ctx := newToolContext(t, RoleShaper)
	outside := t.TempDir()
	writeFixture(t, ctx.Workspace, "lib/inside.txt", "inside\n", 0o644)
	writeFixture(t, outside, "secret.txt", "outside secret\n", 0o644)
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(ctx.Workspace, "lib/escape.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("inside.txt", filepath.Join(ctx.Workspace, "lib/alias.txt")); err != nil {
		t.Fatal(err)
	}

	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"path":"../outside.txt"}`),
		json.RawMessage(`{"path":"lib/escape.txt"}`),
	} {
		_, err := ctx.Execute(context.Background(), ToolRead, raw)
		if err == nil || err.Error() != pathEscapeText {
			t.Errorf("read %s error = %v, want %q", raw, err, pathEscapeText)
		}
	}
	ctx.Process = &fakeProcessRunner{}
	_, err := ctx.Execute(context.Background(), ToolSearch, json.RawMessage(`{"pattern":"secret","path":"lib/escape.txt"}`))
	if err == nil || err.Error() != pathEscapeText {
		t.Fatalf("external symlink search error = %v", err)
	}
	got, err := ctx.Execute(context.Background(), ToolRead, json.RawMessage(`{"path":"lib/alias.txt"}`))
	if err != nil || !strings.Contains(got, "1: inside") {
		t.Fatalf("contained symlink read = %q, %v", got, err)
	}

	_, err = ctx.Execute(context.Background(), ToolWrite, json.RawMessage(`{"path":"lib/inside.txt","content":"denied"}`))
	wantScope := "ERROR: Write target is outside the shaper's two-file scope. Allowed paths: .kogen/intents/greet/intent.md, .kogen/acceptance/greet.t.sh."
	if err == nil || err.Error() != wantScope {
		t.Fatalf("out-of-scope write error = %v, want %q", err, wantScope)
	}

	writeFixture(t, ctx.Workspace, ".kogen/intents/greet/intent.md", "old", 0o644)
	large := strings.Repeat("x", MaxShapeWriteBytes)
	got, err = ctx.Execute(context.Background(), ToolWrite, json.RawMessage(mustJSON(t, map[string]any{
		"path":    ".kogen/intents/greet/intent.md",
		"content": large,
	})))
	if err != nil || got != "Wrote .kogen/intents/greet/intent.md." {
		t.Fatalf("full Shape write result = %q, %v", got, err)
	}
	contents, err := os.ReadFile(filepath.Join(ctx.Workspace, ".kogen/intents/greet/intent.md"))
	if err != nil || string(contents) != large || len(contents) != MaxShapeWriteBytes {
		t.Fatalf("Shape write bytes = %d, %v", len(contents), err)
	}
	_, err = ctx.Execute(context.Background(), ToolWrite, json.RawMessage(mustJSON(t, map[string]any{
		"path":    ".kogen/intents/greet/intent.md",
		"content": large + "x",
	})))
	if err == nil || err.Error() != shapeWriteLimitText {
		t.Fatalf("oversized Shape write error = %v", err)
	}
}

func TestDirectWriteIsFullAndReplacesSymlinkLeafSafely(t *testing.T) {
	ctx := newToolContext(t, RoleBuilderDirect)
	writeFixture(t, ctx.Workspace, "target.txt", "keep target", 0o600)
	if err := os.Symlink("target.txt", filepath.Join(ctx.Workspace, "alias.txt")); err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("generated\n", 30_000)
	got, err := ctx.Execute(context.Background(), ToolWrite, json.RawMessage(mustJSON(t, map[string]any{
		"path": "alias.txt", "content": content,
	})))
	if err != nil || got != "Wrote alias.txt." {
		t.Fatalf("write result = %q, %v", got, err)
	}
	gotFile, err := os.ReadFile(filepath.Join(ctx.Workspace, "alias.txt"))
	if err != nil || string(gotFile) != content {
		t.Fatalf("published bytes = %d, %v", len(gotFile), err)
	}
	if target, err := os.ReadFile(filepath.Join(ctx.Workspace, "target.txt")); err != nil || string(target) != "keep target" {
		t.Fatalf("symlink target changed to %q, %v", target, err)
	}
}

func TestEditReplacesFirstMatchAndPreservesMode(t *testing.T) {
	ctx := newToolContext(t, RoleBuilderDirect)
	writeFixture(t, ctx.Workspace, "script.sh", "one one\n", 0o755)
	got, err := ctx.Execute(context.Background(), ToolEdit, json.RawMessage(`{"path":"script.sh","old":"one","new":"two"}`))
	if err != nil || got != "Edited script.sh." {
		t.Fatalf("edit result = %q, %v", got, err)
	}
	contents, err := os.ReadFile(filepath.Join(ctx.Workspace, "script.sh"))
	if err != nil || string(contents) != "two one\n" {
		t.Fatalf("edited contents = %q, %v", contents, err)
	}
	info, err := os.Stat(filepath.Join(ctx.Workspace, "script.sh"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("edited mode = %v, %v", info.Mode().Perm(), err)
	}
	_, err = ctx.Execute(context.Background(), ToolEdit, json.RawMessage(`{"path":"script.sh","old":"","new":"x"}`))
	if err == nil || err.Error() != invalidArgumentsText {
		t.Fatalf("empty edit source error = %v", err)
	}
}

func TestSearchUsesFullOutputAndFallsBackFromRipgrep(t *testing.T) {
	ctx := newToolContext(t, RoleShaper)
	fake := &fakeProcessRunner{responses: []fakeProcessResponse{
		{unavailable: true},
		{output: strings.Repeat("lib/many.txt:1: match\n", 350)},
	}}
	ctx.Process = fake
	got, err := ctx.Execute(context.Background(), ToolSearch, json.RawMessage(`{"pattern":"match","path":"."}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, "match\n") != 350 {
		t.Fatalf("search returned %d lines, want all 350", strings.Count(got, "match\n"))
	}
	if len(fake.specs) != 2 || fake.specs[0].Executable != "rg" || fake.specs[1].Executable != "grep" {
		t.Fatalf("search commands = %#v", fake.specs)
	}
	if !equalStrings(fake.specs[0].Args, []string{"--line-number", "--color", "never", "--", "match", "."}) {
		t.Fatalf("rg args = %q", fake.specs[0].Args)
	}
	if !equalStrings(fake.specs[1].Args, []string{"-R", "-n", "--", "match", "."}) {
		t.Fatalf("grep args = %q", fake.specs[1].Args)
	}
	entries, err := ctx.RunRoot.ReadDir("logs")
	if err != nil || len(entries) != 0 {
		t.Fatalf("search logs left behind: %v, %v", entries, err)
	}
}

func TestSearchNoMatchesAndPathErrors(t *testing.T) {
	ctx := newToolContext(t, RoleBuilderDirect)
	ctx.Process = &fakeProcessRunner{responses: []fakeProcessResponse{{output: ""}}}
	got, err := ctx.Execute(context.Background(), ToolSearch, json.RawMessage(`{"pattern":"none"}`))
	if err != nil || got != "No matches." {
		t.Fatalf("no-match result = %q, %v", got, err)
	}
	_, err = ctx.Execute(context.Background(), ToolSearch, json.RawMessage(`{"pattern":"x","path":"missing"}`))
	if err == nil || err.Error() != missingFileText {
		t.Fatalf("missing search path error = %v", err)
	}
	if len(ctx.Process.(*fakeProcessRunner).specs) != 1 {
		t.Fatalf("missing search path invoked process: %#v", ctx.Process.(*fakeProcessRunner).specs)
	}
}

func TestDispatchRejectsDisallowedToolsBeforeArgumentValidation(t *testing.T) {
	ctx := newToolContext(t, RoleShaper)
	_, err := ctx.Execute(context.Background(), ToolEdit, json.RawMessage(`null`))
	if err == nil || err.Error() != "ERROR (tool_not_allowed): This stage does not allow the requested tool." {
		t.Fatalf("disallowed edit error = %v", err)
	}
	_, err = ctx.Execute(context.Background(), ToolRead, json.RawMessage(`{"limit":2,"timeout_ms":9}`))
	if err == nil || err.Error() != invalidArgumentsText {
		t.Fatalf("bad read args error = %v", err)
	}
}

type fakeProcessResponse struct {
	output      string
	unavailable bool
	err         error
}

type fakeProcessRunner struct {
	responses []fakeProcessResponse
	specs     []contract.ProcessSpec
}

func (f *fakeProcessRunner) Run(_ context.Context, spec contract.ProcessSpec) (contract.ProcessResult, error) {
	f.specs = append(f.specs, spec)
	if len(f.responses) == 0 {
		return contract.ProcessResult{}, errors.New("unexpected process call")
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	if response.err != nil || response.unavailable {
		return contract.ProcessResult{Unavailable: response.unavailable}, response.err
	}
	if err := os.WriteFile(spec.LogPath, []byte(response.output), 0o600); err != nil {
		return contract.ProcessResult{}, err
	}
	return contract.ProcessResult{LogPath: spec.LogPath}, nil
}

func newToolContext(t *testing.T, role ToolRole) *ToolContext {
	t.Helper()
	workspace := t.TempDir()
	runDir := t.TempDir()
	workspaceRoot, err := safefs.OpenRoot(workspace)
	if err != nil {
		t.Fatal(err)
	}
	runRoot, err := safefs.OpenRoot(runDir)
	if err != nil {
		_ = workspaceRoot.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = workspaceRoot.Close()
		_ = runRoot.Close()
	})
	return &ToolContext{
		Role:          role,
		Workspace:     workspace,
		WorkspaceRoot: workspaceRoot,
		RunDir:        runDir,
		RunRoot:       runRoot,
		Environment:   []string{"PATH=/usr/bin:/bin"},
		ShaperWritePaths: [2]string{
			".kogen/intents/greet/intent.md",
			".kogen/acceptance/greet.t.sh",
		},
	}
}

func writeFixture(t *testing.T, root, name, contents string, mode fs.FileMode) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}
