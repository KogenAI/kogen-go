package setupcache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"kogen-go/internal/approval/prepare"
	"kogen-go/internal/safefs"
)

const (
	setupVersion     = 2
	baselineVersion  = 3
	setupLRULimit    = 3
	setupIndexPath   = "setup/v2/lru.json"
	maxMetadataBytes = 16 << 20
	privateDirectory = fs.FileMode(0o700)
	privateCacheFile = fs.FileMode(0o600)
)

var (
	sha256Identity = regexp.MustCompile(`^[0-9a-f]{64}$`)
	volatileKey    = regexp.MustCompile(`^uncacheable-[0-9a-f]{32}$`)
)

// Cache is safe for concurrent use within one process. The storage directory
// must already exist, be private, and be dedicated to this Kogen installation.
// Its contents are addressed relative to a safefs root; only directory COW
// seeding uses the corresponding absolute path so the shared workspace copier
// can use APFS clones/FICLONE without creating hardlinks.
type Cache struct {
	mu      sync.Mutex
	root    *safefs.Root
	rootDir string
}

var (
	_ prepare.SetupCachePort = (*Cache)(nil)
	_ prepare.BaselineV3Port = (*Cache)(nil)
)

// Open opens an existing private cache directory and creates its versioned
// namespaces using rooted operations. Old namespaces are never consulted.
func Open(directory string) (*Cache, error) {
	if directory == "" || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || strings.ContainsRune(directory, '\x00') {
		return nil, errors.New("setupcache: cache directory must be a clean absolute path")
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, fmt.Errorf("setupcache: resolve cache directory: %w", err)
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return nil, fmt.Errorf("setupcache: inspect cache directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("setupcache: cache directory must be a private real directory (mode=%s)", info.Mode())
	}
	root, err := safefs.OpenRoot(resolved)
	if err != nil {
		return nil, fmt.Errorf("setupcache: open rooted cache directory: %w", err)
	}
	cache := &Cache{root: root, rootDir: resolved}
	for _, directory := range []string{"setup", "setup/v2", "setup/volatile", "baseline", "baseline/v3"} {
		if err := ensurePrivateDirectory(root, directory); err != nil {
			_ = root.Close()
			return nil, fmt.Errorf("setupcache: prepare namespace: %w", err)
		}
	}
	return cache, nil
}

// Close releases the rooted cache capability.
func (c *Cache) Close() error {
	if c == nil || c.root == nil {
		return nil
	}
	return c.root.Close()
}

func ensurePrivateDirectory(root *safefs.Root, name string) error {
	current := ""
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return safefs.ErrUnsafePath
		}
		if current == "" {
			current = part
		} else {
			current += "/" + part
		}
		info, err := root.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			if err := root.MkdirAll(current, privateDirectory); err != nil {
				return err
			}
			info, err = root.Lstat(current)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("%s must be a private real directory", current)
		}
	}
	return nil
}

func randomToken() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func writeJSON(root *safefs.Root, name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return root.Publish(name, data, privateCacheFile, safefs.PublicationReplace)
}

func readJSON(root *safefs.Root, name string, target any) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 || info.Size() < 0 || info.Size() > maxMetadataBytes {
		return errors.New("setupcache: invalid cache metadata file")
	}
	data, err := root.ReadFile(name)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("setupcache: trailing cache metadata")
		}
		return err
	}
	return nil
}

func validRelativePath(value string) bool {
	if value == "" || value == "." || strings.ContainsAny(value, "\\\x00\r\n") || !fs.ValidPath(value) || path.IsAbs(value) || len(value) > 1 && value[1] == ':' {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".git" || part == ".." || part == "." {
			return false
		}
	}
	return true
}

func isWithinPath(parent, child string) bool {
	return child == parent || strings.HasPrefix(child, strings.TrimSuffix(parent, "/")+"/")
}

func removeTree(root *safefs.Root, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() && info.Mode()&fs.ModeSymlink == 0 {
		entries, err := root.ReadDir(name)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := removeTree(root, name+"/"+entry.Name()); err != nil {
				return err
			}
		}
	}
	return root.Remove(name)
}

func cleanDirectoryPath(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsRune(value, '\x00')
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("setupcache: context is required")
	}
	return ctx.Err()
}
