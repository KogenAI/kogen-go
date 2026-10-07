//go:build darwin || linux

package safefs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyTreeCopiesBytesModesAndContainedSymlinksWithoutLinks(t *testing.T) {
	sourcePath := t.TempDir()
	destinationPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourcePath, "tree", "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(sourcePath, "tree", "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, "tree", "bin", "run"), []byte("#!/bin/sh\n"), 0o751); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, "tree", "config", "settings.json"), []byte("{}\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("settings.json", filepath.Join(sourcePath, "tree", "config", "current")); err != nil {
		t.Fatal(err)
	}
	source := mustRoot(t, sourcePath)
	destination := mustRoot(t, destinationPath)

	if err := CopyTree(source, "tree", destination, "seed"); err != nil {
		t.Fatal(err)
	}
	for name, mode := range map[string]os.FileMode{
		"seed/bin/run":              0o751,
		"seed/config/settings.json": 0o640,
	} {
		info, err := os.Stat(filepath.Join(destinationPath, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Errorf("%s mode = %04o, want %04o", name, info.Mode().Perm(), mode)
		}
	}
	linkInfo, err := os.Lstat(filepath.Join(destinationPath, "seed/config/current"))
	if err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("copied link info = %v, %v", linkInfo, err)
	}
	if target, err := os.Readlink(filepath.Join(destinationPath, "seed/config/current")); err != nil || target != "settings.json" {
		t.Fatalf("copied link target = %q, %v", target, err)
	}
	if got, err := os.ReadFile(filepath.Join(destinationPath, "seed/config/current")); err != nil || string(got) != "{}\n" {
		t.Fatalf("copied link target contents = %q, %v", got, err)
	}
	sourceInfo, err := os.Stat(filepath.Join(sourcePath, "tree/bin/run"))
	if err != nil {
		t.Fatal(err)
	}
	destinationInfo, err := os.Stat(filepath.Join(destinationPath, "seed/bin/run"))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(sourceInfo, destinationInfo) {
		t.Fatal("copy shared a source inode")
	}
	if err := os.WriteFile(filepath.Join(destinationPath, "seed/bin/run"), []byte("changed"), 0o751); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(sourcePath, "tree/bin/run")); err != nil || string(got) != "#!/bin/sh\n" {
		t.Fatalf("source changed through copy: %q, %v", got, err)
	}
}

func TestCopyTreeRefusesEscapingSymlinkAndOverlappingTrees(t *testing.T) {
	sourcePath := t.TempDir()
	destinationPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(sourcePath, "tree"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../outside", filepath.Join(sourcePath, "tree", "escape")); err != nil {
		t.Fatal(err)
	}
	source := mustRoot(t, sourcePath)
	destination := mustRoot(t, destinationPath)
	if err := CopyTree(source, "tree", destination, "seed"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("escaping symlink copy error = %v, want ErrUnsafePath", err)
	}
	if _, err := os.Lstat(filepath.Join(destinationPath, "seed/escape")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed copy published escaping symlink: %v", err)
	}
	if err := source.MkdirAll("tree/child", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := CopyTree(source, ".", source, "tree/child/copy"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("overlapping copy error = %v, want ErrUnsafePath", err)
	}
}

func TestCopyReadersDoNotFollowFinalSymlinkLeaves(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootPath, "real"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "real", "file"), []byte("contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(rootPath, "directory-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real/file", filepath.Join(rootPath, "file-link")); err != nil {
		t.Fatal(err)
	}
	root := mustRoot(t, rootPath)
	if _, err := readDirectoryNoFollow(root, "directory-link"); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("directory link read error = %v, want ErrUnsafeFile", err)
	}
	if _, err := readRegularNoFollow(root, "file-link"); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("file link read error = %v, want ErrUnsafeFile", err)
	}
}
