//go:build darwin

package recovery

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func nativeProcessStartedMS(pid int64) (int64, bool, error) {
	if pid <= 0 || pid > int64(^uint32(0)>>1) {
		return 0, false, errors.New("process id is out of range")
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", int(pid))
	if err != nil {
		if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("read Darwin process identity: %w", err)
	}
	if int64(info.Proc.P_pid) != pid {
		return 0, false, nil
	}
	startedMS := int64(info.Proc.P_starttime.Sec)*1_000 + int64(info.Proc.P_starttime.Usec)/1_000
	if startedMS <= 0 {
		return 0, false, errors.New("Darwin process start time is unavailable")
	}
	return startedMS, true, nil
}
