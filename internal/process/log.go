package process

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	"kogen-go/internal/contract"
)

const outputDrainGrace = 500 * time.Millisecond

// processLog keeps an unlinked private file descriptor open while the child
// runs. Output streams into that file; after cleanup a bounded copy is
// published at path with create-only hard-link publication.
type processLog struct {
	dirFD  int
	leaf   string
	path   string
	stream *os.File
}

func openProcessLog(path string) (*processLog, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, '\x00') {
		return nil, errors.New("process: log path must be a clean absolute path")
	}
	parent, leaf := filepath.Dir(path), filepath.Base(path)
	if leaf == "." || leaf == ".." || leaf == string(filepath.Separator) || leaf == "" {
		return nil, errors.New("process: log path must name a file")
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return nil, fmt.Errorf("process: inspect private log directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("process: log directory must be a private, non-symlink directory")
	}
	canonicalParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return nil, fmt.Errorf("process: resolve private log directory: %w", err)
	}
	dirFD, err := openDirectoryNoFollow(canonicalParent)
	if err != nil {
		return nil, fmt.Errorf("process: open rooted log directory: %w", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(dirFD, &stat); err != nil {
		_ = unix.Close(dirFD)
		return nil, fmt.Errorf("process: inspect rooted log directory: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o077 != 0 {
		_ = unix.Close(dirFD)
		return nil, errors.New("process: log directory must be private")
	}
	tempName, tempFD, err := createPrivateTemp(dirFD)
	if err != nil {
		_ = unix.Close(dirFD)
		return nil, fmt.Errorf("process: create private output log: %w", err)
	}
	if err := unix.Unlinkat(dirFD, tempName, 0); err != nil {
		_ = unix.Close(tempFD)
		_ = unix.Close(dirFD)
		return nil, fmt.Errorf("process: unlink private output log: %w", err)
	}
	return &processLog{
		dirFD:  dirFD,
		leaf:   leaf,
		path:   filepath.Join(canonicalParent, leaf),
		stream: os.NewFile(uintptr(tempFD), "kogen-process-output"),
	}, nil
}

func (l *processLog) close() {
	_ = l.stream.Close()
	_ = unix.Close(l.dirFD)
}

func (l *processLog) publish(contents []byte) error {
	name, fd, err := createPrivateTemp(l.dirFD)
	if err != nil {
		return fmt.Errorf("create log publication stage: %w", err)
	}
	keep := true
	defer func() {
		if keep {
			_ = unix.Unlinkat(l.dirFD, name, 0)
		}
	}()
	stage := os.NewFile(uintptr(fd), "kogen-process-log-stage")
	defer stage.Close()
	n, err := stage.Write(contents)
	if err != nil {
		return fmt.Errorf("write log publication stage: %w", err)
	}
	if n != len(contents) {
		return io.ErrShortWrite
	}
	if err := stage.Sync(); err != nil {
		return fmt.Errorf("sync log publication stage: %w", err)
	}
	if err := unix.Linkat(l.dirFD, name, l.dirFD, l.leaf, 0); err != nil {
		return fmt.Errorf("publish process log create-only: %w", err)
	}
	if err := unix.Unlinkat(l.dirFD, name, 0); err != nil {
		return fmt.Errorf("unlink log publication stage: %w", err)
	}
	keep = false
	if err := unix.Fsync(l.dirFD); err != nil {
		return fmt.Errorf("sync log directory: %w", err)
	}
	return nil
}

type outputCapture struct {
	mu        sync.Mutex
	file      *os.File
	logLimit  int
	tailLimit int
	logSize   int
	tail      []byte
	writeErr  error
}

func newOutputCapture(file *os.File, logLimit, tailLimit int) *outputCapture {
	return &outputCapture{
		file:      file,
		logLimit:  logLimit,
		tailLimit: tailLimit,
		tail:      make([]byte, 0, tailLimit),
	}
}

func (c *outputCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	c.updateTail(p)
	logRoom := c.logLimit - c.logSize
	if logRoom <= 0 {
		return len(p), nil
	}
	chunk := p[:min(logRoom, len(p))]
	written, err := c.file.Write(chunk)
	c.logSize += written
	if err == nil && written != len(chunk) {
		err = io.ErrShortWrite
	}
	if err != nil {
		c.writeErr = err
		return written, err
	}
	return len(p), nil
}

func (c *outputCapture) updateTail(p []byte) {
	if c.tailLimit == 0 {
		return
	}
	if len(p) >= c.tailLimit {
		c.tail = append(c.tail[:0], p[len(p)-c.tailLimit:]...)
		return
	}
	excess := max(0, len(c.tail)+len(p)-c.tailLimit)
	copy(c.tail, c.tail[excess:])
	c.tail = append(c.tail[:len(c.tail)-excess], p...)
}

func (c *outputCapture) snapshot() ([]byte, []byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	tail := append([]byte(nil), c.tail...)
	if c.writeErr != nil {
		return nil, tail, c.writeErr
	}
	contents := make([]byte, c.logSize)
	if len(contents) > 0 {
		n, err := c.file.ReadAt(contents, 0)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, tail, err
		}
		if n != len(contents) {
			return nil, tail, io.ErrUnexpectedEOF
		}
	}
	return contents, tail, nil
}

func publishResult(log *processLog, capture *outputCapture, status int, timedOut, unavailable bool, started time.Time) (contract.ProcessResult, error) {
	logBytes, tail, err := capture.snapshot()
	if err != nil {
		return contract.ProcessResult{}, fmt.Errorf("process: read bounded output log: %w", err)
	}
	if err := log.publish(logBytes); err != nil {
		return contract.ProcessResult{}, fmt.Errorf("process: publish private log: %w", err)
	}
	result := contract.ProcessResult{
		ExitStatus:  &status,
		TimedOut:    timedOut,
		Unavailable: unavailable,
		OutputTail:  tail,
		LogPath:     log.path,
		Duration:    time.Since(started),
	}
	return result, nil
}

func createPrivateTemp(dirFD int) (string, int, error) {
	for range 100 {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", -1, err
		}
		name := ".kogen-process-" + hex.EncodeToString(nonce[:]) + ".tmp"
		fd, err := unix.Openat(dirFD, name, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", -1, err
		}
		if err := unix.Fchmod(fd, 0o600); err != nil {
			_ = unix.Close(fd)
			_ = unix.Unlinkat(dirFD, name, 0)
			return "", -1, err
		}
		return name, fd, nil
	}
	return "", -1, errors.New("private log name collisions")
}

func openDirectoryNoFollow(path string) (int, error) {
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	trimmed := strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator))
	if trimmed == "" {
		return fd, nil
	}
	for _, component := range strings.Split(trimmed, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		if component == ".." {
			_ = unix.Close(fd)
			return -1, errors.New("log directory contains parent traversal")
		}
		next, err := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			_ = unix.Close(fd)
			return -1, err
		}
		_ = unix.Close(fd)
		fd = next
	}
	return fd, nil
}
