package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"kogen-go/internal/xspec/diagnostic"
	"kogen-go/internal/xspec/protocol"
)

func TestXspecRegistryContainsProductionBackedGFactories(t *testing.T) {
	factories := xspecFactories()
	wantAvailable := []string{"approve", "intent", "queue", "session", "status", "stream"}
	if len(factories) != len(wantAvailable) {
		t.Fatalf("registered factories = %v, want %v", sortedNames(factories), wantAvailable)
	}
	for _, name := range wantAvailable {
		if factories[name] == nil {
			t.Errorf("production-backed factory %q is missing", name)
		}
	}
	for _, name := range requiredGSlices {
		if containsName(diagnosticSlices, name) {
			t.Errorf("G slice %q is also classified as D", name)
		}
	}
	for _, name := range diagnosticSlices {
		if factories[name] != nil {
			t.Errorf("diagnostic D slice %q was registered as a G replay", name)
		}
	}
}

func TestEveryRegisteredFactoryReturnsFullObservationOnReset(t *testing.T) {
	for _, name := range sortedNames(xspecFactories()) {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			err := protocol.Serve(context.Background(), name, xspecFactories(), strings.NewReader("{\"op\":\"reset\"}\n"), &output)
			if err != nil {
				t.Fatalf("Serve(reset) error = %v", err)
			}
			var observation map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &observation); err != nil {
				t.Fatalf("reset observation is invalid JSON: %v (%q)", err, output.String())
			}
			if observation == nil {
				t.Fatalf("reset observation is not a JSON object: %q", output.String())
			}
		})
	}
}

func TestUnavailableGFactoriesRefuseWithoutWritingAnObservation(t *testing.T) {
	for name, reason := range unavailableGSlices {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			err := serveProtocol(context.Background(), []string{name}, strings.NewReader("{\"op\":\"reset\"}\n"), &output)
			if err == nil || !strings.Contains(err.Error(), reason) {
				t.Fatalf("serveProtocol(%q) error = %v, want explicit adapter refusal %q", name, err, reason)
			}
			if output.Len() != 0 {
				t.Fatalf("unavailable slice wrote a substitute observation: %q", output.String())
			}
		})
	}
}

func TestDComparisonIsObservationalAndCannotCountTowardG(t *testing.T) {
	report, err := diagnostic.Compare(
		diagnostic.Gate,
		json.RawMessage(`{"project":{"phase":"published","tree":"base"}}`),
		json.RawMessage(`{"project":{"phase":"published","tree":"next"}}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Class != "D" || report.CountsTowardG || report.Match || len(report.Divergences) != 1 {
		t.Fatalf("D comparison = %#v", report)
	}
	if report.Divergences[0].Path != "/project/tree" {
		t.Fatalf("D comparison divergence = %#v, want full path /project/tree", report.Divergences[0])
	}
}

func sortedNames(registry protocol.Registry) []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	// The expected order is explicit here, so sorting needs no extra dependency.
	for index := 0; index < len(names); index++ {
		for next := index + 1; next < len(names); next++ {
			if names[next] < names[index] {
				names[index], names[next] = names[next], names[index]
			}
		}
	}
	return names
}

func containsName(names []string, target string) bool {
	for _, name := range names {
		if name == target {
			return true
		}
	}
	return false
}
