package validate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"kogen-go/internal/contract"
)

// stageAndRun writes the generated acceptance source at the adapter-selected
// candidate path using create-only publication, runs checks, then removes the
// staged leaf. Empty candidate-parent directories may remain; they do not add
// entries to the Git tree.
func (v *Validator) stageAndRun(ctx context.Context, source []byte, mode fs.FileMode) (failure *Failure, resultErr error) {
	root, err := v.openRoot()
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := closeRoot(root); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("shape validation: close staging root: %w", closeErr))
		}
	}()
	parent := path.Dir(v.paths.Candidate)
	if err := ensureDirectories(root, parent); err != nil {
		return nil, fmt.Errorf("shape validation: prepare candidate directory: %w", err)
	}
	staged := false
	defer func() {
		if staged {
			if removeErr := root.Remove(v.paths.Candidate); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
				resultErr = errors.Join(resultErr, fmt.Errorf("shape validation: restore staged acceptance path: %w", removeErr))
				failure = nil
			}
		}
	}()
	if _, err := root.Lstat(v.paths.Candidate); err == nil {
		return &Failure{Reason: "acceptance_check_path_conflict", Detail: fmt.Sprintf("checkout-relative staged path %s is already occupied", v.paths.Candidate)}, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("shape validation: inspect staged acceptance path: %w", err)
	}
	if mode == 0 {
		mode = 0o644
	}
	if err := root.Publish(v.paths.Candidate, source, mode.Perm(), contract.PublicationCreateOnly); err != nil {
		if _, statErr := root.Lstat(v.paths.Candidate); statErr == nil {
			return &Failure{Reason: "acceptance_check_path_conflict", Detail: fmt.Sprintf("checkout-relative staged path %s is already occupied", v.paths.Candidate)}, nil
		}
		return nil, fmt.Errorf("shape validation: stage acceptance source: %w", err)
	}
	staged = true
	before, err := v.trees.Snapshot(ctx, v.checkout)
	if err != nil {
		return nil, fmt.Errorf("shape validation: snapshot before acceptance checks: %w", err)
	}
	checks, checkErr := v.checks.Run(ctx, v.checkout, v.paths.Candidate)
	after, snapshotErr := v.trees.Snapshot(ctx, v.checkout)
	if snapshotErr != nil {
		return nil, errors.Join(checkErr, fmt.Errorf("shape validation: snapshot after acceptance checks: %w", snapshotErr))
	}
	stagedBytes, stagedMode, stagedErr := readRegular(root, v.paths.Candidate)
	if before != after || stagedErr != nil || !bytes.Equal(stagedBytes, source) || stagedMode.Perm() != mode.Perm() {
		return &Failure{Reason: "tree_mutated", Detail: "acceptance checks changed the checkout tree"}, nil
	}
	if checkErr != nil {
		return &Failure{Reason: "acceptance_check_failed", Detail: checkErr.Error()}, nil
	}
	for _, check := range checks {
		if check.Status == contract.CheckMutating || (check.TreeBefore != "" && check.TreeAfter != "" && check.TreeBefore != check.TreeAfter) {
			return &Failure{Reason: "tree_mutated", Detail: "acceptance checks changed the checkout tree"}, nil
		}
		if check.Status == contract.CheckUnavailable || check.Unavailable || check.ExitStatus == nil || exitUnavailable(check.ExitStatus) {
			return &Failure{Reason: "acceptance_check_unavailable", Detail: fmt.Sprintf("acceptance check %s is unavailable", check.Name)}, nil
		}
		if check.Status != contract.CheckGreen || check.TimedOut || *check.ExitStatus != 0 {
			exit := -1
			if check.ExitStatus != nil {
				exit = *check.ExitStatus
			}
			return &Failure{Reason: "acceptance_check_failed", Detail: fmt.Sprintf("acceptance check %s failed with exit %d", check.Name, exit)}, nil
		}
	}
	return nil, nil
}

func ensureDirectories(root contract.RootedFS, parent string) error {
	if parent == "." {
		return nil
	}
	current := ""
	for _, segment := range strings.Split(parent, "/") {
		if current == "" {
			current = segment
		} else {
			current += "/" + segment
		}
		info, err := root.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			if err := root.MkdirAll(current, 0o755); err != nil {
				return err
			}
			info, err = root.Lstat(current)
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("parent %q is not a real directory", current)
		}
	}
	return nil
}

func exitUnavailable(status *int) bool {
	return status != nil && (*status == 126 || *status == 127)
}
