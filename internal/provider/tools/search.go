package tools

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"unicode/utf8"

	"kogen-go/internal/contract"
	"kogen-go/internal/process"
	"kogen-go/internal/safefs"
)

const searchProcessError = "search process port is unavailable"

func (c *ToolContext) search(ctx context.Context, args FileArguments) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c.Process == nil || c.RunRoot == nil || c.RunDir == "" {
		return "", toolError(searchProcessError)
	}
	if !filepath.IsAbs(c.Workspace) || filepath.Clean(c.Workspace) != c.Workspace ||
		!filepath.IsAbs(c.RunDir) || filepath.Clean(c.RunDir) != c.RunDir {
		return "", toolError(searchProcessError)
	}
	relative, err := workspaceRelative(c.Workspace, args.Path, true)
	if err != nil {
		return "", pathError(err)
	}
	if err := c.validateSearchPath(relative); err != nil {
		return "", fileError(err)
	}
	if err := c.prepareSearchLogs(); err != nil {
		return "", wrappedToolError(searchProcessError, err)
	}

	result, fallback, err := c.runSearchCommand(ctx, "rg", ripgrepArgs(args.Pattern, relative))
	if err == nil && !fallback {
		return result, nil
	}
	if err != nil && !fallback {
		return "", wrappedToolError(searchProcessError, err)
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	result, unavailable, err := c.runSearchCommand(ctx, "grep", grepArgs(args.Pattern, relative))
	if err != nil {
		return "", wrappedToolError(searchProcessError, err)
	}
	if unavailable {
		return "", toolError(searchProcessError)
	}
	return result, nil
}

func (c *ToolContext) validateSearchPath(relative string) error {
	if relative == "." {
		_, err := c.WorkspaceRoot.ReadDir(".")
		return err
	}
	info, err := c.WorkspaceRoot.Lstat(relative)
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		if _, err := c.WorkspaceRoot.ReadDir(relative); err == nil {
			return nil
		} else if errors.Is(err, safefs.ErrUnsafePath) {
			return err
		}
		file, err := c.WorkspaceRoot.OpenRead(relative)
		if err != nil {
			return err
		}
		return file.Close()
	}
	if info.IsDir() {
		_, err := c.WorkspaceRoot.ReadDir(relative)
		return err
	}
	file, err := c.WorkspaceRoot.OpenRead(relative)
	if err != nil {
		return err
	}
	return file.Close()
}

func (c *ToolContext) prepareSearchLogs() error {
	if err := c.RunRoot.MkdirAll("logs", 0o700); err != nil {
		return err
	}
	info, err := c.RunRoot.Lstat("logs")
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("search log directory is not private")
	}
	return nil
}

// runSearchCommand returns fallback=true only when ripgrep could not be
// launched, or its supervisor invocation failed. A successful no-match exit is
// still a completed search and never causes a second command to run.
func (c *ToolContext) runSearchCommand(ctx context.Context, executable string, args []string) (string, bool, error) {
	leaf, err := randomSearchLogName()
	if err != nil {
		return "", false, err
	}
	relativeLog := "logs/" + leaf
	logPath := filepath.Join(c.RunDir, filepath.FromSlash(relativeLog))
	spec := contract.ProcessSpec{
		Executable:      executable,
		Args:            args,
		Dir:             c.Workspace,
		Env:             append([]string(nil), c.Environment...),
		Timeout:         process.DefaultTimeout,
		OutputLimit:     process.MaximumOutputLimit,
		OutputTailLimit: process.OutputTailBytes,
		LogPath:         logPath,
	}
	observed, err := c.Process.Run(ctx, spec)
	if err != nil || observed.Unavailable {
		_ = c.RunRoot.Remove(relativeLog)
		return "", true, err
	}
	contents, err := c.RunRoot.ReadFile(relativeLog)
	if err != nil {
		_ = c.RunRoot.Remove(relativeLog)
		return "", false, err
	}
	var result string
	if !utf8.Valid(contents) {
		result = "[non-UTF-8 output, base64 encoded]\n" + base64.StdEncoding.EncodeToString(contents) + "\n"
	} else {
		result = string(contents)
	}
	if result == "" {
		result = "No matches."
	}
	if err := c.RunRoot.Remove(relativeLog); err != nil {
		return "", false, err
	}
	return result, false, nil
}

func ripgrepArgs(pattern, path string) []string {
	return []string{"--line-number", "--color", "never", "--", pattern, path}
}

func grepArgs(pattern, path string) []string {
	return []string{"-R", "-n", "--", pattern, path}
}

func randomSearchLogName() (string, error) {
	var nonce [16]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return "", fmt.Errorf("create search log identity: %w", err)
	}
	return "search-" + hex.EncodeToString(nonce[:]) + ".log", nil
}
