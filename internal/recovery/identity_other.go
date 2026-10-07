//go:build !darwin && !linux

package recovery

import "errors"

func nativeProcessStartedMS(int64) (int64, bool, error) {
	return 0, false, errors.New("recovery: native process identity is unavailable on this platform")
}
