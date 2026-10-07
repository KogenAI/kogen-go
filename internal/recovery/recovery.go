// Package recovery reconciles dead Build owners before status derivation and
// queue scheduling. Preservation is an explicit effect port so this package
// cannot remove a workspace before the D3 implementation has made its latest
// bytes durable.
package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/journal"
	"kogen-go/internal/safefs"
)

const claimRef = "refs/kogen/claim"

var runIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var workspaceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// RefEffects are the origin Git operations used to reconcile landing state,
// validate durable preservation refs, and perform owner-checked ref cleanup.
type RefEffects interface {
	ReadRef(context.Context, string) (contract.RefObservation, error)
	ResolveTree(context.Context, string) (contract.ObjectID, error)
	IsAncestor(context.Context, contract.ObjectID, contract.ObjectID) (bool, error)
	CompareAndSwap(context.Context, contract.RefUpdate) (contract.RefUpdateResult, error)
}

// WriterPort stops every run-owned writer before the controller freezes any
// workspace. Implementations should be idempotent; recovery may call this
// again for a terminal run whose cleanup is pending.
type WriterPort interface {
	StopRun(context.Context, RunIdentity) error
}

// PreservationPort durably captures one frozen workspace. It may adopt a
// matching create-only snapshot already published by an earlier recovery
// attempt. A successful return is trusted only after the controller validates
// the record and, for ref-backed records, reads the retained tree from origin.
type PreservationPort interface {
	Preserve(context.Context, PreservationRequest) (journal.RecoveryRecord, error)
}

// OwnerProbe determines whether the recorded PID still names the recorded
// process start instant. Errors are treated conservatively as a live owner.
type OwnerProbe interface {
	Alive(context.Context, int64, int64) (bool, error)
}

type workspaceCleaner interface {
	ReadDir() ([]fs.DirEntry, error)
	Remove(string) error
	Close() error
}

// Config binds the state root and the effects recovery must coordinate. The
// Git command runner is used only to inspect the claim blob.
type Config struct {
	StateRoot string
	Git       contract.GitPort
	GitPolicy contract.GitPolicy
	Refs      RefEffects
	Writers   WriterPort
	Preserver PreservationPort
	Owners    OwnerProbe
	Now       func() time.Time
}

// RunIdentity is the durable process identity attached to one Build.
type RunIdentity struct {
	RunID     string
	OwnerPID  int64
	StartedMS int64
}

// Workspace identifies a single run-owned workspace under the state root.
// Name is the stable workspace suffix used in refs and journal records.
type Workspace struct {
	Name         string
	RelativePath string
	AbsolutePath string
	Directory    bool
}

// PreservationRequest contains all data needed by the D3 effect to publish
// or adopt a complete snapshot. Existing records are copied from run.json;
// the effect compares them with the frozen bytes and never replaces them.
type PreservationRequest struct {
	RunID     string
	Workspace Workspace
	Base      contract.ObjectID
	Existing  []journal.RecoveryRecord
}

// Result describes one recovered or retried run. Terminal status/reason are
// immutable after the initial reconciliation; cleanup failure only leaves the
// durable cleanup_pending obligation set.
type Result struct {
	RunID          string
	Slug           string
	Status         string
	Reason         string
	Retried        bool
	CleanupPending bool
	Preserved      []journal.RecoveryRecord
}

// Issue identifies a failure that prevented reconciliation or cleanup. A
// per-run issue does not stop other runs from being processed.
type Issue struct {
	RunID     string
	Operation string
	Detail    string
}

// SkippedRun records a live or unverifiable owner that recovery left intact.
type SkippedRun struct {
	RunID  string
	Reason string
}

// Report is the complete result of one recovery sweep.
type Report struct {
	Runs    []Result
	Issues  []Issue
	Skipped []SkippedRun
}

// Controller owns a rooted state directory and the effects required to
// reconcile runs. Close it when the state root is no longer needed.
type Controller struct {
	stateRootPath string
	root          *safefs.Root
	cleaner       workspaceCleaner
	git           contract.GitPort
	gitPolicy     contract.GitPolicy
	refs          RefEffects
	writers       WriterPort
	preserver     PreservationPort
	owners        OwnerProbe
	now           func() time.Time
}

// NewController validates and opens the state root, then binds the required
// reconciliation effects. Missing preservation or custody ports are rejected
// so callers cannot accidentally enable destructive recovery without them.
func NewController(config Config) (*Controller, error) {
	if strings.TrimSpace(config.StateRoot) == "" || config.Git == nil || config.Refs == nil || config.Writers == nil || config.Preserver == nil {
		return nil, errors.New("recovery: state root, Git, refs, writer, and preservation ports are required")
	}
	if !filepath.IsAbs(config.StateRoot) || filepath.Clean(config.StateRoot) != config.StateRoot {
		return nil, errors.New("recovery: state root must be a clean absolute path")
	}
	rootPath, err := filepath.EvalSymlinks(config.StateRoot)
	if err != nil {
		return nil, fmt.Errorf("recovery: resolve state root: %w", err)
	}
	info, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("recovery: resolve absolute state root: %w", err)
	}
	root, err := safefs.OpenRoot(info)
	if err != nil {
		return nil, fmt.Errorf("recovery: open state root: %w", err)
	}
	cleaner, err := openWorkspaceCleaner(info)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("recovery: open workspace cleanup root: %w", err)
	}
	if config.GitPolicy.WorkingDirectory == "" {
		_ = root.Close()
		_ = cleaner.Close()
		return nil, errors.New("recovery: origin Git working directory is required")
	}
	owners := config.Owners
	if owners == nil {
		owners = NativeOwnerProbe{}
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &Controller{
		stateRootPath: info, root: root, cleaner: cleaner, git: config.Git,
		gitPolicy: config.GitPolicy, refs: config.Refs,
		writers: config.Writers, preserver: config.Preserver,
		owners: owners, now: now,
	}, nil
}

// Close releases the descriptor-rooted state capability.
func (c *Controller) Close() error {
	if c == nil || c.root == nil {
		return nil
	}
	err := errors.Join(c.root.Close(), c.cleaner.Close())
	c.root = nil
	c.cleaner = nil
	return err
}

// Recover reconciles dead running records and retries terminal cleanup
// obligations. It never retries a Build and never removes candidate or
// recovery refs. A live or uncertain owner is left untouched.
func (c *Controller) Recover(ctx context.Context) (Report, error) {
	if c == nil || c.root == nil || ctx == nil {
		return Report{}, errors.New("recovery: controller and context are required")
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	entries, err := c.runEntries()
	if err != nil {
		return Report{}, err
	}
	report := Report{Runs: make([]Result, 0), Issues: make([]Issue, 0), Skipped: make([]SkippedRun, 0)}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if !entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 || !runIDPattern.MatchString(entry.Name()) {
			continue
		}
		directory := "runs/" + entry.Name()
		store, err := journal.NewRunStore(c.root, directory)
		if err != nil {
			report.Issues = append(report.Issues, issue(entry.Name(), "open_run", err))
			continue
		}
		snapshot, err := store.ReadSnapshot()
		if err != nil {
			report.Issues = append(report.Issues, issue(entry.Name(), "read_run", err))
			continue
		}
		pending := snapshot.Status != "running" && snapshot.CleanupPending
		if snapshot.Status != "running" && !pending {
			continue
		}
		alive, err := c.owners.Alive(ctx, snapshot.OwnerPID, snapshot.OwnerStartedMS)
		if err != nil {
			report.Skipped = append(report.Skipped, SkippedRun{RunID: snapshot.RunID, Reason: "owner_identity_unavailable: " + err.Error()})
			continue
		}
		if alive {
			report.Skipped = append(report.Skipped, SkippedRun{RunID: snapshot.RunID, Reason: "owner_alive"})
			continue
		}
		events, err := store.ReadEvents()
		if err != nil {
			report.Issues = append(report.Issues, issue(snapshot.RunID, "read_events", err))
			continue
		}
		lastEvent := ""
		if len(events) != 0 {
			lastEvent = events[len(events)-1].Event
		}
		base, err := recoveryBase(snapshot, events)
		if err != nil {
			report.Issues = append(report.Issues, issue(snapshot.RunID, "read_base", err))
			continue
		}
		result := Result{RunID: snapshot.RunID, Slug: snapshot.Slug, Status: snapshot.Status, Retried: pending, Preserved: make([]journal.RecoveryRecord, 0)}
		if snapshot.Status == "running" {
			status, reason, err := c.terminalOutcome(ctx, snapshot, lastEvent)
			if err != nil {
				report.Issues = append(report.Issues, issue(snapshot.RunID, "reconcile_landing", err))
				continue
			}
			snapshot.Status = status
			snapshot.CleanupPending = true
			result.Status, result.Reason = status, reason
			if err := recordOutcome(store, &snapshot, c.timestamp(), status, reason); err != nil {
				report.Issues = append(report.Issues, issue(snapshot.RunID, "record_outcome", err))
				continue
			}
		} else {
			result.Status = snapshot.Status
			result.Reason = terminalReason(events, snapshot.Status)
		}
		result = c.reconcileRun(ctx, store, snapshot, base, result, &report)
		report.Runs = append(report.Runs, result)
	}
	return report, nil
}

func (c *Controller) runEntries() ([]fs.DirEntry, error) {
	info, err := c.root.Lstat("runs")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("recovery: inspect runs directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return nil, errors.New("recovery: runs path is not a real directory")
	}
	entries, err := c.root.ReadDir("runs")
	if err != nil {
		return nil, fmt.Errorf("recovery: list runs: %w", err)
	}
	return entries, nil
}

func (c *Controller) terminalOutcome(ctx context.Context, snapshot journal.RunSnapshot, lastEvent string) (string, string, error) {
	if snapshot.Landing != nil {
		candidate, err := gitio.ParseObjectID(snapshot.Landing.CandidateCommit)
		if err != nil {
			return "", "", fmt.Errorf("invalid recorded landing candidate: %w", err)
		}
		branch := snapshot.TargetBranch
		if strings.HasPrefix(branch, "refs/") && !strings.HasPrefix(branch, "refs/heads/") {
			return "", "", errors.New("recorded target branch is outside refs/heads")
		}
		branch = strings.TrimPrefix(branch, "refs/heads/")
		if branch == "" {
			return "", "", errors.New("recorded target branch is empty")
		}
		tip, err := c.refs.ReadRef(ctx, "refs/heads/"+branch)
		if err != nil {
			return "", "", fmt.Errorf("read recorded target branch: %w", err)
		}
		if tip.Exists {
			reachable, err := c.refs.IsAncestor(ctx, candidate, tip.Target)
			if err != nil {
				return "", "", fmt.Errorf("check candidate reachability: %w", err)
			}
			if reachable {
				return "landed", "reconciled", nil
			}
		}
	}
	if lastEvent == "interrupted" {
		return "failed", "interrupted", nil
	}
	return "failed", "crashed", nil
}

func (c *Controller) reconcileRun(ctx context.Context, store *journal.RunStore, snapshot journal.RunSnapshot, base contract.ObjectID, result Result, report *Report) Result {
	identity := RunIdentity{RunID: snapshot.RunID, OwnerPID: snapshot.OwnerPID, StartedMS: snapshot.OwnerStartedMS}
	if err := c.writers.StopRun(ctx, identity); err != nil {
		if recordErr := c.recordCleanupFailure(store, &snapshot, "writers", "stop run writers: "+err.Error()); recordErr != nil {
			report.Issues = append(report.Issues, issue(snapshot.RunID, "record_writer_failure", recordErr))
		}
		report.Issues = append(report.Issues, issue(snapshot.RunID, "stop_writers", err))
		result.CleanupPending = true
		return result
	}

	workspaces, err := c.workspaces(snapshot.RunID)
	if err != nil {
		if recordErr := c.recordCleanupFailure(store, &snapshot, "workspaces", "list run workspaces: "+err.Error()); recordErr != nil {
			report.Issues = append(report.Issues, issue(snapshot.RunID, "record_workspace_failure", recordErr))
		} else {
			c.releaseOwnedClaim(ctx, store, &snapshot, report)
		}
		report.Issues = append(report.Issues, issue(snapshot.RunID, "list_workspaces", err))
		result.CleanupPending = true
		return result
	}

	preserved := make(map[string]bool, len(workspaces))
	preservationOutcomeRecorded := true
	for _, workspace := range workspaces {
		if !workspace.Directory {
			err := errors.New("workspace leaf is not a real directory")
			if recordErr := c.recordCleanupFailure(store, &snapshot, workspace.Name, "preserve workspace: "+err.Error()); recordErr != nil {
				preservationOutcomeRecorded = false
				report.Issues = append(report.Issues, issue(snapshot.RunID, "record_preservation_failure", recordErr))
			}
			report.Issues = append(report.Issues, issue(snapshot.RunID, "preserve_workspace", err))
			continue
		}
		record, preserveErr := c.preserver.Preserve(ctx, PreservationRequest{
			RunID: snapshot.RunID, Workspace: workspace, Base: base,
			Existing: cloneRecoveryRecords(snapshot.Recovery),
		})
		if preserveErr == nil {
			preserveErr = c.validatePreservation(ctx, snapshot.RunID, workspace.Name, base, record)
		}
		if preserveErr != nil {
			if recordErr := c.recordCleanupFailure(store, &snapshot, workspace.Name, "preserve workspace: "+preserveErr.Error()); recordErr != nil {
				preservationOutcomeRecorded = false
				report.Issues = append(report.Issues, issue(snapshot.RunID, "record_preservation_failure", recordErr))
			}
			report.Issues = append(report.Issues, issue(snapshot.RunID, "preserve_workspace", preserveErr))
			continue
		}
		if err := store.RecordRecoveryPreserved(snapshot, c.timestamp(), record); err != nil {
			preservationOutcomeRecorded = false
			report.Issues = append(report.Issues, issue(snapshot.RunID, "record_preservation", err))
			continue
		}
		if !containsRecovery(snapshot.Recovery, record) {
			snapshot.Recovery = append(snapshot.Recovery, record)
		}
		result.Preserved = append(result.Preserved, record)
		preserved[workspace.RelativePath] = true
	}

	if preservationOutcomeRecorded {
		c.releaseOwnedClaim(ctx, store, &snapshot, report)
	}
	if snapshot.Status == "landed" {
		c.removeIncoming(ctx, store, &snapshot, report)
	}

	for _, workspace := range workspaces {
		if !preserved[workspace.RelativePath] {
			continue
		}
		if err := c.cleaner.Remove(workspace.RelativePath); err != nil {
			if recordErr := c.recordCleanupFailure(store, &snapshot, workspace.Name, "remove preserved workspace: "+err.Error()); recordErr != nil {
				report.Issues = append(report.Issues, issue(snapshot.RunID, "record_cleanup_failure", recordErr))
			}
			report.Issues = append(report.Issues, issue(snapshot.RunID, "remove_workspace", err))
		}
	}
	if len(report.IssuesForRun(snapshot.RunID)) == 0 {
		snapshot.CleanupPending = false
		if err := store.WriteSnapshot(snapshot); err != nil {
			report.Issues = append(report.Issues, issue(snapshot.RunID, "clear_cleanup_pending", err))
			snapshot.CleanupPending = true
		}
	}
	result.CleanupPending = snapshot.CleanupPending
	return result
}

func (c *Controller) workspaces(runID string) ([]Workspace, error) {
	entries, err := c.cleaner.ReadDir()
	if err != nil {
		return nil, err
	}
	prefix := runID + "-"
	workspaces := make([]Workspace, 0)
	for _, entry := range entries {
		entryName := entry.Name()
		if !strings.HasPrefix(entryName, prefix) {
			continue
		}
		name := strings.TrimPrefix(entryName, prefix)
		if name == "" {
			continue
		}
		info, err := c.root.Lstat(entryName)
		if err != nil {
			return nil, fmt.Errorf("inspect workspace %q: %w", name, err)
		}
		workspaces = append(workspaces, Workspace{
			Name: name, RelativePath: entryName,
			AbsolutePath: filepath.Join(c.stateRootPath, entryName),
			Directory:    info.IsDir() && info.Mode()&fs.ModeSymlink == 0,
		})
	}
	return workspaces, nil
}

func (c *Controller) validatePreservation(ctx context.Context, runID, workspace string, base contract.ObjectID, record journal.RecoveryRecord) error {
	if err := record.Validate(); err != nil {
		return err
	}
	if record.Workspace != workspace || record.Base != string(base) || record.Verification != "unverified" {
		return errors.New("preservation record identity does not match the frozen workspace")
	}
	if record.Ref != nil {
		if !workspaceNamePattern.MatchString(workspace) {
			return errors.New("workspace name is not valid for a deterministic recovery ref")
		}
		want := "refs/kogen/candidates/" + runID + "/recovery-" + workspace
		if *record.Ref != want {
			return errors.New("recovery ref does not use its deterministic create-only identity")
		}
		if _, err := gitio.ParseObjectID(*record.Tree); err != nil {
			return fmt.Errorf("invalid preserved tree: %w", err)
		}
		ref, err := c.refs.ReadRef(ctx, *record.Ref)
		if err != nil {
			return fmt.Errorf("read preserved ref: %w", err)
		}
		if !ref.Exists {
			return errors.New("preservation ref was not published")
		}
		tree, err := c.refs.ResolveTree(ctx, string(ref.Target))
		if err != nil {
			return fmt.Errorf("resolve preserved ref tree: %w", err)
		}
		if tree != contract.ObjectID(*record.Tree) {
			return errors.New("preservation ref tree does not match the reported tree")
		}
	} else if record.Archive != nil {
		if *record.Archive == "." || !fs.ValidPath(*record.Archive) || path.Clean(*record.Archive) != *record.Archive {
			return errors.New("archive identity must be a safe root-relative identity")
		}
	}
	return nil
}

func (c *Controller) releaseOwnedClaim(ctx context.Context, store *journal.RunStore, snapshot *journal.RunSnapshot, report *Report) {
	claim, err := c.refs.ReadRef(ctx, claimRef)
	if err != nil {
		c.cleanupError(store, snapshot, report, "claim", "read claim: "+err.Error())
		return
	}
	if !claim.Exists {
		return
	}
	if _, err := gitio.ParseObjectID(string(claim.Target)); err != nil {
		c.cleanupError(store, snapshot, report, "claim", "read claim owner: invalid claim ref target")
		return
	}
	result, err := c.git.Exec(ctx, []string{"cat-file", "blob", string(claim.Target) + ":.kogen/claim"}, nil, c.gitPolicy)
	if err != nil {
		c.cleanupError(store, snapshot, report, "claim", "read claim owner: "+err.Error())
		return
	}
	if result.Process.ExitStatus == nil || *result.Process.ExitStatus != 0 {
		c.cleanupError(store, snapshot, report, "claim", "read claim owner: Git did not return the claim blob")
		return
	}
	if strings.TrimSpace(string(result.Stdout)) != snapshot.RunID {
		return
	}
	updated, err := c.refs.CompareAndSwap(ctx, contract.RefUpdate{Name: claimRef, Expected: claim.Target})
	if err != nil || !updated.Updated {
		if err == nil {
			err = errors.New("claim ref changed before release")
		}
		c.cleanupError(store, snapshot, report, "claim", "release owned claim: "+err.Error())
	}
}

func (c *Controller) removeIncoming(ctx context.Context, store *journal.RunStore, snapshot *journal.RunSnapshot, report *Report) {
	name := "refs/kogen/incoming/" + snapshot.RunID
	incoming, err := c.refs.ReadRef(ctx, name)
	if err != nil {
		c.cleanupError(store, snapshot, report, "incoming", "read incoming ref: "+err.Error())
		return
	}
	if !incoming.Exists {
		return
	}
	if snapshot.Landing == nil || string(incoming.Target) != snapshot.Landing.CandidateCommit {
		c.cleanupError(store, snapshot, report, "incoming", "incoming ref target does not match the landed candidate")
		return
	}
	updated, err := c.refs.CompareAndSwap(ctx, contract.RefUpdate{Name: name, Expected: incoming.Target})
	if err != nil || !updated.Updated {
		if err == nil {
			err = errors.New("incoming ref changed before cleanup")
		}
		c.cleanupError(store, snapshot, report, "incoming", "delete landed incoming ref: "+err.Error())
	}
}

func (c *Controller) recordCleanupFailure(store *journal.RunStore, snapshot *journal.RunSnapshot, workspace, detail string) error {
	if workspace == "" {
		workspace = "recovery"
	}
	return store.RecordCleanupFailure(*snapshot, c.timestamp(), workspace, []byte(detail))
}

func (c *Controller) cleanupError(store *journal.RunStore, snapshot *journal.RunSnapshot, report *Report, workspace, detail string) {
	if err := c.recordCleanupFailure(store, snapshot, workspace, detail); err != nil {
		report.Issues = append(report.Issues, issue(snapshot.RunID, "record_cleanup_failure", err))
	}
	report.Issues = append(report.Issues, Issue{RunID: snapshot.RunID, Operation: "cleanup", Detail: detail})
}

func (c *Controller) timestamp() int64 { return c.now().UnixMilli() }

func recoveryBase(snapshot journal.RunSnapshot, events []journal.RunEvent) (contract.ObjectID, error) {
	for _, event := range events {
		if event.Event != "started" {
			continue
		}
		var base string
		if err := json.Unmarshal(event.Fields["base_sha"], &base); err != nil || base == "" {
			continue
		}
		return gitio.ParseObjectID(base)
	}
	if snapshot.Landing != nil && snapshot.Landing.ExpectedParent != "" {
		return gitio.ParseObjectID(snapshot.Landing.ExpectedParent)
	}
	for _, recovery := range snapshot.Recovery {
		if recovery.Base != "" {
			return gitio.ParseObjectID(recovery.Base)
		}
	}
	return "", errors.New("run has no recorded base commit for recovery")
}

func recordOutcome(store *journal.RunStore, snapshot *journal.RunSnapshot, ts int64, status, reason string) error {
	var event journal.RunEvent
	if status == "landed" {
		event = journal.NewRunEvent("reconciled", ts)
		if err := event.Set("status", status); err != nil {
			return err
		}
	} else {
		event = journal.NewRunEvent("finished", ts)
		if err := event.Set("status", status); err != nil {
			return err
		}
		if err := event.Set("reason", reason); err != nil {
			return err
		}
	}
	return store.Record(event, *snapshot)
}

func terminalReason(events []journal.RunEvent, status string) string {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Event != "finished" {
			continue
		}
		var observed string
		if json.Unmarshal(events[index].Fields["reason"], &observed) == nil {
			return observed
		}
	}
	if status == "landed" {
		return "reconciled"
	}
	return ""
}

func containsRecovery(records []journal.RecoveryRecord, candidate journal.RecoveryRecord) bool {
	for _, record := range records {
		if record.Workspace == candidate.Workspace && record.Base == candidate.Base &&
			stringPointer(record.Tree) == stringPointer(candidate.Tree) &&
			stringPointer(record.Ref) == stringPointer(candidate.Ref) &&
			stringPointer(record.Archive) == stringPointer(candidate.Archive) &&
			record.Verification == candidate.Verification {
			return true
		}
	}
	return false
}

func stringPointer(value *string) string {
	if value == nil {
		return "\x00"
	}
	return *value
}

func cloneRecoveryRecords(records []journal.RecoveryRecord) []journal.RecoveryRecord {
	cloned := make([]journal.RecoveryRecord, len(records))
	for index, record := range records {
		cloned[index] = record
		cloned[index].Tree = cloneString(record.Tree)
		cloned[index].Ref = cloneString(record.Ref)
		cloned[index].Archive = cloneString(record.Archive)
	}
	return cloned
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func issue(runID, operation string, err error) Issue {
	return Issue{RunID: runID, Operation: operation, Detail: err.Error()}
}

// IssuesForRun returns a copy of the issues associated with one run. It is a
// helper for consumers that want to display cleanup diagnostics per run.
func (r Report) IssuesForRun(runID string) []Issue {
	issues := make([]Issue, 0)
	for _, issue := range r.Issues {
		if issue.RunID == runID {
			issues = append(issues, issue)
		}
	}
	return issues
}
