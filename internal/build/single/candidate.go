package single

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/journal"
	"kogen-go/internal/process"
)

var safeRunID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// GitCandidatePreserver captures the workspace relative to its immutable base,
// writes a one-parent unverified snapshot, transfers only that commit through
// Git fetch (no hooks), and creates a candidate ref without replacing an
// existing identity.
type GitCandidatePreserver struct {
	WorkspaceGit contract.GitPort
	OriginGit    contract.GitPort
	Environment  process.Environment
	OriginPolicy func(string) contract.GitPolicy
}

func NewGitCandidatePreserver(processes contract.ProcessRunner, environment process.Environment) GitCandidatePreserver {
	return GitCandidatePreserver{
		WorkspaceGit: gitio.NewWorkspace(processes), OriginGit: gitio.NewOrigin(processes),
		Environment:  process.ControllerGitEnvironment(environment),
		OriginPolicy: func(directory string) contract.GitPolicy { return gitio.OriginPolicy(directory, environment) },
	}
}

func (p GitCandidatePreserver) Preserve(ctx context.Context, request CandidateRequest) (journal.RecoveryRecord, error) {
	if ctx == nil || request.Project == nil || !safeRunID.MatchString(request.RunID) ||
		request.Workspace.Path == "" || request.Base.Commit == "" || p.WorkspaceGit == nil || p.OriginGit == nil || p.OriginPolicy == nil {
		return journal.RecoveryRecord{}, errors.New("candidate preservation request is incomplete")
	}
	if err := gitio.ValidateObjectID(request.Base.Commit); err != nil {
		return journal.RecoveryRecord{}, err
	}
	workspacePolicy := gitio.WorkspacePolicy(request.Workspace.Path, p.Environment)
	tree, err := gitio.BuildCandidateTree(ctx, p.WorkspaceGit, workspacePolicy, request.Base.Commit)
	if err != nil {
		return journal.RecoveryRecord{}, fmt.Errorf("snapshot current workspace: %w", err)
	}
	workspacePolicy = withClaimIdentity(workspacePolicy)
	message := []byte("Kogen unverified candidate snapshot\n\nRun: " + request.RunID + "\nWorkspace: " + filepath.Base(request.Workspace.Path) + "\n")
	commit, err := gitio.NewRefPort(p.WorkspaceGit, workspacePolicy).CommitTree(ctx, contract.CommitTreeRequest{
		Tree: tree, Parents: []contract.ObjectID{request.Base.Commit}, Message: message,
	})
	if err != nil {
		return journal.RecoveryRecord{}, fmt.Errorf("create candidate snapshot commit: %w", err)
	}
	if err := transferCommit(ctx, p.OriginGit, p.OriginPolicy(request.Project.Origin), request.Workspace.Path, commit); err != nil {
		return journal.RecoveryRecord{}, fmt.Errorf("transfer candidate snapshot: %w", err)
	}
	workspaceName := filepath.Base(request.Workspace.Path)
	if !safeWorkspaceName(workspaceName) {
		return journal.RecoveryRecord{}, errors.New("candidate workspace name is unsafe")
	}
	ref := "refs/kogen/candidates/" + request.RunID + "/recovery-" + workspaceName
	originPolicy := p.OriginPolicy(request.Project.Origin)
	if err := requireDirectRef(ctx, p.OriginGit, originPolicy, ref); err != nil {
		return journal.RecoveryRecord{}, fmt.Errorf("inspect recovery candidate ref: %w", err)
	}
	refs := gitio.NewRefPort(p.OriginGit, originPolicy)
	format, err := refs.ObjectFormat(ctx)
	if err != nil {
		return journal.RecoveryRecord{}, err
	}
	observed, err := refs.ReadRef(ctx, ref)
	if err != nil {
		return journal.RecoveryRecord{}, err
	}
	if observed.Exists {
		if observed.Target != commit {
			return journal.RecoveryRecord{}, errors.New("refusing to replace a prior recovery candidate")
		}
	} else {
		updated, err := compareRefNoDeref(ctx, p.OriginGit, originPolicy, ref, commit, "", format)
		if err != nil {
			if errors.Is(err, gitio.ErrRefConflict) {
				observed, readErr := refs.ReadRef(ctx, ref)
				if readErr == nil && observed.Exists && observed.Target == commit {
					updated = true
				}
			}
			if !updated {
				return journal.RecoveryRecord{}, err
			}
		} else if !updated {
			return journal.RecoveryRecord{}, errors.New("candidate ref was not published")
		}
	}
	treeText := string(tree)
	refText := ref
	return journal.RecoveryRecord{
		Workspace: workspaceName, Base: string(request.Base.Commit), Tree: &treeText,
		Ref: &refText, Verification: "unverified",
	}, nil
}

func transferCommit(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, workspacePath string, commit contract.ObjectID) error {
	if !filepath.IsAbs(workspacePath) || filepath.Clean(workspacePath) != workspacePath {
		return errors.New("candidate workspace path is not canonical")
	}
	result, err := git.Exec(ctx, []string{"fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--", workspacePath, string(commit)}, nil, policy)
	if err != nil {
		return err
	}
	if result.Process.ExitStatus == nil || result.Process.TimedOut || result.Process.Unavailable || *result.Process.ExitStatus != 0 {
		return errors.New("Git fetch did not transfer the candidate commit")
	}
	resolved, err := gitio.NewRefPort(git, policy).ResolveCommit(ctx, string(commit))
	if err != nil || resolved != commit {
		return errors.New("transferred candidate commit did not resolve in the origin")
	}
	return nil
}

func safeWorkspaceName(value string) bool {
	return value != "" && len(value) <= 128 && !strings.ContainsAny(value, "/\\\x00\r\n") && value != "." && value != ".."
}

var _ CandidatePreserver = GitCandidatePreserver{}
