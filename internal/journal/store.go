package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"

	"kogen-go/internal/safefs"
)

const (
	runSnapshotFile = "run.json"
	runEventsFile   = "events.jsonl"
	transcriptFile  = "transcript.jsonl"
)

// PublicationStage names durable boundaries exposed for crash-injection
// fixtures. An injected error models process death; it does not roll back an
// already published event or snapshot.
type PublicationStage string

const (
	AfterEventAppend    PublicationStage = "after_event_append"
	BeforeSnapshotWrite PublicationStage = "before_snapshot_write"
	AfterSnapshotWrite  PublicationStage = "after_snapshot_write"
)

// FaultInjector is only intended for deterministic crash-boundary fixtures.
// Production callers should use Record, which does not install an injector.
type FaultInjector func(PublicationStage) error

// RunStore persists one run directory beneath a caller-owned descriptor root.
// The root lifetime remains the caller's responsibility.
type RunStore struct {
	root      *safefs.Root
	directory string
	mu        sync.Mutex
}

func NewRunStore(root *safefs.Root, runDirectory string) (*RunStore, error) {
	if root == nil {
		return nil, fmt.Errorf("%w: nil filesystem root", ErrInvalidRecord)
	}
	if err := validateRelativeDirectory(runDirectory); err != nil {
		return nil, err
	}
	return &RunStore{root: root, directory: runDirectory}, nil
}

// Directory returns the validated root-relative run directory. It never
// contains an absolute path.
func (s *RunStore) Directory() string {
	if s == nil {
		return ""
	}
	return s.directory
}

// Create makes the run directory if needed and publishes the initial snapshot
// create-only. It refuses to replace an existing run record.
func (s *RunStore) Create(snapshot RunSnapshot) error {
	if err := s.validateSnapshot(snapshot); err != nil {
		return err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("journal: encode run snapshot: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDirectory(); err != nil {
		return err
	}
	for _, leaf := range []string{runSnapshotFile, runEventsFile} {
		if _, err := s.root.Lstat(s.path(leaf)); err == nil {
			return fmt.Errorf("journal: run record already exists")
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("journal: inspect run record: %w", err)
		}
	}
	if err := s.root.PublishPrivate(s.path(runSnapshotFile), encoded, safefs.PublicationCreateOnly); err != nil {
		return fmt.Errorf("journal: publish initial snapshot: %w", err)
	}
	return nil
}

// Record appends and durably publishes event before atomically replacing the
// matching run.json snapshot. A crash between these writes intentionally leaves
// the append-only journal ahead of the snapshot, never the reverse.
func (s *RunStore) Record(event RunEvent, snapshot RunSnapshot) error {
	return s.RecordWithFaultInjector(event, snapshot, nil)
}

// RecordRecoveryPreserved adds a lossless recovery identity to run.json and
// appends its matching recovery_preserved event using the normal event-first
// durability order. Cleanup remains a caller-controlled later decision.
func (s *RunStore) RecordRecoveryPreserved(snapshot RunSnapshot, ts int64, recovery RecoveryRecord) error {
	if err := s.validateSnapshot(snapshot); err != nil {
		return err
	}
	if err := recovery.Validate(); err != nil {
		return err
	}
	for _, existing := range snapshot.Recovery {
		if sameRecovery(existing, recovery) {
			return nil
		}
	}
	snapshot.Recovery = append(append([]RecoveryRecord(nil), snapshot.Recovery...), recovery)
	if snapshot.Recovery == nil {
		snapshot.Recovery = []RecoveryRecord{}
	}
	event, err := RecoveryPreservedEvent(ts, recovery)
	if err != nil {
		return err
	}
	return s.Record(event, snapshot)
}

// RecordCleanupFailure marks cleanup_pending and durably records the workspace
// and failure detail before a recovery controller retries cleanup.
func (s *RunStore) RecordCleanupFailure(snapshot RunSnapshot, ts int64, workspace string, detail []byte) error {
	event, err := CleanupFailureEvent(ts, workspace, detail)
	if err != nil {
		return err
	}
	snapshot.CleanupPending = true
	return s.Record(event, snapshot)
}

// RecordWithFaultInjector runs an optional deterministic hook at each
// publication boundary. The hook is primarily for crash-injection fixtures.
func (s *RunStore) RecordWithFaultInjector(event RunEvent, snapshot RunSnapshot, inject FaultInjector) error {
	if s == nil || s.root == nil {
		return fmt.Errorf("%w: nil run store", ErrInvalidRecord)
	}
	if _, err := event.MarshalJSON(); err != nil {
		return err
	}
	if err := s.validateSnapshot(snapshot); err != nil {
		return err
	}
	encodedSnapshot, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("journal: encode run snapshot: %w", err)
	}
	encodedEvent, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("journal: encode run event: %w", err)
	}
	encodedEvent = append(encodedEvent, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireInitialized(); err != nil {
		return err
	}
	if err := s.root.AppendPrivate(s.path(runEventsFile), encodedEvent); err != nil {
		return fmt.Errorf("journal: append run event: %w", err)
	}
	if err := injectAt(inject, AfterEventAppend); err != nil {
		return err
	}
	if err := injectAt(inject, BeforeSnapshotWrite); err != nil {
		return err
	}
	if err := s.root.PublishPrivate(s.path(runSnapshotFile), encodedSnapshot, safefs.PublicationReplace); err != nil {
		return fmt.Errorf("journal: publish run snapshot: %w", err)
	}
	if err := injectAt(inject, AfterSnapshotWrite); err != nil {
		return err
	}
	return nil
}

// WriteSnapshot atomically publishes the current run projection without adding
// a journal event. Events with state transitions should use Record instead.
func (s *RunStore) WriteSnapshot(snapshot RunSnapshot) error {
	if s == nil || s.root == nil {
		return fmt.Errorf("%w: nil run store", ErrInvalidRecord)
	}
	if err := s.validateSnapshot(snapshot); err != nil {
		return err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("journal: encode run snapshot: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireInitialized(); err != nil {
		return err
	}
	if err := s.root.PublishPrivate(s.path(runSnapshotFile), encoded, safefs.PublicationReplace); err != nil {
		return fmt.Errorf("journal: publish run snapshot: %w", err)
	}
	return nil
}

// AppendTranscript appends a constrained, privacy-filtered provider record.
// Prompt text, request/response bodies, and header values have no field in the
// transcript schema.
func (s *RunStore) AppendTranscript(record TranscriptRecord) error {
	if s == nil || s.root == nil {
		return fmt.Errorf("%w: nil run store", ErrInvalidRecord)
	}
	if err := record.Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("journal: encode transcript record: %w", err)
	}
	encoded = append(encoded, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireInitialized(); err != nil {
		return err
	}
	if err := s.root.AppendPrivate(s.path(transcriptFile), encoded); err != nil {
		return fmt.Errorf("journal: append transcript: %w", err)
	}
	return nil
}

// AppendAgentEvent writes only bounded state metadata. It intentionally has no
// message, prompt, path, or tool-output field.
func (s *RunStore) AppendAgentEvent(agentID string, event AgentEvent) error {
	if s == nil || s.root == nil {
		return fmt.Errorf("%w: nil run store", ErrInvalidRecord)
	}
	if !isHex(agentID, 32) {
		return fmt.Errorf("%w: agent id must be 32 hexadecimal characters", ErrInvalidRecord)
	}
	if err := event.Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("journal: encode agent event: %w", err)
	}
	encoded = append(encoded, '\n')
	agentDirectory := s.path("agents/" + strings.ToLower(agentID))
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireInitialized(); err != nil {
		return err
	}
	if err := s.root.MkdirAll(agentDirectory, 0o700); err != nil {
		return fmt.Errorf("journal: create agent journal: %w", err)
	}
	if err := s.requireRealDirectory(agentDirectory); err != nil {
		return err
	}
	if err := s.root.AppendPrivate(agentDirectory+"/events.jsonl", encoded); err != nil {
		return fmt.Errorf("journal: append agent event: %w", err)
	}
	return nil
}

func (s *RunStore) ReadSnapshot() (RunSnapshot, error) {
	var snapshot RunSnapshot
	if s == nil || s.root == nil {
		return snapshot, fmt.Errorf("%w: nil run store", ErrInvalidRecord)
	}
	data, err := s.root.ReadFile(s.path(runSnapshotFile))
	if err != nil {
		return snapshot, fmt.Errorf("journal: read run snapshot: %w", err)
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return snapshot, fmt.Errorf("journal: decode run snapshot: %w", err)
	}
	if err := s.validateSnapshot(snapshot); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func (s *RunStore) ReadEvents() ([]RunEvent, error) {
	if s == nil || s.root == nil {
		return nil, fmt.Errorf("%w: nil run store", ErrInvalidRecord)
	}
	data, err := s.root.ReadFile(s.path(runEventsFile))
	if err != nil {
		return nil, fmt.Errorf("journal: read run events: %w", err)
	}
	lines := bytes.Split(data, []byte{'\n'})
	events := make([]RunEvent, 0, len(lines))
	for i, line := range lines {
		if len(line) == 0 && i == len(lines)-1 {
			continue
		}
		if len(line) == 0 {
			return nil, fmt.Errorf("journal: decode event line %d: empty line", i+1)
		}
		var event RunEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, fmt.Errorf("journal: decode event line %d: %w", i+1, err)
		}
		events = append(events, event)
	}
	return events, nil
}

func (s *RunStore) ensureDirectory() error {
	if s == nil || s.root == nil {
		return fmt.Errorf("%w: nil run store", ErrInvalidRecord)
	}
	if err := s.root.MkdirAll(s.directory, 0o700); err != nil {
		return fmt.Errorf("journal: create run directory: %w", err)
	}
	return s.requireRealDirectory(s.directory)
}

func (s *RunStore) requireDirectory() error {
	if s == nil || s.root == nil {
		return fmt.Errorf("%w: nil run store", ErrInvalidRecord)
	}
	return s.requireRealDirectory(s.directory)
}

func (s *RunStore) requireInitialized() error {
	if err := s.requireDirectory(); err != nil {
		return err
	}
	if _, err := s.root.Lstat(s.path(runSnapshotFile)); err != nil {
		return fmt.Errorf("journal: run snapshot is not initialized: %w", err)
	}
	return nil
}

func (s *RunStore) validateSnapshot(snapshot RunSnapshot) error {
	if s == nil || s.root == nil {
		return fmt.Errorf("%w: nil run store", ErrInvalidRecord)
	}
	if err := snapshot.Validate(); err != nil {
		return err
	}
	leaf := s.directory[strings.LastIndex(s.directory, "/")+1:]
	if leaf != snapshot.RunID {
		return fmt.Errorf("%w: snapshot run_id does not match run directory", ErrInvalidRecord)
	}
	return nil
}

func (s *RunStore) requireRealDirectory(name string) error {
	info, err := s.root.Lstat(name)
	if err != nil {
		return fmt.Errorf("journal: inspect run directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: run journal path is not a real directory", ErrInvalidRecord)
	}
	return nil
}

func (s *RunStore) path(leaf string) string {
	if s == nil || s.directory == "" {
		return leaf
	}
	return s.directory + "/" + leaf
}

func sameRecovery(left, right RecoveryRecord) bool {
	return left.Workspace == right.Workspace && left.Base == right.Base &&
		equalStringPointer(left.Tree, right.Tree) && equalStringPointer(left.Ref, right.Ref) &&
		equalStringPointer(left.Archive, right.Archive) && left.Verification == right.Verification
}

func equalStringPointer(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func injectAt(inject FaultInjector, stage PublicationStage) error {
	if inject == nil {
		return nil
	}
	if err := inject(stage); err != nil {
		return fmt.Errorf("journal: injected failure at %s: %w", stage, err)
	}
	return nil
}
