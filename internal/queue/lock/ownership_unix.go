//go:build darwin || linux

package lock

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	queuePIDName  = "queue.pid"
	queueStopName = "queue.stop"
	queueLogName  = "queue.log"
	ownerFileMode = 0o600
	maxOwnerFile  = 128
	startupWait   = 2 * time.Second
	startupPoll   = 10 * time.Millisecond
)

// Owner is the capability returned to the queue drain that owns queue.pid.
// Its open file description remains flocked until Release completes.
type Owner struct {
	mu       sync.Mutex
	dirFD    int
	fileFD   int
	dev      uint64
	ino      uint64
	pid      int
	released bool
}

type fileID struct {
	dev uint64
	ino uint64
}

// Acquire creates queue.pid exclusively, or reports the current live owner.
// StateRoot must already exist; callers establish it before starting a drain.
func Acquire(stateRoot string) (StartResult, error) {
	dirFD, _, err := openStateRoot(stateRoot)
	if err != nil {
		return StartResult{}, failure(ReasonLockFailed, "could not open the queue state root", err)
	}
	keepRoot := false
	defer func() {
		if !keepRoot {
			_ = unix.Close(dirFD)
		}
	}()
	pid := os.Getpid()
	for attempt := 0; attempt < 2; attempt++ {
		id, publishErr := publishAtID(dirFD, queuePIDName, []byte(strconv.Itoa(pid)+"\n"), true)
		if publishErr == nil {
			fd, openErr := openOwnerFile(dirFD)
			if openErr != nil {
				_ = removeEntryIfID(dirFD, queuePIDName, id)
				return StartResult{}, failure(ReasonLockFailed, "could not open the new queue.pid", openErr)
			}
			if idFromFD(fd) != id || !entryMatches(dirFD, queuePIDName, id) {
				_ = unix.Close(fd)
				continue
			}
			if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
				_ = unix.Close(fd)
				return StartResult{}, failure(ReasonLockFailed, "could not own queue.pid", err)
			}
			if err := removeStopMarker(dirFD); err != nil {
				_ = removeEntryIfID(dirFD, queuePIDName, idFromFD(fd))
				_ = unix.Flock(fd, unix.LOCK_UN)
				_ = unix.Close(fd)
				return StartResult{}, failure(ReasonStopReadFailed, "could not clear the previous queue.stop marker", err)
			}
			owner := newOwner(dirFD, fd, pid)
			keepRoot = true
			return StartResult{Owner: owner}, nil
		}
		if id.ino != 0 {
			_ = removeEntryIfID(dirFD, queuePIDName, id)
		}
		if !errors.Is(publishErr, os.ErrExist) && !errors.Is(publishErr, unix.EEXIST) {
			return StartResult{}, failure(ReasonLockFailed, "could not create queue.pid", publishErr)
		}

		fd, openErr := openOwnerFile(dirFD)
		if errors.Is(openErr, unix.ENOENT) {
			continue
		}
		if openErr != nil {
			return StartResult{}, failure(ReasonLockUnavailable, "queue.pid is not a safe regular owner file", openErr)
		}
		id = idFromFD(fd)
		ownerPID, valid, readErr := readOwnerPID(fd)
		if readErr != nil {
			_ = unix.Close(fd)
			return StartResult{}, failure(ReasonLockUnavailable, "could not read queue.pid", readErr)
		}
		if valid {
			alive, probeErr := processAlive(ownerPID)
			if probeErr != nil {
				_ = unix.Close(fd)
				return StartResult{}, failure(ReasonOwnerProbeFailed, "could not determine whether the queue owner is live", probeErr)
			}
			if alive && entryMatches(dirFD, queuePIDName, id) {
				_ = unix.Close(fd)
				return StartResult{PID: ownerPID}, nil
			}
		}
		if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
			if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
				ownerPID, valid, readErr := readOwnerPID(fd)
				_ = unix.Close(fd)
				if readErr != nil {
					return StartResult{}, failure(ReasonLockUnavailable, "could not read queue.pid", readErr)
				}
				if valid {
					alive, probeErr := processAlive(ownerPID)
					if probeErr != nil {
						return StartResult{}, failure(ReasonOwnerProbeFailed, "could not determine whether the queue owner is live", probeErr)
					}
					if alive && entryMatches(dirFD, queuePIDName, id) {
						return StartResult{PID: ownerPID}, nil
					}
				}
				continue
			}
			_ = unix.Close(fd)
			return StartResult{}, failure(ReasonLockUnavailable, "could not coordinate stale queue.pid takeover", err)
		}
		if !entryMatches(dirFD, queuePIDName, id) {
			_ = unix.Flock(fd, unix.LOCK_UN)
			_ = unix.Close(fd)
			continue
		}
		ownerPID, valid, readErr = readOwnerPID(fd)
		if readErr != nil {
			_ = unix.Flock(fd, unix.LOCK_UN)
			_ = unix.Close(fd)
			return StartResult{}, failure(ReasonLockUnavailable, "could not read queue.pid", readErr)
		}
		if valid {
			alive, probeErr := processAlive(ownerPID)
			if probeErr != nil {
				_ = unix.Flock(fd, unix.LOCK_UN)
				_ = unix.Close(fd)
				return StartResult{}, failure(ReasonOwnerProbeFailed, "could not determine whether the queue owner is live", probeErr)
			}
			if alive {
				_ = unix.Flock(fd, unix.LOCK_UN)
				_ = unix.Close(fd)
				return StartResult{PID: ownerPID}, nil
			}
		}
		if err := removeEntryIfID(dirFD, queuePIDName, id); err != nil && !errors.Is(err, unix.ENOENT) {
			_ = unix.Flock(fd, unix.LOCK_UN)
			_ = unix.Close(fd)
			return StartResult{}, failure(ReasonLockUnavailable, "could not remove the dead queue owner", err)
		}
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = unix.Close(fd)
	}
	return StartResult{}, failure(ReasonLockUnavailable, "could not take over a stale queue lock after two attempts", nil)
}

func newOwner(dirFD, fileFD, pid int) *Owner {
	id := idFromFD(fileFD)
	return &Owner{dirFD: dirFD, fileFD: fileFD, dev: id.dev, ino: id.ino, pid: pid}
}

// PID returns the process id recorded in queue.pid.
func (o *Owner) PID() int {
	if o == nil {
		return 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.pid
}

// StopRequested reports whether queue.stop exists at the rooted state path.
func (o *Owner) StopRequested() (bool, error) {
	if o == nil {
		return false, ErrReleased
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.released {
		return false, ErrReleased
	}
	var stat unix.Stat_t
	err := unix.Fstatat(o.dirFD, queueStopName, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, failure(ReasonStopReadFailed, "could not inspect queue.stop", err)
	}
	return true, nil
}

// Release removes queue.pid only while the path still names this owner's exact
// inode and contents. A replacement lock is left untouched.
func (o *Owner) Release() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.released {
		return nil
	}
	var result error
	if entryMatches(o.dirFD, queuePIDName, fileID{dev: o.dev, ino: o.ino}) {
		pid, valid, err := readOwnerPID(o.fileFD)
		if err != nil {
			result = failure(ReasonLockUnavailable, "could not verify queue.pid ownership before release", err)
		} else if valid && pid == o.pid {
			if err := removeEntryIfID(o.dirFD, queuePIDName, fileID{dev: o.dev, ino: o.ino}); err != nil && !errors.Is(err, unix.ENOENT) {
				result = failure(ReasonLockUnavailable, "could not release queue.pid", err)
			}
		}
	}
	if err := unix.Flock(o.fileFD, unix.LOCK_UN); result == nil && err != nil {
		result = failure(ReasonLockUnavailable, "could not unlock queue.pid", err)
	}
	if err := unix.Close(o.fileFD); result == nil && err != nil {
		result = failure(ReasonLockUnavailable, "could not close queue.pid", err)
	}
	if err := unix.Close(o.dirFD); result == nil && err != nil {
		result = failure(ReasonLockUnavailable, "could not close the queue state root", err)
	}
	o.released = true
	return result
}

// RequestStop writes the durable stop marker only when queue.pid identifies a
// live owner. A dead lock is removed after the inode is locked and rechecked.
func RequestStop(stateRoot string) (StopResult, error) {
	dirFD, _, err := openStateRoot(stateRoot)
	if err != nil {
		return StopResult{}, failure(ReasonStopReadFailed, "could not open the queue state root", err)
	}
	defer unix.Close(dirFD)
	for attempt := 0; attempt < 2; attempt++ {
		fd, err := openOwnerFile(dirFD)
		if errors.Is(err, unix.ENOENT) {
			return StopResult{}, nil
		}
		if err != nil {
			return StopResult{}, failure(ReasonLockUnavailable, "queue.pid is not a safe regular owner file", err)
		}
		id := idFromFD(fd)
		pid, valid, readErr := readOwnerPID(fd)
		if readErr != nil {
			_ = unix.Close(fd)
			return StopResult{}, failure(ReasonLockUnavailable, "could not read queue.pid", readErr)
		}
		if valid {
			alive, probeErr := processAlive(pid)
			if probeErr != nil {
				_ = unix.Close(fd)
				return StopResult{}, failure(ReasonOwnerProbeFailed, "could not determine whether the queue owner is live", probeErr)
			}
			if alive && entryMatches(dirFD, queuePIDName, id) {
				if err := publishAt(dirFD, queueStopName, []byte("stop\n"), false); err != nil {
					_ = unix.Close(fd)
					return StopResult{}, failure(ReasonStopWriteFailed, "could not write queue.stop", err)
				}
				_ = unix.Close(fd)
				return StopResult{Requested: true, PID: pid}, nil
			}
		}
		if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
			_ = unix.Close(fd)
			if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
				continue
			}
			return StopResult{}, failure(ReasonLockUnavailable, "could not coordinate stale queue lock removal", err)
		}
		if entryMatches(dirFD, queuePIDName, id) {
			if err := removeEntryIfID(dirFD, queuePIDName, id); err != nil && !errors.Is(err, unix.ENOENT) {
				_ = unix.Flock(fd, unix.LOCK_UN)
				_ = unix.Close(fd)
				return StopResult{}, failure(ReasonLockUnavailable, "could not remove a dead queue owner", err)
			}
		}
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = unix.Close(fd)
	}
	return StopResult{}, nil
}

func removeStopMarker(dirFD int) error {
	err := unix.Unlinkat(dirFD, queueStopName, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	return unix.Fsync(dirFD)
}

func openOwnerFile(dirFD int) (int, error) {
	fd, err := unix.Openat(dirFD, queuePIDName,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return -1, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		_ = unix.Close(fd)
		return -1, errors.New("queue.pid must be a single-link regular file")
	}
	return fd, nil
}

func readOwnerPID(fd int) (int, bool, error) {
	var buf [maxOwnerFile + 1]byte
	n, err := unix.Pread(fd, buf[:], 0)
	if err != nil {
		return 0, false, err
	}
	if n == 0 {
		return 0, false, nil
	}
	if n > maxOwnerFile {
		return 0, false, nil
	}
	text := string(buf[:n])
	if !strings.HasSuffix(text, "\n") {
		return 0, false, nil
	}
	text = strings.TrimSuffix(text, "\n")
	if text == "" || strings.ContainsAny(text, " \t\r\n") {
		return 0, false, nil
	}
	pid, err := strconv.Atoi(text)
	if err != nil || pid <= 0 {
		return 0, false, nil
	}
	return pid, true, nil
}

func idFromFD(fd int) fileID {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fileID{}
	}
	return fileID{dev: uint64(stat.Dev), ino: stat.Ino}
}

func entryMatches(dirFD int, name string, id fileID) bool {
	if id.ino == 0 {
		return false
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(dirFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false
	}
	return stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Nlink == 1 && uint64(stat.Dev) == id.dev && stat.Ino == id.ino
}

func removeEntryIfID(dirFD int, name string, id fileID) error {
	if !entryMatches(dirFD, name, id) {
		return nil
	}
	if err := unix.Unlinkat(dirFD, name, 0); err != nil {
		return err
	}
	return unix.Fsync(dirFD)
}

// publishAt safely replaces a single regular file or symlink leaf. It never
// opens a destination symlink and refuses hard-linked or special file leaves.
func publishAt(dirFD int, name string, contents []byte, createOnly bool) error {
	_, err := publishAtID(dirFD, name, contents, createOnly)
	return err
}

func publishAtID(dirFD int, name string, contents []byte, createOnly bool) (fileID, error) {
	if err := checkPublicationLeaf(dirFD, name, createOnly); err != nil {
		return fileID{}, err
	}
	temp, fd, err := createTempAt(dirFD)
	if err != nil {
		return fileID{}, err
	}
	keepTemp := true
	defer func() {
		if keepTemp {
			_ = unix.Unlinkat(dirFD, temp, 0)
		}
	}()
	if err := writeAndSync(fd, contents); err != nil {
		_ = unix.Close(fd)
		return fileID{}, err
	}
	tempID := idFromFD(fd)
	if err := unix.Close(fd); err != nil {
		return fileID{}, err
	}
	if err := checkPublicationLeaf(dirFD, name, createOnly); err != nil {
		return fileID{}, err
	}
	if createOnly {
		if err := unix.Linkat(dirFD, temp, dirFD, name, 0); err != nil {
			return fileID{}, err
		}
		if err := unix.Unlinkat(dirFD, temp, 0); err != nil {
			return tempID, err
		}
	} else if err := unix.Renameat(dirFD, temp, dirFD, name); err != nil {
		return fileID{}, err
	}
	keepTemp = false
	if err := unix.Fsync(dirFD); err != nil {
		return tempID, err
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(dirFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return tempID, err
	}
	return fileID{dev: uint64(stat.Dev), ino: stat.Ino}, nil
}

func checkPublicationLeaf(dirFD int, name string, createOnly bool) error {
	var stat unix.Stat_t
	err := unix.Fstatat(dirFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	if createOnly {
		return os.ErrExist
	}
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		if stat.Nlink != 1 {
			return errors.New("queue state file has multiple hard links")
		}
	case unix.S_IFLNK:
		// renameat replaces the symlink entry itself.
	default:
		return errors.New("queue state file is not regular")
	}
	return nil
}

func createTempAt(dirFD int) (string, int, error) {
	for attempt := 0; attempt < 64; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", -1, err
		}
		name := ".kogen-queue-" + hex.EncodeToString(random[:]) + ".tmp"
		fd, err := unix.Openat(dirFD, name,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
			ownerFileMode)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", -1, err
		}
		return name, fd, nil
	}
	return "", -1, os.ErrExist
}

func writeAndSync(fd int, contents []byte) error {
	if err := unix.Fchmod(fd, ownerFileMode); err != nil {
		return err
	}
	for len(contents) > 0 {
		n, err := unix.Write(fd, contents)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		contents = contents[n:]
	}
	return unix.Fsync(fd)
}

func openStateRoot(root string) (int, string, error) {
	if root == "" || strings.ContainsRune(root, 0) {
		return -1, "", errors.New("queue state root is empty or invalid")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return -1, "", err
	}
	abs = filepath.Clean(abs)
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return -1, "", err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return -1, "", err
	}
	resolved = filepath.Clean(resolved)
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, "", err
	}
	volume := filepath.VolumeName(resolved)
	remainder := strings.TrimPrefix(resolved, volume+string(filepath.Separator))
	for _, component := range strings.Split(remainder, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		if component == ".." {
			_ = unix.Close(fd)
			return -1, "", errors.New("queue state root must not contain parent traversal")
		}
		next, openErr := unix.Openat(fd, component,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			_ = unix.Close(fd)
			return -1, "", openErr
		}
		_ = unix.Close(fd)
		fd = next
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return -1, "", err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = unix.Close(fd)
		return -1, "", errors.New("queue state root is not a directory")
	}
	return fd, resolved, nil
}

// StateRoot returns a cleaned absolute form useful for CLI output.
func StateRoot(path string) (string, error) {
	fd, resolved, err := openStateRoot(path)
	if err != nil {
		return "", err
	}
	_ = unix.Close(fd)
	return resolved, nil
}

func readOwnerAt(dirFD int) (int, fileID, bool, error) {
	fd, err := openOwnerFile(dirFD)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return 0, fileID{}, false, nil
		}
		return 0, fileID{}, false, err
	}
	defer unix.Close(fd)
	pid, valid, err := readOwnerPID(fd)
	return pid, idFromFD(fd), valid, err
}
