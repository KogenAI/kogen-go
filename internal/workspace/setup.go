package workspace

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"kogen-go/internal/contract"
)

// SetupAttempt retains each process actually run during one setup-stage pass.
// Output and log paths remain those of the process supervisor; a retry is
// never silently erased.
type SetupAttempt struct {
	Results []contract.ProcessResult
}

// SetupOutcome retains both supervised attempts, including partial command
// sequences when a process fails before the stage completes.
type SetupOutcome struct {
	Attempts []SetupAttempt
}

// SetupFailedError reports the required one retry after a setup command exits
// unsuccessfully, times out, or is unavailable.
type SetupFailedError struct {
	Outcome SetupOutcome
}

func (e *SetupFailedError) Error() string {
	if e == nil {
		return ErrSetupFailed.Error()
	}
	return fmt.Sprintf("%s after %d attempts", ErrSetupFailed, len(e.Outcome.Attempts))
}

func (e *SetupFailedError) Is(target error) bool { return target == ErrSetupFailed }

// ToolMissingError marks an unavailable acceptance runner on the immutable
// base. A caller must stop the Build with environment/tool_missing.
type ToolMissingError struct {
	Result contract.ProcessResult
}

func (e *ToolMissingError) Error() string        { return ErrToolMissing.Error() }
func (e *ToolMissingError) Is(target error) bool { return target == ErrToolMissing }

// RunSetup supervises setup and retries one unsuccessful process result once.
// logPath is a private absolute base path; distinct .attempt-N logs retain both
// outcomes. Invocation/supervisor errors and context cancellation are returned
// immediately and do not trigger another setup process.
func RunSetup(ctx context.Context, runner contract.ProcessRunner, specs []contract.ProcessSpec) (SetupOutcome, error) {
	if ctx == nil || runner == nil {
		return SetupOutcome{}, errors.New("workspace: setup requires context and process runner")
	}
	for _, spec := range specs {
		if spec.Executable == "" || spec.Dir == "" || spec.LogPath == "" || !filepath.IsAbs(spec.LogPath) || filepath.Clean(spec.LogPath) != spec.LogPath {
			return SetupOutcome{}, errors.New("workspace: setup requires executables, directories, and clean absolute log paths")
		}
	}
	outcome := SetupOutcome{Attempts: make([]SetupAttempt, 0, 2)}
	if len(specs) == 0 {
		return outcome, nil
	}
	for attempt := 1; attempt <= 2; attempt++ {
		currentAttempt := SetupAttempt{Results: make([]contract.ProcessResult, 0, len(specs))}
		failed := false
		for index, spec := range specs {
			if err := ctx.Err(); err != nil {
				outcome.Attempts = append(outcome.Attempts, currentAttempt)
				return outcome, err
			}
			current := spec
			current.Args = append([]string(nil), spec.Args...)
			current.Env = append([]string(nil), spec.Env...)
			current.Stdin = append([]byte(nil), spec.Stdin...)
			current.LogPath = setupAttemptLogPath(spec.LogPath, attempt, index, len(specs))
			result, err := runner.Run(ctx, current)
			currentAttempt.Results = append(currentAttempt.Results, cloneProcessResult(result))
			if err != nil {
				outcome.Attempts = append(outcome.Attempts, currentAttempt)
				return outcome, err
			}
			if !processSucceeded(result) {
				failed = true
				break
			}
		}
		outcome.Attempts = append(outcome.Attempts, currentAttempt)
		if !failed {
			return outcome, nil
		}
	}
	return outcome, &SetupFailedError{Outcome: outcome}
}

func setupAttemptLogPath(base string, attempt, command, commandCount int) string {
	if commandCount == 1 {
		return fmt.Sprintf("%s.attempt-%d", base, attempt)
	}
	return fmt.Sprintf("%s.attempt-%d-step-%d", base, attempt, command+1)
}

func cloneProcessResult(result contract.ProcessResult) contract.ProcessResult {
	result.OutputTail = append([]byte(nil), result.OutputTail...)
	if result.ExitStatus != nil {
		status := *result.ExitStatus
		result.ExitStatus = &status
	}
	return result
}

// RunBaseAcceptance executes the approved acceptance runner once against the
// first rung's base workspace. A missing runner is an environment/tool_missing
// stop; ordinary red or timed-out outcomes are returned for gate policy to
// record. It does not retry a test suite or grant candidate acceptance.
func RunBaseAcceptance(ctx context.Context, rung string, runner contract.ProcessRunner, spec contract.ProcessSpec) (contract.ProcessResult, error) {
	if ctx == nil || runner == nil {
		return contract.ProcessResult{}, errors.New("workspace: base acceptance requires context and process runner")
	}
	if rung != "R1" {
		return contract.ProcessResult{}, errors.New("workspace: base acceptance runs only on the first rung")
	}
	if spec.Executable == "" || spec.Dir == "" || spec.LogPath == "" || !filepath.IsAbs(spec.LogPath) || filepath.Clean(spec.LogPath) != spec.LogPath {
		return contract.ProcessResult{}, errors.New("workspace: base acceptance requires an executable, directory, and clean absolute log path")
	}
	result, err := runner.Run(ctx, spec)
	if err != nil {
		return result, err
	}
	if !processAvailable(result) {
		return result, &ToolMissingError{Result: result}
	}
	return result, nil
}

func processSucceeded(result contract.ProcessResult) bool {
	return processAvailable(result) && !result.TimedOut && result.ExitStatus != nil && *result.ExitStatus == 0
}

func processAvailable(result contract.ProcessResult) bool {
	if result.Unavailable || result.ExitStatus == nil {
		return false
	}
	return *result.ExitStatus != 126 && *result.ExitStatus != 127
}
