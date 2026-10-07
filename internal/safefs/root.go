// Package safefs provides descriptor-rooted filesystem operations for controller
// data written into mutable worktrees and state directories.
package safefs

import (
	"errors"
	"io"
	"io/fs"
	"sync"

	"kogen-go/internal/contract"
)

var (
	// ErrClosed is returned after a Root has been closed.
	ErrClosed = errors.New("safefs: root is closed")
	// ErrUnsafePath identifies paths that are not safe canonical rooted names.
	ErrUnsafePath = errors.New("safefs: unsafe path")
	// ErrUnsafeFile identifies a non-regular or multiply linked file where a
	// regular controller file is required.
	ErrUnsafeFile = errors.New("safefs: unsafe file kind")
)

// PublicationMode selects whether Publish replaces an existing leaf or only
// succeeds when that leaf is absent.
type PublicationMode = contract.PublicationMode

const (
	PublicationReplace    = contract.PublicationReplace
	PublicationCreateOnly = contract.PublicationCreateOnly
)

// Root is a filesystem capability anchored to a directory opened by OpenRoot.
// Operations remain relative to that directory if its original path is moved.
// Root is safe for concurrent use; Close waits for operations already in flight.
type Root struct {
	mu       sync.RWMutex
	appendMu sync.Mutex
	ops      platformRoot
}

// OpenRoot opens path as the descriptor root for subsequent operations.
func OpenRoot(path string) (*Root, error) {
	ops, err := openPlatformRoot(path)
	if err != nil {
		return nil, err
	}
	return &Root{ops: ops}, nil
}

// Opener adapts OpenRoot to contract.RootOpener.
type Opener struct{}

func (Opener) OpenRoot(path string) (contract.RootedFS, error) {
	return OpenRoot(path)
}

var (
	_ contract.RootedFS   = (*Root)(nil)
	_ contract.RootOpener = Opener{}
)

// Close releases the root descriptor. Operations after Close return ErrClosed.
func (r *Root) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ops == nil {
		return nil
	}
	err := r.ops.close()
	r.ops = nil
	return err
}

func (r *Root) withOps(fn func(platformRoot) error) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.ops == nil {
		return ErrClosed
	}
	return fn(r.ops)
}

// OpenRead opens a regular file for reading. Symlinks are followed only while
// their resolved target remains beneath the root. FIFO and device nodes are
// rejected without waiting for a peer to open them.
func (r *Root) OpenRead(name string) (io.ReadCloser, error) {
	if _, err := validateName(name, false); err != nil {
		return nil, err
	}
	var file io.ReadCloser
	err := r.withOps(func(ops platformRoot) error {
		var err error
		file, err = ops.openRead(name)
		return err
	})
	return file, err
}

// ReadFile reads a regular rooted file completely.
func (r *Root) ReadFile(name string) ([]byte, error) {
	file, err := r.OpenRead(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

// ReadDir lists a rooted directory in name order. A final in-root symlink is
// followed; a symlink that resolves outside the root is refused.
func (r *Root) ReadDir(name string) ([]fs.DirEntry, error) {
	if _, err := validateName(name, true); err != nil {
		return nil, err
	}
	var entries []fs.DirEntry
	err := r.withOps(func(ops platformRoot) error {
		var err error
		entries, err = ops.readDir(name)
		return err
	})
	return entries, err
}

// Lstat reports a path's leaf without following a final symlink.
func (r *Root) Lstat(name string) (fs.FileInfo, error) {
	if _, err := validateName(name, true); err != nil {
		return nil, err
	}
	var info fs.FileInfo
	err := r.withOps(func(ops platformRoot) error {
		var err error
		info, err = ops.lstat(name)
		return err
	})
	return info, err
}

// Readlink returns the raw relative or absolute target of a rooted symlink.
// It does not open the target.
func (r *Root) Readlink(name string) (string, error) {
	if _, err := validateName(name, false); err != nil {
		return "", err
	}
	var target string
	err := r.withOps(func(ops platformRoot) error {
		var err error
		target, err = ops.readlink(name)
		return err
	})
	return target, err
}

// MkdirAll creates the named directory and its parents with descriptor-rooted
// traversal. Existing in-root directory symlinks are allowed.
func (r *Root) MkdirAll(name string, perm fs.FileMode) error {
	if _, err := validateName(name, true); err != nil {
		return err
	}
	return r.withOps(func(ops platformRoot) error { return ops.mkdirAll(name, perm) })
}

// Publish writes bytes to a private temporary file, syncs it, then atomically
// publishes the leaf and syncs its parent directory. Replace replaces a symlink
// leaf itself; it never opens the symlink target.
func (r *Root) Publish(name string, contents []byte, perm fs.FileMode, mode PublicationMode) error {
	if _, err := validateName(name, false); err != nil {
		return err
	}
	if mode != PublicationReplace && mode != PublicationCreateOnly {
		return errors.New("safefs: invalid publication mode")
	}
	return r.withOps(func(ops platformRoot) error { return ops.publish(name, contents, perm, mode) })
}

// Append appends bytes using copy-on-write publication. It does not mutate an
// existing inode, so a hardlink created concurrently cannot redirect writes.
func (r *Root) Append(name string, contents []byte, perm fs.FileMode) error {
	if _, err := validateName(name, false); err != nil {
		return err
	}
	r.appendMu.Lock()
	defer r.appendMu.Unlock()
	var old []byte
	err := r.withOps(func(ops platformRoot) error {
		info, err := ops.lstat(name)
		if err == nil {
			if !info.Mode().IsRegular() {
				return ErrUnsafeFile
			}
			old, err = ops.readFile(name)
			return err
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	combined := make([]byte, 0, len(old)+len(contents))
	combined = append(combined, old...)
	combined = append(combined, contents...)
	return r.withOps(func(ops platformRoot) error {
		return ops.publish(name, combined, perm, PublicationReplace)
	})
}

// Remove unlinks one rooted file, symlink, or empty directory and syncs its
// parent. It never follows a final symlink.
func (r *Root) Remove(name string) error {
	if _, err := validateName(name, false); err != nil {
		return err
	}
	return r.withOps(func(ops platformRoot) error { return ops.remove(name) })
}

// Rename atomically renames one rooted entry to another rooted name.
func (r *Root) Rename(oldName, newName string) error {
	if _, err := validateName(oldName, false); err != nil {
		return err
	}
	if _, err := validateName(newName, false); err != nil {
		return err
	}
	return r.withOps(func(ops platformRoot) error { return ops.rename(oldName, newName) })
}

// Symlink creates a relative symlink whose lexical target stays within the
// rooted tree. Operations that later follow it still resolve each component
// under the descriptor root.
func (r *Root) Symlink(target, name string) error {
	if _, err := validateName(name, false); err != nil {
		return err
	}
	return r.withOps(func(ops platformRoot) error { return ops.symlink(target, name) })
}

// SyncDir flushes a rooted directory descriptor to stable storage.
func (r *Root) SyncDir(name string) error {
	if _, err := validateName(name, true); err != nil {
		return err
	}
	return r.withOps(func(ops platformRoot) error { return ops.syncDir(name) })
}

type platformRoot interface {
	close() error
	openRead(string) (io.ReadCloser, error)
	readFile(string) ([]byte, error)
	readDir(string) ([]fs.DirEntry, error)
	lstat(string) (fs.FileInfo, error)
	readlink(string) (string, error)
	mkdirAll(string, fs.FileMode) error
	publish(string, []byte, fs.FileMode, PublicationMode) error
	remove(string) error
	rename(string, string) error
	symlink(string, string) error
	syncDir(string) error
}
