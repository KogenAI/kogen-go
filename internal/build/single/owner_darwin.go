//go:build darwin

package single

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func currentProcessStartedMS() (int64, error) {
	pid := os.Getpid()
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, fmt.Errorf("read current process identity: %w", err)
	}
	if int(info.Proc.P_pid) != pid {
		return 0, errors.New("current process identity changed during probe")
	}
	started := int64(info.Proc.P_starttime.Sec)*1_000 + int64(info.Proc.P_starttime.Usec)/1_000
	if started <= 0 {
		return 0, errors.New("current process start time is unavailable")
	}
	return started, nil
}
