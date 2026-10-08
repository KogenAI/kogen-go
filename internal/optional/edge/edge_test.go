package edge

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
)

type generatorFunc func(context.Context, []byte, Recipe) ([]byte, error)

func (f generatorFunc) Generate(ctx context.Context, request []byte, recipe Recipe) ([]byte, error) {
	return f(ctx, request, recipe)
}

type runnerFunc func(context.Context, RunRequest) (Observation, error)

func (f runnerFunc) Run(ctx context.Context, request RunRequest) (Observation, error) {
	return f(ctx, request)
}

func TestSharedRecipeIsStableAndBoundsBothPaths(t *testing.T) {
	first := SharedRecipe()
	second := SharedRecipe()
	if first != second {
		t.Fatalf("shared recipe changed between reads: %#v %#v", first, second)
	}
	if first.Version != "edge-v1" || first.MaxRequestBytes <= 0 || first.MaxSuiteBytes <= 0 || first.MaxTests <= 0 || first.GenerationTimeout <= 0 || first.TestTimeout <= 0 || first.MaxOutputTailBytes <= 0 || first.MaxConcurrentChecks != 2 || first.MaxParallelCandidates < 2 {
		t.Fatalf("shared recipe has incomplete or unbounded fields: %#v", first)
	}
}

func TestDisabledProbeHasNoEffectsAndDoesNotReplaceAcceptance(t *testing.T) {
	generatorCalls, runnerCalls := 0, 0
	controller, err := New(generatorFunc(func(context.Context, []byte, Recipe) ([]byte, error) {
		generatorCalls++
		return nil, nil
	}), runnerFunc(func(context.Context, RunRequest) (Observation, error) {
		runnerCalls++
		return passObservation(), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	outcome := controller.Probe(context.Background(), ProbeRequest{})
	if outcome.Required || outcome.Status != StatusSkipped || !outcome.Passed() || !outcome.CanLand(true) || outcome.CanLand(false) {
		t.Fatalf("disabled probe changed acceptance outcome: %+v", outcome)
	}
	if generatorCalls != 0 || runnerCalls != 0 {
		t.Fatalf("disabled probe performed work: generator=%d runner=%d", generatorCalls, runnerCalls)
	}
}

func TestProbeUsesExactRequestSharedRecipeAndReturnsStableSuite(t *testing.T) {
	requestBytes := []byte("\nRequest: preserve these exact bytes.\n")
	generated := []byte("assert edge boundary\n")
	wantRequest := append([]byte(nil), requestBytes...)
	var generationRecipe Recipe
	var runRequest RunRequest
	controller, err := New(generatorFunc(func(_ context.Context, got []byte, recipe Recipe) ([]byte, error) {
		if !bytes.Equal(got, wantRequest) {
			t.Fatalf("generator Request changed: %q", got)
		}
		generationRecipe = recipe
		return generated, nil
	}), runnerFunc(func(_ context.Context, got RunRequest) (Observation, error) {
		runRequest = got
		return passObservation(), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	outcome := controller.Probe(context.Background(), ProbeRequest{
		Enabled: true, CandidateID: "R1", Workspace: "/work/R1", Request: requestBytes,
	})
	if outcome.Status != StatusPassed || !outcome.Required || outcome.Failure != "" {
		t.Fatalf("valid edge probe did not pass: %+v", outcome)
	}
	wantRecipe := SharedRecipe()
	if generationRecipe != wantRecipe || runRequest.Recipe != wantRecipe {
		t.Fatalf("generator and runner did not share recipe: generation=%#v runner=%#v", generationRecipe, runRequest.Recipe)
	}
	if !bytes.Equal(outcome.Suite.Source, generated) || outcome.Suite.SHA256 == "" || runRequest.Suite.SHA256 != outcome.Suite.SHA256 || runRequest.CandidateID != "R1" || runRequest.SuiteOwnerID != "R1" {
		t.Fatalf("probe lost generated suite identity: outcome=%+v runner=%+v", outcome, runRequest)
	}
	generated[0] = 'X'
	if outcome.Suite.Source[0] != 'a' {
		t.Fatal("generated suite retained mutable generator-owned bytes")
	}
	if !bytes.Equal(outcome.Observation.OutputTail, []byte("green")) {
		t.Fatalf("runner observation was not preserved: %+v", outcome.Observation)
	}
}

func TestProbeRejectsGenerationAndRunnerFailuresWithoutPassing(t *testing.T) {
	tests := []struct {
		name       string
		generate   func(context.Context, []byte, Recipe) ([]byte, error)
		run        func(context.Context, RunRequest) (Observation, error)
		wantStatus Status
		wantReason Failure
	}{
		{"generator error", func(context.Context, []byte, Recipe) ([]byte, error) { return nil, errors.New("private detail") }, nil, StatusStopped, FailureGenerator},
		{"empty suite", func(context.Context, []byte, Recipe) ([]byte, error) { return nil, nil }, nil, StatusFailed, FailureSuiteInvalid},
		{"timeout", func(context.Context, []byte, Recipe) ([]byte, error) { return []byte("test"), nil }, func(context.Context, RunRequest) (Observation, error) {
			row := passObservation()
			row.TimedOut = true
			return row, nil
		}, StatusStopped, FailureRunnerLimit},
		{"unavailable", func(context.Context, []byte, Recipe) ([]byte, error) { return []byte("test"), nil }, func(context.Context, RunRequest) (Observation, error) {
			row := passObservation()
			row.Unavailable = true
			return row, nil
		}, StatusStopped, FailureRunnerUnavailable},
		{"zero tests", func(context.Context, []byte, Recipe) ([]byte, error) { return []byte("test"), nil }, func(context.Context, RunRequest) (Observation, error) {
			row := passObservation()
			row.TotalTests = 0
			row.PassedTests = 0
			return row, nil
		}, StatusFailed, FailureNoTests},
		{"test failed", func(context.Context, []byte, Recipe) ([]byte, error) { return []byte("test"), nil }, func(context.Context, RunRequest) (Observation, error) {
			row := passObservation()
			row.ExitStatus = intPointer(1)
			row.TotalTests = 1
			row.FailedTests = 1
			row.PassedTests = 0
			return row, nil
		}, StatusFailed, FailureTestsFailed},
		{"tree changed", func(context.Context, []byte, Recipe) ([]byte, error) { return []byte("test"), nil }, func(context.Context, RunRequest) (Observation, error) {
			row := passObservation()
			row.TreeAfter = "after"
			return row, nil
		}, StatusFailed, FailureTreeMutated},
		{"missing tree evidence", func(context.Context, []byte, Recipe) ([]byte, error) { return []byte("test"), nil }, func(context.Context, RunRequest) (Observation, error) {
			row := passObservation()
			row.TreeBefore = ""
			return row, nil
		}, StatusStopped, FailureObservation},
		{"runner error", func(context.Context, []byte, Recipe) ([]byte, error) { return []byte("test"), nil }, func(context.Context, RunRequest) (Observation, error) {
			return Observation{}, errors.New("unbounded diagnostic must not escape")
		}, StatusStopped, FailureRunner},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := runnerFunc(func(ctx context.Context, request RunRequest) (Observation, error) {
				if test.run == nil {
					return passObservation(), nil
				}
				return test.run(ctx, request)
			})
			controller, err := New(generatorFunc(test.generate), runner)
			if err != nil {
				t.Fatal(err)
			}
			outcome := controller.Probe(context.Background(), ProbeRequest{
				Enabled: true, CandidateID: "R1", Workspace: "/work/R1", Request: []byte("raw request"),
			})
			if outcome.Status != test.wantStatus || outcome.Failure != test.wantReason || outcome.Passed() || outcome.CanLand(true) {
				t.Fatalf("failure was not blocking: %+v", outcome)
			}
		})
	}
}

func TestProbeBoundsObservedOutputAndRejectsMalformedInputs(t *testing.T) {
	controller, err := New(generatorFunc(func(context.Context, []byte, Recipe) ([]byte, error) {
		return []byte("generated"), nil
	}), runnerFunc(func(context.Context, RunRequest) (Observation, error) {
		row := passObservation()
		row.OutputTail = bytes.Repeat([]byte("x"), SharedRecipe().MaxOutputTailBytes+3)
		return row, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	request := ProbeRequest{Enabled: true, CandidateID: "R1", Workspace: "/work/R1", Request: []byte("request")}
	outcome := controller.Probe(context.Background(), request)
	if outcome.Status != StatusPassed || len(outcome.Observation.OutputTail) != SharedRecipe().MaxOutputTailBytes {
		t.Fatalf("output tail was not bounded: status=%s size=%d", outcome.Status, len(outcome.Observation.OutputTail))
	}
	for _, mutate := range []func(*ProbeRequest){
		func(r *ProbeRequest) { r.CandidateID = "../R1" },
		func(r *ProbeRequest) { r.Workspace = "relative/path" },
		func(r *ProbeRequest) { r.Request = []byte("bad\x00request") },
		func(r *ProbeRequest) { r.Request = bytes.Repeat([]byte("x"), SharedRecipe().MaxRequestBytes+1) },
	} {
		bad := ProbeRequest{Enabled: true, CandidateID: "R1", Workspace: "/work/R1", Request: []byte("request")}
		mutate(&bad)
		blocked := controller.Probe(context.Background(), bad)
		if blocked.Status != StatusStopped || blocked.Failure != FailureInvalidRequest || blocked.Passed() {
			t.Fatalf("invalid probe input passed: %+v", blocked)
		}
	}
}

func TestProbeRequiresNonemptyObservedTestSetAndRejectsImpossibleCounts(t *testing.T) {
	controller, err := New(generatorFunc(func(context.Context, []byte, Recipe) ([]byte, error) {
		return []byte("generated"), nil
	}), runnerFunc(func(context.Context, RunRequest) (Observation, error) {
		row := passObservation()
		row.PassedTests++
		return row, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	outcome := controller.Probe(context.Background(), ProbeRequest{
		Enabled: true, CandidateID: "R1", Workspace: "/work/R1", Request: []byte("request"),
	})
	if outcome.Status != StatusStopped || outcome.Failure != FailureObservation || outcome.Passed() {
		t.Fatalf("impossible test counters passed: %+v", outcome)
	}
}

func passObservation() Observation {
	return Observation{
		ExitStatus: intPointer(0), TotalTests: 2, PassedTests: 2,
		TreeBefore: "tree-id", TreeAfter: "tree-id", OutputTail: []byte("green"),
	}
}

func intPointer(value int) *int { return &value }

func TestStatusValuesRemainStable(t *testing.T) {
	if got := []Status{StatusSkipped, StatusPassed, StatusFailed, StatusStopped}; !reflect.DeepEqual(got, []Status{"skipped", "passed", "failed", "stopped"}) {
		t.Fatalf("status vocabulary changed: %v", got)
	}
}
