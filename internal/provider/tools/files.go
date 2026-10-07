package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"kogen-go/internal/contract"
	"kogen-go/internal/safefs"
)

const (
	ToolRead   = "read"
	ToolSearch = "search"
	ToolWrite  = "write"
	ToolEdit   = "edit"

	// MaxShapeWriteBytes is the byte limit for either file written by Shape.
	MaxShapeWriteBytes = 200_000
)

// ToolRole controls the file tools callable in a provider stage.
type ToolRole string

const (
	RoleBuilderDirect ToolRole = "builder_direct"
	RoleShaper        ToolRole = "shaper"
	RoleBuilderShell  ToolRole = "builder_shell"
	RolePlanner       ToolRole = "planner"
	RoleAuditor       ToolRole = "auditor"
)

const (
	pathEscapeText      = "Path escapes the worktree."
	missingFileText     = "ERROR: File does not exist."
	binaryFileText      = "ERROR: File is binary or is not UTF-8 text."
	shapeWriteLimitText = "ERROR: Write content exceeds 200000 bytes."
)

// ToolContext binds direct tool effects to the workspace and run roots. The
// roots are descriptor-backed implementations such as internal/safefs.Root.
// Workspace and RunDir are the absolute paths used only by the supervised
// search process.
type ToolContext struct {
	Role             ToolRole
	Workspace        string
	WorkspaceRoot    contract.RootedFS
	RunDir           string
	RunRoot          contract.RootedFS
	Process          contract.ProcessRunner
	Environment      []string
	ShaperWritePaths [2]string
}

// AllowedFileTools returns the sorted file-tool allowlist for role.
func AllowedFileTools(role ToolRole) []string {
	switch role {
	case RoleBuilderDirect:
		return []string{ToolEdit, ToolRead, ToolSearch, ToolWrite}
	case RoleShaper:
		return []string{ToolRead, ToolSearch, ToolWrite}
	default:
		return []string{}
	}
}

// Execute validates an allowed direct tool call, performs the filesystem or
// search effect, and returns the unshortened result. Callers apply the shared
// tool-result budget after this method; write bytes are never shortened.
func (c *ToolContext) Execute(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	if c == nil {
		return "", toolError("provider tool workspace is unavailable")
	}
	if !contains(AllowedFileTools(c.Role), name) {
		return "", toolError("ERROR (tool_not_allowed): This stage does not allow the requested tool.")
	}
	args, err := ParseFileArguments(name, raw)
	if err != nil {
		return "", err
	}
	if c.WorkspaceRoot == nil {
		return "", toolError("provider tool workspace is unavailable")
	}
	switch name {
	case ToolRead:
		return c.read(args)
	case ToolWrite:
		return c.write(args)
	case ToolEdit:
		return c.edit(args)
	case ToolSearch:
		return c.search(ctx, args)
	default:
		return "", toolError("ERROR (tool_not_allowed): This stage does not allow the requested tool.")
	}
}

func (c *ToolContext) read(args FileArguments) (string, error) {
	relative, err := workspaceRelative(c.Workspace, args.Path, false)
	if err != nil {
		return "", pathError(err)
	}
	contents, err := c.WorkspaceRoot.ReadFile(relative)
	if err != nil {
		return "", fileError(err)
	}
	if !utf8.Valid(contents) || bytes.IndexByte(contents, 0) >= 0 {
		return "", toolError(binaryFileText)
	}
	lines := textLines(string(contents))
	start := min(args.Offset-1, len(lines))
	end := min(start+args.Limit, len(lines))
	var output strings.Builder
	output.WriteString(relative)
	output.WriteString(":\n")
	for index := start; index < end; index++ {
		fmt.Fprintf(&output, "%d: %s\n", index+1, lines[index])
	}
	if end < len(lines) {
		fmt.Fprintf(&output, "\n[continue with offset=%d]", end+1)
	}
	return output.String(), nil
}

func (c *ToolContext) write(args FileArguments) (string, error) {
	if c.Role == RoleShaper && len(args.Content) > MaxShapeWriteBytes {
		return "", toolError(shapeWriteLimitText)
	}
	relative, err := workspaceRelative(c.Workspace, args.Path, false)
	if err != nil {
		return "", pathError(err)
	}
	if c.Role == RoleShaper {
		if filepath.IsAbs(filepath.FromSlash(args.Path)) || !c.shaperMayWrite(args.Path, relative) {
			return "", c.shaperScopeError()
		}
	}
	mode, err := c.publicationMode(relative)
	if err != nil {
		return "", fileError(err)
	}
	if err := c.WorkspaceRoot.Publish(relative, []byte(args.Content), mode, safefs.PublicationReplace); err != nil {
		return "", fileError(err)
	}
	return "Wrote " + relative + ".", nil
}

func (c *ToolContext) edit(args FileArguments) (string, error) {
	relative, err := workspaceRelative(c.Workspace, args.Path, false)
	if err != nil {
		return "", pathError(err)
	}
	contents, err := c.WorkspaceRoot.ReadFile(relative)
	if err != nil {
		return "", fileError(err)
	}
	if !utf8.Valid(contents) || bytes.IndexByte(contents, 0) >= 0 {
		return "", toolError(binaryFileText)
	}
	text := string(contents)
	if args.Old == "" || !strings.Contains(text, args.Old) {
		return "", toolError(invalidArgumentsText)
	}
	updated := strings.Replace(text, args.Old, args.New, 1)
	mode, err := c.publicationMode(relative)
	if err != nil {
		return "", fileError(err)
	}
	if err := c.WorkspaceRoot.Publish(relative, []byte(updated), mode, safefs.PublicationReplace); err != nil {
		return "", fileError(err)
	}
	return "Edited " + relative + ".", nil
}

func (c *ToolContext) publicationMode(relative string) (fs.FileMode, error) {
	info, err := c.WorkspaceRoot.Lstat(relative)
	if errors.Is(err, fs.ErrNotExist) {
		return 0o644, nil
	}
	if err != nil {
		return 0, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		if err := c.verifySymlinkTarget(relative); err != nil {
			return 0, err
		}
		return 0o644, nil
	}
	if !info.Mode().IsRegular() {
		return 0, safefs.ErrUnsafeFile
	}
	return info.Mode().Perm(), nil
}

func (c *ToolContext) verifySymlinkTarget(relative string) error {
	if _, err := c.WorkspaceRoot.ReadDir(relative); err == nil {
		return safefs.ErrUnsafeFile
	} else if errors.Is(err, safefs.ErrUnsafePath) {
		return err
	}
	file, err := c.WorkspaceRoot.OpenRead(relative)
	if err != nil {
		return err
	}
	return file.Close()
}

func (c *ToolContext) shaperMayWrite(requested, relative string) bool {
	for _, allowed := range c.ShaperWritePaths {
		if allowed == "" || filepath.IsAbs(filepath.FromSlash(allowed)) {
			continue
		}
		allowedPath, err := workspaceRelative(c.Workspace, allowed, false)
		if err == nil && allowedPath == relative && requested[0] != '/' {
			return true
		}
	}
	return false
}

func (c *ToolContext) shaperScopeError() error {
	return toolError(fmt.Sprintf("ERROR: Write target is outside the shaper's two-file scope. Allowed paths: %s, %s.", c.ShaperWritePaths[0], c.ShaperWritePaths[1]))
}

func workspaceRelative(workspace, requested string, allowRoot bool) (string, error) {
	if strings.ContainsRune(requested, 0) {
		return "", safefs.ErrUnsafePath
	}
	if workspace == "" || !filepath.IsAbs(workspace) || filepath.Clean(workspace) != workspace {
		return "", fmt.Errorf("workspace path is not a clean absolute path")
	}
	if requested == "" {
		requested = "."
	}
	var candidate string
	if filepath.IsAbs(filepath.FromSlash(requested)) {
		var err error
		candidate, err = filepath.Rel(workspace, filepath.Clean(filepath.FromSlash(requested)))
		if err != nil {
			return "", safefs.ErrUnsafePath
		}
	} else {
		candidate = filepath.FromSlash(requested)
	}
	clean := filepath.Clean(candidate)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", safefs.ErrUnsafePath
	}
	if clean == "." {
		if allowRoot {
			return ".", nil
		}
		return "", safefs.ErrUnsafePath
	}
	if filepath.IsAbs(clean) {
		return "", safefs.ErrUnsafePath
	}
	return filepath.ToSlash(clean), nil
}

func pathError(err error) error {
	if errors.Is(err, safefs.ErrUnsafePath) {
		return wrappedToolError(pathEscapeText, err)
	}
	return fileError(err)
}

func fileError(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return wrappedToolError(missingFileText, err)
	case errors.Is(err, safefs.ErrUnsafePath):
		return wrappedToolError(pathEscapeText, err)
	case errors.Is(err, safefs.ErrUnsafeFile):
		return wrappedToolError(binaryFileText, err)
	default:
		return wrappedToolError(err.Error(), err)
	}
}

func textLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if strings.HasSuffix(text, "\n") {
		lines = lines[:len(lines)-1]
	}
	for index := range lines {
		lines[index] = strings.TrimSuffix(lines[index], "\r")
	}
	return lines
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
