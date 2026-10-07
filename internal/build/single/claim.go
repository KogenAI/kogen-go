package single

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/project"
)

const buildClaimRef = "refs/kogen/claim"

// GitClaimSource owns the origin-wide claim ref with compare-and-swap. It uses
// Git plumbing and --no-deref so a symbolic ref can never redirect ownership.
type GitClaimSource struct {
	Git    contract.GitPort
	Policy func(string) contract.GitPolicy
}

type gitClaim struct {
	git    contract.GitPort
	policy contract.GitPolicy
	ref    string
	commit contract.ObjectID
	format gitio.ObjectFormat
}

func (s GitClaimSource) Acquire(ctx context.Context, resolved *project.Resolution, runID string) (Claim, bool, error) {
	if ctx == nil || resolved == nil || s.Git == nil || s.Policy == nil || !runIDPattern.MatchString(runID) {
		return nil, false, errors.New("Build claim requires context, resolved project, Git and a safe run id")
	}
	policy := s.Policy(resolved.Origin)
	if policy.WorkingDirectory != resolved.Origin {
		return nil, false, errors.New("Build claim Git policy is not rooted at the resolved origin")
	}
	if err := requireDirectRef(ctx, s.Git, policy, buildClaimRef); err != nil {
		return nil, false, fmt.Errorf("inspect Build claim: %w", err)
	}
	refs := gitio.NewRefPort(s.Git, policy)
	format, err := refs.ObjectFormat(ctx)
	if err != nil {
		return nil, false, err
	}
	if existing, err := refs.ReadRef(ctx, buildClaimRef); err != nil {
		return nil, false, err
	} else if existing.Exists {
		return nil, false, nil
	}
	commit, err := s.writeClaimCommit(ctx, policy, format, runID)
	if err != nil {
		return nil, false, err
	}
	updated, err := compareRefNoDeref(ctx, s.Git, policy, buildClaimRef, commit, "", format)
	if err != nil {
		if errors.Is(err, gitio.ErrRefConflict) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !updated {
		return nil, false, nil
	}
	return &gitClaim{git: s.Git, policy: policy, ref: buildClaimRef, commit: commit, format: format}, true, nil
}

func (s GitClaimSource) writeClaimCommit(ctx context.Context, policy contract.GitPolicy, format gitio.ObjectFormat, runID string) (contract.ObjectID, error) {
	blob, err := gitSuccess(ctx, s.Git, policy, []string{"hash-object", "-w", "--no-filters", "--stdin"}, []byte(runID+"\n"))
	if err != nil {
		return "", fmt.Errorf("write claim owner blob: %w", err)
	}
	blobID, err := gitio.ParseObjectID(strings.TrimSpace(string(blob)))
	if err != nil {
		return "", err
	}
	leafTree, err := gitSuccess(ctx, s.Git, policy, []string{"mktree", "-z"}, []byte("100644 blob "+string(blobID)+"\tclaim\x00"))
	if err != nil {
		return "", fmt.Errorf("write claim directory tree: %w", err)
	}
	leafTreeID, err := gitio.ParseObjectID(strings.TrimSpace(string(leafTree)))
	if err != nil {
		return "", err
	}
	rootTree, err := gitSuccess(ctx, s.Git, policy, []string{"mktree", "-z"}, []byte("040000 tree "+string(leafTreeID)+"\t.kogen\x00"))
	if err != nil {
		return "", fmt.Errorf("write claim root tree: %w", err)
	}
	rootTreeID, err := gitio.ParseObjectID(strings.TrimSpace(string(rootTree)))
	if err != nil {
		return "", err
	}
	commitPolicy := withClaimIdentity(policy)
	message := []byte("Kogen Build claim\n\nRun: " + runID + "\n")
	commitBytes, err := gitSuccess(ctx, s.Git, commitPolicy, []string{"commit-tree", string(rootTreeID)}, message)
	if err != nil {
		return "", fmt.Errorf("create Build claim object: %w", err)
	}
	commit, err := gitio.ParseObjectID(strings.TrimSpace(string(commitBytes)))
	if err != nil {
		return "", err
	}
	if format == gitio.ObjectFormatSHA1 && len(commit) != 40 || format == gitio.ObjectFormatSHA256 && len(commit) != 64 {
		return "", errors.New("Build claim object format mismatch")
	}
	return commit, nil
}

func (claim *gitClaim) Release(ctx context.Context) error {
	if claim == nil || claim.git == nil {
		return nil
	}
	if err := requireDirectRef(ctx, claim.git, claim.policy, claim.ref); err != nil {
		return err
	}
	observed, err := gitio.NewRefPort(claim.git, claim.policy).ReadRef(ctx, claim.ref)
	if err != nil {
		return err
	}
	if !observed.Exists {
		return nil
	}
	if observed.Target != claim.commit {
		return errors.New("Build claim changed ownership before release")
	}
	result, err := claim.git.Exec(ctx, []string{"update-ref", "--no-deref", "-d", claim.ref, string(claim.commit)}, nil, claim.policy)
	if err != nil {
		return err
	}
	if result.Process.ExitStatus == nil || result.Process.TimedOut || result.Process.Unavailable || *result.Process.ExitStatus != 0 {
		return errors.New("release Build claim: compare-and-swap failed")
	}
	return nil
}

func compareRefNoDeref(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, name string, next contract.ObjectID, expected string, format gitio.ObjectFormat) (bool, error) {
	old := expected
	if old == "" {
		if format == gitio.ObjectFormatSHA256 {
			old = strings.Repeat("0", 64)
		} else {
			old = strings.Repeat("0", 40)
		}
	}
	result, err := git.Exec(ctx, []string{"update-ref", "--no-deref", name, string(next), old}, nil, policy)
	if err != nil {
		return false, err
	}
	if result.Process.ExitStatus == nil || result.Process.TimedOut || result.Process.Unavailable {
		return false, errors.New("Build claim compare-and-swap returned no usable status")
	}
	if *result.Process.ExitStatus == 0 {
		return true, nil
	}
	observed, readErr := gitio.NewRefPort(git, policy).ReadRef(ctx, name)
	if readErr == nil && observed.Exists {
		return false, gitio.ErrRefConflict
	}
	return false, errors.New("Build claim compare-and-swap failed")
}

func gitSuccess(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, args []string, stdin []byte) ([]byte, error) {
	result, err := git.Exec(ctx, args, stdin, policy)
	if err != nil {
		return nil, err
	}
	if result.Process.ExitStatus == nil || result.Process.TimedOut || result.Process.Unavailable || *result.Process.ExitStatus != 0 {
		return nil, fmt.Errorf("Git %s failed", args[0])
	}
	return result.Stdout, nil
}

func withClaimIdentity(policy contract.GitPolicy) contract.GitPolicy {
	policy.Environment = append([]string(nil), policy.Environment...)
	values := map[string]string{
		"GIT_AUTHOR_NAME": "Kogen Controller", "GIT_AUTHOR_EMAIL": "kogen-controller@invalid",
		"GIT_COMMITTER_NAME": "Kogen Controller", "GIT_COMMITTER_EMAIL": "kogen-controller@invalid",
	}
	seen := make(map[string]bool)
	for index, entry := range policy.Environment {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if value, found := values[name]; found {
			policy.Environment[index] = name + "=" + value
			seen[name] = true
		}
	}
	for name, value := range values {
		if !seen[name] {
			policy.Environment = append(policy.Environment, name+"="+value)
		}
	}
	return policy
}

var _ ClaimSource = GitClaimSource{}
