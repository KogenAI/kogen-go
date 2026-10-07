//go:build linux

package workspace

import (
	"errors"

	"golang.org/x/sys/unix"
)

func cloneFileAt(sourceFD, destinationParent int, name string, mode uint32) (bool, error) {
	destinationFD, err := unix.Openat(destinationParent, name, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return false, err
	}
	cloneErr := unix.IoctlFileClone(destinationFD, sourceFD)
	if cloneErr != nil {
		closeErr := unix.Close(destinationFD)
		_ = unix.Unlinkat(destinationParent, name, 0)
		if cloneUnavailable(cloneErr) {
			return false, errCOWUnsupported
		}
		return false, errors.Join(cloneErr, closeErr)
	}
	if err := unix.Fchmod(destinationFD, mode); err != nil {
		_ = unix.Close(destinationFD)
		_ = unix.Unlinkat(destinationParent, name, 0)
		return false, err
	}
	if err := unix.Fsync(destinationFD); err != nil {
		_ = unix.Close(destinationFD)
		_ = unix.Unlinkat(destinationParent, name, 0)
		return false, err
	}
	if err := unix.Close(destinationFD); err != nil {
		_ = unix.Unlinkat(destinationParent, name, 0)
		return false, err
	}
	return true, nil
}

func cloneUnavailable(err error) bool {
	return errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOTSUP) ||
		errors.Is(err, unix.EXDEV) || errors.Is(err, unix.EINVAL) ||
		errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.ENOTTY)
}
