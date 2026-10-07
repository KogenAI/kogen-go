package gitio

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"kogen-go/internal/contract"
)

// ignoredWorkspacePaths asks Git to evaluate the workspace's .gitignore files
// with an isolated metadata directory. This preserves nested rules and
// negation while excluding workspace info/exclude, config and global excludes.
func ignoredWorkspacePaths(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, root string, paths []string) (_ map[string]struct{}, resultErr error) {
	ignored := make(map[string]struct{})
	if len(paths) == 0 {
		return ignored, nil
	}
	metadata, ignorePolicy, err := newPrivateIgnoreGitDirectory(ctx, git, policy, root)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, metadata.close()) }()

	input := make([]byte, 0)
	for _, path := range paths {
		input = append(input, path...)
		input = append(input, 0)
	}
	result, err := git.Exec(ctx, []string{"check-ignore", "--no-index", "-z", "--stdin"}, input, ignorePolicy)
	if err != nil {
		return nil, err
	}
	if result.Process.ExitStatus == nil {
		return nil, &GitError{Kind: ErrorProcess, Operation: "check-ignore", Detail: "supervisor returned no exit status"}
	}
	status := *result.Process.ExitStatus
	if status != 0 && status != 1 {
		return nil, exitError("check-ignore", result)
	}
	if len(result.StderrTail) != 0 {
		return nil, &GitError{Kind: ErrorProcess, Operation: "check-ignore", ExitStatus: result.Process.ExitStatus, Detail: strings.TrimSpace(string(result.StderrTail))}
	}
	values, err := nulValues(result.Stdout)
	if err != nil {
		return nil, &GitError{Kind: ErrorProcess, Operation: "check-ignore", Detail: "Git returned malformed NUL-delimited paths", Cause: err}
	}
	if status == 1 && len(values) != 0 {
		return nil, &GitError{Kind: ErrorProcess, Operation: "check-ignore", Detail: "Git returned ignored paths with a no-match status"}
	}
	if status == 0 && len(values) == 0 {
		return nil, &GitError{Kind: ErrorProcess, Operation: "check-ignore", Detail: "Git reported a match without returning paths"}
	}
	known := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		known[path] = struct{}{}
	}
	for _, path := range values {
		if _, ok := known[path]; !ok {
			return nil, &GitError{Kind: ErrorProcess, Operation: "check-ignore", Detail: fmt.Sprintf("Git returned an unknown path %q", path)}
		}
		ignored[path] = struct{}{}
	}
	return ignored, nil
}
