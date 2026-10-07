//go:build linux

package lock

import (
	"errors"
	"os"
	"strconv"
	"strings"

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
	contents, readErr := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if errors.Is(readErr, os.ErrNotExist) {
		return false, nil
	}
	if readErr != nil {
		// kill(2) already established that the process exists. A procfs policy
		// denial must not allow another queue to steal its live lock.
		return true, nil
	}
	closeParen := strings.LastIndexByte(string(contents), ')')
	if closeParen < 0 || closeParen+1 >= len(contents) {
		return false, errors.New("malformed Linux process identity")
	}
	fields := strings.Fields(string(contents[closeParen+1:]))
	if len(fields) == 0 {
		return false, errors.New("short Linux process identity")
	}
	// Zombie and dead processes cannot own or service a queue stop request.
	return fields[0] != "Z" && fields[0] != "X" && fields[0] != "x", nil
}
