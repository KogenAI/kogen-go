package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/process"
	"kogen-go/internal/safefs"
)

const (
	ToolShell  = "shell"
	ToolFinish = "finish"
	ToolOutput = "tool_output"

	finishGuardText      = "finish requires an empty object and must be the only tool call. Continue implementing, then call finish alone with {}."
	completionRequested  = "Completion requested. Kogen will run the gate."
	unknownOutputHandle  = "ERROR: Unknown or unavailable tool-output handle."
	shellUnavailableText = "shell process port is unavailable"
	shellFailedText      = "shell process could not be run"
	privateFileText      = "private tool file could not be accessed"
)

const defaultShellTimeout = 120 * time.Second

// Dispatch validates and executes the shell, finish and tool_output tools,
// then routes the direct file tools through Execute. The shared tool-result
// budget is applied after every successful non-finish tool call.
func (c *ToolContext) Dispatch(ctx context.Context, name string, raw json.RawMessage, callCount int, resultTokens uint64) (string, error) {
	if c == nil || !toolAllowed(c.Role, name) {
		return "", toolError("ERROR (tool_not_allowed): This stage does not allow the requested tool.")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if name == ToolFinish {
		return executeFinish(raw, callCount)
	}
	if resultTokens == 0 {
		resultTokens = DefaultToolResultTokens
	}

	var result string
	var err error
	switch name {
	case ToolShell:
		var command string
		command, err = parseShellArguments(raw)
		if err == nil {
			var output []byte
			output, err = c.runShell(ctx, command)
			result = string(output)
		}
	case ToolOutput:
		var arguments ToolOutputArguments
		arguments, err = ParseToolOutputArguments(raw)
		if err == nil {
			result, err = ReadToolOutput(c.RunRoot, arguments)
		}
	default:
		result, err = c.Execute(ctx, name, raw)
	}
	if err != nil {
		return "", err
	}
	return BoundToolResult(c.RunRoot, []byte(result), resultTokens)
}

func toolAllowed(role ToolRole, name string) bool {
	switch role {
	case RoleBuilderShell:
		return name == ToolShell || name == ToolFinish || name == ToolOutput
	case RoleBuilderDirect:
		return name == ToolShell || name == ToolFinish || name == ToolOutput || contains(AllowedFileTools(role), name)
	case RoleShaper:
		return contains(AllowedFileTools(role), name)
	default:
		return false
	}
}

func parseShellArguments(raw json.RawMessage) (string, error) {
	object, err := decodeToolObject(raw)
	if err != nil {
		return "", toolError(invalidArgumentsText)
	}
	value, ok := object["cmd"]
	if !ok {
		return "", toolError(invalidArgumentsText)
	}
	var command string
	if err := json.Unmarshal(value, &command); err != nil {
		return "", toolError(invalidArgumentsText)
	}
	return command, nil
}

func decodeToolObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, errors.New("tool arguments must be an object")
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return nil, err
	}
	return object, nil
}

func (c *ToolContext) runShell(ctx context.Context, command string) (_ []byte, returnedErr error) {
	if c.Process == nil || c.RunRoot == nil || c.RunDir == "" {
		return nil, toolError(shellUnavailableText)
	}
	if !cleanAbsolutePath(c.RunDir) || !cleanAbsolutePath(c.Workspace) {
		return nil, toolError(shellUnavailableText)
	}
	if err := ensurePrivateToolDirectory(c.RunRoot, "tmp"); err != nil {
		return nil, wrappedToolError(privateFileText, err)
	}
	if err := ensurePrivateToolDirectory(c.RunRoot, "logs"); err != nil {
		return nil, wrappedToolError(privateFileText, err)
	}

	scriptName, scriptAbsPath, err := newPrivateToolPath(c.RunDir, "tmp", "shell", ".sh")
	if err != nil {
		return nil, wrappedToolError(privateFileText, err)
	}
	if len(scriptAbsPath) > process.MaximumArgumentSize {
		return nil, toolError(shellUnavailableText)
	}
	if err := c.RunRoot.Publish(scriptName, []byte(command), 0o600, contract.PublicationCreateOnly); err != nil {
		return nil, wrappedToolError(privateFileText, err)
	}
	scriptPresent := true
	defer func() {
		if !scriptPresent {
			return
		}
		if err := c.RunRoot.Remove(scriptName); err != nil && !errors.Is(err, fs.ErrNotExist) {
			returnedErr = errors.Join(returnedErr, wrappedToolError(privateFileText, err))
		}
	}()

	logName, logPath, err := newPrivateToolPath(c.RunDir, "logs", "shell", ".log")
	if err != nil {
		return nil, wrappedToolError(privateFileText, err)
	}
	logPresent := true
	defer func() {
		if logPresent {
			_ = c.RunRoot.Remove(logName)
		}
	}()
	observed, runErr := c.Process.Run(ctx, contract.ProcessSpec{
		Executable:      "sh",
		Args:            []string{scriptAbsPath},
		Dir:             c.Workspace,
		Env:             append([]string(nil), c.Environment...),
		Timeout:         scaledShellTimeout(),
		OutputLimit:     process.MaximumOutputLimit,
		OutputTailLimit: process.OutputTailBytes,
		LogPath:         logPath,
	})
	if runErr != nil {
		return nil, wrappedToolError(shellFailedText, runErr)
	}
	if err := c.RunRoot.Remove(scriptName); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, wrappedToolError(privateFileText, err)
	}
	scriptPresent = false

	output, logErr := readPrivateProcessLog(c.RunRoot, logName)
	// Process logs are transient and may contain unredacted command output. The
	// bounded, redacted result handle is the only copy retained for retrieval.
	removeErr := c.RunRoot.Remove(logName)
	if removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
		return nil, wrappedToolError(privateFileText, removeErr)
	}
	logPresent = false
	if logErr != nil {
		output = append([]byte("[process log unavailable; captured tail may be incomplete]\n"), observed.OutputTail...)
	}
	if observed.TimedOut {
		output = append([]byte("timed out after 120 seconds\n"), output...)
	} else {
		if len(output) > 0 && output[len(output)-1] != '\n' {
			output = append(output, '\n')
		}
		status := -1
		if observed.ExitStatus != nil {
			status = *observed.ExitStatus
		}
		output = append(output, []byte(fmt.Sprintf("exit %d\n", status))...)
	}
	return output, nil
}

func readPrivateProcessLog(root contract.RootedFS, name string) ([]byte, error) {
	if root == nil {
		return nil, fs.ErrNotExist
	}
	return readPrivateRegularFile(root, name)
}

func ensurePrivateToolDirectory(root contract.RootedFS, name string) error {
	if err := root.MkdirAll(name, 0o700); err != nil {
		return err
	}
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return safefs.ErrUnsafeFile
	}
	return nil
}

func newPrivateToolPath(runDir, directory, prefix, suffix string) (relative, absolute string, err error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", "", err
	}
	leaf := prefix + "-" + hex.EncodeToString(nonce[:]) + suffix
	relative = directory + "/" + leaf
	absolute = filepath.Join(runDir, filepath.FromSlash(relative))
	if len(absolute) > process.MaximumArgumentSize {
		return "", "", errors.New("private tool path exceeds argv limit")
	}
	return relative, absolute, nil
}

func cleanAbsolutePath(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsRune(value, '\x00')
}

func scaledShellTimeout() time.Duration {
	value, err := strconvParseScale(os.Getenv("KOGEN_TIME_SCALE"))
	if err != nil {
		return defaultShellTimeout
	}
	nanos := float64(defaultShellTimeout) * value
	if nanos < 1 {
		return time.Nanosecond
	}
	if nanos >= float64(math.MaxInt64) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(math.Floor(nanos))
}

func strconvParseScale(value string) (float64, error) {
	if value == "" {
		return 0, errors.New("time scale is unset")
	}
	scale, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(scale) || math.IsInf(scale, 0) || scale <= 0 {
		return 0, errors.New("time scale is invalid")
	}
	return scale, nil
}
