//go:build darwin || linux

package safefs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRootRejectsEscapesAndReservedPaths(t *testing.T) {
	rootPath := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rootPath, "escape")); err != nil {
		t.Fatal(err)
	}
	root := mustRoot(t, rootPath)

	for _, name := range []string{"../secret", "escape/secret", ".git/config", "dir/.git/config", "nul\x00name", "NUL.txt"} {
		t.Run(name, func(t *testing.T) {
			if _, err := root.ReadFile(name); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("ReadFile(%q) error = %v, want ErrUnsafePath", name, err)
			}
		})
	}
	if got, err := os.ReadFile(filepath.Join(outside, "secret")); err != nil || string(got) != "secret" {
		t.Fatalf("outside file changed: %q, %v", got, err)
	}
}

func TestRootAllowsContainedRelativeSymlink(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootPath, "real"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "real", "file"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(rootPath, "link")); err != nil {
		t.Fatal(err)
	}
	root := mustRoot(t, rootPath)
	got, err := root.ReadFile("link/file")
	if err != nil || string(got) != "inside" {
		t.Fatalf("ReadFile through contained symlink = %q, %v", got, err)
	}
	if err := root.Publish("link/new", []byte("new"), 0o600, PublicationCreateOnly); err != nil {
		t.Fatalf("Publish through contained parent symlink: %v", err)
	}
	got, err = os.ReadFile(filepath.Join(rootPath, "real", "new"))
	if err != nil || string(got) != "new" {
		t.Fatalf("published file = %q, %v", got, err)
	}
}

func TestPublishReplacesSymlinkLeafWithoutTouchingTarget(t *testing.T) {
	rootPath := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "target")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(rootPath, "leaf")); err != nil {
		t.Fatal(err)
	}
	root := mustRoot(t, rootPath)
	if err := root.Publish("leaf", []byte("replacement"), 0o600, PublicationReplace); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "untouched" {
		t.Fatalf("symlink target changed: %q, %v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(rootPath, "leaf"))
	if err != nil || string(got) != "replacement" {
		t.Fatalf("replacement leaf = %q, %v", got, err)
	}
}

func TestRootRejectsFIFOAndDeviceFilesWithoutBlocking(t *testing.T) {
	rootPath := t.TempDir()
	if err := unix.Mkfifo(filepath.Join(rootPath, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := mustRoot(t, rootPath)

	start := time.Now()
	if _, err := root.OpenRead("pipe"); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("OpenRead(FIFO) error = %v, want ErrUnsafeFile", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("OpenRead(FIFO) blocked")
	}
	if err := root.Append("pipe", []byte("x"), 0o600); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("Append(FIFO) error = %v, want ErrUnsafeFile", err)
	}
	if err := root.Publish("pipe", []byte("x"), 0o600, PublicationReplace); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("Publish(FIFO) error = %v, want ErrUnsafeFile", err)
	}

	device := filepath.Join(rootPath, "device")
	if err := unix.Mknod(device, unix.S_IFCHR|0o600, int(unix.Mkdev(1, 3))); err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) || errors.Is(err, unix.ENOTSUP) {
			t.Logf("character-device fixture unavailable: %v", err)
			devFD, openErr := unix.Open("/dev", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
			if openErr != nil {
				t.Fatalf("open /dev for device-kind fixture: %v", openErr)
			}
			deviceFile, kindErr := openRegularAt(devFD, "null", unix.O_RDONLY|unix.O_NONBLOCK)
			if deviceFile != nil {
				_ = deviceFile.Close()
			}
			_ = unix.Close(devFD)
			if !errors.Is(kindErr, ErrUnsafeFile) {
				t.Fatalf("openRegularAt(/dev/null) error = %v, want ErrUnsafeFile", kindErr)
			}
			return
		}
		t.Fatal(err)
	}
	if _, err := root.OpenRead("device"); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("OpenRead(device) error = %v, want ErrUnsafeFile", err)
	}
}

func TestAppendRefusesHardlinkAliases(t *testing.T) {
	rootPath := t.TempDir()
	first := filepath.Join(rootPath, "first")
	second := filepath.Join(rootPath, "second")
	if err := os.WriteFile(first, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(first, second); err != nil {
		t.Fatal(err)
	}
	root := mustRoot(t, rootPath)
	if err := root.Append("first", []byte(" changed"), 0o600); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("Append(hardlink) error = %v, want ErrUnsafeFile", err)
	}
	for _, path := range []string{first, second} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "original" {
			t.Fatalf("hardlink alias %s changed: %q, %v", path, got, err)
		}
	}
}

func TestParentSymlinkReplacementRaceCannotRedirectPublication(t *testing.T) {
	rootPath := t.TempDir()
	outside := t.TempDir()
	parent := filepath.Join(rootPath, "parent")
	parked := filepath.Join(rootPath, "parked")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "sentinel"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := mustRoot(t, rootPath)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var swapped atomic.Int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := os.Rename(parent, parked); err != nil {
				continue
			}
			if err := os.Symlink(outside, parent); err == nil {
				swapped.Add(1)
				_ = os.Remove(parent)
			}
			_ = os.Rename(parked, parent)
		}
	}()

	for i := 0; i < 250; i++ {
		_ = root.Publish("parent/target", []byte("inside"), 0o600, PublicationReplace)
	}
	close(stop)
	wg.Wait()
	if swapped.Load() == 0 {
		t.Fatal("parent replacement fixture did not install a symlink")
	}

	if _, err := os.Lstat(filepath.Join(outside, "target")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("publication escaped through swapped parent: Lstat error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(outside, "sentinel"))
	if err != nil || string(got) != "safe" {
		t.Fatalf("outside sentinel changed: %q, %v", got, err)
	}
}

func TestMkdirAllAndPublicationModes(t *testing.T) {
	rootPath := t.TempDir()
	root := mustRoot(t, rootPath)
	if err := root.MkdirAll("a/b", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := root.Publish("a/b/file", []byte("one"), 0o600, PublicationCreateOnly); err != nil {
		t.Fatal(err)
	}
	if err := root.Publish("a/b/file", []byte("two"), 0o600, PublicationCreateOnly); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second create-only publication error = %v, want fs.ErrExist", err)
	}
	if err := root.SyncDir("a/b"); err != nil {
		t.Fatal(err)
	}
	if err := root.Rename("a/b/file", "a/b/renamed"); err != nil {
		t.Fatal(err)
	}
	if got, err := root.ReadFile("a/b/renamed"); err != nil || string(got) != "one" {
		t.Fatalf("renamed contents = %q, %v", got, err)
	}
	if err := root.Remove("a/b/renamed"); err != nil {
		t.Fatal(err)
	}
}

func TestClosedRootRejectsOperations(t *testing.T) {
	root := mustRoot(t, t.TempDir())
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := root.ReadDir("."); !errors.Is(err, ErrClosed) {
		t.Fatalf("ReadDir after Close error = %v, want ErrClosed", err)
	}
}

func mustRoot(t *testing.T, path string) *Root {
	t.Helper()
	root, err := OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}
