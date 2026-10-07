package journal

import (
	"fmt"
)

// AuditItem is advice from the observational Build auditor. It does not carry
// any field capable of changing verification, ranking, or landing eligibility.
type AuditItem struct {
	ID      string `json:"id"`
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

// AuditEvent constructs the v1.3 observational audit receipt. The result always
// states that no item was demoted and the advisory item set is empty.
func AuditEvent(ts int64, rung string, items []AuditItem) (RunEvent, error) {
	if rung != "" && !telemetryTokenPattern.MatchString(rung) {
		return RunEvent{}, fmt.Errorf("%w: invalid audit rung", ErrInvalidRecord)
	}
	for _, item := range items {
		if item.ID == "" || (item.Verdict != "valid" && item.Verdict != "over_strict" && item.Verdict != "contradicts") {
			return RunEvent{}, fmt.Errorf("%w: invalid audit item", ErrInvalidRecord)
		}
	}
	if items == nil {
		items = []AuditItem{}
	}
	event := NewRunEvent("audit", ts)
	for key, value := range map[string]any{
		"mode": "observational", "rung": rung, "items": items,
		"demoted": false, "advisory_items": []string{},
	} {
		if err := event.Set(key, value); err != nil {
			return RunEvent{}, err
		}
	}
	return event, nil
}

// RecoveryPreservedEvent records a successful durable preservation. The
// snapshot must separately include the same record before cleanup begins.
func RecoveryPreservedEvent(ts int64, recovery RecoveryRecord) (RunEvent, error) {
	if err := recovery.Validate(); err != nil {
		return RunEvent{}, err
	}
	event := NewRunEvent("recovery_preserved", ts)
	for key, value := range map[string]any{
		"workspace": recovery.Workspace, "base": recovery.Base,
		"tree": recovery.Tree, "ref": recovery.Ref, "archive": recovery.Archive,
		"verification": "unverified",
	} {
		if err := event.Set(key, value); err != nil {
			return RunEvent{}, err
		}
	}
	return event, nil
}

// CleanupFailureEvent names the workspace and reason that prevented cleanup.
// Callers set RunSnapshot.CleanupPending before recording this event.
func CleanupFailureEvent(ts int64, workspace string, detail []byte) (RunEvent, error) {
	if workspace == "" {
		return RunEvent{}, fmt.Errorf("%w: cleanup failure requires a workspace", ErrInvalidRecord)
	}
	event := NewRunEvent("cleanup_failure", ts)
	if err := event.Set("workspace", workspace); err != nil {
		return RunEvent{}, err
	}
	if err := event.SetText("detail", detail); err != nil {
		return RunEvent{}, err
	}
	return event, nil
}
