//go:build darwin || linux

package refresh

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	ownerFileName = "owner"
	lockWaitMS    = 90_000
	lockStaleMS   = 60_000
	lockPoll      = 25 * time.Millisecond
	ownerLimit    = 4096
)

// Lock owns one provider/label refresh directory until Release. Its String
// methods deliberately omit the owner token and filesystem location.
type Lock struct {
	mu       sync.Mutex
	parentFD int
	dirFD    int
	ownerFD  int
	name     string
	token    string
	released bool
}

func (*Lock) String() string   { return "refresh.Lock{REDACTED}" }
func (*Lock) GoString() string { return "refresh.Lock{REDACTED}" }

// Acquire takes the cross-process lock at ~/.kogen/locks/<provider>-<label>.lock.
// It honors context cancellation and the scaled 90 second provider wait limit.
// The owner file is first published empty, then populated with pid, start time,
// and an unpredictable ownership token. Empty and malformed owner files use
// the lock directory's modification time for stale detection.
func Acquire(ctx context.Context, home, provider, label string) (*Lock, error) {
	if ctx == nil || home == "" || (provider != "chatgpt" && provider != "grok") || !validLabel(label) {
		return nil, ErrInvalidLock
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	parentFD, err := openLockParent(home)
	if err != nil {
		return nil, err
	}

	name := provider + "-" + label + ".lock"
	waitFor := scaledLockDuration(lockWaitMS)
	staleAfter := scaledLockDuration(lockStaleMS)
	started := time.Now()
	deadline := started.Add(waitFor)
	for {
		if err := ctx.Err(); err != nil {
			_ = unix.Close(parentFD)
			return nil, err
		}
		if time.Until(deadline) <= 0 {
			_ = unix.Close(parentFD)
			return nil, &LockTimeoutError{Provider: provider}
		}

		if err := unix.Mkdirat(parentFD, name, 0o700); err == nil {
			if err := unix.Fsync(parentFD); err != nil {
				_ = unix.Close(parentFD)
				return nil, lockFailure("could not publish credential lock")
			}
			lock, err := initializeLock(ctx, parentFD, name, deadline)
			if err == nil {
				return lock, nil
			}
			if errors.Is(err, errLockPathChanged) {
				continue
			}
			_ = unix.Close(parentFD)
			if errors.Is(err, ErrLockWaitTimeout) {
				return nil, &LockTimeoutError{Provider: provider}
			}
			return nil, err
		} else if !errors.Is(err, unix.EEXIST) {
			_ = unix.Close(parentFD)
			return nil, lockFailure("could not acquire credential lock")
		}

		reaped, err := reapIfStale(parentFD, name, staleAfter)
		if err != nil {
			_ = unix.Close(parentFD)
			return nil, err
		}
		if reaped {
			continue
		}
		if err := waitForPoll(ctx, deadline, provider); err != nil {
			_ = unix.Close(parentFD)
			return nil, err
		}
	}
}

// Release releases only the directory whose owner file still carries this
// lock's token. It is safe to call more than once.
func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	l.released = true
	defer l.closeFDs()

	contents, err := readOwner(l.ownerFD)
	if err != nil || ownerToken(contents) != l.token {
		return ErrLockOwnership
	}
	if !pathIsSameDirectory(l.parentFD, l.name, l.dirFD) {
		return ErrLockOwnership
	}
	if err := unix.Unlinkat(l.dirFD, ownerFileName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return lockFailure("could not release credential lock")
	}
	if err := unix.Fsync(l.dirFD); err != nil {
		return lockFailure("could not release credential lock")
	}
	if err := unix.Unlinkat(l.parentFD, l.name, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
		return lockFailure("could not release credential lock")
	}
	if err := unix.Fsync(l.parentFD); err != nil {
		return lockFailure("could not release credential lock")
	}
	return nil
}

func (l *Lock) closeFDs() {
	if l.ownerFD >= 0 {
		_ = unix.Flock(l.ownerFD, unix.LOCK_UN)
		_ = unix.Close(l.ownerFD)
		l.ownerFD = -1
	}
	if l.dirFD >= 0 {
		_ = unix.Close(l.dirFD)
		l.dirFD = -1
	}
	if l.parentFD >= 0 {
		_ = unix.Close(l.parentFD)
		l.parentFD = -1
	}
}

func initializeLock(ctx context.Context, parentFD int, name string, deadline time.Time) (*Lock, error) {
	dirFD, err := openPrivateDirAt(parentFD, name)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, errLockPathChanged
		}
		return nil, lockFailure("could not secure credential lock")
	}
	if !pathIsSameDirectory(parentFD, name, dirFD) {
		_ = unix.Close(dirFD)
		return nil, errLockPathChanged
	}
	ownerFD, err := unix.Openat(dirFD, ownerFileName,
		unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		_ = unix.Close(dirFD)
		if errors.Is(err, unix.EEXIST) || errors.Is(err, unix.ENOENT) {
			return nil, errLockPathChanged
		}
		return nil, lockFailure("could not publish credential lock")
	}
	if err := validateOwnerFile(ownerFD); err != nil {
		_ = unix.Close(ownerFD)
		_ = unix.Close(dirFD)
		return nil, lockFailure("could not secure credential lock")
	}
	if err := unix.Fsync(dirFD); err != nil {
		_ = unix.Close(ownerFD)
		_ = unix.Close(dirFD)
		return nil, lockFailure("could not publish credential lock")
	}
	if err := acquireOwnerFile(ctx, ownerFD, deadline); err != nil {
		// A competing waiter may briefly take the empty owner's flock before
		// this creator. Only clean up when this descriptor acquired that flock.
		_ = unix.Close(ownerFD)
		_ = unix.Close(dirFD)
		return nil, err
	}
	token, err := randomLockToken()
	if err != nil {
		cleanupOwnedDirectory(parentFD, name, dirFD, ownerFD)
		_ = unix.Flock(ownerFD, unix.LOCK_UN)
		_ = unix.Close(ownerFD)
		_ = unix.Close(dirFD)
		return nil, lockFailure("could not publish credential lock")
	}
	owner := []byte(fmt.Sprintf("%d %d %s\n", os.Getpid(), time.Now().UnixMilli(), token))
	if err := writeOwner(ownerFD, owner); err != nil || unix.Fsync(dirFD) != nil {
		cleanupOwnedDirectory(parentFD, name, dirFD, ownerFD)
		_ = unix.Flock(ownerFD, unix.LOCK_UN)
		_ = unix.Close(ownerFD)
		_ = unix.Close(dirFD)
		return nil, lockFailure("could not publish credential lock")
	}
	return &Lock{parentFD: parentFD, dirFD: dirFD, ownerFD: ownerFD, name: name, token: token}, nil
}

func cleanupOwnedDirectory(parentFD int, name string, dirFD, ownerFD int) {
	if !pathIsSameDirectory(parentFD, name, dirFD) {
		return
	}
	if err := unix.Unlinkat(dirFD, ownerFileName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return
	}
	if unix.Fsync(dirFD) != nil {
		return
	}
	if err := unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
		return
	}
	_ = unix.Fsync(parentFD)
}

func reapIfStale(parentFD int, name string, staleAfter time.Duration) (bool, error) {
	dirFD, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return true, nil
		}
		return false, lockFailure("could not secure credential lock")
	}
	defer unix.Close(dirFD)
	if !pathIsSameDirectory(parentFD, name, dirFD) {
		return true, nil
	}
	dirInfo, err := statFD(dirFD)
	if err != nil || !isPrivateDirectory(dirInfo) {
		return false, ErrUnsafeLock
	}

	ownerFD, err := unix.Openat(dirFD, ownerFileName,
		unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	ownerMissing := errors.Is(err, unix.ENOENT)
	if err != nil && !ownerMissing {
		return false, ErrUnsafeLock
	}
	if ownerMissing {
		if time.Since(statModTime(&dirInfo)) < staleAfter {
			return false, nil
		}
		// Claim an empty owner file before reclaiming a directory left between
		// mkdir and owner publication. O_EXCL lets only one waiter inspect it.
		ownerFD, err = unix.Openat(dirFD, ownerFileName,
			unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if err != nil {
			if errors.Is(err, unix.EEXIST) || errors.Is(err, unix.ENOENT) {
				return true, nil
			}
			return false, ErrUnsafeLock
		}
		if err := unix.Fsync(dirFD); err != nil {
			_ = unix.Close(ownerFD)
			return false, lockFailure("could not publish credential lock")
		}
	} else if err := validateOwnerFile(ownerFD); err != nil {
		_ = unix.Close(ownerFD)
		return false, ErrUnsafeLock
	}
	defer unix.Close(ownerFD)

	if err := unix.Flock(ownerFD, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return false, nil
		}
		return false, lockFailure("could not acquire credential lock")
	}
	defer unix.Flock(ownerFD, unix.LOCK_UN)

	stale := time.Since(statModTime(&dirInfo)) >= staleAfter
	if !ownerMissing {
		contents, readErr := readOwner(ownerFD)
		if readErr == nil {
			if timestamp, ok := ownerTimestamp(contents); ok {
				stale = time.Since(time.UnixMilli(timestamp)) >= staleAfter
			}
		}
	}
	if !stale {
		return false, nil
	}
	if !pathIsSameDirectory(parentFD, name, dirFD) {
		return true, nil
	}
	return true, moveStaleDirectory(parentFD, name, dirFD, ownerFD)
}

func moveStaleDirectory(parentFD int, name string, dirFD, ownerFD int) error {
	for attempt := 0; attempt < 4; attempt++ {
		token, err := randomLockToken()
		if err != nil {
			return lockFailure("could not reclaim stale credential lock")
		}
		tombstone := ".stale-" + token
		var existing unix.Stat_t
		if err := unix.Fstatat(parentFD, tombstone, &existing, unix.AT_SYMLINK_NOFOLLOW); err == nil {
			continue
		} else if !errors.Is(err, unix.ENOENT) {
			return lockFailure("could not reclaim stale credential lock")
		}
		if err := unix.Renameat(parentFD, name, parentFD, tombstone); err != nil {
			if errors.Is(err, unix.ENOENT) {
				return nil
			}
			return lockFailure("could not reclaim stale credential lock")
		}
		if err := unix.Fsync(parentFD); err != nil {
			return lockFailure("could not reclaim stale credential lock")
		}
		if !pathIsSameDirectory(parentFD, tombstone, dirFD) {
			return lockFailure("could not reclaim stale credential lock")
		}
		if err := unix.Unlinkat(dirFD, ownerFileName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return lockFailure("could not reclaim stale credential lock")
		}
		if err := unix.Fsync(dirFD); err != nil {
			return lockFailure("could not reclaim stale credential lock")
		}
		if err := unix.Unlinkat(parentFD, tombstone, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
			return lockFailure("could not reclaim stale credential lock")
		}
		if err := unix.Fsync(parentFD); err != nil {
			return lockFailure("could not reclaim stale credential lock")
		}
		return nil
	}
	return lockFailure("could not reclaim stale credential lock")
}

func openLockParent(home string) (int, error) {
	absHome, err := filepath.Abs(home)
	if err != nil {
		return -1, lockFailure("could not open credential lock directory")
	}
	resolvedHome, err := filepath.EvalSymlinks(absHome)
	if err != nil {
		return -1, lockFailure("could not open credential lock directory")
	}
	homeFD, err := unix.Open(resolvedHome, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, lockFailure("could not open credential lock directory")
	}
	stateFD, err := openOrCreatePrivateDir(homeFD, ".kogen")
	if err != nil {
		_ = unix.Close(homeFD)
		return -1, lockFailure("could not secure credential lock directory")
	}
	locksFD, err := openOrCreatePrivateDir(stateFD, "locks")
	_ = unix.Close(stateFD)
	_ = unix.Close(homeFD)
	if err != nil {
		return -1, lockFailure("could not secure credential lock directory")
	}
	return locksFD, nil
}

func openOrCreatePrivateDir(parentFD int, name string) (int, error) {
	err := unix.Mkdirat(parentFD, name, 0o700)
	if err == nil {
		if err := unix.Fsync(parentFD); err != nil {
			return -1, err
		}
	} else if !errors.Is(err, unix.EEXIST) {
		return -1, err
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	info, err := statFD(fd)
	if err != nil || !isPrivateDirectory(info) || !pathIsSameDirectory(parentFD, name, fd) {
		_ = unix.Close(fd)
		return -1, ErrUnsafeLock
	}
	return fd, nil
}

func openPrivateDirAt(parentFD int, name string) (int, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	info, err := statFD(fd)
	if err != nil || !isPrivateDirectory(info) || !pathIsSameDirectory(parentFD, name, fd) {
		_ = unix.Close(fd)
		return -1, ErrUnsafeLock
	}
	return fd, nil
}

func validateOwnerFile(fd int) error {
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil {
		return ErrUnsafeLock
	}
	if info.Mode&unix.S_IFMT != unix.S_IFREG || info.Nlink != 1 ||
		info.Uid != uint32(os.Geteuid()) || info.Mode&0o777 != 0o600 || info.Size > ownerLimit {
		return ErrUnsafeLock
	}
	return nil
}

func isPrivateDirectory(info unix.Stat_t) bool {
	return info.Mode&unix.S_IFMT == unix.S_IFDIR && info.Uid == uint32(os.Geteuid()) && info.Mode&0o777 == 0o700
}

func statFD(fd int) (unix.Stat_t, error) {
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil {
		return unix.Stat_t{}, err
	}
	return info, nil
}

func pathIsSameDirectory(parentFD int, name string, dirFD int) bool {
	var pathInfo, fdInfo unix.Stat_t
	if unix.Fstatat(parentFD, name, &pathInfo, unix.AT_SYMLINK_NOFOLLOW) != nil || unix.Fstat(dirFD, &fdInfo) != nil {
		return false
	}
	return pathInfo.Mode&unix.S_IFMT == unix.S_IFDIR && pathInfo.Dev == fdInfo.Dev && pathInfo.Ino == fdInfo.Ino
}

func acquireOwnerFile(ctx context.Context, fd int, deadline time.Time) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err == nil {
			return nil
		} else if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return lockFailure("could not acquire credential lock")
		}
		if time.Until(deadline) <= 0 {
			return ErrLockWaitTimeout
		}
		timer := time.NewTimer(min(lockPoll, time.Until(deadline)))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func waitForPoll(ctx context.Context, deadline time.Time, provider string) error {
	if time.Until(deadline) <= 0 {
		return &LockTimeoutError{Provider: provider}
	}
	timer := time.NewTimer(min(lockPoll, time.Until(deadline)))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func readOwner(fd int) ([]byte, error) {
	buffer := make([]byte, ownerLimit+1)
	count, err := unix.Pread(fd, buffer, 0)
	if err != nil {
		return nil, err
	}
	if count > ownerLimit {
		return nil, ErrUnsafeLock
	}
	return buffer[:count], nil
}

func writeOwner(fd int, contents []byte) error {
	if err := unix.Ftruncate(fd, 0); err != nil {
		return err
	}
	for len(contents) > 0 {
		written, err := unix.Write(fd, contents)
		if err != nil {
			return err
		}
		if written == 0 {
			return errors.New("short write")
		}
		contents = contents[written:]
	}
	return unix.Fsync(fd)
}

func ownerTimestamp(contents []byte) (int64, bool) {
	fields := strings.Fields(string(contents))
	if len(fields) < 3 {
		return 0, false
	}
	if _, err := strconv.ParseInt(fields[0], 10, 32); err != nil {
		return 0, false
	}
	timestamp, err := strconv.ParseInt(fields[1], 10, 64)
	return timestamp, err == nil && timestamp >= 0 && fields[2] != ""
}

func ownerToken(contents []byte) string {
	fields := strings.Fields(string(contents))
	if len(fields) < 3 {
		return ""
	}
	return fields[2]
}

func randomLockToken() (string, error) {
	var bytes [24]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func validLabel(label string) bool {
	if len(label) < 1 || len(label) > 64 || !isASCIIAlphaNum(label[0]) {
		return false
	}
	for i := 1; i < len(label); i++ {
		b := label[i]
		if !isASCIIAlphaNum(b) && b != '.' && b != '_' && b != '-' {
			return false
		}
	}
	return true
}

func isASCIIAlphaNum(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
}

func scaledLockDuration(milliseconds int64) time.Duration {
	scale := 1.0
	if value, err := strconv.ParseFloat(os.Getenv("KOGEN_TIME_SCALE"), 64); err == nil && !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 {
		scale = value
	}
	ms := math.Floor(float64(milliseconds) * scale)
	if ms < 1 {
		ms = 1
	}
	// An extreme environment value must not turn a bounded lock wait into an
	// effectively unbounded one.
	const maximum = 24 * time.Hour
	if ms > float64(maximum/time.Millisecond) {
		return maximum
	}
	return time.Duration(ms) * time.Millisecond
}

func statModTime(info *unix.Stat_t) time.Time {
	return time.Unix(info.Mtim.Sec, info.Mtim.Nsec)
}

var errLockPathChanged = errors.New("refresh: lock path changed while opening")

func lockFailure(reason string) error { return fmt.Errorf("%w: %s", ErrLockUnavailable, reason) }
