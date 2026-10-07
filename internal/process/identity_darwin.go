//go:build darwin

package process

import (
	"errors"
	"fmt"
	"syscall"

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
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return processIdentity{}, fmt.Errorf("read Darwin process identity: %w", err)
	}
	if int(info.Proc.P_pid) != pid {
		return processIdentity{}, errors.New("Darwin returned a different process identity")
	}
	start := uint64(info.Proc.P_starttime.Sec)*1_000_000 + uint64(info.Proc.P_starttime.Usec)
	if start == 0 {
		return processIdentity{}, errors.New("Darwin process start time is unavailable")
	}
	return processIdentity{pid: pid, start: start, pgid: int(info.Eproc.Pgid)}, nil
}

func signalProcessGroup(identity processIdentity, signal unix.Signal) error {
	if !processIdentityCurrent(identity) {
		return errors.New("process group identity no longer matches")
	}
	return unix.Kill(-identity.pgid, signal)
}

func enableChildSubreaper() error { return nil }

func reapOwnedChildren() error { return nil }
