package process

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	internalProcessMode = "KOGEN_INTERNAL_PROCESS_MODE"
	guardianMode        = "guardian"
	anchorMode          = "anchor"
	guardianReadyFD     = 5
	guardianEventsFD    = 4
	guardianControlFD   = 3
	guardianInputFD     = 6
	guardianOutputFD    = 7
	guardianReadyByte   = 'R'
	guardianErrorByte   = 'E'
	guardianWaitLimit   = 1500 * time.Millisecond
)

type guardianRequest struct {
	Operation  string   `json:"operation"`
	Executable string   `json:"executable,omitempty"`
	Argv0      string   `json:"argv0,omitempty"`
	Args       []string `json:"args,omitempty"`
	Dir        string   `json:"dir,omitempty"`
	Env        []string `json:"env,omitempty"`
}

type guardianEvent struct {
	Type        string `json:"type"`
	ExitStatus  int    `json:"exit_status,omitempty"`
	Unavailable bool   `json:"unavailable,omitempty"`
	Error       string `json:"error,omitempty"`
}

type processIdentity struct {
	pid   int
	start uint64
	pgid  int
}

type guardianWaitResult struct {
	err error
}

type guardianSession struct {
	control *os.File
	events  <-chan guardianEvent
	done    <-chan guardianWaitResult
}

func startGuardian(stdin, stdout *os.File) (*guardianSession, error) {
	controlRead, controlWrite, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create control pipe: %w", err)
	}
	eventsRead, eventsWrite, err := os.Pipe()
	if err != nil {
		_ = controlRead.Close()
		_ = controlWrite.Close()
		return nil, fmt.Errorf("create event pipe: %w", err)
	}
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		_ = controlRead.Close()
		_ = controlWrite.Close()
		_ = eventsRead.Close()
		_ = eventsWrite.Close()
		return nil, fmt.Errorf("create launch handshake pipe: %w", err)
	}

	executable, err := os.Executable()
	if err != nil {
		closeFiles(controlRead, controlWrite, eventsRead, eventsWrite, readyRead, readyWrite)
		return nil, fmt.Errorf("resolve private guardian executable: %w", err)
	}
	command := exec.Command(executable)
	command.Args = privateProcessArgs(executable)
	command.Env = []string{internalProcessMode + "=" + guardianMode}
	command.ExtraFiles = []*os.File{controlRead, eventsWrite, readyWrite, stdin, stdout}
	command.SysProcAttr = newProcessGroupAttr(0)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		closeFiles(controlRead, controlWrite, eventsRead, eventsWrite, readyRead, readyWrite)
		return nil, fmt.Errorf("execute private guardian: %w", err)
	}
	_ = controlRead.Close()
	_ = eventsWrite.Close()
	_ = readyWrite.Close()

	ready, err := readGuardianHandshake(readyRead)
	_ = readyRead.Close()
	if err != nil {
		abortGuardianStart(command, controlWrite)
		_ = eventsRead.Close()
		return nil, err
	}
	if ready != guardianReadyByte {
		abortGuardianStart(command, controlWrite)
		_ = eventsRead.Close()
		return nil, errors.New("private guardian rejected its launch handshake")
	}

	done := make(chan guardianWaitResult, 1)
	go func() { done <- guardianWaitResult{err: command.Wait()} }()

	eventStream := make(chan guardianEvent, 8)
	go func() {
		defer close(eventStream)
		decoder := json.NewDecoder(eventsRead)
		for {
			var event guardianEvent
			if err := decoder.Decode(&event); err != nil {
				if !errors.Is(err, io.EOF) {
					eventStream <- guardianEvent{Type: "protocol_error", Error: err.Error()}
				}
				return
			}
			eventStream <- event
		}
	}()
	return &guardianSession{
		control: controlWrite,
		events:  eventStream,
		done:    done,
	}, nil
}

func (g *guardianSession) send(request guardianRequest) error {
	if g == nil || g.control == nil {
		return errors.New("guardian control channel is closed")
	}
	if err := json.NewEncoder(g.control).Encode(request); err != nil {
		return err
	}
	return nil
}

func (g *guardianSession) closeControl() {
	if g == nil || g.control == nil {
		return
	}
	_ = g.control.Close()
	g.control = nil
}

func (g *guardianSession) waitForExit(limit time.Duration) (guardianWaitResult, error) {
	if g == nil || g.done == nil {
		return guardianWaitResult{}, errors.New("guardian process is unavailable")
	}
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case result := <-g.done:
		return result, nil
	case <-timer.C:
		return guardianWaitResult{}, errors.New("guardian did not exit before cleanup deadline")
	}
}

func (g *guardianSession) waitAfterFailure() {
	if g == nil {
		return
	}
	g.closeControl()
	if _, err := g.waitForExit(guardianWaitLimit); err != nil {
		_, _ = g.waitForExit(time.Second)
	}
}

func readGuardianHandshake(pipe *os.File) (byte, error) {
	readDone := make(chan struct {
		value byte
		err   error
	}, 1)
	go func() {
		var one [1]byte
		_, err := io.ReadFull(pipe, one[:])
		readDone <- struct {
			value byte
			err   error
		}{value: one[0], err: err}
	}()
	timer := time.NewTimer(guardianWaitLimit)
	defer timer.Stop()
	select {
	case result := <-readDone:
		if result.err != nil {
			return 0, fmt.Errorf("read private guardian handshake: %w", result.err)
		}
		return result.value, nil
	case <-timer.C:
		return 0, errors.New("private guardian launch handshake timed out")
	}
}

func abortGuardianStart(command *exec.Cmd, control *os.File) {
	_ = control.Close()
	// No waiter has been installed yet, so the direct child PID remains pinned
	// by its unreaped child record while this signal is sent.
	_ = command.Process.Kill()
	_ = command.Wait()
}

func guardianDescriptorsPresent() bool {
	return descriptorIsPipe(guardianControlFD) && descriptorIsPipe(guardianEventsFD) &&
		descriptorIsPipe(guardianReadyFD) && descriptorIsPipe(guardianInputFD) && descriptorIsPipe(guardianOutputFD)
}

func anchorDescriptorsPresent() bool {
	return descriptorIsPipe(guardianControlFD) && descriptorIsPipe(guardianEventsFD)
}

func descriptorIsPipe(fd int) bool {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return false
	}
	return stat.Mode&unix.S_IFMT == unix.S_IFIFO
}

func guardianEntry() int {
	control := os.NewFile(guardianControlFD, "guardian-control")
	events := os.NewFile(guardianEventsFD, "guardian-events")
	ready := os.NewFile(guardianReadyFD, "guardian-ready")
	stdin := os.NewFile(guardianInputFD, "guardian-child-stdin")
	stdout := os.NewFile(guardianOutputFD, "guardian-child-output")
	defer closeFiles(control, events, ready, stdin, stdout)
	eventEncoder := json.NewEncoder(events)
	readySent := false
	defer func() {
		if !readySent {
			_, _ = ready.Write([]byte{guardianErrorByte})
		}
	}()

	if err := enableChildSubreaper(); err != nil {
		return 125
	}
	executable, err := os.Executable()
	if err != nil {
		return 125
	}
	anchorCommand, anchorControl, anchorIdentity, err := startGroupAnchor(executable)
	if err != nil {
		return 125
	}
	defer anchorControl.Close()
	if _, err := ready.Write([]byte{guardianReadyByte}); err != nil {
		_ = cleanupProcessGroup(anchorIdentity)
		_ = anchorControl.Close()
		_ = anchorCommand.Wait()
		_ = reapOwnedChildren()
		return 125
	}
	readySent = true
	_ = ready.Close()

	decoder := json.NewDecoder(control)
	var request guardianRequest
	if err := decoder.Decode(&request); err != nil || request.Operation != "start" {
		_ = cleanupProcessGroup(anchorIdentity)
		_ = anchorControl.Close()
		_ = anchorCommand.Wait()
		_ = reapOwnedChildren()
		return 0
	}
	for _, entry := range request.Env {
		key, _, _ := strings.Cut(entry, "=")
		if key == internalProcessMode {
			_ = eventEncoder.Encode(guardianEvent{Type: "start_error", ExitStatus: 125, Error: "child environment uses reserved private control key"})
			_ = cleanupProcessGroup(anchorIdentity)
			_ = anchorControl.Close()
			_ = anchorCommand.Wait()
			_ = reapOwnedChildren()
			_ = eventEncoder.Encode(guardianEvent{Type: "finished", ExitStatus: 125})
			return 0
		}
	}

	worker := exec.Command(request.Executable, request.Args...)
	worker.Args = append([]string{request.Argv0}, request.Args...)
	worker.Dir = request.Dir
	worker.Env = append([]string{}, request.Env...)
	worker.Stdin = stdin
	worker.Stdout = stdout
	worker.Stderr = stdout
	worker.SysProcAttr = newProcessGroupAttr(anchorIdentity.pid)
	if err := worker.Start(); err != nil {
		status, unavailable := unavailableForStartError(err)
		if status == 0 {
			status = 125
		}
		_ = eventEncoder.Encode(guardianEvent{Type: "start_error", ExitStatus: status, Unavailable: unavailable, Error: err.Error()})
		_ = cleanupProcessGroup(anchorIdentity)
		_ = anchorControl.Close()
		_ = anchorCommand.Wait()
		_ = reapOwnedChildren()
		_ = eventEncoder.Encode(guardianEvent{Type: "finished", ExitStatus: status})
		return 0
	}
	_ = stdin.Close()
	_ = stdout.Close()
	if err := eventEncoder.Encode(guardianEvent{Type: "started"}); err != nil {
		// Control EOF is the authoritative parent-death signal. Continue into
		// the wait loop so the worker group is stopped even if the reader died.
	}

	workerDone := make(chan childWaitResult, 1)
	go func() {
		waitErr := worker.Wait()
		state := worker.ProcessState
		var exitError *exec.ExitError
		if errors.As(waitErr, &exitError) {
			waitErr = nil
		}
		status := 125
		if state != nil {
			status = exitStatus(state)
		}
		workerDone <- childWaitResult{status: status, err: waitErr}
	}()
	commands := make(chan guardianRequest, 1)
	go func() {
		for {
			var command guardianRequest
			if err := decoder.Decode(&command); err != nil {
				if errors.Is(err, io.EOF) {
					commands <- guardianRequest{Operation: "parent_eof"}
				} else {
					commands <- guardianRequest{Operation: "parent_eof"}
				}
				return
			}
			commands <- command
		}
	}()

	var childResult *childWaitResult
	cleanup := false
	for !cleanup {
		select {
		case result := <-workerDone:
			childResult = &result
			_ = eventEncoder.Encode(guardianEvent{Type: "exited", ExitStatus: result.status, Error: errorText(result.err)})
			command := <-commands
			cleanup = command.Operation == "cleanup" || command.Operation == "parent_eof"
		case command := <-commands:
			cleanup = command.Operation == "cleanup" || command.Operation == "parent_eof"
		}
	}

	groupErr := cleanupProcessGroup(anchorIdentity)
	if childResult == nil {
		result := <-workerDone
		childResult = &result
		_ = eventEncoder.Encode(guardianEvent{Type: "exited", ExitStatus: result.status, Error: errorText(result.err)})
	}
	if err := anchorControl.Close(); err != nil && groupErr == nil {
		groupErr = err
	}
	anchorErr := anchorCommand.Wait()
	reapErr := reapOwnedChildren()
	if groupErr == nil {
		var exitError *exec.ExitError
		if anchorErr != nil && !errors.As(anchorErr, &exitError) {
			groupErr = anchorErr
		}
	}
	if groupErr == nil {
		groupErr = reapErr
	}
	if childResult.err == nil && groupErr != nil {
		childResult.err = groupErr
	}
	_ = eventEncoder.Encode(guardianEvent{Type: "finished", ExitStatus: childResult.status, Error: errorText(childResult.err)})
	if groupErr != nil {
		return 124
	}
	return 0
}

type childWaitResult struct {
	status int
	err    error
}

func startGroupAnchor(executable string) (*exec.Cmd, *os.File, processIdentity, error) {
	controlRead, controlWrite, err := os.Pipe()
	if err != nil {
		return nil, nil, processIdentity{}, fmt.Errorf("create anchor control pipe: %w", err)
	}
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		closeFiles(controlRead, controlWrite)
		return nil, nil, processIdentity{}, fmt.Errorf("create anchor handshake pipe: %w", err)
	}
	command := exec.Command(executable)
	command.Args = privateProcessArgs(executable)
	command.Env = []string{internalProcessMode + "=" + anchorMode}
	command.ExtraFiles = []*os.File{controlRead, readyWrite}
	command.SysProcAttr = newProcessGroupAttr(0)
	command.Stdin = nil
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		closeFiles(controlRead, controlWrite, readyRead, readyWrite)
		return nil, nil, processIdentity{}, fmt.Errorf("start group anchor: %w", err)
	}
	_ = controlRead.Close()
	_ = readyWrite.Close()
	identity, err := captureProcessIdentity(command.Process.Pid)
	if err != nil {
		_ = controlWrite.Close()
		_ = readyRead.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, nil, processIdentity{}, fmt.Errorf("identify group anchor: %w", err)
	}
	if identity.pgid != identity.pid {
		_ = controlWrite.Close()
		_ = readyRead.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, nil, processIdentity{}, errors.New("group anchor did not establish its own process group")
	}
	ready, err := readGuardianHandshake(readyRead)
	_ = readyRead.Close()
	if err != nil || ready != guardianReadyByte {
		_ = controlWrite.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
		if err == nil {
			err = errors.New("group anchor rejected its launch handshake")
		}
		return nil, nil, processIdentity{}, err
	}
	return command, controlWrite, identity, nil
}

func anchorEntry() int {
	control := os.NewFile(guardianControlFD, "anchor-control")
	ready := os.NewFile(guardianEventsFD, "anchor-ready")
	defer closeFiles(control, ready)
	signal.Ignore(syscall.SIGTERM)
	identity, err := captureProcessIdentity(os.Getpid())
	if err != nil || identity.pgid != os.Getpid() {
		_, _ = ready.Write([]byte{guardianErrorByte})
		return 125
	}
	if _, err := ready.Write([]byte{guardianReadyByte}); err != nil {
		return 125
	}
	_, _ = io.Copy(io.Discard, control)
	_ = cleanupProcessGroup(identity)
	return 0
}

func cleanupProcessGroup(identity processIdentity) error {
	if !processIdentityCurrent(identity) {
		return errors.New("process group identity changed before cleanup")
	}
	if err := signalProcessGroup(identity, unix.SIGTERM); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	time.Sleep(terminationGrace)
	if !processIdentityCurrent(identity) {
		return errors.New("process group identity changed during cleanup")
	}
	if err := signalProcessGroup(identity, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	return nil
}

func eventError(event guardianEvent) error {
	if event.Error == "" {
		return nil
	}
	return errors.New(event.Error)
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func closeFiles(files ...*os.File) {
	for _, file := range files {
		if file != nil {
			_ = file.Close()
		}
	}
}

func privateProcessArgs(executable string) []string {
	args := []string{executable}
	if strings.HasSuffix(executable, ".test") {
		args = append(args, "-test.run=^$")
	}
	return args
}
