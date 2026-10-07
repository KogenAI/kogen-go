package remove

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/project"
	"kogen-go/internal/safefs"
	"kogen-go/internal/yamlmini"
)

var (
	slugPattern  = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	runIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// Request selects one Intent and whether the user explicitly accepts removing
// its approval and prior failed or parked Build state.
type Request struct {
	Project *project.Resolution
	Slug    string
	Force   bool
}

// Dependencies are the supervised Git, path policy, and rooted filesystem
// effects required by removal.
type Dependencies struct {
	Git    contract.GitPort
	Policy project.PolicyForDirectory
	Roots  contract.RootOpener
}

// Result identifies the local path-limited commit created by Remove.
type Result struct {
	Commit contract.ObjectID
}

// Failure is a command-facing refusal or controller/environment error.
// Refusals use the stable intent/<reason> prefix; operational failures retain
// a class and a bounded detail from the supervised operation.
type Failure struct {
	Class  contract.ErrorClass
	Reason string
	Exit   contract.ExitCode
	Detail string
	Cause  error
}

func (f *Failure) Error() string {
	if f == nil {
		return "intent/remove_failed"
	}
	class := f.Class
	if class == "" {
		class = "intent"
	}
	message := string(class) + "/" + f.Reason
	if f.Detail != "" {
		message += ": " + f.Detail
	} else if f.Cause != nil {
		message += ": " + f.Cause.Error()
	}
	return message
}

func (f *Failure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Cause
}

type runRecord struct {
	RunID          string `json:"run_id"`
	Slug           string `json:"slug"`
	ApprovalCommit string `json:"approval_commit"`
	Status         string `json:"status"`
	StartedMS      int64  `json:"started_ms"`
	Reason         string `json:"-"`
}

// Remove refuses to remove a currently claimed Build even with Force. It
// builds the removal commit from HEAD through a private index, advances only
// the checkout ref, removes only the two source paths from the real index, and
// CAS-deletes only the approval ref value observed before the operation.
func Remove(ctx context.Context, request Request, dependencies Dependencies) (*Result, error) {
	if ctx == nil {
		return nil, failure("controller", "remove_failed", 70, "context is required", nil)
	}
	if request.Project == nil || dependencies.Git == nil || dependencies.Policy == nil {
		return nil, failure("controller", "remove_failed", 70, "resolved project, Git port, and policy are required", nil)
	}
	if !validSlug(request.Slug) {
		return nil, failure("intent", "invalid_slug", 2, "Slug must use lowercase letters, digits, and dashes.", nil)
	}
	project := request.Project
	if !cleanAbsolute(project.Checkout) || !cleanAbsolute(project.Origin) || !cleanAbsolute(project.StateRoot) || project.Base == "" {
		return nil, failure("controller", "remove_failed", 70, "project checkout, origin, state root, and base must be resolved", nil)
	}

	roots := dependencies.Roots
	if roots == nil {
		roots = safefs.Opener{}
	}
	checkout, err := roots.OpenRoot(project.Checkout)
	if err != nil {
		return nil, operationFailure("cannot open checkout root", err)
	}
	defer closeRoot(checkout)

	intentPath := ".kogen/intents/" + request.Slug + "/intent.md"
	intentInfo, err := checkout.Lstat(intentPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, failure("intent", "not_found", 2, "Intent does not exist", nil)
	}
	if err != nil {
		return nil, operationFailure("cannot inspect Intent source", err)
	}
	if !intentInfo.Mode().IsRegular() {
		return nil, failure("environment", "unsafe_intent_source", 3, "Intent source is not a regular file", nil)
	}

	originPolicy := dependencies.Policy(project.Origin)
	checkoutPolicy := dependencies.Policy(project.Checkout)
	originRefs := gitio.NewRefPort(dependencies.Git, originPolicy)

	claim, err := originRefs.ReadRef(ctx, "refs/kogen/claim")
	if err != nil {
		return nil, operationFailure("cannot read active Build claim", err)
	}
	stateRoot, err := openStateRoot(roots, project.StateRoot)
	if err != nil {
		return nil, operationFailure("cannot open Build state", err)
	}
	if stateRoot != nil {
		defer closeRoot(stateRoot)
	}
	if claim.Exists {
		claimRunID, err := claimRun(ctx, dependencies.Git, originPolicy, claim.Target)
		if err != nil {
			return nil, operationFailure("cannot read active Build claim", err)
		}
		if claimRunID != "" {
			run, readErr := readRun(stateRoot, claimRunID)
			if readErr != nil {
				return nil, operationFailure("cannot read claimed Build state", readErr)
			}
			if run == nil {
				return nil, failure("controller", "claim_state_missing", 70, "active Build claim has no readable run snapshot", nil)
			}
			if run.Slug == request.Slug && run.Status == "running" {
				return nil, failure("intent", "remove_blocked", 2, "Intent is in an active Build and cannot be removed", nil)
			}
		}
	}

	approvalRef := "refs/kogen/intents/" + request.Slug
	approval, err := originRefs.ReadRef(ctx, approvalRef)
	if err != nil {
		return nil, operationFailure("cannot read approval ref", err)
	}
	if approval.Exists {
		symbolic, symErr := symbolicRef(ctx, dependencies.Git, originPolicy, approvalRef)
		if symErr != nil {
			return nil, operationFailure("cannot inspect approval ref", symErr)
		}
		if symbolic {
			return nil, failure("controller", "approval_ref_unsafe", 70, "approval ref is symbolic and was left unchanged", nil)
		}
	}

	baseCommit, err := originRefs.ResolveCommit(ctx, project.Base)
	if err != nil {
		return nil, operationFailure("cannot resolve configured base", err)
	}
	landed, err := pathExistsAtCommit(ctx, dependencies.Git, originPolicy, baseCommit, intentPath)
	if err != nil {
		return nil, operationFailure("cannot inspect configured base", err)
	}
	latest, err := latestRunForSlug(stateRoot, request.Slug)
	if err != nil {
		return nil, operationFailure("cannot read Build history", err)
	}
	if !request.Force && !landed {
		if detail := forceReason(latest, approval); detail != "" {
			return nil, failure("intent", "remove_requires_force", 2, detail, nil)
		}
	}

	acceptancePath, err := acceptanceSourcePath(project, checkout, request.Slug)
	if err != nil {
		return nil, failure("controller", "remove_failed", 70, "cannot resolve acceptance source path", err)
	}
	paths := []string{intentPath, acceptancePath}
	commit, err := pathLimitedCommit(ctx, dependencies.Git, checkoutPolicy, request.Slug, paths)
	if err != nil {
		var removalFailure *Failure
		if errors.As(err, &removalFailure) {
			return nil, removalFailure
		}
		return nil, operationFailure("cannot commit Intent removal", err)
	}

	for _, sourcePath := range []string{".kogen/intents/" + request.Slug, acceptancePath} {
		if err := removeTree(checkout, sourcePath); err != nil {
			return nil, operationFailure("cannot remove Intent source files", err)
		}
	}

	if approval.Exists {
		if err := deleteApprovalRefCAS(ctx, dependencies.Git, originPolicy, approvalRef, approval.Target); err != nil {
			return nil, err
		}
	} else {
		// Do not claim success if another owner created an approval while the
		// local path commit was being published. It remains theirs to inspect.
		current, readErr := originRefs.ReadRef(ctx, approvalRef)
		if readErr != nil {
			return nil, operationFailure("cannot recheck approval ref", readErr)
		}
		if current.Exists {
			return nil, approvalRefChanged(nil)
		}
	}
	return &Result{Commit: commit}, nil
}

func validSlug(slug string) bool {
	return len(slug) >= 3 && len(slug) <= 48 && slugPattern.MatchString(slug)
}

func cleanAbsolute(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsRune(value, '\x00')
}

func failure(class, reason string, exit contract.ExitCode, detail string, cause error) *Failure {
	return &Failure{Class: contract.ErrorClass(class), Reason: reason, Exit: exit, Detail: detail, Cause: cause}
}

func operationFailure(operation string, cause error) *Failure {
	return failure("environment", "remove_failed", 3, operation, cause)
}

func closeRoot(root contract.RootedFS) {
	if closer, ok := root.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}

func openStateRoot(roots contract.RootOpener, directory string) (contract.RootedFS, error) {
	root, err := roots.OpenRoot(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return root, err
}

func claimRun(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, target contract.ObjectID) (string, error) {
	result, err := runGit(ctx, git, policy, []string{"cat-file", "blob", string(target) + ":.kogen/claim"}, nil)
	if err != nil {
		return "", err
	}
	if result.Process.ExitStatus == nil || *result.Process.ExitStatus != 0 {
		return "", gitResultError("read claim blob", result)
	}
	runID := strings.TrimSpace(string(result.Stdout))
	if runID == "" {
		return "", errors.New("claim blob has an empty run identity")
	}
	if !runIDPattern.MatchString(runID) {
		return "", errors.New("claim blob has an invalid run identity")
	}
	return runID, nil
}

func readRun(root contract.RootedFS, runID string) (*runRecord, error) {
	if root == nil || !runIDPattern.MatchString(runID) {
		return nil, nil
	}
	directory := "runs/" + runID
	dirInfo, err := root.Lstat(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !dirInfo.IsDir() || dirInfo.Mode()&fs.ModeSymlink != 0 {
		return nil, errors.New("run directory is not a real directory")
	}
	name := directory + "/run.json"
	fileInfo, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !fileInfo.Mode().IsRegular() {
		return nil, errors.New("run snapshot is not a regular file")
	}
	contents, err := root.ReadFile(name)
	if err != nil {
		return nil, err
	}
	var record runRecord
	if err := json.Unmarshal(contents, &record); err != nil {
		return nil, fmt.Errorf("decode run snapshot: %w", err)
	}
	if record.RunID != runID || !validSlug(record.Slug) || record.StartedMS < 0 {
		return nil, errors.New("run snapshot identity is inconsistent")
	}
	if record.Status == "failed" {
		record.Reason, err = finishedReason(root, directory+"/events.jsonl")
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	return &record, nil
}

func finishedReason(root contract.RootedFS, name string) (string, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("run event journal is not a regular file")
	}
	contents, err := root.ReadFile(name)
	if err != nil {
		return "", err
	}
	var reason string
	scanner := bufio.NewScanner(strings.NewReader(string(contents)))
	for scanner.Scan() {
		var event struct {
			Event  string `json:"event"`
			Reason string `json:"reason"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Event == "finished" {
			reason = event.Reason
		}
	}
	return reason, scanner.Err()
}

func latestRunForSlug(root contract.RootedFS, slug string) (*runRecord, error) {
	if root == nil {
		return nil, nil
	}
	runsInfo, err := root.Lstat("runs")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !runsInfo.IsDir() || runsInfo.Mode()&fs.ModeSymlink != 0 {
		return nil, errors.New("runs state is not a real directory")
	}
	entries, err := root.ReadDir("runs")
	if err != nil {
		return nil, err
	}
	var latest *runRecord
	for _, entry := range entries {
		if !entry.IsDir() || !runIDPattern.MatchString(entry.Name()) {
			continue
		}
		record, readErr := readRun(root, entry.Name())
		if readErr != nil || record == nil || record.Slug != slug {
			continue
		}
		if latest == nil || record.StartedMS > latest.StartedMS || record.StartedMS == latest.StartedMS && record.RunID > latest.RunID {
			latest = record
		}
	}
	return latest, nil
}

func forceReason(latest *runRecord, approval contract.RefObservation) string {
	if latest != nil && (!approval.Exists || latest.ApprovalCommit == string(approval.Target)) {
		switch latest.Status {
		case "failed":
			if latest.Reason == "interrupted" {
				return "Intent still has an approval ref; pass --force to discard the approval and remove its files"
			}
			return "Intent still has a failed Build approval; pass --force to discard the approval and remove its files"
		case "parked":
			return "Intent still has a parked Build approval; pass --force to discard the approval and remove its files"
		case "interrupted":
			return "Intent still has an approval ref; pass --force to discard the approval and remove its files"
		}
	}
	if approval.Exists {
		return "Intent approved or queued; pass --force to discard the approval and remove its files"
	}
	return ""
}

func pathExistsAtCommit(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, commit contract.ObjectID, name string) (bool, error) {
	pathspec := ":(literal)" + name
	result, err := runGit(ctx, git, policy, []string{"ls-tree", "-r", "--name-only", "-z", string(commit), "--", pathspec}, nil)
	if err != nil {
		return false, err
	}
	if result.Process.ExitStatus == nil || *result.Process.ExitStatus != 0 {
		return false, gitResultError("read base tree path", result)
	}
	for _, candidate := range bytesSplitNUL(result.Stdout) {
		if candidate == name {
			return true, nil
		}
	}
	return false, nil
}

func bytesSplitNUL(contents []byte) []string {
	if len(contents) == 0 {
		return nil
	}
	parts := strings.Split(string(contents), "\x00")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func acceptanceSourcePath(resolved *project.Resolution, root contract.RootedFS, slug string) (string, error) {
	extension, adapter := "", ""
	if resolved.Config != nil {
		if config, ok := resolved.Config.Raw["acceptance"].(yamlmini.Mapping); ok {
			adapter, _ = config["adapter"].(string)
			extension, _ = config["ext"].(string)
		}
	}
	if extension == "" {
		switch adapter {
		case "rails":
			extension = "_test.rb"
		case "exunit":
			extension = "_test.exs"
		case "command":
			return "", errors.New("command acceptance requires ext")
		default:
			if isRegular(root, "Gemfile") && isRegular(root, "config/application.rb") {
				extension = "_test.rb"
			} else if isRegular(root, "mix.exs") {
				extension = "_test.exs"
			} else {
				extension = ".t.sh"
			}
		}
	}
	if extension == "." || extension == ".." || strings.ContainsAny(extension, "/\\\x00\r\n") {
		return "", errors.New("acceptance extension is unsafe")
	}
	return path.Join(".kogen", "acceptance", slug+extension), nil
}

func isRegular(root contract.RootedFS, name string) bool {
	info, err := root.Lstat(name)
	return err == nil && info.Mode().IsRegular()
}

func pathLimitedCommit(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, slug string, paths []string) (contract.ObjectID, error) {
	refs := gitio.NewRefPort(git, policy)
	parent, err := refs.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return "", err
	}
	branchRef, detached, err := checkoutRef(ctx, git, policy)
	if err != nil {
		return "", err
	}
	indexDir, err := os.MkdirTemp("", "kogen-remove-index-")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(indexDir, 0o700); err != nil {
		_ = os.RemoveAll(indexDir)
		return "", err
	}
	defer os.RemoveAll(indexDir)
	privatePolicy, err := privateIndexPolicy(policy, filepath.Join(indexDir, "index"))
	if err != nil {
		return "", err
	}
	if err := gitSuccess(ctx, git, privatePolicy, []string{"read-tree", string(parent)}, nil, "read checkout base into private index"); err != nil {
		return "", err
	}
	tracked, err := runGit(ctx, git, privatePolicy, append([]string{"ls-files", "--error-unmatch", "--"}, paths...), nil)
	if err != nil {
		return "", err
	}
	if tracked.Process.ExitStatus == nil || *tracked.Process.ExitStatus == 1 {
		return "", failure("intent", "remove_requires_commit", 2, "Intent files must be tracked to record their removal", nil)
	}
	if *tracked.Process.ExitStatus != 0 {
		return "", gitResultError("check tracked Intent paths", tracked)
	}
	removeArgs := append([]string{"update-index", "--force-remove", "--"}, paths...)
	if err := gitSuccess(ctx, git, privatePolicy, removeArgs, nil, "remove Intent paths from private index"); err != nil {
		return "", err
	}
	treeResult, err := runGit(ctx, git, privatePolicy, []string{"write-tree"}, nil)
	if err != nil {
		return "", err
	}
	if treeResult.Process.ExitStatus == nil || *treeResult.Process.ExitStatus != 0 {
		return "", gitResultError("write removal tree", treeResult)
	}
	tree, err := gitio.ParseObjectID(strings.TrimSpace(string(treeResult.Stdout)))
	if err != nil {
		return "", err
	}
	commit, err := refs.CommitTree(ctx, contract.CommitTreeRequest{
		Tree: tree, Parents: []contract.ObjectID{parent}, Message: []byte("Remove Intent " + slug + "\n"),
	})
	if err != nil {
		return "", err
	}
	refArg := branchRef
	if detached {
		refArg = "HEAD"
	}
	update, err := runGit(ctx, git, policy, []string{"update-ref", "--no-deref", refArg, string(commit), string(parent)}, nil)
	if err != nil {
		return "", err
	}
	if update.Process.ExitStatus == nil || *update.Process.ExitStatus != 0 {
		return "", gitResultError("publish checkout removal commit", update)
	}
	if err := gitSuccess(ctx, git, policy, removeArgs, nil, "remove Intent paths from checkout index"); err != nil {
		return "", err
	}
	return commit, nil
}

func checkoutRef(ctx context.Context, git contract.GitPort, policy contract.GitPolicy) (string, bool, error) {
	result, err := runGit(ctx, git, policy, []string{"symbolic-ref", "-q", "HEAD"}, nil)
	if err != nil {
		return "", false, err
	}
	if result.Process.ExitStatus == nil {
		return "", false, errors.New("symbolic-ref returned no exit status")
	}
	if *result.Process.ExitStatus == 1 {
		return "", true, nil
	}
	if *result.Process.ExitStatus != 0 {
		return "", false, gitResultError("resolve checkout HEAD ref", result)
	}
	ref := strings.TrimSpace(string(result.Stdout))
	if !strings.HasPrefix(ref, "refs/heads/") || strings.ContainsAny(ref, "\x00\r\n") {
		return "", false, errors.New("checkout HEAD does not resolve to a branch ref")
	}
	valid, err := runGit(ctx, git, policy, []string{"check-ref-format", ref}, nil)
	if err != nil {
		return "", false, err
	}
	if valid.Process.ExitStatus == nil || *valid.Process.ExitStatus != 0 {
		return "", false, gitResultError("validate checkout branch ref", valid)
	}
	nested, err := runGit(ctx, git, policy, []string{"symbolic-ref", "-q", ref}, nil)
	if err != nil {
		return "", false, err
	}
	if nested.Process.ExitStatus == nil {
		return "", false, errors.New("symbolic-ref returned no exit status")
	}
	if *nested.Process.ExitStatus == 0 {
		return "", false, errors.New("checkout branch ref is symbolic")
	}
	if *nested.Process.ExitStatus != 1 {
		return "", false, gitResultError("inspect checkout branch ref", nested)
	}
	return ref, false, nil
}

func privateIndexPolicy(policy contract.GitPolicy, indexPath string) (contract.GitPolicy, error) {
	environment := make(map[string]string, len(policy.Environment)+1)
	for _, item := range policy.Environment {
		key, value, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			return contract.GitPolicy{}, errors.New("Git policy contains malformed environment")
		}
		environment[key] = value
	}
	for _, key := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_PREFIX", "GIT_CEILING_DIRECTORIES",
		"GIT_DISCOVERY_ACROSS_FILESYSTEM",
	} {
		delete(environment, key)
	}
	environment["GIT_INDEX_FILE"] = indexPath
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	policy.Environment = make([]string, 0, len(keys))
	for _, key := range keys {
		policy.Environment = append(policy.Environment, key+"="+environment[key])
	}
	return policy, nil
}

func gitSuccess(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, args []string, stdin []byte, operation string) error {
	result, err := runGit(ctx, git, policy, args, stdin)
	if err != nil {
		return err
	}
	if result.Process.ExitStatus == nil || *result.Process.ExitStatus != 0 {
		return gitResultError(operation, result)
	}
	return nil
}

func runGit(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, args []string, stdin []byte) (contract.GitResult, error) {
	result, err := git.Exec(ctx, args, stdin, policy)
	if err != nil {
		return result, err
	}
	if result.Process.TimedOut {
		return result, errors.New("supervised Git operation timed out")
	}
	if result.Process.Unavailable {
		return result, errors.New("Git executable is unavailable")
	}
	return result, nil
}

func gitResultError(operation string, result contract.GitResult) error {
	detail := strings.TrimSpace(string(result.StderrTail))
	if detail == "" {
		detail = fmt.Sprintf("exit status %v", result.Process.ExitStatus)
	}
	return fmt.Errorf("git %s: %s", operation, detail)
}

func symbolicRef(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, name string) (bool, error) {
	result, err := runGit(ctx, git, policy, []string{"symbolic-ref", "-q", name}, nil)
	if err != nil {
		return false, err
	}
	if result.Process.ExitStatus == nil {
		return false, errors.New("symbolic-ref returned no exit status")
	}
	switch *result.Process.ExitStatus {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, gitResultError("inspect ref", result)
	}
}

func deleteApprovalRefCAS(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, name string, expected contract.ObjectID) error {
	result, err := runGit(ctx, git, policy, []string{"update-ref", "--no-deref", "-d", name, string(expected)}, nil)
	if err != nil {
		return operationFailure("cannot CAS-delete approval ref", err)
	}
	if result.Process.ExitStatus != nil && *result.Process.ExitStatus == 0 {
		return nil
	}
	current, readErr := gitio.NewRefPort(git, policy).ReadRef(ctx, name)
	if readErr != nil || !current.Exists || current.Target != expected {
		return approvalRefChanged(readErr)
	}
	symbolic, symErr := symbolicRef(ctx, git, policy, name)
	if symErr != nil || symbolic {
		return approvalRefChanged(symErr)
	}
	return operationFailure("cannot CAS-delete approval ref", gitResultError("delete approval ref", result))
}

func approvalRefChanged(cause error) *Failure {
	return failure("controller", "approval_ref_changed", 70, "approval ref changed while removing the Intent; review the ref before retrying", cause)
}

func removeTree(root contract.RootedFS, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() && info.Mode()&fs.ModeSymlink == 0 {
		entries, err := root.ReadDir(name)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := removeTree(root, path.Join(name, entry.Name())); err != nil {
				return err
			}
		}
	}
	return root.Remove(name)
}
