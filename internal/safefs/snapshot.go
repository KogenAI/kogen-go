package safefs

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

var errDurableDirectoryOpsUnavailable = errors.New("safefs: exclusive durable directory operations unavailable")

type durableDirectoryOps interface {
	createPrivateSibling(parent, prefix string) (string, error)
	renameNoReplace(oldName, newName string) error
	renameExchange(oldName, newName string) (bool, error)
	identity() (rootIdentity, error)
}

type rootIdentity struct {
	device uint64
	inode  uint64
}

// Snapshot durably publishes a complete copy of a rooted directory at
// snapshotName. The snapshot path must not already exist. It is copied into an
// exclusive 0700 sibling directory, every copied file/directory is synced, and
// the completed directory is atomically renamed into place without replacing
// a concurrently created leaf. Callers must stop writers when they require a
// point-in-time snapshot; this method does not freeze the source tree.
func Snapshot(source *Root, sourceName string, destination *Root, snapshotName string) (result error) {
	if source == nil || destination == nil {
		return ErrUnsafePath
	}
	if _, err := validateName(sourceName, true); err != nil {
		return err
	}
	if _, err := validateName(snapshotName, false); err != nil {
		return err
	}
	if err := rejectOverlappingTrees(source, sourceName, destination, snapshotName); err != nil {
		return err
	}
	info, err := source.Lstat(sourceName)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return ErrUnsafeFile
	}
	parent, _, err := parentAndLeaf(snapshotName)
	if err != nil {
		return err
	}
	if err := destination.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	stage, err := createPrivateSibling(destination, parent, ".kogen-snapshot-")
	if err != nil {
		return err
	}
	stageOwned := true
	defer func() {
		if stageOwned {
			if cleanupErr := removeTree(destination, stage); cleanupErr != nil {
				result = errors.Join(result, fmt.Errorf("remove incomplete snapshot %q: %w", stage, cleanupErr))
			}
		}
	}()
	if err := CopyTree(source, sourceName, destination, stage); err != nil {
		return err
	}
	if err := destination.SyncDir(stage); err != nil {
		return err
	}
	if err := renameNoReplace(destination, stage, snapshotName); err != nil {
		return err
	}
	stageOwned = false
	return nil
}

// Restore replaces destinationName with a complete materialized copy of a
// rooted snapshot directory. The new tree is fully copied and synced before
// publication. When a destination exists, it is atomically exchanged with the
// completed tree; the prior entry remains under the private stage name until
// cleanup. Snapshot files are copied through Publish; no hardlinks or reflinks
// are created.
func Restore(snapshot *Root, snapshotName string, destination *Root, destinationName string) (result error) {
	if snapshot == nil || destination == nil {
		return ErrUnsafePath
	}
	if _, err := validateName(snapshotName, true); err != nil {
		return err
	}
	if _, err := validateName(destinationName, false); err != nil {
		return err
	}
	if err := rejectOverlappingTrees(snapshot, snapshotName, destination, destinationName); err != nil {
		return err
	}
	info, err := snapshot.Lstat(snapshotName)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return ErrUnsafeFile
	}
	parent, _, err := parentAndLeaf(destinationName)
	if err != nil {
		return err
	}
	if err := destination.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	stage, err := createPrivateSibling(destination, parent, ".kogen-restore-")
	if err != nil {
		return err
	}
	stageOwned := true
	defer func() {
		if stageOwned {
			if cleanupErr := removeTree(destination, stage); cleanupErr != nil {
				result = errors.Join(result, fmt.Errorf("remove incomplete restore %q: %w", stage, cleanupErr))
			}
		}
	}()
	if err := CopyTree(snapshot, snapshotName, destination, stage); err != nil {
		return err
	}
	if err := destination.SyncDir(stage); err != nil {
		return err
	}

	if _, err := destination.Lstat(destinationName); errors.Is(err, fs.ErrNotExist) {
		if err := renameNoReplace(destination, stage, destinationName); err != nil {
			return err
		}
		stageOwned = false
		return nil
	} else if err != nil {
		return err
	}

	// Exchange is atomic, so a crash cannot leave the destination missing. The
	// old tree occupies the private stage name until cleanup completes.
	exchanged, err := renameExchange(destination, stage, destinationName)
	if !exchanged && errors.Is(err, fs.ErrNotExist) {
		if err := renameNoReplace(destination, stage, destinationName); err != nil {
			return err
		}
		stageOwned = false
		return nil
	}
	if !exchanged {
		return err
	}
	stageOwned = false
	if err != nil {
		return fmt.Errorf("restored destination but directory sync failed; prior tree retained at %q: %w", stage, err)
	}
	if err := removeTree(destination, stage); err != nil {
		return fmt.Errorf("restored destination but could not remove prior tree %q: %w", stage, err)
	}
	return nil
}

func parentAndLeaf(name string) (string, string, error) {
	parts, err := validateName(name, false)
	if err != nil {
		return "", "", err
	}
	leaf := parts[len(parts)-1]
	if len(parts) == 1 {
		return ".", leaf, nil
	}
	return strings.Join(parts[:len(parts)-1], "/"), leaf, nil
}

func createPrivateSibling(root *Root, parent, prefix string) (string, error) {
	var name string
	err := root.withOps(func(ops platformRoot) error {
		durable, ok := ops.(durableDirectoryOps)
		if !ok {
			return errDurableDirectoryOpsUnavailable
		}
		var err error
		name, err = durable.createPrivateSibling(parent, prefix)
		return err
	})
	return name, err
}

func renameNoReplace(root *Root, oldName, newName string) error {
	return root.withOps(func(ops platformRoot) error {
		durable, ok := ops.(durableDirectoryOps)
		if !ok {
			return errDurableDirectoryOpsUnavailable
		}
		return durable.renameNoReplace(oldName, newName)
	})
}

func rootsAreSame(first, second *Root) (bool, error) {
	if first == second {
		return true, nil
	}
	firstIdentity, firstKnown, err := identityFor(first)
	if err != nil || !firstKnown {
		return false, err
	}
	secondIdentity, secondKnown, err := identityFor(second)
	if err != nil || !secondKnown {
		return false, err
	}
	return firstIdentity == secondIdentity, nil
}

func identityFor(root *Root) (rootIdentity, bool, error) {
	var identity rootIdentity
	var known bool
	err := root.withOps(func(ops platformRoot) error {
		durable, ok := ops.(durableDirectoryOps)
		if !ok {
			return nil
		}
		var err error
		identity, err = durable.identity()
		known = err == nil
		return err
	})
	return identity, known, err
}

func renameExchange(root *Root, oldName, newName string) (bool, error) {
	var exchanged bool
	err := root.withOps(func(ops platformRoot) error {
		durable, ok := ops.(durableDirectoryOps)
		if !ok {
			return errDurableDirectoryOpsUnavailable
		}
		var err error
		exchanged, err = durable.renameExchange(oldName, newName)
		return err
	})
	return exchanged, err
}
