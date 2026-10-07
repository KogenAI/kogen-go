//go:build darwin || linux

package safefs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishPrivateUsesOwnerOnlyModeAndReplacesSymlinkLeaf(t *testing.T) {
	rootPath := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "target")
	if err := os.WriteFile(target, []byte("leave me"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"state", "cache", "staging"} {
		if err := os.Mkdir(filepath.Join(rootPath, dir), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(rootPath, dir, "record")); err != nil {
			t.Fatal(err)
		}
	}
	root := mustRoot(t, rootPath)

	for _, dir := range []string{"state", "cache", "staging"} {
		t.Run(dir, func(t *testing.T) {
			if err := root.PublishPrivate(dir+"/record", []byte("private"), PublicationReplace); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(filepath.Join(rootPath, dir, "record"))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("published mode = %04o, want 0600", info.Mode().Perm())
			}
			got, err := os.ReadFile(filepath.Join(rootPath, dir, "record"))
			if err != nil || string(got) != "private" {
				t.Fatalf("published contents = %q, %v", got, err)
			}
		})
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "leave me" {
		t.Fatalf("symlink target changed: %q, %v", got, err)
	}
}

func TestPublishPrivateRefusesSymlinkedStateCacheAndStagingParents(t *testing.T) {
	rootPath := t.TempDir()
	outside := t.TempDir()
	for _, dir := range []string{"state", "cache", "staging"} {
		if err := os.Symlink(outside, filepath.Join(rootPath, dir)); err != nil {
			t.Fatal(err)
		}
	}
	root := mustRoot(t, rootPath)
	for _, dir := range []string{"state", "cache", "staging"} {
		t.Run(dir, func(t *testing.T) {
			if err := root.PublishPrivate(dir+"/record", []byte("private"), PublicationReplace); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("publish through symlink parent error = %v, want ErrUnsafePath", err)
			}
			if _, err := os.Lstat(filepath.Join(outside, "record")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("symlink target received a publication: %v", err)
			}
		})
	}
}

func TestPublishPrivateCreateOnlyAndHardlinkRefusal(t *testing.T) {
	rootPath := t.TempDir()
	first := filepath.Join(rootPath, "first")
	second := filepath.Join(rootPath, "second")
	if err := os.WriteFile(first, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := mustRoot(t, rootPath)
	if err := root.PublishPrivate("created", []byte("one"), PublicationCreateOnly); err != nil {
		t.Fatal(err)
	}
	if err := root.PublishPrivate("created", []byte("two"), PublicationCreateOnly); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second create-only publish error = %v, want fs.ErrExist", err)
	}
	if err := os.Link(first, second); err != nil {
		t.Fatal(err)
	}
	if err := root.PublishPrivate("first", []byte("replacement"), PublicationReplace); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("hardlinked replacement error = %v, want ErrUnsafeFile", err)
	}
	for _, name := range []string{"first", "second"} {
		got, err := os.ReadFile(filepath.Join(rootPath, name))
		if err != nil || string(got) != "original" {
			t.Fatalf("hardlink %s changed: %q, %v", name, got, err)
		}
	}
}
