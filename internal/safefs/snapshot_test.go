//go:build darwin || linux

package safefs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotIsPrivateCreateOnlyAndRestoreIsExactCopy(t *testing.T) {
	workspacePath := t.TempDir()
	statePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspacePath, "tree", "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspacePath, "tree", "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspacePath, "tree", "bin", "run"), []byte("original"), 0o751); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspacePath, "tree", "config", "settings"), []byte("before"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("settings", filepath.Join(workspacePath, "tree", "config", "current")); err != nil {
		t.Fatal(err)
	}
	workspace := mustRoot(t, workspacePath)
	state := mustRoot(t, statePath)

	if err := Snapshot(workspace, "tree", state, "snapshots/run"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(statePath, "snapshots/run"))
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("snapshot root info = %v, %v; want private 0700 directory", info, err)
	}
	if err := Snapshot(workspace, "tree", state, "snapshots/run"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("snapshot overwrite error = %v, want already exists", err)
	}
	if err := os.WriteFile(filepath.Join(workspacePath, "tree/config/settings"), []byte("after"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(statePath, "workspace"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(statePath, "workspace/stale"), []byte("remove me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Restore(state, "snapshots/run", state, "workspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(statePath, "workspace/stale")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore retained stale entry: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(statePath, "workspace/config/settings")); err != nil || string(got) != "before" {
		t.Fatalf("restored bytes = %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(statePath, "workspace/bin/run")); err != nil || string(got) != "original" {
		t.Fatalf("restored executable bytes = %q, %v", got, err)
	}
	modeInfo, err := os.Stat(filepath.Join(statePath, "workspace/bin/run"))
	if err != nil || modeInfo.Mode().Perm() != 0o751 {
		t.Fatalf("restored executable mode = %v, %v", modeInfo, err)
	}
	linkInfo, err := os.Lstat(filepath.Join(statePath, "workspace/config/current"))
	if err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("restored link info = %v, %v", linkInfo, err)
	}
	snapshotFile, err := os.Stat(filepath.Join(statePath, "snapshots/run/bin/run"))
	if err != nil {
		t.Fatal(err)
	}
	restoredFile, err := os.Stat(filepath.Join(statePath, "workspace/bin/run"))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(snapshotFile, restoredFile) {
		t.Fatal("restore hardlinked a snapshot file")
	}
	if got, err := os.ReadFile(filepath.Join(statePath, "snapshots/run/config/settings")); err != nil || string(got) != "before" {
		t.Fatalf("snapshot changed after source edit/restore: %q, %v", got, err)
	}
}

func TestRestoreReplacesSymlinkLeafWithoutFollowingIt(t *testing.T) {
	snapshotPath := t.TempDir()
	destinationPath := t.TempDir()
	outsidePath := t.TempDir()
	if err := os.Mkdir(filepath.Join(snapshotPath, "snapshot"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshotPath, "snapshot", "new"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsidePath, "sentinel"), []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, filepath.Join(destinationPath, "restore")); err != nil {
		t.Fatal(err)
	}
	snapshot := mustRoot(t, snapshotPath)
	destination := mustRoot(t, destinationPath)
	if err := Restore(snapshot, "snapshot", destination, "restore"); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(outsidePath, "sentinel")); err != nil || string(got) != "untouched" {
		t.Fatalf("symlink target changed: %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(destinationPath, "restore/new")); err != nil || string(got) != "safe" {
		t.Fatalf("restored file = %q, %v", got, err)
	}
}
