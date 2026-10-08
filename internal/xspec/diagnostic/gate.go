package diagnostic

import (
	"time"

	"kogen-go/internal/acceptance"
	"kogen-go/internal/contract"
	"kogen-go/internal/findings"
	"kogen-go/internal/gate"
)

// GateObservation contains the behavioral facts exposed by one production
// GateReport. It omits process tails, log paths, and check environment values;
// those are execution details and can contain local paths or credentials.
type GateObservation struct {
	Verdict            gate.Verdict           `json:"verdict"`
	Verified           bool                   `json:"verified"`
	Landable           bool                   `json:"landable"`
	Counts             GateCounts             `json:"counts"`
	Receipt            *GateReceipt           `json:"receipt"`
	Fixes              []GateFix              `json:"fixes"`
	BaseChecks         []GateCheck            `json:"base_checks"`
	Checks             []GateCheck            `json:"checks"`
	InitialAcceptance  AcceptanceObservation  `json:"initial_acceptance"`
	RetryAcceptance    *AcceptanceObservation `json:"retry_acceptance"`
	BaseAcceptance     *AcceptanceObservation `json:"base_acceptance"`
	Flake              *gate.FlakeEvidence    `json:"flake,omitempty"`
	ProtectionFindings []ProtectedFinding     `json:"protection_findings"`
	RestoredPaths      []string               `json:"restored_paths"`
	AuditAdvice        []AuditAdvice          `json:"audit_advice"`
}

type GateCounts struct {
	Passed int `json:"passed"`
	Total  int `json:"total"`
}

type GateReceipt struct {
	BaseTree                string      `json:"base_tree"`
	CandidateTree           string      `json:"candidate_tree"`
	ApprovalSHA256          string      `json:"approval_sha256,omitempty"`
	ProtectedManifestSHA256 string      `json:"protected_manifest_sha256,omitempty"`
	Checks                  []GateCheck `json:"checks"`
	StartedAt               time.Time   `json:"started_at"`
	CompletedAt             time.Time   `json:"completed_at"`
}

type GateFix struct {
	Name        string `json:"name"`
	ExitStatus  *int   `json:"exit_status"`
	TimedOut    bool   `json:"timed_out"`
	Unavailable bool   `json:"unavailable"`
}

type GateCheck struct {
	Name         string               `json:"name"`
	Status       contract.CheckStatus `json:"status"`
	ExitStatus   *int                 `json:"exit_status"`
	TimedOut     bool                 `json:"timed_out"`
	Unavailable  bool                 `json:"unavailable"`
	Findings     []findings.Finding   `json:"findings"`
	TreeBefore   string               `json:"tree_before"`
	TreeAfter    string               `json:"tree_after"`
	ChangedPaths []string             `json:"changed_paths"`
	Excused      bool                 `json:"excused"`
	BaseStatus   contract.CheckStatus `json:"base_status"`
}

type AcceptanceObservation struct {
	ExitStatus  *int                   `json:"exit_status"`
	TimedOut    bool                   `json:"timed_out"`
	Unavailable bool                   `json:"unavailable"`
	Rows        []acceptance.LedgerRow `json:"rows"`
	ItemPass    map[string]bool        `json:"item_pass"`
	Failures    []AcceptanceFailure    `json:"failures"`
	TreeBefore  string                 `json:"tree_before"`
	TreeAfter   string                 `json:"tree_after"`
}

type AcceptanceFailure struct {
	Kind   acceptance.FailureKind `json:"kind"`
	Line   int                    `json:"line"`
	Detail string                 `json:"detail,omitempty"`
}

type ProtectedFinding struct {
	Path           string `json:"path"`
	ExpectedSHA256 string `json:"expected_sha256"`
	ActualSHA256   string `json:"actual_sha256"`
}

type AuditAdvice struct {
	ID      string `json:"id"`
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

// ObserveGate maps values from the production GateReport accessors. The
// adapter does not decide a verdict, excusal, item count, or landing result.
func ObserveGate(report *gate.GateReport) (GateObservation, error) {
	if report == nil {
		return GateObservation{}, errNilProductionResult("gate report")
	}
	counts := report.Counts()
	observation := GateObservation{
		Verdict: report.Verdict(), Verified: report.IsVerified(), Landable: report.IsLandable(),
		Counts: GateCounts{Passed: counts.Passed, Total: counts.Total},
		Fixes:  make([]GateFix, 0), BaseChecks: make([]GateCheck, 0), Checks: make([]GateCheck, 0),
		ProtectionFindings: make([]ProtectedFinding, 0), RestoredPaths: report.RestoredPaths(),
		AuditAdvice: make([]AuditAdvice, 0),
	}
	if observation.RestoredPaths == nil {
		observation.RestoredPaths = []string{}
	}
	if receipt, ok := report.Receipt(); ok {
		observation.Receipt = &GateReceipt{
			BaseTree: receipt.BaseTree, CandidateTree: receipt.CandidateTree,
			ApprovalSHA256: receipt.ApprovalSHA256, ProtectedManifestSHA256: receipt.ProtectedManifestSHA256,
			Checks: checksObservation(receipt.Checks), StartedAt: receipt.StartedAt,
			CompletedAt: receipt.CompletedAt,
		}
	}
	for _, fix := range report.Fixes() {
		observation.Fixes = append(observation.Fixes, GateFix{
			Name: fix.Spec.Name, ExitStatus: cloneInt(fix.ExitStatus),
			TimedOut: fix.TimedOut, Unavailable: fix.Unavailable,
		})
	}
	observation.BaseChecks = checksObservation(report.BaseChecks())
	observation.Checks = checksObservation(report.Checks())
	observation.InitialAcceptance = acceptanceObservation(report.InitialAcceptance())
	if retry, ok := report.RetryAcceptance(); ok {
		mapped := acceptanceObservation(retry)
		observation.RetryAcceptance = &mapped
	}
	if base, ok := report.BaseAcceptance(); ok {
		mapped := acceptanceObservation(base)
		observation.BaseAcceptance = &mapped
	}
	if flake, ok := report.Flake(); ok {
		observation.Flake = &flake
	}
	for _, finding := range report.ProtectionFindings() {
		observation.ProtectionFindings = append(observation.ProtectionFindings, ProtectedFinding{
			Path: finding.Path, ExpectedSHA256: finding.ExpectedSHA256, ActualSHA256: finding.ActualSHA256,
		})
	}
	for _, advice := range report.AuditAdvice() {
		observation.AuditAdvice = append(observation.AuditAdvice, AuditAdvice{
			ID: advice.ID, Verdict: advice.Verdict, Reason: advice.Reason,
		})
	}
	return observation, nil
}

func checksObservation(checks []gate.CheckObservation) []GateCheck {
	result := make([]GateCheck, 0, len(checks))
	for _, check := range checks {
		findingsCopy := append([]findings.Finding(nil), check.Findings...)
		if findingsCopy == nil {
			findingsCopy = []findings.Finding{}
		}
		paths := append([]string(nil), check.ChangedPaths...)
		if paths == nil {
			paths = []string{}
		}
		result = append(result, GateCheck{
			Name: check.Spec.Name, Status: check.Status, ExitStatus: cloneInt(check.ExitStatus),
			TimedOut: check.TimedOut, Unavailable: check.Unavailable, Findings: findingsCopy,
			TreeBefore: check.TreeBefore, TreeAfter: check.TreeAfter, ChangedPaths: paths,
			Excused: check.Excused, BaseStatus: check.BaseStatus,
		})
	}
	return result
}

func acceptanceObservation(result acceptance.Result) AcceptanceObservation {
	rows := append([]acceptance.LedgerRow(nil), result.Rows...)
	if rows == nil {
		rows = []acceptance.LedgerRow{}
	}
	itemPass := make(map[string]bool, len(result.ItemPass))
	for item, passed := range result.ItemPass {
		itemPass[item] = passed
	}
	failures := make([]AcceptanceFailure, 0, len(result.Failures))
	for _, failure := range result.Failures {
		failures = append(failures, AcceptanceFailure{Kind: failure.Kind, Line: failure.Line, Detail: failure.Detail})
	}
	return AcceptanceObservation{
		ExitStatus: cloneInt(result.Process.ExitStatus), TimedOut: result.Process.TimedOut,
		Unavailable: result.Process.Unavailable, Rows: rows, ItemPass: itemPass,
		Failures: failures, TreeBefore: result.TreeBefore, TreeAfter: result.TreeAfter,
	}
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
