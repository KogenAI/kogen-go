package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestAcquireStopAndOwnerOnlyRelease(t *testing.T) {
	root := t.TempDir()
	stopTarget := filepath.Join(t.TempDir(), "stop-target")
	if err := os.WriteFile(stopTarget, []byte("preserve\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(stopTarget, filepath.Join(root, queueStopName)); err != nil {
		t.Fatal(err)
	}

	started, err := Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	if started.Owner == nil || started.PID != 0 {
		t.Fatalf("first acquire = %#v, want an owner", started)
	}
	if started.Owner.PID() != os.Getpid() {
		t.Fatalf("owner pid = %d, want %d", started.Owner.PID(), os.Getpid())
	}
	contents, err := os.ReadFile(filepath.Join(root, queuePIDName))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != fmt.Sprintf("%d\n", os.Getpid()) {
		t.Fatalf("queue.pid = %q", contents)
	}
	if _, err := os.Lstat(filepath.Join(root, queueStopName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale queue.stop was not cleared: %v", err)
	}
	if contents, err := os.ReadFile(stopTarget); err != nil || string(contents) != "preserve\n" {
		t.Fatalf("stale stop symlink target = %q, %v", contents, err)
	}

	second, err := Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	if second.Owner != nil || second.PID != os.Getpid() {
		t.Fatalf("second acquire = %#v, want current process as owner", second)
	}
	requested, err := RequestStop(root)
	if err != nil {
		t.Fatal(err)
	}
	if !requested.Requested || requested.PID != os.Getpid() {
		t.Fatalf("stop result = %#v", requested)
	}
	if stop, err := started.Owner.StopRequested(); err != nil || !stop {
		t.Fatalf("owner stop marker = %t, %v", stop, err)
	}
	if marker, err := os.ReadFile(filepath.Join(root, queueStopName)); err != nil || string(marker) != "stop\n" {
		t.Fatalf("queue.stop = %q, %v", marker, err)
	}

	if err := started.Owner.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, queuePIDName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned queue.pid remains: %v", err)
	}
	if _, err := started.Owner.StopRequested(); !errors.Is(err, ErrReleased) {
		t.Fatalf("released owner stop check error = %v", err)
	}
}

func TestAcquireTakesOverDeadPIDInTwoAttempts(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, queuePIDName), []byte("2147483647\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	started, err := Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	if started.Owner == nil {
		t.Fatalf("stale owner was not replaced: %#v", started)
	}
	defer started.Owner.Release()
	contents, err := os.ReadFile(filepath.Join(root, queuePIDName))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != fmt.Sprintf("%d\n", os.Getpid()) {
		t.Fatalf("replacement queue.pid = %q", contents)
	}
}

func TestConcurrentAcquireCreatesOneOwner(t *testing.T) {
	root := t.TempDir()
	const attempts = 12
	start := make(chan struct{})
	type result struct {
		started StartResult
		err     error
	}
	results := make(chan result, attempts)
	for range attempts {
		go func() {
			<-start
			started, err := Acquire(root)
			results <- result{started: started, err: err}
		}()
	}
	close(start)
	owners := 0
	var acquired []*Owner
	for range attempts {
		item := <-results
		if item.err != nil {
			t.Fatalf("concurrent Acquire() error: %v", item.err)
		}
		if item.started.Owner != nil {
			owners++
			acquired = append(acquired, item.started.Owner)
		} else if item.started.PID != os.Getpid() {
			t.Errorf("concurrent owner pid = %d, want %d", item.started.PID, os.Getpid())
		}
	}
	for _, owner := range acquired {
		if err := owner.Release(); err != nil {
			t.Errorf("release concurrent owner: %v", err)
		}
	}
	if owners != 1 {
		t.Fatalf("concurrent acquires created %d owners, want exactly one", owners)
	}
}

func TestReleaseLeavesReplacementQueuePIDAlone(t *testing.T) {
	root := t.TempDir()
	started, err := Acquire(root)
	if err != nil || started.Owner == nil {
		t.Fatalf("Acquire() = %#v, %v", started, err)
	}
	temporary := filepath.Join(root, "replacement")
	if err := os.WriteFile(temporary, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, filepath.Join(root, queuePIDName)); err != nil {
		t.Fatal(err)
	}
	if err := started.Owner.Release(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(root, queuePIDName))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != fmt.Sprintf("%d\n", os.Getpid()) {
		t.Fatalf("replacement queue.pid was changed: %q", contents)
	}
}

func TestUnsafeQueuePIDLeavesSymlinkTargetAndHardlinkAlone(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, []byte("preserve\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, queuePIDName)); err != nil {
			t.Fatal(err)
		}
		_, err := Acquire(root)
		var typed *Error
		if !errors.As(err, &typed) || typed.Reason != ReasonLockUnavailable {
			t.Fatalf("Acquire() error = %v, want unsafe lock refusal", err)
		}
		contents, err := os.ReadFile(target)
		if err != nil || string(contents) != "preserve\n" {
			t.Fatalf("symlink target = %q, %v", contents, err)
		}
	})

	t.Run("hardlink", func(t *testing.T) {
		root := t.TempDir()
		source := filepath.Join(root, "source")
		if err := os.WriteFile(source, []byte("2147483647\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(source, filepath.Join(root, queuePIDName)); err != nil {
			t.Fatal(err)
		}
		_, err := Acquire(root)
		if err == nil {
			t.Fatal("Acquire() unexpectedly accepted a hard-linked queue.pid")
		}
		contents, err := os.ReadFile(source)
		if err != nil || string(contents) != "2147483647\n" {
			t.Fatalf("hard-link source = %q, %v", contents, err)
		}
	})
}

func TestRequestStopRemovesDeadOwnerAndReturnsNotRunning(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, queuePIDName), []byte("2147483647\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := RequestStop(root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Requested {
		t.Fatalf("dead queue received a stop: %#v", result)
	}
	if _, err := os.Lstat(filepath.Join(root, queuePIDName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dead queue.pid remains: %v", err)
	}
}

func TestRequestStopRefusesHardLinkedStopMarker(t *testing.T) {
	root := t.TempDir()
	started, err := Acquire(root)
	if err != nil || started.Owner == nil {
		t.Fatalf("Acquire() = %#v, %v", started, err)
	}
	defer started.Owner.Release()
	target := filepath.Join(root, "stop-target")
	if err := os.WriteFile(target, []byte("preserve\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, filepath.Join(root, queueStopName)); err != nil {
		t.Fatal(err)
	}
	result, err := RequestStop(root)
	var typed *Error
	if !errors.As(err, &typed) || typed.Reason != ReasonStopWriteFailed {
		t.Fatalf("RequestStop() = %#v, %v; want safe publication refusal", result, err)
	}
	contents, err := os.ReadFile(target)
	if err != nil || string(contents) != "preserve\n" {
		t.Fatalf("stop marker hardlink target = %q, %v", contents, err)
	}
}

func TestDetachRefusesMissingInstalledBinary(t *testing.T) {
	_, err := Detach(DetachSpec{StateRoot: t.TempDir(), Executable: filepath.Join(t.TempDir(), "missing-kogen")})
	var typed *Error
	if !errors.As(err, &typed) || typed.Reason != ReasonDetachUnavailable {
		t.Fatalf("Detach() error = %v, want detach_unavailable", err)
	}
	if !strings.Contains(err.Error(), "--detach needs an installed kogen") {
		t.Fatalf("Detach() error = %q", err)
	}
}

func TestDetachedLaunchUsesNewSessionAndAppendsOutput(t *testing.T) {
	if os.Getenv("KOGEN_LOCK_DETACH_HELPER") == "1" {
		root := os.Getenv("KOGEN_LOCK_DETACH_ROOT")
		if root == "" {
			os.Exit(88)
		}
		contents := []byte(strconv.Itoa(os.Getpid()) + "\n")
		if err := os.WriteFile(filepath.Join(root, queuePIDName), contents, 0o600); err != nil {
			os.Exit(89)
		}
		_, _ = fmt.Fprintln(os.Stdout, "detached helper output")
		time.Sleep(700 * time.Millisecond)
		_ = os.Remove(filepath.Join(root, queuePIDName))
		os.Exit(0)
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, queueLogName), []byte("prior log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "KOGEN_LOCK_DETACH_HELPER=1", "KOGEN_LOCK_DETACH_ROOT="+root)
	result, err := Detach(DetachSpec{
		StateRoot:      root,
		Executable:     executable,
		Args:           []string{"-test.run=^TestDetachedLaunchUsesNewSessionAndAppendsOutput$"},
		Dir:            root,
		Env:            env,
		StartupTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := StateRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if result.PID <= 0 || result.AlreadyRunning || result.LogPath != filepath.Join(canonicalRoot, queueLogName) {
		t.Fatalf("Detach() = %#v", result)
	}
	if session, err := unix.Getsid(result.PID); err == nil && session != result.PID {
		t.Fatalf("detached pid %d has session %d", result.PID, session)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(result.LogPath)
		if err == nil && strings.Contains(string(contents), "prior log\n") && strings.Contains(string(contents), "detached helper output") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	contents, _ := os.ReadFile(result.LogPath)
	t.Fatalf("detached log did not receive helper output: %q", contents)
}

func TestAcquireRefusesNonregularQueuePID(t *testing.T) {
	root := t.TempDir()
	// A directory at queue.pid is not a stale regular lock and must be preserved.
	if err := os.Mkdir(filepath.Join(root, queuePIDName), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := Acquire(root)
	var typed *Error
	if !errors.As(err, &typed) || typed.Reason != ReasonLockUnavailable {
		t.Fatalf("Acquire() error = %v, want queue_lock_unavailable", err)
	}
	if info, err := os.Lstat(filepath.Join(root, queuePIDName)); err != nil || !info.IsDir() {
		t.Fatalf("queue.pid directory was changed: %v", err)
	}
}

func TestQueuePIDUsesSafeBoundedDecimal(t *testing.T) {
	for _, value := range []string{"0\n", "-1\n", " 1\n", "1 2\n", "1", strings.Repeat("9", maxOwnerFile+1)} {
		fd, err := unix.Open(filepath.Join(t.TempDir(), "owner"), unix.O_CREAT|unix.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := unix.Write(fd, []byte(value)); err != nil {
			t.Fatal(err)
		}
		pid, valid, err := readOwnerPID(fd)
		_ = unix.Close(fd)
		if err != nil || valid || pid != 0 {
			t.Errorf("readOwnerPID(%q) = %d, %t, %v", value, pid, valid, err)
		}
	}
}
