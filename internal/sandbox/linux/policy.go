package linux

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/safefs"
)

// Configuration is the complete filesystem policy for one process stage.
// Build callers list the candidate workspace in WritablePaths and checkout
// plus origin in WriteDeniedPaths. Checkout-stage callers list the checkout
// itself in WritablePaths. Linux keyrings are added from Home automatically.
type Configuration struct {
	RunDir             string
	Workspace          string
	Home               string
	WritablePaths      []string
	WriteDeniedPaths   []string
	ProtectedReadPaths []string
}

type validatedConfiguration struct {
	runDir             string
	workspace          string
	writablePaths      []string
	writeDeniedPaths   []string
	protectedReadPaths []string
}

func (c Configuration) validate() (validatedConfiguration, error) {
	runDir, err := cleanAbsolutePath(c.RunDir)
	if err != nil || runDir == string(filepath.Separator) {
		return validatedConfiguration{}, errors.New("sandbox: invalid run directory")
	}
	workspace, err := cleanAbsolutePath(c.Workspace)
	if err != nil || workspace == string(filepath.Separator) {
		return validatedConfiguration{}, errors.New("sandbox: invalid workspace")
	}
	if err := requirePrivateRunDir(runDir); err != nil {
		return validatedConfiguration{}, err
	}
	if err := requireDirectory(workspace); err != nil {
		return validatedConfiguration{}, fmt.Errorf("sandbox: inspect workspace: %w", err)
	}

	validated := validatedConfiguration{runDir: runDir, workspace: workspace}
	for _, path := range c.WritablePaths {
		path, err := cleanAbsolutePath(path)
		if err != nil || path == string(filepath.Separator) {
			return validatedConfiguration{}, errors.New("sandbox: invalid writable path")
		}
		validated.writablePaths = append(validated.writablePaths, path)
	}
	for _, path := range c.WriteDeniedPaths {
		path, err := cleanAbsolutePath(path)
		if err != nil || path == string(filepath.Separator) {
			return validatedConfiguration{}, errors.New("sandbox: invalid write-denied path")
		}
		validated.writeDeniedPaths = append(validated.writeDeniedPaths, path)
	}
	for _, path := range c.ProtectedReadPaths {
		path, err := cleanAbsolutePath(path)
		if err != nil || path == string(filepath.Separator) {
			return validatedConfiguration{}, errors.New("sandbox: invalid protected-read path")
		}
		validated.protectedReadPaths = append(validated.protectedReadPaths, path)
	}
	if c.Home != "" {
		home, err := cleanAbsolutePath(c.Home)
		if err != nil || home == string(filepath.Separator) {
			return validatedConfiguration{}, errors.New("sandbox: invalid home path")
		}
		validated.protectedReadPaths = append(validated.protectedReadPaths, filepath.Join(home, ".local/share/keyrings"))
	}
	validated.writablePaths = uniqueStrings(validated.writablePaths)
	validated.writeDeniedPaths = uniqueStrings(validated.writeDeniedPaths)
	validated.protectedReadPaths = uniqueStrings(validated.protectedReadPaths)
	return validated, nil
}

func requirePrivateRunDir(runDir string) error {
	info, err := os.Lstat(runDir)
	if err != nil {
		return fmt.Errorf("sandbox: inspect run directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("sandbox: run directory must be private and not a symlink")
	}
	root, err := safefs.OpenRoot(runDir)
	if err != nil {
		return fmt.Errorf("sandbox: open rooted run directory: %w", err)
	}
	defer root.Close()
	tmpInfo, err := root.Lstat("tmp")
	if errors.Is(err, fs.ErrNotExist) {
		if err := root.MkdirAll("tmp", 0o700); err != nil {
			return fmt.Errorf("sandbox: create private temp directory: %w", err)
		}
		tmpInfo, err = root.Lstat("tmp")
	}
	if err != nil {
		return fmt.Errorf("sandbox: inspect private temp directory: %w", err)
	}
	if !tmpInfo.IsDir() || tmpInfo.Mode()&os.ModeSymlink != 0 || tmpInfo.Mode().Perm()&0o077 != 0 {
		return errors.New("sandbox: temp directory must be private and not a symlink")
	}
	return nil
}

func requireDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("path is not a directory")
	}
	return nil
}

func cleanAbsolutePath(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, '\x00') {
		return "", errors.New("path must be a clean absolute path")
	}
	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("path contains a control character")
		}
	}
	return path, nil
}

func prepareWithTool(spec contract.ProcessSpec, config validatedConfiguration, tool string) (contract.ProcessSpec, error) {
	if spec.Executable == "" {
		return contract.ProcessSpec{}, errors.New("sandbox: child executable is required")
	}
	if _, err := cleanAbsolutePath(spec.Dir); err != nil {
		return contract.ProcessSpec{}, errors.New("sandbox: child working directory must be a clean absolute path")
	}
	args, err := bubblewrapArgs(spec, config)
	if err != nil {
		return contract.ProcessSpec{}, err
	}
	args = append(args, "--", spec.Executable)
	args = append(args, spec.Args...)
	child := spec
	child.Executable = tool
	child.Args = args
	return child, nil
}

func bubblewrapArgs(spec contract.ProcessSpec, config validatedConfiguration) ([]string, error) {
	args := []string{
		"--unshare-all",
		"--share-net",
		"--die-with-parent",
		"--new-session",
		"--cap-drop", "ALL",
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--bind", "/tmp", "/tmp",
	}

	writableCandidates := append([]string(nil), config.writablePaths...)
	writableCandidates = append(writableCandidates, config.workspace, filepath.Join(config.runDir, "logs"), filepath.Join(config.runDir, "tmp"), filepath.Join(config.runDir, "reports"), filepath.Join(config.runDir, "mise-state"), filepath.Join(config.runDir, "mise-cache"))
	writable, err := existingPaths(writableCandidates)
	if err != nil {
		return nil, fmt.Errorf("sandbox: normalize writable paths: %w", err)
	}
	writable = uniqueStrings(writable)
	sort.Strings(writable)
	for _, path := range writable {
		if path == "/tmp" {
			continue
		}
		exists, info, err := pathInfo(path)
		if err != nil {
			return nil, fmt.Errorf("sandbox: inspect writable path: %w", err)
		}
		if !exists {
			continue
		}
		if !info.IsDir() {
			return nil, errors.New("sandbox: writable path must be a directory")
		}
		args = append(args, "--bind", path, path)
	}

	denied, err := existingPaths(config.writeDeniedPaths)
	if err != nil {
		return nil, fmt.Errorf("sandbox: normalize write-denied paths: %w", err)
	}
	denied = uniqueStrings(denied)
	sort.Strings(denied)
	for _, path := range denied {
		exists, _, err := pathInfo(path)
		if err != nil {
			return nil, fmt.Errorf("sandbox: inspect write-denied path: %w", err)
		}
		if exists {
			args = append(args, "--ro-bind", path, path)
		}
	}

	protected, err := existingPaths(config.protectedReadPaths)
	if err != nil {
		return nil, fmt.Errorf("sandbox: normalize protected-read paths: %w", err)
	}
	protected = uniqueStrings(protected)
	sort.Strings(protected)
	for _, path := range protected {
		exists, info, err := pathInfo(path)
		if err != nil {
			return nil, fmt.Errorf("sandbox: inspect protected-read path: %w", err)
		}
		if !exists {
			continue
		}
		switch {
		case info.IsDir():
			args = append(args, "--tmpfs", path)
		case info.Mode().IsRegular():
			args = append(args, "--ro-bind", "/dev/null", path)
		default:
			return nil, errors.New("sandbox: protected-read path must be a directory or regular file")
		}
	}
	args = append(args, "--chdir", spec.Dir)
	for _, arg := range args {
		if len(arg) > 4<<10 || strings.ContainsRune(arg, '\x00') {
			return nil, errors.New("sandbox: generated bubblewrap argument is invalid or exceeds 4 KiB")
		}
	}
	return args, nil
}

func existingPaths(paths []string) ([]string, error) {
	result := make([]string, 0, len(paths))
	for _, value := range paths {
		path, exists, err := resolveExistingPath(value)
		if err != nil {
			return nil, err
		}
		if exists {
			result = append(result, path)
		}
	}
	return result, nil
}

func resolveExistingPath(path string) (string, bool, error) {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved), true, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", false, err
	}
	current := path
	var suffix []string
	for {
		info, err := os.Lstat(current)
		if err == nil {
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr != nil {
				if info.Mode()&os.ModeSymlink != 0 {
					return "", false, errors.New("dangling symlink in sandbox path")
				}
				return "", false, resolveErr
			}
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return filepath.Clean(resolved), false, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", false, err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false, nil
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func pathInfo(path string) (bool, fs.FileInfo, error) {
	resolved, exists, err := resolveExistingPath(path)
	if err != nil || !exists {
		return exists, nil, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil, nil
		}
		return false, nil, err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return false, nil, errors.New("sandbox path is not a regular file or directory")
	}
	return true, info, nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
