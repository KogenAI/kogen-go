// Package gate runs the verification boundary for an approved candidate.
// It applies fixes once, freezes the exact post-fix tree, runs base-relative
// checks and approved acceptance tests, and issues a receipt only when the
// required evidence supports a green result.
package gate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"kogen-go/internal/acceptance"
	acceptancecommand "kogen-go/internal/acceptance/command"
	"kogen-go/internal/contract"
	"kogen-go/internal/findings"
	"kogen-go/internal/protection"
	"kogen-go/internal/safefs"
)

var (
	ErrInvalidRequest = errors.New("gate: invalid request")
	ErrBaseTree       = errors.New("gate: checked base tree does not match the requested base")
)

// AcceptanceRunner executes the configured acceptance command and interprets
// its ledger. Implementations return runner observations; gate policy remains
// here.
type AcceptanceRunner interface {
	Run(context.Context, AcceptanceExecution) (acceptance.Result, error)
}

// AcceptanceExecution gives each attempt the same explicit seed. The adapter
// must apply it to its runner protocol; changing only the run directory cannot
// change the seed or command configuration.
type AcceptanceExecution struct {
	Request acceptancecommand.Request
	Seed    string
	Attempt string
}

// BaselineCheck is one check result bound to the exact base tree that was
// checked during approval.
type BaselineCheck struct {
	Name       string
	Status     contract.CheckStatus
	ExitStatus *int
	Findings   []contract.FindingIdentity
}

// CheckBaseline binds rows to their checked tree. A baseline from another tree
// is ignored; the current immutable base run then supplies the comparison.
type CheckBaseline struct {
	Tree   string
	Checks []BaselineCheck
}

// AcceptancePlan contains the approved test bytes and the configured runner
// request. ExpectedItems is the complete approved item set. ChangeItems only
// affects landability after the full set has passed.
type AcceptancePlan struct {
	Request       acceptancecommand.Request
	ApprovedBytes []byte
	ChangeItems   []string
}

// Request is one complete verification attempt. BaseWorkspace must be a
// workspace materialized from ExpectedBaseTree. Tree snapshots use the
// base-relative Git metadata supplied by the caller.
type Request struct {
	Processes          contract.ProcessRunner
	AcceptanceRunner   AcceptanceRunner
	Trees              acceptance.TreeSnapshotter
	Roots              contract.RootOpener
	BaseWorkspace      string
	CandidateWorkspace string
	RunDir             string
	ExpectedBaseTree   string
	ApprovalSHA256     string
	Baseline           *CheckBaseline
	Fixes              []contract.CheckSpec
	Checks             []contract.CheckSpec
	Acceptance         AcceptancePlan
	Protection         *protection.Protector
	HomeDir            string
	TempDir            string
}

// Verdict is the result of checking all required evidence.
type Verdict string

const (
	VerdictGreen      Verdict = "green"
	VerdictUnverified Verdict = "unverified"
)

// CheckObservation records the actual check status plus its base comparison.
type CheckObservation struct {
	Spec         contract.CheckSpec
	Status       contract.CheckStatus
	ExitStatus   *int
	TimedOut     bool
	Unavailable  bool
	Findings     []findings.Finding
	OutputTail   []byte
	LogPath      string
	TreeBefore   string
	TreeAfter    string
	ChangedPaths []string
	Excused      bool
	BaseStatus   contract.CheckStatus
}

// FixObservation records one configured formatter/fix invocation.
type FixObservation struct {
	Spec        contract.CheckSpec
	ExitStatus  *int
	TimedOut    bool
	Unavailable bool
	OutputTail  []byte
	LogPath     string
}

// AcceptanceAttempt is one accepted-test run.
type AcceptanceAttempt struct {
	Result acceptance.Result
	RunDir string
}

// FlakeEvidence records the initial candidate run, its single same-seed
// retry, the base result, and the IDs allowed by the bounded base-red policy.
// The on-disk record intentionally excludes output text and process paths.
type FlakeEvidence struct {
	Seed       string             `json:"seed"`
	Initial    AcceptanceSummary  `json:"initial"`
	Retry      AcceptanceSummary  `json:"retry"`
	Base       *AcceptanceSummary `json:"base,omitempty"`
	ExcusedIDs []string           `json:"excused_ids"`
	Persisted  bool               `json:"-"`
	StoreError string             `json:"-"`
}

// AcceptanceSummary is the minimal durable outcome needed to review flake
// excusing without storing logs or test output in the gate receipt.
type AcceptanceSummary struct {
	ExitStatus  *int                 `json:"exit_status"`
	TimedOut    bool                 `json:"timed_out"`
	Unavailable bool                 `json:"unavailable"`
	Failures    []acceptance.Failure `json:"failures"`
	ItemPass    map[string]bool      `json:"item_pass"`
	TreeBefore  string               `json:"tree_before"`
	TreeAfter   string               `json:"tree_after"`
}

// VerificationReceipt binds the successful gate to the exact post-fix tree.
// It is created only after checks and acceptance finish with that same tree.
type VerificationReceipt struct {
	BaseTree                string             `json:"base_tree"`
	CandidateTree           string             `json:"candidate_tree"`
	ApprovalSHA256          string             `json:"approval_sha256,omitempty"`
	ProtectedManifestSHA256 string             `json:"protected_manifest_sha256,omitempty"`
	Checks                  []CheckObservation `json:"checks"`
	StartedAt               time.Time          `json:"started_at"`
	CompletedAt             time.Time          `json:"completed_at"`
}

// AcceptanceCounts are fixed by the gate's approved item set and observed
// results. Auditor advice is never included in these values.
type AcceptanceCounts struct {
	Passed int
	Total  int
}

// AuditAdvice is a display-only observation. It has no acceptance effect.
type AuditAdvice struct {
	ID      string
	Verdict string
	Reason  string
}

// GateReport exposes immutable observations through copy-returning accessors.
// RecordAuditAdvice can append display-only advice but cannot recalculate a
// verdict, receipt, acceptance set, or count.
type GateReport struct {
	verdict         Verdict
	receipt         *VerificationReceipt
	fixes           []FixObservation
	baseChecks      []CheckObservation
	checks          []CheckObservation
	initial         acceptance.Result
	retry           *acceptance.Result
	baseAcceptance  *acceptance.Result
	flake           *FlakeEvidence
	protection      []protection.Finding
	restoredPaths   []string
	approvedItems   []string
	changeItems     []string
	effectivePass   map[string]bool
	passedCount     int
	changeItemsPass bool
	treeStable      bool
	fixesPass       bool
	checksPass      bool
	acceptancePass  bool
	auditAdvice     []AuditAdvice
	startedAt       time.Time
	completedAt     time.Time
	homeDir         string
	tempDir         string
}

func (r *GateReport) Verdict() Verdict {
	if r == nil {
		return VerdictUnverified
	}
	return r.verdict
}

func (r *GateReport) IsVerified() bool { return r != nil && r.receipt != nil }

// IsLandable requires a verified receipt and at least one approved change item
// that passed according to the fixed gate result.
func (r *GateReport) IsLandable() bool {
	return r != nil && r.receipt != nil && r.changeItemsPass
}

func (r *GateReport) Receipt() (VerificationReceipt, bool) {
	if r == nil || r.receipt == nil {
		return VerificationReceipt{}, false
	}
	return cloneReceipt(*r.receipt), true
}

func (r *GateReport) Fixes() []FixObservation {
	if r == nil {
		return nil
	}
	return cloneFixes(r.fixes)
}

func (r *GateReport) BaseChecks() []CheckObservation {
	if r == nil {
		return nil
	}
	return cloneChecks(r.baseChecks)
}

func (r *GateReport) Checks() []CheckObservation {
	if r == nil {
		return nil
	}
	return cloneChecks(r.checks)
}

func (r *GateReport) InitialAcceptance() acceptance.Result {
	if r == nil {
		return acceptance.Result{}
	}
	return cloneAcceptance(r.initial)
}

func (r *GateReport) RetryAcceptance() (acceptance.Result, bool) {
	if r == nil || r.retry == nil {
		return acceptance.Result{}, false
	}
	return cloneAcceptance(*r.retry), true
}

func (r *GateReport) BaseAcceptance() (acceptance.Result, bool) {
	if r == nil || r.baseAcceptance == nil {
		return acceptance.Result{}, false
	}
	return cloneAcceptance(*r.baseAcceptance), true
}

func (r *GateReport) Flake() (FlakeEvidence, bool) {
	if r == nil || r.flake == nil {
		return FlakeEvidence{}, false
	}
	return cloneFlake(*r.flake), true
}

func (r *GateReport) Counts() AcceptanceCounts {
	if r == nil {
		return AcceptanceCounts{}
	}
	return AcceptanceCounts{Passed: r.passedCount, Total: len(r.approvedItems)}
}

func (r *GateReport) ProtectionFindings() []protection.Finding {
	if r == nil {
		return nil
	}
	return append([]protection.Finding(nil), r.protection...)
}

func (r *GateReport) RestoredPaths() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.restoredPaths...)
}

func (r *GateReport) AuditAdvice() []AuditAdvice {
	if r == nil {
		return nil
	}
	return append([]AuditAdvice(nil), r.auditAdvice...)
}

// RecordAuditAdvice appends observational advice without changing verified
// results, pass counts, landability, or the receipt.
func (r *GateReport) RecordAuditAdvice(items []AuditAdvice) {
	if r == nil || len(items) == 0 {
		return
	}
	r.auditAdvice = append(r.auditAdvice, items...)
}

// Run executes one gate pass. It returns an unverified report for observed
// check, fix, acceptance, or protection failures; infrastructure errors return
// an error and never produce a receipt.
func Run(ctx context.Context, request Request) (*GateReport, error) {
	if err := validateRequest(ctx, request); err != nil {
		return nil, err
	}
	started := time.Now()
	roots := request.Roots
	if roots == nil {
		roots = safefs.Opener{}
	}
	baseRoot, err := roots.OpenRoot(request.BaseWorkspace)
	if err != nil {
		return nil, fmt.Errorf("gate: open base workspace: %w", err)
	}
	defer closeRoot(baseRoot)
	candidateRoot, err := roots.OpenRoot(request.CandidateWorkspace)
	if err != nil {
		return nil, fmt.Errorf("gate: open candidate workspace: %w", err)
	}
	defer closeRoot(candidateRoot)
	runRoot, err := roots.OpenRoot(request.RunDir)
	if err != nil {
		return nil, fmt.Errorf("gate: open run directory: %w", err)
	}
	defer closeRoot(runRoot)
	if err := ensureRunRoot(runRoot); err != nil {
		return nil, err
	}
	if err := acceptance.EnsurePrivateRunDirectories(runRoot); err != nil {
		return nil, err
	}
	if err := ensureWorkspaceRoot(baseRoot, "base"); err != nil {
		return nil, err
	}
	if err := ensureWorkspaceRoot(candidateRoot, "candidate"); err != nil {
		return nil, err
	}
	baseInfo, err := baseRoot.Lstat(".")
	if err != nil {
		return nil, fmt.Errorf("gate: inspect base root identity: %w", err)
	}
	candidateInfo, err := candidateRoot.Lstat(".")
	if err != nil {
		return nil, fmt.Errorf("gate: inspect candidate root identity: %w", err)
	}
	runInfo, err := runRoot.Lstat(".")
	if err != nil {
		return nil, fmt.Errorf("gate: inspect run root identity: %w", err)
	}
	if sameRootFile(baseInfo, candidateInfo) || sameRootFile(baseInfo, runInfo) || sameRootFile(candidateInfo, runInfo) || pathOverlap(request.BaseWorkspace, request.CandidateWorkspace) || pathOverlap(request.BaseWorkspace, request.RunDir) || pathOverlap(request.CandidateWorkspace, request.RunDir) {
		return nil, fmt.Errorf("%w: base, candidate, and run roots must be separate", ErrInvalidRequest)
	}

	baseTree, err := request.Trees.Snapshot(ctx, request.BaseWorkspace)
	if err != nil {
		return nil, fmt.Errorf("gate: snapshot checked base: %w", err)
	}
	if baseTree != request.ExpectedBaseTree {
		return nil, fmt.Errorf("%w: expected %s, got %s", ErrBaseTree, request.ExpectedBaseTree, baseTree)
	}

	approvedTestPath, err := request.Acceptance.Request.Config.CandidatePath(request.Acceptance.Request.Slug)
	if err != nil {
		return nil, fmt.Errorf("gate: candidate acceptance path: %w", err)
	}
	approvedSourcePath, err := request.Acceptance.Request.Config.SourcePath(request.Acceptance.Request.Slug)
	if err != nil {
		return nil, fmt.Errorf("gate: source acceptance path: %w", err)
	}
	if err := installApprovedAcceptance(candidateRoot, approvedSourcePath, approvedTestPath, request.Acceptance.ApprovedBytes); err != nil {
		return nil, err
	}
	var restored []string
	if request.Protection != nil {
		paths, err := request.Protection.RestoreAfterBatch(candidateRoot)
		if err != nil {
			return nil, fmt.Errorf("gate: restore protected files before verification: %w", err)
		}
		restored = append(restored, paths...)
	}
	if err := ensureApprovedAcceptance(candidateRoot, approvedSourcePath, approvedTestPath, request.Acceptance.ApprovedBytes); err != nil {
		return nil, err
	}
	protectionFindings, err := guardProtection(request.Protection, candidateRoot)
	if err != nil {
		return nil, err
	}

	report := &GateReport{startedAt: started, approvedItems: append([]string(nil), request.Acceptance.Request.ExpectedItems...), changeItems: append([]string(nil), request.Acceptance.ChangeItems...), protection: protectionFindings, effectivePass: make(map[string]bool, len(request.Acceptance.Request.ExpectedItems)), homeDir: request.HomeDir, tempDir: request.TempDir}
	for i, fix := range request.Fixes {
		observation, err := runFix(ctx, request.Processes, runRoot, request.RunDir, request.CandidateWorkspace, fix, i)
		if err != nil {
			return nil, err
		}
		report.fixes = append(report.fixes, observation)
		if request.Protection != nil {
			paths, err := request.Protection.RestoreAfterBatch(candidateRoot)
			if err != nil {
				return nil, fmt.Errorf("gate: restore protected files after fix %q: %w", fix.Name, err)
			}
			restored = append(restored, paths...)
		}
	}
	if request.Protection != nil {
		guarded, err := request.Protection.Guard(candidateRoot)
		if err != nil {
			return nil, fmt.Errorf("gate: guard protected files after fixes: %w", err)
		}
		report.protection = mergeProtectionFindings(report.protection, guarded)
	}
	if err := ensureApprovedAcceptance(candidateRoot, approvedSourcePath, approvedTestPath, request.Acceptance.ApprovedBytes); err != nil {
		return nil, err
	}

	// T is captured only after every configured fix has run exactly once.
	postFixSnapshot, err := captureWorkspaceAt(roots, request.CandidateWorkspace, candidateRoot)
	if err != nil {
		return nil, fmt.Errorf("gate: capture post-fix workspace: %w", err)
	}
	verifiedTree, err := request.Trees.Snapshot(ctx, request.CandidateWorkspace)
	if err != nil {
		return nil, fmt.Errorf("gate: snapshot post-fix tree: %w", err)
	}

	baselineRows := make(map[string]BaselineCheck)
	if request.Baseline != nil && request.Baseline.Tree == baseTree {
		for _, row := range request.Baseline.Checks {
			baselineRows[row.Name] = cloneBaseline(row)
		}
	}
	for i, check := range request.Checks {
		baseObservation, err := runCheck(ctx, request, baseRoot, runRoot, request.RunDir, request.BaseWorkspace, check, "base", i)
		if err != nil {
			return nil, err
		}
		report.baseChecks = append(report.baseChecks, baseObservation)
		candidateObservation, err := runCheck(ctx, request, candidateRoot, runRoot, request.RunDir, request.CandidateWorkspace, check, "candidate", i)
		if err != nil {
			return nil, err
		}
		baseStatus := baseObservation.Status
		baseline, ok := baselineRows[check.Name]
		if !ok {
			baseline = baselineFromObservation(baseObservation)
		}
		candidateObservation.BaseStatus = baseStatus
		candidateObservation.Excused = findings.IsExcused(
			baseline.Status,
			candidateObservation.Status,
			baseline.ExitStatus,
			candidateObservation.ExitStatus,
			baseline.Findings,
			findingIdentities(candidateObservation.Findings),
		)
		report.checks = append(report.checks, candidateObservation)
		if request.Protection != nil {
			paths, err := request.Protection.RestoreAfterBatch(candidateRoot)
			if err != nil {
				return nil, fmt.Errorf("gate: restore protected files after check %q: %w", check.Name, err)
			}
			restored = append(restored, paths...)
		}
		if err := ensureApprovedAcceptance(candidateRoot, approvedSourcePath, approvedTestPath, request.Acceptance.ApprovedBytes); err != nil {
			return nil, err
		}
	}

	seed := deterministicSeed(request.ApprovalSHA256, verifiedTree)
	initial, err := runAcceptance(ctx, request, candidateRoot, request.CandidateWorkspace, "initial", seed, approvedSourcePath, approvedTestPath, request.Acceptance.ApprovedBytes)
	if err != nil {
		return nil, err
	}
	report.initial = initial
	failedItems := failedItems(request.Acceptance.Request.ExpectedItems, initial.ItemPass)
	if len(failedItems) != 0 && len(initial.Failures) == 0 && !initial.Process.TimedOut && !initial.Process.Unavailable {
		retry, retryErr := runAcceptance(ctx, request, candidateRoot, request.CandidateWorkspace, "retry", seed, approvedSourcePath, approvedTestPath, request.Acceptance.ApprovedBytes)
		if retryErr != nil {
			return nil, retryErr
		}
		report.retry = &retry
		var baseSummary *AcceptanceSummary
		var flakyIDs []string
		if noAcceptanceFailures(retry) && allItemsPass(request.Acceptance.Request.ExpectedItems, retry.ItemPass) {
			baseAcceptance, baseErr := runBaseAcceptance(ctx, request, baseRoot, request.BaseWorkspace, baseTree, "base", seed, approvedSourcePath, approvedTestPath)
			if baseErr != nil {
				return nil, baseErr
			}
			report.baseAcceptance = &baseAcceptance
			baseSummaryValue := summarizeAcceptance(baseAcceptance)
			baseSummary = &baseSummaryValue
			flakyIDs = intersectBaseRed(failedItems, baseAcceptance.ItemPass)
			if !noAcceptanceFailures(baseAcceptance) || baseAcceptance.Process.TimedOut || baseAcceptance.Process.Unavailable {
				flakyIDs = nil
			}
			if len(flakyIDs) > 2 {
				flakyIDs = flakyIDs[:2]
			}
		}
		evidence := newFlakeEvidence(seed, initial, retry, baseSummary, flakyIDs)
		if persistErr := persistFlakeEvidence(runRoot, evidence); persistErr != nil {
			evidence.StoreError = persistErr.Error()
		} else {
			evidence.Persisted = true
		}
		report.flake = &evidence
		if evidence.Persisted {
			for _, id := range evidence.ExcusedIDs {
				report.effectivePass[id] = true
			}
		}
	}
	if report.effectivePass == nil {
		report.effectivePass = make(map[string]bool, len(request.Acceptance.Request.ExpectedItems))
	}
	for _, item := range request.Acceptance.Request.ExpectedItems {
		if initial.ItemPass[item] {
			report.effectivePass[item] = true
		}
	}

	// Acceptance and checks are untrusted executables. Repair any detected tree
	// mutation back to T before deciding whether a receipt can be issued.
	currentTree, err := request.Trees.Snapshot(ctx, request.CandidateWorkspace)
	if err != nil {
		return nil, fmt.Errorf("gate: snapshot final candidate tree: %w", err)
	}
	if currentTree != verifiedTree {
		currentWorkspace, err := captureWorkspaceAt(roots, request.CandidateWorkspace, candidateRoot)
		if err != nil {
			return nil, fmt.Errorf("gate: capture candidate before final rollback: %w", err)
		}
		if err := postFixSnapshot.Restore(candidateRoot, currentWorkspace); err != nil {
			return nil, fmt.Errorf("gate: restore candidate to post-fix tree: %w", err)
		}
		restoredTree, err := request.Trees.Snapshot(ctx, request.CandidateWorkspace)
		if err != nil {
			return nil, fmt.Errorf("gate: verify restored candidate tree: %w", err)
		}
		report.treeStable = false
		if restoredTree != verifiedTree {
			return nil, fmt.Errorf("gate: final rollback did not restore the post-fix tree")
		}
	} else {
		report.treeStable = true
	}
	if request.Protection != nil {
		paths, err := request.Protection.RestoreAfterBatch(candidateRoot)
		if err != nil {
			return nil, fmt.Errorf("gate: restore protected files after acceptance: %w", err)
		}
		restored = append(restored, paths...)
		guarded, err := request.Protection.Guard(candidateRoot)
		if err != nil {
			return nil, fmt.Errorf("gate: final protected-file guard: %w", err)
		}
		report.protection = mergeProtectionFindings(report.protection, guarded)
	}
	if err := ensureApprovedAcceptance(candidateRoot, approvedSourcePath, approvedTestPath, request.Acceptance.ApprovedBytes); err != nil {
		return nil, err
	}
	report.restoredPaths = uniqueStrings(restored)

	for _, item := range request.Acceptance.Request.ExpectedItems {
		if report.effectivePass[item] {
			report.passedCount++
		}
	}
	report.fixesPass = allFixesPass(report.fixes)
	report.checksPass = allChecksPass(report.checks)
	report.acceptancePass = noAcceptanceFailures(initial) && report.passedCount == len(report.approvedItems)
	report.changeItemsPass = anyItemPass(request.Acceptance.ChangeItems, report.effectivePass)
	report.completedAt = time.Now()
	green := report.fixesPass && report.checksPass && report.acceptancePass && report.treeStable && len(report.protection) == 0
	if green {
		report.verdict = VerdictGreen
		report.receipt = &VerificationReceipt{
			BaseTree:                baseTree,
			CandidateTree:           verifiedTree,
			ApprovalSHA256:          request.ApprovalSHA256,
			ProtectedManifestSHA256: protectionDigest(request.Protection, approvedSourcePath, approvedTestPath, request.Acceptance.ApprovedBytes),
			Checks:                  cloneChecks(report.checks),
			StartedAt:               started,
			CompletedAt:             report.completedAt,
		}
	} else {
		report.verdict = VerdictUnverified
	}
	return report, nil
}

func validateRequest(ctx context.Context, request Request) error {
	if ctx == nil || request.Processes == nil || request.AcceptanceRunner == nil || request.Trees == nil {
		return fmt.Errorf("%w: context, process runner, acceptance runner, and tree snapshotter are required", ErrInvalidRequest)
	}
	for name, value := range map[string]string{"base_workspace": request.BaseWorkspace, "candidate_workspace": request.CandidateWorkspace, "run_dir": request.RunDir} {
		if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("%w: %s must be a clean absolute path", ErrInvalidRequest, name)
		}
	}
	if request.BaseWorkspace == request.CandidateWorkspace || request.RunDir == request.BaseWorkspace || request.RunDir == request.CandidateWorkspace {
		return fmt.Errorf("%w: base, candidate, and run directories must be distinct", ErrInvalidRequest)
	}
	if request.ExpectedBaseTree == "" {
		return fmt.Errorf("%w: expected base tree is required", ErrInvalidRequest)
	}
	if !validObjectID(request.ExpectedBaseTree) {
		return fmt.Errorf("%w: expected base tree is not a Git object ID", ErrInvalidRequest)
	}
	if len(request.ApprovalSHA256) != 64 || strings.ToLower(request.ApprovalSHA256) != request.ApprovalSHA256 {
		return fmt.Errorf("%w: approval hash must be a lowercase SHA-256 digest", ErrInvalidRequest)
	}
	if err := request.Acceptance.Request.Config.Validate(); err != nil {
		return fmt.Errorf("%w: acceptance config: %v", ErrInvalidRequest, err)
	}
	if request.Acceptance.Request.Slug == "" || len(request.Acceptance.ApprovedBytes) == 0 {
		return fmt.Errorf("%w: acceptance slug and approved bytes are required", ErrInvalidRequest)
	}
	items := request.Acceptance.Request.ExpectedItems
	if len(items) == 0 {
		return fmt.Errorf("%w: at least one approved acceptance item is required", ErrInvalidRequest)
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if !acceptanceItemPattern.MatchString(item) {
			return fmt.Errorf("%w: invalid acceptance item %q", ErrInvalidRequest, item)
		}
		if _, exists := seen[item]; exists {
			return fmt.Errorf("%w: duplicate acceptance item %q", ErrInvalidRequest, item)
		}
		seen[item] = struct{}{}
	}
	for _, item := range request.Acceptance.ChangeItems {
		if _, exists := seen[item]; !exists {
			return fmt.Errorf("%w: change item %q is not an approved item", ErrInvalidRequest, item)
		}
	}
	if !cleanRelative(request.Acceptance.Request.Config.CandidateDir) {
		return fmt.Errorf("%w: unsafe acceptance candidate directory", ErrInvalidRequest)
	}
	for _, command := range append(append([]contract.CheckSpec(nil), request.Fixes...), request.Checks...) {
		if command.Name == "" || command.Program == "" || command.Timeout <= 0 {
			return fmt.Errorf("%w: each fix and check needs a name, program, and positive timeout", ErrInvalidRequest)
		}
		for _, arg := range append([]string{command.Program}, command.Args...) {
			if strings.ContainsRune(arg, '\x00') || len(arg) > 4096 {
				return fmt.Errorf("%w: invalid or oversized argv value for %q", ErrInvalidRequest, command.Name)
			}
		}
	}
	if err := uniqueCommandNames(request.Fixes); err != nil {
		return err
	}
	if err := uniqueCommandNames(request.Checks); err != nil {
		return err
	}
	return nil
}

func uniqueCommandNames(commands []contract.CheckSpec) error {
	seen := make(map[string]struct{}, len(commands))
	for _, command := range commands {
		if _, exists := seen[command.Name]; exists {
			return fmt.Errorf("%w: duplicate command name %q", ErrInvalidRequest, command.Name)
		}
		seen[command.Name] = struct{}{}
	}
	return nil
}

func runFix(ctx context.Context, runner contract.ProcessRunner, runRoot contract.RootedFS, runDir, workdir string, fix contract.CheckSpec, index int) (FixObservation, error) {
	logName := fmt.Sprintf("logs/fix-%03d-%s.log", index+1, digestPrefix(fix.Name))
	if err := prepareLog(runRoot, logName); err != nil {
		return FixObservation{}, err
	}
	result, err := runner.Run(ctx, contract.ProcessSpec{Executable: fix.Program, Args: append([]string(nil), fix.Args...), Env: append([]string(nil), fix.Env...), Dir: workdir, Timeout: fix.Timeout, LogPath: filepath.Join(runDir, filepath.FromSlash(logName))})
	if err != nil {
		return FixObservation{}, fmt.Errorf("gate: run fix %q: %w", fix.Name, err)
	}
	return FixObservation{Spec: fix, ExitStatus: cloneInt(result.ExitStatus), TimedOut: result.TimedOut, Unavailable: result.Unavailable || result.ExitStatus == nil || unavailableExit(result.ExitStatus), OutputTail: append([]byte(nil), result.OutputTail...), LogPath: result.LogPath}, nil
}

func runCheck(ctx context.Context, request Request, root, runRoot contract.RootedFS, runDir, workdir string, check contract.CheckSpec, side string, index int) (CheckObservation, error) {
	beforeTree, err := request.Trees.Snapshot(ctx, workdir)
	if err != nil {
		return CheckObservation{}, fmt.Errorf("gate: snapshot %s tree before check %q: %w", side, check.Name, err)
	}
	beforeWorkspace, err := captureWorkspaceAt(request.Roots, workdir, root)
	if err != nil {
		return CheckObservation{}, fmt.Errorf("gate: capture %s workspace before check %q: %w", side, check.Name, err)
	}
	logName := fmt.Sprintf("logs/check-%s-%03d-%s.log", side, index+1, digestPrefix(check.Name))
	if err := prepareLog(runRoot, logName); err != nil {
		return CheckObservation{}, err
	}
	processResult, runErr := request.Processes.Run(ctx, contract.ProcessSpec{Executable: check.Program, Args: append([]string(nil), check.Args...), Env: append([]string(nil), check.Env...), Dir: workdir, Timeout: check.Timeout, LogPath: filepath.Join(runDir, filepath.FromSlash(logName))})
	afterTree, treeErr := request.Trees.Snapshot(ctx, workdir)
	afterWorkspace, workspaceErr := captureWorkspaceAt(request.Roots, workdir, root)
	if treeErr != nil || workspaceErr != nil {
		if afterWorkspace != nil {
			_ = beforeWorkspace.Restore(root, afterWorkspace)
		}
		return CheckObservation{}, errors.Join(runErr, fmt.Errorf("gate: observe %s check %q after running: %w", side, check.Name, errors.Join(treeErr, workspaceErr)))
	}
	changedPaths := beforeWorkspace.ChangedPaths(afterWorkspace)
	mutated := beforeTree != afterTree || len(changedPaths) != 0
	if mutated {
		if err := beforeWorkspace.Restore(root, afterWorkspace); err != nil {
			return CheckObservation{}, errors.Join(runErr, fmt.Errorf("gate: rollback mutating %s check %q: %w", side, check.Name, err))
		}
		restored, err := request.Trees.Snapshot(ctx, workdir)
		if err != nil {
			return CheckObservation{}, errors.Join(runErr, fmt.Errorf("gate: verify rollback of %s check %q: %w", side, check.Name, err))
		}
		if restored != beforeTree {
			afterRestore, captureErr := captureWorkspaceAt(request.Roots, workdir, root)
			changed := beforeWorkspace.ChangedPaths(afterRestore)
			return CheckObservation{}, errors.Join(runErr, fmt.Errorf("gate: rollback of %s check %q did not restore the original tree: got %s, want %s; workspace entries=%d/%d differences=%v capture=%v", side, check.Name, restored, beforeTree, len(beforeWorkspace.entries), len(afterRestore.entries), changed, captureErr))
		}
	}
	if runErr != nil {
		return CheckObservation{}, fmt.Errorf("gate: run %s check %q: %w", side, check.Name, runErr)
	}
	logBytes, err := readProcessLog(runRoot, logName, processResult.OutputTail)
	if err != nil {
		return CheckObservation{}, err
	}
	status := classifyCheck(processResult, mutated)
	if status == contract.CheckMutating {
		mutated = true
	}
	return CheckObservation{
		Spec: check, Status: status, ExitStatus: cloneInt(processResult.ExitStatus), TimedOut: processResult.TimedOut,
		Unavailable: processResult.Unavailable || processResult.ExitStatus == nil || unavailableExit(processResult.ExitStatus), Findings: findings.ParseGNU(logBytes),
		OutputTail: append([]byte(nil), processResult.OutputTail...), LogPath: processResult.LogPath,
		TreeBefore: beforeTree, TreeAfter: afterTree, ChangedPaths: changedPaths,
	}, nil
}

func runAcceptance(ctx context.Context, request Request, candidateRoot contract.RootedFS, workdir, attempt, seed, sourcePath, candidatePath string, approved []byte) (acceptance.Result, error) {
	if err := requestRootForRunDir(request, "acceptance/"+attempt); err != nil {
		return acceptance.Result{}, err
	}
	workspace, err := captureWorkspaceAt(request.Roots, workdir, candidateRoot)
	if err != nil {
		return acceptance.Result{}, fmt.Errorf("gate: capture candidate before acceptance %s: %w", attempt, err)
	}
	before, err := request.Trees.Snapshot(ctx, workdir)
	if err != nil {
		return acceptance.Result{}, fmt.Errorf("gate: snapshot candidate before acceptance %s: %w", attempt, err)
	}
	if err := installApprovedAcceptance(candidateRoot, sourcePath, candidatePath, approved); err != nil {
		return acceptance.Result{}, err
	}
	if request.Protection != nil {
		if _, err := request.Protection.RestoreAfterBatch(candidateRoot); err != nil {
			return acceptance.Result{}, fmt.Errorf("gate: restore protected files before acceptance %s: %w", attempt, err)
		}
	}
	if err := ensureApprovedAcceptance(candidateRoot, sourcePath, candidatePath, approved); err != nil {
		return acceptance.Result{}, err
	}
	commandRequest := request.Acceptance.Request
	commandRequest.Workdir = workdir
	commandRequest.RunDir = filepath.Join(request.RunDir, "acceptance", attempt)
	commandRequest.ReportPath = ""
	result, runErr := request.AcceptanceRunner.Run(ctx, AcceptanceExecution{Request: commandRequest, Seed: seed, Attempt: attempt})
	after, treeErr := request.Trees.Snapshot(ctx, workdir)
	afterWorkspace, workspaceErr := captureWorkspaceAt(request.Roots, workdir, candidateRoot)
	if treeErr != nil || workspaceErr != nil {
		if afterWorkspace != nil {
			_ = workspace.Restore(candidateRoot, afterWorkspace)
		}
		return acceptance.Result{}, errors.Join(runErr, fmt.Errorf("gate: snapshot candidate after acceptance %s: %w", attempt, errors.Join(treeErr, workspaceErr)))
	}
	changedPaths := workspace.ChangedPaths(afterWorkspace)
	if after != before || len(changedPaths) != 0 {
		if err := workspace.Restore(candidateRoot, afterWorkspace); err != nil {
			return acceptance.Result{}, errors.Join(runErr, fmt.Errorf("gate: rollback mutating acceptance %s: %w", attempt, err))
		}
		restored, err := request.Trees.Snapshot(ctx, workdir)
		if err != nil || restored != before {
			return acceptance.Result{}, errors.Join(runErr, fmt.Errorf("gate: acceptance rollback %s failed to restore the tree: %v", attempt, err))
		}
		appendAcceptanceFailure(&result, acceptance.Failure{Kind: acceptance.FailureTreeMutated})
	}
	if request.Protection != nil {
		if _, err := request.Protection.RestoreAfterBatch(candidateRoot); err != nil {
			return acceptance.Result{}, errors.Join(runErr, fmt.Errorf("gate: restore protected files after acceptance %s: %w", attempt, err))
		}
	}
	if err := ensureApprovedAcceptance(candidateRoot, sourcePath, candidatePath, approved); err != nil {
		return result, err
	}
	if runErr != nil {
		return result, fmt.Errorf("gate: run acceptance %s: %w", attempt, runErr)
	}
	return result, nil
}

func runBaseAcceptance(ctx context.Context, request Request, baseRoot contract.RootedFS, workdir, baseTree, attempt, seed, sourcePath, candidatePath string) (acceptance.Result, error) {
	if err := requestRootForRunDir(request, "acceptance/"+attempt); err != nil {
		return acceptance.Result{}, err
	}
	workspace, err := captureWorkspaceAt(request.Roots, workdir, baseRoot)
	if err != nil {
		return acceptance.Result{}, fmt.Errorf("gate: capture base before flake check: %w", err)
	}
	if err := installApprovedAcceptance(baseRoot, sourcePath, candidatePath, request.Acceptance.ApprovedBytes); err != nil {
		return acceptance.Result{}, err
	}
	commandRequest := request.Acceptance.Request
	commandRequest.Workdir = workdir
	commandRequest.RunDir = filepath.Join(request.RunDir, "acceptance", attempt)
	commandRequest.ReportPath = ""
	result, runErr := request.AcceptanceRunner.Run(ctx, AcceptanceExecution{Request: commandRequest, Seed: seed, Attempt: attempt})
	currentWorkspace, captureErr := captureWorkspaceAt(request.Roots, workdir, baseRoot)
	if captureErr != nil {
		return acceptance.Result{}, errors.Join(runErr, fmt.Errorf("gate: capture checked base after flake evidence run: %w", captureErr))
	}
	if err := workspace.Restore(baseRoot, currentWorkspace); err != nil {
		return acceptance.Result{}, errors.Join(runErr, fmt.Errorf("gate: restore checked base after flake evidence run: %w", err))
	}
	restored, treeErr := request.Trees.Snapshot(ctx, workdir)
	if treeErr != nil || restored != baseTree {
		afterRestore, captureErr := captureWorkspaceAt(request.Roots, workdir, baseRoot)
		changed := workspace.ChangedPaths(afterRestore)
		return acceptance.Result{}, errors.Join(runErr, fmt.Errorf("gate: checked base changed during flake evidence run (tree %s, want %s): %v; workspace entries=%d/%d differences=%v capture=%v", restored, baseTree, treeErr, len(workspace.entries), len(afterRestore.entries), changed, captureErr))
	}
	if runErr != nil {
		return result, fmt.Errorf("gate: run base acceptance for flake evidence: %w", runErr)
	}
	return result, nil
}

func requestRootForRunDir(request Request, relative string) error {
	roots := request.Roots
	if roots == nil {
		roots = safefs.Opener{}
	}
	root, err := roots.OpenRoot(request.RunDir)
	if err != nil {
		return fmt.Errorf("gate: open run root for acceptance: %w", err)
	}
	defer closeRoot(root)
	if err := root.MkdirAll(relative, 0o700); err != nil {
		return fmt.Errorf("gate: create private acceptance run directory: %w", err)
	}
	info, err := root.Lstat(relative)
	if err != nil {
		return fmt.Errorf("gate: inspect private acceptance run directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("gate: acceptance run directory must be private and real")
	}
	return nil
}

func installApprovedAcceptance(root contract.RootedFS, sourcePath, candidatePath string, approved []byte) error {
	if !cleanRelative(sourcePath) || !cleanRelative(candidatePath) || sourcePath == candidatePath {
		return fmt.Errorf("%w: unsafe acceptance source or candidate path", ErrInvalidRequest)
	}
	if err := root.MkdirAll(path.Dir(candidatePath), 0o755); err != nil {
		return fmt.Errorf("gate: create candidate acceptance directory: %w", err)
	}
	if info, err := root.Lstat(candidatePath); err == nil {
		if info.IsDir() {
			return fmt.Errorf("gate: candidate acceptance path is a directory")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("gate: inspect candidate acceptance path: %w", err)
	}
	if err := root.Publish(candidatePath, approved, 0o644, safefs.PublicationReplace); err != nil {
		return fmt.Errorf("gate: install approved acceptance bytes: %w", err)
	}
	if info, err := root.Lstat(sourcePath); err == nil {
		if info.IsDir() {
			return fmt.Errorf("gate: acceptance source path is a directory")
		}
		if err := root.Remove(sourcePath); err != nil {
			return fmt.Errorf("gate: remove acceptance source copy: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("gate: inspect acceptance source copy: %w", err)
	}
	return nil
}

func ensureApprovedAcceptance(root contract.RootedFS, sourcePath, candidatePath string, approved []byte) error {
	info, err := root.Lstat(candidatePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("gate: approved candidate acceptance file is missing or unsafe: %v", err)
	}
	contents, err := root.ReadFile(candidatePath)
	if err != nil {
		return fmt.Errorf("gate: read approved candidate acceptance file: %w", err)
	}
	if !bytes.Equal(contents, approved) {
		return fmt.Errorf("gate: candidate acceptance bytes differ from the approved bytes")
	}
	if _, err := root.Lstat(sourcePath); err == nil {
		return fmt.Errorf("gate: acceptance source copy must remain absent")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("gate: inspect removed acceptance source: %w", err)
	}
	return nil
}

func prepareLog(root contract.RootedFS, relative string) error {
	if err := root.MkdirAll(path.Dir(relative), 0o700); err != nil {
		return fmt.Errorf("gate: prepare process log directory: %w", err)
	}
	if _, err := root.Lstat(relative); err == nil {
		if err := root.Remove(relative); err != nil {
			return fmt.Errorf("gate: remove prior process log: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("gate: inspect process log: %w", err)
	}
	return nil
}

func readProcessLog(root contract.RootedFS, relative string, fallback []byte) ([]byte, error) {
	info, err := root.Lstat(relative)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return append([]byte(nil), fallback...), nil
		}
		return nil, fmt.Errorf("gate: inspect process log: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("gate: process log is not a private regular file")
	}
	data, err := root.ReadFile(relative)
	if err != nil {
		return nil, fmt.Errorf("gate: read process log: %w", err)
	}
	return data, nil
}

func classifyCheck(result contract.ProcessResult, mutated bool) contract.CheckStatus {
	switch {
	case mutated:
		return contract.CheckMutating
	case result.TimedOut:
		return contract.CheckTimeout
	case result.Unavailable || result.ExitStatus == nil || unavailableExit(result.ExitStatus):
		return contract.CheckUnavailable
	case result.ExitStatus == nil || *result.ExitStatus != 0:
		return contract.CheckRed
	default:
		return contract.CheckGreen
	}
}

func baselineFromObservation(observation CheckObservation) BaselineCheck {
	return BaselineCheck{Name: observation.Spec.Name, Status: observation.Status, ExitStatus: cloneInt(observation.ExitStatus), Findings: findingIdentities(observation.Findings)}
}

func findingIdentities(items []findings.Finding) []contract.FindingIdentity {
	return findings.Identities(items)
}

func failedItems(expected []string, itemPass map[string]bool) []string {
	failed := make([]string, 0)
	for _, item := range expected {
		if !itemPass[item] {
			failed = append(failed, item)
		}
	}
	sort.Strings(failed)
	return failed
}

func intersectBaseRed(candidateFailed []string, basePass map[string]bool) []string {
	ids := make([]string, 0)
	for _, id := range candidateFailed {
		if !basePass[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func noAcceptanceFailures(result acceptance.Result) bool { return len(result.Failures) == 0 }

func allItemsPass(expected []string, itemPass map[string]bool) bool {
	if len(expected) == 0 {
		return false
	}
	for _, item := range expected {
		if !itemPass[item] {
			return false
		}
	}
	return true
}

func allFixesPass(fixes []FixObservation) bool {
	for _, fix := range fixes {
		if fix.TimedOut || fix.Unavailable || fix.ExitStatus == nil || *fix.ExitStatus != 0 {
			return false
		}
	}
	return true
}

func allChecksPass(checks []CheckObservation) bool {
	for _, check := range checks {
		if check.Status != contract.CheckGreen && !check.Excused {
			return false
		}
	}
	return true
}

func anyItemPass(items []string, pass map[string]bool) bool {
	for _, item := range items {
		if pass[item] {
			return true
		}
	}
	return false
}

func appendAcceptanceFailure(result *acceptance.Result, failure acceptance.Failure) {
	if result == nil {
		return
	}
	for _, existing := range result.Failures {
		if existing.Kind == failure.Kind {
			return
		}
	}
	result.Failures = append(result.Failures, failure)
}

func newFlakeEvidence(seed string, initial, retry acceptance.Result, base *AcceptanceSummary, ids []string) FlakeEvidence {
	var copyBase *AcceptanceSummary
	if base != nil {
		cloned := cloneSummary(*base)
		copyBase = &cloned
	}
	return FlakeEvidence{Seed: seed, Initial: summarizeAcceptance(initial), Retry: summarizeAcceptance(retry), Base: copyBase, ExcusedIDs: append([]string(nil), ids...)}
}

func summarizeAcceptance(result acceptance.Result) AcceptanceSummary {
	return AcceptanceSummary{ExitStatus: cloneInt(result.Process.ExitStatus), TimedOut: result.Process.TimedOut, Unavailable: result.Process.Unavailable || result.Process.ExitStatus == nil || unavailableExit(result.Process.ExitStatus), Failures: append([]acceptance.Failure(nil), result.Failures...), ItemPass: cloneBoolMap(result.ItemPass), TreeBefore: result.TreeBefore, TreeAfter: result.TreeAfter}
}

func persistFlakeEvidence(root contract.RootedFS, evidence FlakeEvidence) error {
	contents, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	return root.Publish("flake-evidence.json", contents, 0o600, safefs.PublicationCreateOnly)
}

func guardProtection(protector *protection.Protector, root contract.RootedFS) ([]protection.Finding, error) {
	if protector == nil {
		return nil, nil
	}
	findings, err := protector.Guard(root)
	if err != nil {
		return nil, fmt.Errorf("gate: check protected files: %w", err)
	}
	return findings, nil
}

func mergeProtectionFindings(left, right []protection.Finding) []protection.Finding {
	byPath := make(map[string]protection.Finding, len(left)+len(right))
	for _, item := range left {
		byPath[item.Path] = item
	}
	for _, item := range right {
		byPath[item.Path] = item
	}
	result := make([]protection.Finding, 0, len(byPath))
	for _, item := range byPath {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result
}

func protectionDigest(protector *protection.Protector, sourcePath, candidatePath string, approved []byte) string {
	manifest := make(protection.Manifest)
	if protector != nil {
		for name, entry := range protector.Manifest() {
			manifest[name] = entry
		}
	}
	approvedHash := sha256.Sum256(approved)
	manifest[sourcePath] = protection.Entry{SHA256: protection.AbsentSHA256}
	manifest[candidatePath] = protection.Entry{SHA256: hex.EncodeToString(approvedHash[:]), Mode: 0o100644, Present: true}
	keys := make([]string, 0, len(manifest))
	for name := range manifest {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	hash := sha256.New()
	for _, name := range keys {
		entry := manifest[name]
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%o\x00%t\x00", name, entry.SHA256, entry.Mode, entry.Present)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func ensureRunRoot(root contract.RootedFS) error {
	info, err := root.Lstat(".")
	if err != nil {
		return fmt.Errorf("gate: inspect run root: %w", err)
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("gate: run directory must be a private real directory")
	}
	return nil
}

func ensureWorkspaceRoot(root contract.RootedFS, name string) error {
	info, err := root.Lstat(".")
	if err != nil {
		return fmt.Errorf("gate: inspect %s root: %w", name, err)
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("gate: %s workspace must be a real directory", name)
	}
	return nil
}

func closeRoot(root contract.RootedFS) {
	if closer, ok := root.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}

func cloneReceipt(receipt VerificationReceipt) VerificationReceipt {
	receipt.Checks = cloneChecks(receipt.Checks)
	return receipt
}

func cloneChecks(items []CheckObservation) []CheckObservation {
	result := make([]CheckObservation, len(items))
	for index, item := range items {
		item.Spec.Args = append([]string(nil), item.Spec.Args...)
		item.Spec.Env = append([]string(nil), item.Spec.Env...)
		item.ExitStatus = cloneInt(item.ExitStatus)
		item.Findings = append([]findings.Finding(nil), item.Findings...)
		item.OutputTail = append([]byte(nil), item.OutputTail...)
		item.ChangedPaths = append([]string(nil), item.ChangedPaths...)
		result[index] = item
	}
	return result
}

func cloneFixes(items []FixObservation) []FixObservation {
	result := make([]FixObservation, len(items))
	for index, item := range items {
		item.Spec.Args = append([]string(nil), item.Spec.Args...)
		item.Spec.Env = append([]string(nil), item.Spec.Env...)
		item.ExitStatus = cloneInt(item.ExitStatus)
		item.OutputTail = append([]byte(nil), item.OutputTail...)
		result[index] = item
	}
	return result
}

func cloneAcceptance(result acceptance.Result) acceptance.Result {
	result.Process.ExitStatus = cloneInt(result.Process.ExitStatus)
	result.Process.OutputTail = append([]byte(nil), result.Process.OutputTail...)
	result.Rows = append([]acceptance.LedgerRow(nil), result.Rows...)
	result.Failures = append([]acceptance.Failure(nil), result.Failures...)
	result.ItemPass = cloneBoolMap(result.ItemPass)
	return result
}

func cloneFlake(evidence FlakeEvidence) FlakeEvidence {
	evidence.Initial = cloneSummary(evidence.Initial)
	evidence.Retry = cloneSummary(evidence.Retry)
	if evidence.Base != nil {
		base := cloneSummary(*evidence.Base)
		evidence.Base = &base
	}
	evidence.ExcusedIDs = append([]string(nil), evidence.ExcusedIDs...)
	return evidence
}

func cloneSummary(summary AcceptanceSummary) AcceptanceSummary {
	summary.ExitStatus = cloneInt(summary.ExitStatus)
	summary.Failures = append([]acceptance.Failure(nil), summary.Failures...)
	summary.ItemPass = cloneBoolMap(summary.ItemPass)
	return summary
}

func cloneBaseline(row BaselineCheck) BaselineCheck {
	row.ExitStatus = cloneInt(row.ExitStatus)
	row.Findings = append([]contract.FindingIdentity(nil), row.Findings...)
	return row
}

func cloneBoolMap(input map[string]bool) map[string]bool {
	if input == nil {
		return nil
	}
	output := make(map[string]bool, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneInt(input *int) *int {
	if input == nil {
		return nil
	}
	value := *input
	return &value
}

func unavailableExit(status *int) bool { return status != nil && (*status == 126 || *status == 127) }

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func pathOverlap(left, right string) bool {
	within := func(parent, child string) bool {
		relative, err := filepath.Rel(parent, child)
		return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
	}
	return within(left, right) || within(right, left)
}

func digestPrefix(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:6])
}

func deterministicSeed(approvalSHA256, candidateTree string) string {
	digest := sha256.Sum256([]byte(approvalSHA256 + "\x00" + candidateTree))
	return fmt.Sprintf("%d", uint64(binary.BigEndian.Uint32(digest[:4]))+1)
}

func cleanRelative(value string) bool {
	if value == "" || path.IsAbs(value) || path.Clean(value) != value || !fs.ValidPath(value) || strings.Contains(value, "\\") || strings.ContainsRune(value, '\x00') {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".git" || part == ".." {
			return false
		}
	}
	return true
}

func uniqueStrings(items []string) []string {
	set := make(map[string]struct{}, len(items))
	for _, item := range items {
		set[item] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for item := range set {
		result = append(result, item)
	}
	sort.Strings(result)
	return result
}

var acceptanceItemPattern = regexp.MustCompile(`^A[1-9][0-9]*$`)
