//go:build darwin || linux

package workspace

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

var errCOWUnsupported = errors.New("workspace: copy-on-write cloning unsupported")

// SeedReport makes the actual copy strategy observable to its caller.
type SeedReport struct {
	Files       int
	COWCopies   int
	PlainCopies int
	Directories int
	Symlinks    int
}

// SeedDirectory merges one setup-output directory into a workspace directory.
// It uses APFS clones or Linux FICLONE where available and falls back to byte
// copies per file. It never creates hardlinks and never follows a source or
// destination symlink while traversing directories.
func SeedDirectory(sourceRoot, sourceDirectory, destinationRoot, destinationDirectory string) (SeedReport, error) {
	if err := validateWorkspacePath(sourceDirectory); err != nil {
		return SeedReport{}, fmt.Errorf("workspace: seed source: %w", err)
	}
	if err := validateWorkspacePath(destinationDirectory); err != nil {
		return SeedReport{}, fmt.Errorf("workspace: seed destination: %w", err)
	}
	sourceRoot, err := canonicalDirectory(sourceRoot)
	if err != nil {
		return SeedReport{}, fmt.Errorf("workspace: seed source root: %w", err)
	}
	destinationRoot, err = canonicalDirectory(destinationRoot)
	if err != nil {
		return SeedReport{}, fmt.Errorf("workspace: seed destination root: %w", err)
	}
	if pathsOverlap(sourceRoot, destinationRoot) {
		return SeedReport{}, errors.New("workspace: seed roots must not overlap")
	}
	srcRootFD, err := openDirectoryPath(sourceRoot)
	if err != nil {
		return SeedReport{}, fmt.Errorf("workspace: open seed source root: %w", err)
	}
	defer unix.Close(srcRootFD)
	dstRootFD, err := openDirectoryPath(destinationRoot)
	if err != nil {
		return SeedReport{}, fmt.Errorf("workspace: open seed destination root: %w", err)
	}
	defer unix.Close(dstRootFD)
	srcFD, err := openDirectoryAt(srcRootFD, sourceDirectory)
	if err != nil {
		return SeedReport{}, fmt.Errorf("workspace: open setup output %q: %w", sourceDirectory, err)
	}
	defer unix.Close(srcFD)
	dstFD, err := ensureDirectoryAt(dstRootFD, destinationDirectory, 0o700)
	if err != nil {
		return SeedReport{}, fmt.Errorf("workspace: prepare seed destination %q: %w", destinationDirectory, err)
	}
	defer unix.Close(dstFD)
	var report SeedReport
	if err := copyDirectory(srcFD, dstFD, ".", &report); err != nil {
		return report, fmt.Errorf("workspace: seed %q: %w", sourceDirectory, err)
	}
	var rootStat unix.Stat_t
	if err := unix.Fstat(srcFD, &rootStat); err != nil {
		return report, fmt.Errorf("workspace: inspect setup output: %w", err)
	}
	if err := unix.Fchmod(dstFD, uint32(rootStat.Mode&0o777)); err != nil {
		return report, fmt.Errorf("workspace: set seeded directory mode: %w", err)
	}
	if err := unix.Fsync(dstFD); err != nil {
		return report, fmt.Errorf("workspace: sync seeded directory: %w", err)
	}
	return report, nil
}

func copyDirectory(sourceFD, destinationFD int, relative string, report *SeedReport) error {
	entries, err := readDirectory(sourceFD)
	if err != nil {
		return err
	}
	for _, name := range entries {
		var sourceStat unix.Stat_t
		if err := unix.Fstatat(sourceFD, name, &sourceStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("inspect %q: %w", name, err)
		}
		childRelative := name
		if relative != "." {
			childRelative = relative + "/" + name
		}
		switch sourceStat.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			sourceChild, err := unix.Openat(sourceFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if err != nil {
				return fmt.Errorf("open source directory %q: %w", childRelative, err)
			}
			destinationChild, err := ensureDirectoryEntry(destinationFD, name, 0o700)
			if err != nil {
				_ = unix.Close(sourceChild)
				return fmt.Errorf("prepare directory %q: %w", childRelative, err)
			}
			if err := copyDirectory(sourceChild, destinationChild, childRelative, report); err != nil {
				_ = unix.Close(sourceChild)
				_ = unix.Close(destinationChild)
				return fmt.Errorf("copy directory %q: %w", childRelative, err)
			}
			_ = unix.Close(sourceChild)
			var childStat unix.Stat_t
			if err := unix.Fstatat(sourceFD, name, &childStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				_ = unix.Close(destinationChild)
				return err
			}
			if err := unix.Fchmod(destinationChild, uint32(childStat.Mode&0o777)); err != nil {
				_ = unix.Close(destinationChild)
				return fmt.Errorf("set directory mode %q: %w", childRelative, err)
			}
			if err := unix.Fsync(destinationChild); err != nil {
				_ = unix.Close(destinationChild)
				return fmt.Errorf("sync directory %q: %w", childRelative, err)
			}
			_ = unix.Close(destinationChild)
			report.Directories++
		case unix.S_IFREG:
			if err := copyRegularAt(sourceFD, destinationFD, name, uint32(sourceStat.Mode&0o777), report); err != nil {
				return fmt.Errorf("copy file %q: %w", childRelative, err)
			}
			report.Files++
		case unix.S_IFLNK:
			target, err := readlinkAt(sourceFD, name)
			if err != nil {
				return fmt.Errorf("read symlink %q: %w", childRelative, err)
			}
			if err := validateSeedSymlink(childRelative, target); err != nil {
				return err
			}
			if err := removeEntry(destinationFD, name); err != nil {
				return fmt.Errorf("replace symlink destination %q: %w", childRelative, err)
			}
			if err := unix.Symlinkat(target, destinationFD, name); err != nil {
				return fmt.Errorf("publish symlink %q: %w", childRelative, err)
			}
			if err := unix.Fsync(destinationFD); err != nil {
				return fmt.Errorf("sync symlink parent %q: %w", childRelative, err)
			}
			report.Symlinks++
		default:
			return fmt.Errorf("workspace: setup output contains unsupported file kind at %q", childRelative)
		}
	}
	return nil
}

func copyRegularAt(sourceParent, destinationParent int, name string, mode uint32, report *SeedReport) error {
	sourceFD, err := unix.Openat(sourceParent, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer unix.Close(sourceFD)
	var sourceStat unix.Stat_t
	if err := unix.Fstat(sourceFD, &sourceStat); err != nil {
		return err
	}
	if sourceStat.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("source changed from a regular file")
	}
	temporary, err := temporaryLeaf()
	if err != nil {
		return err
	}
	cloned, cloneErr := cloneFileAt(sourceFD, destinationParent, temporary, mode)
	if cloneErr != nil && !errors.Is(cloneErr, errCOWUnsupported) {
		return cloneErr
	}
	if !cloned {
		if err := copyPlainAt(sourceFD, destinationParent, temporary, mode); err != nil {
			return err
		}
		report.PlainCopies++
	} else {
		report.COWCopies++
	}
	if err := removeEntry(destinationParent, name); err != nil {
		_ = unix.Unlinkat(destinationParent, temporary, 0)
		return err
	}
	if err := unix.Renameat(destinationParent, temporary, destinationParent, name); err != nil {
		_ = unix.Unlinkat(destinationParent, temporary, 0)
		return err
	}
	return unix.Fsync(destinationParent)
}

func copyPlainAt(sourceFD, destinationParent int, name string, mode uint32) error {
	destinationFD, err := unix.Openat(destinationParent, name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	destination := os.NewFile(uintptr(destinationFD), name)
	sourceDup, err := unix.Dup(sourceFD)
	if err != nil {
		_ = destination.Close()
		_ = unix.Unlinkat(destinationParent, name, 0)
		return err
	}
	source := os.NewFile(uintptr(sourceDup), "seed-source")
	_, copyErr := io.Copy(destination, source)
	closeSourceErr := source.Close()
	modeErr := unix.Fchmod(destinationFD, mode)
	syncErr := destination.Sync()
	closeDestinationErr := destination.Close()
	if err := errors.Join(copyErr, closeSourceErr, modeErr, syncErr, closeDestinationErr); err != nil {
		_ = unix.Unlinkat(destinationParent, name, 0)
		return err
	}
	return nil
}
