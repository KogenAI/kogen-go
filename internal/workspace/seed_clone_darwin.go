//go:build darwin

package workspace

import (
	"errors"

	"golang.org/x/sys/unix"
)

func cloneFileAt(sourceFD, destinationParent int, name string, mode uint32) (bool, error) {
	if err := unix.Fclonefileat(sourceFD, destinationParent, name, 0); err != nil {
		if cloneUnavailable(err) {
			return false, errCOWUnsupported
		}
		return false, err
	}
	destinationFD, err := unix.Openat(destinationParent, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err == nil {
		err = unix.Fchmod(destinationFD, mode)
	}
	if err == nil {
		err = unix.Fsync(destinationFD)
	}
	if destinationFD >= 0 {
		err = errors.Join(err, unix.Close(destinationFD))
	}
	if err != nil {
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
