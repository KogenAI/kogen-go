package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/safefs"
)

var (
	ErrWorkspaceExists = errors.New("workspace: destination already exists")
	ErrSetupFailed     = errors.New("environment/setup_failed")
	ErrToolMissing     = errors.New("environment/tool_missing")
)

var (
	runIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	rungPattern  = regexp.MustCompile(`^R[1-9][0-9]*$`)
)

// Factory creates one detached, local workspace for each Build rung. Git must
// be the controlled workspace Git port (normally gitio.NewWorkspace), and
// Policy must be its fixed workspace policy rather than project child config.
type Factory struct {
	Git    contract.GitPort
	Policy contract.GitPolicy
}

// CloneRequest identifies the immutable source/base and the unique workspace
// leaf. The source is normally the local checkout containing the resolved
// origin base commit; it is never updated by this operation.
type CloneRequest struct {
	Source        string
	WorkspacesDir string
	RunID         string
	Rung          string
	BaseCommit    contract.ObjectID
}

// Workspace is a newly cloned workspace rooted at one detached base commit.
// Its path is absolute and its run/rung identity is safe for recovery naming.
type Workspace struct {
	Path       string
	RunID      string
	Rung       string
	BaseCommit contract.ObjectID
}

// Create clones the source with no hardlinks, no checkout, and no template,
// then checks out baseCommit detached. Existing destinations are never reused.
// If creation fails before this method returns, only its incomplete clone is
// removed; returned workspaces require their lifecycle owner's cleanup policy.
func (f Factory) Create(ctx context.Context, request CloneRequest) (_ Workspace, resultErr error) {
	if ctx == nil || f.Git == nil {
		return Workspace{}, errors.New("workspace: context and supervised Git port are required")
	}
	if !runIDPattern.MatchString(request.RunID) || !rungPattern.MatchString(request.Rung) {
		return Workspace{}, errors.New("workspace: run id or rung is not a safe workspace identity")
	}
	if err := gitio.ValidateObjectID(request.BaseCommit); err != nil {
		return Workspace{}, fmt.Errorf("workspace: invalid base commit: %w", err)
	}
	source, err := canonicalDirectory(request.Source)
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace: source: %w", err)
	}
	workspacesDir, err := canonicalDirectory(request.WorkspacesDir)
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace: workspaces directory: %w", err)
	}
	rootInfo, err := os.Stat(workspacesDir)
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace: inspect workspaces directory: %w", err)
	}
	if rootInfo.Mode().Perm()&0o022 != 0 {
		return Workspace{}, errors.New("workspace: workspaces directory must not be group or world writable")
	}
	if pathsOverlap(source, workspacesDir) {
		return Workspace{}, errors.New("workspace: source and workspaces directory must not overlap")
	}
	root, err := safefs.OpenRoot(workspacesDir)
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace: open workspaces directory: %w", err)
	}
	defer closeRoot(root)
	leaf := request.RunID + "-" + request.Rung
	if _, err := root.Lstat(leaf); err == nil {
		return Workspace{}, ErrWorkspaceExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Workspace{}, fmt.Errorf("workspace: inspect destination: %w", err)
	}
	if err := createWorkspaceDirectory(workspacesDir, leaf); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Workspace{}, ErrWorkspaceExists
		}
		return Workspace{}, fmt.Errorf("workspace: create destination: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			if cleanupErr := removeTree(root, leaf); cleanupErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("workspace: remove incomplete clone: %w", cleanupErr))
			}
		}
	}()
	info, err := root.Lstat(leaf)
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		if err == nil {
			err = errors.New("destination is not a real directory")
		}
		return Workspace{}, fmt.Errorf("workspace: validate destination: %w", err)
	}
	created := Workspace{
		Path:       filepath.Join(workspacesDir, leaf),
		RunID:      request.RunID,
		Rung:       request.Rung,
		BaseCommit: request.BaseCommit,
	}
	policy := f.Policy
	// The workspace Git port probes repository-local helper config before each
	// invocation. Run clone from the source repository so that probe uses a
	// repository context while clone itself still targets the explicit sibling.
	policy.WorkingDirectory = source
	cloneArgs := []string{
		"clone", "--quiet", "--local", "--no-hardlinks", "--no-checkout", "--template=", "--",
		source, created.Path,
	}
	if _, err := checkedGit(ctx, f.Git, cloneArgs, policy, "clone"); err != nil {
		return Workspace{}, err
	}
	policy.WorkingDirectory = created.Path
	if _, err := checkedGit(ctx, f.Git, []string{"checkout", "--quiet", "--detach", string(request.BaseCommit)}, policy, "checkout detached base"); err != nil {
		return Workspace{}, err
	}
	resolved, err := checkedGit(ctx, f.Git, []string{"rev-parse", "--verify", "HEAD^{commit}"}, policy, "verify workspace base")
	if err != nil {
		return Workspace{}, err
	}
	if strings.TrimSpace(string(resolved.Stdout)) != string(request.BaseCommit) {
		return Workspace{}, errors.New("workspace: detached checkout did not resolve to the requested base")
	}
	branch, err := f.Git.Exec(ctx, []string{"symbolic-ref", "--quiet", "HEAD"}, nil, policy)
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace: verify detached HEAD: %w", err)
	}
	if branch.Process.ExitStatus == nil || *branch.Process.ExitStatus != 1 || branch.Process.TimedOut || branch.Process.Unavailable || len(branch.Stdout) != 0 {
		return Workspace{}, errors.New("workspace: checkout HEAD is not detached")
	}
	complete = true
	return created, nil
}

func checkedGit(ctx context.Context, git contract.GitPort, args []string, policy contract.GitPolicy, operation string) (contract.GitResult, error) {
	result, err := git.Exec(ctx, args, nil, policy)
	if err != nil {
		return result, fmt.Errorf("workspace: Git %s: %w", operation, err)
	}
	if result.Process.ExitStatus == nil || *result.Process.ExitStatus != 0 || result.Process.TimedOut || result.Process.Unavailable {
		exit := "unknown"
		if result.Process.ExitStatus != nil {
			exit = fmt.Sprint(*result.Process.ExitStatus)
		}
		return result, fmt.Errorf("workspace: Git %s failed (exit=%s timed_out=%t unavailable=%t)", operation, exit, result.Process.TimedOut, result.Process.Unavailable)
	}
	return result, nil
}

func canonicalDirectory(value string) (string, error) {
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsRune(value, '\x00') {
		return "", errors.New("path must be a clean absolute directory")
	}
	resolved, err := filepath.EvalSymlinks(value)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	return resolved, nil
}

func pathsOverlap(left, right string) bool {
	relative, err := filepath.Rel(left, right)
	if err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return true
	}
	relative, err = filepath.Rel(right, left)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func closeRoot(root *safefs.Root) {
	if root != nil {
		_ = root.Close()
	}
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
