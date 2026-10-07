package darwin

import (
	"crypto/rand"
	"encoding/hex"
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

const sandboxExecPath = "/usr/bin/sandbox-exec"

// Configuration contains the complete filesystem policy for one stage. A
// checkout policy includes the checkout among WritablePaths; a Build policy
// includes it in WriteDeniedPaths and uses the candidate workspace instead.
type Configuration struct {
	RunDir             string
	Workspace          string
	WritablePaths      []string
	WriteDeniedPaths   []string
	ProtectedReadPaths []string
}

// ProbeResult records a real positive/negative enforcement check without
// exposing user paths or probe file contents.
type ProbeResult struct {
	Available bool
	Reason    string
	Checks    []string
}

// ToolAvailable reports whether the fixed OS sandbox executable is present
// and executable. The caller still must run Probe before reporting confined.
func ToolAvailable() bool {
	info, err := os.Stat(sandboxExecPath)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// Profile returns the deterministic Seatbelt profile for configuration.
func Profile(configuration Configuration) ([]byte, error) {
	if _, _, err := validateConfiguration(configuration); err != nil {
		return nil, err
	}
	for _, path := range configuration.WritablePaths {
		if path == string(filepath.Separator) {
			return nil, errors.New("sandbox: writable path cannot be the filesystem root")
		}
	}
	readDenied, err := normalizedPaths(configuration.ProtectedReadPaths)
	if err != nil {
		return nil, fmt.Errorf("sandbox: normalize protected read paths: %w", err)
	}
	writable, err := normalizedPaths(append(append([]string(nil), configuration.WritablePaths...), "/tmp"))
	if err != nil {
		return nil, fmt.Errorf("sandbox: normalize writable paths: %w", err)
	}
	writeDenied, err := normalizedPaths(configuration.WriteDeniedPaths)
	if err != nil {
		return nil, fmt.Errorf("sandbox: normalize denied write paths: %w", err)
	}

	var profile strings.Builder
	profile.WriteString("(version 1)\n(deny default)\n(allow process*)\n(allow sysctl-read)\n(allow mach-lookup)\n(allow network*)\n")
	profile.WriteString("(allow file-read*)\n(allow file-write* (literal \"/dev/null\"))\n")
	for _, path := range writable {
		fmt.Fprintf(&profile, "(allow file-write* (subpath %s))\n", sbplLiteral(path))
	}
	// Denials follow the broad read and write grants so protected locations
	// cannot be reopened by an overlapping writable/cache prefix.
	for _, path := range readDenied {
		fmt.Fprintf(&profile, "(deny file-read* (subpath %s))\n", sbplLiteral(path))
	}
	for _, path := range writeDenied {
		fmt.Fprintf(&profile, "(deny file-write* (subpath %s))\n", sbplLiteral(path))
	}
	return []byte(profile.String()), nil
}

// Prepare writes the private profile through safefs and returns a supervised
// invocation of sandbox-exec plus a cleanup function. The profile leaf is
// created exclusively at mode 0600 under a mode 0700 run/tmp directory.
func Prepare(spec contract.ProcessSpec, configuration Configuration) (contract.ProcessSpec, func() error, error) {
	contents, err := Profile(configuration)
	if err != nil {
		return contract.ProcessSpec{}, nil, err
	}
	root, err := openPrivateRunRoot(configuration.RunDir)
	if err != nil {
		return contract.ProcessSpec{}, nil, err
	}
	name, err := randomName("sandbox-policy", ".sb")
	if err == nil {
		err = root.Publish("tmp/"+name, contents, 0o600, safefs.PublicationCreateOnly)
	}
	closeErr := root.Close()
	if err != nil {
		return contract.ProcessSpec{}, nil, errors.Join(err, closeErr)
	}
	if closeErr != nil {
		cleanupErr := removeRunFile(configuration.RunDir, "tmp/"+name)
		return contract.ProcessSpec{}, nil, errors.Join(closeErr, cleanupErr)
	}

	profilePath := filepath.Join(configuration.RunDir, "tmp", name)
	child := spec
	child.Executable = sandboxExecPath
	child.Args = make([]string, 0, len(spec.Args)+2)
	child.Args = append(child.Args, "-f", profilePath, spec.Executable)
	child.Args = append(child.Args, spec.Args...)
	cleanup := func() error { return removeRunFile(configuration.RunDir, "tmp/"+name) }
	return child, cleanup, nil
}

func validateConfiguration(configuration Configuration) (string, string, error) {
	runDir, err := cleanPath(configuration.RunDir)
	if err != nil {
		return "", "", fmt.Errorf("sandbox: invalid run directory: %w", err)
	}
	workspace, err := cleanPath(configuration.Workspace)
	if err != nil {
		return "", "", fmt.Errorf("sandbox: invalid workspace: %w", err)
	}
	if runDir == string(filepath.Separator) || workspace == string(filepath.Separator) {
		return "", "", errors.New("sandbox: run directory and workspace must not be the filesystem root")
	}
	return runDir, workspace, nil
}

func cleanPath(value string) (string, error) {
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsRune(value, '\x00') {
		return "", errors.New("path must be a clean absolute path")
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("path contains a control character")
		}
	}
	return value, nil
}

func normalizedPaths(paths []string) ([]string, error) {
	set := make(map[string]struct{}, len(paths)*2)
	for _, value := range paths {
		path, err := cleanPath(value)
		if err != nil {
			return nil, err
		}
		set[path] = struct{}{}
		if resolved := resolveExistingPrefix(path); resolved != path {
			set[resolved] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for path := range set {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}

func resolveExistingPrefix(path string) string {
	current := path
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return filepath.Clean(resolved)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func sbplLiteral(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	return "\"" + value + "\""
}

func openPrivateRunRoot(runDir string) (*safefs.Root, error) {
	runDir, err := cleanPath(runDir)
	if err != nil {
		return nil, fmt.Errorf("sandbox: invalid run directory: %w", err)
	}
	info, err := os.Lstat(runDir)
	if err != nil {
		return nil, fmt.Errorf("sandbox: inspect run directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("sandbox: run directory must be private and not a symlink")
	}
	root, err := safefs.OpenRoot(runDir)
	if err != nil {
		return nil, fmt.Errorf("sandbox: open rooted run directory: %w", err)
	}
	if err := root.MkdirAll("tmp", 0o700); err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("sandbox: create private temp directory: %w", err)
	}
	tmpInfo, err := root.Lstat("tmp")
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("sandbox: inspect private temp directory: %w", err)
	}
	if !tmpInfo.IsDir() || tmpInfo.Mode()&fs.ModeSymlink != 0 || tmpInfo.Mode().Perm()&0o077 != 0 {
		_ = root.Close()
		return nil, errors.New("sandbox: temp directory must be private and not a symlink")
	}
	return root, nil
}

func removeRunFile(runDir, name string) error {
	root, err := openPrivateRunRoot(runDir)
	if err != nil {
		return err
	}
	removeErr := root.Remove(name)
	closeErr := root.Close()
	if errors.Is(removeErr, fs.ErrNotExist) {
		removeErr = nil
	}
	return errors.Join(removeErr, closeErr)
}

func randomName(prefix, suffix string) (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("sandbox: generate private file name: %w", err)
	}
	return prefix + "-" + hex.EncodeToString(bytes[:]) + suffix, nil
}
