// Package prepare implements the read/check half of Intent approval. It binds
// a card to exact source bytes and an exact resolved base tree; publication of
// the immutable approval ref belongs to approval/publish.
package prepare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"kogen-go/internal/acceptance"
	"kogen-go/internal/contract"
	"kogen-go/internal/findings"
	"kogen-go/internal/gitio"
	"kogen-go/internal/intent"
	"kogen-go/internal/process"
	"kogen-go/internal/project"
	"kogen-go/internal/protection"
	"kogen-go/internal/safefs"
	"kogen-go/internal/yamlmini"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var approvalPrefixPattern = regexp.MustCompile(`^[0-9a-f]{6,64}$`)
var authorIdentPattern = regexp.MustCompile(`^(.+ <[^<>]+>) [0-9]+ [+-][0-9]{4}$`)

// Failure is a public preparation failure with the reason and exit code used
// by the command adapter. Error messages intentionally avoid printing the
// full child environment, cache identity, or raw source bytes.
type Failure struct {
	Reason string
	Exit   contract.ExitCode
	Detail string
}

func (f *Failure) Error() string {
	if f == nil {
		return "approval preparation failed"
	}
	if f.Detail == "" {
		return "intent/" + f.Reason
	}
	return "intent/" + f.Reason + ": " + f.Detail
}

func failure(reason, detail string, exit contract.ExitCode) error {
	return &Failure{Reason: reason, Detail: detail, Exit: exit}
}

// Request contains already-resolved project identity plus command-local
// inputs. HashPrefix is nil for card generation. Source and candidate paths
// may be supplied by an adapter; otherwise they are derived from project.yaml.
type Request struct {
	Project                 *project.Resolution
	Slug                    string
	HashPrefix              string
	By                      string
	RunDir                  string
	AcceptanceSourcePath    string
	AcceptanceCandidatePath string
	BaseEnvironment         process.Environment
	KogenRuntimePaths       []string
	AdapterStackHomes       []string
	AdapterVersion          string
	Toolchain               map[string]string
	ToolchainKnown          bool
}

// ScratchRequest asks for a private, detached checkout of the resolved base.
// The implementation must not reuse a dirty user checkout or share hardlinks.
type ScratchRequest struct {
	Origin     string
	RunDir     string
	BaseCommit contract.ObjectID
	BaseTree   contract.ObjectID
}

// ScratchWorkspace is a disposable, private checkout. ResetToBase restores the
// exact resolved base after a mutating check; Close releases its private files.
type ScratchWorkspace interface {
	Directory() string
	Tree() contract.ObjectID
	ResetToBase(context.Context) error
	Close() error
}

// ScratchPort materializes a detached checkout from an immutable base commit.
type ScratchPort interface {
	OpenExactBase(context.Context, ScratchRequest) (ScratchWorkspace, error)
}

// SetupRequest is the normalized v2 setup-cache input. Setup cache identity is
// independent from the verification-baseline identity.
type SetupRequest struct {
	BaseTree       string
	Workspace      string
	RunDir         string
	OS             string
	Arch           string
	Toolchain      map[string]string
	ToolchainKnown bool
	Inputs         []string
	Outputs        []string
	Checks         []contract.CheckSpec
	ChildEnv       process.Environment
	KeyEnvironment process.Environment
}

// String and GoString omit paths and environment values from setup-cache
// diagnostics. Those values can expose local project details or credentials.
func (r SetupRequest) String() string {
	return fmt.Sprintf("SetupRequest{v=2 base_tree=%q inputs=%d outputs=%d checks=%d}", r.BaseTree, len(r.Inputs), len(r.Outputs), len(r.Checks))
}

func (r SetupRequest) GoString() string { return r.String() }

// SetupResult reports the v2 setup key returned by the setup-cache port.
type SetupResult struct {
	Key    string
	Reused bool
}

// SetupCachePort runs setup through its v2 cache, and restores the cached
// outputs after a scratch checkout is reset. Implementations belong to package
// setupcache; this package never treats a setup hit as a baseline hit.
type SetupCachePort interface {
	Run(context.Context, SetupRequest, func(context.Context) error) (SetupResult, error)
	Restore(context.Context, string, string) error
}

// BaselineKey is the independent v3 identity for exact-base check observations.
// Digest is the SHA-256 of the canonical v3 document. Cacheable is false when
// any required toolchain/setup/adapter identity is unknown.
type BaselineKey struct {
	Version          int
	CheckedBaseTree  string
	SetupKey         string
	Checks           []contract.CheckSpec
	ChildEnvironment []string
	Toolchain        map[string]string
	OS               string
	Arch             string
	AdapterVersion   string
	Digest           string
	Cacheable        bool
}

// String and GoString redact all key inputs, which can contain local paths or
// environment values. The digest is the only safe cache identity to log.
func (k BaselineKey) String() string {
	return fmt.Sprintf("BaselineKey{v=%d digest=%q cacheable=%t}", k.Version, k.Digest, k.Cacheable)
}

func (k BaselineKey) GoString() string { return k.String() }

type baselineKeyDocument struct {
	Version          int               `json:"v"`
	CheckedBaseTree  string            `json:"checked_base_tree"`
	SetupKey         string            `json:"setup_key"`
	Checks           []checkKey        `json:"checks"`
	ChildEnvironment []string          `json:"child_env"`
	Toolchain        map[string]string `json:"toolchain"`
	OS               string            `json:"os"`
	Arch             string            `json:"arch"`
	AdapterVersion   string            `json:"adapter_version"`
}

type checkKey struct {
	Name    string   `json:"name"`
	Adapter string   `json:"adapter"`
	Program string   `json:"program"`
	Args    []string `json:"args"`
	Timeout int64    `json:"timeout_ms"`
}

// NewBaselineKey builds the canonical v3 baseline identity. It preserves check
// order and full deadlines. An unknown identity prevents cache reuse while
// still allowing the caller to run and return fresh observations.
func NewBaselineKey(baseTree, setupKey string, checks []contract.CheckSpec, childEnv process.Environment, toolchain map[string]string, toolchainKnown bool, osName, arch, adapterVersion string) (BaselineKey, error) {
	if err := gitio.ValidateObjectID(contract.ObjectID(baseTree)); err != nil {
		return BaselineKey{}, fmt.Errorf("approval baseline: invalid checked base tree: %w", err)
	}
	env, err := canonicalEnvironment(childEnv)
	if err != nil {
		return BaselineKey{}, err
	}
	document := baselineKeyDocument{
		Version: 3, CheckedBaseTree: baseTree, SetupKey: setupKey,
		Checks: make([]checkKey, len(checks)), ChildEnvironment: env,
		Toolchain: cloneStrings(toolchain), OS: osName, Arch: arch,
		AdapterVersion: adapterVersion,
	}
	for index, spec := range checks {
		if spec.Name == "" || spec.Program == "" || spec.Timeout <= 0 || spec.Timeout%time.Millisecond != 0 {
			return BaselineKey{}, fmt.Errorf("approval baseline: check %d is not normalized", index+1)
		}
		document.Checks[index] = checkKey{
			Name: spec.Name, Adapter: spec.Adapter, Program: spec.Program,
			Args:    append([]string(nil), spec.Args...),
			Timeout: spec.Timeout.Milliseconds(),
		}
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return BaselineKey{}, fmt.Errorf("approval baseline: encode v3 identity: %w", err)
	}
	digest := intent.IntentSHA256(encoded)
	cacheable := sha256Pattern.MatchString(setupKey) && toolchainKnown &&
		strings.TrimSpace(osName) != "" && strings.TrimSpace(arch) != "" && strings.TrimSpace(adapterVersion) != ""
	for name, version := range toolchain {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(version) == "" {
			cacheable = false
		}
	}
	return BaselineKey{
		Version: 3, CheckedBaseTree: baseTree, SetupKey: setupKey,
		Checks: identityChecks(checks), ChildEnvironment: env,
		Toolchain: cloneStrings(toolchain), OS: osName, Arch: arch,
		AdapterVersion: adapterVersion, Digest: digest, Cacheable: cacheable,
	}, nil
}

// BaselineRow is the approval.json representation of one configured check.
// Line numbers are retained for card rendering but excluded from JSON.
type BaselineRow struct {
	Name       string               `json:"name"`
	Status     contract.CheckStatus `json:"status"`
	ExitStatus *int                 `json:"exit_status"`
	Findings   []BaselineFinding    `json:"findings"`
}

type BaselineFinding struct {
	Path    string  `json:"path"`
	Rule    string  `json:"rule"`
	Symbol  string  `json:"symbol"`
	Message string  `json:"message"`
	Line    *uint32 `json:"-"`
}

// BaselineResult is returned by the independent setupcache v3 port.
type BaselineResult struct {
	Rows   []BaselineRow
	Reused bool
}

// BaselineV3Port reads/writes only v3 baseline entries. GetOrCompute must
// ignore legacy entries and must call compute on a miss or when key.Cacheable
// is false.
type BaselineV3Port interface {
	GetOrCompute(context.Context, BaselineKey, func(context.Context) ([]BaselineRow, error)) (BaselineResult, error)
}

// Dependencies are the effect ports needed to prepare a card or approval.
// There is intentionally no approval-ref publisher here; approval/publish owns
// the later compare-and-swap and late source re-read.
type Dependencies struct {
	Git       contract.GitPort
	Policy    func(string) contract.GitPolicy
	Roots     contract.RootOpener
	Processes contract.ProcessRunner
	Checks    contract.AcceptanceAdapter
	Trees     acceptance.TreeSnapshotter
	Scratch   ScratchPort
	Setup     SetupCachePort
	Baselines BaselineV3Port
	Manifest  ManifestBuilder
}

// ManifestBuilder allows the production protection package to be replaced by
// a deterministic effect fake in component tests.
type ManifestBuilder interface {
	Build(context.Context, contract.GitPort, contract.GitPolicy, protection.BuildOptions) (*protection.BuildResult, error)
}

// ProtectionBuilder delegates manifest construction and stale-checkout checks
// to the shared protection implementation.
type ProtectionBuilder struct{}

func (ProtectionBuilder) Build(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, options protection.BuildOptions) (*protection.BuildResult, error) {
	return protection.BuildManifest(ctx, git, policy, options)
}

// Prepared is the immutable observation consumed by the later approval
// publisher. SourceBytes must be re-read and re-hashed immediately before CAS.
type Prepared struct {
	Intent                  *intent.Intent
	IntentBytes             []byte
	AcceptanceBytes         []byte
	IntentSHA256            string
	ApprovalSHA256          string
	Approver                string
	BaseCommit              contract.ObjectID
	BaseTree                contract.ObjectID
	AcceptanceSourcePath    string
	AcceptanceCandidatePath string
	ProtectedManifest       protection.Manifest
	CheckBaseline           []BaselineRow
	Warnings                []Warning
	Card                    string
	IsCard                  bool
	SetupReused             bool
	BaselineReused          bool
}

// Prepare computes the exact source hash before any setup/check work. A
// mismatching supplied prefix returns immediately without resolving identity,
// base, manifest, scratch checkout, or caches.
func Prepare(ctx context.Context, request Request, deps Dependencies) (prepared *Prepared, resultErr error) {
	if ctx == nil {
		return nil, errors.New("approval preparation: context is required")
	}
	if request.Project == nil || request.Project.Checkout == "" || request.Project.Origin == "" || request.Project.Base == "" {
		return nil, failure("project_unavailable", "resolved project and base are required", contract.ExitCode(3))
	}
	if !validSlug(request.Slug) {
		return nil, failure("invalid_slug", "Slug must use lowercase letters, digits, and dashes.", contract.ExitCode(2))
	}
	if strings.ContainsAny(request.By, "\r\n") {
		return nil, failure("approval_by_invalid", "--by must be a single line", contract.ExitCode(2))
	}
	if request.HashPrefix != "" && !approvalPrefixPattern.MatchString(request.HashPrefix) {
		return nil, failure("approval_hash_invalid", "hash must be 6 to 64 lowercase hexadecimal characters", contract.ExitCode(2))
	}
	if !cleanAbsolute(request.Project.Checkout) || !cleanAbsolute(request.Project.Origin) {
		return nil, failure("project_unavailable", "checkout and origin paths must be clean absolute paths", contract.ExitCode(3))
	}
	roots := deps.Roots
	if roots == nil {
		roots = safefs.Opener{}
	}
	checkout, err := roots.OpenRoot(request.Project.Checkout)
	if err != nil {
		return nil, failure("parse", "the Intent cannot be read", contract.ExitCode(1))
	}
	defer closeRoot(checkout)
	intentPath := path.Join(".kogen", "intents", request.Slug, "intent.md")
	intentBytes, err := readRegular(checkout, intentPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, failure("not_found", "Intent does not exist", contract.ExitCode(2))
		}
		return nil, failure("parse", "the Intent cannot be read", contract.ExitCode(1))
	}
	sourcePath, candidatePath, err := acceptancePaths(checkout, request.Project, request.Slug, request.AcceptanceSourcePath, request.AcceptanceCandidatePath)
	if err != nil {
		return nil, failure("acceptance_missing", err.Error(), contract.ExitCode(2))
	}
	var acceptanceBytes []byte
	if request.HashPrefix != "" {
		acceptanceBytes, err = readRegular(checkout, sourcePath)
		if err != nil {
			return nil, failure("acceptance_missing", sourcePath+" does not exist", contract.ExitCode(2))
		}
	}
	approvalHash := intent.ApprovalSHA256(intentBytes, acceptanceBytes)
	if request.HashPrefix != "" && !strings.HasPrefix(approvalHash, request.HashPrefix) {
		return nil, failure("hash_mismatch", fmt.Sprintf("%s is now %s, not %s; review it again with kogen intent approve %s", request.Slug, approvalHash[:8], request.HashPrefix, request.Slug), contract.ExitCode(1))
	}
	parsed, err := intent.Parse(request.Slug, intentBytes)
	if err != nil {
		return nil, failure("parse", "the Intent cannot be read\n"+err.Error(), contract.ExitCode(1))
	}
	if lintErrors := blockingLint(parsed); len(lintErrors) != 0 {
		var detail strings.Builder
		detail.WriteString("the Intent needs changes")
		for _, issue := range lintErrors {
			detail.WriteByte('\n')
			detail.WriteString(issue.Rule)
			if issue.Line != nil {
				fmt.Fprintf(&detail, " at line %d", *issue.Line)
			}
			detail.WriteString(": ")
			detail.WriteString(issue.Message)
		}
		return nil, failure("lint", detail.String(), contract.ExitCode(1))
	}
	if request.HashPrefix == "" {
		acceptanceBytes, err = readRegular(checkout, sourcePath)
		if err != nil {
			return nil, failure("acceptance_missing", sourcePath+" does not exist", contract.ExitCode(2))
		}
		approvalHash = intent.ApprovalSHA256(intentBytes, acceptanceBytes)
	}
	if err := requirePreparationPorts(deps); err != nil {
		return nil, err
	}
	approver, err := resolveApprover(ctx, deps, request.Project.Checkout, request.By)
	if err != nil {
		return nil, err
	}
	policy := deps.Policy(request.Project.Origin)
	refs := gitio.NewRefPort(deps.Git, policy)
	baseCommit, err := refs.ResolveCommit(ctx, request.Project.Base)
	if err != nil {
		return nil, failure("base_unavailable", "could not resolve the configured base", contract.ExitCode(3))
	}
	baseTree, err := refs.ResolveTree(ctx, string(baseCommit))
	if err != nil {
		return nil, failure("base_unavailable", "could not resolve the configured base tree", contract.ExitCode(3))
	}
	manifestBuilder := deps.Manifest
	if manifestBuilder == nil {
		manifestBuilder = ProtectionBuilder{}
	}
	manifestResult, err := manifestBuilder.Build(ctx, deps.Git, policy, protection.BuildOptions{
		BaseCommit: baseCommit, CheckoutRoot: request.Project.Checkout,
		Config: request.Project.Config, Intent: parsed,
		CandidatePath: candidatePath, CandidateBytes: acceptanceBytes,
	})
	if err != nil {
		var behind *protection.CheckoutBehindBaseError
		if errors.As(err, &behind) {
			paths := behind.Paths
			if len(paths) > 2 {
				paths = paths[:2]
			}
			return nil, failure("checkout_behind_base", fmt.Sprintf("checkout is behind %s: %d paths differ (%s); update your checkout first", request.Project.Base, len(behind.Paths), strings.Join(paths, ", ")), contract.ExitCode(3))
		}
		return nil, failure("approval_manifest_failed", "could not build the protected manifest", contract.ExitCode(3))
	}
	if manifestResult == nil {
		return nil, failure("approval_manifest_failed", "protected manifest builder returned no result", contract.ExitCode(3))
	}

	result := &Prepared{
		Intent: parsed, IntentBytes: bytes.Clone(intentBytes), AcceptanceBytes: bytes.Clone(acceptanceBytes),
		IntentSHA256: intent.IntentSHA256(intentBytes), ApprovalSHA256: approvalHash,
		Approver: approver, BaseCommit: baseCommit, BaseTree: baseTree,
		AcceptanceSourcePath: sourcePath, AcceptanceCandidatePath: candidatePath,
		ProtectedManifest: manifestResult.Manifest, IsCard: request.HashPrefix == "",
	}
	result.Warnings = matchingShapeWarnings(checkout, request.Slug, approvalHash)
	result.Warnings = mergeStyleWarnings(result.Warnings, parsed)

	if request.RunDir == "" || !cleanAbsolute(request.RunDir) {
		return nil, failure("approval_check_failed", "approval run directory must be a clean absolute path", contract.ExitCode(3))
	}
	baseWorkspace, err := deps.Scratch.OpenExactBase(ctx, ScratchRequest{
		Origin: request.Project.Origin, RunDir: request.RunDir,
		BaseCommit: baseCommit, BaseTree: baseTree,
	})
	if err != nil {
		return nil, failure("approval_check_failed", "could not create exact-base scratch checkout", contract.ExitCode(3))
	}
	if baseWorkspace == nil {
		return nil, failure("approval_check_failed", "scratch port returned no checkout", contract.ExitCode(3))
	}
	defer func() {
		if closeErr := baseWorkspace.Close(); closeErr != nil {
			prepared = nil
			resultErr = errors.Join(resultErr, failure("approval_check_cleanup_failed", "could not clean up the exact-base scratch checkout", contract.ExitCode(3)))
		}
	}()
	if !cleanAbsolute(baseWorkspace.Directory()) || baseWorkspace.Tree() != baseTree {
		return nil, failure("approval_check_failed", "scratch checkout is not bound to the resolved base tree", contract.ExitCode(3))
	}
	scratchRoot, err := roots.OpenRoot(baseWorkspace.Directory())
	if err != nil {
		return nil, failure("approval_check_failed", "could not open exact-base scratch checkout", contract.ExitCode(3))
	}
	defer closeRoot(scratchRoot)
	if err := requireTree(ctx, deps.Trees, baseWorkspace.Directory(), string(baseTree)); err != nil {
		return nil, failure("approval_check_failed", "scratch checkout does not contain the exact resolved base tree", contract.ExitCode(3))
	}
	runRoot, err := roots.OpenRoot(request.RunDir)
	if err != nil {
		return nil, failure("approval_check_failed", "could not open approval run directory", contract.ExitCode(3))
	}
	defer closeRoot(runRoot)
	environment, err := buildEnvironment(ctx, request, deps, runRoot, baseWorkspace.Directory())
	if err != nil {
		return nil, failure("approval_check_failed", "could not construct the approval child environment", contract.ExitCode(3))
	}
	setupChecks, err := configuredChecks(request.Project.Config, "setup", environment)
	if err != nil {
		return nil, failure("approval_check_failed", "invalid setup configuration", contract.ExitCode(3))
	}
	baselineChecks, err := configuredChecks(request.Project.Config, "checks", environment)
	if err != nil {
		return nil, failure("approval_check_failed", "invalid check configuration", contract.ExitCode(3))
	}
	acceptanceChecks, err := configuredChecks(request.Project.Config, "acceptance_checks", environment)
	if err != nil {
		return nil, failure("approval_check_failed", "invalid acceptance-check configuration", contract.ExitCode(3))
	}
	inputs, outputs := setupPaths(request.Project.Config)
	setupRequest := SetupRequest{
		BaseTree: string(baseTree), Workspace: baseWorkspace.Directory(), RunDir: request.RunDir,
		OS: runtime.GOOS, Arch: runtime.GOARCH,
		Toolchain: cloneStrings(request.Toolchain), ToolchainKnown: request.ToolchainKnown,
		Inputs: inputs, Outputs: outputs, Checks: cloneChecks(setupChecks),
		ChildEnv: cloneEnvironment(environment), KeyEnvironment: process.SetupKeyEnvironment(environment),
	}
	setupResult, err := deps.Setup.Run(ctx, setupRequest, func(ctx context.Context) error {
		return runSetup(ctx, deps.Checks, baseWorkspace.Directory(), setupChecks)
	})
	if err != nil {
		return nil, setupFailure(err)
	}
	result.SetupReused = setupResult.Reused
	if err := requireTree(ctx, deps.Trees, baseWorkspace.Directory(), string(baseTree)); err != nil {
		return nil, failure("setup_failed", "setup changed the resolved base tree", contract.ExitCode(3))
	}
	baselineEnv, err := canonicalEnvironment(process.SetupKeyEnvironment(environment))
	if err != nil {
		return nil, failure("approval_check_failed", "could not canonicalize the approval environment", contract.ExitCode(3))
	}
	keyEnvironment := process.SetupKeyEnvironment(environment)
	baselineKey, err := NewBaselineKey(string(baseTree), setupResult.Key, baselineChecks, keyEnvironment, request.Toolchain, request.ToolchainKnown, runtime.GOOS, runtime.GOARCH, request.AdapterVersion)
	if err != nil {
		return nil, failure("approval_check_failed", "could not construct the independent baseline v3 identity", contract.ExitCode(3))
	}
	// BaselineKey owns the canonical environment form, while this check catches
	// accidental differences between the environment used for checks and key.
	if !equalStrings(baselineEnv, baselineKey.ChildEnvironment) {
		return nil, failure("approval_check_failed", "baseline environment identity is inconsistent", contract.ExitCode(3))
	}
	computed := false
	baseline, err := deps.Baselines.GetOrCompute(ctx, baselineKey, func(ctx context.Context) ([]BaselineRow, error) {
		computed = true
		return runBaseline(ctx, deps, baseWorkspace, baseTree, setupResult.Key, baselineChecks)
	})
	if err != nil {
		return nil, failure("approval_check_failed", "could not run or load the base check baseline", contract.ExitCode(3))
	}
	if baseline.Reused && !baselineKey.Cacheable {
		return nil, failure("approval_check_failed", "baseline port reused an uncacheable v3 identity", contract.ExitCode(3))
	}
	if !baseline.Reused && !computed {
		return nil, failure("approval_check_failed", "baseline port returned a miss without running checks", contract.ExitCode(3))
	}
	if err := validateBaselineRows(baseline.Rows, baselineChecks); err != nil {
		return nil, failure("approval_check_failed", "baseline port returned invalid rows", contract.ExitCode(3))
	}
	result.CheckBaseline = cloneBaseline(baseline.Rows)
	result.BaselineReused = baseline.Reused
	if len(acceptanceChecks) != 0 {
		if err := stageAndCheck(ctx, deps, scratchRoot, baseWorkspace.Directory(), candidatePath, acceptanceBytes, acceptanceChecks); err != nil {
			return nil, err
		}
	}
	if result.IsCard {
		result.Card = RenderCard(parsed, request.Project.Base, string(baseCommit), approvalHash, approver, result.Warnings, result.CheckBaseline)
	}
	return result, nil
}

func requirePreparationPorts(deps Dependencies) error {
	if deps.Git == nil || deps.Policy == nil || deps.Processes == nil || deps.Checks == nil || deps.Trees == nil || deps.Scratch == nil || deps.Setup == nil || deps.Baselines == nil {
		return failure("approval_check_failed", "approval preparation effect ports are unavailable", contract.ExitCode(3))
	}
	return nil
}

func resolveApprover(ctx context.Context, deps Dependencies, checkout, by string) (string, error) {
	if strings.TrimSpace(by) != "" {
		return by, nil
	}
	result, err := deps.Git.Exec(ctx, []string{"var", "GIT_AUTHOR_IDENT"}, nil, deps.Policy(checkout))
	if err != nil || result.Process.ExitStatus == nil || *result.Process.ExitStatus != 0 || result.Process.TimedOut || result.Process.Unavailable {
		return "", failure("approval_identity_unavailable", "configure git user.name and user.email", contract.ExitCode(2))
	}
	identity := strings.TrimSpace(string(result.Stdout))
	match := authorIdentPattern.FindStringSubmatch(identity)
	if match == nil || strings.TrimSpace(match[1]) == "" {
		return "", failure("approval_identity_unavailable", "configure git user.name and user.email", contract.ExitCode(2))
	}
	return match[1], nil
}

func buildEnvironment(ctx context.Context, request Request, deps Dependencies, runRoot contract.RootedFS, workspace string) (process.Environment, error) {
	base := request.BaseEnvironment
	if base == nil {
		base = process.HostEnvironment()
	}
	projectEnv := process.Environment{}
	if request.Project.Config != nil {
		if raw, ok := request.Project.Config.Raw["env"].(yamlmini.Mapping); ok {
			for key, value := range raw {
				text, valid := value.(string)
				if !valid {
					return nil, fmt.Errorf("project env %s is not a string", key)
				}
				projectEnv[key] = text
			}
		}
	}
	return process.BuildChildEnvironment(ctx, deps.Processes, process.EnvironmentRequest{
		Base: base, RunDir: request.RunDir, RunRoot: runRoot,
		ProjectRoot: workspace, Workspace: workspace,
		Project: projectEnv, KogenRuntimePaths: append([]string(nil), request.KogenRuntimePaths...),
		AdapterStackHomes: append([]string(nil), request.AdapterStackHomes...),
	})
}

func runSetup(ctx context.Context, checks contract.AcceptanceAdapter, workspace string, specs []contract.CheckSpec) error {
	for _, spec := range specs {
		result, err := checks.Run(ctx, workspace, spec)
		if err != nil {
			return fmt.Errorf("setup %s could not be supervised", spec.Name)
		}
		if normalizedStatus(result) != contract.CheckGreen {
			return fmt.Errorf("setup %s failed", spec.Name)
		}
	}
	return nil
}

func setupFailure(err error) error {
	if err == nil {
		return failure("setup_failed", "configured setup did not complete", contract.ExitCode(3))
	}
	return failure("setup_failed", "configured setup did not complete: "+err.Error(), contract.ExitCode(3))
}

func runBaseline(ctx context.Context, deps Dependencies, workspace ScratchWorkspace, baseTree contract.ObjectID, setupKey string, checks []contract.CheckSpec) ([]BaselineRow, error) {
	rows := make([]BaselineRow, 0, len(checks))
	for _, spec := range checks {
		if err := requireTree(ctx, deps.Trees, workspace.Directory(), string(baseTree)); err != nil {
			return nil, fmt.Errorf("baseline check %s did not start on the resolved base tree", spec.Name)
		}
		observed, err := deps.Checks.Run(ctx, workspace.Directory(), spec)
		if err != nil {
			return nil, fmt.Errorf("baseline check %s could not be supervised", spec.Name)
		}
		status := normalizedStatus(observed)
		if observed.TreeBefore != "" && observed.TreeBefore != string(baseTree) {
			return nil, fmt.Errorf("baseline check %s observed a different starting tree", spec.Name)
		}
		actualTree, snapshotErr := deps.Trees.Snapshot(ctx, workspace.Directory())
		if snapshotErr != nil {
			return nil, fmt.Errorf("baseline check %s could not be verified", spec.Name)
		}
		if actualTree != string(baseTree) || observed.TreeAfter != "" && observed.TreeAfter != string(baseTree) {
			status = contract.CheckMutating
		}
		parsed := findings.ParseGNU(observed.OutputTail)
		row := BaselineRow{Name: spec.Name, Status: status, ExitStatus: cloneInt(observed.ExitStatus), Findings: make([]BaselineFinding, 0, len(parsed))}
		for _, finding := range parsed {
			row.Findings = append(row.Findings, BaselineFinding{Path: finding.Path, Rule: finding.Rule, Symbol: finding.Symbol, Message: finding.Message, Line: cloneUint32(finding.Line)})
		}
		rows = append(rows, row)
		if status == contract.CheckMutating {
			if err := workspace.ResetToBase(ctx); err != nil {
				return nil, fmt.Errorf("baseline check %s left a tree that could not be reset", spec.Name)
			}
			if err := requireTree(ctx, deps.Trees, workspace.Directory(), string(baseTree)); err != nil {
				return nil, fmt.Errorf("baseline check %s reset did not restore the resolved base", spec.Name)
			}
			if err := deps.Setup.Restore(ctx, setupKey, workspace.Directory()); err != nil {
				return nil, fmt.Errorf("baseline check %s could not restore setup products", spec.Name)
			}
		}
	}
	return rows, nil
}

func stageAndCheck(ctx context.Context, deps Dependencies, root contract.RootedFS, workspace, candidatePath string, contents []byte, checks []contract.CheckSpec) (resultErr error) {
	if err := validateRelativePath(candidatePath); err != nil {
		return failure("acceptance_check_path_conflict", candidatePath, contract.ExitCode(3))
	}
	if _, err := root.Lstat(candidatePath); err == nil {
		return failure("acceptance_check_path_conflict", candidatePath, contract.ExitCode(3))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return failure("acceptance_check_path_conflict", candidatePath, contract.ExitCode(3))
	}
	parents, err := createSafeParents(root, path.Dir(candidatePath))
	if err != nil {
		removeParents(root, parents)
		return failure("acceptance_check_path_conflict", candidatePath, contract.ExitCode(3))
	}
	if err := root.Publish(candidatePath, contents, 0o644, safefs.PublicationCreateOnly); err != nil {
		removeParents(root, parents)
		return failure("acceptance_check_path_conflict", candidatePath, contract.ExitCode(3))
	}
	defer func() {
		if err := root.Remove(candidatePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			resultErr = errors.Join(resultErr, failure("acceptance_check_cleanup_failed", "could not restore the staged acceptance path", contract.ExitCode(3)))
		}
		removeParents(root, parents)
	}()
	for _, spec := range checks {
		resolved := spec
		resolved.Program = strings.ReplaceAll(resolved.Program, "{path}", filepath.Join(workspace, filepath.FromSlash(candidatePath)))
		resolved.Args = append([]string(nil), resolved.Args...)
		for index := range resolved.Args {
			resolved.Args[index] = strings.ReplaceAll(resolved.Args[index], "{path}", filepath.Join(workspace, filepath.FromSlash(candidatePath)))
		}
		observed, err := deps.Checks.Run(ctx, workspace, resolved)
		if err != nil {
			return failure("approval_check_failed", "acceptance check could not be supervised", contract.ExitCode(3))
		}
		staged, stagedErr := readRegular(root, candidatePath)
		changedTree := observed.TreeBefore != "" && observed.TreeAfter != "" && observed.TreeBefore != observed.TreeAfter
		if stagedErr != nil || !bytes.Equal(staged, contents) || normalizedStatus(observed) == contract.CheckMutating || changedTree {
			return failure("acceptance_check_mutated", "acceptance check changed the staged test or scratch tree", contract.ExitCode(3))
		}
		status := normalizedStatus(observed)
		if status == contract.CheckUnavailable || observed.ExitStatus != nil && (*observed.ExitStatus == 126 || *observed.ExitStatus == 127) {
			return failure("tool_missing", "acceptance check command is unavailable", contract.ExitCode(3))
		}
		if status != contract.CheckGreen {
			return failure("acceptance_check_failed", "a configured acceptance check failed", contract.ExitCode(1))
		}
	}
	return nil
}

func configuredChecks(config *project.Config, field string, environment process.Environment) ([]contract.CheckSpec, error) {
	if config == nil {
		return nil, nil
	}
	rows, exists := config.Raw[field]
	if !exists {
		return nil, nil
	}
	sequence, ok := rows.(yamlmini.Sequence)
	if !ok {
		return nil, fmt.Errorf("%s must be a sequence", field)
	}
	env, err := canonicalEnvironment(environment)
	if err != nil {
		return nil, err
	}
	specs := make([]contract.CheckSpec, 0, len(sequence))
	for index, row := range sequence {
		mapping, ok := row.(yamlmini.Mapping)
		if !ok {
			return nil, fmt.Errorf("%s[%d] must be a map", field, index+1)
		}
		name, _ := mapping["name"].(string)
		args, ok := mapping["argv"].(yamlmini.Sequence)
		if !ok || len(args) == 0 {
			return nil, fmt.Errorf("%s[%d].argv must be nonempty", field, index+1)
		}
		argv := make([]string, 0, len(args))
		for _, arg := range args {
			text, ok := arg.(string)
			if !ok || text == "" || strings.ContainsRune(text, '\x00') {
				return nil, fmt.Errorf("%s[%d].argv must contain strings", field, index+1)
			}
			argv = append(argv, text)
		}
		timeoutText, _ := mapping["timeout_ms"].(string)
		timeoutMS, err := strconv.ParseInt(timeoutText, 10, 64)
		if err != nil || timeoutMS <= 0 || timeoutMS > int64((1<<63-1)/int64(time.Millisecond)) {
			return nil, fmt.Errorf("%s[%d].timeout_ms must be positive", field, index+1)
		}
		specs = append(specs, contract.CheckSpec{
			Name: name, Adapter: "command", Program: argv[0], Args: argv[1:],
			Env: append([]string(nil), env...), Timeout: time.Duration(timeoutMS) * time.Millisecond,
		})
	}
	return specs, nil
}

func setupPaths(config *project.Config) ([]string, []string) {
	if config == nil {
		return nil, nil
	}
	return configPaths(config.Raw["setup_inputs"]), configPaths(config.Raw["setup_outputs"])
}

func configPaths(value any) []string {
	sequence, ok := value.(yamlmini.Sequence)
	if !ok {
		return nil
	}
	paths := make([]string, 0, len(sequence))
	for _, item := range sequence {
		if text, ok := item.(string); ok {
			paths = append(paths, text)
		}
	}
	return paths
}

func acceptancePaths(root contract.RootedFS, resolved *project.Resolution, slug, sourceOverride, candidateOverride string) (string, string, error) {
	extension, candidateDir := "", "test/acceptance"
	adapter := ""
	if resolved.Config != nil {
		if mapping, ok := resolved.Config.Raw["acceptance"].(yamlmini.Mapping); ok {
			adapter, _ = mapping["adapter"].(string)
			if value, ok := mapping["ext"].(string); ok {
				extension = value
			}
			if value, ok := mapping["candidate_dir"].(string); ok {
				candidateDir = value
			}
		}
	}
	if extension == "" {
		switch adapter {
		case "rails":
			extension = "_test.rb"
		case "exunit":
			extension = "_test.exs"
		case "command":
			return "", "", errors.New("command acceptance requires ext")
		default:
			if present(root, "Gemfile") && present(root, "config/application.rb") {
				extension = "_test.rb"
			} else if present(root, "mix.exs") {
				extension = "_test.exs"
			} else {
				extension = ".t.sh"
			}
		}
	}
	if strings.ContainsAny(extension, "/\\\x00\r\n") || extension == "." || extension == ".." {
		return "", "", errors.New("invalid acceptance extension")
	}
	if sourceOverride == "" {
		sourceOverride = path.Join(".kogen", "acceptance", slug+extension)
	}
	if candidateOverride == "" {
		candidateOverride = path.Join(candidateDir, slug+extension)
	}
	if err := validateRelativePath(sourceOverride); err != nil {
		return "", "", errors.New("invalid acceptance source path")
	}
	if err := validateRelativePath(candidateOverride); err != nil {
		return "", "", errors.New("invalid acceptance candidate path")
	}
	return sourceOverride, candidateOverride, nil
}

func present(root contract.RootedFS, name string) bool {
	_, err := root.Lstat(name)
	return err == nil
}

type shapeWarningsDocument struct {
	ApprovalSHA256 string    `json:"approval_sha256"`
	Warnings       []Warning `json:"warnings"`
}

// Warning is one user-facing approval warning from Shape or Intent lint.
type Warning struct {
	Code    string   `json:"code"`
	ItemIDs []string `json:"item_ids"`
	Message string   `json:"message"`
}

func matchingShapeWarnings(root contract.RootedFS, slug, hash string) []Warning {
	data, err := readRegular(root, path.Join(".kogen", "intents", slug, "shape-warnings.json"))
	if err != nil {
		return nil
	}
	var document shapeWarningsDocument
	if json.Unmarshal(data, &document) != nil || document.ApprovalSHA256 != hash {
		return nil
	}
	return cloneWarnings(document.Warnings)
}

func mergeStyleWarnings(warnings []Warning, parsed *intent.Intent) []Warning {
	result := cloneWarnings(warnings)
	for _, issue := range parsed.Lint() {
		if issue.Severity != intent.LintStyle {
			continue
		}
		ids := []string(nil)
		if issue.Line != nil {
			for _, item := range parsed.Acceptance {
				if item.Line == *issue.Line {
					ids = []string{item.ID}
					break
				}
			}
		}
		warning := Warning{Code: "lint_" + issue.Rule, ItemIDs: ids, Message: issue.Message}
		duplicate := false
		for _, existing := range result {
			if existing.Code == warning.Code && equalStrings(existing.ItemIDs, warning.ItemIDs) && existing.Message == warning.Message {
				duplicate = true
				break
			}
		}
		if !duplicate {
			result = append(result, warning)
		}
	}
	return result
}

func blockingLint(parsed *intent.Intent) []intent.LintIssue {
	var issues []intent.LintIssue
	for _, issue := range parsed.Lint() {
		if issue.Severity == intent.LintError {
			issues = append(issues, issue)
		}
	}
	return issues
}

func readRegular(root contract.RootedFS, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
		return nil, safefs.ErrUnsafeFile
	}
	return root.ReadFile(name)
}

func requireTree(ctx context.Context, trees acceptance.TreeSnapshotter, workspace, expected string) error {
	actual, err := trees.Snapshot(ctx, workspace)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("tree %s does not equal base %s", actual, expected)
	}
	return nil
}

func normalizedStatus(result contract.CheckResult) contract.CheckStatus {
	if result.Status != "" {
		return result.Status
	}
	switch {
	case result.TreeBefore != "" && result.TreeAfter != "" && result.TreeBefore != result.TreeAfter:
		return contract.CheckMutating
	case result.TimedOut:
		return contract.CheckTimeout
	case result.Unavailable || result.ExitStatus == nil || *result.ExitStatus == 126 || *result.ExitStatus == 127:
		return contract.CheckUnavailable
	case *result.ExitStatus == 0:
		return contract.CheckGreen
	default:
		return contract.CheckRed
	}
}

func validateBaselineRows(rows []BaselineRow, checks []contract.CheckSpec) error {
	if len(rows) != len(checks) {
		return fmt.Errorf("got %d rows for %d checks", len(rows), len(checks))
	}
	for index, row := range rows {
		if row.Name != checks[index].Name {
			return fmt.Errorf("baseline row %d has wrong check name", index+1)
		}
		switch row.Status {
		case contract.CheckGreen, contract.CheckRed, contract.CheckUnavailable, contract.CheckTimeout, contract.CheckMutating:
		default:
			return fmt.Errorf("baseline row %d has invalid status", index+1)
		}
	}
	return nil
}

func createSafeParents(root contract.RootedFS, parent string) ([]string, error) {
	if parent == "." || parent == "" {
		return nil, nil
	}
	if err := validateRelativePath(parent); err != nil {
		return nil, err
	}
	created := make([]string, 0)
	current := ""
	for _, segment := range strings.Split(parent, "/") {
		if current == "" {
			current = segment
		} else {
			current += "/" + segment
		}
		info, err := root.Lstat(current)
		if err == nil {
			if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
				return created, fmt.Errorf("unsafe acceptance parent %q", current)
			}
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return created, err
		}
		if err := root.MkdirAll(current, 0o755); err != nil {
			return created, err
		}
		created = append(created, current)
	}
	return created, nil
}

func removeParents(root contract.RootedFS, parents []string) {
	for index := len(parents) - 1; index >= 0; index-- {
		_ = root.Remove(parents[index])
	}
}

func canonicalEnvironment(environment process.Environment) ([]string, error) {
	keys := make([]string, 0, len(environment))
	for key, value := range environment {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("invalid child environment")
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]string, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, key+"="+environment[key])
	}
	return entries, nil
}

func cloneEnvironment(source process.Environment) process.Environment {
	result := make(process.Environment, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneStrings(source map[string]string) map[string]string {
	if source == nil {
		return map[string]string{}
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneChecks(source []contract.CheckSpec) []contract.CheckSpec {
	result := make([]contract.CheckSpec, len(source))
	for index, spec := range source {
		result[index] = spec
		result[index].Args = append([]string(nil), spec.Args...)
		result[index].Env = append([]string(nil), spec.Env...)
	}
	return result
}

func identityChecks(source []contract.CheckSpec) []contract.CheckSpec {
	result := cloneChecks(source)
	for index := range result {
		result[index].Env = nil
	}
	return result
}

func cloneBaseline(source []BaselineRow) []BaselineRow {
	result := make([]BaselineRow, len(source))
	for index, row := range source {
		result[index] = row
		result[index].ExitStatus = cloneInt(row.ExitStatus)
		result[index].Findings = make([]BaselineFinding, len(row.Findings))
		for findingIndex, finding := range row.Findings {
			result[index].Findings[findingIndex] = finding
			result[index].Findings[findingIndex].Line = cloneUint32(finding.Line)
		}
	}
	return result
}

func cloneWarnings(source []Warning) []Warning {
	result := make([]Warning, len(source))
	for index, warning := range source {
		result[index] = warning
		result[index].ItemIDs = append([]string(nil), warning.ItemIDs...)
	}
	return result
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneUint32(value *uint32) *uint32 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func validSlug(slug string) bool {
	if len(slug) < 3 || len(slug) > 48 || slug[0] == '-' || slug[len(slug)-1] == '-' || strings.Contains(slug, "--") {
		return false
	}
	for _, character := range slug {
		if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func validateRelativePath(value string) error {
	if value == "" || value == "." || strings.ContainsAny(value, "\\\x00\r\n") || !fs.ValidPath(value) || path.IsAbs(value) || path.Clean(value) != value {
		return errors.New("unsafe relative path")
	}
	for _, part := range strings.Split(value, "/") {
		if strings.EqualFold(part, ".git") {
			return errors.New("unsafe relative path")
		}
	}
	return nil
}

func cleanAbsolute(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsRune(value, '\x00')
}

func closeRoot(root contract.RootedFS) {
	if closer, ok := root.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
