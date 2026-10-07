package commit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/gate"
	"kogen-go/internal/gitio"
	"kogen-go/internal/intent"
	"kogen-go/internal/process"
	"kogen-go/internal/protection"
	"kogen-go/internal/safefs"
)

var (
	ErrInvalidRequest       = errors.New("landing commit: invalid request")
	ErrNotLandable          = errors.New("landing commit: candidate is not landable")
	ErrVerificationMismatch = errors.New("landing commit: verification receipt does not match the candidate")
	ErrProtectedBytes       = errors.New("landing commit: approved or protected bytes do not match")
)

// Request is the immutable input needed to create a landing commit. IntentBytes
// and CandidateBytes are the original approved bytes, not copies taken from a
// builder-controlled Git index. Gate must be the result of a successful gate
// pass, and Protection must be the approval-bound manifest used by that pass.
// Environment is the controller's origin environment captured before any
// project child environment is constructed.
type Request struct {
	Processes            contract.ProcessRunner
	Environment          process.Environment
	Workspace            string
	BaseCommit           contract.ObjectID
	Slug                 string
	IntentBytes          []byte
	AcceptanceSourcePath string
	CandidatePath        string
	CandidateBytes       []byte
	Protection           *protection.Protector
	Gate                 *gate.GateReport
}

// Result identifies the immutable commit object created in the winning
// workspace. VerifiedTree is the gate's base-relative candidate snapshot.
// Tree additionally contains the exact approved Intent and candidate-test
// blobs, even when native ignore rules exclude those paths from the snapshot.
// Create does not publish a ref or move the workspace's HEAD; those effects
// belong to landing publication.
type Result struct {
	Commit       contract.ObjectID
	Parent       contract.ObjectID
	VerifiedTree contract.ObjectID
	Tree         contract.ObjectID
	Message      []byte
}

// Create snapshots the winning workspace relative to BaseCommit, binds that
// snapshot and the exact approved bytes to a landable gate receipt, composes
// the final tree, and creates a one-parent commit through the supervised
// origin Git policy. The caller must have stopped and reaped workspace writers
// before calling Create.
func Create(ctx context.Context, request Request) (Result, error) {
	if err := validateRequest(ctx, request); err != nil {
		return Result{}, err
	}
	request.IntentBytes = bytes.Clone(request.IntentBytes)
	request.CandidateBytes = bytes.Clone(request.CandidateBytes)
	request.Environment = cleanRepositorySelectors(request.Environment)

	if !request.Gate.IsLandable() {
		return Result{}, ErrNotLandable
	}
	receipt, ok := request.Gate.Receipt()
	if !ok {
		return Result{}, ErrNotLandable
	}
	parsedIntent, err := intent.Parse(request.Slug, request.IntentBytes)
	if err != nil {
		return Result{}, fmt.Errorf("%w: approved Intent is invalid: %v", ErrInvalidRequest, err)
	}
	title := parsedIntent.Frontmatter.Title
	if strings.TrimSpace(title) == "" || strings.ContainsAny(title, "\r\n\x00") {
		return Result{}, fmt.Errorf("%w: Intent title must be one non-empty line", ErrInvalidRequest)
	}
	message := []byte(title + "\n\nKogen-Intent: " + request.Slug + "\n")
	approvalHash := intent.ApprovalSHA256(request.IntentBytes, request.CandidateBytes)
	if receipt.ApprovalSHA256 != approvalHash {
		return Result{}, fmt.Errorf("%w: approval hash differs from the gate receipt", ErrVerificationMismatch)
	}

	root, err := safefs.OpenRoot(request.Workspace)
	if err != nil {
		return Result{}, fmt.Errorf("landing commit: open candidate workspace: %w", err)
	}
	defer root.Close()
	if err := verifyProtectedInputs(root, request, parsedIntent.RawBytes()); err != nil {
		return Result{}, err
	}

	processes := request.Processes
	if processes == nil {
		processes = process.Supervisor{}
	}
	environment := request.Environment
	workspaceGit := gitio.NewWorkspace(processes)
	workspacePolicy := gitio.WorkspacePolicy(request.Workspace, environment)
	baseMetadata, err := gitio.LoadBaseMetadata(ctx, workspaceGit, workspacePolicy, request.BaseCommit)
	if err != nil {
		return Result{}, fmt.Errorf("landing commit: load immutable base metadata: %w", err)
	}
	baseRefs := gitio.NewRefPort(workspaceGit, workspacePolicy)
	resolvedBase, err := baseRefs.ResolveCommit(ctx, string(request.BaseCommit))
	if err != nil {
		return Result{}, fmt.Errorf("landing commit: resolve base commit: %w", err)
	}
	if resolvedBase != request.BaseCommit || string(baseMetadata.Tree()) != receipt.BaseTree {
		return Result{}, fmt.Errorf("%w: base commit/tree differs from the gate receipt", ErrVerificationMismatch)
	}

	verifiedTree, err := gitio.SnapshotCandidateTree(ctx, workspaceGit, workspacePolicy, baseMetadata)
	if err != nil {
		return Result{}, fmt.Errorf("landing commit: snapshot verified candidate: %w", err)
	}
	if string(verifiedTree) != receipt.CandidateTree {
		return Result{}, fmt.Errorf("%w: candidate tree is %s, gate verified %s", ErrVerificationMismatch, verifiedTree, receipt.CandidateTree)
	}

	finalTree, err := composeLandingTree(ctx, workspaceGit, workspacePolicy, verifiedTree, request, baseMetadata)
	if err != nil {
		return Result{}, err
	}
	if err := verifyLandingTree(ctx, workspaceGit, workspacePolicy, finalTree, request, parsedIntent.RawBytes(), baseMetadata); err != nil {
		return Result{}, err
	}
	if err := verifyProtectedInputs(root, request, parsedIntent.RawBytes()); err != nil {
		return Result{}, err
	}
	// Re-capture immediately before commit creation. This catches workspace
	// changes during tree composition; process custody still owns the stronger
	// guarantee that no writer can race this final observation.
	currentTree, err := gitio.SnapshotCandidateTree(ctx, workspaceGit, workspacePolicy, baseMetadata)
	if err != nil {
		return Result{}, fmt.Errorf("landing commit: recheck candidate before commit: %w", err)
	}
	if currentTree != verifiedTree {
		return Result{}, fmt.Errorf("%w: candidate changed during commit preparation", ErrVerificationMismatch)
	}

	originGit := gitio.NewOrigin(processes)
	originPolicy := gitio.OriginPolicy(request.Workspace, environment)
	originRefs := gitio.NewRefPort(originGit, originPolicy)
	commit, err := originRefs.CommitTree(ctx, contract.CommitTreeRequest{
		Tree:    finalTree,
		Parents: []contract.ObjectID{request.BaseCommit},
		Message: message,
	})
	if err != nil {
		return Result{}, fmt.Errorf("landing commit: create signed-or-user-policy commit: %w", err)
	}
	if err := verifyCommitObject(ctx, originGit, originPolicy, commit, request.BaseCommit, finalTree, message); err != nil {
		return Result{}, err
	}
	return Result{
		Commit:       commit,
		Parent:       request.BaseCommit,
		VerifiedTree: verifiedTree,
		Tree:         finalTree,
		Message:      bytes.Clone(message),
	}, nil
}

func validateRequest(ctx context.Context, request Request) error {
	if ctx == nil || request.Gate == nil || request.Protection == nil {
		return fmt.Errorf("%w: context, gate report, and protection manifest are required", ErrInvalidRequest)
	}
	if request.Workspace == "" || !filepath.IsAbs(request.Workspace) || filepath.Clean(request.Workspace) != request.Workspace || strings.ContainsRune(request.Workspace, '\x00') {
		return fmt.Errorf("%w: workspace must be a clean absolute path", ErrInvalidRequest)
	}
	if request.Environment == nil || request.Environment["PATH"] == "" {
		return fmt.Errorf("%w: captured controller Git environment with PATH is required", ErrInvalidRequest)
	}
	if err := gitio.ValidateObjectID(request.BaseCommit); err != nil {
		return fmt.Errorf("%w: base commit is not a full object ID: %v", ErrInvalidRequest, err)
	}
	if request.Slug == "" || len(request.IntentBytes) == 0 || len(request.CandidateBytes) == 0 {
		return fmt.Errorf("%w: Intent slug and approved Intent and candidate-test bytes are required", ErrInvalidRequest)
	}
	if !validGitPath(request.AcceptanceSourcePath) || !validGitPath(request.CandidatePath) || request.AcceptanceSourcePath == request.CandidatePath {
		return fmt.Errorf("%w: source and candidate test paths must be distinct canonical repository paths", ErrInvalidRequest)
	}
	intentPath := path.Join(".kogen", "intents", request.Slug, "intent.md")
	if !validGitPath(intentPath) || pathOverlap(intentPath, request.CandidatePath) || pathOverlap(intentPath, request.AcceptanceSourcePath) {
		return fmt.Errorf("%w: approved Intent and acceptance paths overlap", ErrInvalidRequest)
	}
	if pathOverlap(request.AcceptanceSourcePath, request.CandidatePath) {
		return fmt.Errorf("%w: source and candidate test paths overlap", ErrInvalidRequest)
	}
	if _, ok := request.Gate.Receipt(); !ok {
		return ErrNotLandable
	}
	return nil
}

func verifyProtectedInputs(root contract.RootedFS, request Request, intentBytes []byte) error {
	manifest := request.Protection.Manifest()
	intentPath := path.Join(".kogen", "intents", request.Slug, "intent.md")
	for name, expected := range map[string][]byte{
		intentPath:            intentBytes,
		request.CandidatePath: request.CandidateBytes,
	} {
		entry, ok := manifest[name]
		if !ok || !entry.Present || entry.Mode != gitio.GitModeRegular || !bytes.Equal(entry.Bytes, expected) {
			return fmt.Errorf("%w: approval manifest entry %q is absent or differs from approved bytes", ErrProtectedBytes, name)
		}
	}
	findings, err := request.Protection.Guard(root)
	if err != nil {
		return fmt.Errorf("landing commit: guard protected paths: %w", err)
	}
	if len(findings) != 0 {
		return fmt.Errorf("%w: protected path %q differs from approval", ErrProtectedBytes, findings[0].Path)
	}
	if _, err := root.Lstat(request.AcceptanceSourcePath); err == nil {
		return fmt.Errorf("%w: acceptance source copy %q must be absent", ErrProtectedBytes, request.AcceptanceSourcePath)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("landing commit: inspect acceptance source copy: %w", err)
	}
	return nil
}

func composeLandingTree(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, verifiedTree contract.ObjectID, request Request, base *gitio.BaseMetadata) (_ contract.ObjectID, resultErr error) {
	directory, err := os.MkdirTemp("", "kogen-landing-index-")
	if err != nil {
		return "", fmt.Errorf("landing commit: create private index directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		_ = os.RemoveAll(directory)
		return "", fmt.Errorf("landing commit: protect private index directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(directory); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("landing commit: remove private index directory: %w", err))
		}
	}()

	indexPath := filepath.Join(directory, "index")
	indexPolicy, err := policyWithIndex(policy, indexPath)
	if err != nil {
		return "", err
	}
	if err := checkedGit(ctx, git, []string{"read-tree", string(verifiedTree)}, nil, indexPolicy, "read verified tree"); err != nil {
		return "", err
	}
	format, err := gitio.NewRefPort(git, policy).ObjectFormat(ctx)
	if err != nil {
		return "", fmt.Errorf("landing commit: inspect Git object format: %w", err)
	}
	intentPath := path.Join(".kogen", "intents", request.Slug, "intent.md")
	files := []gitio.TreeFile{
		{Path: intentPath, Mode: gitio.GitModeRegular, Bytes: request.IntentBytes},
		{Path: request.CandidatePath, Mode: gitio.GitModeRegular, Bytes: request.CandidateBytes},
	}
	indexInfo := make([]byte, 0)
	for _, file := range files {
		result, err := git.Exec(ctx, []string{"hash-object", "-w", "--no-filters", "--stdin"}, file.Bytes, indexPolicy)
		if err != nil {
			return "", fmt.Errorf("landing commit: hash approved bytes for %q: %w", file.Path, err)
		}
		if err := requireGitSuccess("hash approved bytes", result); err != nil {
			return "", err
		}
		object, err := gitio.ParseObjectID(strings.TrimSpace(string(result.Stdout)))
		if err != nil {
			return "", fmt.Errorf("landing commit: parse approved blob ID: %w", err)
		}
		if len(object) != objectIDLength(format) {
			return "", errors.New("landing commit: approved blob ID does not match the repository object format")
		}
		indexInfo = append(indexInfo, fmt.Sprintf("%o %s\t", file.Mode, object)...)
		indexInfo = append(indexInfo, file.Path...)
		indexInfo = append(indexInfo, 0)
	}
	zero := strings.Repeat("0", objectIDLength(format))
	indexInfo = append(indexInfo, "0 "+zero+"\t"...)
	indexInfo = append(indexInfo, request.AcceptanceSourcePath...)
	indexInfo = append(indexInfo, 0)
	updated, err := git.Exec(ctx, []string{"update-index", "-z", "--index-info"}, indexInfo, indexPolicy)
	if err != nil {
		return "", fmt.Errorf("landing commit: stage exact approved tree entries: %w", err)
	}
	if err := requireGitSuccess("stage approved tree entries", updated); err != nil {
		return "", err
	}
	written, err := git.Exec(ctx, []string{"write-tree"}, nil, indexPolicy)
	if err != nil {
		return "", fmt.Errorf("landing commit: write final tree: %w", err)
	}
	if err := requireGitSuccess("write final tree", written); err != nil {
		return "", err
	}
	tree, err := gitio.ParseObjectID(strings.TrimSpace(string(written.Stdout)))
	if err != nil {
		return "", fmt.Errorf("landing commit: parse final tree ID: %w", err)
	}
	if len(tree) != objectIDLength(format) {
		return "", errors.New("landing commit: final tree ID does not match the repository object format")
	}
	if base == nil || base.Tree() == "" {
		return "", errors.New("landing commit: immutable base metadata is required")
	}
	return tree, nil
}

func verifyLandingTree(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, tree contract.ObjectID, request Request, intentBytes []byte, base *gitio.BaseMetadata) error {
	result, err := git.Exec(ctx, []string{"ls-tree", "-r", "-z", string(tree)}, nil, policy)
	if err != nil {
		return fmt.Errorf("landing commit: inspect final tree: %w", err)
	}
	if err := requireGitSuccess("inspect final tree", result); err != nil {
		return err
	}
	entries, err := parseTreeEntries(result.Stdout)
	if err != nil {
		return fmt.Errorf("landing commit: parse final tree: %w", err)
	}
	intentPath := path.Join(".kogen", "intents", request.Slug, "intent.md")
	for name, content := range map[string][]byte{intentPath: intentBytes, request.CandidatePath: request.CandidateBytes} {
		entry, ok := entries[name]
		if !ok || entry.Mode != "100644" || entry.Type != "blob" {
			return fmt.Errorf("%w: final tree is missing approved file %q", ErrProtectedBytes, name)
		}
		blob, err := git.Exec(ctx, []string{"cat-file", "blob", entry.Object}, nil, policy)
		if err != nil {
			return fmt.Errorf("landing commit: read approved blob %q: %w", name, err)
		}
		if err := requireGitSuccess("read approved blob", blob); err != nil {
			return err
		}
		if !bytes.Equal(blob.Stdout, content) {
			return fmt.Errorf("%w: final tree blob %q differs from approved bytes", ErrProtectedBytes, name)
		}
	}
	if _, exists := entries[request.AcceptanceSourcePath]; exists {
		return fmt.Errorf("%w: final tree contains acceptance source copy %q", ErrProtectedBytes, request.AcceptanceSourcePath)
	}
	if base == nil || base.Tree() == "" {
		return errors.New("landing commit: immutable base metadata is required")
	}
	return nil
}

type treeEntry struct {
	Mode   string
	Type   string
	Object string
}

func parseTreeEntries(data []byte) (map[string]treeEntry, error) {
	entries := make(map[string]treeEntry)
	if len(data) == 0 {
		return entries, nil
	}
	if data[len(data)-1] != 0 {
		return nil, errors.New("Git tree listing is not NUL terminated")
	}
	for _, record := range bytes.Split(data[:len(data)-1], []byte{0}) {
		metadata, name, ok := bytes.Cut(record, []byte{'\t'})
		if !ok || len(name) == 0 {
			return nil, errors.New("Git tree listing contains a malformed entry")
		}
		fields := strings.Fields(string(metadata))
		if len(fields) != 3 {
			return nil, errors.New("Git tree listing contains malformed metadata")
		}
		pathName := string(name)
		if _, exists := entries[pathName]; exists {
			return nil, fmt.Errorf("Git tree listing repeats path %q", pathName)
		}
		entries[pathName] = treeEntry{Mode: fields[0], Type: fields[1], Object: fields[2]}
	}
	return entries, nil
}

func verifyCommitObject(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, commit, parent, tree contract.ObjectID, message []byte) error {
	result, err := git.Exec(ctx, []string{"cat-file", "commit", string(commit)}, nil, policy)
	if err != nil {
		return fmt.Errorf("landing commit: inspect created commit: %w", err)
	}
	if err := requireGitSuccess("inspect created commit", result); err != nil {
		return err
	}
	header, actualMessage, ok := bytes.Cut(result.Stdout, []byte("\n\n"))
	if !ok || !bytes.Equal(actualMessage, message) {
		return fmt.Errorf("%w: created commit message differs from the required message", ErrVerificationMismatch)
	}
	var actualTree contract.ObjectID
	parents := make([]contract.ObjectID, 0, 2)
	for _, line := range bytes.Split(header, []byte{'\n'}) {
		key, value, found := bytes.Cut(line, []byte{' '})
		if !found {
			continue
		}
		switch string(key) {
		case "tree":
			if actualTree != "" {
				return errors.New("landing commit: created commit has duplicate tree headers")
			}
			actualTree = contract.ObjectID(value)
		case "parent":
			parents = append(parents, contract.ObjectID(value))
		}
	}
	if actualTree != tree || len(parents) != 1 || parents[0] != parent {
		return fmt.Errorf("%w: created commit tree or sole parent differs from the verified landing inputs", ErrVerificationMismatch)
	}
	return nil
}

func checkedGit(ctx context.Context, git contract.GitPort, args []string, stdin []byte, policy contract.GitPolicy, operation string) error {
	result, err := git.Exec(ctx, args, stdin, policy)
	if err != nil {
		return fmt.Errorf("landing commit: Git %s: %w", operation, err)
	}
	return requireGitSuccess(operation, result)
}

func requireGitSuccess(operation string, result contract.GitResult) error {
	if result.Process.ExitStatus == nil || *result.Process.ExitStatus != 0 || result.Process.TimedOut || result.Process.Unavailable {
		status := "unknown"
		if result.Process.ExitStatus != nil {
			status = fmt.Sprint(*result.Process.ExitStatus)
		}
		detail := strings.TrimSpace(string(result.StderrTail))
		if detail != "" {
			return fmt.Errorf("landing commit: Git %s failed (exit=%s): %s", operation, status, detail)
		}
		return fmt.Errorf("landing commit: Git %s failed (exit=%s)", operation, status)
	}
	return nil
}

func policyWithIndex(policy contract.GitPolicy, indexPath string) (contract.GitPolicy, error) {
	entries := make(process.Environment)
	for _, entry := range policy.Environment {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			return contract.GitPolicy{}, errors.New("landing commit: malformed Git environment")
		}
		if repositorySelector(key) {
			continue
		}
		entries[key] = value
	}
	entries["GIT_INDEX_FILE"] = indexPath
	policy.Environment = environmentList(entries)
	return policy, nil
}

func cleanRepositorySelectors(environment process.Environment) process.Environment {
	clean := make(process.Environment, len(environment))
	for key, value := range environment {
		if repositorySelector(key) {
			continue
		}
		clean[key] = value
	}
	return clean
}

func repositorySelector(key string) bool {
	switch key {
	case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_PREFIX", "GIT_CEILING_DIRECTORIES",
		"GIT_DISCOVERY_ACROSS_FILESYSTEM":
		return true
	default:
		return false
	}
}

func environmentList(environment process.Environment) []string {
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	// Git's operation runner accepts an ordinary KEY=value vector. Sorting here
	// keeps repeated invocations deterministic and avoids environment leakage.
	sort.Strings(keys)
	entries := make([]string, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, key+"="+environment[key])
	}
	return entries
}

func objectIDLength(format gitio.ObjectFormat) int {
	if format == gitio.ObjectFormatSHA256 {
		return 64
	}
	return 40
}

func validGitPath(value string) bool {
	if value == "" || !fs.ValidPath(value) || strings.ContainsRune(value, '\x00') {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if strings.EqualFold(component, ".git") {
			return false
		}
	}
	return true
}

func pathOverlap(left, right string) bool {
	if left == right {
		return true
	}
	return strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}
