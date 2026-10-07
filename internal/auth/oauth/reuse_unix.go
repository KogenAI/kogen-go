//go:build darwin || linux

package oauth

import (
	"syscall"

	"golang.org/x/sys/unix"
)

func setReuseAddress(_ string, _ string, raw syscall.RawConn) error {
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		socketErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
	}); err != nil {
		return err
	}
	return socketErr
}
