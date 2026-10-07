package protection

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/safefs"
)

const nonFileSHA256 = "5085ae2e1b2b6767ba0b4feda80c08b9bc3f27defaad1a06ec5bcf05ff91ac98"

// Finding reports an observed mismatch without changing the manifest or
// workspace. The gate maps these to protected/<path> red findings.
type Finding struct {
	Path           string
	ExpectedSHA256 string
	ActualSHA256   string
}

// Protector applies a validated immutable manifest and the source-copy
// deletion list to one mutable Build workspace.
type Protector struct {
	manifest      Manifest
	removedSource []string
}

// NewProtector validates and copies manifest material so later caller changes
// cannot change what is guarded or restored.
func NewProtector(manifest Manifest, removedSources []string) (*Protector, error) {
	copyOfManifest := make(Manifest, len(manifest))
	for name, entry := range manifest {
		if err := validateProtectedPath(name); err != nil {
			return nil, err
		}
		if entry.Present {
			if entry.SHA256 != digest(entry.Bytes) {
				return nil, fmt.Errorf("protection: invalid manifest digest for %q", name)
			}
			if entry.Mode != modeRegular && entry.Mode != modeExec && entry.Mode != modeSymlink {
				return nil, fmt.Errorf("protection: unsupported manifest mode %o for %q", entry.Mode, name)
			}
			entry.Bytes = append([]byte(nil), entry.Bytes...)
		} else if entry.SHA256 != AbsentSHA256 || len(entry.Bytes) != 0 {
			return nil, fmt.Errorf("protection: invalid absent manifest entry for %q", name)
		}
		copyOfManifest[name] = entry
	}
	removed := make([]string, 0, len(removedSources))
	seen := make(map[string]struct{}, len(removedSources))
	for _, name := range removedSources {
		if err := validateProtectedPath(name); err != nil {
			return nil, err
		}
		if _, collision := copyOfManifest[name]; collision {
			return nil, fmt.Errorf("protection: removed source %q also appears in the manifest", name)
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		removed = append(removed, name)
	}
	sort.Strings(removed)
	return &Protector{manifest: copyOfManifest, removedSource: removed}, nil
}

// Manifest returns a defensive copy suitable for hashing or serializing.
func (p *Protector) Manifest() Manifest {
	if p == nil {
		return nil
	}
	result := make(Manifest, len(p.manifest))
	for name, entry := range p.manifest {
		entry.Bytes = append([]byte(nil), entry.Bytes...)
		result[name] = entry
	}
	return result
}

// Guard checks protected bytes and confirms source acceptance copies are
// absent. It only reads through the rooted filesystem capability.
func (p *Protector) Guard(root contract.RootedFS) ([]Finding, error) {
	if p == nil || root == nil {
		return nil, errors.New("protection: protector and rooted filesystem are required")
	}
	findings := make([]Finding, 0)
	for _, name := range sortedManifestPaths(p.manifest) {
		actual, _, err := workspaceHash(root, name)
		if err != nil {
			return nil, fmt.Errorf("guard protected path %q: %w", name, err)
		}
		entry := p.manifest[name]
		if actual != entry.SHA256 {
			findings = append(findings, Finding{Path: name, ExpectedSHA256: entry.SHA256, ActualSHA256: actual})
		}
	}
	for _, name := range p.removedSource {
		actual, _, err := workspaceHash(root, name)
		if err != nil {
			return nil, fmt.Errorf("guard removed acceptance source %q: %w", name, err)
		}
		if actual != AbsentSHA256 {
			findings = append(findings, Finding{Path: name, ExpectedSHA256: AbsentSHA256, ActualSHA256: actual})
		}
	}
	return findings, nil
}

// RestoreAfterBatch restores changed manifest entries and deletes source
// acceptance copies. Call after each completed tool batch; Guard belongs before
// each verification and before the landing commit.
func (p *Protector) RestoreAfterBatch(root contract.RootedFS) ([]string, error) {
	if p == nil || root == nil {
		return nil, errors.New("protection: protector and rooted filesystem are required")
	}
	var restored []string
	for _, name := range sortedManifestPaths(p.manifest) {
		entry := p.manifest[name]
		actual, _, err := workspaceHash(root, name)
		if err != nil {
			return restored, fmt.Errorf("inspect protected path %q before restore: %w", name, err)
		}
		if actual == entry.SHA256 {
			continue
		}
		if !entry.Present {
			if err := removeTree(root, name); err != nil {
				return restored, fmt.Errorf("remove protected path %q: %w", name, err)
			}
		} else if err := restoreEntry(root, name, entry); err != nil {
			return restored, fmt.Errorf("restore protected path %q: %w", name, err)
		}
		restored = append(restored, name)
	}
	for _, name := range p.removedSource {
		actual, _, err := workspaceHash(root, name)
		if err != nil {
			return restored, fmt.Errorf("inspect acceptance source %q before removal: %w", name, err)
		}
		if actual == AbsentSHA256 {
			continue
		}
		if err := removeTree(root, name); err != nil {
			return restored, fmt.Errorf("remove acceptance source %q: %w", name, err)
		}
		restored = append(restored, name)
	}
	return restored, nil
}

func workspaceHash(root contract.RootedFS, name string) (string, []byte, error) {
	if err := validateProtectedPath(name); err != nil {
		return "", nil, err
	}
	parts := strings.Split(name, "/")
	parent := ""
	for _, component := range parts[:len(parts)-1] {
		if parent == "" {
			parent = component
		} else {
			parent += "/" + component
		}
		info, err := root.Lstat(parent)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrInvalid) {
			return AbsentSHA256, nil, nil
		}
		if err != nil {
			return "", nil, err
		}
		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return AbsentSHA256, nil, nil
		}
	}
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrInvalid) {
		return AbsentSHA256, nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		target, err := root.Readlink(name)
		if err != nil {
			return "", nil, err
		}
		bytes := []byte(target)
		return digest(bytes), bytes, nil
	}
	if !info.Mode().IsRegular() {
		return nonFileSHA256, nil, nil
	}
	content, err := root.ReadFile(name)
	if err != nil {
		return "", nil, err
	}
	return digest(content), content, nil
}

func restoreEntry(root contract.RootedFS, name string, entry Entry) error {
	if err := ensureParents(root, name); err != nil {
		return err
	}
	if entry.Mode == modeSymlink {
		if err := removeNonDirectoryLeaf(root, name); err != nil {
			return err
		}
		return publishSymlink(root, name, string(entry.Bytes))
	}
	if err := removeDirectoryLeaf(root, name); err != nil {
		return err
	}
	permission := fs.FileMode(0o644)
	if entry.Mode == modeExec {
		permission = 0o755
	}
	return root.Publish(name, entry.Bytes, permission, safefs.PublicationReplace)
}

func ensureParents(root contract.RootedFS, name string) error {
	parts := strings.Split(name, "/")
	parent := ""
	for _, component := range parts[:len(parts)-1] {
		if parent == "" {
			parent = component
		} else {
			parent += "/" + component
		}
		info, err := root.Lstat(parent)
		if errors.Is(err, fs.ErrNotExist) {
			if err := root.MkdirAll(parent, 0o700); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.IsDir() && info.Mode()&fs.ModeSymlink == 0 {
			continue
		}
		if err := removeTree(root, parent); err != nil {
			return err
		}
		if err := root.MkdirAll(parent, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func removeDirectoryLeaf(root contract.RootedFS, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() && info.Mode()&fs.ModeSymlink == 0 {
		return removeTree(root, name)
	}
	return nil
}

func removeNonDirectoryLeaf(root contract.RootedFS, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() && info.Mode()&fs.ModeSymlink == 0 {
		return removeTree(root, name)
	}
	return root.Remove(name)
}

func removeTree(root contract.RootedFS, name string) error {
	parts := strings.Split(name, "/")
	parent := ""
	for _, component := range parts[:len(parts)-1] {
		if parent == "" {
			parent = component
		} else {
			parent += "/" + component
		}
		parentInfo, parentErr := root.Lstat(parent)
		if errors.Is(parentErr, fs.ErrNotExist) {
			return nil
		}
		if parentErr != nil {
			return parentErr
		}
		if !parentInfo.IsDir() || parentInfo.Mode()&fs.ModeSymlink != 0 {
			return nil
		}
	}
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
			child := path.Join(name, entry.Name())
			if err := removeTree(root, child); err != nil {
				return err
			}
		}
	}
	return root.Remove(name)
}

func publishSymlink(root contract.RootedFS, name, target string) error {
	parent := path.Dir(name)
	if parent == "." {
		parent = ""
	}
	for attempt := 0; attempt < 8; attempt++ {
		var nonce [8]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return err
		}
		leaf := ".kogen-protection-" + hex.EncodeToString(nonce[:])
		temporary := leaf
		if parent != "" {
			temporary = path.Join(parent, leaf)
		}
		if err := root.Symlink(target, temporary); err != nil {
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return err
		}
		if err := root.Rename(temporary, name); err != nil {
			_ = root.Remove(temporary)
			return err
		}
		return nil
	}
	return errors.New("protection: could not allocate a temporary symlink leaf")
}

func sortedManifestPaths(manifest Manifest) []string {
	paths := make([]string, 0, len(manifest))
	for name := range manifest {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	return paths
}
