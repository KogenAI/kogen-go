//go:build darwin

package lock

import (
	"errors"

	"golang.org/x/sys/unix"
)

func processAlive(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	err := unix.Kill(pid, 0)
	if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.EINVAL) {
		return false, nil
	}
	if errors.Is(err, unix.EPERM) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
