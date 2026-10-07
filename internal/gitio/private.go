package gitio

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"kogen-go/internal/contract"
)

// privateDirectory owns a controller-created directory. Git indexes and the
// isolated metadata directory used by check-ignore live here, never in a
// builder-controlled index or metadata path.
type privateDirectory struct {
	path string
}

func newPrivateDirectory(prefix string) (*privateDirectory, error) {
	directory, err := os.MkdirTemp("", prefix)
	if err != nil {
		return nil, &GitError{Kind: ErrorProcess, Operation: "create private Git directory", Detail: err.Error(), Cause: err}
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		_ = os.RemoveAll(directory)
		return nil, &GitError{Kind: ErrorProcess, Operation: "protect private Git directory", Detail: err.Error(), Cause: err}
	}
	return &privateDirectory{path: directory}, nil
}

func (d *privateDirectory) close() error {
	if d == nil || d.path == "" {
		return nil
	}
	err := os.RemoveAll(d.path)
	d.path = ""
	if err != nil {
		return &GitError{Kind: ErrorProcess, Operation: "remove private Git directory", Detail: err.Error(), Cause: err}
	}
	return nil
}

type privateIndex struct {
	directory *privateDirectory
	path      string
}

func newPrivateIndex() (*privateIndex, error) {
	directory, err := newPrivateDirectory("kogen-git-index-")
	if err != nil {
		return nil, err
	}
	return &privateIndex{directory: directory, path: filepath.Join(directory.path, "index")}, nil
}

func (i *privateIndex) close() error {
	if i == nil {
		return nil
	}
	return i.directory.close()
}

var repositoryContextEnvironment = [...]string{
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_COMMON_DIR",
	"GIT_INDEX_FILE",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_NAMESPACE",
	"GIT_PREFIX",
	"GIT_CEILING_DIRECTORIES",
	"GIT_DISCOVERY_ACROSS_FILESYSTEM",
}

// workspaceGitPolicy removes inherited repository selectors and resolves the
// workspace before any controller Git operation. Workspace Git metadata is
// accepted only when .git and its object directory are real directories.
func workspaceGitPolicy(policy contract.GitPolicy) (contract.GitPolicy, string, error) {
	if !filepath.IsAbs(policy.WorkingDirectory) || filepath.Clean(policy.WorkingDirectory) != policy.WorkingDirectory || strings.ContainsRune(policy.WorkingDirectory, '\x00') {
		return contract.GitPolicy{}, "", &GitError{Kind: ErrorInvalid, Operation: "workspace Git metadata", Detail: "working directory must be a clean absolute path"}
	}
	root, err := filepath.EvalSymlinks(policy.WorkingDirectory)
	if err != nil {
		return contract.GitPolicy{}, "", &GitError{Kind: ErrorInvalid, Operation: "workspace Git metadata", Detail: "cannot resolve workspace directory", Cause: err}
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return contract.GitPolicy{}, "", &GitError{Kind: ErrorInvalid, Operation: "workspace Git metadata", Detail: "workspace is not a directory", Cause: err}
	}
	if err := validateWorkspaceGitDirectory(root); err != nil {
		return contract.GitPolicy{}, "", err
	}
	clean, err := withoutRepositoryContext(policy)
	if err != nil {
		return contract.GitPolicy{}, "", err
	}
	clean.WorkingDirectory = root
	return clean, root, nil
}

func validateWorkspaceGitDirectory(root string) error {
	gitDirectory := filepath.Join(root, ".git")
	info, err := os.Lstat(gitDirectory)
	if err != nil {
		return &GitError{Kind: ErrorInvalid, Operation: "workspace Git metadata", Detail: "workspace .git must be a real directory", Cause: err}
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return &GitError{Kind: ErrorInvalid, Operation: "workspace Git metadata", Detail: "workspace .git must be a real directory"}
	}
	objects := filepath.Join(gitDirectory, "objects")
	info, err = os.Lstat(objects)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return &GitError{Kind: ErrorInvalid, Operation: "workspace Git metadata", Detail: "workspace .git/objects must be a real directory", Cause: err}
	}
	// Object writes may create a loose object below a two-hex fanout directory.
	// Refuse symlinks anywhere in the object database so Git cannot follow a
	// builder-supplied directory link while publishing candidate blobs/trees.
	err = filepath.WalkDir(objects, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == objects {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("symlink in workspace object database: %s", filepath.Base(path))
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported entry in workspace object database: %s", filepath.Base(path))
		}
		if filepath.Clean(path) == filepath.Join(objects, "info", "alternates") {
			return errors.New("workspace object alternates are not trusted")
		}
		return nil
	})
	if err != nil {
		return &GitError{Kind: ErrorInvalid, Operation: "workspace Git metadata", Detail: err.Error(), Cause: err}
	}
	return nil
}

func withoutRepositoryContext(policy contract.GitPolicy) (contract.GitPolicy, error) {
	env, err := parseEnvironment(policy.Environment)
	if err != nil {
		return contract.GitPolicy{}, &GitError{Kind: ErrorInvalid, Operation: "workspace Git metadata", Detail: err.Error(), Cause: err}
	}
	for _, key := range repositoryContextEnvironment {
		delete(env, key)
	}
	policy.Environment = environmentList(env)
	return policy, nil
}

func withRepositoryContext(policy contract.GitPolicy, values map[string]string) (contract.GitPolicy, error) {
	clean, err := withoutRepositoryContext(policy)
	if err != nil {
		return contract.GitPolicy{}, err
	}
	env, err := parseEnvironment(clean.Environment)
	if err != nil {
		return contract.GitPolicy{}, &GitError{Kind: ErrorInvalid, Operation: "workspace Git metadata", Detail: err.Error(), Cause: err}
	}
	for key, value := range values {
		env[key] = value
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]string, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, key+"="+env[key])
	}
	clean.Environment = entries
	return clean, nil
}

func newPrivateIgnoreGitDirectory(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, root string) (_ *privateDirectory, resultPolicy contract.GitPolicy, resultErr error) {
	directory, err := newPrivateDirectory("kogen-git-ignore-")
	if err != nil {
		return nil, contract.GitPolicy{}, err
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, directory.close())
		}
	}()
	result, err := git.Exec(ctx, []string{"init", "--bare", "--quiet", "--template=", directory.path}, nil, policy)
	if err != nil {
		return nil, contract.GitPolicy{}, err
	}
	if err := requireSuccess("init private ignore metadata", result); err != nil {
		return nil, contract.GitPolicy{}, err
	}
	exclude := filepath.Join(directory.path, "info", "exclude")
	if err := os.Remove(exclude); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, contract.GitPolicy{}, &GitError{Kind: ErrorProcess, Operation: "disable private Git excludes", Detail: err.Error(), Cause: err}
	}
	resultPolicy, err = withRepositoryContext(policy, map[string]string{
		"GIT_DIR":       directory.path,
		"GIT_WORK_TREE": root,
	})
	if err != nil {
		return nil, contract.GitPolicy{}, err
	}
	return directory, resultPolicy, nil
}

func requireSuccess(operation string, result contract.GitResult) error {
	if result.Process.ExitStatus == nil {
		return &GitError{Kind: ErrorProcess, Operation: operation, Detail: "supervisor returned no exit status"}
	}
	if *result.Process.ExitStatus != 0 {
		return exitError(operation, result)
	}
	return nil
}
