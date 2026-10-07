package setupcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kogen-go/internal/approval/prepare"
	"kogen-go/internal/safefs"
	"kogen-go/internal/workspace"
)

type setupIndex struct {
	Version int      `json:"v"`
	Keys    []string `json:"keys"`
}

type setupManifest struct {
	Version int           `json:"v"`
	Key     string        `json:"key"`
	Outputs []setupOutput `json:"outputs"`
}

type setupOutput struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Mode   uint32 `json:"mode"`
	Target string `json:"target,omitempty"`
}

type setupKeyDocument struct {
	Version   int                  `json:"v"`
	Inputs    []setupInputIdentity `json:"inputs"`
	Outputs   []string             `json:"outputs"`
	Checks    []setupCheckIdentity `json:"checks"`
	ChildEnv  []string             `json:"child_env"`
	Toolchain map[string]string    `json:"toolchain"`
	OS        string               `json:"os"`
	Arch      string               `json:"arch"`
}

type setupInputIdentity struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Mode   uint32 `json:"mode"`
	SHA256 string `json:"sha256,omitempty"`
	Target string `json:"target,omitempty"`
}

type setupCheckIdentity struct {
	Name    string   `json:"name"`
	Adapter string   `json:"adapter"`
	Program string   `json:"program"`
	Args    []string `json:"args"`
	Env     []string `json:"env"`
	Timeout int64    `json:"timeout_ms"`
}

// Run returns the v2 setup key and either restores its output snapshot or runs
// the supplied setup effect once. Setup identity is deliberately independent
// of BaseTree: source-only changes outside setup_inputs can reuse products.
func (c *Cache) Run(ctx context.Context, request prepare.SetupRequest, run func(context.Context) error) (prepare.SetupResult, error) {
	if err := checkContext(ctx); err != nil {
		return prepare.SetupResult{}, err
	}
	if c == nil || c.root == nil || run == nil {
		return prepare.SetupResult{}, errors.New("setupcache: cache and setup operation are required")
	}
	key, cacheable, outputs, err := setupIdentity(request)
	if err != nil {
		return prepare.SetupResult{}, err
	}
	if !cleanDirectoryPath(request.Workspace) {
		return prepare.SetupResult{}, errors.New("setupcache: workspace must be a clean absolute path")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if err := checkContext(ctx); err != nil {
		return prepare.SetupResult{}, err
	}
	if cacheable {
		keys := c.readSetupIndex()
		if containsKey(keys, key) {
			manifest, err := c.loadSetupManifest("setup/v2/"+key, key, outputs)
			if err == nil {
				if err := c.restoreSetupOutputs(request.Workspace, "setup/v2/"+key, manifest.Outputs); err != nil {
					return prepare.SetupResult{}, fmt.Errorf("setupcache: restore cached setup products: %w", err)
				}
				keys = touchKey(keys, key)
				if err := c.writeSetupIndex(keys); err != nil {
					return prepare.SetupResult{}, fmt.Errorf("setupcache: record setup cache use: %w", err)
				}
				return prepare.SetupResult{Key: key, Reused: true}, nil
			}
			if err := removeTree(c.root, "setup/v2/"+key); err != nil {
				return prepare.SetupResult{}, fmt.Errorf("setupcache: remove invalid setup entry: %w", err)
			}
			keys = removeKey(keys, key)
			if err := c.writeSetupIndex(keys); err != nil {
				return prepare.SetupResult{}, fmt.Errorf("setupcache: remove invalid setup index entry: %w", err)
			}
		}
	}

	if err := run(ctx); err != nil {
		return prepare.SetupResult{}, err
	}
	if err := checkContext(ctx); err != nil {
		return prepare.SetupResult{}, err
	}
	if !cacheable {
		token, err := randomToken()
		if err != nil {
			return prepare.SetupResult{}, fmt.Errorf("setupcache: make volatile setup identity: %w", err)
		}
		volatile := "uncacheable-" + token
		if err := c.saveSetupEntry(request.Workspace, "setup/volatile/"+token, volatile, outputs); err != nil {
			return prepare.SetupResult{}, fmt.Errorf("setupcache: retain setup products for baseline restoration: %w", err)
		}
		return prepare.SetupResult{Key: volatile}, nil
	}

	if err := c.saveSetupEntry(request.Workspace, "setup/v2/"+key, key, outputs); err != nil {
		return prepare.SetupResult{}, fmt.Errorf("setupcache: publish setup products: %w", err)
	}
	keys := touchKey(c.readSetupIndex(), key)
	var evicted []string
	if len(keys) > setupLRULimit {
		evicted = append(evicted, keys[setupLRULimit:]...)
		keys = keys[:setupLRULimit]
	}
	if err := c.writeSetupIndex(keys); err != nil {
		return prepare.SetupResult{}, fmt.Errorf("setupcache: publish setup LRU: %w", err)
	}
	for _, oldKey := range evicted {
		if err := removeTree(c.root, "setup/v2/"+oldKey); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return prepare.SetupResult{}, fmt.Errorf("setupcache: evict setup entry: %w", err)
		}
	}
	return prepare.SetupResult{Key: key}, nil
}

// Restore reapplies setup products after the approval adapter resets a
// mutating baseline check to the exact base tree. It does not execute setup.
func (c *Cache) Restore(ctx context.Context, key, destination string) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if c == nil || c.root == nil {
		return errors.New("setupcache: cache is unavailable")
	}
	if !cleanDirectoryPath(destination) {
		return errors.New("setupcache: restore destination must be a clean absolute path")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := checkContext(ctx); err != nil {
		return err
	}
	entry := ""
	switch {
	case sha256Identity.MatchString(key):
		if !containsKey(c.readSetupIndex(), key) {
			return errors.New("setupcache: setup key is not present in the v2 cache")
		}
		entry = "setup/v2/" + key
	case volatileKey.MatchString(key):
		entry = "setup/volatile/" + strings.TrimPrefix(key, "uncacheable-")
	default:
		return errors.New("setupcache: unknown setup key format")
	}
	manifest, err := c.loadSetupManifest(entry, key, nil)
	if err != nil {
		return fmt.Errorf("setupcache: read setup product snapshot: %w", err)
	}
	return c.restoreSetupOutputs(destination, entry, manifest.Outputs)
}

func setupIdentity(request prepare.SetupRequest) (string, bool, []string, error) {
	if request.Workspace == "" {
		return "", false, nil, errors.New("setupcache: workspace is required")
	}
	if !cleanDirectoryPath(request.Workspace) {
		return "", false, nil, errors.New("setupcache: workspace must be a clean absolute path")
	}
	inputs := append([]string{}, request.Inputs...)
	outputs := append([]string{}, request.Outputs...)
	for _, item := range inputs {
		if !validRelativePath(item) {
			return "", false, nil, fmt.Errorf("setupcache: unsafe setup input path %q", item)
		}
	}
	for _, item := range outputs {
		if !validRelativePath(item) {
			return "", false, nil, fmt.Errorf("setupcache: unsafe setup output path %q", item)
		}
	}
	sort.Strings(inputs)
	sort.Strings(outputs)
	for index, current := range outputs {
		if index > 0 && isWithinPath(outputs[index-1], current) {
			return "", false, nil, errors.New("setupcache: setup output paths overlap")
		}
	}

	workspaceRoot, err := safefs.OpenRoot(request.Workspace)
	if err != nil {
		return "", false, nil, fmt.Errorf("setupcache: open workspace root: %w", err)
	}
	defer workspaceRoot.Close()

	document := setupKeyDocument{
		Version:   setupVersion,
		Inputs:    make([]setupInputIdentity, 0),
		Outputs:   outputs,
		Checks:    make([]setupCheckIdentity, len(request.Checks)),
		ChildEnv:  canonicalMap(request.KeyEnvironment),
		Toolchain: cloneMap(request.Toolchain), OS: request.OS, Arch: request.Arch,
	}
	cacheable := request.ToolchainKnown && strings.TrimSpace(request.OS) != "" && strings.TrimSpace(request.Arch) != ""
	for name, version := range request.Toolchain {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(version) == "" {
			cacheable = false
		}
	}
	if document.ChildEnv == nil {
		return "", false, nil, errors.New("setupcache: invalid environment identity")
	}

	inputKnown := true
	for _, input := range inputs {
		identities, known, err := collectInputIdentity(workspaceRoot, input)
		if err != nil {
			return "", false, nil, err
		}
		document.Inputs = append(document.Inputs, identities...)
		inputKnown = inputKnown && known
	}
	cacheable = cacheable && inputKnown
	for index, spec := range request.Checks {
		if spec.Name == "" || spec.Program == "" || spec.Timeout <= 0 || spec.Timeout%time.Millisecond != 0 {
			return "", false, nil, fmt.Errorf("setupcache: setup check %d is not normalized", index+1)
		}
		if strings.ContainsAny(spec.Name+spec.Adapter+spec.Program, "\x00") {
			return "", false, nil, fmt.Errorf("setupcache: setup check %d contains NUL", index+1)
		}
		for _, arg := range spec.Args {
			if strings.ContainsRune(arg, '\x00') {
				return "", false, nil, fmt.Errorf("setupcache: setup check %d has an invalid argument", index+1)
			}
		}
		env, known := canonicalList(spec.Env)
		cacheable = cacheable && known
		document.Checks[index] = setupCheckIdentity{
			Name: spec.Name, Adapter: spec.Adapter, Program: spec.Program,
			Args: append([]string{}, spec.Args...), Env: env,
			Timeout: spec.Timeout.Milliseconds(),
		}
	}
	for _, entry := range document.ChildEnv {
		if _, _, ok := strings.Cut(entry, "="); !ok {
			return "", false, nil, errors.New("setupcache: invalid environment identity")
		}
	}

	encoded, err := json.Marshal(document)
	if err != nil {
		return "", false, nil, fmt.Errorf("setupcache: encode v2 setup identity: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), cacheable, outputs, nil
}

func canonicalMap(values map[string]string) []string {
	if values == nil {
		return []string{}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]string, 0, len(keys))
	for _, key := range keys {
		value := values[key]
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, '\x00') {
			return nil
		}
		if excludedSetupEnvironmentKey(key) {
			continue
		}
		entries = append(entries, key+"="+value)
	}
	return entries
}

func canonicalList(values []string) ([]string, bool) {
	ordered := append([]string{}, values...)
	sort.Strings(ordered)
	result := make([]string, 0, len(ordered))
	seen := map[string]struct{}{}
	for _, entry := range ordered {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" || strings.ContainsRune(key, '\x00') || strings.ContainsRune(entry, '\x00') {
			return result, false
		}
		if _, exists := seen[key]; exists {
			return result, false
		}
		seen[key] = struct{}{}
		if excludedSetupEnvironmentKey(key) {
			continue
		}
		result = append(result, entry)
	}
	return result, true
}

func excludedSetupEnvironmentKey(key string) bool {
	return key == "MISE_STATE_DIR" || key == "MISE_CACHE_DIR" || key == "MISE_TRUSTED_CONFIG_PATHS"
}

func collectInputIdentity(root *safefs.Root, name string) ([]setupInputIdentity, bool, error) {
	identities := make([]setupInputIdentity, 0)
	known, err := walkInput(root, name, &identities)
	return identities, known, err
}

func walkInput(root *safefs.Root, name string, identities *[]setupInputIdentity) (bool, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		*identities = append(*identities, setupInputIdentity{Path: name, Kind: "missing"})
		return true, nil
	}
	if err != nil {
		*identities = append(*identities, setupInputIdentity{Path: name, Kind: "unknown"})
		return false, nil
	}
	mode := uint32(info.Mode().Perm())
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := root.Readlink(name)
		if err != nil || strings.ContainsRune(target, '\x00') {
			*identities = append(*identities, setupInputIdentity{Path: name, Kind: "unknown"})
			return false, nil
		}
		*identities = append(*identities, setupInputIdentity{Path: name, Kind: "symlink", Mode: mode, Target: target})
		// The key hashes declared source bytes. A symlink can refer to content
		// outside those declared bytes, so its target identity is insufficient
		// evidence for reuse.
		return false, nil
	case info.IsDir():
		*identities = append(*identities, setupInputIdentity{Path: name, Kind: "directory", Mode: mode})
		entries, err := root.ReadDir(name)
		if err != nil {
			return false, nil
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		known := true
		for _, entry := range entries {
			child := name + "/" + entry.Name()
			childKnown, err := walkInput(root, child, identities)
			if err != nil {
				return false, err
			}
			known = known && childKnown
		}
		return known, nil
	case info.Mode().IsRegular():
		data, err := root.ReadFile(name)
		if err != nil {
			*identities = append(*identities, setupInputIdentity{Path: name, Kind: "unknown"})
			return false, nil
		}
		digest := sha256.Sum256(data)
		*identities = append(*identities, setupInputIdentity{Path: name, Kind: "file", Mode: mode, SHA256: hex.EncodeToString(digest[:])})
		return true, nil
	default:
		*identities = append(*identities, setupInputIdentity{Path: name, Kind: "unknown"})
		return false, nil
	}
}

func (c *Cache) saveSetupEntry(workspaceDirectory, final, key string, outputs []string) error {
	token, err := randomToken()
	if err != nil {
		return err
	}
	parent := path.Dir(final)
	stage := parent + "/.stage-" + token
	if err := ensurePrivateDirectory(c.root, stage); err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			_ = removeTree(c.root, stage)
		}
	}()
	if err := ensurePrivateDirectory(c.root, stage+"/outputs"); err != nil {
		return err
	}
	workspaceRoot, err := safefs.OpenRoot(workspaceDirectory)
	if err != nil {
		return fmt.Errorf("open setup workspace: %w", err)
	}
	defer workspaceRoot.Close()
	manifest := setupManifest{Version: setupVersion, Key: key, Outputs: make([]setupOutput, 0, len(outputs))}
	for _, output := range outputs {
		info, err := workspaceRoot.Lstat(output)
		if errors.Is(err, fs.ErrNotExist) {
			manifest.Outputs = append(manifest.Outputs, setupOutput{Path: output, Kind: "missing"})
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect setup output %q: %w", output, err)
		}
		stored := path.Join(stage, "outputs", output)
		if err := ensurePrivateDirectory(c.root, path.Dir(stored)); err != nil {
			return err
		}
		mode := uint32(info.Mode().Perm())
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := workspaceRoot.Readlink(output)
			if err != nil || !safeLinkTarget(output, target) {
				return fmt.Errorf("setup output %q has an unsafe symlink", output)
			}
			if err := c.root.Symlink(target, stored); err != nil {
				return fmt.Errorf("store setup output symlink %q: %w", output, err)
			}
			manifest.Outputs = append(manifest.Outputs, setupOutput{Path: output, Kind: "symlink", Mode: mode, Target: target})
		case info.IsDir():
			if _, err := workspace.SeedDirectory(workspaceDirectory, output, c.rootDir, filepath.FromSlash(stored)); err != nil {
				return fmt.Errorf("copy setup output directory %q: %w", output, err)
			}
			manifest.Outputs = append(manifest.Outputs, setupOutput{Path: output, Kind: "directory", Mode: mode})
		case info.Mode().IsRegular():
			data, err := workspaceRoot.ReadFile(output)
			if err != nil {
				return fmt.Errorf("read setup output file %q: %w", output, err)
			}
			if err := c.root.Publish(stored, data, fs.FileMode(mode), safefs.PublicationCreateOnly); err != nil {
				return fmt.Errorf("store setup output file %q: %w", output, err)
			}
			manifest.Outputs = append(manifest.Outputs, setupOutput{Path: output, Kind: "file", Mode: mode})
		default:
			return fmt.Errorf("setup output %q is not a regular file, directory, or safe symlink", output)
		}
	}
	if err := writeJSON(c.root, stage+"/manifest.json", manifest); err != nil {
		return fmt.Errorf("write setup snapshot manifest: %w", err)
	}
	if err := removeTree(c.root, final); err != nil {
		return err
	}
	if err := c.root.Rename(stage, final); err != nil {
		return err
	}
	complete = true
	return nil
}

func (c *Cache) loadSetupManifest(entry, key string, expectedOutputs []string) (setupManifest, error) {
	var manifest setupManifest
	if err := readJSON(c.root, entry+"/manifest.json", &manifest); err != nil {
		return setupManifest{}, err
	}
	if manifest.Version != setupVersion || manifest.Key != key || manifest.Outputs == nil {
		return setupManifest{}, errors.New("setupcache: setup entry has an incompatible version or identity")
	}
	paths := make([]string, len(manifest.Outputs))
	for index, output := range manifest.Outputs {
		if !validRelativePath(output.Path) || index > 0 && manifest.Outputs[index-1].Path >= output.Path {
			return setupManifest{}, errors.New("setupcache: setup entry has invalid output paths")
		}
		paths[index] = output.Path
		switch output.Kind {
		case "missing", "directory", "file", "symlink":
		default:
			return setupManifest{}, errors.New("setupcache: setup entry has an invalid output kind")
		}
		if output.Kind == "symlink" && !safeLinkTarget(output.Path, output.Target) {
			return setupManifest{}, errors.New("setupcache: setup entry has an unsafe symlink")
		}
	}
	if expectedOutputs != nil && !equalStrings(paths, expectedOutputs) {
		return setupManifest{}, errors.New("setupcache: setup entry output paths do not match the request")
	}
	return manifest, nil
}

func (c *Cache) restoreSetupOutputs(destination, entry string, outputs []setupOutput) error {
	workspaceRoot, err := safefs.OpenRoot(destination)
	if err != nil {
		return fmt.Errorf("open restore destination: %w", err)
	}
	defer workspaceRoot.Close()
	for _, output := range outputs {
		if err := removeTree(workspaceRoot, output.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove prior setup output %q: %w", output.Path, err)
		}
		if output.Kind == "missing" {
			continue
		}
		stored := path.Join(entry, "outputs", output.Path)
		if output.Kind != "directory" {
			if err := ensureDestinationParents(workspaceRoot, path.Dir(output.Path)); err != nil {
				return fmt.Errorf("prepare setup output parent for %q: %w", output.Path, err)
			}
		}
		switch output.Kind {
		case "directory":
			if _, err := workspace.SeedDirectory(c.rootDir, filepath.FromSlash(stored), destination, output.Path); err != nil {
				return fmt.Errorf("restore setup output directory %q: %w", output.Path, err)
			}
		case "file":
			info, err := c.root.Lstat(stored)
			if err != nil || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
				return fmt.Errorf("setup output file %q is missing or unsafe", output.Path)
			}
			data, err := c.root.ReadFile(stored)
			if err != nil {
				return err
			}
			if err := workspaceRoot.Publish(output.Path, data, fs.FileMode(output.Mode), safefs.PublicationCreateOnly); err != nil {
				return fmt.Errorf("publish setup output file %q: %w", output.Path, err)
			}
		case "symlink":
			target, err := c.root.Readlink(stored)
			if err != nil || target != output.Target {
				return fmt.Errorf("setup output symlink %q is missing or changed", output.Path)
			}
			if err := workspaceRoot.Symlink(target, output.Path); err != nil {
				return fmt.Errorf("publish setup output symlink %q: %w", output.Path, err)
			}
		default:
			return errors.New("setupcache: unsupported setup output kind")
		}
	}
	return nil
}

func ensureDestinationParents(root *safefs.Root, name string) error {
	if name == "." {
		return nil
	}
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
			if err := root.MkdirAll(current, 0o700); err != nil {
				return err
			}
			info, err = root.Lstat(current)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is not a real directory", current)
		}
	}
	return nil
}

func safeLinkTarget(name, target string) bool {
	if target == "" || path.IsAbs(target) || strings.ContainsRune(target, '\x00') || strings.Contains(target, "\\") {
		return false
	}
	resolved := path.Clean(path.Join(path.Dir(name), target))
	return resolved != ".." && !strings.HasPrefix(resolved, "../") && !path.IsAbs(resolved)
}

func (c *Cache) readSetupIndex() []string {
	var index setupIndex
	if err := readJSON(c.root, setupIndexPath, &index); err != nil || index.Version != setupVersion || index.Keys == nil {
		return []string{}
	}
	result := make([]string, 0, len(index.Keys))
	seen := map[string]struct{}{}
	for _, key := range index.Keys {
		if !sha256Identity.MatchString(key) {
			return []string{}
		}
		if _, ok := seen[key]; ok {
			return []string{}
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	if len(result) > setupLRULimit {
		return []string{}
	}
	return result
}

func (c *Cache) writeSetupIndex(keys []string) error {
	if len(keys) > setupLRULimit {
		keys = keys[:setupLRULimit]
	}
	return writeJSON(c.root, setupIndexPath, setupIndex{Version: setupVersion, Keys: append([]string(nil), keys...)})
}

func touchKey(keys []string, key string) []string {
	result := make([]string, 0, len(keys)+1)
	result = append(result, key)
	for _, current := range keys {
		if current != key {
			result = append(result, current)
		}
	}
	return result
}

func removeKey(keys []string, key string) []string {
	result := make([]string, 0, len(keys))
	for _, current := range keys {
		if current != key {
			result = append(result, current)
		}
	}
	return result
}

func containsKey(keys []string, key string) bool {
	for _, current := range keys {
		if current == key {
			return true
		}
	}
	return false
}

func cloneMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
