//go:build darwin || linux

package safefs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"sort"
	"syscall"

	"golang.org/x/sys/unix"
)

func (r *unixRoot) createPrivateSibling(parent, prefix string) (string, error) {
	resolved, err := r.resolve(parent, true)
	if err != nil {
		return "", err
	}
	defer resolved.close()
	parentFD := resolved.directoryFD
	closeParent := false
	if !resolved.isDir {
		parentFD, err = unix.Openat(resolved.parentFD, resolved.name,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return "", err
		}
		closeParent = true
	}
	if closeParent {
		defer unix.Close(parentFD)
	}
	for attempt := 0; attempt < 64; attempt++ {
		var suffix [16]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return "", err
		}
		leaf := prefix + hex.EncodeToString(suffix[:])
		if _, err := validateName(leaf, false); err != nil {
			return "", err
		}
		err := unix.Mkdirat(parentFD, leaf, 0o700)
		if errors.Is(err, syscall.EEXIST) {
			continue
		}
		if err != nil {
			return "", err
		}
		directoryFD, openErr := unix.Openat(parentFD, leaf,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			_ = unix.Unlinkat(parentFD, leaf, unix.AT_REMOVEDIR)
			return "", openErr
		}
		modeErr := unix.Fchmod(directoryFD, 0o700)
		syncErr := unix.Fsync(directoryFD)
		closeErr := unix.Close(directoryFD)
		if err := errors.Join(modeErr, syncErr, closeErr); err != nil {
			_ = unix.Unlinkat(parentFD, leaf, unix.AT_REMOVEDIR)
			return "", err
		}
		if err := unix.Fsync(parentFD); err != nil {
			_ = unix.Unlinkat(parentFD, leaf, unix.AT_REMOVEDIR)
			return "", err
		}
		return joinRootName(parent, leaf), nil
	}
	return "", fs.ErrExist
}

func (r *unixRoot) renameNoReplace(oldName, newName string) error {
	oldPath, err := r.resolve(oldName, false)
	if err != nil {
		return err
	}
	defer oldPath.close()
	newPath, err := r.resolve(newName, false)
	if err != nil {
		return err
	}
	defer newPath.close()
	if oldPath.isDir || newPath.isDir {
		return ErrUnsafePath
	}
	if err := renameAtNoReplace(oldPath.parentFD, oldPath.name, newPath.parentFD, newPath.name); err != nil {
		return err
	}
	if err := unix.Fsync(oldPath.parentFD); err != nil {
		return err
	}
	return unix.Fsync(newPath.parentFD)
}

func (r *unixRoot) renameExchange(oldName, newName string) (bool, error) {
	oldPath, err := r.resolve(oldName, false)
	if err != nil {
		return false, err
	}
	defer oldPath.close()
	newPath, err := r.resolve(newName, false)
	if err != nil {
		return false, err
	}
	defer newPath.close()
	if oldPath.isDir || newPath.isDir {
		return false, ErrUnsafePath
	}
	if err := renameAtExchange(oldPath.parentFD, oldPath.name, newPath.parentFD, newPath.name); err != nil {
		return false, err
	}
	if err := unix.Fsync(oldPath.parentFD); err != nil {
		return true, err
	}
	if err := unix.Fsync(newPath.parentFD); err != nil {
		return true, err
	}
	return true, nil
}

func (r *unixRoot) identity() (rootIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(r.fd, &stat); err != nil {
		return rootIdentity{}, err
	}
	return rootIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino)}, nil
}

func (r *unixRoot) openRegularNoFollow(name string) (io.ReadCloser, error) {
	resolved, err := r.resolve(name, false)
	if err != nil {
		return nil, err
	}
	defer resolved.close()
	if resolved.isDir {
		return nil, ErrUnsafeFile
	}
	return openRegularAt(resolved.parentFD, resolved.name, unix.O_RDONLY|unix.O_NONBLOCK)
}

func (r *unixRoot) readDirectoryNoFollow(name string) ([]fs.DirEntry, error) {
	resolved, err := r.resolve(name, false)
	if err != nil {
		return nil, err
	}
	defer resolved.close()
	var fd int
	if resolved.isDir {
		fd, err = unix.Dup(resolved.directoryFD)
		if err != nil {
			return nil, err
		}
		unix.CloseOnExec(fd)
	} else {
		var stat unix.Stat_t
		if err := unix.Fstatat(resolved.parentFD, resolved.name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return nil, err
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
			return nil, ErrUnsafeFile
		}
		fd, err = unix.Openat(resolved.parentFD, resolved.name,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return nil, err
		}
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, ErrUnsafePath
	}
	entries, err := file.ReadDir(-1)
	closeErr := file.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}
