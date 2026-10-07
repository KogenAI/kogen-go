package journal

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalidRecord = errors.New("journal: invalid record")
	eventNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// RunEvent is one timestamped JSONL event. Fields are kept as JSON values so
// callers can preserve event-specific schema without changing the journal
// envelope. A present reason field must always be a string.
type RunEvent struct {
	Event  string
	TS     int64
	Fields map[string]json.RawMessage
}

func NewRunEvent(event string, ts int64) RunEvent {
	return RunEvent{Event: event, TS: ts, Fields: make(map[string]json.RawMessage)}
}

// Set adds or replaces one JSON field. The envelope keys are reserved.
func (e *RunEvent) Set(key string, value any) error {
	if e == nil || key == "" || key == "event" || key == "ts" {
		return fmt.Errorf("%w: reserved or empty event field %q", ErrInvalidRecord, key)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%w: event field %q: %v", ErrInvalidRecord, key, err)
	}
	if e.Fields == nil {
		e.Fields = make(map[string]json.RawMessage)
	}
	e.Fields[key] = encoded
	return nil
}

// SetText preserves valid UTF-8 text. Arbitrary bytes are represented as
// base64 under <key>_base64, matching the run journal format.
func (e *RunEvent) SetText(key string, value []byte) error {
	if !utf8.Valid(value) {
		if err := e.Set(key+"_base64", base64.StdEncoding.EncodeToString(value)); err != nil {
			return err
		}
		delete(e.Fields, key)
		return nil
	}
	if err := e.Set(key, string(value)); err != nil {
		return err
	}
	delete(e.Fields, key+"_base64")
	return nil
}

func (e RunEvent) MarshalJSON() ([]byte, error) {
	if !eventNamePattern.MatchString(e.Event) {
		return nil, fmt.Errorf("%w: invalid event name", ErrInvalidRecord)
	}
	fields := make(map[string]json.RawMessage, len(e.Fields)+2)
	name, _ := json.Marshal(e.Event)
	ts, _ := json.Marshal(e.TS)
	fields["event"], fields["ts"] = name, ts
	for key, raw := range e.Fields {
		if key == "" || key == "event" || key == "ts" || !json.Valid(raw) {
			return nil, fmt.Errorf("%w: invalid event field %q", ErrInvalidRecord, key)
		}
		fields[key] = bytes.Clone(raw)
	}
	if raw, ok := fields["reason"]; ok {
		var reason any
		if err := json.Unmarshal(raw, &reason); err != nil {
			return nil, fmt.Errorf("%w: reason must be a string", ErrInvalidRecord)
		}
		if _, ok := reason.(string); !ok {
			return nil, fmt.Errorf("%w: reason must be a string", ErrInvalidRecord)
		}
	}
	return json.Marshal(fields)
}

func (e *RunEvent) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	eventRaw, hasEvent := fields["event"]
	tsRaw, hasTS := fields["ts"]
	if !hasEvent || !hasTS {
		return fmt.Errorf("%w: event and ts are required", ErrInvalidRecord)
	}
	var eventName string
	if err := json.Unmarshal(eventRaw, &eventName); err != nil || eventName == "" || len(bytes.TrimSpace(eventRaw)) == 0 || bytes.TrimSpace(eventRaw)[0] != '"' {
		return fmt.Errorf("%w: event name must be a string", ErrInvalidRecord)
	}
	ts, err := strconv.ParseInt(string(bytes.TrimSpace(tsRaw)), 10, 64)
	if err != nil {
		return fmt.Errorf("%w: event ts must be an integer", ErrInvalidRecord)
	}
	if !eventNamePattern.MatchString(eventName) {
		return fmt.Errorf("%w: invalid event name", ErrInvalidRecord)
	}
	delete(fields, "event")
	delete(fields, "ts")
	*e = RunEvent{Event: eventName, TS: ts, Fields: fields}
	_, err = e.MarshalJSON()
	return err
}

// LandingRecord is the immutable landing intent persisted before the base ref
// moves.
type LandingRecord struct {
	ApprovalCommit  string `json:"approval_commit"`
	RunID           string `json:"run_id"`
	ExpectedParent  string `json:"expected_parent"`
	FinalTree       string `json:"final_tree"`
	CandidateCommit string `json:"candidate_commit"`
}

// RecoveryRecord identifies a complete preserved tree as unverified. A record
// names either a retained ref and tree, or an archive and manifest identity.
type RecoveryRecord struct {
	Workspace    string  `json:"workspace"`
	Base         string  `json:"base"`
	Tree         *string `json:"tree"`
	Ref          *string `json:"ref"`
	Archive      *string `json:"archive"`
	Verification string  `json:"verification"`
}

func (r RecoveryRecord) Validate() error {
	if strings.TrimSpace(r.Workspace) == "" || strings.TrimSpace(r.Base) == "" || r.Verification != "unverified" {
		return fmt.Errorf("%w: recovery requires workspace, base, and unverified status", ErrInvalidRecord)
	}
	refTree := r.Tree != nil && *r.Tree != "" && r.Ref != nil && *r.Ref != "" && r.Archive == nil
	archive := r.Archive != nil && *r.Archive != "" && r.Tree == nil && r.Ref == nil
	if !refTree && !archive {
		return fmt.Errorf("%w: recovery must identify a ref/tree or archive", ErrInvalidRecord)
	}
	return nil
}

// RunSnapshot is the durable run.json projection. MarshalJSON always emits
// landing (including null), recovery (including []), and cleanup_pending.
type RunSnapshot struct {
	Schema         int                        `json:"schema"`
	RunID          string                     `json:"run_id"`
	Slug           string                     `json:"slug"`
	ApprovalSHA256 string                     `json:"approval_sha256"`
	ApprovalCommit string                     `json:"approval_commit"`
	TargetBranch   string                     `json:"target_branch"`
	Status         string                     `json:"status"`
	Landing        *LandingRecord             `json:"landing"`
	OwnerPID       int64                      `json:"owner_pid"`
	OwnerStartedMS int64                      `json:"owner_started_ms"`
	StartedMS      int64                      `json:"started_ms"`
	Recovery       []RecoveryRecord           `json:"recovery"`
	CleanupPending bool                       `json:"cleanup_pending"`
	Fields         map[string]json.RawMessage `json:"-"`
}

func (s RunSnapshot) Validate() error {
	if s.Schema != 2 || !isHex(s.RunID, 32) {
		return fmt.Errorf("%w: run snapshot requires schema 2 and a 32-hex run_id", ErrInvalidRecord)
	}
	if s.Slug == "" || !isHex(s.ApprovalSHA256, 64) || (!isHex(s.ApprovalCommit, 40) && !isHex(s.ApprovalCommit, 64)) || s.TargetBranch == "" {
		return fmt.Errorf("%w: incomplete run snapshot identity", ErrInvalidRecord)
	}
	if s.OwnerPID <= 0 || s.OwnerStartedMS <= 0 || s.StartedMS <= 0 {
		return fmt.Errorf("%w: run owner and start timestamps must be positive", ErrInvalidRecord)
	}
	switch s.Status {
	case "running", "landed", "failed", "parked", "stopped":
	default:
		return fmt.Errorf("%w: invalid run status %q", ErrInvalidRecord, s.Status)
	}
	for _, recovery := range s.Recovery {
		if err := recovery.Validate(); err != nil {
			return err
		}
	}
	if s.Landing != nil {
		if s.Landing.ApprovalCommit == "" || s.Landing.ApprovalCommit != s.ApprovalCommit || s.Landing.RunID != s.RunID || s.Landing.ExpectedParent == "" || s.Landing.FinalTree == "" || s.Landing.CandidateCommit == "" {
			return fmt.Errorf("%w: incomplete landing record", ErrInvalidRecord)
		}
	}
	return nil
}

func (s RunSnapshot) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	fields := make(map[string]json.RawMessage, len(s.Fields)+13)
	recovery := s.Recovery
	if recovery == nil {
		recovery = []RecoveryRecord{}
	}
	known := map[string]any{
		"schema": s.Schema, "run_id": s.RunID, "slug": s.Slug,
		"approval_sha256": s.ApprovalSHA256, "approval_commit": s.ApprovalCommit,
		"target_branch": s.TargetBranch, "status": s.Status, "landing": s.Landing,
		"owner_pid": s.OwnerPID, "owner_started_ms": s.OwnerStartedMS,
		"started_ms": s.StartedMS, "recovery": recovery, "cleanup_pending": s.CleanupPending,
	}
	for key, value := range known {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		fields[key] = encoded
	}
	for key, raw := range s.Fields {
		if _, reserved := known[key]; reserved || key == "" || !json.Valid(raw) {
			return nil, fmt.Errorf("%w: invalid or reserved snapshot field %q", ErrInvalidRecord, key)
		}
		fields[key] = bytes.Clone(raw)
	}
	return json.Marshal(fields)
}

func (s *RunSnapshot) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, key := range []string{"schema", "run_id", "slug", "approval_sha256", "approval_commit", "target_branch", "status", "landing", "owner_pid", "owner_started_ms", "started_ms"} {
		if _, exists := fields[key]; !exists {
			return fmt.Errorf("%w: run snapshot field %q is required", ErrInvalidRecord, key)
		}
	}
	if raw, exists := fields["cleanup_pending"]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("%w: cleanup_pending must be boolean", ErrInvalidRecord)
	}
	decode := func(key string, target any) error {
		raw, exists := fields[key]
		if !exists {
			return nil
		}
		return json.Unmarshal(raw, target)
	}
	var snapshot RunSnapshot
	for key, target := range map[string]any{
		"schema": &snapshot.Schema, "run_id": &snapshot.RunID, "slug": &snapshot.Slug,
		"approval_sha256": &snapshot.ApprovalSHA256, "approval_commit": &snapshot.ApprovalCommit,
		"target_branch": &snapshot.TargetBranch, "status": &snapshot.Status, "landing": &snapshot.Landing,
		"owner_pid": &snapshot.OwnerPID, "owner_started_ms": &snapshot.OwnerStartedMS,
		"started_ms": &snapshot.StartedMS, "recovery": &snapshot.Recovery,
		"cleanup_pending": &snapshot.CleanupPending,
	} {
		if err := decode(key, target); err != nil {
			return fmt.Errorf("journal: decode run snapshot field %q: %w", key, err)
		}
		delete(fields, key)
	}
	if snapshot.Recovery == nil {
		snapshot.Recovery = []RecoveryRecord{}
	}
	snapshot.Fields = fields
	*s = snapshot
	return nil
}

func isHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, c := range value {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func validateRelativeDirectory(name string) error {
	if name == "" || name == "." || !fs.ValidPath(name) || strings.ContainsRune(name, 0) {
		return fmt.Errorf("%w: invalid run directory", ErrInvalidRecord)
	}
	for _, part := range strings.Split(name, "/") {
		if strings.EqualFold(part, ".git") {
			return fmt.Errorf("%w: invalid run directory", ErrInvalidRecord)
		}
	}
	return nil
}
