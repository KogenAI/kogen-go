package journal

import (
	"fmt"
)

// AgentEvent is deliberately limited to lifecycle metadata. Tool arguments,
// tool output, conversation text, and filesystem paths do not belong here.
type AgentEvent struct {
	Event  string `json:"event"`
	TS     int64  `json:"ts"`
	Role   string `json:"role"`
	Status string `json:"status,omitempty"`
}

var agentRoles = map[string]struct{}{
	"builder": {}, "context": {}, "planner": {}, "reviewer": {}, "auditor": {},
	"shaper": {}, "fallback_shaper": {}, "agent": {},
}

var agentLifecycle = map[string]string{
	"started": "running", "agent_started": "running",
	"activity": "running", "progress": "running", "tool_started": "running",
	"tool_finished": "running",
	"waiting":       "waiting", "agent_waiting": "waiting",
	"finished": "complete", "agent_finished": "complete",
	"failed": "failed", "cancelled": "cancelled", "stopped": "stopped",
}

func (e AgentEvent) Validate() error {
	if _, ok := agentRoles[e.Role]; !ok {
		return fmt.Errorf("%w: invalid agent role", ErrInvalidRecord)
	}
	expectedStatus, ok := agentLifecycle[e.Event]
	if !ok || (e.Status != "" && e.Status != expectedStatus) {
		return fmt.Errorf("%w: invalid agent lifecycle event", ErrInvalidRecord)
	}
	return nil
}
