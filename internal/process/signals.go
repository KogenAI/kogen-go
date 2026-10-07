package process

import (
	"errors"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const terminationGrace = 200 * time.Millisecond

// stopGroup gives all group members the specified grace interval after TERM,
// then sends KILL. ESRCH means the group has already exited and is success.
func stopGroup(pgid int) error {
	err := unix.Kill(-pgid, unix.SIGTERM)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	time.Sleep(terminationGrace)
	err = unix.Kill(-pgid, unix.SIGKILL)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}

func signaledStatus(signal syscall.Signal) int {
	return 128 + int(signal)
}
