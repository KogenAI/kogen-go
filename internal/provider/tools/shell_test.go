package tools

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"kogen-go/internal/contract"
	"kogen-go/internal/process"
	"kogen-go/internal/provider/wire"
	"kogen-go/internal/safefs"
)

type shellProcessFixture struct {
	output     []byte
	writeLog   bool
	result     contract.ProcessResult
	err        error
	spec       contract.ProcessSpec
	script     []byte
	scriptMode fs.FileMode
}

func (f *shellProcessFixture) Run(_ context.Context, spec contract.ProcessSpec) (contract.ProcessResult, error) {
	f.spec = spec
	if len(spec.Args) == 1 {
		f.script, _ = os.ReadFile(spec.Args[0])
		if info, err := os.Stat(spec.Args[0]); err == nil {
			f.scriptMode = info.Mode().Perm()
		}
	}
	if f.writeLog {
		if err := os.WriteFile(spec.LogPath, f.output, 0o600); err != nil {
			return contract.ProcessResult{}, err
		}
	}
	return f.result, f.err
}

func TestShellUsesPrivateScriptAndOnlyPassesItsPathInArgv(t *testing.T) {
	ctx := newToolContext(t, RoleBuilderShell)
	command := "printf '%s' '" + strings.Repeat("x", 300<<10) + "' > heredoc.txt"
	fake := &shellProcessFixture{output: []byte("ok\n"), writeLog: true, result: contract.ProcessResult{ExitStatus: intPointer(0)}}
	ctx.Process = fake
	t.Setenv("KOGEN_TIME_SCALE", "0.02")

	got, err := ctx.Dispatch(context.Background(), ToolShell, json.RawMessage(mustJSON(t, map[string]any{
		"cmd": command, "timeout_ms": 1,
	})), 1, 2000)
	if err != nil || got != "ok\nexit 0\n" {
		t.Fatalf("shell result = %q, %v", got, err)
	}
	if fake.spec.Executable != "sh" || len(fake.spec.Args) != 1 || len(fake.spec.Args[0]) > 4096 {
		t.Fatalf("shell argv = %#v %q", fake.spec.Executable, fake.spec.Args)
	}
	if strings.Contains(fake.spec.Args[0], strings.Repeat("x", 64)) || string(fake.script) != command {
		t.Fatal("command bytes were not kept in the private script file")
	}
	if fake.scriptMode != 0o600 || fake.spec.Stdin != nil || fake.spec.Dir != ctx.Workspace {
		t.Fatalf("script mode/stdin/dir = %04o %v %q", fake.scriptMode, fake.spec.Stdin, fake.spec.Dir)
	}
	if fake.spec.Timeout != 2400*time.Millisecond {
		t.Fatalf("shell timeout = %s, want 2.4s", fake.spec.Timeout)
	}
	if fake.spec.OutputLimit != process.MaximumOutputLimit || fake.spec.OutputTailLimit != process.OutputTailBytes {
		t.Fatalf("shell output limits = %d/%d", fake.spec.OutputLimit, fake.spec.OutputTailLimit)
	}
	if _, err := os.Stat(fake.spec.Args[0]); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("private script remains after process: %v", err)
	}
}

func TestShellRunsSupervisedCommandAndMergesStreams(t *testing.T) {
	ctx := newToolContext(t, RoleBuilderShell)
	ctx.Process = process.Supervisor{}
	ctx.Environment = []string{"PATH=/bin:/usr/bin"}
	got, err := ctx.Dispatch(context.Background(), ToolShell, json.RawMessage(`{"cmd":"printf out; printf err >&2; exit 7"}`), 1, 2000)
	if err != nil || got != "outerr\nexit 7\n" {
		t.Fatalf("merged shell output = %q, %v", got, err)
	}
}

func TestShellMissingLogAndTimeoutPrefixesUseCapturedTail(t *testing.T) {
	ctx := newToolContext(t, RoleBuilderShell)
	fake := &shellProcessFixture{result: contract.ProcessResult{
		TimedOut: true, OutputTail: []byte("last\xff"),
	}}
	ctx.Process = fake
	got, err := ctx.Dispatch(context.Background(), ToolShell, json.RawMessage(`{"cmd":"sleep forever"}`), 1, 2000)
	if err != nil {
		t.Fatal(err)
	}
	want := "[non-UTF-8 output, base64 encoded]\n" + base64.StdEncoding.EncodeToString(append([]byte("timed out after 120 seconds\n[process log unavailable; captured tail may be incomplete]\nlast"), 0xff))
	if got != want {
		t.Fatalf("missing timed-out log = %q, want %q", got, want)
	}
}

func TestDispatchRoutesBuilderToolsAndEnforcesFinishAlone(t *testing.T) {
	ctx := newToolContext(t, RoleBuilderDirect)
	ctx.Process = &shellProcessFixture{output: []byte("shell\n"), writeLog: true, result: contract.ProcessResult{ExitStatus: intPointer(0)}}
	for _, test := range []struct {
		args  json.RawMessage
		count int
	}{
		{json.RawMessage(`{"extra":true}`), 1},
		{json.RawMessage(`[]`), 1},
		{json.RawMessage(`{}`), 2},
	} {
		if _, err := ctx.Dispatch(context.Background(), ToolFinish, test.args, test.count, 2000); err == nil || err.Error() != finishGuardText {
			t.Errorf("finish(%s, %d) error = %v", test.args, test.count, err)
		}
	}
	got, err := ctx.Dispatch(context.Background(), ToolFinish, json.RawMessage(`{}`), 1, 2000)
	if err != nil || got != completionRequested {
		t.Fatalf("standalone finish = %q, %v", got, err)
	}
	if _, err := ctx.Dispatch(context.Background(), ToolShell, json.RawMessage(`{"cmd":"true"}`), 1, 2000); err != nil {
		t.Fatalf("builder-direct shell: %v", err)
	}
	shaper := newToolContext(t, RoleShaper)
	if _, err := shaper.Dispatch(context.Background(), ToolFinish, json.RawMessage(`{}`), 1, 2000); err == nil || err.Error() != "ERROR (tool_not_allowed): This stage does not allow the requested tool." {
		t.Fatalf("shaper finish error = %v", err)
	}
}

func TestFinishPolicyRefusesOnlyTheFirstEmptyClaim(t *testing.T) {
	var policy FinishPolicy
	first := policy.Finish(false)
	if first.Action != FinishContinue || first.Feedback != emptyFinishFeedback || policy.EmptyFinishes() != 1 {
		t.Fatalf("first empty finish = %#v, count=%d", first, policy.EmptyFinishes())
	}
	second := policy.Finish(false)
	if second.Action != FinishVerify || second.Feedback != "" {
		t.Fatalf("second empty finish = %#v", second)
	}
	changed := policy.Finish(true)
	if changed.Action != FinishVerify {
		t.Fatalf("changed finish = %#v", changed)
	}
}

func TestCanonicalFinishSchemaIsStrictAndEmpty(t *testing.T) {
	for _, raw := range wire.CanonicalToolSchemas() {
		var schema map[string]any
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		if schema["name"] == ToolFinish {
			parameters := schema["parameters"].(map[string]any)
			if schema["strict"] != true || len(parameters["required"].([]any)) != 0 || len(parameters["properties"].(map[string]any)) != 0 || parameters["additionalProperties"] != false {
				t.Fatalf("finish schema = %s", raw)
			}
			return
		}
	}
	t.Fatal("canonical schema union has no finish schema")
}

func TestScaleShellTimeoutRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"", "not-a-number", "0", "-1", "NaN", "+Inf"} {
		t.Setenv("KOGEN_TIME_SCALE", value)
		if got := scaledShellTimeout(); got != defaultShellTimeout {
			t.Errorf("scale %q produced %s", value, got)
		}
	}
	t.Setenv("KOGEN_TIME_SCALE", "0.02")
	if got := scaledShellTimeout(); got != 2400*time.Millisecond {
		t.Fatalf("scaled timeout = %s", got)
	}
}

func intPointer(value int) *int { return &value }

func TestShellLogHandleDoesNotContainCommandOrRunPath(t *testing.T) {
	ctx := newToolContext(t, RoleBuilderShell)
	secret := "sk-secret-command-marker"
	fake := &shellProcessFixture{output: []byte("Bearer " + secret), writeLog: true, result: contract.ProcessResult{ExitStatus: intPointer(0)}}
	ctx.Process = fake
	got, err := ctx.Dispatch(context.Background(), ToolShell, json.RawMessage(mustJSON(t, map[string]any{"cmd": "echo " + secret})), 1, 128)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, secret) || strings.Contains(fake.spec.Args[0], secret) {
		t.Fatalf("secret leaked to result/argv: %q %q", got, fake.spec.Args)
	}
}

func TestTruncationNoticeFitsMaximumAndContainsContentHandle(t *testing.T) {
	ctx := newToolContext(t, RoleBuilderShell)
	contents := []byte(strings.Repeat("α", 5000) + "middle" + strings.Repeat("ω", 5000))
	got, err := BoundToolResult(ctx.RunRoot, contents, 128)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 512 || !utf8.ValidString(got) || !strings.Contains(got, "truncated/range") || !strings.HasPrefix(got, "α") || !strings.HasSuffix(got, "ω") {
		t.Fatalf("bounded result len=%d utf8=%t: %q", len(got), utf8.ValidString(got), got)
	}
	handle := handleFromNotice(t, got)
	stored, err := ctx.RunRoot.ReadFile("logs/" + toolResultFilePrefix + handle + toolResultFileSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if !equalBytes(stored, contents) || fmt.Sprintf("%x", sha256.Sum256(stored)) != handle {
		t.Fatal("stored full result or hash does not match the redacted payload")
	}
	info, err := ctx.RunRoot.Lstat("logs/" + toolResultFilePrefix + handle + toolResultFileSuffix)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stored result mode = %v, %v", info, err)
	}
}

func TestToolBudgetRedactsBearerJWTAndAPIKeysBeforeStorage(t *testing.T) {
	ctx := newToolContext(t, RoleBuilderShell)
	jwt := "header.payload.signature"
	apiKey := "sk-proj-secret"
	secret := []byte("Bearer bearer-secret\n" + jwt + "\n" + apiKey + "\n" + strings.Repeat("x", 9000))
	got, err := BoundToolResult(ctx.RunRoot, secret, 128)
	if err != nil {
		t.Fatal(err)
	}
	handle := handleFromNotice(t, got)
	stored, err := ctx.RunRoot.ReadFile("logs/" + toolResultFilePrefix + handle + toolResultFileSuffix)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"bearer-secret", jwt, apiKey} {
		if strings.Contains(string(stored), value) || strings.Contains(got, value) {
			t.Errorf("secret %q was not redacted", value)
		}
	}
	if !strings.Contains(string(stored), "Bearer [REDACTED]") || !strings.Contains(string(stored), "[REDACTED]") {
		t.Fatalf("redacted full result = %q", stored[:min(len(stored), 100)])
	}
}

func TestNonUTF8ResultBase64EncodesTheWholeRedactedPayload(t *testing.T) {
	ctx := newToolContext(t, RoleBuilderShell)
	payload := append([]byte("Bearer secret-value\nhead"), 0xff, 0x00, 0xfe)
	redacted := []byte("Bearer [REDACTED]\nhead\xff\x00\xfe")
	want := "[non-UTF-8 output, base64 encoded]\n" + base64.StdEncoding.EncodeToString(redacted)
	got, err := BoundToolResult(ctx.RunRoot, payload, 2000)
	if err != nil || got != want {
		t.Fatalf("base64 output = %q, %v; want %q", got, err, want)
	}
}

func TestToolOutputReadsUtf8SafeByteRangesAndRejectsSymlinks(t *testing.T) {
	ctx := newToolContext(t, RoleBuilderShell)
	full := strings.Repeat("x", 600) + "éZ" + strings.Repeat("y", 600)
	preview, err := BoundToolResult(ctx.RunRoot, []byte(full), 128)
	if err != nil {
		t.Fatal(err)
	}
	handle := handleFromNotice(t, preview)
	insideRune := uint64(601)
	limit := uint64(2)
	got, err := ReadToolOutput(ctx.RunRoot, ToolOutputArguments{Handle: handle, OutputOffset: insideRune, OutputLimit: &limit})
	if err != nil || got != "Z" {
		t.Fatalf("UTF-8 adjusted range = %q, %v", got, err)
	}
	got, err = ctx.Dispatch(context.Background(), ToolOutput, json.RawMessage(mustJSON(t, map[string]any{
		"handle": handle, "output_offset": 0, "output_limit": 0,
	})), 1, 2000)
	if err != nil || got != "" {
		t.Fatalf("zero-length tool_output = %q, %v", got, err)
	}
	if _, err := ctx.Dispatch(context.Background(), ToolOutput, json.RawMessage(`{"handle":"../x"}`), 1, 2000); err == nil || err.Error() != unknownOutputHandle {
		t.Fatalf("bad handle error = %v", err)
	}
	if _, err := ParseToolOutputArguments(json.RawMessage(`{"handle":"` + handle + `","output_offset":-1}`)); err == nil || err.Error() != invalidArgumentsText {
		t.Fatalf("negative range parse error = %v", err)
	}
	name := "logs/" + toolResultFilePrefix + handle + toolResultFileSuffix
	if err := ctx.RunRoot.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := ctx.RunRoot.Publish("logs/other.log", []byte(full), 0o600, safefs.PublicationCreateOnly); err != nil {
		t.Fatal(err)
	}
	if err := ctx.RunRoot.Symlink(name, "other.log"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadToolOutput(ctx.RunRoot, ToolOutputArguments{Handle: handle}); err == nil || err.Error() != unknownOutputHandle {
		t.Fatalf("symlink handle error = %v", err)
	}
}

func handleFromNotice(t *testing.T, value string) string {
	t.Helper()
	const marker = "retrieve with tool_output handle="
	index := strings.Index(value, marker)
	if index < 0 {
		t.Fatalf("no tool-output handle in %q", value)
	}
	start := index + len(marker)
	if len(value) < start+64 {
		t.Fatalf("truncated handle in %q", value)
	}
	return value[start : start+64]
}

var _ contract.ProcessRunner = (*shellProcessFixture)(nil)
