// Package protection builds approval-bound protected manifests and enforces
// them in mutable Build workspaces.
package protection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/intent"
	"kogen-go/internal/project"
	"kogen-go/internal/safefs"
	"kogen-go/internal/yamlmini"
)

const (
	// AbsentSHA256 binds a path that must remain absent.
	AbsentSHA256 = "23518d434b5b519ad017aaaa4ca8e63cc5402a8051dec5242c918913969cfeec"
	modeRegular  = 0o100644
	modeExec     = 0o100755
	modeSymlink  = 0o120000
)

var ErrCheckoutBehindBase = errors.New("environment/checkout_behind_base")

// Entry contains the digest persisted in approval.json and the source material
// needed to restore the exact base or approved bytes in a Build workspace.
// Mode is the Git mode for a present base file; approved own files use 100644.
type Entry struct {
	SHA256  string
	Bytes   []byte
	Mode    uint32
	Present bool
}

// Manifest maps canonical repository paths to their protected base/approved
// content. Absent paths carry AbsentSHA256 and no bytes.
type Manifest map[string]Entry

// Hashes returns the path-to-digest projection persisted in approval.json.
func (m Manifest) Hashes() map[string]string {
	result := make(map[string]string, len(m))
	for name, entry := range m {
		result[name] = entry.SHA256
	}
	return result
}

// BuildOptions binds manifest selection to the immutable origin base and the
// project/Intent whose approved bytes will be restored in a Build workspace.
type BuildOptions struct {
	BaseCommit     contract.ObjectID
	CheckoutRoot   string
	Config         *project.Config
	Intent         *intent.Intent
	CandidatePath  string
	CandidateBytes []byte
	// OwnFiles are controller-approved bytes at their Build workspace paths,
	// for any additional controller-owned files beyond Intent and acceptance.
	OwnFiles map[string][]byte
	// RemovedSources are source acceptance copies that must remain absent once
	// their approved bytes have been installed at a candidate path.
	RemovedSources []string
}

// BuildResult retains the manifest even when the checkout is stale so callers
// can include the precise mismatch paths in a diagnostic.
type BuildResult struct {
	Manifest Manifest
	Behind   []string
}

// CheckoutBehindBaseError reports non-own protected paths whose checkout
// bytes differ from the immutable base bytes.
type CheckoutBehindBaseError struct{ Paths []string }

func (e *CheckoutBehindBaseError) Error() string {
	return fmt.Sprintf("%s: %s", ErrCheckoutBehindBase, strings.Join(e.Paths, ", "))
}

func (e *CheckoutBehindBaseError) Is(target error) bool { return target == ErrCheckoutBehindBase }

// BuildManifest expands spec protected-path globs, infers gate programs, reads
// exact blobs from BaseCommit, and checks that every non-own selected checkout
// path still has its base bytes. Git calls stay behind the supervised GitPort.
func BuildManifest(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, options BuildOptions) (*BuildResult, error) {
	if ctx == nil || git == nil || options.CheckoutRoot == "" || options.Intent == nil || options.CandidatePath == "" {
		return nil, errors.New("protection: context, Git port, checkout root, Intent, and candidate path are required")
	}
	if err := gitio.ValidateObjectID(options.BaseCommit); err != nil {
		return nil, fmt.Errorf("protection base commit: %w", err)
	}
	root, err := safefs.OpenRoot(options.CheckoutRoot)
	if err != nil {
		return nil, fmt.Errorf("protection checkout root: %w", err)
	}
	defer root.Close()

	refs := gitio.NewRefPort(git, policy)
	baseCommit, err := refs.ResolveCommit(ctx, string(options.BaseCommit))
	if err != nil {
		return nil, fmt.Errorf("resolve protection base commit: %w", err)
	}
	baseTree, err := refs.ResolveTree(ctx, string(baseCommit))
	if err != nil {
		return nil, fmt.Errorf("resolve protection base tree: %w", err)
	}
	baseFiles, err := listBaseFiles(ctx, git, policy, baseTree)
	if err != nil {
		return nil, err
	}
	checkoutPaths, err := listCheckoutFiles(root)
	if err != nil {
		return nil, fmt.Errorf("enumerate protection checkout: %w", err)
	}
	checkoutSet := make(map[string]struct{}, len(checkoutPaths))
	for _, name := range checkoutPaths {
		checkoutSet[name] = struct{}{}
	}
	baseSet := make(map[string]struct{}, len(baseFiles))
	for name := range baseFiles {
		baseSet[name] = struct{}{}
	}

	selected := make(map[string]struct{})
	ownFiles := make(map[string][]byte, len(options.OwnFiles)+2)
	for name, content := range options.OwnFiles {
		if err := validateProtectedPath(name); err != nil {
			return nil, err
		}
		ownFiles[name] = bytes.Clone(content)
		selected[name] = struct{}{}
	}
	intentPath := ".kogen/intents/" + options.Intent.Slug + "/intent.md"
	if err := validateProtectedPath(intentPath); err != nil {
		return nil, err
	}
	if old, exists := ownFiles[intentPath]; exists && !bytes.Equal(old, options.Intent.RawBytes()) {
		return nil, fmt.Errorf("protection: approved Intent bytes conflict at %q", intentPath)
	}
	ownFiles[intentPath] = options.Intent.RawBytes()
	selected[intentPath] = struct{}{}
	if err := validateProtectedPath(options.CandidatePath); err != nil {
		return nil, err
	}
	if old, exists := ownFiles[options.CandidatePath]; exists && !bytes.Equal(old, options.CandidateBytes) {
		return nil, fmt.Errorf("protection: approved candidate bytes conflict at %q", options.CandidatePath)
	}
	for _, sourcePath := range options.RemovedSources {
		if sourcePath == options.CandidatePath {
			return nil, fmt.Errorf("protection: candidate path %q is also a removed source", options.CandidatePath)
		}
	}
	ownFiles[options.CandidatePath] = bytes.Clone(options.CandidateBytes)
	selected[options.CandidatePath] = struct{}{}
	removed := make(map[string]struct{}, len(options.RemovedSources))
	for _, name := range options.RemovedSources {
		if err := validateProtectedPath(name); err != nil {
			return nil, err
		}
		removed[name] = struct{}{}
	}
	for ownPath := range ownFiles {
		for sourcePath := range removed {
			if pathsOverlap(ownPath, sourcePath) {
				return nil, fmt.Errorf("protection: approved path %q overlaps removed source %q", ownPath, sourcePath)
			}
		}
	}

	config := yamlmini.Mapping(nil)
	if options.Config != nil {
		config = options.Config.Raw
	}
	for _, field := range []string{"protected_paths"} {
		patterns, err := stringList(config, field)
		if err != nil {
			return nil, err
		}
		if err := addPatterns(selected, patterns, baseSet, checkoutSet); err != nil {
			return nil, err
		}
	}
	changesGate := options.Intent.Frontmatter.ChangesGate
	if !changesGate {
		addLiteral(selected, ".kogen/project.yaml")
		gatePatterns, err := stringList(config, "gate_paths")
		if err != nil {
			return nil, err
		}
		if err := addPatterns(selected, gatePatterns, baseSet, checkoutSet); err != nil {
			return nil, err
		}
		for _, field := range []string{"checks", "acceptance_checks", "fix"} {
			commands, err := commandLists(config, field)
			if err != nil {
				return nil, err
			}
			for _, argv := range commands {
				addGatePrograms(selected, argv, baseSet, checkoutSet)
			}
		}
		acceptanceRun, err := acceptanceCommand(config)
		if err != nil {
			return nil, err
		}
		if len(acceptanceRun) != 0 {
			addGatePrograms(selected, acceptanceRun, baseSet, checkoutSet)
		}
	}
	for name := range removed {
		delete(selected, name)
	}

	baseOIDs := make(map[string]struct{})
	for name := range selected {
		if entry, exists := baseFiles[name]; exists {
			if entry.kind != "blob" || entry.mode != modeRegular && entry.mode != modeExec && entry.mode != modeSymlink {
				return nil, fmt.Errorf("protection: selected base path %q has unsupported Git mode/type %o %s", name, entry.mode, entry.kind)
			}
			baseOIDs[entry.oid] = struct{}{}
		}
	}
	blobs, err := readBlobs(ctx, git, policy, baseOIDs)
	if err != nil {
		return nil, err
	}

	result := &BuildResult{Manifest: make(Manifest, len(selected))}
	behind := make([]string, 0)
	names := sortedKeys(selected)
	for _, name := range names {
		if approved, isOwn := ownFiles[name]; isOwn {
			result.Manifest[name] = presentEntry(approved, modeRegular)
			continue
		}
		base, exists := baseFiles[name]
		if !exists {
			result.Manifest[name] = absentEntry()
			actual, _, err := workspaceHash(root, name)
			if err != nil {
				return nil, fmt.Errorf("inspect protected checkout path %q: %w", name, err)
			}
			if actual != AbsentSHA256 {
				behind = append(behind, name)
			}
			continue
		}
		content, ok := blobs[base.oid]
		if !ok {
			return nil, fmt.Errorf("protection: Git omitted selected blob for %q", name)
		}
		result.Manifest[name] = presentEntry(content, base.mode)
		actual, _, err := workspaceHash(root, name)
		if err != nil {
			return nil, fmt.Errorf("inspect protected checkout path %q: %w", name, err)
		}
		if actual != result.Manifest[name].SHA256 {
			behind = append(behind, name)
		}
	}
	result.Behind = behind
	if len(behind) != 0 {
		return result, &CheckoutBehindBaseError{Paths: append([]string(nil), behind...)}
	}
	return result, nil
}

func presentEntry(content []byte, mode uint32) Entry {
	copyOfBytes := bytes.Clone(content)
	return Entry{SHA256: digest(copyOfBytes), Bytes: copyOfBytes, Mode: mode, Present: true}
}

func absentEntry() Entry { return Entry{SHA256: AbsentSHA256} }

func digest(content []byte) string {
	hash := sha256.Sum256(content)
	return hex.EncodeToString(hash[:])
}

type treeFile struct {
	mode uint32
	kind string
	oid  string
}

func listBaseFiles(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, tree contract.ObjectID) (map[string]treeFile, error) {
	result, err := git.Exec(ctx, []string{"ls-tree", "-rz", "--full-tree", string(tree)}, nil, policy)
	if err != nil {
		return nil, fmt.Errorf("list protection base tree: %w", err)
	}
	if err := requireSuccess("list protection base tree", result); err != nil {
		return nil, err
	}
	entries := make(map[string]treeFile)
	for _, record := range bytes.Split(result.Stdout, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		separator := bytes.IndexByte(record, '\t')
		if separator < 0 {
			return nil, errors.New("protection: Git returned malformed NUL-delimited tree entry")
		}
		fields := strings.Split(string(record[:separator]), " ")
		if len(fields) != 3 {
			return nil, errors.New("protection: Git returned malformed tree metadata")
		}
		mode, err := strconv.ParseUint(fields[0], 8, 32)
		if err != nil {
			return nil, fmt.Errorf("protection: malformed Git tree mode %q", fields[0])
		}
		name := string(record[separator+1:])
		if name == "" {
			return nil, errors.New("protection: Git returned an empty tree path")
		}
		entries[name] = treeFile{mode: uint32(mode), kind: fields[1], oid: fields[2]}
	}
	return entries, nil
}

func readBlobs(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, oids map[string]struct{}) (map[string][]byte, error) {
	ordered := make([]string, 0, len(oids))
	for oid := range oids {
		ordered = append(ordered, oid)
	}
	sort.Strings(ordered)
	if len(ordered) == 0 {
		return map[string][]byte{}, nil
	}
	var input bytes.Buffer
	for _, oid := range ordered {
		input.WriteString(oid)
		input.WriteByte('\n')
	}
	result, err := git.Exec(ctx, []string{"cat-file", "--batch"}, input.Bytes(), policy)
	if err != nil {
		return nil, fmt.Errorf("read protection base blobs: %w", err)
	}
	if err := requireSuccess("read protection base blobs", result); err != nil {
		return nil, err
	}
	output := result.Stdout
	contents := make(map[string][]byte, len(ordered))
	for len(ordered) != 0 {
		lineEnd := bytes.IndexByte(output, '\n')
		if lineEnd < 0 {
			return nil, errors.New("protection: Git returned a truncated cat-file header")
		}
		fields := strings.Fields(string(output[:lineEnd]))
		if len(fields) != 3 || fields[1] != "blob" {
			return nil, errors.New("protection: Git returned a non-blob or malformed cat-file header")
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 || size > int64(len(output)-lineEnd-1) {
			return nil, errors.New("protection: Git returned an invalid cat-file blob size")
		}
		oid := fields[0]
		wantOID := ordered[0]
		if oid != wantOID {
			return nil, errors.New("protection: Git returned base blobs out of order")
		}
		start := lineEnd + 1
		end := start + int(size)
		if end >= len(output) || output[end] != '\n' {
			return nil, errors.New("protection: Git returned a malformed cat-file blob boundary")
		}
		contents[oid] = bytes.Clone(output[start:end])
		output = output[end+1:]
		ordered = ordered[1:]
	}
	if len(output) != 0 {
		return nil, errors.New("protection: Git returned trailing cat-file output")
	}
	return contents, nil
}

func requireSuccess(operation string, result contract.GitResult) error {
	if result.Process.ExitStatus == nil {
		return fmt.Errorf("protection: %s returned no exit status", operation)
	}
	if *result.Process.ExitStatus != 0 || result.Process.TimedOut || result.Process.Unavailable {
		return fmt.Errorf("protection: %s failed (exit %d): %s", operation, *result.Process.ExitStatus, strings.TrimSpace(string(result.StderrTail)))
	}
	if len(result.StderrTail) != 0 {
		return fmt.Errorf("protection: %s wrote to stderr: %s", operation, strings.TrimSpace(string(result.StderrTail)))
	}
	return nil
}

func listCheckoutFiles(root *safefs.Root) ([]string, error) {
	var names []string
	var visit func(string) error
	visit = func(directory string) error {
		entries, err := root.ReadDir(directory)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			name := entry.Name()
			if strings.EqualFold(name, ".git") {
				continue
			}
			full := name
			if directory != "." {
				full = path.Join(directory, name)
			}
			info, err := root.Lstat(full)
			if err != nil {
				return err
			}
			if info.IsDir() {
				if err := visit(full); err != nil {
					return err
				}
			} else {
				names = append(names, full)
			}
		}
		return nil
	}
	if err := visit("."); err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

func addPatterns(selected map[string]struct{}, patterns []string, base, checkout map[string]struct{}) error {
	candidates := make(map[string]struct{}, len(base)+len(checkout))
	for name := range base {
		candidates[name] = struct{}{}
	}
	for name := range checkout {
		candidates[name] = struct{}{}
	}
	for _, pattern := range patterns {
		glob, err := CompileGlob(pattern)
		if err != nil {
			return err
		}
		matched := false
		for name := range candidates {
			if glob.Match(name) {
				if err := validateProtectedPath(name); err != nil {
					return err
				}
				selected[name] = struct{}{}
				matched = true
			}
		}
		if !matched && !hasGlobMagic(pattern) && !strings.HasSuffix(pattern, "/") {
			if err := validateProtectedPath(pattern); err != nil {
				return err
			}
			selected[pattern] = struct{}{}
		}
	}
	return nil
}

func addLiteral(selected map[string]struct{}, name string) { selected[name] = struct{}{} }

func addGatePrograms(selected map[string]struct{}, argv []string, base, checkout map[string]struct{}) {
	if len(argv) == 0 || argv[0] == "" {
		return
	}
	program := argv[0]
	if path.Base(program) == "make" {
		for _, makefile := range []string{"Makefile", "GNUmakefile", "makefile"} {
			addIfPresent(selected, makefile, base, checkout)
		}
		return
	}
	interpreters := map[string]struct{}{
		"sh": {}, "bash": {}, "zsh": {}, "dash": {}, "python": {}, "python3": {},
		"ruby": {}, "node": {}, "perl": {}, "elixir": {}, "escript": {},
	}
	if _, ok := interpreters[path.Base(program)]; ok {
		program = ""
		for _, arg := range argv[1:] {
			if !strings.HasPrefix(arg, "-") {
				program = arg
				break
			}
		}
	}
	program = strings.TrimPrefix(program, "./")
	if program == "" || strings.HasPrefix(program, "/") {
		return
	}
	addIfPresent(selected, program, base, checkout)
}

func addIfPresent(selected map[string]struct{}, name string, base, checkout map[string]struct{}) {
	if name == "" || strings.HasPrefix(name, "/") {
		return
	}
	name = strings.TrimPrefix(name, "./")
	if _, ok := base[name]; ok {
		if validateProtectedPath(name) == nil {
			selected[name] = struct{}{}
		}
		return
	}
	if _, ok := checkout[name]; ok && validateProtectedPath(name) == nil {
		selected[name] = struct{}{}
	}
}

func stringList(config yamlmini.Mapping, field string) ([]string, error) {
	value, exists := config[field]
	if !exists {
		return nil, nil
	}
	rows, ok := value.(yamlmini.Sequence)
	if !ok {
		return nil, fmt.Errorf("protection: project %s must be a list", field)
	}
	result := make([]string, 0, len(rows))
	for _, row := range rows {
		text, ok := row.(string)
		if !ok {
			return nil, fmt.Errorf("protection: project %s must contain only strings", field)
		}
		result = append(result, text)
	}
	return result, nil
}

func commandLists(config yamlmini.Mapping, field string) ([][]string, error) {
	value, exists := config[field]
	if !exists {
		return nil, nil
	}
	rows, ok := value.(yamlmini.Sequence)
	if !ok {
		return nil, fmt.Errorf("protection: project %s must be a list", field)
	}
	commands := make([][]string, 0, len(rows))
	for _, row := range rows {
		check, ok := row.(yamlmini.Mapping)
		if !ok {
			return nil, fmt.Errorf("protection: project %s entries must be maps", field)
		}
		args, ok := check["argv"].(yamlmini.Sequence)
		if !ok {
			return nil, fmt.Errorf("protection: project %s argv must be a list", field)
		}
		argv := make([]string, 0, len(args))
		for _, arg := range args {
			text, ok := arg.(string)
			if !ok {
				return nil, fmt.Errorf("protection: project %s argv must contain only strings", field)
			}
			argv = append(argv, text)
		}
		commands = append(commands, argv)
	}
	return commands, nil
}

func acceptanceCommand(config yamlmini.Mapping) ([]string, error) {
	value, exists := config["acceptance"]
	if !exists {
		return nil, nil
	}
	acceptance, ok := value.(yamlmini.Mapping)
	if !ok {
		return nil, errors.New("protection: project acceptance must be a map")
	}
	run, exists := acceptance["run"]
	if !exists {
		return nil, nil
	}
	args, ok := run.(yamlmini.Sequence)
	if !ok {
		return nil, errors.New("protection: project acceptance.run must be a list")
	}
	argv := make([]string, 0, len(args))
	for _, arg := range args {
		text, ok := arg.(string)
		if !ok {
			return nil, errors.New("protection: project acceptance.run must contain only strings")
		}
		argv = append(argv, text)
	}
	return argv, nil
}

func validateProtectedPath(name string) error {
	if !fs.ValidPath(name) || name == "." {
		return fmt.Errorf("protection: unsafe path %q", name)
	}
	for _, component := range strings.Split(name, "/") {
		if strings.EqualFold(component, ".git") {
			return fmt.Errorf("protection: unsafe path %q", name)
		}
	}
	return nil
}

func pathsOverlap(first, second string) bool {
	return first == second || strings.HasPrefix(first, second+"/") || strings.HasPrefix(second, first+"/")
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	sort.Strings(keys)
	return keys
}
