package safefs

import (
	"errors"
	"io"
	"io/fs"
	"path"
)

// CopyTree copies one rooted file, symlink, or directory tree into another
// rooted location. Regular files are copied through Publish, so they never
// share an inode with the source. Directories merge into an existing directory;
// entries absent from the source are left untouched. Regular-file permission
// bits and newly created directory permission bits are retained. Symlinks are recreated only when their raw
// relative target stays within the copied subtree. Special files are refused.
// CopyTree may leave already copied entries in place when it returns an error;
// use Snapshot or Restore when the destination must be all-or-nothing.
func CopyTree(source *Root, sourceName string, destination *Root, destinationName string) error {
	if source == nil || destination == nil {
		return ErrUnsafePath
	}
	if _, err := validateName(sourceName, true); err != nil {
		return err
	}
	if _, err := validateName(destinationName, true); err != nil {
		return err
	}
	if sourceName == destinationName {
		if same, err := rootsAreSame(source, destination); err != nil {
			return err
		} else if same {
			return nil
		}
	}
	if err := rejectOverlappingTrees(source, sourceName, destination, destinationName); err != nil {
		return err
	}
	return copyEntry(source, sourceName, destination, destinationName, nil)
}

func copyEntry(source *Root, sourceName string, destination *Root, destinationName string, relative []string) error {
	info, err := source.Lstat(sourceName)
	if err != nil {
		return err
	}
	switch {
	case info.IsDir():
		if destinationName != "." {
			if existing, err := destination.Lstat(destinationName); err == nil {
				if !existing.IsDir() {
					if err := removeTree(destination, destinationName); err != nil {
						return err
					}
				}
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if err := destination.MkdirAll(destinationName, info.Mode().Perm()); err != nil {
				return err
			}
		}
		entries, err := readDirectoryNoFollow(source, sourceName)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			childSource := joinRootName(sourceName, entry.Name())
			childDestination := joinRootName(destinationName, entry.Name())
			childRelative := append(append([]string(nil), relative...), entry.Name())
			if err := copyEntry(source, childSource, destination, childDestination, childRelative); err != nil {
				return err
			}
		}
		return destination.SyncDir(destinationName)

	case info.Mode().IsRegular():
		if destinationName == "." {
			return ErrUnsafePath
		}
		if existing, err := destination.Lstat(destinationName); err == nil && existing.IsDir() {
			if err := removeTree(destination, destinationName); err != nil {
				return err
			}
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		contents, err := readRegularNoFollow(source, sourceName)
		if err != nil {
			return err
		}
		return destination.Publish(destinationName, contents, info.Mode().Perm(), PublicationReplace)

	case info.Mode()&fs.ModeSymlink != 0:
		if destinationName == "." {
			return ErrUnsafePath
		}
		target, err := source.Readlink(sourceName)
		if err != nil {
			return err
		}
		parent := relative
		if len(parent) > 0 {
			parent = parent[:len(parent)-1]
		}
		if _, err := validateSymlinkTarget(parent, target); err != nil {
			return err
		}
		if _, err := destination.Lstat(destinationName); err == nil {
			if err := removeTree(destination, destinationName); err != nil {
				return err
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return destination.Symlink(target, destinationName)

	default:
		return ErrUnsafeFile
	}
}

func readRegularNoFollow(root *Root, name string) ([]byte, error) {
	var file io.ReadCloser
	err := root.withOps(func(ops platformRoot) error {
		reader, ok := ops.(interface {
			openRegularNoFollow(string) (io.ReadCloser, error)
		})
		if !ok {
			return ErrUnsafeFile
		}
		var err error
		file, err = reader.openRegularNoFollow(name)
		return err
	})
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

func readDirectoryNoFollow(root *Root, name string) ([]fs.DirEntry, error) {
	var entries []fs.DirEntry
	err := root.withOps(func(ops platformRoot) error {
		reader, ok := ops.(interface {
			readDirectoryNoFollow(string) ([]fs.DirEntry, error)
		})
		if !ok {
			return ErrUnsafePath
		}
		var err error
		entries, err = reader.readDirectoryNoFollow(name)
		return err
	})
	return entries, err
}

func joinRootName(parent, child string) string {
	if parent == "." {
		return child
	}
	return parent + "/" + child
}

func removeTree(root *Root, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		entries, err := readDirectoryNoFollow(root, name)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := removeTree(root, joinRootName(name, entry.Name())); err != nil {
				return err
			}
		}
	}
	return root.Remove(name)
}

func rejectOverlappingTrees(source *Root, sourceName string, destination *Root, destinationName string) error {
	same, err := rootsAreSame(source, destination)
	if err != nil || !same {
		return err
	}
	sourceName = path.Clean(sourceName)
	destinationName = path.Clean(destinationName)
	if sourceName == destinationName {
		return nil
	}
	if isRootAncestor(sourceName, destinationName) || isRootAncestor(destinationName, sourceName) {
		return ErrUnsafePath
	}
	return nil
}

func isRootAncestor(parent, child string) bool {
	if parent == "." {
		return child != "."
	}
	return len(child) > len(parent) && child[:len(parent)] == parent && child[len(parent)] == '/'
}
