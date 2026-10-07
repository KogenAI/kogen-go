package gitio

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/process"
)

type ObjectFormat string

const (
	ObjectFormatSHA1   ObjectFormat = "sha1"
	ObjectFormatSHA256 ObjectFormat = "sha256"
)

var objectIDPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// ParseObjectID accepts only complete canonical Git object IDs. Git's
// abbreviation syntax is deliberately not accepted at this boundary.
func ParseObjectID(value string) (contract.ObjectID, error) {
	if !objectIDPattern.MatchString(value) {
		return "", &GitError{Kind: ErrorInvalid, Operation: "object id", Detail: "object id must be a complete 40- or 64-character lowercase hexadecimal value"}
	}
	return contract.ObjectID(value), nil
}

// ValidateObjectID validates a complete SHA-1 or SHA-256 object ID.
func ValidateObjectID(value contract.ObjectID) error {
	_, err := ParseObjectID(string(value))
	return err
}

// RefPort provides the primitive repository reads, commit creation and
// compare-and-swap operations used by approval and landing.
type RefPort struct {
	git    contract.GitPort
	policy contract.GitPolicy
}

var _ contract.LandingPort = (*RefPort)(nil)

func NewRefPort(git contract.GitPort, policy contract.GitPolicy) *RefPort {
	return &RefPort{git: git, policy: policy}
}

// NewLandingRefPort binds the production identity/signing runner to a fixed
// origin policy. Use WorkspacePolicy/NewWorkspace for controlled workspaces.
func NewLandingRefPort(processRunner contract.ProcessRunner, directory string, base process.Environment) *RefPort {
	return NewRefPort(NewOrigin(processRunner), OriginPolicy(directory, base))
}

func (r *RefPort) ObjectFormat(ctx context.Context) (ObjectFormat, error) {
	result, err := r.checked(ctx, "object format", []string{"rev-parse", "--show-object-format"}, nil)
	if err != nil {
		return "", err
	}
	switch strings.TrimSpace(string(result.Stdout)) {
	case string(ObjectFormatSHA1):
		return ObjectFormatSHA1, nil
	case string(ObjectFormatSHA256):
		return ObjectFormatSHA256, nil
	default:
		return "", &GitError{Kind: ErrorProcess, Operation: "object format", Detail: "Git reported an unsupported object format"}
	}
}

func (r *RefPort) ResolveCommit(ctx context.Context, revision string) (contract.ObjectID, error) {
	return r.resolve(ctx, "commit", revision)
}

func (r *RefPort) ResolveTree(ctx context.Context, revision string) (contract.ObjectID, error) {
	return r.resolve(ctx, "tree", revision)
}

func (r *RefPort) resolve(ctx context.Context, kind, revision string) (contract.ObjectID, error) {
	if revision == "" || strings.ContainsRune(revision, '\x00') {
		return "", &GitError{Kind: ErrorInvalid, Operation: "resolve " + kind, Detail: "revision must be a non-empty Git revision"}
	}
	expression := revision + "^{" + kind + "}"
	result, err := r.checked(ctx, "resolve "+kind, []string{"rev-parse", "--verify", "--quiet", "--end-of-options", expression}, nil)
	if err != nil {
		return "", err
	}
	id, err := ParseObjectID(strings.TrimSpace(string(result.Stdout)))
	if err != nil {
		return "", err
	}
	format, err := r.ObjectFormat(ctx)
	if err != nil {
		return "", err
	}
	if err := validateObjectIDForFormat(id, format); err != nil {
		return "", err
	}
	return id, nil
}

func (r *RefPort) ReadRef(ctx context.Context, name string) (contract.RefObservation, error) {
	if err := r.checkRefName(ctx, name); err != nil {
		return contract.RefObservation{}, err
	}
	result, err := r.git.Exec(ctx, []string{"rev-parse", "--verify", "--quiet", "--end-of-options", name}, nil, r.policy)
	if err != nil {
		return contract.RefObservation{}, err
	}
	if result.Process.ExitStatus == nil {
		return contract.RefObservation{}, &GitError{Kind: ErrorProcess, Operation: "read ref", Detail: "supervisor returned no exit status"}
	}
	if *result.Process.ExitStatus == 1 {
		return contract.RefObservation{Name: name, Exists: false}, nil
	}
	if *result.Process.ExitStatus != 0 {
		return contract.RefObservation{}, exitError("read ref", result)
	}
	id, err := ParseObjectID(strings.TrimSpace(string(result.Stdout)))
	if err != nil {
		return contract.RefObservation{}, err
	}
	if err := r.validateObjectFormat(ctx, id); err != nil {
		return contract.RefObservation{}, err
	}
	return contract.RefObservation{Name: name, Target: id, Exists: true}, nil
}

func (r *RefPort) CompareAndSwap(ctx context.Context, update contract.RefUpdate) (contract.RefUpdateResult, error) {
	if err := r.checkRefName(ctx, update.Name); err != nil {
		return contract.RefUpdateResult{}, err
	}
	format, err := r.ObjectFormat(ctx)
	if err != nil {
		return contract.RefUpdateResult{}, err
	}
	if update.Next == "" {
		return r.DeleteRefCAS(ctx, update.Name, update.Expected)
	}
	if err := validateObjectIDForFormat(update.Next, format); err != nil {
		return contract.RefUpdateResult{}, err
	}
	old := string(update.Expected)
	if old == "" {
		old = zeroObjectID(format)
	} else if err := validateObjectIDForFormat(update.Expected, format); err != nil {
		return contract.RefUpdateResult{}, err
	}
	result, err := r.git.Exec(ctx, []string{"update-ref", update.Name, string(update.Next), old}, nil, r.policy)
	if err != nil {
		return contract.RefUpdateResult{}, err
	}
	if result.Process.ExitStatus == nil {
		return contract.RefUpdateResult{}, &GitError{Kind: ErrorProcess, Operation: "update-ref", Detail: "supervisor returned no exit status"}
	}
	if result.Process.ExitStatus != nil && *result.Process.ExitStatus == 0 {
		return contract.RefUpdateResult{Updated: true, ObservedTarget: update.Next}, nil
	}
	gitErr := exitError("update-ref", result)
	if isCASConflict(gitErr) {
		observed, readErr := r.ReadRef(ctx, update.Name)
		if readErr != nil {
			return contract.RefUpdateResult{}, readErr
		}
		return contract.RefUpdateResult{Updated: false, ObservedTarget: observed.Target}, &GitError{
			Kind: ErrorRefConflict, Operation: "update-ref", ExitStatus: gitErr.ExitStatus,
			Detail: gitErr.Detail, Cause: ErrRefConflict,
		}
	}
	return contract.RefUpdateResult{}, gitErr
}

func (r *RefPort) DeleteRefCAS(ctx context.Context, name string, expected contract.ObjectID) (contract.RefUpdateResult, error) {
	if err := r.checkRefName(ctx, name); err != nil {
		return contract.RefUpdateResult{}, err
	}
	format, err := r.ObjectFormat(ctx)
	if err != nil {
		return contract.RefUpdateResult{}, err
	}
	if err := validateObjectIDForFormat(expected, format); err != nil {
		return contract.RefUpdateResult{}, err
	}
	result, err := r.git.Exec(ctx, []string{"update-ref", "-d", name, string(expected)}, nil, r.policy)
	if err != nil {
		return contract.RefUpdateResult{}, err
	}
	if result.Process.ExitStatus == nil {
		return contract.RefUpdateResult{}, &GitError{Kind: ErrorProcess, Operation: "update-ref -d", Detail: "supervisor returned no exit status"}
	}
	if result.Process.ExitStatus != nil && *result.Process.ExitStatus == 0 {
		return contract.RefUpdateResult{Updated: true}, nil
	}
	gitErr := exitError("update-ref -d", result)
	if isCASConflict(gitErr) {
		observed, readErr := r.ReadRef(ctx, name)
		if readErr != nil {
			return contract.RefUpdateResult{}, readErr
		}
		return contract.RefUpdateResult{Updated: false, ObservedTarget: observed.Target}, &GitError{
			Kind: ErrorRefConflict, Operation: "update-ref -d", ExitStatus: gitErr.ExitStatus,
			Detail: gitErr.Detail, Cause: ErrRefConflict,
		}
	}
	return contract.RefUpdateResult{}, gitErr
}

func (r *RefPort) CommitTree(ctx context.Context, request contract.CommitTreeRequest) (contract.ObjectID, error) {
	format, err := r.ObjectFormat(ctx)
	if err != nil {
		return "", err
	}
	if err := validateObjectIDForFormat(request.Tree, format); err != nil {
		return "", fmt.Errorf("commit tree: %w", err)
	}
	args := []string{"commit-tree", string(request.Tree)}
	for _, parent := range request.Parents {
		if err := validateObjectIDForFormat(parent, format); err != nil {
			return "", fmt.Errorf("commit parent: %w", err)
		}
		args = append(args, "-p", string(parent))
	}
	if strings.IndexByte(string(request.Message), 0) >= 0 {
		return "", &GitError{Kind: ErrorInvalid, Operation: "commit-tree", Detail: "commit message contains NUL"}
	}
	result, err := r.checked(ctx, "commit-tree", args, request.Message)
	if err != nil {
		return "", err
	}
	id, err := ParseObjectID(strings.TrimSpace(string(result.Stdout)))
	if err != nil {
		return "", err
	}
	if err := validateObjectIDForFormat(id, format); err != nil {
		return "", err
	}
	return id, nil
}

func (r *RefPort) IsAncestor(ctx context.Context, ancestor, descendant contract.ObjectID) (bool, error) {
	format, err := r.ObjectFormat(ctx)
	if err != nil {
		return false, err
	}
	if err := validateObjectIDForFormat(ancestor, format); err != nil {
		return false, err
	}
	if err := validateObjectIDForFormat(descendant, format); err != nil {
		return false, err
	}
	result, err := r.git.Exec(ctx, []string{"merge-base", "--is-ancestor", string(ancestor), string(descendant)}, nil, r.policy)
	if err != nil {
		return false, err
	}
	if result.Process.ExitStatus == nil {
		return false, &GitError{Kind: ErrorProcess, Operation: "merge-base --is-ancestor", Detail: "supervisor returned no exit status"}
	}
	switch *result.Process.ExitStatus {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, exitError("merge-base --is-ancestor", result)
	}
}

func (r *RefPort) checkRefName(ctx context.Context, name string) error {
	if !strings.HasPrefix(name, "refs/") || strings.ContainsRune(name, '\x00') {
		return &GitError{Kind: ErrorInvalid, Operation: "ref name", Detail: "ref name must be fully qualified under refs/"}
	}
	result, err := r.git.Exec(ctx, []string{"check-ref-format", name}, nil, r.policy)
	if err != nil {
		return err
	}
	if result.Process.ExitStatus == nil {
		return &GitError{Kind: ErrorProcess, Operation: "check-ref-format", Detail: "supervisor returned no exit status"}
	}
	if *result.Process.ExitStatus != 0 {
		return &GitError{Kind: ErrorInvalid, Operation: "ref name", ExitStatus: result.Process.ExitStatus, Detail: "Git rejected the ref name"}
	}
	return nil
}

func (r *RefPort) validateObjectFormat(ctx context.Context, id contract.ObjectID) error {
	format, err := r.ObjectFormat(ctx)
	if err != nil {
		return err
	}
	return validateObjectIDForFormat(id, format)
}

func (r *RefPort) checked(ctx context.Context, operation string, args []string, stdin []byte) (contract.GitResult, error) {
	if r == nil || r.git == nil {
		return contract.GitResult{}, &GitError{Kind: ErrorInvalid, Operation: operation, Detail: "Git port is required"}
	}
	result, err := r.git.Exec(ctx, args, stdin, r.policy)
	if err != nil {
		return result, err
	}
	if result.Process.ExitStatus == nil {
		return result, &GitError{Kind: ErrorProcess, Operation: operation, Detail: "supervisor returned no exit status"}
	}
	if *result.Process.ExitStatus != 0 {
		return result, exitError(operation, result)
	}
	return result, nil
}

func validateObjectIDForFormat(id contract.ObjectID, format ObjectFormat) error {
	if err := ValidateObjectID(id); err != nil {
		return err
	}
	want := 40
	if format == ObjectFormatSHA256 {
		want = 64
	}
	if len(id) != want {
		return &GitError{Kind: ErrorInvalid, Operation: "object id", Detail: fmt.Sprintf("object id length does not match repository format %s", format)}
	}
	return nil
}

func zeroObjectID(format ObjectFormat) string {
	if format == ObjectFormatSHA256 {
		return strings.Repeat("0", 64)
	}
	return strings.Repeat("0", 40)
}

func isCASConflict(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "cannot lock ref") || strings.Contains(message, "is at") ||
		strings.Contains(message, "reference already exists") || strings.Contains(message, "expected")
}
