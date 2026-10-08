package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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
	"kogen-go/internal/safefs"
	"kogen-go/internal/sandbox"
	"kogen-go/internal/sandbox/linux"
	"kogen-go/internal/shape/witness"
)

// optionalWiringDependencies are the optional-feature ports already owned by
// their component packages. This adapter adds no retry, acceptance, or landing
// policy of its own.
type optionalWiringDependencies struct {
	Processes      contract.ProcessRunner
	Host           process.Environment
	Integrity      sandbox.IntegritySnapshotter
	EdgeGenerator  edge.Generator
	EdgeRunner     edge.Runner
	WitnessEffects witness.Effects
}

// optionalWiring is the app boundary for opt-in Build features. Stage
// execution still goes through staged.Controller, edge checks remain an
// additional landing conjunct, witness mode delegates to the witness state
// machine, and checkpoint bytes/epochs remain owned by checkpoint.
type optionalWiring struct {
	processes      contract.ProcessRunner
	host           process.Environment
	integrity      sandbox.IntegritySnapshotter
	edge           *edge.Controller
	witnessEffects witness.Effects
	sandbox        *buildSandboxWiring
}

func newOptionalWiring(deps optionalWiringDependencies) (*optionalWiring, error) {
	if (deps.EdgeGenerator == nil) != (deps.EdgeRunner == nil) {
		return nil, errors.New("optional wiring requires both edge generator and runner")
	}
	if deps.Host == nil {
		deps.Host = process.HostEnvironment()
	} else {
		deps.Host = cloneEnvironment(deps.Host)
	}
	wiring := &optionalWiring{
		processes: deps.Processes, host: deps.Host, integrity: deps.Integrity,
		witnessEffects: deps.WitnessEffects,
	}
	if deps.EdgeGenerator != nil {
		controller, err := edge.New(deps.EdgeGenerator, deps.EdgeRunner)
		if err != nil {
			return nil, fmt.Errorf("initialize edge checks: %w", err)
		}
		wiring.edge = controller
	}
	if deps.Processes != nil {
		wiring.sandbox = &buildSandboxWiring{
			inner: deps.Processes, host: cloneEnvironment(deps.Host), integrity: deps.Integrity,
			macRunners: make(map[string]*sandbox.Runner),
		}
	}
	return wiring, nil
}

// ResolveBuild returns the role-resolved stage program for any supported
// recipe. It does not invent local role defaults or replace the shared Build
// controller.
func (w *optionalWiring) ResolveBuild(build recipe.Build, roles contract.RoleManifest, modelFallback *bool) (staged.Program, error) {
	if w == nil {
		return staged.Program{}, errors.New("optional wiring is required")
	}
	return staged.Resolve(build, roles, modelFallback)
}

// RunStaged executes the recipe's optional context/plan/review stages around
// the same recipe-aware Build controller used by ordinary Builds.
func (w *optionalWiring) RunStaged(ctx context.Context, stages staged.StageRunner, build staged.BuildController, request staged.Request) (staged.Outcome, error) {
	if w == nil {
		return staged.Outcome{}, errors.New("optional wiring is required")
	}
	controller, err := staged.NewController(staged.Dependencies{Stages: stages, Build: build})
	if err != nil {
		return staged.Outcome{}, err
	}
	return controller.Run(ctx, request)
}

// EdgeProbe runs the generated cross-check only when the resolved Build
// enables it. Missing edge adapters produce a required stopped result; they
// can never turn an enabled check into a pass.
func (w *optionalWiring) EdgeProbe(ctx context.Context, build recipe.Build, request edge.ProbeRequest) edge.Outcome {
	required := build.Recipe.Edge || build.Settings.EdgeTests
	if !required {
		return edge.Outcome{Status: edge.StatusSkipped}
	}
	if w == nil || w.edge == nil {
		return edge.Outcome{Required: true, Status: edge.StatusStopped, Failure: edge.FailureRunnerUnavailable}
	}
	request.Enabled = true
	return w.edge.Probe(ctx, request)
}

// CanLand keeps the approved acceptance result mandatory and adds edge
// evidence only for recipes that enabled that path.
func (w *optionalWiring) CanLand(build recipe.Build, acceptancePassed bool, edgeResult edge.Outcome) bool {
	if !build.Recipe.Edge && !build.Settings.EdgeTests {
		return acceptancePassed
	}
	return edgeResult.CanLand(acceptancePassed)
}

// RunWitness delegates opt-in witness execution to its controller. The
// default no-witness request stays a true no-op even without witness effects.
func (w *optionalWiring) RunWitness(ctx context.Context, request witness.Request) (witness.Result, error) {
	if request.Mode == "" || request.Mode == witness.ModeNone {
		return witness.Run(ctx, request, nil)
	}
	if w == nil || w.witnessEffects == nil {
		return witness.Result{}, errors.New("witness effects are unavailable")
	}
	return witness.Run(ctx, request, w.witnessEffects)
}

// ShouldCheckpoint applies the explicit opt-in threshold and byte boundary.
func (w *optionalWiring) ShouldCheckpoint(historyBytes, contextBytes int) (bool, error) {
	if err := checkpoint.ValidateContextBytes(contextBytes); err != nil {
		return false, err
	}
	return checkpoint.ShouldCheckpoint(historyBytes, contextBytes), nil
}

func (w *optionalWiring) PrepareCheckpointSummarizer(current *session.Conversation, turn int, prefix wire.Prefix) (checkpoint.SummarizerTurn, error) {
	return checkpoint.PrepareSummarizer(current, turn, prefix)
}

func (w *optionalWiring) BuildCheckpoint(summary string, maxBytes int) (checkpoint.Checkpoint, error) {
	return checkpoint.BuildCheckpoint(summary, maxBytes)
}

func (w *optionalWiring) ContinueCheckpoint(builder contract.ConversationIdentity, approved checkpoint.ApprovedInputs, value checkpoint.Checkpoint, prefix wire.Prefix) (checkpoint.ContinuationTurn, error) {
	return checkpoint.NewContinuation(builder, approved, value, prefix)
}

// ProcessRunner returns the platform-aware child runner for Build effects.
// A caller must first attach it to a Build by calling ProbeBuildSandbox.
func (w *optionalWiring) ProcessRunner() contract.ProcessRunner {
	if w == nil || w.sandbox == nil {
		return nil
	}
	return w.sandbox
}

// ConfigureSingleBuild attaches both sandbox hooks supported by the single
// Build controller. The same runner performs the preflight probe and child
// execution so unavailable confinement is paired with integrity snapshots.
func (w *optionalWiring) ConfigureSingleBuild(deps single.Dependencies) single.Dependencies {
	if w != nil && w.sandbox != nil {
		deps.Processes = w.sandbox
		deps.Sandbox = w.sandbox
	}
	return deps
}

// ProbeBuildSandbox runs the positive/negative probe before candidate code.
// The returned text is the stable reason to record when confinement is
// unavailable; an empty string means the selected mode needs no warning.
func (w *optionalWiring) ProbeBuildSandbox(ctx context.Context, resolved *project.Resolution, runDir string) (string, error) {
	if w == nil || w.sandbox == nil {
		return "", errors.New("Build sandbox process runner is unavailable")
	}
	return w.sandbox.Probe(ctx, resolved, runDir)
}

// buildSandboxWiring implements the single.Build sandbox ports. Darwin uses
// the shared Seatbelt runner. Linux explicitly selects the real pinned
// bubblewrap probe and Prepare path; it does not treat cross-compilation or a
// present bwrap binary as proof of confinement.
type buildSandboxWiring struct {
	inner     contract.ProcessRunner
	host      process.Environment
	integrity sandbox.IntegritySnapshotter

	mu         sync.Mutex
	bound      bool
	resolved   *project.Resolution
	runDir     string
	enabled    bool
	available  bool
	reason     string
	macRunners map[string]*sandbox.Runner
}

func (w *buildSandboxWiring) Probe(ctx context.Context, resolved *project.Resolution, runDir string) (string, error) {
	if ctx == nil || resolved == nil || w == nil || w.inner == nil {
		return "", errors.New("Build sandbox probe requires context, project, and process runner")
	}
	runDir, err := cleanOptionalAbsolute(runDir)
	if err != nil {
		return "", errors.New("Build sandbox run directory must be a clean absolute path")
	}
	checkout, err := cleanOptionalAbsolute(resolved.Checkout)
	if err != nil {
		return "", errors.New("Build sandbox checkout must be a clean absolute path")
	}
	origin, err := cleanOptionalAbsolute(resolved.Origin)
	if err != nil {
		return "", errors.New("Build sandbox origin must be a clean absolute path")
	}
	enabled := buildSandboxEnabled(resolved)

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.bound {
		if w.runDir != runDir || w.resolved.Checkout != checkout || w.resolved.Origin != origin {
			return "", errors.New("Build sandbox wiring is already bound to a different run")
		}
		return warningReason(w.enabled, w.host, w.available, w.reason), nil
	}
	w.resolved = resolved
	w.runDir = runDir
	w.enabled = enabled
	if !enabled || w.host["KOGEN_SANDBOXED"] == "1" {
		w.available = true
		w.bound = true
		return "", nil
	}
	if w.host["KOGEN_SANDBOX"] == "unavailable" {
		w.reason = "forced by KOGEN_SANDBOX=unavailable"
		w.bound = true
		return w.reason, nil
	}

	probeWorkspace, cleanup, err := makeProbeWorkspace(runDir)
	if err != nil {
		return "", err
	}
	template := contract.ProcessSpec{
		Executable: "/bin/sh", Dir: probeWorkspace,
		Env:     []string{"PATH=/usr/bin:/bin"},
		Timeout: 5 * time.Second, OutputLimit: 8 << 10, OutputTailLimit: 8 << 10,
		LogPath: filepath.Join(runDir, "logs", "sandbox-probe.log"),
	}
	availability, probeErr := w.probe(ctx, resolved, runDir, probeWorkspace, template)
	cleanupErr := cleanup()
	if probeErr != nil || cleanupErr != nil {
		return "", errors.Join(probeErr, cleanupErr)
	}
	w.available = availability.Available
	w.reason = availability.Reason
	if !w.available && w.reason == "" {
		w.reason = "no supported confinement tool is available on this host"
	}
	w.bound = true
	return warningReason(w.enabled, w.host, w.available, w.reason), nil
}

func (w *buildSandboxWiring) probe(ctx context.Context, resolved *project.Resolution, runDir, workspace string, template contract.ProcessSpec) (sandbox.Availability, error) {
	switch runtime.GOOS {
	case "darwin":
		policy, err := sandbox.NewBuildPolicy(true, workspace, resolved.Checkout, resolved.Origin, runDir, plainEnvironment(w.host))
		if err != nil {
			return sandbox.Availability{}, err
		}
		return sandbox.Probe(ctx, w.inner, template, policy)
	case "linux":
		result, err := linux.Probe(ctx, w.inner, template, linuxBuildConfiguration(runDir, workspace, resolved.Checkout, resolved.Origin, w.host))
		return sandbox.Availability{Available: result.Available, Reason: result.Reason, Checks: append([]string(nil), result.Checks...)}, err
	default:
		return sandbox.Availability{Reason: "no supported confinement tool is available on this host"}, nil
	}
}

func (w *buildSandboxWiring) Run(ctx context.Context, spec contract.ProcessSpec) (contract.ProcessResult, error) {
	if w == nil || w.inner == nil || ctx == nil {
		return contract.ProcessResult{}, errors.New("Build sandbox process runner requires context and process runner")
	}
	w.mu.Lock()
	if !w.bound || w.resolved == nil {
		w.mu.Unlock()
		return contract.ProcessResult{}, errors.New("Build sandbox must be probed before project processes run")
	}
	resolved, runDir, enabled, available, reason := w.resolved, w.runDir, w.enabled, w.available, w.reason
	ready := w.host["KOGEN_SANDBOXED"] == "1"
	forced := w.host["KOGEN_SANDBOX"] == "unavailable"
	w.mu.Unlock()

	if !enabled || ready {
		return w.inner.Run(ctx, spec)
	}
	if forced || !available || (runtime.GOOS != "darwin" && runtime.GOOS != "linux") {
		_ = reason // retained on the SandboxProbe result for the run journal
		return w.runWithIntegrity(ctx, spec)
	}

	switch runtime.GOOS {
	case "darwin":
		policy, err := sandbox.NewBuildPolicy(true, spec.Dir, resolved.Checkout, resolved.Origin, runDir, plainEnvironment(w.host))
		if err != nil {
			return contract.ProcessResult{}, err
		}
		w.mu.Lock()
		runner := w.macRunners[spec.Dir]
		if runner == nil {
			runner = sandbox.NewRunner(w.inner, policy, w.integrity)
			w.macRunners[spec.Dir] = runner
		}
		w.mu.Unlock()
		execution, err := runner.Run(ctx, spec)
		return execution.Process, err
	case "linux":
		configuration := linuxBuildConfiguration(runDir, spec.Dir, resolved.Checkout, resolved.Origin, w.host)
		prepared, err := linux.Prepare(spec, configuration)
		if err != nil {
			return contract.ProcessResult{}, err
		}
		return w.inner.Run(ctx, prepared)
	default:
		return w.runWithIntegrity(ctx, spec)
	}
}

func (w *buildSandboxWiring) runWithIntegrity(ctx context.Context, spec contract.ProcessSpec) (contract.ProcessResult, error) {
	if w.integrity == nil {
		return contract.ProcessResult{}, sandbox.ErrIntegritySnapshotRequired
	}
	before, err := w.integrity.Snapshot(ctx)
	if err != nil {
		return contract.ProcessResult{}, fmt.Errorf("sandbox: read pre-child integrity snapshot: %w", err)
	}
	result, processErr := w.inner.Run(ctx, spec)
	after, snapshotErr := w.integrity.Snapshot(ctx)
	if snapshotErr == nil && before != after {
		snapshotErr = sandbox.ErrIntegrityChanged
	}
	if snapshotErr != nil || processErr != nil {
		return contract.ProcessResult{}, errors.Join(snapshotErr, processErr)
	}
	return result, nil
}

func warningReason(enabled bool, host process.Environment, available bool, reason string) string {
	if !enabled || host["KOGEN_SANDBOXED"] == "1" || available {
		return ""
	}
	if reason == "" {
		reason = "no supported confinement tool is available on this host"
	}
	return reason
}

func buildSandboxEnabled(resolved *project.Resolution) bool {
	if resolved != nil && resolved.Config != nil {
		if value, ok := resolved.Config.Raw["sandbox"].(string); ok && value == "false" {
			return false
		}
	}
	return true
}

func makeProbeWorkspace(runDir string) (string, func() error, error) {
	root, err := safefs.OpenRoot(runDir)
	if err != nil {
		return "", nil, fmt.Errorf("sandbox: open private run root: %w", err)
	}
	const name = "tmp/sandbox-probe-workspace"
	if _, err := root.Lstat(name); err == nil {
		_ = root.Close()
		return "", nil, errors.New("sandbox: probe workspace already exists")
	} else if !errors.Is(err, fs.ErrNotExist) {
		_ = root.Close()
		return "", nil, err
	}
	if err := root.MkdirAll(name, 0o700); err != nil {
		_ = root.Close()
		return "", nil, err
	}
	workspace := filepath.Join(runDir, filepath.FromSlash(name))
	return workspace, func() error {
		removeErr := root.Remove(name)
		closeErr := root.Close()
		return errors.Join(removeErr, closeErr)
	}, nil
}

func linuxBuildConfiguration(runDir, workspace, checkout, origin string, host process.Environment) linux.Configuration {
	home := host["HOME"]
	writable := []string{
		workspace, filepath.Join(runDir, "logs"), filepath.Join(runDir, "tmp"),
		filepath.Join(runDir, "reports"), filepath.Join(runDir, "mise-state"),
		filepath.Join(runDir, "mise-cache"), "/tmp",
	}
	protected := make([]string, 0, 8)
	if home != "" {
		for _, relative := range []string{".cache/mise", ".hex", ".cache/rebar3", ".npm", ".cargo/registry", ".cargo/git", ".cache/go-build"} {
			writable = append(writable, filepath.Join(home, filepath.FromSlash(relative)))
		}
		for _, relative := range []string{".kogen/credentials", ".ssh", ".gnupg", ".codex"} {
			protected = append(protected, filepath.Join(home, filepath.FromSlash(relative)))
		}
		if entries, err := os.ReadDir(filepath.Join(home, ".kogen")); err == nil {
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "credentials") {
					protected = append(protected, filepath.Join(home, ".kogen", entry.Name()))
				}
			}
		}
	}
	if goModuleCache := host["GOMODCACHE"]; goModuleCache != "" {
		writable = append(writable, goModuleCache)
	}
	if authPath := host["KOGEN_AUTH_PATH"]; authPath != "" {
		protected = append(protected, authPath)
	}
	return linux.Configuration{
		RunDir: runDir, Workspace: workspace, Home: home,
		WritablePaths: writable, WriteDeniedPaths: []string{checkout, origin},
		ProtectedReadPaths: protected,
	}
}

func plainEnvironment(environment process.Environment) map[string]string {
	result := make(map[string]string, len(environment))
	for key, value := range environment {
		result[key] = value
	}
	return result
}

func cloneEnvironment(environment process.Environment) process.Environment {
	result := make(process.Environment, len(environment))
	for key, value := range environment {
		result[key] = value
	}
	return result
}

func cleanOptionalAbsolute(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, '\x00') {
		return "", errors.New("path must be clean and absolute")
	}
	return path, nil
}

var _ contract.ProcessRunner = (*buildSandboxWiring)(nil)
var _ single.SandboxProbe = (*buildSandboxWiring)(nil)
