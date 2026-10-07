//go:build darwin || linux

package recovery

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

type descriptorWorkspaceCleaner struct {
	mu     sync.Mutex
	rootFD int
	closed bool
}

func openWorkspaceCleaner(rootPath string) (workspaceCleaner, error) {
	fd, err := openDirectoryPath(rootPath)
	if err != nil {
		return nil, err
	}
	return &descriptorWorkspaceCleaner{rootFD: fd}, nil
}

func (c *descriptorWorkspaceCleaner) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return unix.Close(c.rootFD)
}

func (c *descriptorWorkspaceCleaner) Remove(relative string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("recovery: workspace cleanup root is closed")
	}
	return removeWorkspaceAt(c.rootFD, relative)
}

func (c *descriptorWorkspaceCleaner) ReadDir() ([]fs.DirEntry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("recovery: workspace cleanup root is closed")
	}
	fd, err := unix.Openat(c.rootFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "recovery-state-root")
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("recovery: create state root reader")
	}
	entries, readErr := file.ReadDir(-1)
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

// removeWorkspaceTree is kept as a small rooted helper for component tests.
func removeWorkspaceTree(rootPath, relative string) error {
	if !filepath.IsAbs(rootPath) || filepath.Clean(rootPath) != rootPath {
		return errors.New("recovery: unsafe workspace cleanup path")
	}
	cleaner, err := openWorkspaceCleaner(rootPath)
	if err != nil {
		return fmt.Errorf("open state root for cleanup: %w", err)
	}
	defer cleaner.Close()
	return cleaner.Remove(relative)
}

// removeWorkspaceAt deletes one state-root child using only descriptor-relative,
// no-follow operations. The caller has confirmed preservation and stopped all
// run-owned writers.
func removeWorkspaceAt(rootFD int, relative string) error {
	if !fs.ValidPath(relative) || strings.Contains(relative, "/") {
		return errors.New("recovery: unsafe workspace cleanup path")
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(rootFD, relative, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.New("recovery: workspace cleanup leaf is not a real directory")
	}
	workspaceFD, err := unix.Openat(rootFD, relative, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	removeErr := removeDirectoryContents(workspaceFD)
	closeErr := unix.Close(workspaceFD)
	if err := errors.Join(removeErr, closeErr); err != nil {
		return err
	}
	if err := unix.Unlinkat(rootFD, relative, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	return unix.Fsync(rootFD)
}

func openDirectoryPath(absolute string) (int, error) {
	if !filepath.IsAbs(absolute) || filepath.Clean(absolute) != absolute {
		return -1, errors.New("directory path must be clean and absolute")
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	relative := strings.TrimPrefix(absolute, string(filepath.Separator))
	if relative == "" {
		return fd, nil
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." {
			_ = unix.Close(fd)
			return -1, errors.New("directory path contains an unsafe component")
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}

func removeDirectoryContents(directoryFD int) error {
	copyFD, err := unix.Dup(directoryFD)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(copyFD), "recovery-workspace")
	if file == nil {
		_ = unix.Close(copyFD)
		return errors.New("recovery: create directory reader")
	}
	entries, readErr := file.ReadDir(-1)
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
			return errors.New("recovery: invalid directory entry during cleanup")
		}
		var stat unix.Stat_t
		if err := unix.Fstatat(directoryFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
			childFD, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if err != nil {
				return err
			}
			removeErr := removeDirectoryContents(childFD)
			syncErr := unix.Fsync(childFD)
			closeErr := unix.Close(childFD)
			if err := errors.Join(removeErr, syncErr, closeErr); err != nil {
				return err
			}
			if err := unix.Unlinkat(directoryFD, name, unix.AT_REMOVEDIR); err != nil {
				return err
			}
		} else {
			if err := unix.Unlinkat(directoryFD, name, 0); err != nil {
				return err
			}
		}
	}
	if err := unix.Fsync(directoryFD); err != nil {
		return err
	}
	return nil
}
