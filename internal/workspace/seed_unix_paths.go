//go:build darwin || linux

package workspace

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

func createWorkspaceDirectory(root, leaf string) error {
	rootFD, err := openDirectoryPath(root)
	if err != nil {
		return err
	}
	defer unix.Close(rootFD)
	return unix.Mkdirat(rootFD, leaf, 0o700)
}

func ensureDirectoryAt(rootFD int, relative string, mode uint32) (int, error) {
	parts := strings.Split(relative, "/")
	currentFD, err := unix.Dup(rootFD)
	if err != nil {
		return -1, err
	}
	for _, part := range parts {
		nextFD, openErr := unix.Openat(currentFD, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			var stat unix.Stat_t
			if statErr := unix.Fstatat(currentFD, part, &stat, unix.AT_SYMLINK_NOFOLLOW); statErr == nil {
				if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
					_ = unix.Close(currentFD)
					return -1, openErr
				}
				if removeErr := removeEntry(currentFD, part); removeErr != nil {
					_ = unix.Close(currentFD)
					return -1, removeErr
				}
			} else if !errors.Is(statErr, unix.ENOENT) {
				_ = unix.Close(currentFD)
				return -1, statErr
			}
			if mkdirErr := unix.Mkdirat(currentFD, part, mode); mkdirErr != nil {
				_ = unix.Close(currentFD)
				return -1, mkdirErr
			}
			nextFD, openErr = unix.Openat(currentFD, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		_ = unix.Close(currentFD)
		if openErr != nil {
			return -1, openErr
		}
		currentFD = nextFD
	}
	return currentFD, nil
}

func ensureDirectoryEntry(parentFD int, name string, mode uint32) (int, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err == nil {
		// Seed targets remain writable while their children are copied.
		if err := unix.Fchmod(fd, 0o700); err != nil {
			_ = unix.Close(fd)
			return -1, err
		}
		return fd, nil
	}
	if !errors.Is(err, unix.ENOENT) {
		var stat unix.Stat_t
		if statErr := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); statErr == nil {
			if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
				return -1, err
			}
			if removeErr := removeEntry(parentFD, name); removeErr != nil {
				return -1, removeErr
			}
		} else if !errors.Is(statErr, unix.ENOENT) {
			return -1, statErr
		}
	}
	if err := unix.Mkdirat(parentFD, name, mode); err != nil {
		return -1, err
	}
	return unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
}

func openDirectoryPath(value string) (int, error) {
	if !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return -1, errors.New("directory root must be clean and absolute")
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	trimmed := strings.TrimPrefix(value, string(filepath.Separator))
	if trimmed == "" {
		return fd, nil
	}
	for _, part := range strings.Split(trimmed, string(filepath.Separator)) {
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}

func openDirectoryAt(rootFD int, relative string) (int, error) {
	if err := validateWorkspacePath(relative); err != nil {
		return -1, err
	}
	fd, err := unix.Dup(rootFD)
	if err != nil {
		return -1, err
	}
	for _, part := range strings.Split(relative, "/") {
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}

func readDirectory(fd int) ([]string, error) {
	duplicate, err := unix.Openat(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(duplicate), "workspace-seed-directory")
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

func removeEntry(parentFD int, name string) error {
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return nil
	} else if err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
		directoryFD, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		entries, err := readDirectory(directoryFD)
		if err != nil {
			_ = unix.Close(directoryFD)
			return err
		}
		for _, entry := range entries {
			if err := removeEntry(directoryFD, entry); err != nil {
				_ = unix.Close(directoryFD)
				return err
			}
		}
		if err := unix.Close(directoryFD); err != nil {
			return err
		}
		return unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR)
	}
	return unix.Unlinkat(parentFD, name, 0)
}

func readlinkAt(parentFD int, name string) (string, error) {
	buffer := make([]byte, 256)
	for len(buffer) <= 1<<20 {
		n, err := unix.Readlinkat(parentFD, name, buffer)
		if err != nil {
			return "", err
		}
		if n < len(buffer) {
			return string(buffer[:n]), nil
		}
		buffer = make([]byte, len(buffer)*2)
	}
	return "", errors.New("symlink target exceeds the supported size")
}

func validateSeedSymlink(relative, target string) error {
	if target == "" || filepath.IsAbs(target) || strings.ContainsRune(target, '\x00') {
		return fmt.Errorf("workspace: setup symlink %q has an absolute or empty target", relative)
	}
	resolved := path.Clean(path.Join(path.Dir(relative), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") || path.IsAbs(resolved) {
		return fmt.Errorf("workspace: setup symlink %q escapes its copied directory", relative)
	}
	return nil
}

func temporaryLeaf() (string, error) {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return ".kogen-seed-" + hex.EncodeToString(random[:]), nil
}
