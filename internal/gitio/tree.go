package gitio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/safefs"
)

const (
	// GitModeRegular is a non-executable regular file in a Git tree.
	GitModeRegular uint32 = 0o100644
	// GitModeExecutable is an executable regular file in a Git tree.
	GitModeExecutable uint32 = 0o100755
	// GitModeSymlink is a symbolic link in a Git tree.
	GitModeSymlink uint32 = 0o120000
)

// TreeFile is exact path, mode and blob content for a Git tree. WriteExactTree
// writes these bytes directly and does not consult ignore rules; it is intended
// for approved bytes as well as other controller-owned exact tree content.
type TreeFile struct {
	Path  string
	Mode  uint32
	Bytes []byte
}

// BaseMetadata binds a tree object to the paths tracked by that immutable tree.
// Its fields are private so callers cannot replace the tracked set with the
// builder's mutable HEAD or index contents.
type BaseMetadata struct {
	tree    contract.ObjectID
	format  ObjectFormat
	tracked map[string]struct{}
}

// Tree returns the immutable tree ID represented by this metadata.
func (m *BaseMetadata) Tree() contract.ObjectID {
	if m == nil {
		return ""
	}
	return m.tree
}

// TrackedPaths returns a sorted copy of the immutable base's tracked paths.
func (m *BaseMetadata) TrackedPaths() []string {
	if m == nil {
		return nil
	}
	paths := make([]string, 0, len(m.tracked))
	for path := range m.tracked {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// LoadBaseMetadata resolves an immutable commit/tree and captures its tracked
// paths through a fresh private index. Workspace HEAD and index are never read.
func LoadBaseMetadata(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, base contract.ObjectID) (_ *BaseMetadata, resultErr error) {
	if ctx == nil || git == nil {
		return nil, &GitError{Kind: ErrorInvalid, Operation: "load candidate base", Detail: "context and Git port are required"}
	}
	workspacePolicy, _, err := workspaceGitPolicy(policy)
	if err != nil {
		return nil, err
	}
	refs := NewRefPort(git, workspacePolicy)
	format, err := refs.ObjectFormat(ctx)
	if err != nil {
		return nil, err
	}
	tree, err := refs.ResolveTree(ctx, string(base))
	if err != nil {
		return nil, err
	}
	index, err := newPrivateIndex()
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, index.close()) }()

	indexPolicy, err := withRepositoryContext(workspacePolicy, map[string]string{"GIT_INDEX_FILE": index.path})
	if err != nil {
		return nil, err
	}
	read, err := git.Exec(ctx, []string{"read-tree", string(tree)}, nil, indexPolicy)
	if err != nil {
		return nil, err
	}
	if err := requireSuccess("read immutable base tree", read); err != nil {
		return nil, err
	}
	listed, err := git.Exec(ctx, []string{"ls-files", "--cached", "-z"}, nil, indexPolicy)
	if err != nil {
		return nil, err
	}
	if err := requireSuccess("list immutable base paths", listed); err != nil {
		return nil, err
	}
	paths, err := nulValues(listed.Stdout)
	if err != nil {
		return nil, &GitError{Kind: ErrorProcess, Operation: "list immutable base paths", Detail: "Git returned malformed NUL-delimited paths", Cause: err}
	}
	tracked := make(map[string]struct{}, len(paths))
	for _, trackedPath := range paths {
		if err := validateTreePath(trackedPath); err != nil {
			return nil, &GitError{Kind: ErrorProcess, Operation: "list immutable base paths", Detail: err.Error(), Cause: err}
		}
		tracked[trackedPath] = struct{}{}
	}
	return &BaseMetadata{tree: tree, format: format, tracked: tracked}, nil
}

// BuildCandidateTree loads base tracking metadata and captures the current
// workspace as a complete tree, independent of the workspace's HEAD and index.
func BuildCandidateTree(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, base contract.ObjectID) (contract.ObjectID, error) {
	metadata, err := LoadBaseMetadata(ctx, git, policy, base)
	if err != nil {
		return "", err
	}
	return SnapshotCandidateTree(ctx, git, policy, metadata)
}

// SnapshotCandidateTree captures a workspace relative to trusted base metadata.
// Tracked base paths survive new ignore rules; other ignored paths are removed
// according to native Git check-ignore evaluation.
func SnapshotCandidateTree(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, base *BaseMetadata) (contract.ObjectID, error) {
	if ctx == nil || git == nil || base == nil || base.tree == "" || (base.format != ObjectFormatSHA1 && base.format != ObjectFormatSHA256) {
		return "", &GitError{Kind: ErrorInvalid, Operation: "snapshot candidate tree", Detail: "context, Git port and trusted base metadata are required"}
	}
	workspacePolicy, root, err := workspaceGitPolicy(policy)
	if err != nil {
		return "", err
	}
	format, err := NewRefPort(git, workspacePolicy).ObjectFormat(ctx)
	if err != nil {
		return "", err
	}
	if format != base.format {
		return "", &GitError{Kind: ErrorInvalid, Operation: "snapshot candidate tree", Detail: "base metadata and workspace use different Git object formats"}
	}
	files, err := captureWorkspaceFiles(root)
	if err != nil {
		return "", err
	}
	paths := make([]string, len(files))
	for index, file := range files {
		paths[index] = file.Path
	}
	ignored, err := ignoredWorkspacePaths(ctx, git, workspacePolicy, root, paths)
	if err != nil {
		return "", err
	}
	included := files[:0]
	for _, file := range files {
		_, isIgnored := ignored[file.Path]
		_, trackedAtBase := base.tracked[file.Path]
		if trackedAtBase || !isIgnored {
			included = append(included, file)
		}
	}
	return writeExactTree(ctx, git, workspacePolicy, format, included)
}

// WriteExactTree writes the supplied bytes and modes into the workspace object
// database through a fresh private index. It does not apply Git ignore rules.
func WriteExactTree(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, files []TreeFile) (contract.ObjectID, error) {
	if ctx == nil || git == nil {
		return "", &GitError{Kind: ErrorInvalid, Operation: "write exact tree", Detail: "context and Git port are required"}
	}
	workspacePolicy, _, err := workspaceGitPolicy(policy)
	if err != nil {
		return "", err
	}
	format, err := NewRefPort(git, workspacePolicy).ObjectFormat(ctx)
	if err != nil {
		return "", err
	}
	return writeExactTree(ctx, git, workspacePolicy, format, files)
}

func writeExactTree(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, format ObjectFormat, files []TreeFile) (_ contract.ObjectID, resultErr error) {
	ordered := append([]TreeFile(nil), files...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	paths := make(map[string]struct{}, len(ordered))
	for _, file := range ordered {
		if err := validateTreePath(file.Path); err != nil {
			return "", &GitError{Kind: ErrorInvalid, Operation: "write exact tree", Detail: err.Error(), Cause: err}
		}
		if file.Mode != GitModeRegular && file.Mode != GitModeExecutable && file.Mode != GitModeSymlink {
			return "", &GitError{Kind: ErrorInvalid, Operation: "write exact tree", Detail: fmt.Sprintf("unsupported Git file mode %o for %q", file.Mode, file.Path)}
		}
		if _, duplicate := paths[file.Path]; duplicate {
			return "", &GitError{Kind: ErrorInvalid, Operation: "write exact tree", Detail: fmt.Sprintf("duplicate Git path %q", file.Path)}
		}
		paths[file.Path] = struct{}{}
	}
	for filePath := range paths {
		for parent := path.Dir(filePath); parent != "."; parent = path.Dir(parent) {
			if _, conflict := paths[parent]; conflict {
				return "", &GitError{Kind: ErrorInvalid, Operation: "write exact tree", Detail: fmt.Sprintf("file path %q is also a parent of %q", parent, filePath)}
			}
		}
	}
	index, err := newPrivateIndex()
	if err != nil {
		return "", err
	}
	defer func() { resultErr = errors.Join(resultErr, index.close()) }()
	indexPolicy, err := withRepositoryContext(policy, map[string]string{"GIT_INDEX_FILE": index.path})
	if err != nil {
		return "", err
	}
	empty, err := git.Exec(ctx, []string{"read-tree", "--empty"}, nil, indexPolicy)
	if err != nil {
		return "", err
	}
	if err := requireSuccess("create private tree index", empty); err != nil {
		return "", err
	}
	objectIDs := make(map[string]contract.ObjectID)
	indexInfo := make([]byte, 0)
	for _, file := range ordered {
		key := string(file.Bytes)
		object, ok := objectIDs[key]
		if !ok {
			hashed, err := git.Exec(ctx, []string{"hash-object", "-w", "--no-filters", "--stdin"}, file.Bytes, indexPolicy)
			if err != nil {
				return "", err
			}
			if err := requireSuccess("hash exact tree blob", hashed); err != nil {
				return "", err
			}
			object, err = objectIDForFormat(bytes.TrimSpace(hashed.Stdout), format)
			if err != nil {
				return "", err
			}
			objectIDs[key] = object
		}
		indexInfo = append(indexInfo, fmt.Sprintf("%o %s\t", file.Mode, object)...)
		indexInfo = append(indexInfo, file.Path...)
		indexInfo = append(indexInfo, 0)
	}
	if len(indexInfo) != 0 {
		updated, err := git.Exec(ctx, []string{"update-index", "-z", "--index-info"}, indexInfo, indexPolicy)
		if err != nil {
			return "", err
		}
		if err := requireSuccess("update private tree index", updated); err != nil {
			return "", err
		}
	}
	written, err := git.Exec(ctx, []string{"write-tree"}, nil, indexPolicy)
	if err != nil {
		return "", err
	}
	if err := requireSuccess("write candidate tree", written); err != nil {
		return "", err
	}
	return objectIDForFormat(bytes.TrimSpace(written.Stdout), format)
}

func captureWorkspaceFiles(root string) ([]TreeFile, error) {
	filesystem, err := safefs.OpenRoot(root)
	if err != nil {
		return nil, &GitError{Kind: ErrorInvalid, Operation: "read workspace tree", Detail: err.Error(), Cause: err}
	}
	defer filesystem.Close()
	files := make([]TreeFile, 0)
	var visit func(directory string) error
	visit = func(directory string) error {
		entries, err := filesystem.ReadDir(directory)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, child := range entries {
			name := child.Name()
			if strings.EqualFold(name, ".git") {
				continue
			}
			filePath := name
			if directory != "." {
				filePath = path.Join(directory, name)
			}
			info, err := filesystem.Lstat(filePath)
			if err != nil {
				return err
			}
			switch {
			case info.IsDir():
				if err := visit(filePath); err != nil {
					return err
				}
			case info.Mode()&fs.ModeSymlink != 0:
				target, err := filesystem.Readlink(filePath)
				if err != nil {
					return err
				}
				files = append(files, TreeFile{Path: filePath, Mode: GitModeSymlink, Bytes: []byte(target)})
			case info.Mode().IsRegular():
				contents, err := filesystem.ReadFile(filePath)
				if err != nil {
					return err
				}
				mode := GitModeRegular
				if info.Mode().Perm()&0o111 != 0 {
					mode = GitModeExecutable
				}
				files = append(files, TreeFile{Path: filePath, Mode: mode, Bytes: contents})
			default:
				return &GitError{Kind: ErrorInvalid, Operation: "read workspace tree", Detail: fmt.Sprintf("unsupported workspace entry %q", filePath)}
			}
		}
		return nil
	}
	if err := visit("."); err != nil {
		return nil, &GitError{Kind: ErrorInvalid, Operation: "read workspace tree", Detail: err.Error(), Cause: err}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func validateTreePath(value string) error {
	if !fs.ValidPath(value) || value == "." || strings.ContainsRune(value, '\x00') {
		return errors.New("Git path must be a non-empty canonical relative path")
	}
	for _, component := range strings.Split(value, "/") {
		if strings.EqualFold(component, ".git") {
			return errors.New("Git path may not contain a .git component")
		}
	}
	return nil
}

func objectIDForFormat(raw []byte, format ObjectFormat) (contract.ObjectID, error) {
	object, err := ParseObjectID(string(raw))
	if err != nil {
		return "", err
	}
	want := 40
	if format == ObjectFormatSHA256 {
		want = 64
	} else if format != ObjectFormatSHA1 {
		return "", &GitError{Kind: ErrorProcess, Operation: "Git object id", Detail: "unsupported repository object format"}
	}
	if len(object) != want {
		return "", &GitError{Kind: ErrorProcess, Operation: "Git object id", Detail: fmt.Sprintf("Git returned an object id with length %d for %s", len(object), format)}
	}
	return object, nil
}
