//go:build linux

package process

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func newProcessGroupAttr(pgid int) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pgid: pgid}
}

func captureProcessIdentity(pid int) (processIdentity, error) {
	if pid <= 1 {
		return processIdentity{}, errors.New("process identity requires pid greater than one")
	}
	return readProcessIdentity(pid)
}

func processIdentityCurrent(identity processIdentity) bool {
	current, err := readProcessIdentity(identity.pid)
	return err == nil && current == identity
}

func readProcessIdentity(pid int) (processIdentity, error) {
	contents, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return processIdentity{}, err
	}
	closeParen := strings.LastIndexByte(string(contents), ')')
	if closeParen < 0 || closeParen+1 >= len(contents) {
		return processIdentity{}, errors.New("malformed /proc process stat")
	}
	fields := strings.Fields(string(contents[closeParen+1:]))
	if len(fields) <= 19 {
		return processIdentity{}, errors.New("short /proc process stat")
	}
	groupID, err := strconv.Atoi(fields[2])
	if err != nil {
		return processIdentity{}, fmt.Errorf("parse process group id: %w", err)
	}
	startTime, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return processIdentity{}, fmt.Errorf("parse process start time: %w", err)
	}
	if startTime == 0 {
		return processIdentity{}, errors.New("process start time is unavailable")
	}
	return processIdentity{pid: pid, start: startTime, pgid: groupID}, nil
}

func signalProcessGroup(identity processIdentity, signal unix.Signal) error {
	if !processIdentityCurrent(identity) {
		return errors.New("process group identity no longer matches")
	}
	return unix.Kill(-identity.pgid, signal)
}

func enableChildSubreaper() error {
	return unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
}

func reapOwnedChildren() error {
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		var status unix.WaitStatus
		pid, err := unix.Wait4(-1, &status, unix.WNOHANG, nil)
		if err == unix.EINTR {
			continue
		}
		if errors.Is(err, unix.ECHILD) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reap guardian descendants: %w", err)
		}
		if pid > 0 {
			continue
		}
		if time.Now().After(deadline) {
			return errors.New("guardian descendants did not reap within 500 ms")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
