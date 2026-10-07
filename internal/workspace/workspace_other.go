//go:build !darwin && !linux

package workspace

import (
	"os"
	"path/filepath"
)

func createWorkspaceDirectory(root, leaf string) error {
	return os.Mkdir(filepath.Join(root, leaf), 0o700)
}
