//go:build darwin || linux

package lock

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Detach starts queue start in a new session, with no stdin and combined
// stdout/stderr appended to queue.log. It waits briefly for the child to own
// queue.pid so callers do not report a launch that immediately failed.
func Detach(spec DetachSpec) (DetachResult, error) {
	dirFD, rootPath, err := openStateRoot(spec.StateRoot)
	if err != nil {
		return DetachResult{}, failure(ReasonDetachUnavailable, "--detach needs an installed kogen; run kogen queue start in the background instead", err)
	}
	defer unix.Close(dirFD)
	if pid, live, err := liveOwnerAt(dirFD); err != nil {
		return DetachResult{}, failure(ReasonDetachUnavailable, "could not inspect the queue owner before detach", err)
	} else if live {
		return DetachResult{PID: pid, AlreadyRunning: true}, nil
	}

	executable, err := detachExecutable(spec.Executable)
	if err != nil {
		return DetachResult{}, failure(ReasonDetachUnavailable, "--detach needs an installed kogen; run kogen queue start in the background instead", err)
	}
	logFile, err := openLogAppend(dirFD)
	if err != nil {
		return DetachResult{}, failure(ReasonDetachUnavailable, "could not open queue.log for detached output", err)
	}
	defer logFile.Close()
	nullInput, err := os.Open(os.DevNull)
	if err != nil {
		return DetachResult{}, failure(ReasonDetachUnavailable, "could not open /dev/null for detached stdin", err)
	}
	defer nullInput.Close()
	args := append([]string(nil), spec.Args...)
	if args == nil {
		args = []string{"queue", "start"}
	}
	command := exec.Command(executable, args...)
	command.Dir = spec.Dir
	command.Stdin = nullInput
	command.Stdout = logFile
	command.Stderr = logFile
	if spec.Env != nil {
		command.Env = append([]string(nil), spec.Env...)
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return DetachResult{}, failure(ReasonDetachUnavailable, "--detach needs an installed kogen; run kogen queue start in the background instead", err)
	}
	pid := command.Process.Pid
	if err := command.Process.Release(); err != nil {
		return DetachResult{}, failure(ReasonDetachUnavailable, "could not detach the queue process", err)
	}

	startupTimeout := spec.StartupTimeout
	if startupTimeout <= 0 {
		startupTimeout = startupWait
	}
	deadline := time.Now().Add(startupTimeout)
	for {
		ownerPID, live, err := liveOwnerAt(dirFD)
		if err != nil {
			return DetachResult{}, failure(ReasonDetachUnavailable, "could not verify detached queue ownership", err)
		}
		if live {
			if ownerPID == pid {
				return DetachResult{PID: pid, LogPath: filepath.Join(rootPath, queueLogName)}, nil
			}
			return DetachResult{PID: ownerPID, AlreadyRunning: true}, nil
		}
		alive, probeErr := processAlive(pid)
		if probeErr != nil {
			return DetachResult{}, failure(ReasonDetachUnavailable, "could not verify the detached process", probeErr)
		}
		if !alive {
			return DetachResult{}, failure(ReasonDetachUnavailable, "the detached queue did not acquire queue.pid", nil)
		}
		if time.Now().After(deadline) {
			return DetachResult{}, failure(ReasonDetachUnavailable, "the detached queue did not acquire queue.pid before the startup deadline", nil)
		}
		time.Sleep(startupPoll)
	}
}

func liveOwnerAt(dirFD int) (int, bool, error) {
	pid, id, valid, err := readOwnerAt(dirFD)
	if err != nil || !valid {
		return 0, false, err
	}
	if !entryMatches(dirFD, queuePIDName, id) {
		return 0, false, nil
	}
	alive, err := processAlive(pid)
	if err != nil || !alive {
		return pid, false, err
	}
	return pid, true, nil
}

func detachExecutable(explicit string) (string, error) {
	if explicit != "" {
		path, err := filepath.Abs(explicit)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			return "", errors.New("detached executable is not an executable regular file")
		}
		return path, nil
	}
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if filepath.Base(path) != "kogen" {
		return "", errors.New("the current executable is not an installed kogen binary")
	}
	if tempRoot, tempErr := filepath.EvalSymlinks(os.TempDir()); tempErr == nil {
		tempRoot = filepath.Clean(tempRoot)
		rel, relErr := filepath.Rel(tempRoot, path)
		if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", errors.New("the current kogen executable is in a temporary build directory")
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("the installed kogen binary is not executable")
	}
	return path, nil
}

func openLogAppend(dirFD int) (*os.File, error) {
	fd, err := unix.Openat(dirFD, queueLogName,
		unix.O_WRONLY|unix.O_APPEND|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		ownerFileMode)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		_ = unix.Close(fd)
		return nil, errors.New("queue.log must be a single-link regular file")
	}
	if err := unix.Fchmod(fd, ownerFileMode); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if err := unix.Fsync(dirFD); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	file := os.NewFile(uintptr(fd), queueLogName)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("could not wrap queue.log descriptor")
	}
	return file, nil
}
