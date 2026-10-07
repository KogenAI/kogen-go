package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"kogen-go/internal/contract"
)

func TestProcessChildHelper(t *testing.T) {
	mode := os.Getenv("KOGEN_PROCESS_TEST_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "hang":
		_, _ = fmt.Fprintln(os.Stdout, "helper-started")
		for {
			time.Sleep(time.Hour)
		}
	case "chatty":
		for {
			_, _ = fmt.Fprint(os.Stdout, "0123456789abcdef")
		}
	case "report":
		_, _ = fmt.Fprintf(os.Stdout, "%s|%s|%s", os.Getenv("ONLY_CHILD_ENV"), os.Args[len(os.Args)-1], os.Getenv("HOST_SECRET"))
		os.Exit(0)
	default:
		os.Exit(91)
	}
}

func TestRunUsesExactEnvironmentAndArguments(t *testing.T) {
	root := t.TempDir()
	spec := newTestSpec(root, "sh")
	spec.Args = []string{
		"-c",
		`printf '%s|%s|%s' "$ONLY_CHILD_ENV" "$1" "${HOST_SECRET-unset}"`,
		"argv-zero",
		"argument with spaces",
	}
	spec.Env = []string{
		"KOGEN_PROCESS_TEST_HELPER=report",
		"ONLY_CHILD_ENV=present",
		"PATH=/usr/bin:/bin",
	}
	t.Setenv("HOST_SECRET", "must-not-leak")

	result, err := (Supervisor{}).Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := result.ExitStatus, intPointer(0); !intPointersEqual(got, want) {
		t.Fatalf("exit status = %v, want %d", got, *want)
	}
	if got, want := string(result.OutputTail), "present|argument with spaces|unset"; got != want {
		t.Fatalf("child output = %q, want %q", got, want)
	}
	if got, want := readTestLog(t, result.LogPath), "present|argument with spaces|unset"; got != want {
		t.Fatalf("log = %q, want %q", got, want)
	}
}

func TestRunBoundsLogAndPreservesExactTail(t *testing.T) {
	root := t.TempDir()
	spec := newTestSpec(root, "/bin/sh")
	spec.Args = []string{"-c", "printf '0123456789abcdefghijklmnopqrstuvwxyz'"}
	spec.Env = []string{"PATH=/bin:/usr/bin"}
	spec.OutputLimit = 12
	spec.OutputTailLimit = 8

	result, err := (Supervisor{}).Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := readTestLog(t, result.LogPath), "0123456789ab"; got != want {
		t.Fatalf("bounded log = %q, want %q", got, want)
	}
	info, err := os.Stat(result.LogPath)
	if err != nil {
		t.Fatalf("stat private log: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("log mode = %04o, want %04o", got, want)
	}
	if got, want := string(result.OutputTail), "stuvwxyz"; got != want {
		t.Fatalf("tail = %q, want %q", got, want)
	}
}

func TestRunCombinesOutputStreamsInWriteOrder(t *testing.T) {
	root := t.TempDir()
	spec := newTestSpec(root, "/bin/sh")
	spec.Args = []string{"-c", "printf out; printf err >&2; printf end"}
	spec.Env = []string{"PATH=/bin:/usr/bin"}

	result, err := (Supervisor{}).Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := string(result.OutputTail), "outerrend"; got != want {
		t.Fatalf("combined output = %q, want %q", got, want)
	}
}

func TestHangingHelperTimesOut(t *testing.T) {
	root := t.TempDir()
	spec := newTestSpec(root, os.Args[0])
	spec.Args = []string{"-test.run=^TestProcessChildHelper$"}
	spec.Env = []string{
		"KOGEN_PROCESS_TEST_HELPER=hang",
		"PATH=/usr/bin:/bin",
	}
	spec.Timeout = 100 * time.Millisecond
	spec.OutputLimit = 1024
	spec.OutputTailLimit = 16

	started := time.Now()
	result, err := (Supervisor{}).Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.TimedOut {
		t.Fatal("result is not marked timed out")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("hanging process took %s to stop", elapsed)
	}
	if len(result.OutputTail) > 16 {
		t.Fatalf("tail has %d bytes, maximum is 16", len(result.OutputTail))
	}
	if got := readTestLog(t, result.LogPath); !strings.Contains(got, "helper-started") {
		t.Fatalf("log does not contain helper output: %q", got)
	}
	if result.ExitStatus == nil || (*result.ExitStatus != signaledStatus(syscall.SIGTERM) && *result.ExitStatus != signaledStatus(syscall.SIGKILL)) {
		t.Fatalf("timeout exit status = %v, want SIGTERM or SIGKILL mapping", result.ExitStatus)
	}
}

func TestChattyHelperStopsAtIndependentWallDeadline(t *testing.T) {
	root := t.TempDir()
	spec := newTestSpec(root, os.Args[0])
	spec.Args = []string{"-test.run=^TestProcessChildHelper$"}
	spec.Env = []string{
		"KOGEN_PROCESS_TEST_HELPER=chatty",
		"PATH=/usr/bin:/bin",
	}
	spec.Timeout = 100 * time.Millisecond
	spec.OutputLimit = 1024
	spec.OutputTailLimit = 16

	started := time.Now()
	result, err := (Supervisor{}).Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.TimedOut {
		t.Fatal("result is not marked timed out")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("chatty process took %s to stop", elapsed)
	}
	if got, want := len(result.OutputTail), 16; got > want {
		t.Fatalf("tail has %d bytes, maximum is %d", got, want)
	}
	info, err := os.Stat(result.LogPath)
	if err != nil {
		t.Fatalf("stat process log: %v", err)
	}
	if info.Size() > 1024 {
		t.Fatalf("bounded log size = %d, maximum is 1024", info.Size())
	}
}

func TestTermTrapperGetsGraceThenKill(t *testing.T) {
	root := t.TempDir()
	spec := newTestSpec(root, "/bin/sh")
	spec.Args = []string{"-c", "trap 'printf term-seen; while :; do :; done' TERM; while :; do sleep 1; done"}
	spec.Env = []string{"PATH=/bin:/usr/bin"}
	spec.Timeout = 75 * time.Millisecond

	result, err := (Supervisor{}).Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.TimedOut {
		t.Fatal("result is not marked timed out")
	}
	if result.ExitStatus == nil || *result.ExitStatus != signaledStatus(syscall.SIGKILL) {
		t.Fatalf("exit status = %v, want %d", result.ExitStatus, signaledStatus(syscall.SIGKILL))
	}
	if !strings.Contains(string(result.OutputTail), "term-seen") {
		t.Fatalf("TERM trap did not run; tail is %q", result.OutputTail)
	}
}

func TestNormalExitStopsBackgroundGrandchild(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(root, "grandchild.pid")
	spec := newTestSpec(root, "/bin/sh")
	spec.Args = []string{"-c", "sleep 60 & echo $! > \"$PID_FILE\"; exit 0"}
	spec.Env = []string{"PATH=/bin:/usr/bin", "PID_FILE=" + pidFile}

	result, err := (Supervisor{}).Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ExitStatus == nil || *result.ExitStatus != 0 {
		t.Fatalf("exit status = %v, want 0", result.ExitStatus)
	}
	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read grandchild pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatalf("parse grandchild pid: %v", err)
	}
	waitForProcessGone(t, pid)
}

func TestRunMapsSignalExitStatusAndMissingProgram(t *testing.T) {
	root := t.TempDir()
	spec := newTestSpec(root, "/bin/sh")
	spec.Args = []string{"-c", "kill -TERM $$"}
	spec.Env = []string{"PATH=/bin:/usr/bin"}
	result, err := (Supervisor{}).Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run() signal error = %v", err)
	}
	if result.ExitStatus == nil || *result.ExitStatus != signaledStatus(syscall.SIGTERM) {
		t.Fatalf("signal exit status = %v, want %d", result.ExitStatus, signaledStatus(syscall.SIGTERM))
	}

	missing := newTestSpec(root, "kogen-no-such-child-program")
	missing.Env = []string{"PATH=/nonexistent"}
	missing.LogPath = filepath.Join(root, "logs", "missing.log")
	result, err = (Supervisor{}).Run(context.Background(), missing)
	if err != nil {
		t.Fatalf("Run() missing program error = %v", err)
	}
	if !result.Unavailable || result.ExitStatus == nil || *result.ExitStatus != 127 {
		t.Fatalf("missing program result = %+v, want unavailable 127", result)
	}
	badDirectory := newTestSpec(root, "/bin/sh")
	badDirectory.Dir = filepath.Join(root, "missing-workdir")
	if result, err := (Supervisor{}).Run(context.Background(), badDirectory); err == nil || result.Unavailable {
		t.Fatalf("missing cwd should be an invocation error, got result=%+v err=%v", result, err)
	}
}

func TestRunStopsGroupOnContextCancellation(t *testing.T) {
	root := t.TempDir()
	spec := newTestSpec(root, os.Args[0])
	spec.Args = []string{"-test.run=^TestProcessChildHelper$"}
	spec.Env = []string{"KOGEN_PROCESS_TEST_HELPER=hang", "PATH=/usr/bin:/bin"}
	spec.Timeout = 5 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	var result contract.ProcessResult
	var runErr error
	go func() {
		result, runErr = (Supervisor{}).Run(ctx, spec)
		close(finished)
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", runErr)
	}
	if result.ExitStatus == nil || (*result.ExitStatus != signaledStatus(syscall.SIGTERM) && *result.ExitStatus != signaledStatus(syscall.SIGKILL)) {
		t.Fatalf("canceled child exit status = %v", result.ExitStatus)
	}
}

func TestContextDeadlineIsReportedAsTimeout(t *testing.T) {
	root := t.TempDir()
	spec := newTestSpec(root, os.Args[0])
	spec.Args = []string{"-test.run=^TestProcessChildHelper$"}
	spec.Env = []string{"KOGEN_PROCESS_TEST_HELPER=hang", "PATH=/usr/bin:/bin"}
	spec.Timeout = 5 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()

	result, err := (Supervisor{}).Run(ctx, spec)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.TimedOut {
		t.Fatal("context deadline was not reported as a timeout")
	}
}

func TestRunRejectsOversizedArgAndInvalidLogLocation(t *testing.T) {
	root := t.TempDir()
	spec := newTestSpec(root, "/bin/echo")
	spec.Args = []string{strings.Repeat("x", MaximumArgumentSize+1)}
	if _, err := (Supervisor{}).Run(context.Background(), spec); err == nil {
		t.Fatal("Run() accepted an oversized argv element")
	}

	spec = newTestSpec(root, "/bin/echo")
	spec.LogPath = filepath.Join(root, "public", "child.log")
	if err := os.Mkdir(filepath.Dir(spec.LogPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := (Supervisor{}).Run(context.Background(), spec); err == nil {
		t.Fatal("Run() accepted a group-readable log directory")
	}

	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec = newTestSpec(root, "/bin/sh")
	if err := os.Symlink(outside, spec.LogPath); err != nil {
		t.Fatal(err)
	}
	spec.Args = []string{"-c", "printf replace"}
	spec.Env = []string{"PATH=/bin:/usr/bin"}
	if _, err := (Supervisor{}).Run(context.Background(), spec); err == nil {
		t.Fatal("Run() replaced an existing log symlink")
	}
	if got := readTestLog(t, outside); got != "keep" {
		t.Fatalf("external symlink target changed to %q", got)
	}
}

func newTestSpec(root, executable string) contract.ProcessSpec {
	logDir := filepath.Join(root, "logs")
	if err := os.Mkdir(logDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		panic(err)
	}
	return contract.ProcessSpec{
		Executable: executable,
		Dir:        root,
		LogPath:    filepath.Join(logDir, "process.log"),
	}
}

func readTestLog(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read process log: %v", err)
	}
	return string(contents)
}

func waitForProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) || processIsZombie(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %d survived process-group cleanup", pid)
}

func processIsZombie(pid int) bool {
	output, err := exec.Command("/bin/ps", "-o", "stat=", "-p", fmt.Sprint(pid)).Output()
	if err != nil {
		return true
	}
	state := strings.TrimSpace(string(output))
	return state == "" || strings.HasPrefix(state, "Z")
}

func intPointer(value int) *int { return &value }

func intPointersEqual(first, second *int) bool {
	if first == nil || second == nil {
		return first == second
	}
	return *first == *second
}
