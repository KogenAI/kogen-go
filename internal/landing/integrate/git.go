package integrate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/process"
	"kogen-go/internal/safefs"
)

func validatePaths(repository, workspace string) (string, string, error) {
	if !cleanAbsolute(repository) || !cleanAbsolute(workspace) {
		return "", "", errors.New("landing integration: repository and workspace must be clean absolute paths")
	}
	repo, err := filepath.EvalSymlinks(repository)
	if err != nil {
		return "", "", fmt.Errorf("landing integration: resolve repository: %w", err)
	}
	work, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", "", fmt.Errorf("landing integration: resolve workspace: %w", err)
	}
	repoInfo, err := os.Stat(repo)
	if err != nil {
		return "", "", fmt.Errorf("landing integration: inspect repository: %w", err)
	}
	if !repoInfo.IsDir() {
		return "", "", errors.New("landing integration: repository is not a directory")
	}
	workInfo, err := os.Stat(work)
	if err != nil {
		return "", "", fmt.Errorf("landing integration: inspect workspace: %w", err)
	}
	if !workInfo.IsDir() {
		return "", "", errors.New("landing integration: workspace is not a directory")
	}
	if pathsOverlap(repo, work) {
		return "", "", errors.New("landing integration: repository and candidate workspace must be separate")
	}
	return repo, work, nil
}

func cleanAbsolute(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsRune(value, '\x00')
}

func pathsOverlap(left, right string) bool {
	relative, err := filepath.Rel(left, right)
	if err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return true
	}
	relative, err = filepath.Rel(right, left)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func branchRef(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, branch string) (string, error) {
	if branch == "" || strings.ContainsRune(branch, '\x00') {
		return "", errors.New("landing integration: target branch is empty or malformed")
	}
	ref := "refs/heads/" + branch
	result, err := git.Exec(ctx, []string{"check-ref-format", ref}, nil, policy)
	if err != nil {
		return "", fmt.Errorf("landing integration: validate target branch: %w", err)
	}
	if err := requireSuccess("validate target branch", result); err != nil {
		return "", err
	}
	return ref, nil
}

func fetchBase(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, repository, branch string) (contract.ObjectID, contract.ObjectID, error) {
	baseRef, err := branchRef(ctx, git, policy, branch)
	if err != nil {
		return "", "", err
	}
	fetchedRef := "refs/kogen/moved-base/" + branch
	result, err := git.Exec(ctx, []string{"check-ref-format", fetchedRef}, nil, policy)
	if err != nil {
		return "", "", fmt.Errorf("landing integration: validate private base ref: %w", err)
	}
	if err := requireSuccess("validate private base ref", result); err != nil {
		return "", "", err
	}
	args := []string{
		"fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head",
		repository, "+" + baseRef + ":" + fetchedRef,
	}
	result, err = git.Exec(ctx, args, nil, policy)
	if err != nil {
		return "", "", fmt.Errorf("landing integration: fetch target tip: %w", err)
	}
	if err := requireSuccess("fetch target tip", result); err != nil {
		return "", "", err
	}
	refs := gitio.NewRefPort(git, policy)
	base, err := refs.ResolveCommit(ctx, fetchedRef)
	if err != nil {
		return "", "", fmt.Errorf("landing integration: resolve fetched target tip: %w", err)
	}
	tree, err := refs.ResolveTree(ctx, string(base))
	if err != nil {
		return "", "", fmt.Errorf("landing integration: resolve fetched target tree: %w", err)
	}
	return base, tree, nil
}

func workspaceGit(processes contract.ProcessRunner, workspace string, environment process.Environment) (contract.GitPort, contract.GitPolicy, error) {
	if environment == nil || environment["PATH"] == "" {
		return nil, contract.GitPolicy{}, errors.New("landing integration: captured controller Git environment with PATH is required")
	}
	git := gitio.NewWorkspace(processes)
	policy := gitio.WorkspacePolicy(workspace, environment)
	return git, policy, nil
}

func originGit(processes contract.ProcessRunner, repository string, environment process.Environment) (contract.GitPort, contract.GitPolicy) {
	return gitio.NewOrigin(processes), gitio.OriginPolicy(repository, environment)
}

func resetWorkspaceGitConfig(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, workspace string) error {
	refs := gitio.NewRefPort(git, policy)
	format, err := refs.ObjectFormat(ctx)
	if err != nil {
		return fmt.Errorf("landing integration: inspect workspace object format: %w", err)
	}
	version := "0"
	extension := ""
	if format == gitio.ObjectFormatSHA256 {
		version = "1"
		extension = "\n[extensions]\n\tobjectformat = sha256\n"
	}
	contents := []byte("[core]\n\trepositoryformatversion = " + version + "\n\tfilemode = true\n\tbare = false\n\thooksPath = /dev/null\n\tfsmonitor = false\n\tautocrlf = false\n\texcludesfile = /dev/null\n\tattributesfile = /dev/null\n[commit]\n\tgpgsign = false\n[rebase]\n\tautostash = false\n\tupdaterefs = false\n[rerere]\n\tenabled = false\n" + extension)
	gitDirectory := filepath.Join(workspace, ".git")
	info, err := os.Lstat(gitDirectory)
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return errors.New("landing integration: workspace .git must be a real directory")
	}
	root, err := safefs.OpenRoot(gitDirectory)
	if err != nil {
		return fmt.Errorf("landing integration: open workspace Git directory for reset: %w", err)
	}
	defer root.Close()
	afterOpen, err := os.Lstat(gitDirectory)
	if err != nil || !afterOpen.IsDir() || afterOpen.Mode()&fs.ModeSymlink != 0 || !os.SameFile(info, afterOpen) {
		return errors.New("landing integration: workspace Git directory changed while opening its descriptor")
	}
	if info, err := root.Lstat("config"); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
			return errors.New("landing integration: workspace Git config must be a regular file")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("landing integration: inspect workspace Git config: %w", err)
	}
	if err := root.PublishPrivate("config", contents, safefs.PublicationReplace); err != nil {
		return fmt.Errorf("landing integration: reset untrusted workspace Git config: %w", err)
	}
	return nil
}

func requireSuccess(operation string, result contract.GitResult) error {
	if result.Process.ExitStatus == nil || result.Process.TimedOut || result.Process.Unavailable {
		return fmt.Errorf("landing integration: Git %s returned no usable exit status", operation)
	}
	if *result.Process.ExitStatus != 0 {
		detail := strings.TrimSpace(string(result.StderrTail))
		if detail == "" {
			detail = "Git exited unsuccessfully"
		}
		if len(detail) > 1024 {
			detail = detail[len(detail)-1024:]
		}
		return fmt.Errorf("landing integration: Git %s failed: %s", operation, detail)
	}
	return nil
}

func execChecked(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, args []string, stdin []byte, operation string) (contract.GitResult, error) {
	result, err := git.Exec(ctx, args, stdin, policy)
	if err != nil {
		return result, fmt.Errorf("landing integration: Git %s: %w", operation, err)
	}
	if err := requireSuccess(operation, result); err != nil {
		return result, err
	}
	return result, nil
}
