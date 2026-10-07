//go:build darwin

package safefs

import "golang.org/x/sys/unix"

func renameAtNoReplace(oldDir int, oldName string, newDir int, newName string) error {
	return unix.RenameatxNp(oldDir, oldName, newDir, newName, unix.RENAME_EXCL)
}

func renameAtExchange(oldDir int, oldName string, newDir int, newName string) error {
	return unix.RenameatxNp(oldDir, oldName, newDir, newName, unix.RENAME_SWAP)
}
