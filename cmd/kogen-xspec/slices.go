package main

import (
	"fmt"

	"kogen-go/internal/xspec/intentapprove"
	"kogen-go/internal/xspec/protocol"
	"kogen-go/internal/xspec/queuestatus"
	"kogen-go/internal/xspec/streamsession"
)

// requiredGSlices is the release replay set. D slices stay in a separate
// diagnostic lane and are never registered as counted G observations.
var requiredGSlices = []string{
	"intent", "approve", "queue", "status", "recovery", "rebase", "stream", "session",
}

// diagnosticSlices are reported independently by the production observation
// adapters in internal/xspec/diagnostic.
var diagnosticSlices = []string{"gate", "orchestration", "accounts", "setup-cache"}

// recovery and rebase currently have real-I/O component fixtures but no
// production event adapter that can expose the full migrated Quint
// observation. Refuse them explicitly instead of replaying a copied policy
// machine or counting an incomplete observation as a pass.
var unavailableGSlices = map[string]string{
	"recovery": "recovery replay is unavailable: the production event adapter for the migrated v1.3 schema is not wired",
	"rebase":   "rebase replay is unavailable: the production event adapter for the migrated v1.3 schema is not wired",
}

func xspecFactories() protocol.Registry {
	registry := make(protocol.Registry)
	for _, source := range []protocol.Registry{
		intentapprove.Factories(),
		queuestatus.Factories(),
		{
			"stream":  streamsession.StreamFactory,
			"session": streamsession.SessionFactory,
		},
	} {
		for name, factory := range source {
			if _, exists := registry[name]; exists {
				panic(fmt.Sprintf("duplicate xspec factory %q", name))
			}
			registry[name] = factory
		}
	}
	return registry
}
