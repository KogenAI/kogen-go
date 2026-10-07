package gate

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"reflect"
	"sort"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/safefs"
)

type workspaceEntry struct {
	kind    byte
	mode    fs.FileMode
	content []byte
}

// workspaceSnapshot is an in-memory rollback image for the user-visible
// workspace. Git's private metadata is deliberately outside this tree image;
// the authoritative candidate tree comparison is performed by TreeSnapshotter.
type workspaceSnapshot struct {
	entries map[string]workspaceEntry
}

// captureWorkspaceAt reopens the same directory to obtain a fresh descriptor
// for root enumeration, then verifies that it is the already-held workspace
// inode. This avoids reusing a directory stream offset while preserving the
// caller's rooted identity check.
func captureWorkspaceAt(opener contract.RootOpener, path string, expected contract.RootedFS) (*workspaceSnapshot, error) {
	if opener == nil {
		opener = safefs.Opener{}
	}
	if expected == nil {
		return nil, errors.New("gate: expected rooted workspace is required")
	}
	root, err := opener.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	defer closeRoot(root)
	left, err := expected.Lstat(".")
	if err != nil {
		return nil, err
	}
	right, err := root.Lstat(".")
	if err != nil {
		return nil, err
	}
	if !sameRootFile(left, right) {
		return nil, errors.New("gate: workspace path changed while capturing rollback state")
	}
	return captureWorkspace(root)
}

func sameRootFile(left, right fs.FileInfo) bool {
	if os.SameFile(left, right) {
		return true
	}
	deviceLeft, inodeLeft, okLeft := fileIdentity(left)
	deviceRight, inodeRight, okRight := fileIdentity(right)
	return okLeft && okRight && deviceLeft == deviceRight && inodeLeft == inodeRight
}

func fileIdentity(info fs.FileInfo) (uint64, uint64, bool) {
	if info == nil || info.Sys() == nil {
		return 0, 0, false
	}
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return 0, 0, false
	}
	device := value.FieldByName("Dev")
	inode := value.FieldByName("Ino")
	if !device.IsValid() || !inode.IsValid() {
		return 0, 0, false
	}
	return reflectUint(device), reflectUint(inode), true
}

func reflectUint(value reflect.Value) uint64 {
	switch value.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return value.Uint()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return uint64(value.Int())
	default:
		return 0
	}
}

func captureWorkspace(root contract.RootedFS) (*workspaceSnapshot, error) {
	if root == nil {
		return nil, errors.New("gate: workspace root is required")
	}
	snapshot := &workspaceSnapshot{entries: make(map[string]workspaceEntry)}
	var walk func(string) error
	walk = func(directory string) error {
		entries, err := root.ReadDir(directory)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			if directory == "." && entry.Name() == ".git" {
				continue
			}
			name := entry.Name()
			if directory != "." {
				name = directory + "/" + name
			}
			if !fs.ValidPath(name) || strings.Contains(name, "\\") {
				return fmt.Errorf("gate: unsafe workspace entry %q", name)
			}
			info, err := root.Lstat(name)
			if err != nil {
				return fmt.Errorf("gate: inspect workspace entry %q: %w", name, err)
			}
			switch {
			case info.Mode()&fs.ModeSymlink != 0:
				target, err := root.Readlink(name)
				if err != nil {
					return fmt.Errorf("gate: read workspace symlink %q: %w", name, err)
				}
				snapshot.entries[name] = workspaceEntry{kind: 'l', content: []byte(target)}
			case info.IsDir():
				snapshot.entries[name] = workspaceEntry{kind: 'd'}
				if err := walk(name); err != nil {
					return err
				}
			case info.Mode().IsRegular():
				contents, err := root.ReadFile(name)
				if err != nil {
					return fmt.Errorf("gate: read workspace file %q: %w", name, err)
				}
				snapshot.entries[name] = workspaceEntry{kind: 'f', mode: info.Mode().Perm(), content: contents}
			default:
				return fmt.Errorf("gate: unsupported workspace entry %q", name)
			}
		}
		return nil
	}
	if err := walk("."); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (s *workspaceSnapshot) ChangedPaths(other *workspaceSnapshot) []string {
	if s == nil || other == nil {
		return nil
	}
	paths := make(map[string]struct{}, len(s.entries)+len(other.entries))
	for name := range s.entries {
		paths[name] = struct{}{}
	}
	for name := range other.entries {
		paths[name] = struct{}{}
	}
	changed := make([]string, 0)
	for name := range paths {
		left, leftOK := s.entries[name]
		right, rightOK := other.entries[name]
		if leftOK != rightOK || left.kind != right.kind || left.mode != right.mode || !bytes.Equal(left.content, right.content) {
			changed = append(changed, name)
		}
	}
	sort.Strings(changed)
	return changed
}

// Restore replaces every user-visible path beneath the root from the captured
// bytes. Each file publication is rooted and atomic; the Git metadata entry is
// never traversed or rewritten.
func (s *workspaceSnapshot) Restore(root contract.RootedFS, current *workspaceSnapshot) error {
	if s == nil || root == nil || current == nil {
		return errors.New("gate: rollback snapshot, current snapshot, and rooted workspace are required")
	}
	rootNames := make(map[string]struct{})
	for name := range current.entries {
		first, _, _ := strings.Cut(name, "/")
		if first != ".git" {
			rootNames[first] = struct{}{}
		}
	}
	for name := range s.entries {
		first, _, _ := strings.Cut(name, "/")
		if first != ".git" {
			rootNames[first] = struct{}{}
		}
	}
	removeNames := make([]string, 0, len(rootNames))
	for name := range rootNames {
		removeNames = append(removeNames, name)
	}
	sort.Strings(removeNames)
	for _, name := range removeNames {
		if err := removeWorkspaceEntry(root, name); err != nil {
			return fmt.Errorf("gate: remove changed workspace path %q: %w", name, err)
		}
	}

	names := make([]string, 0, len(s.entries))
	for name := range s.entries {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		depthI, depthJ := strings.Count(names[i], "/"), strings.Count(names[j], "/")
		if depthI != depthJ {
			return depthI < depthJ
		}
		return names[i] < names[j]
	})
	for _, name := range names {
		entry := s.entries[name]
		switch entry.kind {
		case 'd':
			if err := root.MkdirAll(name, 0o755); err != nil {
				return fmt.Errorf("gate: restore workspace directory %q: %w", name, err)
			}
		case 'f':
			if err := root.MkdirAll(path.Dir(name), 0o755); err != nil {
				return fmt.Errorf("gate: restore parent for %q: %w", name, err)
			}
			if err := root.Publish(name, entry.content, entry.mode, contract.PublicationReplace); err != nil {
				return fmt.Errorf("gate: restore workspace file %q: %w", name, err)
			}
		case 'l':
			if err := root.MkdirAll(path.Dir(name), 0o755); err != nil {
				return fmt.Errorf("gate: restore symlink parent for %q: %w", name, err)
			}
			if err := root.Symlink(string(entry.content), name); err != nil {
				return fmt.Errorf("gate: restore workspace symlink %q: %w", name, err)
			}
		default:
			return fmt.Errorf("gate: invalid rollback entry %q", name)
		}
	}
	if err := root.SyncDir("."); err != nil {
		return fmt.Errorf("gate: sync workspace after rollback: %w", err)
	}
	return nil
}

func removeWorkspaceEntry(root contract.RootedFS, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() && info.Mode()&fs.ModeSymlink == 0 {
		children, err := root.ReadDir(name)
		if err != nil {
			return err
		}
		for _, child := range children {
			if err := removeWorkspaceEntry(root, name+"/"+child.Name()); err != nil {
				return err
			}
		}
	}
	return root.Remove(name)
}
