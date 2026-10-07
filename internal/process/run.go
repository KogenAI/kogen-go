package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"kogen-go/internal/contract"
)

const (
	DefaultTimeout      = 120 * time.Second
	DefaultOutputLimit  = 16 << 20
	MaximumOutputLimit  = 64 << 20
	OutputTailBytes     = 16 << 10
	MaximumArgumentSize = 4 << 10
)

// Supervisor runs children in a fresh process group and keeps their output
// bounded. The caller supplies a private absolute LogPath in the run dir.
type Supervisor struct{}

var _ contract.ProcessRunner = Supervisor{}

// Run starts one child with exactly spec.Env, drains stdout and stderr without
// allowing output volume to control its deadline, then terminates any remaining
// process-group members. On context cancellation it returns the observed child
// result together with ctx.Err() after the group has been stopped and reaped.
func (Supervisor) Run(ctx context.Context, spec contract.ProcessSpec) (contract.ProcessResult, error) {
	started := time.Now()
	if ctx == nil {
		return contract.ProcessResult{}, errors.New("process: nil context")
	}
	spec.Args = append([]string(nil), spec.Args...)
	spec.Env = append([]string(nil), spec.Env...)
	spec.Stdin = append([]byte(nil), spec.Stdin...)
	if err := validate(spec); err != nil {
		return contract.ProcessResult{}, err
	}
	timeout := spec.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	outputLimit := spec.OutputLimit
	if outputLimit == 0 {
		outputLimit = DefaultOutputLimit
	}
	tailLimit := spec.OutputTailLimit
	if tailLimit == 0 {
		tailLimit = OutputTailBytes
	}
	log, err := openProcessLog(spec.LogPath)
	if err != nil {
		return contract.ProcessResult{}, err
	}
	defer log.close()
	capture := newOutputCapture(log.stream, int(outputLimit), tailLimit)

	if err := ctx.Err(); err != nil {
		return contract.ProcessResult{}, err
	}
	workingDirectory, err := os.Stat(spec.Dir)
	if err != nil {
		return contract.ProcessResult{}, fmt.Errorf("process: inspect working directory: %w", err)
	}
	if !workingDirectory.IsDir() {
		return contract.ProcessResult{}, errors.New("process: working directory is not a directory")
	}
	program, unavailableStatus, lookupErr := resolveExecutable(spec.Executable, spec.Env, spec.Dir)
	if lookupErr != nil {
		if unavailableStatus == 0 {
			return contract.ProcessResult{}, lookupErr
		}
		_, _ = fmt.Fprintf(capture, "%s\n", lookupErr)
		return publishResult(log, capture, unavailableStatus, false, true, started)
	}

	cmd := exec.Command(program, spec.Args...)
	cmd.Args = append([]string{spec.Executable}, spec.Args...)
	cmd.Dir = spec.Dir
	// An empty but non-nil slice means no host environment is inherited.
	cmd.Env = append(make([]string, 0, len(spec.Env)), spec.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	outputRead, outputWrite, err := os.Pipe()
	if err != nil {
		return contract.ProcessResult{}, fmt.Errorf("process: open output pipe: %w", err)
	}
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		_ = outputRead.Close()
		_ = outputWrite.Close()
		return contract.ProcessResult{}, fmt.Errorf("process: open stdin pipe: %w", err)
	}
	cmd.Stdin = stdinRead
	cmd.Stdout = outputWrite
	cmd.Stderr = outputWrite

	processStarted := time.Now()
	if err := cmd.Start(); err != nil {
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		_ = outputRead.Close()
		_ = outputWrite.Close()
		status, unavailable := unavailableForStartError(err)
		if unavailable {
			_, _ = fmt.Fprintf(capture, "%s\n", err)
			return publishResult(log, capture, status, false, true, started)
		}
		return contract.ProcessResult{}, fmt.Errorf("process: start child: %w", err)
	}
	_ = stdinRead.Close()
	_ = outputWrite.Close()
	pgid := cmd.Process.Pid

	readerDone := make(chan error, 1)
	go func() {
		_, readErr := io.Copy(capture, outputRead)
		readerDone <- readErr
	}()
	inputDone := make(chan error, 1)
	go func(input []byte) {
		_, writeErr := stdinWrite.Write(input)
		closeErr := stdinWrite.Close()
		if writeErr != nil {
			inputDone <- writeErr
			return
		}
		inputDone <- closeErr
	}(append([]byte(nil), spec.Stdin...))

	type waitResult struct {
		state *os.ProcessState
		err   error
	}
	waitDone := make(chan waitResult, 1)
	go func() {
		state, waitErr := cmd.Process.Wait()
		waitDone <- waitResult{state: state, err: waitErr}
	}()

	deadline := processStarted.Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	delay := time.Until(deadline)
	if delay < 0 {
		delay = 0
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()

	var waited waitResult
	timedOut := false
	var cancelErr error
	select {
	case waited = <-waitDone:
		// The direct child completed. Still stop background descendants in its
		// group before allowing either output pipe to finish.
		if err := stopGroup(pgid); err != nil {
			_ = cmd.Process.Kill()
		}
	case <-timer.C:
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			timedOut = true
		} else if ctx.Err() != nil {
			cancelErr = ctx.Err()
		} else {
			timedOut = true
		}
		if err := stopGroup(pgid); err != nil {
			_ = cmd.Process.Kill()
		}
		waited = <-waitDone
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			timedOut = true
		} else {
			cancelErr = ctx.Err()
		}
		if err := stopGroup(pgid); err != nil {
			_ = cmd.Process.Kill()
		}
		waited = <-waitDone
	}
	_ = stdinWrite.Close()
	if inputErr := <-inputDone; inputErr != nil && !errors.Is(inputErr, syscall.EPIPE) {
		// A child that exited before consuming all stdin is a valid observation;
		// other input errors mean the requested invocation was not delivered.
		if waited.err == nil && cancelErr == nil && !timedOut {
			_ = drainOutput(outputRead, readerDone)
			return contract.ProcessResult{}, fmt.Errorf("process: write child stdin: %w", inputErr)
		}
	}
	readErr := drainOutput(outputRead, readerDone)
	if errors.Is(readErr, os.ErrClosed) {
		readErr = nil
	}
	if waited.err != nil {
		return contract.ProcessResult{}, fmt.Errorf("process: wait for child: %w", waited.err)
	}
	if waited.state == nil {
		return contract.ProcessResult{}, errors.New("process: child wait returned no status")
	}
	result, err := publishResult(log, capture, exitStatus(waited.state), timedOut, false, started)
	if err != nil {
		return result, err
	}
	if readErr != nil {
		return result, fmt.Errorf("process: read child output: %w", readErr)
	}
	if cancelErr != nil {
		return result, cancelErr
	}
	return result, nil
}

func drainOutput(pipe *os.File, done <-chan error) error {
	timer := time.NewTimer(outputDrainGrace)
	defer timer.Stop()
	select {
	case readErr := <-done:
		_ = pipe.Close()
		return readErr
	case <-timer.C:
		_ = pipe.Close()
		return <-done
	}
}

func validate(spec contract.ProcessSpec) error {
	if spec.Executable == "" {
		return errors.New("process: executable must not be empty")
	}
	if strings.ContainsRune(spec.Executable, '\x00') {
		return errors.New("process: executable contains NUL")
	}
	if spec.Dir == "" {
		return errors.New("process: working directory must be explicit")
	}
	if strings.ContainsRune(spec.Dir, '\x00') {
		return errors.New("process: working directory contains NUL")
	}
	if spec.Timeout < 0 {
		return errors.New("process: timeout must not be negative")
	}
	if spec.OutputLimit < 0 || spec.OutputLimit > MaximumOutputLimit {
		return fmt.Errorf("process: output limit must be between 0 and %d bytes", MaximumOutputLimit)
	}
	if spec.OutputTailLimit < 0 || spec.OutputTailLimit > OutputTailBytes {
		return fmt.Errorf("process: output tail limit must be between 0 and %d bytes", OutputTailBytes)
	}
	if spec.LogPath == "" {
		return errors.New("process: private log path must be explicit")
	}
	for index, arg := range spec.Args {
		if strings.ContainsRune(arg, '\x00') {
			return fmt.Errorf("process: argv element %d contains NUL", index+1)
		}
		if len(arg) > MaximumArgumentSize {
			return fmt.Errorf("process: argv element %d is %d bytes; maximum is %d", index+1, len(arg), MaximumArgumentSize)
		}
	}
	if len(spec.Executable) > MaximumArgumentSize {
		return fmt.Errorf("process: executable is %d bytes; maximum is %d", len(spec.Executable), MaximumArgumentSize)
	}
	envKeys := make(map[string]struct{}, len(spec.Env))
	for _, entry := range spec.Env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" || strings.ContainsRune(entry, '\x00') || strings.ContainsRune(key, '=') {
			return errors.New("process: environment entry must be a NUL-free KEY=value pair")
		}
		if _, duplicate := envKeys[key]; duplicate {
			return fmt.Errorf("process: duplicate environment key %q", key)
		}
		envKeys[key] = struct{}{}
	}
	return nil
}

func resolveExecutable(name string, env []string, dir string) (string, int, error) {
	workingDir, err := filepath.Abs(dir)
	if err != nil {
		return "", 0, fmt.Errorf("process: resolve working directory: %w", err)
	}
	if strings.ContainsRune(name, filepath.Separator) {
		candidate := name
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(workingDir, candidate)
		}
		return executableCandidate(candidate, name)
	}

	var childPath string
	hasPath := false
	for _, entry := range env {
		if key, value, ok := strings.Cut(entry, "="); ok && key == "PATH" {
			childPath = value
			hasPath = true
		}
	}
	if !hasPath {
		return "", 127, fmt.Errorf("exec: %q: executable not found in child PATH", name)
	}
	components := filepath.SplitList(childPath)
	if childPath == "" {
		components = []string{""}
	}
	var denied error
	for _, component := range components {
		if component == "" {
			component = "."
		}
		if !filepath.IsAbs(component) {
			component = filepath.Join(workingDir, component)
		}
		candidate := filepath.Join(component, name)
		if path, status, candidateErr := executableCandidate(candidate, name); candidateErr == nil {
			return path, 0, nil
		} else if status == 126 && denied == nil {
			denied = candidateErr
		}
	}
	if denied != nil {
		return "", 126, denied
	}
	return "", 127, fmt.Errorf("exec: %q: executable not found in child PATH", name)
}

func executableCandidate(candidate, original string) (string, int, error) {
	info, err := os.Stat(candidate)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return "", 126, fmt.Errorf("exec: %q: permission denied", original)
		}
		return "", 127, err
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return "", 126, fmt.Errorf("exec: %q: not an executable regular file", original)
	}
	if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return "", 126, fmt.Errorf("exec: %q: permission denied", original)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", 0, err
	}
	return abs, 0, nil
}

func unavailableForStartError(err error) (int, bool) {
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Op != "fork/exec" {
		return 0, false
	}
	if errors.Is(pathErr.Err, os.ErrNotExist) {
		return 127, true
	}
	if errors.Is(pathErr.Err, os.ErrPermission) || errors.Is(pathErr.Err, syscall.ENOEXEC) {
		return 126, true
	}
	return 0, false
}

func exitStatus(state *os.ProcessState) int {
	code := state.ExitCode()
	if code < 0 {
		if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			code = signaledStatus(status.Signal())
		}
	}
	return code
}
