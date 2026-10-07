//go:build !darwin && !linux

package recovery

import "errors"

func openWorkspaceCleaner(string) (workspaceCleaner, error) {
	return nil, errors.New("recovery: descriptor-rooted workspace cleanup is unavailable on this platform")
}

func removeWorkspaceTree(string, string) error {
	return errors.New("recovery: descriptor-rooted workspace cleanup is unavailable on this platform")
}
