//go:build !darwin && !linux

package single

import "errors"

func currentProcessStartedMS() (int64, error) {
	return 0, errors.New("native process identity is unavailable on this platform")
}
