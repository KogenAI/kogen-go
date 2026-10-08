package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"kogen-go/internal/build/recipe"
	"kogen-go/internal/build/single"
	"kogen-go/internal/contract"
	"kogen-go/internal/optional/checkpoint"
	"kogen-go/internal/optional/edge"
	"kogen-go/internal/optional/staged"
	"kogen-go/internal/process"
	"kogen-go/internal/project"
	"kogen-go/internal/provider/session"
	"kogen-go/internal/provider/wire"
	"kogen-go/internal/sandbox"
	"kogen-go/internal/shape/witness"
	"kogen-go/internal/yamlmini"
)

type optionalProcessRunner func(context.Context, contract.ProcessSpec) (contract.ProcessResult, error)

func (runner optionalProcessRunner) Run(ctx context.Context, spec contract.ProcessSpec) (contract.ProcessResult, error) {
	return runner(ctx, spec)
}

type optionalEdgeGenerator func(context.Context, []byte, edge.Recipe) ([]byte, error)

func (generator optionalEdgeGenerator) Generate(ctx context.Context, request []byte, selected edge.Recipe) ([]byte, error) {
	return generator(ctx, request, selected)
}

type optionalEdgeRunner func(context.Context, edge.RunRequest) (edge.Observation, error)

func (runner optionalEdgeRunner) Run(ctx context.Context, request edge.RunRequest) (edge.Observation, error) {
	return runner(ctx, request)
}

func TestOptionalWiringResolvesEveryRecipeFixture(t *testing.T) {
	wiring, err := newOptionalWiring(optionalWiringDependencies{})
	if err != nil {
		t.Fatal(err)
	}
	roles := optionalRoles()
	names := []string{
		"ladder", "ladder-diverse", "ladder-luna", "ladder-sol-low", "ladder-sol-medium",
		"ladder-sol-high", "plan-shell", "staged", "direct", "direct-escalate",
		"direct-shell", "escalate-shell", "ladder+edge",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			parsed, err := recipe.Parse(name)
			if err != nil {
				t.Fatal(err)
			}
			program, err := wiring.ResolveBuild(recipe.Build{Recipe: parsed}, roles, nil)
			if err != nil {
				t.Fatal(err)
			}
			if program.Build.Recipe.Name != name || len(program.Builders) != len(parsed.Rungs) {
				t.Fatalf("resolved recipe = %q with %d builder stages; want %q with %d", program.Build.Recipe.Name, len(program.Builders), name, len(parsed.Rungs))
			}
			for index, stage := range program.Builders {
				role, allowed, err := stagedToolPolicy(parsed.Rungs[index].Tools)
				if err != nil {
					t.Fatal(err)
				}
				if string(stage.ToolRole) != role || !reflect.DeepEqual(stage.AllowedTools, allowed) {
					t.Errorf("builder stage %d tools = %q/%q, want %q/%q", index+1, stage.ToolRole, stage.AllowedTools, role, allowed)
				}
			}
			if (program.Context != nil) != (parsed.Kind == recipe.Staged) || (program.Review != nil) != (parsed.Kind == recipe.Staged) {
				t.Errorf("staged context/review = %t/%t for %s", program.Context != nil, program.Review != nil, name)
			}
		})
	}
}

func TestOptionalWiringRequiresEdgeEvidenceAsAnAdditionalLandingConjunct(t *testing.T) {
	build, err := recipe.Parse("ladder+edge")
	if err != nil {
		t.Fatal(err)
	}
	missing, err := newOptionalWiring(optionalWiringDependencies{})
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	request := edge.ProbeRequest{CandidateID: "R1", Workspace: workspace, Request: []byte("exact approved request")}
	stopped := missing.EdgeProbe(context.Background(), recipe.Build{Recipe: build}, request)
	if !stopped.Required || stopped.Passed() || missing.CanLand(recipe.Build{Recipe: build}, true, stopped) {
		t.Fatalf("missing edge adapter was not a required blocker: %+v", stopped)
	}

	var generated, ran atomic.Int32
	wiring, err := newOptionalWiring(optionalWiringDependencies{
		EdgeGenerator: optionalEdgeGenerator(func(_ context.Context, got []byte, selected edge.Recipe) ([]byte, error) {
			generated.Add(1)
			if string(got) != "exact approved request" || selected != edge.SharedRecipe() {
				t.Fatalf("edge generation inputs changed: request=%q recipe=%+v", got, selected)
			}
			return []byte("assert boundary"), nil
		}),
		EdgeRunner: optionalEdgeRunner(func(_ context.Context, request edge.RunRequest) (edge.Observation, error) {
			ran.Add(1)
			if request.CandidateID != "R1" || request.SuiteOwnerID != "R1" || request.Workspace != workspace {
				t.Fatalf("edge runner received invalid candidate: %+v", request)
			}
			status := 0
			return edge.Observation{ExitStatus: &status, TotalTests: 1, PassedTests: 1, TreeBefore: "base", TreeAfter: "base"}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	passed := wiring.EdgeProbe(context.Background(), recipe.Build{Recipe: build}, request)
	if !passed.Passed() || !wiring.CanLand(recipe.Build{Recipe: build}, true, passed) || wiring.CanLand(recipe.Build{Recipe: build}, false, passed) {
		t.Fatalf("edge evidence did not remain conjunctive with approved acceptance: %+v", passed)
	}
	if generated.Load() != 1 || ran.Load() != 1 {
		t.Fatalf("edge effects = generate:%d run:%d, want one each", generated.Load(), ran.Load())
	}

	plain, _ := recipe.Parse("ladder")
	skipped := wiring.EdgeProbe(context.Background(), recipe.Build{Recipe: plain}, request)
	if skipped.Required || skipped.Status != edge.StatusSkipped || !wiring.CanLand(recipe.Build{Recipe: plain}, true, skipped) || wiring.CanLand(recipe.Build{Recipe: plain}, false, skipped) {
		t.Fatalf("default recipe changed ordinary acceptance semantics: %+v", skipped)
	}
}

func TestOptionalCheckpointWiringKeepsApprovedInputsAndStartsFreshEpoch(t *testing.T) {
	wiring, err := newOptionalWiring(optionalWiringDependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if enabled, err := wiring.ShouldCheckpoint(20_000, 0); err != nil || enabled {
		t.Fatalf("omitted checkpoint threshold = %t, %v; want disabled", enabled, err)
	}
	if enabled, err := wiring.ShouldCheckpoint(checkpoint.MinimumContextBytes-1, checkpoint.MinimumContextBytes); err != nil || enabled {
		t.Fatalf("checkpoint ran below byte threshold = %t, %v", enabled, err)
	}
	if _, err := wiring.ShouldCheckpoint(20_000, checkpoint.MinimumContextBytes-1); err == nil {
		t.Fatal("below-minimum checkpoint threshold was accepted")
	}

	identity, err := session.Bind(session.Binding{
		RunID: "0123456789abcdef0123456789abcdef", CacheKey: "opaque-cache-key",
		Role: "builder", Provider: "chatgpt", Model: "gpt-6-luna", Effort: "max",
		Stage: "build", Attempt: "builder", Rung: "R1", Epoch: "initial",
	})
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := session.New(identity)
	if err != nil {
		t.Fatal(err)
	}
	requestItem := json.RawMessage(` {"role":"user","content":[{"type":"input_text","text":"approved request"}]} `)
	planItem := json.RawMessage(`{"role":"user","content":[{"type":"input_text","text":"approved plan"}]}`)
	if err := conversation.Append(contract.SessionInput, requestItem); err != nil {
		t.Fatal(err)
	}
	if err := conversation.Append(contract.SessionInput, planItem); err != nil {
		t.Fatal(err)
	}
	prefix := wire.DefaultPrefix()
	summarizer, err := wiring.PrepareCheckpointSummarizer(conversation, 3, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if summarizer.Conversation.Identity().Epoch != "checkpoint-3" || summarizer.ToolChoice != checkpoint.ToolChoiceNone || len(summarizer.CallableTools) != 0 {
		t.Fatalf("checkpoint summarizer identity/tools = %+v / %q %q", summarizer.Conversation.Identity(), summarizer.CallableTools, summarizer.ToolChoice)
	}
	checkpointValue, err := wiring.BuildCheckpoint("completed work and current blockers", 2048)
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := wiring.ContinueCheckpoint(identity, checkpoint.ApprovedInputs{Request: requestItem, Plan: planItem}, checkpointValue, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if continuation.Conversation.Identity().Epoch != checkpointValue.Epoch() {
		t.Fatalf("continuation epoch = %q, want checkpoint digest %q", continuation.Conversation.Identity().Epoch, checkpointValue.Epoch())
	}
	history := continuation.Conversation.ProtocolSession().History
	if len(history) != 3 || string(history[0].Raw) != string(requestItem) || string(history[1].Raw) != string(planItem) || !json.Valid(history[2].Raw) {
		t.Fatalf("continuation history did not retain exact approved inputs: %#v", history)
	}
	if _, err := wiring.BuildCheckpoint("", 2048); !errors.Is(err, checkpoint.ErrContinuationFailed) {
		t.Fatalf("empty checkpoint error = %v, want continuation_failed", err)
	}
}

func TestOptionalWitnessDefaultIsNoOpAndEnabledModeNeedsEffects(t *testing.T) {
	wiring, err := newOptionalWiring(optionalWiringDependencies{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := wiring.RunWitness(context.Background(), witness.Request{Mode: witness.ModeNone})
	if err != nil || result.Feasibility != witness.FeasibilityNotChecked || result.Proof != nil {
		t.Fatalf("default witness mode = %+v, %v", result, err)
	}
	_, err = wiring.RunWitness(context.Background(), witness.Request{Mode: witness.ModeWitness})
	if err == nil {
		t.Fatal("enabled witness mode silently passed without production effects")
	}
}

func TestBuildSandboxUnavailableFallbackRequiresAndChecksIntegrity(t *testing.T) {
	root := t.TempDir()
	resolved, runDir := sandboxFixture(t, root, yamlmini.Mapping{"sandbox": "true"})
	var processCalls atomic.Int32
	var snapshotCalls atomic.Int32
	status := 0
	wiring, err := newOptionalWiring(optionalWiringDependencies{
		Processes: optionalProcessRunner(func(context.Context, contract.ProcessSpec) (contract.ProcessResult, error) {
			processCalls.Add(1)
			return contract.ProcessResult{ExitStatus: &status}, nil
		}),
		Host: process.Environment{"HOME": filepath.Join(root, "home"), "KOGEN_SANDBOX": "unavailable"},
		Integrity: sandbox.IntegritySnapshotFunc(func(context.Context) (string, error) {
			snapshotCalls.Add(1)
			return "same", nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	warning, err := wiring.ProbeBuildSandbox(context.Background(), resolved, runDir)
	if err != nil || warning != "forced by KOGEN_SANDBOX=unavailable" {
		t.Fatalf("forced unavailable probe = %q, %v", warning, err)
	}
	dependencies := wiring.ConfigureSingleBuild(single.Dependencies{})
	if dependencies.Sandbox == nil || dependencies.Processes == nil || wiring.ProcessRunner() == nil {
		t.Fatal("sandbox runner was not attached to the single Build ports")
	}
	if _, err := dependencies.Processes.Run(context.Background(), contract.ProcessSpec{Executable: "child", Dir: resolved.Checkout}); err != nil {
		t.Fatal(err)
	}
	if processCalls.Load() != 1 || snapshotCalls.Load() != 2 {
		t.Fatalf("unconfined child effects = process:%d snapshots:%d, want one process and a before/after pair", processCalls.Load(), snapshotCalls.Load())
	}
}

func TestBuildSandboxRefusesChangedIntegrityOnUnavailableHost(t *testing.T) {
	root := t.TempDir()
	resolved, runDir := sandboxFixture(t, root, yamlmini.Mapping{"sandbox": "true"})
	status := 0
	var snapshots atomic.Int32
	wiring, err := newOptionalWiring(optionalWiringDependencies{
		Processes: optionalProcessRunner(func(context.Context, contract.ProcessSpec) (contract.ProcessResult, error) {
			return contract.ProcessResult{ExitStatus: &status}, nil
		}),
		Host: process.Environment{"HOME": filepath.Join(root, "home"), "KOGEN_SANDBOX": "unavailable"},
		Integrity: sandbox.IntegritySnapshotFunc(func(context.Context) (string, error) {
			if snapshots.Add(1) == 1 {
				return "before", nil
			}
			return "after", nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wiring.ProbeBuildSandbox(context.Background(), resolved, runDir); err != nil {
		t.Fatal(err)
	}
	_, err = wiring.ProcessRunner().Run(context.Background(), contract.ProcessSpec{Executable: "child", Dir: resolved.Checkout})
	if !errors.Is(err, sandbox.ErrIntegrityChanged) {
		t.Fatalf("changed source integrity error = %v, want sandbox integrity failure", err)
	}
}

func TestBuildSandboxOffModeDoesNotProbeOrRequireIntegrity(t *testing.T) {
	root := t.TempDir()
	resolved, runDir := sandboxFixture(t, root, yamlmini.Mapping{"sandbox": "false"})
	var processCalls, snapshotCalls atomic.Int32
	status := 0
	wiring, err := newOptionalWiring(optionalWiringDependencies{
		Processes: optionalProcessRunner(func(context.Context, contract.ProcessSpec) (contract.ProcessResult, error) {
			processCalls.Add(1)
			return contract.ProcessResult{ExitStatus: &status}, nil
		}),
		Host: process.Environment{"HOME": filepath.Join(root, "home")},
		Integrity: sandbox.IntegritySnapshotFunc(func(context.Context) (string, error) {
			snapshotCalls.Add(1)
			return "unused", nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if warning, err := wiring.ProbeBuildSandbox(context.Background(), resolved, runDir); err != nil || warning != "" {
		t.Fatalf("off-mode probe = %q, %v", warning, err)
	}
	if _, err := wiring.ProcessRunner().Run(context.Background(), contract.ProcessSpec{Executable: "child", Dir: resolved.Checkout}); err != nil {
		t.Fatal(err)
	}
	if processCalls.Load() != 1 || snapshotCalls.Load() != 0 {
		t.Fatalf("off-mode effects = process:%d snapshots:%d", processCalls.Load(), snapshotCalls.Load())
	}
}

func TestBuildSandboxUsesRealHostProbeAndSupervisedChild(t *testing.T) {
	root := t.TempDir()
	resolved, runDir := sandboxFixture(t, root, yamlmini.Mapping{"sandbox": "true"})
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	var snapshots atomic.Int32
	wiring, err := newOptionalWiring(optionalWiringDependencies{
		Processes: process.Supervisor{},
		Host:      process.Environment{"HOME": home},
		Integrity: sandbox.IntegritySnapshotFunc(func(context.Context) (string, error) {
			snapshots.Add(1)
			return "stable", nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	warning, err := wiring.ProbeBuildSandbox(context.Background(), resolved, runDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("host=%s sandbox_available=%t warning=%q", runtime.GOOS, warning == "", warning)

	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	writePath := filepath.Join(workspace, "child-write.txt")
	spec := contract.ProcessSpec{
		Executable: "/bin/sh", Dir: workspace,
		Env: []string{"PATH=/usr/bin:/bin"}, Timeout: 4 * time.Second,
		OutputLimit: 8 << 10, OutputTailLimit: 8 << 10,
		LogPath: filepath.Join(runDir, "logs", "sandbox-child.log"),
	}
	// Keep the path in the supervised command's positional input, not its argv
	// string interpolation in production wiring. This local shell fixture only
	// writes a harmless sentinel beneath its declared workspace.
	spec.Args = []string{"-c", "printf allowed > \"$1\"", "sandbox-test", writePath}
	result, err := wiring.ProcessRunner().Run(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitStatus == nil || *result.ExitStatus != 0 {
		t.Fatalf("sandboxed child status=%v unavailable=%t tail=%q", result.ExitStatus, result.Unavailable, result.OutputTail)
	}
	contents, err := os.ReadFile(writePath)
	if err != nil || string(contents) != "allowed" {
		t.Fatalf("declared workspace write = %q, %v", contents, err)
	}
}

func sandboxFixture(t *testing.T, root string, raw yamlmini.Mapping) (*project.Resolution, string) {
	t.Helper()
	checkout := filepath.Join(root, "checkout")
	origin := filepath.Join(root, "origin")
	runDir := filepath.Join(root, "run")
	for _, path := range []string{checkout, origin, runDir, filepath.Join(runDir, "logs"), filepath.Join(runDir, "tmp"), filepath.Join(runDir, "reports")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return &project.Resolution{
		Checkout: checkout, Origin: origin,
		Config: &project.Config{Raw: raw},
	}, runDir
}

func optionalRoles() contract.RoleManifest {
	return contract.RoleManifest{Effective: map[contract.RoleName]contract.RoleSettings{
		"builder":  {Provider: "chatgpt", Model: "gpt-6-luna", Effort: "max"},
		"planner":  {Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high"},
		"shaper":   {Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high"},
		"auditor":  {Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high"},
		"context":  {Provider: "chatgpt", Model: "gpt-6-luna", Effort: "max"},
		"reviewer": {Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high"},
	}}
}

func stagedToolPolicy(value recipe.ToolSet) (string, []string, error) {
	role, names, err := staged.ToolPolicy(value)
	return string(role), names, err
}
