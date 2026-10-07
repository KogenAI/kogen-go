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

func TestRunReportsChildResultWhenItExitsWithoutReadingLargeStdin(t *testing.T) {
	for iteration := 0; iteration < 20; iteration++ {
		t.Run(strconv.Itoa(iteration), func(t *testing.T) {
			spec := newTestSpec(t.TempDir(), "/bin/sh")
			spec.Args = []string{"-c", "printf child-finished; exit 23"}
			spec.Env = []string{"PATH=/bin:/usr/bin"}
			spec.Stdin = make([]byte, 1<<20)

			result, err := (Supervisor{}).Run(context.Background(), spec)
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if got, want := result.ExitStatus, intPointer(23); !intPointersEqual(got, want) {
				t.Fatalf("exit status = %v, want %d", got, *want)
			}
			if got, want := string(result.OutputTail), "child-finished"; got != want {
				t.Fatalf("child output = %q, want %q", got, want)
			}
			if got, want := readTestLog(t, result.LogPath), "child-finished"; got != want {
				t.Fatalf("child log = %q, want %q", got, want)
			}
		})
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

func TestParentDeathGuardianStopsWorkerAndGrandchild(t *testing.T) {
	const fixtureEnv = "KOGEN_TEST_GUARDIAN_PARENT_FIXTURE"
	if root := os.Getenv(fixtureEnv); root != "" {
		pidFile := filepath.Join(root, "worker.pid")
		grandchildFile := filepath.Join(root, "grandchild.pid")
		session, outputRead := startGuardianTestSession(t, guardianRequest{
			Operation:  "start",
			Executable: "/bin/sh",
			Argv0:      "/bin/sh",
			Args:       []string{"-c", `echo $$ > "$PID_FILE"; /bin/sleep 60 & echo $! > "$GRANDCHILD_FILE"; wait`},
			Dir:        root,
			Env: []string{
				"PATH=/bin:/usr/bin",
				"PID_FILE=" + pidFile,
				"GRANDCHILD_FILE=" + grandchildFile,
			},
		})
		defer outputRead.Close()
		defer func() {
			session.closeControl()
			_, _ = session.waitForExit(2 * time.Second)
		}()
		for {
			event := nextGuardianTestEvent(t, session, 5*time.Second)
			if event.Type == "started" {
				select {}
			}
			if event.Type == "start_error" || event.Type == "finished" {
				t.Fatalf("guardian fixture failed before parent termination: %+v", event)
			}
		}
	}

	root := t.TempDir()
	worker := exec.Command(os.Args[0], "-test.run=^TestParentDeathGuardianStopsWorkerAndGrandchild$")
	worker.Env = append(os.Environ(), fixtureEnv+"="+root)
	worker.Stdout = nil
	worker.Stderr = nil
	if err := worker.Start(); err != nil {
		t.Fatalf("start guardian parent fixture: %v", err)
	}
	defer func() {
		_ = worker.Process.Kill()
		_ = worker.Wait()
	}()
	workerPIDPath := filepath.Join(root, "worker.pid")
	grandchildPIDPath := filepath.Join(root, "grandchild.pid")
	waitForTestFile(t, workerPIDPath, 5*time.Second)
	waitForTestFile(t, grandchildPIDPath, 5*time.Second)
	workerPID := readTestPID(t, workerPIDPath)
	grandchildPID := readTestPID(t, grandchildPIDPath)
	if err := worker.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL guardian parent fixture: %v", err)
	}
	_ = worker.Wait()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if processIsGone(workerPID) && processIsGone(grandchildPID) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("parent-death guardian left worker %d or grandchild %d alive beyond two seconds", workerPID, grandchildPID)
}

func TestGuardianNormalCleanupStopsStrayGrandchild(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(root, "grandchild.pid")
	session, outputRead := startGuardianTestSession(t, guardianRequest{
		Operation:  "start",
		Executable: "/bin/sh",
		Argv0:      "/bin/sh",
		Args:       []string{"-c", `sleep 60 & echo $! > "$PID_FILE"`},
		Dir:        root,
		Env:        []string{"PATH=/bin:/usr/bin", "PID_FILE=" + pidFile},
	})
	finished := false
	defer func() {
		_ = outputRead.Close()
		if !finished {
			session.closeControl()
			_, _ = session.waitForExit(2 * time.Second)
		}
	}()
	var exit guardianEvent
	for {
		event := nextGuardianTestEvent(t, session, 5*time.Second)
		if event.Type == "exited" {
			exit = event
			break
		}
		if event.Type == "start_error" || event.Type == "finished" {
			t.Fatalf("guardian child failed before normal cleanup: %+v", event)
		}
	}
	if exit.ExitStatus != 0 {
		t.Fatalf("worker exit status = %d, want 0", exit.ExitStatus)
	}
	if err := session.send(guardianRequest{Operation: "cleanup"}); err != nil {
		t.Fatalf("request normal guardian cleanup: %v", err)
	}
	for {
		event := nextGuardianTestEvent(t, session, 2*time.Second)
		if event.Type == "finished" {
			if event.Error != "" {
				t.Fatalf("guardian cleanup failed: %s", event.Error)
			}
			break
		}
	}
	session.closeControl()
	if _, err := session.waitForExit(10 * time.Second); err != nil {
		t.Fatalf("wait for guardian cleanup: %v", err)
	}
	finished = true
	grandchildPID := readTestPID(t, pidFile)
	waitForProcessGone(t, grandchildPID)
}

func TestProcessIdentityIncludesPIDStartAndGroup(t *testing.T) {
	identity, err := captureProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatalf("capture current process identity: %v", err)
	}
	if !processIdentityCurrent(identity) {
		t.Fatal("captured process identity does not match current PID")
	}
	identity.start++
	if processIdentityCurrent(identity) {
		t.Fatal("changed start token still matched the live process")
	}
}

func TestPrivateGroupAnchorHandshake(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	anchor, control, identity, err := startGroupAnchor(executable)
	if err != nil {
		t.Fatalf("start group anchor: %v", err)
	}
	defer func() {
		_ = control.Close()
		_ = anchor.Wait()
	}()
	if identity.pid != anchor.Process.Pid || identity.pgid != identity.pid {
		t.Fatalf("anchor identity = %+v, pid = %d", identity, anchor.Process.Pid)
	}
	if err := control.Close(); err != nil {
		t.Fatal(err)
	}
	if err := anchor.Wait(); err == nil {
		t.Fatal("anchor survived its EOF cleanup signal")
	}
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

func processIsGone(pid int) bool {
	if pid <= 1 {
		return true
	}
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return true
	}
	return processIsZombie(pid)
}

func waitForTestFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fixture did not create %s before deadline", filepath.Base(path))
}

func readTestPID(t *testing.T, path string) int {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture pid %s: %v", filepath.Base(path), err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(contents)))
	if err != nil || pid <= 1 {
		t.Fatalf("invalid fixture pid %q", strings.TrimSpace(string(contents)))
	}
	return pid
}

func startGuardianTestSession(t *testing.T, request guardianRequest) (*guardianSession, *os.File) {
	t.Helper()
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outputRead, outputWrite, err := os.Pipe()
	if err != nil {
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		t.Fatal(err)
	}
	session, err := startGuardian(stdinRead, outputWrite)
	_ = stdinRead.Close()
	_ = stdinWrite.Close()
	_ = outputWrite.Close()
	if err != nil {
		_ = outputRead.Close()
		t.Fatalf("start guardian fixture: %v", err)
	}
	if err := session.send(request); err != nil {
		session.waitAfterFailure()
		_ = outputRead.Close()
		t.Fatalf("send guardian fixture request: %v", err)
	}
	return session, outputRead
}

func nextGuardianTestEvent(t *testing.T, session *guardianSession, timeout time.Duration) guardianEvent {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case event, ok := <-session.events:
		if !ok {
			t.Fatal("guardian event stream closed unexpectedly")
		}
		if event.Type == "protocol_error" {
			t.Fatalf("guardian protocol error: %s", event.Error)
		}
		return event
	case <-timer.C:
		t.Fatal("timed out waiting for guardian event")
		return guardianEvent{}
	}
}

func intPointer(value int) *int { return &value }

func intPointersEqual(first, second *int) bool {
	if first == nil || second == nil {
		return first == second
	}
	return *first == *second
}
