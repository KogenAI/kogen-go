//go:build linux

package safefs

import "golang.org/x/sys/unix"

func renameAtNoReplace(oldDir int, oldName string, newDir int, newName string) error {
	return unix.Renameat2(oldDir, oldName, newDir, newName, unix.RENAME_NOREPLACE)
}

func renameAtExchange(oldDir int, oldName string, newDir int, newName string) error {
	return unix.Renameat2(oldDir, oldName, newDir, newName, unix.RENAME_EXCHANGE)
}
