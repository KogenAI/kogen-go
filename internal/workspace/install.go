package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"kogen-go/internal/safefs"
)

const (
	gitModeRegular = 0o100644
	gitModeExec    = 0o100755
)

// ApprovedFile is one exact controller-approved regular file. GitMode is
// 0100644 or 0100755; zero selects 0100644. ExpectedSHA256, when supplied,
// prevents a stale or mismatched byte buffer from being installed.
type ApprovedFile struct {
	Path           string
	Bytes          []byte
	GitMode        uint32
	ExpectedSHA256 string
}

// InstallPlan installs all approved files, then removes approved source files
// that must remain absent in the Build workspace. Removal paths are files, not
// recursive directories. Validation completes before the first write.
type InstallPlan struct {
	Files   []ApprovedFile
	Removes []string
}

// InstalledFile records the exact content identity written to a workspace.
type InstalledFile struct {
	Path    string
	SHA256  string
	GitMode uint32
}

// InstallApproved publishes the exact approved bytes with their approved mode
// through safefs. It replaces symlink leaves themselves, never follows them,
// and atomically publishes each regular file before removing staged sources.
func InstallApproved(workspacePath string, plan InstallPlan) ([]InstalledFile, error) {
	if workspacePath == "" || !filepath.IsAbs(workspacePath) || filepath.Clean(workspacePath) != workspacePath || strings.ContainsRune(workspacePath, '\x00') {
		return nil, errors.New("workspace: workspace path must be a clean absolute directory")
	}
	files := append([]ApprovedFile(nil), plan.Files...)
	for index := range files {
		if err := validateWorkspacePath(files[index].Path); err != nil {
			return nil, err
		}
		if files[index].GitMode == 0 {
			files[index].GitMode = gitModeRegular
		}
		if files[index].GitMode != gitModeRegular && files[index].GitMode != gitModeExec {
			return nil, fmt.Errorf("workspace: approved file %q has unsupported Git mode %06o", files[index].Path, files[index].GitMode)
		}
		digest := sha256.Sum256(files[index].Bytes)
		actual := hex.EncodeToString(digest[:])
		if files[index].ExpectedSHA256 != "" && files[index].ExpectedSHA256 != actual {
			return nil, fmt.Errorf("workspace: approved bytes do not match the expected digest for %q", files[index].Path)
		}
	}
	removes := append([]string(nil), plan.Removes...)
	for _, name := range removes {
		if err := validateWorkspacePath(name); err != nil {
			return nil, err
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	sort.Strings(removes)
	for i := 1; i < len(files); i++ {
		if pathsOverlapRelative(files[i-1].Path, files[i].Path) {
			return nil, fmt.Errorf("workspace: overlapping approved paths %q and %q", files[i-1].Path, files[i].Path)
		}
	}
	for i := 1; i < len(removes); i++ {
		if pathsOverlapRelative(removes[i-1], removes[i]) {
			return nil, fmt.Errorf("workspace: overlapping removal paths %q and %q", removes[i-1], removes[i])
		}
	}
	for _, name := range removes {
		for _, file := range files {
			if pathsOverlapRelative(name, file.Path) {
				return nil, fmt.Errorf("workspace: removal path %q overlaps approved file %q", name, file.Path)
			}
		}
	}

	root, err := safefs.OpenRoot(workspacePath)
	if err != nil {
		return nil, fmt.Errorf("workspace: open workspace for approved files: %w", err)
	}
	defer closeRoot(root)
	for _, name := range removes {
		info, err := root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("workspace: inspect staged source %q: %w", name, err)
		}
		if info.IsDir() && info.Mode()&fs.ModeSymlink == 0 {
			return nil, fmt.Errorf("workspace: staged source %q is a directory", name)
		}
	}
	installed := make([]InstalledFile, 0, len(files))
	for _, file := range files {
		mode := fs.FileMode(0o644)
		if file.GitMode == gitModeExec {
			mode = 0o755
		}
		if err := root.Publish(file.Path, file.Bytes, mode, safefs.PublicationReplace); err != nil {
			return nil, fmt.Errorf("workspace: install approved file %q: %w", file.Path, err)
		}
		digest := sha256.Sum256(file.Bytes)
		installed = append(installed, InstalledFile{
			Path: file.Path, SHA256: hex.EncodeToString(digest[:]), GitMode: file.GitMode,
		})
	}
	for _, name := range removes {
		if _, err := root.Lstat(name); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("workspace: re-inspect staged source %q: %w", name, err)
		}
		if err := root.Remove(name); err != nil {
			return nil, fmt.Errorf("workspace: remove staged source %q: %w", name, err)
		}
	}
	return installed, nil
}

func validateWorkspacePath(name string) error {
	if name == "" || name == "." || !fs.ValidPath(name) || strings.ContainsAny(name, "\\\x00\r\n") || path.Clean(name) != name {
		return fmt.Errorf("workspace: unsafe repository path %q", name)
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == ".git" || segment == ".." {
			return fmt.Errorf("workspace: unsafe repository path %q", name)
		}
	}
	return nil
}

func pathsOverlapRelative(left, right string) bool {
	if left == right {
		return true
	}
	return strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}
