package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kogen-go/internal/acceptance"
	"kogen-go/internal/approval/prepare"
	"kogen-go/internal/approval/publish"
	"kogen-go/internal/approval/remove"
	"kogen-go/internal/cli/parse"
	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/process"
	"kogen-go/internal/project"
	"kogen-go/internal/safefs"
	"kogen-go/internal/setupcache"
	"kogen-go/internal/workspace"
)

func (cli *CLI) approve(command parse.Command, cwd string) int {
	ctx := context.Background()
	ports := cli.ports()
	resolved, exit, message := cli.resolveProject(ctx, command, cwd, ports)
	if exit != 0 {
		return cli.writeRawError(message, exit)
	}
	runID, err := randomIdentity()
	if err != nil {
		return cli.writeError("controller", "internal_error", "could not allocate a private approval run", 70)
	}
	runDir, cacheDir, runRoot, err := prepareRunDirectories(resolved, runID)
	if err != nil {
		return cli.writeError("environment", "approval_check_failed", "could not create private approval state", 3)
	}
	defer closeRoot(runRoot)
	cache, err := setupcache.Open(cacheDir)
	if err != nil {
		return cli.writeError("environment", "approval_check_failed", "could not open the private approval cache", 3)
	}
	defer cache.Close()

	baseCommit, err := gitio.NewRefPort(ports.workspaceGit, gitio.WorkspacePolicy(resolved.Origin, ports.env)).ResolveCommit(ctx, resolved.Base)
	if err != nil {
		return cli.writeError("environment", "base_unavailable", resolved.Base, 3)
	}
	baseMetadata, err := gitio.LoadBaseMetadata(ctx, ports.workspaceGit, gitio.WorkspacePolicy(resolved.Checkout, ports.env), baseCommit)
	if err != nil {
		return cli.writeError("environment", "approval_check_failed", "could not load exact-base Git metadata", 3)
	}
	trees := acceptance.CandidateTree{
		Git: ports.workspaceGit, Policy: gitio.WorkspacePolicy(resolved.Checkout, ports.env), Base: baseMetadata,
	}
	checks := acceptance.Adapter{
		Processes: ports.processes, Trees: trees, Roots: safefs.Opener{}, RunDir: runDir,
	}
	scratch := &scratchFactory{
		git: ports.workspaceGit, environment: ports.env,
		project: resolved, workspacesDir: filepath.Join(runDir, "scratch"),
	}
	dependencies := prepare.Dependencies{
		Git:    ports.originGit,
		Policy: func(directory string) contract.GitPolicy { return gitio.OriginPolicy(directory, ports.env) },
		Roots:  safefs.Opener{}, Processes: ports.processes, Checks: &checks, Trees: trees,
		Scratch: scratch, Setup: cache, Baselines: cache,
	}
	request := prepare.Request{
		Project: resolved, Slug: value(command.Slug), HashPrefix: value(command.Hash), By: value(command.By),
		RunDir: runDir, BaseEnvironment: ports.env, AdapterVersion: "kogen-go-i1",
		ToolchainKnown: false,
	}
	prepared, err := prepare.Prepare(ctx, request, dependencies)
	if err != nil {
		return cli.writeApprovalError(err)
	}
	if command.Hash == nil {
		_, _ = ioWriteString(cli.Out, prepared.Card)
		return 5
	}
	published, err := publish.Publish(ctx, publish.Request{
		Project: resolved, Prepared: prepared, GivenHashPrefix: *command.Hash,
		At: time.Now().UTC().Format(time.RFC3339Nano),
	}, publish.Dependencies{
		Git:    ports.originGit,
		Policy: func(directory string) contract.GitPolicy { return gitio.OriginPolicy(directory, ports.env) },
		Roots:  safefs.Opener{},
	})
	if err != nil {
		return cli.writePublishError(err)
	}
	card := prepare.RenderCard(prepared.Intent, resolved.Base, string(prepared.BaseCommit), prepared.ApprovalSHA256, prepared.Approver, prepared.Warnings, prepared.CheckBaseline)
	warnings := approvalWarningBlock(card)
	output := warnings
	if output != "" {
		output += "\n"
	}
	output += fmt.Sprintf("approved %s %s (approval %s); it is queued\nNext: kogen queue start (does nothing if the queue is already running)\n", prepared.Intent.Slug, prepared.ApprovalSHA256[:8], shortObject(published.Commit))
	_, _ = ioWriteString(cli.Out, output)
	return 0
}

func (cli *CLI) remove(command parse.Command, cwd string) int {
	ctx := context.Background()
	ports := cli.ports()
	resolved, exit, message := cli.resolveProject(ctx, command, cwd, ports)
	if exit != 0 {
		return cli.writeRawError(message, exit)
	}
	result, err := remove.Remove(ctx, remove.Request{Project: resolved, Slug: value(command.Slug), Force: command.Force}, remove.Dependencies{
		Git:    ports.originGit,
		Policy: func(directory string) contract.GitPolicy { return gitio.OriginPolicy(directory, ports.env) },
		Roots:  safefs.Opener{},
	})
	if err != nil {
		var failure *remove.Failure
		if errors.As(err, &failure) {
			class := string(failure.Class)
			if class == "" {
				class = "intent"
			}
			return cli.writeError(class, failure.Reason, failure.Detail, int(failure.Exit))
		}
		return cli.writeError("controller", "internal_error", "Intent removal failed", 70)
	}
	_, _ = ioWriteString(cli.Out, fmt.Sprintf("removed: %s\ncommit: %s\n", value(command.Slug), result.Commit))
	return 0
}

func (cli *CLI) writeApprovalError(err error) int {
	var failure *prepare.Failure
	if errors.As(err, &failure) {
		class := "intent"
		exit := int(failure.Exit)
		if exit == 3 {
			class = "environment"
		}
		return cli.writeError(class, failure.Reason, failure.Detail, exit)
	}
	return cli.writeError("controller", "internal_error", "approval preparation failed", 70)
}

func (cli *CLI) writePublishError(err error) int {
	var failure *publish.Failure
	if errors.As(err, &failure) {
		exit, class := 3, "environment"
		if failure.Reason == "approval_ref_conflict" {
			exit, class = 70, "controller"
		}
		detail := ""
		if failure.Cause != nil {
			detail = failure.Cause.Error()
		}
		return cli.writeError(class, failure.Reason, detail, exit)
	}
	return cli.writeError("controller", "internal_error", "approval publication failed", 70)
}

func (cli *CLI) writeRawError(message string, exit int) int {
	_, _ = ioWriteString(cli.Out, message+"\n")
	return exit
}

func prepareRunDirectories(resolved *project.Resolution, runID string) (runDir, cacheDir string, runRoot *safefs.Root, resultErr error) {
	if resolved == nil || resolved.StateRoot == "" || resolved.Checkout == "" || !filepath.IsAbs(resolved.StateRoot) {
		return "", "", nil, errors.New("resolved state root is required")
	}
	home := filepath.Dir(filepath.Dir(filepath.Dir(resolved.StateRoot)))
	// StateRoot is <HOME>/.kogen/workspaces/<project-key>.
	stateRelative, err := filepath.Rel(home, resolved.StateRoot)
	if err != nil || stateRelative == "." || strings.HasPrefix(stateRelative, ".."+string(filepath.Separator)) {
		return "", "", nil, errors.New("state root is outside HOME")
	}
	homeRoot, err := safefs.OpenRoot(home)
	if err != nil {
		return "", "", nil, err
	}
	defer closeRoot(homeRoot)
	state := filepath.ToSlash(stateRelative)
	if err := homeRoot.MkdirAll(state+"/runs/"+runID, 0o700); err != nil {
		return "", "", nil, err
	}
	if err := homeRoot.MkdirAll(state+"/cache", 0o700); err != nil {
		return "", "", nil, err
	}
	if err := homeRoot.MkdirAll(state+"/runs/"+runID+"/scratch", 0o700); err != nil {
		return "", "", nil, err
	}
	runDir = filepath.Join(resolved.StateRoot, "runs", runID)
	cacheDir = filepath.Join(resolved.StateRoot, "cache")
	runRoot, err = safefs.OpenRoot(runDir)
	if err != nil {
		return "", "", nil, err
	}
	return runDir, cacheDir, runRoot, nil
}

type scratchFactory struct {
	git           contract.GitPort
	environment   process.Environment
	project       *project.Resolution
	workspacesDir string
}

func (factory *scratchFactory) OpenExactBase(ctx context.Context, request prepare.ScratchRequest) (prepare.ScratchWorkspace, error) {
	if factory == nil || factory.git == nil || factory.project == nil || request.BaseCommit == "" {
		return nil, errors.New("approval scratch workspace configuration is incomplete")
	}
	policy := gitio.WorkspacePolicy(factory.project.Checkout, factory.environment)
	tree, err := gitio.NewRefPort(factory.git, policy).ResolveTree(ctx, string(request.BaseCommit))
	if err != nil {
		return nil, err
	}
	created, err := (workspace.Factory{Git: factory.git, Policy: policy}).Create(ctx, workspace.CloneRequest{
		Source: factory.project.Checkout, WorkspacesDir: factory.workspacesDir,
		RunID: filepath.Base(request.RunDir), Rung: "R1", BaseCommit: request.BaseCommit,
	})
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(factory.workspacesDir)
	if err != nil {
		_ = removePrivateTree(filepath.Dir(created.Path), filepath.Base(created.Path))
		return nil, err
	}
	return &scratchWorkspace{git: factory.git, env: factory.environment, directory: created.Path, base: request.BaseCommit, tree: tree, root: root}, nil
}

type scratchWorkspace struct {
	git       contract.GitPort
	env       process.Environment
	directory string
	base      contract.ObjectID
	tree      contract.ObjectID
	root      *os.Root
}

func (workspace *scratchWorkspace) Directory() string       { return workspace.directory }
func (workspace *scratchWorkspace) Tree() contract.ObjectID { return workspace.tree }

func (workspace *scratchWorkspace) ResetToBase(ctx context.Context) error {
	policy := gitio.WorkspacePolicy(workspace.directory, workspace.env)
	for _, args := range [][]string{
		{"reset", "--hard", string(workspace.base)},
		{"clean", "-ffdx"},
	} {
		result, err := workspace.git.Exec(ctx, args, nil, policy)
		if err != nil || result.Process.ExitStatus == nil || *result.Process.ExitStatus != 0 || result.Process.TimedOut || result.Process.Unavailable {
			return fmt.Errorf("reset exact-base scratch checkout: git %s failed", args[0])
		}
	}
	return nil
}

func (workspace *scratchWorkspace) Close() error {
	if workspace == nil {
		return nil
	}
	var err error
	if workspace.root != nil {
		name := filepath.Base(workspace.directory)
		info, inspectErr := workspace.root.Lstat(name)
		if errors.Is(inspectErr, fs.ErrNotExist) {
			err = nil
		} else if inspectErr != nil {
			err = inspectErr
		} else if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			err = errors.New("scratch workspace is not a private real directory")
		} else {
			err = workspace.root.RemoveAll(name)
		}
		err = errors.Join(err, workspace.root.Close())
		workspace.root = nil
	}
	return err
}

func removePrivateTree(parent, name string) error {
	root, err := os.OpenRoot(parent)
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return errors.New("scratch workspace is not a private real directory")
	}
	return root.RemoveAll(name)
}

func randomIdentity() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func approvalWarningBlock(card string) string {
	start := strings.Index(card, "Warnings\n")
	if start < 0 {
		return ""
	}
	end := strings.Index(card[start:], "Approve with:\n")
	if end < 0 {
		return ""
	}
	return strings.TrimRight(card[start:start+end], "\n")
}

func value(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func shortObject(value contract.ObjectID) string {
	text := string(value)
	if len(text) > 8 {
		return text[:8]
	}
	return text
}

func closeRoot(root interface{ Close() error }) {
	if root != nil {
		_ = root.Close()
	}
}

func ioWriteString(writer interface{ Write([]byte) (int, error) }, value string) (int, error) {
	return io.WriteString(writer, value)
}
