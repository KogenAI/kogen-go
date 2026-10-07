package recovery

import (
	"context"
	"errors"
	"fmt"
)

// NativeOwnerProbe checks both PID liveness and the recorded process start
// second. A reused PID is dead for this run; an unavailable probe returns an
// error so the controller leaves the run untouched.
type NativeOwnerProbe struct{}

func (NativeOwnerProbe) Alive(ctx context.Context, pid, startedMS int64) (bool, error) {
	if ctx == nil || pid <= 0 || startedMS <= 0 {
		return false, errors.New("recovery: invalid owner process identity")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	actualMS, exists, err := nativeProcessStartedMS(pid)
	if err != nil {
		return false, fmt.Errorf("recovery: process identity probe failed: %w", err)
	}
	if !exists {
		return false, nil
	}
	// Owner records historically have one-second precision. Native ports return
	// the process start instant; a one-second window accounts for that persisted
	// precision while still rejecting ordinary PID reuse.
	delta := actualMS - startedMS
	if delta < 0 {
		delta = -delta
	}
	return delta <= 1_000, nil
}
