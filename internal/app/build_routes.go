package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kogen-go/internal/acceptance"
	acceptancecommand "kogen-go/internal/acceptance/command"
	"kogen-go/internal/build/single"
	"kogen-go/internal/cli/parse"
	"kogen-go/internal/contract"
	"kogen-go/internal/gate"
	"kogen-go/internal/gitio"
	"kogen-go/internal/landing/commit"
	"kogen-go/internal/landing/integrate"
	"kogen-go/internal/landing/publish"
	"kogen-go/internal/process"
	"kogen-go/internal/project"
	"kogen-go/internal/queue/drain"
	"kogen-go/internal/queue/schedule"
	"kogen-go/internal/recovery"
	"kogen-go/internal/recovery/preserve"
	"kogen-go/internal/safefs"
	"kogen-go/internal/status/derive"
	"kogen-go/internal/workspace"
	"kogen-go/internal/yamlmini"
)

type buildRoutes struct {
	drain     *drain.Controller
	recovery  *recovery.Controller
	preserver *preserve.Preserver
}

func (routes *buildRoutes) Close() error {
	if routes == nil {
		return nil
	}
	var result error
	if routes.recovery != nil {
		result = errors.Join(result, routes.recovery.Close())
		routes.recovery = nil
	}
	if routes.preserver != nil {
		result = errors.Join(result, routes.preserver.Close())
		routes.preserver = nil
	}
	return result
}

func (cli *CLI) queue(command parse.Command, cwd string) int {
	ctx := context.Background()
	ports := cli.ports()
	resolved, exit, message := cli.resolveProject(ctx, command, cwd, ports)
	if exit != 0 {
		return cli.writeRawError(message, exit)
	}
	routes, err := newBuildRoutes(cli, ports, resolved)
	if err != nil {
		return writeBuildRouteError(cli, err)
	}
	defer routes.Close()

	if command.Route == parse.RouteQueueStop {
		_, err = routes.drain.Stop(ctx, resolved)
	} else {
		executable, _ := os.Executable()
		result, startErr := routes.drain.Start(ctx, resolved, drain.StartOptions{
			Detach: command.Detach, Executable: executable,
		})
		err = startErr
		if err == nil {
			return result.ExitCode
		}
	}
	if err != nil {
		return writeBuildRouteError(cli, err)
	}
	return 0
}

func newBuildRoutes(cli *CLI, ports runtimePorts, resolved *project.Resolution) (_ *buildRoutes, resultErr error) {
	if cli == nil || resolved == nil {
		return nil, errors.New("queue route requires a CLI and resolved project")
	}
	if err := ensureBuildStateRoot(resolved.StateRoot); err != nil {
		return nil, fmt.Errorf("prepare private Build state: %w", err)
	}
	canonicalStateRoot, err := filepath.EvalSymlinks(resolved.StateRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve private Build state: %w", err)
	}
	canonicalStateRoot, err = filepath.Abs(canonicalStateRoot)
	if err != nil || filepath.Clean(canonicalStateRoot) != canonicalStateRoot {
		return nil, errors.New("resolved private Build state is not a clean absolute path")
	}
	resolved.StateRoot = canonicalStateRoot
	originPolicy := gitio.OriginPolicy(resolved.Origin, ports.env)
	workspaceEnvironment := process.ControllerGitEnvironment(ports.env)
	workspaceGit := ports.workspaceGit
	originGit := ports.originGit
	workspacePolicy := gitio.WorkspacePolicy(resolved.Origin, workspaceEnvironment)

	preserver, err := preserve.New(preserve.Config{
		StateRoot: resolved.StateRoot, WorkspaceGit: workspaceGit,
		WorkspaceEnvironment: workspaceEnvironment, OriginGit: originGit,
		OriginPolicy: originPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize recovery preservation: %w", err)
	}
	closePreserver := true
	defer func() {
		if closePreserver {
			resultErr = errors.Join(resultErr, preserver.Close())
		}
	}()

	recoveryController, err := recovery.NewController(recovery.Config{
		StateRoot: resolved.StateRoot, Git: originGit, GitPolicy: originPolicy,
		Refs: gitio.NewRefPort(originGit, originPolicy), Writers: parentDeathWriters{}, Preserver: preserver,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize Build recovery: %w", err)
	}
	closeRecovery := true
	defer func() {
		if closeRecovery {
			resultErr = errors.Join(resultErr, recoveryController.Close())
		}
	}()

	factory := workspace.Factory{Git: workspaceGit, Policy: workspacePolicy}
	verifier := &buildGateVerifier{ports: ports, resolved: resolved, factory: factory}
	agent := cli.buildAgent
	if agent == nil {
		agent = unavailableBuildAgent{}
	}
	lander := buildLander{processes: ports.processes, environment: workspaceEnvironment, verifier: verifier}
	singleController, err := single.NewController(single.Dependencies{
		Approvals: single.GitApprovalSource{
			Git: originGit, Policy: func(directory string) contract.GitPolicy { return gitio.OriginPolicy(directory, ports.env) },
			Roots: safefs.Opener{},
		},
		Claims: single.GitClaimSource{
			Git: originGit, Policy: func(directory string) contract.GitPolicy { return gitio.OriginPolicy(directory, ports.env) },
		},
		Agent: agent, Workspaces: factory, Verifier: verifier, Lander: lander,
		Candidates: single.NewGitCandidatePreserver(ports.processes, ports.env),
		Git:        originGit, Processes: ports.processes, Environment: workspaceEnvironment,
		OriginPolicy: originPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize single-rung Build: %w", err)
	}
	recoveryPort := recoveryRoute{controller: recoveryController}
	snapshot := statusQueueSnapshot{ports: ports, resolved: resolved}
	queueController, err := drain.NewController(drain.Dependencies{
		Recovery: recoveryPort, Snapshot: snapshot, Build: drain.SingleBuilder{Controller: singleController},
		Output: cli.Out, Signals: cli.buildSignals,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize queue drain: %w", err)
	}
	closePreserver, closeRecovery = false, false
	return &buildRoutes{drain: queueController, recovery: recoveryController, preserver: preserver}, nil
}

type recoveryRoute struct{ controller *recovery.Controller }

func (route recoveryRoute) Recover(ctx context.Context, _ *project.Resolution) error {
	if route.controller == nil {
		return errors.New("recovery controller is unavailable")
	}
	report, err := route.controller.Recover(ctx)
	if err != nil {
		return err
	}
	if len(report.Issues) != 0 {
		issues := make([]string, 0, len(report.Issues))
		for _, issue := range report.Issues {
			// Approval preparation creates private run directories for its own
			// scratch work but does not publish Build run.json. Recovery reports
			// those empty directories as read_run/not-exist; they carry no Build
			// owner, workspace, or candidate to reconcile.
			if issue.Operation == "read_run" && strings.Contains(issue.Detail, "read run snapshot: no such file or directory") {
				continue
			}
			issues = append(issues, issue.RunID+"/"+issue.Operation+": "+issue.Detail)
		}
		if len(issues) != 0 {
			return fmt.Errorf("recovery reported issues: %s", strings.Join(issues, "; "))
		}
	}
	return nil
}

// A dead run owner cannot still hold an HTTP response or a supervised child
// group: process.Supervisor's guardian owns child cleanup when its parent dies.
type parentDeathWriters struct{}

func (parentDeathWriters) StopRun(ctx context.Context, _ recovery.RunIdentity) error {
	return ctx.Err()
}

type statusQueueSnapshot struct {
	ports    runtimePorts
	resolved *project.Resolution
}

func (snapshot statusQueueSnapshot) Load(ctx context.Context, resolved *project.Resolution) ([]schedule.QueueApproval, error) {
	if snapshot.resolved == nil || resolved == nil || snapshot.resolved.Origin != resolved.Origin || snapshot.ports.originGit == nil {
		return nil, errors.New("queue snapshot is not bound to the resolved origin")
	}
	status, err := inspectSyntheticStatus(ctx, snapshot.ports, resolved)
	if err != nil {
		return nil, err
	}
	refs, err := readApprovalRefs(ctx, snapshot.ports, resolved)
	if err != nil {
		return nil, err
	}
	queue := make([]schedule.QueueApproval, 0, len(status.Board.Queue))
	for _, row := range status.Board.Rows {
		if row.Status != derive.Approved && row.Status != derive.Blocked {
			continue
		}
		ref, exists := refs[row.Intent.Slug]
		if !exists {
			continue
		}
		approvalKey, branch := snapshot.approvalKey(ctx, resolved, row.Intent.Slug, ref.commit)
		queue = append(queue, schedule.QueueApproval{
			Slug: row.Intent.Slug, ApprovalTime: ref.timestamp, Priority: int64(row.Intent.Priority),
			ApprovalKey: approvalKey, ApprovalCommit: ref.commit, TargetBranch: branch,
			Blocked: row.Status == derive.Blocked,
		})
	}
	return queue, nil
}

func (snapshot statusQueueSnapshot) approvalKey(ctx context.Context, resolved *project.Resolution, slug, commitID string) (string, string) {
	branch := resolved.Base
	key := commitID
	policy := gitio.OriginPolicy(resolved.Origin, snapshot.ports.env)
	result, err := snapshot.ports.originGit.Exec(ctx, []string{"show", commitID + ":.kogen/intents/" + slug + "/approval.json"}, nil, policy)
	if err != nil || !success(result.Process) {
		return key, branch
	}
	var document struct {
		ApprovalSHA256 string `json:"approval_sha256"`
		TargetBranch   string `json:"target_branch"`
	}
	if json.Unmarshal(result.Stdout, &document) != nil {
		return key, branch
	}
	if document.ApprovalSHA256 != "" {
		key = document.ApprovalSHA256
	}
	if document.TargetBranch != "" {
		branch = document.TargetBranch
	}
	return key, branch
}

func ensureBuildStateRoot(stateRoot string) error {
	if !filepath.IsAbs(stateRoot) || filepath.Clean(stateRoot) != stateRoot || stateRoot == string(filepath.Separator) {
		return errors.New("state root must be a clean absolute directory")
	}
	root, err := safefs.OpenRoot(string(filepath.Separator))
	if err != nil {
		return err
	}
	defer root.Close()
	name := strings.TrimPrefix(stateRoot, string(filepath.Separator))
	if err := root.MkdirAll(filepath.ToSlash(name), 0o700); err != nil {
		return err
	}
	info, err := root.Lstat(filepath.ToSlash(name))
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("state root must be a private real directory")
	}
	parent := filepath.ToSlash(filepath.Dir(name))
	if parent == "." {
		parent = ""
	}
	return root.SyncDir(parent)
}

type unavailableBuildAgent struct{}

func (unavailableBuildAgent) Plan(context.Context, single.PlanRequest) (single.PlanResult, error) {
	return single.PlanResult{}, &contract.Failure{
		Class: "provider", Reason: "build_agent_unavailable", Exit: 4,
		Message: "provider/build_agent_unavailable: planner and builder adapter is not configured",
	}
}

func (unavailableBuildAgent) Develop(context.Context, single.DevelopRequest) (single.Development, error) {
	return single.Development{}, &contract.Failure{
		Class: "provider", Reason: "build_agent_unavailable", Exit: 4,
		Message: "provider/build_agent_unavailable: planner and builder adapter is not configured",
	}
}

type buildGateVerifier struct {
	ports    runtimePorts
	resolved *project.Resolution
	factory  workspace.Factory
}

func (verifier *buildGateVerifier) Verify(ctx context.Context, request single.VerifyRequest) (*gate.GateReport, error) {
	if verifier == nil || verifier.resolved == nil || request.Project == nil || request.Approval == nil || request.Workspace.Path == "" {
		return nil, errors.New("Build gate request is incomplete")
	}
	if request.Base.Commit == "" || request.Base.Tree == "" {
		return nil, errors.New("Build gate has no immutable base")
	}
	runID := filepath.Base(request.RunDir)
	if len(runID) != 32 {
		return nil, errors.New("Build gate run identity is invalid")
	}
	workspacesDir := filepath.Join(request.Project.StateRoot, "workspaces")
	baseWork, err := verifier.factory.Create(ctx, workspace.CloneRequest{
		Source: request.Project.Origin, WorkspacesDir: workspacesDir, RunID: runID,
		Rung: "R2", BaseCommit: request.Base.Commit,
	})
	if err != nil {
		return nil, fmt.Errorf("materialize checked base: %w", err)
	}
	defer func() { _ = removePrivateTree(workspacesDir, filepath.Base(baseWork.Path)) }()

	baseMetadata, err := gitio.LoadBaseMetadata(ctx, verifier.ports.workspaceGit, gitio.WorkspacePolicy(request.Project.Checkout, verifier.ports.env), request.Base.Commit)
	if err != nil {
		return nil, fmt.Errorf("load immutable gate base metadata: %w", err)
	}
	trees := acceptance.CandidateTree{
		Git: verifier.ports.workspaceGit, Policy: gitio.WorkspacePolicy(request.Workspace.Path, verifier.ports.env), Base: baseMetadata,
	}
	acceptanceConfig, err := gateAcceptanceConfig(request.Project, request.Approval)
	if err != nil {
		return nil, err
	}
	expected := make([]string, 0, len(request.Approval.Intent.Acceptance))
	for _, item := range request.Approval.Intent.Acceptance {
		expected = append(expected, item.ID)
	}
	changeItems := make([]string, 0, len(request.Approval.Intent.Verify))
	for _, item := range request.Approval.Intent.Verify {
		if item.IsChange() {
			changeItems = append(changeItems, item.ID)
		}
	}
	acceptanceRunner := buildAcceptanceRunner{
		processes: verifier.ports.processes, trees: trees, environment: verifier.ports.env,
		resolved: request.Project,
	}
	return gate.Run(ctx, gate.Request{
		Processes: verifier.ports.processes, AcceptanceRunner: acceptanceRunner, Trees: trees, Roots: safefs.Opener{},
		BaseWorkspace: baseWork.Path, CandidateWorkspace: request.Workspace.Path, RunDir: request.RunDir,
		ExpectedBaseTree: string(request.Base.Tree), ApprovalSHA256: request.Approval.ApprovalSHA256,
		Baseline: request.Baseline, Fixes: request.Approval.Fixes, Checks: request.Approval.Checks,
		Acceptance: gate.AcceptancePlan{
			Request:       acceptancecommand.Request{Config: acceptanceConfig, Slug: request.Approval.Slug, ExpectedItems: expected},
			ApprovedBytes: append([]byte(nil), request.Approval.AcceptanceBytes...), ChangeItems: changeItems,
		},
		Protection: request.Approval.Protection, HomeDir: verifier.ports.env["HOME"], TempDir: verifier.ports.env["TMPDIR"],
	})
}

type buildAcceptanceRunner struct {
	processes   contract.ProcessRunner
	trees       acceptance.TreeSnapshotter
	environment process.Environment
	resolved    *project.Resolution
}

func (runner buildAcceptanceRunner) Run(ctx context.Context, execution gate.AcceptanceExecution) (acceptance.Result, error) {
	if runner.processes == nil || runner.trees == nil || runner.resolved == nil {
		return acceptance.Result{}, errors.New("Build acceptance runner is not configured")
	}
	request := execution.Request
	root, err := safefs.OpenRoot(request.RunDir)
	if err != nil {
		return acceptance.Result{}, fmt.Errorf("open acceptance attempt root: %w", err)
	}
	defer root.Close()
	projectEnvironment, err := configuredProjectEnvironment(runner.resolved.Config)
	if err != nil {
		return acceptance.Result{}, err
	}
	childEnvironment, err := process.BuildChildEnvironment(ctx, runner.processes, process.EnvironmentRequest{
		Base: runner.environment, RunRoot: root, RunDir: request.RunDir, Workspace: request.Workdir,
		ProjectRoot: runner.resolved.Checkout, Project: projectEnvironment,
	})
	if err != nil {
		return acceptance.Result{}, fmt.Errorf("build acceptance environment: %w", err)
	}
	childEnvironment["KOGEN_TEST_SEED"] = execution.Seed
	childEnvironment["KOGEN_TEST_ATTEMPT"] = execution.Attempt
	request.Environment = childEnvironment
	return (&acceptancecommand.Runner{Processes: runner.processes, Trees: runner.trees, Roots: safefs.Opener{}}).Run(ctx, request)
}

func gateAcceptanceConfig(resolved *project.Resolution, approved *single.Approval) (acceptancecommand.Config, error) {
	if resolved == nil || approved == nil {
		return acceptancecommand.Config{}, errors.New("acceptance configuration requires a project and approval")
	}
	config := acceptancecommand.Config{
		Extension:    strings.TrimPrefix(filepath.Base(approved.AcceptanceSourcePath), approved.Slug),
		CandidateDir: filepath.ToSlash(filepath.Dir(approved.CandidatePath)),
		Run:          []string{"sh", "{path}"}, Timeout: acceptance.NormalizeTimeout(0),
	}
	if config.CandidateDir == "." {
		config.CandidateDir = "test/acceptance"
	}
	if resolved.Config == nil {
		return config, config.Validate()
	}
	settings, ok := resolved.Config.Raw["acceptance"].(yamlmini.Mapping)
	if !ok {
		return config, config.Validate()
	}
	if adapter, _ := settings["adapter"].(string); adapter != "" && adapter != "command" {
		return acceptancecommand.Config{}, fmt.Errorf("acceptance adapter %q is not wired into the first Build route", adapter)
	}
	if extension, ok := settings["ext"].(string); ok && extension != "" {
		config.Extension = extension
	}
	if candidateDir, ok := settings["candidate_dir"].(string); ok && candidateDir != "" {
		config.CandidateDir = candidateDir
	}
	if run, ok := settings["run"].(yamlmini.Sequence); ok {
		config.Run = make([]string, len(run))
		for index, value := range run {
			arg, ok := value.(string)
			if !ok {
				return acceptancecommand.Config{}, fmt.Errorf("acceptance.run item %d is not a string", index+1)
			}
			config.Run[index] = arg
		}
	}
	if timeout, ok := settings["timeout_ms"].(int); ok {
		config.Timeout = time.Duration(timeout) * time.Millisecond
	}
	return config, config.Validate()
}

func configuredProjectEnvironment(config *project.Config) (process.Environment, error) {
	result := make(process.Environment)
	if config == nil {
		return result, nil
	}
	values, ok := config.Raw["env"].(yamlmini.Mapping)
	if !ok {
		return result, nil
	}
	for key, raw := range values {
		value, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("project env %q is not a string", key)
		}
		result[key] = value
	}
	return result, nil
}

type buildLander struct {
	processes   contract.ProcessRunner
	environment process.Environment
	verifier    *buildGateVerifier
}

func (lander buildLander) Land(ctx context.Context, request single.LandingRequest) (integrate.Result, error) {
	if request.Project == nil || request.Approval == nil || request.Store == nil || request.Snapshot == nil || lander.verifier == nil {
		return integrate.Result{}, errors.New("landing request is incomplete")
	}
	workspacePath := request.Workspace.Path
	verifier := movedBaseGateVerifier{base: lander.verifier, project: request.Project, approval: request.Approval, request: request}
	result, err := integrate.Land(ctx, integrate.Request{
		Processes: lander.processes, Environment: lander.environment,
		Repository: request.Project.Origin, Workspace: workspacePath, Branch: request.Approval.TargetBranch,
		RunID: request.RunID, Candidate: request.Candidate, RunStore: request.Store, Snapshot: request.Snapshot,
		Verifier: verifier, Repairer: unavailableLandingRepairer{},
		CreateCandidate: func(ctx context.Context, base integrate.Base, report *gate.GateReport) (commit.Result, error) {
			return commit.Create(ctx, commit.Request{
				Processes: lander.processes, Environment: lander.environment, Workspace: workspacePath,
				BaseCommit: base.Commit, Slug: request.Approval.Slug, IntentBytes: request.Approval.IntentBytes,
				AcceptanceSourcePath: request.Approval.AcceptanceSourcePath, CandidatePath: request.Approval.CandidatePath,
				CandidateBytes: request.Approval.AcceptanceBytes, Protection: request.Approval.Protection, Gate: report,
			})
		},
		Publish: func(ctx context.Context, candidate commit.Result) (publish.Result, error) {
			return publish.Publish(ctx, publish.Request{
				Processes: lander.processes, Environment: lander.environment, Repository: request.Project.Origin,
				Store: request.Store, Snapshot: request.Snapshot, Candidate: candidate,
			})
		},
		ReleaseClaim: func(context.Context) error { return nil },
	})
	if err != nil {
		return result, err
	}
	if result.Kind == integrate.OutcomeLanded || result.Kind == integrate.OutcomeParked {
		if err := removeLandedWorkspace(request.Project.StateRoot, request.Workspace, request.RunID); err != nil {
			result.CleanupErrors = append(result.CleanupErrors, "workspace cleanup: "+err.Error())
		}
	}
	return result, nil
}

// removeLandedWorkspace removes the already-preserved checkout through an
// os.Root descriptor. The Build controller's later cleanup uses safefs names,
// which intentionally reject .git; removing this private directory here lets
// that terminal cleanup observe a missing workspace without traversing Git
// metadata as ordinary controller data.
func removeLandedWorkspace(stateRoot string, current workspace.Workspace, runID string) error {
	if stateRoot == "" || !filepath.IsAbs(stateRoot) || filepath.Clean(stateRoot) != stateRoot ||
		current.Path == "" || !filepath.IsAbs(current.Path) || filepath.Clean(current.Path) != current.Path ||
		current.RunID != runID || current.Rung != single.RungName || filepath.Base(current.Path) != runID+"-"+single.RungName {
		return errors.New("landed workspace identity is invalid")
	}
	stateRoot, err := filepath.EvalSymlinks(stateRoot)
	if err != nil {
		return fmt.Errorf("resolve private state root: %w", err)
	}
	workspacesDir := filepath.Join(stateRoot, "workspaces")
	canonicalWorkspacesDir, err := filepath.EvalSymlinks(workspacesDir)
	if err != nil {
		return fmt.Errorf("resolve private workspace root: %w", err)
	}
	workspaceParent, err := filepath.EvalSymlinks(filepath.Dir(current.Path))
	if err != nil || workspaceParent != canonicalWorkspacesDir {
		return errors.New("landed workspace is outside the private workspace root")
	}
	root, err := os.OpenRoot(canonicalWorkspacesDir)
	if err != nil {
		return fmt.Errorf("open private workspace root: %w", err)
	}
	defer root.Close()
	leaf := filepath.Base(current.Path)
	info, err := root.Lstat(leaf)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect landed workspace: %w", err)
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return errors.New("landed workspace is not a real directory")
	}
	if err := root.RemoveAll(leaf); err != nil {
		return fmt.Errorf("remove landed workspace: %w", err)
	}
	return nil
}

type movedBaseGateVerifier struct {
	base     *buildGateVerifier
	project  *project.Resolution
	approval *single.Approval
	request  single.LandingRequest
}

func (verifier movedBaseGateVerifier) Verify(ctx context.Context, request integrate.VerifyRequest) (*gate.GateReport, error) {
	if verifier.base == nil || verifier.request.RunID == "" {
		return nil, errors.New("moved-base verifier is not configured")
	}
	path := request.Workspace
	return verifier.base.Verify(ctx, single.VerifyRequest{
		Project: verifier.project, Approval: verifier.approval, Base: request.Base,
		Workspace: workspace.Workspace{Path: path, RunID: verifier.request.RunID, Rung: single.RungName, BaseCommit: request.Base.Commit},
		RunDir:    filepath.Join(verifier.project.StateRoot, "runs", verifier.request.RunID),
		Baseline:  verifier.approval.Baseline,
	})
}

type unavailableLandingRepairer struct{}

func (unavailableLandingRepairer) Repair(context.Context, integrate.RepairRequest) error {
	return integrate.ErrRepairSpent
}

func writeBuildRouteError(cli *CLI, err error) int {
	var failure *contract.Failure
	if errors.As(err, &failure) {
		detail := ""
		if failure.Cause != nil {
			detail = failure.Cause.Error()
		}
		return cli.writeError(string(failure.Class), string(failure.Reason), detail, int(failure.Exit))
	}
	return cli.writeError("controller", "queue_unavailable", err.Error(), 70)
}

var _ drain.RecoveryPort = recoveryRoute{}
var _ drain.SnapshotPort = statusQueueSnapshot{}
var _ single.Verifier = (*buildGateVerifier)(nil)
var _ single.Lander = buildLander{}
var _ integrate.Verifier = movedBaseGateVerifier{}
var _ integrate.Repairer = unavailableLandingRepairer{}
var _ gate.AcceptanceRunner = buildAcceptanceRunner{}
var _ single.Agent = unavailableBuildAgent{}
