// Package lock implements exclusive queue ownership, stop requests, and the
// detached queue process protocol.
package lock

import (
	"errors"
	"time"
)

const (
	ReasonLockFailed        = "queue_lock_failed"
	ReasonLockUnavailable   = "queue_lock_unavailable"
	ReasonOwnerProbeFailed  = "queue_owner_probe_failed"
	ReasonStopReadFailed    = "queue_stop_read_failed"
	ReasonStopWriteFailed   = "queue_stop_write_failed"
	ReasonDetachUnavailable = "detach_unavailable"
)

// Error carries a stable controller reason alongside a diagnostic cause.
type Error struct {
	Reason string
	Detail string
	Cause  error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Detail != "" {
		return e.Detail
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return e.Reason
}

func (e *Error) Unwrap() error { return e.Cause }

func failure(reason, detail string, cause error) *Error {
	if detail == "" && cause != nil {
		detail = cause.Error()
	}
	return &Error{Reason: reason, Detail: detail, Cause: cause}
}

// StartResult has either an Owner when acquired or the PID of the current
// owner. PID is zero when Owner is non-nil.
type StartResult struct {
	Owner *Owner
	PID   int
}

// StopResult reports whether a live owner received a stop marker.
type StopResult struct {
	Requested bool
	PID       int
}

// DetachSpec describes the executable invocation used to relaunch queue start.
// An empty Executable resolves the installed kogen binary from os.Executable.
// An empty Args defaults to ["queue", "start"].
type DetachSpec struct {
	StateRoot      string
	Executable     string
	Args           []string
	Dir            string
	Env            []string
	StartupTimeout time.Duration
}

// DetachResult describes a successful launch or an already-running queue.
type DetachResult struct {
	PID            int
	LogPath        string
	AlreadyRunning bool
}

// ErrReleased is returned when a released Owner is used again.
var ErrReleased = errors.New("queue lock owner has been released")
