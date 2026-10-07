//go:build darwin || linux

package safefs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type unixRoot struct {
	fd int
}

func openPlatformRoot(path string) (platformRoot, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return nil, ErrUnsafePath
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = unix.Close(fd)
		return nil, ErrUnsafePath
	}
	return &unixRoot{fd: fd}, nil
}

func (r *unixRoot) close() error { return unix.Close(r.fd) }

// resolve returns either a parent descriptor and leaf, or a directory
// descriptor when the requested path resolves to the root/current directory.
// It follows symlink components by reading their targets relative to the
// already-open parent descriptor. Parent traversal never reopens a path by
// name, and every opened directory uses O_NOFOLLOW.
func (r *unixRoot) resolve(name string, followFinal bool) (resolvedPath, error) {
	parts, err := validateName(name, true)
	if err != nil {
		return resolvedPath{}, err
	}
	rootFD, err := unix.Dup(r.fd)
	if err != nil {
		return resolvedPath{}, err
	}
	unix.CloseOnExec(rootFD)
	stack := []int{rootFD}
	names := make([]string, 0, len(parts))
	queue := append([]string(nil), parts...)
	links := 0

	fail := func(err error) (resolvedPath, error) {
		closeFDs(stack)
		return resolvedPath{}, err
	}
	for len(queue) > 0 {
		component := queue[0]
		queue = queue[1:]
		switch component {
		case "", ".":
			continue
		case "..":
			if len(stack) == 1 {
				return fail(ErrUnsafePath)
			}
			_ = unix.Close(stack[len(stack)-1])
			stack = stack[:len(stack)-1]
			names = names[:len(names)-1]
			continue
		}
		if forbiddenComponent(component) {
			return fail(ErrUnsafePath)
		}

		parentFD := stack[len(stack)-1]
		last := len(queue) == 0
		var stat unix.Stat_t
		if err := unix.Fstatat(parentFD, component, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if last && !followFinal && errors.Is(err, syscall.ENOENT) {
				keptParent, dupErr := unix.Dup(parentFD)
				if dupErr != nil {
					return fail(dupErr)
				}
				unix.CloseOnExec(keptParent)
				logical := append([]string(nil), names...)
				closeFDs(stack)
				return resolvedPath{parentFD: keptParent, directoryFD: -1, name: component, logicalParent: logical}, nil
			}
			return fail(err)
		}
		isSymlink := stat.Mode&unix.S_IFMT == unix.S_IFLNK
		if isSymlink && (!last || followFinal) {
			links++
			if links > 40 {
				return fail(syscall.ELOOP)
			}
			target, err := readlinkAt(parentFD, component)
			if err != nil {
				return fail(err)
			}
			targetParts, err := splitLinkTarget(target)
			if err != nil {
				return fail(err)
			}
			queue = append(targetParts, queue...)
			continue
		}

		if !last {
			if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
				return fail(syscall.ENOTDIR)
			}
			next, err := unix.Openat(parentFD, component,
				unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if err != nil {
				return fail(err)
			}
			stack = append(stack, next)
			names = append(names, component)
			continue
		}

		keptParent, err := unix.Dup(parentFD)
		if err != nil {
			return fail(err)
		}
		unix.CloseOnExec(keptParent)
		logical := append([]string(nil), names...)
		closeFDs(stack)
		return resolvedPath{parentFD: keptParent, directoryFD: -1, name: component, logicalParent: logical}, nil
	}

	directory, err := unix.Dup(stack[len(stack)-1])
	if err != nil {
		return fail(err)
	}
	unix.CloseOnExec(directory)
	logical := append([]string(nil), names...)
	closeFDs(stack)
	return resolvedPath{parentFD: -1, directoryFD: directory, isDir: true, logicalParent: logical}, nil
}

type resolvedPath struct {
	parentFD      int
	name          string
	directoryFD   int
	isDir         bool
	logicalParent []string
}

func (p resolvedPath) close() {
	if p.isDir {
		_ = unix.Close(p.directoryFD)
	} else if p.parentFD >= 0 {
		_ = unix.Close(p.parentFD)
	}
}

func closeFDs(fds []int) {
	for _, fd := range fds {
		_ = unix.Close(fd)
	}
}

func (r *unixRoot) openRead(name string) (io.ReadCloser, error) {
	resolved, err := r.resolve(name, true)
	if err != nil {
		return nil, err
	}
	defer resolved.close()
	if resolved.isDir {
		return nil, ErrUnsafeFile
	}
	return openRegularAt(resolved.parentFD, resolved.name, unix.O_RDONLY|unix.O_NONBLOCK)
}

func (r *unixRoot) readFile(name string) ([]byte, error) {
	file, err := r.openRead(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

func (r *unixRoot) readDir(name string) ([]fs.DirEntry, error) {
	resolved, err := r.resolve(name, true)
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
	_ = file.Close()
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

func (r *unixRoot) lstat(name string) (fs.FileInfo, error) {
	resolved, err := r.resolve(name, false)
	if err != nil {
		return nil, err
	}
	defer resolved.close()
	var stat unix.Stat_t
	if resolved.isDir {
		if err := unix.Fstat(resolved.directoryFD, &stat); err != nil {
			return nil, err
		}
		return makeFileInfo(".", stat), nil
	}
	if err := unix.Fstatat(resolved.parentFD, resolved.name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	return makeFileInfo(resolved.name, stat), nil
}

func (r *unixRoot) readlink(name string) (string, error) {
	resolved, err := r.resolve(name, false)
	if err != nil {
		return "", err
	}
	defer resolved.close()
	if resolved.isDir {
		return "", syscall.EINVAL
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(resolved.parentFD, resolved.name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return "", err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFLNK {
		return "", syscall.EINVAL
	}
	return readlinkAt(resolved.parentFD, resolved.name)
}

func (r *unixRoot) mkdirAll(name string, perm fs.FileMode) error {
	parts, err := validateName(name, true)
	if err != nil || len(parts) == 0 {
		return err
	}
	for i := 1; i <= len(parts); i++ {
		prefix := strings.Join(parts[:i], "/")
		resolved, err := r.resolve(prefix, true)
		if err == nil {
			if resolved.isDir {
				resolved.close()
				continue
			}
			fd, openErr := unix.Openat(resolved.parentFD, resolved.name,
				unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			resolved.close()
			if openErr != nil {
				return openErr
			}
			_ = unix.Close(fd)
			continue
		}
		if !errors.Is(err, syscall.ENOENT) {
			return err
		}
		parent, err := r.resolve(prefix, false)
		if err != nil {
			return err
		}
		if parent.isDir {
			parent.close()
			return ErrUnsafePath
		}
		mkdirErr := unix.Mkdirat(parent.parentFD, parent.name, uint32(perm.Perm()))
		if mkdirErr == nil {
			if err := unix.Fsync(parent.parentFD); err != nil {
				parent.close()
				return err
			}
		} else if !errors.Is(mkdirErr, syscall.EEXIST) {
			parent.close()
			return mkdirErr
		}
		parent.close()
		// Re-resolve after EEXIST as well: a concurrent symlink is accepted only
		// when its target is safely contained and is a directory.
		check, err := r.resolve(prefix, true)
		if err != nil {
			return err
		}
		if check.isDir {
			check.close()
			continue
		}
		fd, err := unix.Openat(check.parentFD, check.name,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		check.close()
		if err != nil {
			return err
		}
		_ = unix.Close(fd)
	}
	return nil
}

func (r *unixRoot) publish(name string, contents []byte, perm fs.FileMode, mode PublicationMode) error {
	resolved, err := r.resolve(name, false)
	if err != nil {
		return err
	}
	defer resolved.close()
	if resolved.isDir {
		return ErrUnsafePath
	}
	if err := checkPublicationLeaf(resolved.parentFD, resolved.name, mode); err != nil {
		return err
	}
	temp, file, err := createTempAt(resolved.parentFD)
	if err != nil {
		return err
	}
	keepTemp := true
	defer func() {
		if keepTemp {
			_ = unix.Unlinkat(resolved.parentFD, temp, 0)
		}
	}()
	for len(contents) > 0 {
		n, writeErr := unix.Write(file, contents)
		if writeErr == syscall.EINTR {
			continue
		}
		if writeErr != nil {
			_ = unix.Close(file)
			return writeErr
		}
		if n == 0 {
			_ = unix.Close(file)
			return io.ErrShortWrite
		}
		contents = contents[n:]
	}
	if err := unix.Fchmod(file, uint32(perm.Perm())); err != nil {
		_ = unix.Close(file)
		return err
	}
	if err := unix.Fsync(file); err != nil {
		_ = unix.Close(file)
		return err
	}
	if err := unix.Close(file); err != nil {
		return err
	}
	// Check again immediately before publication. Publication never opens the
	// destination; rename replaces a symlink directory entry itself.
	if err := checkPublicationLeaf(resolved.parentFD, resolved.name, mode); err != nil {
		return err
	}
	if mode == PublicationCreateOnly {
		if err := unix.Linkat(resolved.parentFD, temp, resolved.parentFD, resolved.name, 0); err != nil {
			return err
		}
		if err := unix.Unlinkat(resolved.parentFD, temp, 0); err != nil {
			return err
		}
	} else {
		if err := unix.Renameat(resolved.parentFD, temp, resolved.parentFD, resolved.name); err != nil {
			return err
		}
	}
	keepTemp = false
	return unix.Fsync(resolved.parentFD)
}

func (r *unixRoot) remove(name string) error {
	resolved, err := r.resolve(name, false)
	if err != nil {
		return err
	}
	defer resolved.close()
	if resolved.isDir {
		return ErrUnsafePath
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(resolved.parentFD, resolved.name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	flags := 0
	if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
		flags = unix.AT_REMOVEDIR
	}
	if err := unix.Unlinkat(resolved.parentFD, resolved.name, flags); err != nil {
		return err
	}
	return unix.Fsync(resolved.parentFD)
}

func (r *unixRoot) rename(oldName, newName string) error {
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
	if err := checkPublicationLeaf(newPath.parentFD, newPath.name, PublicationReplace); err != nil {
		return err
	}
	if err := unix.Renameat(oldPath.parentFD, oldPath.name, newPath.parentFD, newPath.name); err != nil {
		return err
	}
	if err := unix.Fsync(oldPath.parentFD); err != nil {
		return err
	}
	return unix.Fsync(newPath.parentFD)
}

func (r *unixRoot) symlink(target, name string) error {
	resolved, err := r.resolve(name, false)
	if err != nil {
		return err
	}
	defer resolved.close()
	if resolved.isDir {
		return ErrUnsafePath
	}
	if _, err := validateSymlinkTarget(resolved.logicalParent, target); err != nil {
		return err
	}
	if err := unix.Symlinkat(target, resolved.parentFD, resolved.name); err != nil {
		return err
	}
	return unix.Fsync(resolved.parentFD)
}

func (r *unixRoot) syncDir(name string) error {
	resolved, err := r.resolve(name, true)
	if err != nil {
		return err
	}
	defer resolved.close()
	var fd int
	if resolved.isDir {
		fd, err = unix.Dup(resolved.directoryFD)
		if err != nil {
			return err
		}
		unix.CloseOnExec(fd)
		defer unix.Close(fd)
	} else {
		fd, err = unix.Openat(resolved.parentFD, resolved.name,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		defer unix.Close(fd)
	}
	return unix.Fsync(fd)
}

func openRegularAt(parentFD int, name string, flags int) (*os.File, error) {
	var before unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, ErrUnsafeFile
	}
	fd, err := unix.Openat(parentFD, name, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if after.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fd)
		return nil, ErrUnsafeFile
	}
	return os.NewFile(uintptr(fd), name), nil
}

func checkPublicationLeaf(parentFD int, name string, mode PublicationMode) error {
	var stat unix.Stat_t
	err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, syscall.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	if mode == PublicationCreateOnly {
		return fs.ErrExist
	}
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		if stat.Nlink > 1 {
			return ErrUnsafeFile
		}
		return nil
	case unix.S_IFLNK:
		return nil
	default:
		return ErrUnsafeFile
	}
}

func createTempAt(parentFD int) (string, int, error) {
	for i := 0; i < 64; i++ {
		var suffix [16]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return "", -1, err
		}
		name := ".kogen-publish-" + hex.EncodeToString(suffix[:]) + ".tmp"
		fd, err := unix.Openat(parentFD, name,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if errors.Is(err, syscall.EEXIST) {
			continue
		}
		if err != nil {
			return "", -1, err
		}
		return name, fd, nil
	}
	return "", -1, fs.ErrExist
}

func readlinkAt(parentFD int, name string) (string, error) {
	buffer := make([]byte, 64*1024)
	n, err := unix.Readlinkat(parentFD, name, buffer)
	if err != nil {
		return "", err
	}
	if n == len(buffer) {
		return "", syscall.ENAMETOOLONG
	}
	return string(buffer[:n]), nil
}

type unixFileInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
	stat    unix.Stat_t
}

func makeFileInfo(name string, stat unix.Stat_t) fs.FileInfo {
	mode := fs.FileMode(stat.Mode & 0o777)
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		mode |= fs.ModeDir
	case unix.S_IFLNK:
		mode |= fs.ModeSymlink
	case unix.S_IFIFO:
		mode |= fs.ModeNamedPipe
	case unix.S_IFSOCK:
		mode |= fs.ModeSocket
	case unix.S_IFCHR:
		mode |= fs.ModeDevice | fs.ModeCharDevice
	case unix.S_IFBLK:
		mode |= fs.ModeDevice
	}
	if stat.Mode&unix.S_ISUID != 0 {
		mode |= fs.ModeSetuid
	}
	if stat.Mode&unix.S_ISGID != 0 {
		mode |= fs.ModeSetgid
	}
	if stat.Mode&unix.S_ISVTX != 0 {
		mode |= fs.ModeSticky
	}
	return unixFileInfo{name: name, size: stat.Size, mode: mode, modTime: statModTime(stat), stat: stat}
}

func (i unixFileInfo) Name() string       { return i.name }
func (i unixFileInfo) Size() int64        { return i.size }
func (i unixFileInfo) Mode() fs.FileMode  { return i.mode }
func (i unixFileInfo) ModTime() time.Time { return i.modTime }
func (i unixFileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i unixFileInfo) Sys() any           { stat := i.stat; return &stat }

// Stat_t uses Mtim on Linux and Mtimespec on Darwin. Reflection keeps this
// small metadata adapter portable across both supported Unix targets.
func statModTime(stat unix.Stat_t) time.Time {
	value := reflect.ValueOf(stat)
	for _, field := range []string{"Mtim", "Mtimespec"} {
		mtime := value.FieldByName(field)
		if !mtime.IsValid() {
			continue
		}
		sec := mtime.FieldByName("Sec")
		nsec := mtime.FieldByName("Nsec")
		if sec.IsValid() && nsec.IsValid() {
			return time.Unix(sec.Int(), nsec.Int())
		}
	}
	return time.Time{}
}
