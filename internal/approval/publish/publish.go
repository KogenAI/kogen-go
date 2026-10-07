package publish

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
	"sort"
	"strings"
	"time"

	"kogen-go/internal/approval/prepare"
	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/intent"
	"kogen-go/internal/project"
	"kogen-go/internal/safefs"
)

var (
	approvalHashPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	approvalPrefixPattern = regexp.MustCompile(`^[0-9a-f]{6,64}$`)
	approvalSlugPattern   = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

// Failure is a publish refusal with a stable command-facing reason.
type Failure struct {
	Reason string
	Cause  error
}

func (failure *Failure) Error() string {
	if failure == nil {
		return "approval publish failed"
	}
	message := "intent/" + failure.Reason
	if failure.Cause != nil {
		message += ": " + failure.Cause.Error()
	}
	return message
}

func (failure *Failure) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.Cause
}

func failure(reason string, cause error) *Failure {
	return &Failure{Reason: reason, Cause: cause}
}

// Request contains the resolved project, its approved immutable preparation,
// the timestamp shared by approval.json and the commit trailers, and an
// optional witness record for witness-mode approval.
type Request struct {
	Project         *project.Resolution
	Prepared        *prepare.Prepared
	GivenHashPrefix string
	At              string
	Witness         *Witness
}

// Witness is the v1.2 witness record persisted in approval.json.
type Witness struct {
	Verdict    string `json:"verdict"`
	Commit     string `json:"commit"`
	DiffSHA256 string `json:"diff_sha256"`
	BaseSHA    string `json:"base_sha"`
}

// Dependencies are the effect ports used to read the live sources and to
// write exact Git objects and the approval ref.
type Dependencies struct {
	Git    contract.GitPort
	Policy project.PolicyForDirectory
	Roots  contract.RootOpener
}

// Result identifies the immutable approval commit that won the ref CAS.
type Result struct {
	Ref     string
	Commit  contract.ObjectID
	Parent  contract.ObjectID
	Retried bool
}

type approvalDocument struct {
	Schema            int                   `json:"schema"`
	Slug              string                `json:"slug"`
	ApprovalSHA256    string                `json:"approval_sha256"`
	IntentSHA256      string                `json:"intent_sha256"`
	TargetBranch      string                `json:"target_branch"`
	BaseSHA           string                `json:"base_sha"`
	Domains           []string              `json:"domains"`
	AcceptancePaths   []string              `json:"acceptance_paths"`
	ProtectedManifest map[string]string     `json:"protected_manifest"`
	CheckBaseline     []prepare.BaselineRow `json:"check_baseline"`
	Witness           *Witness              `json:"witness"`
	By                string                `json:"by"`
	At                string                `json:"at"`
}

type treeEntry struct {
	mode     string
	typeName string
	name     string
	object   contract.ObjectID
}

// Publish commits the prepared approval package and updates its per-Intent ref
// with an expected-old-value CAS. A lost ref race is reread and retried once;
// the source digest is checked immediately before each CAS.
func Publish(ctx context.Context, request Request, dependencies Dependencies) (*Result, error) {
	if ctx == nil {
		return nil, failure("approval_publish_failed", errors.New("context is required"))
	}
	if err := validateRequest(request, dependencies); err != nil {
		return nil, err
	}
	prepared := request.Prepared
	policy := dependencies.Policy(request.Project.Origin)
	if policy.WorkingDirectory != request.Project.Origin {
		return nil, failure("approval_publish_failed", errors.New("origin Git policy must be rooted at the resolved origin"))
	}
	refs := gitio.NewRefPort(dependencies.Git, policy)

	roots := dependencies.Roots
	if roots == nil {
		roots = safefs.Opener{}
	}
	checkout, err := roots.OpenRoot(request.Project.Checkout)
	if err != nil {
		return nil, failure("approval_publish_failed", fmt.Errorf("open checkout root: %w", err))
	}
	defer closeRoot(checkout)

	paths := packagePaths(prepared)
	if err := readAndVerifySources(checkout, paths, prepared, request.GivenHashPrefix); err != nil {
		return nil, err
	}
	ledger := matchingLedger(checkout, prepared.Intent.Slug, prepared.ApprovalSHA256)
	approvalBytes, err := encodeApproval(request)
	if err != nil {
		return nil, failure("approval_publish_failed", err)
	}
	files := []blobFile{
		{path: paths.intent, bytes: bytes.Clone(prepared.IntentBytes)},
		{path: ".kogen/intents/" + prepared.Intent.Slug + "/approval.json", bytes: approvalBytes},
		{path: paths.acceptance, bytes: bytes.Clone(prepared.AcceptanceBytes)},
	}
	if ledger != nil {
		files = append(files, blobFile{path: ".kogen/intents/" + prepared.Intent.Slug + "/ledger.json", bytes: ledger})
	}

	format, err := refs.ObjectFormat(ctx)
	if err != nil {
		return nil, failure("approval_publish_failed", fmt.Errorf("read origin object format: %w", err))
	}
	baseCommit, err := refs.ResolveCommit(ctx, string(prepared.BaseCommit))
	if err != nil {
		return nil, failure("approval_publish_failed", fmt.Errorf("prepared base commit is unavailable: %w", err))
	}
	if baseCommit != prepared.BaseCommit {
		return nil, failure("approval_publish_failed", errors.New("prepared base commit resolved to a different object"))
	}
	ref := "refs/kogen/intents/" + prepared.Intent.Slug
	current, err := readApprovalRef(ctx, dependencies.Git, policy, refs, ref)
	if err != nil {
		return nil, failure("approval_publish_failed", fmt.Errorf("read approval ref: %w", err))
	}
	tree, err := writeApprovalTree(ctx, dependencies.Git, policy, format, files)
	if err != nil {
		return nil, failure("approval_publish_failed", err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		var parent contract.ObjectID
		parents := []contract.ObjectID(nil)
		expected := contract.ObjectID("")
		if current.Exists {
			parent, err = refs.ResolveCommit(ctx, string(current.Target))
			if err != nil {
				return nil, failure("approval_publish_failed", fmt.Errorf("approval ref does not name a commit: %w", err))
			}
			if parent != current.Target {
				return nil, failure("approval_publish_failed", errors.New("approval ref does not name a commit object"))
			}
			parents = []contract.ObjectID{parent}
			expected = current.Target
		}

		message := approvalMessage(prepared.Intent.Slug, prepared.Approver, prepared.ApprovalSHA256, request.At)
		commit, err := refs.CommitTree(ctx, contract.CommitTreeRequest{Tree: tree, Parents: parents, Message: []byte(message)})
		if err != nil {
			return nil, failure("approval_publish_failed", fmt.Errorf("create immutable approval commit: %w", err))
		}
		if err := readAndVerifySources(checkout, paths, prepared, request.GivenHashPrefix); err != nil {
			return nil, err
		}

		updated, err := compareAndSwap(ctx, dependencies.Git, policy, format, ref, expected, commit)
		if err != nil {
			return nil, failure("approval_publish_failed", err)
		}
		if updated {
			return &Result{Ref: ref, Commit: commit, Parent: parent, Retried: attempt != 0}, nil
		}
		if attempt == 1 {
			return nil, failure("approval_ref_conflict", errors.New("approval ref moved twice while publishing; the concurrent ref was preserved"))
		}
		current, err = readApprovalRef(ctx, dependencies.Git, policy, refs, ref)
		if err != nil {
			return nil, failure("approval_publish_failed", fmt.Errorf("reread approval ref after a lost race: %w", err))
		}
	}
	return nil, failure("approval_publish_failed", errors.New("unreachable approval publisher state"))
}

type packageFilePaths struct {
	intent     string
	acceptance string
}

func packagePaths(prepared *prepare.Prepared) packageFilePaths {
	return packageFilePaths{
		intent:     ".kogen/intents/" + prepared.Intent.Slug + "/intent.md",
		acceptance: prepared.AcceptanceSourcePath,
	}
}

func validateRequest(request Request, dependencies Dependencies) error {
	prepared := request.Prepared
	if request.Project == nil || prepared == nil || prepared.Intent == nil {
		return failure("approval_publish_failed", errors.New("resolved project and prepared approval are required"))
	}
	if dependencies.Git == nil || dependencies.Policy == nil {
		return failure("approval_publish_failed", errors.New("Git and origin policy ports are required"))
	}
	if !cleanAbsolute(request.Project.Checkout) || !cleanAbsolute(request.Project.Origin) || request.Project.Base == "" {
		return failure("approval_publish_failed", errors.New("project checkout, origin, and base must be resolved"))
	}
	if prepared.IsCard {
		return failure("approval_publish_failed", errors.New("a card-only preparation cannot be published"))
	}
	if !approvalPrefixPattern.MatchString(request.GivenHashPrefix) || !strings.HasPrefix(prepared.ApprovalSHA256, request.GivenHashPrefix) {
		return failure("approval_publish_failed", errors.New("provided approval hash prefix does not match the prepared digest"))
	}
	slug := prepared.Intent.Slug
	if !approvalSlugPattern.MatchString(slug) {
		return failure("approval_publish_failed", errors.New("prepared Intent slug is invalid"))
	}
	if request.Project.Base == "" || prepared.BaseCommit == "" {
		return failure("approval_publish_failed", errors.New("prepared base identity is missing"))
	}
	if err := gitio.ValidateObjectID(prepared.BaseCommit); err != nil {
		return failure("approval_publish_failed", fmt.Errorf("prepared base commit: %w", err))
	}
	if request.At == "" {
		return failure("approval_publish_failed", errors.New("approval timestamp is required"))
	}
	if _, err := time.Parse(time.RFC3339, request.At); err != nil {
		return failure("approval_publish_failed", fmt.Errorf("approval timestamp is not RFC 3339: %w", err))
	}
	if strings.TrimSpace(prepared.Approver) == "" || strings.ContainsAny(prepared.Approver, "\r\n\x00") {
		return failure("approval_publish_failed", errors.New("prepared approver must be a non-empty single line"))
	}
	if len(prepared.IntentBytes) == 0 || !bytes.Equal(prepared.Intent.RawBytes(), prepared.IntentBytes) {
		return failure("approval_publish_failed", errors.New("prepared Intent object does not match its exact source bytes"))
	}
	if !approvalHashPattern.MatchString(prepared.ApprovalSHA256) || intent.ApprovalSHA256(prepared.IntentBytes, prepared.AcceptanceBytes) != prepared.ApprovalSHA256 {
		return failure("approval_publish_failed", errors.New("prepared approval digest does not match the exact source bytes"))
	}
	if intent.IntentSHA256(prepared.IntentBytes) != prepared.IntentSHA256 {
		return failure("approval_publish_failed", errors.New("prepared Intent digest does not match the exact Intent bytes"))
	}
	if err := validateAcceptanceSourcePath(prepared.Intent.Slug, prepared.AcceptanceSourcePath); err != nil {
		return failure("approval_publish_failed", err)
	}
	return nil
}

func validateAcceptanceSourcePath(slug, name string) error {
	if !fs.ValidPath(name) || path.Clean(name) != name || path.Dir(name) != ".kogen/acceptance" || strings.ContainsRune(name, '\\') {
		return errors.New("prepared acceptance source path is unsafe")
	}
	leaf := path.Base(name)
	suffix := strings.TrimPrefix(leaf, slug)
	if suffix == "" || suffix == leaf || (suffix[0] != '.' && suffix[0] != '_') || strings.ContainsRune(leaf, 0) {
		return errors.New("prepared acceptance source path does not belong to the Intent")
	}
	return nil
}

func cleanAbsolute(name string) bool {
	return name != "" && !strings.ContainsRune(name, 0) && filepath.IsAbs(name) && filepath.Clean(name) == name
}

func encodeApproval(request Request) ([]byte, error) {
	prepared := request.Prepared
	baseline := append([]prepare.BaselineRow(nil), prepared.CheckBaseline...)
	if baseline == nil {
		baseline = []prepare.BaselineRow{}
	}
	for index := range baseline {
		baseline[index].Findings = append([]prepare.BaselineFinding(nil), baseline[index].Findings...)
		if baseline[index].Findings == nil {
			baseline[index].Findings = []prepare.BaselineFinding{}
		}
	}
	domains := append([]string(nil), prepared.Intent.Frontmatter.Domains...)
	if domains == nil {
		domains = []string{}
	}
	acceptancePaths := []string{prepared.AcceptanceSourcePath}
	manifest := prepared.ProtectedManifest.Hashes()
	if manifest == nil {
		manifest = map[string]string{}
	}
	document := approvalDocument{
		Schema: 2, Slug: prepared.Intent.Slug, ApprovalSHA256: prepared.ApprovalSHA256,
		IntentSHA256: prepared.IntentSHA256, TargetBranch: request.Project.Base,
		BaseSHA: string(prepared.BaseCommit), Domains: domains, AcceptancePaths: acceptancePaths,
		ProtectedManifest: manifest, CheckBaseline: baseline, Witness: cloneWitness(request.Witness),
		By: prepared.Approver, At: request.At,
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(encoded.Bytes(), []byte{'\n'}), nil
}

func cloneWitness(witness *Witness) *Witness {
	if witness == nil {
		return nil
	}
	copy := *witness
	return &copy
}

func matchingLedger(root contract.RootedFS, slug, hash string) []byte {
	name := ".kogen/intents/" + slug + "/ledger.json"
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
		return nil
	}
	data, err := root.ReadFile(name)
	if err != nil {
		return nil
	}
	var header struct {
		ApprovalSHA256 string            `json:"approval_sha256"`
		Rows           []json.RawMessage `json:"rows"`
	}
	if json.Unmarshal(data, &header) != nil || header.ApprovalSHA256 != hash || header.Rows == nil {
		return nil
	}
	return data
}

func readAndVerifySources(root contract.RootedFS, paths packageFilePaths, prepared *prepare.Prepared, givenPrefix string) error {
	intentBytes, err := readRegular(root, paths.intent)
	if err != nil {
		return failure("hash_mismatch", fmt.Errorf("%s is now unavailable, not %s; review it again with kogen intent approve %s: %w", prepared.Intent.Slug, givenPrefix, prepared.Intent.Slug, err))
	}
	acceptanceBytes, err := readRegular(root, paths.acceptance)
	if err != nil {
		return failure("hash_mismatch", fmt.Errorf("%s is now unavailable, not %s; review it again with kogen intent approve %s: %w", prepared.Intent.Slug, givenPrefix, prepared.Intent.Slug, err))
	}
	actualHash := intent.ApprovalSHA256(intentBytes, acceptanceBytes)
	if actualHash != prepared.ApprovalSHA256 || !bytes.Equal(intentBytes, prepared.IntentBytes) || !bytes.Equal(acceptanceBytes, prepared.AcceptanceBytes) {
		return failure("hash_mismatch", fmt.Errorf("%s is now %s, not %s; review it again with kogen intent approve %s", prepared.Intent.Slug, actualHash[:8], givenPrefix, prepared.Intent.Slug))
	}
	return nil
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

func closeRoot(root contract.RootedFS) {
	if closer, ok := root.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}

func writeApprovalTree(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, format gitio.ObjectFormat, files []blobFile) (contract.ObjectID, error) {
	objects := make(map[string]contract.ObjectID, len(files))
	for _, file := range files {
		if err := validateTreePath(file.path); err != nil {
			return "", err
		}
		result, err := git.Exec(ctx, []string{"hash-object", "-w", "--no-filters", "--stdin"}, file.bytes, policy)
		if err != nil {
			return "", fmt.Errorf("write exact approval blob %q: %w", file.path, err)
		}
		if err := requireSuccess("hash exact approval blob", result); err != nil {
			return "", err
		}
		object, err := parseObjectID(result.Stdout, format)
		if err != nil {
			return "", fmt.Errorf("approval blob %q: %w", file.path, err)
		}
		objects[file.path] = object
	}

	approvalDir := ".kogen/intents/"
	intentPath := ""
	acceptancePath := ""
	ledgerPath := ""
	approvalPath := ""
	for _, file := range files {
		switch {
		case strings.HasSuffix(file.path, "/intent.md") && strings.HasPrefix(file.path, approvalDir):
			intentPath = file.path
		case strings.HasSuffix(file.path, "/approval.json") && strings.HasPrefix(file.path, approvalDir):
			approvalPath = file.path
		case strings.HasSuffix(file.path, "/ledger.json") && strings.HasPrefix(file.path, approvalDir):
			ledgerPath = file.path
		case strings.HasPrefix(file.path, ".kogen/acceptance/"):
			acceptancePath = file.path
		}
	}
	if intentPath == "" || approvalPath == "" || acceptancePath == "" {
		return "", errors.New("approval tree is missing a required package path")
	}
	intentDir := path.Dir(intentPath)
	approvalEntries := []treeEntry{
		{mode: "100644", typeName: "blob", name: "approval.json", object: objects[approvalPath]},
		{mode: "100644", typeName: "blob", name: "intent.md", object: objects[intentPath]},
	}
	if ledgerPath != "" {
		approvalEntries = append(approvalEntries, treeEntry{mode: "100644", typeName: "blob", name: "ledger.json", object: objects[ledgerPath]})
	}
	approvalTree, err := writeTree(ctx, git, policy, format, approvalEntries)
	if err != nil {
		return "", err
	}
	intentsTree, err := writeTree(ctx, git, policy, format, []treeEntry{{mode: "040000", typeName: "tree", name: path.Base(intentDir), object: approvalTree}})
	if err != nil {
		return "", err
	}
	acceptanceTree, err := writeTree(ctx, git, policy, format, []treeEntry{{mode: "100644", typeName: "blob", name: path.Base(acceptancePath), object: objects[acceptancePath]}})
	if err != nil {
		return "", err
	}
	kogenTree, err := writeTree(ctx, git, policy, format, []treeEntry{
		{mode: "040000", typeName: "tree", name: "acceptance", object: acceptanceTree},
		{mode: "040000", typeName: "tree", name: "intents", object: intentsTree},
	})
	if err != nil {
		return "", err
	}
	return writeTree(ctx, git, policy, format, []treeEntry{{mode: "040000", typeName: "tree", name: ".kogen", object: kogenTree}})
}

type blobFile struct {
	path  string
	bytes []byte
}

func validateTreePath(name string) error {
	if !fs.ValidPath(name) || path.Clean(name) != name || strings.ContainsRune(name, '\\') {
		return fmt.Errorf("unsafe approval tree path %q", name)
	}
	for _, part := range strings.Split(name, "/") {
		if strings.EqualFold(part, ".git") || strings.ContainsRune(part, 0) {
			return fmt.Errorf("unsafe approval tree path %q", name)
		}
	}
	return nil
}

func writeTree(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, format gitio.ObjectFormat, entries []treeEntry) (contract.ObjectID, error) {
	ordered := append([]treeEntry(nil), entries...)
	sort.Slice(ordered, func(i, j int) bool {
		return bytes.Compare(treeSortName(ordered[i]), treeSortName(ordered[j])) < 0
	})
	var input bytes.Buffer
	for _, entry := range ordered {
		if entry.name == "" || strings.ContainsRune(entry.name, '/') || strings.ContainsRune(entry.name, 0) {
			return "", errors.New("invalid approval tree entry name")
		}
		if entry.typeName != "blob" && entry.typeName != "tree" {
			return "", errors.New("invalid approval tree object type")
		}
		if _, err := gitio.ParseObjectID(string(entry.object)); err != nil {
			return "", err
		}
		fmt.Fprintf(&input, "%s %s %s\t", entry.mode, entry.typeName, entry.object)
		input.WriteString(entry.name)
		input.WriteByte(0)
	}
	result, err := git.Exec(ctx, []string{"mktree", "-z"}, input.Bytes(), policy)
	if err != nil {
		return "", fmt.Errorf("write approval tree: %w", err)
	}
	if err := requireSuccess("write approval tree", result); err != nil {
		return "", err
	}
	return parseObjectID(result.Stdout, format)
}

func treeSortName(entry treeEntry) []byte {
	suffix := byte(0)
	if entry.typeName == "tree" {
		suffix = '/'
	}
	return append(append([]byte(nil), entry.name...), suffix)
}

func parseObjectID(output []byte, format gitio.ObjectFormat) (contract.ObjectID, error) {
	id, err := gitio.ParseObjectID(strings.TrimSpace(string(output)))
	if err != nil {
		return "", err
	}
	want := 40
	if format == gitio.ObjectFormatSHA256 {
		want = 64
	}
	if len(id) != want {
		return "", fmt.Errorf("Git object ID length does not match %s format", format)
	}
	return id, nil
}

func requireSuccess(operation string, result contract.GitResult) error {
	if result.Process.ExitStatus == nil {
		return fmt.Errorf("%s: supervised Git process returned no exit status", operation)
	}
	if result.Process.TimedOut || result.Process.Unavailable {
		return fmt.Errorf("%s: Git process timed out or was unavailable", operation)
	}
	if *result.Process.ExitStatus != 0 {
		detail := strings.TrimSpace(string(result.StderrTail))
		if detail == "" {
			detail = "Git exited unsuccessfully"
		}
		return fmt.Errorf("%s: %s", operation, detail)
	}
	return nil
}

func readApprovalRef(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, refs *gitio.RefPort, ref string) (contract.RefObservation, error) {
	result, err := git.Exec(ctx, []string{"symbolic-ref", "-q", "--no-recurse", ref}, nil, policy)
	if err != nil {
		return contract.RefObservation{}, fmt.Errorf("check approval ref type: %w", err)
	}
	if result.Process.ExitStatus == nil || result.Process.TimedOut || result.Process.Unavailable {
		return contract.RefObservation{}, errors.New("check approval ref type: supervised Git process returned no usable exit status")
	}
	switch *result.Process.ExitStatus {
	case 0:
		return contract.RefObservation{}, errors.New("approval ref is symbolic; refusing to redirect publication")
	case 1:
	default:
		detail := strings.TrimSpace(string(result.StderrTail))
		if detail == "" {
			detail = "Git exited unsuccessfully"
		}
		return contract.RefObservation{}, fmt.Errorf("check approval ref type: %s", detail)
	}
	return refs.ReadRef(ctx, ref)
}

func compareAndSwap(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, format gitio.ObjectFormat, ref string, expected, next contract.ObjectID) (bool, error) {
	old := string(expected)
	if old == "" {
		if format == gitio.ObjectFormatSHA256 {
			old = strings.Repeat("0", 64)
		} else {
			old = strings.Repeat("0", 40)
		}
	}
	result, err := git.Exec(ctx, []string{"update-ref", "--no-deref", ref, string(next), old}, nil, policy)
	if err != nil {
		return false, fmt.Errorf("compare-and-swap approval ref: %w", err)
	}
	if result.Process.ExitStatus == nil {
		return false, errors.New("compare-and-swap approval ref: supervised Git process returned no exit status")
	}
	if result.Process.TimedOut || result.Process.Unavailable {
		return false, errors.New("compare-and-swap approval ref: Git process timed out or was unavailable")
	}
	if *result.Process.ExitStatus == 0 {
		return true, nil
	}
	if lostRace(result) {
		return false, nil
	}
	detail := strings.TrimSpace(string(result.StderrTail))
	if detail == "" {
		detail = "Git exited unsuccessfully"
	}
	return false, fmt.Errorf("compare-and-swap approval ref: %s", detail)
}

func lostRace(result contract.GitResult) bool {
	detail := strings.ToLower(string(result.StderrTail) + "\n" + string(result.Stdout))
	return strings.Contains(detail, "cannot lock ref") || strings.Contains(detail, " is at ") || strings.Contains(detail, "reference already exists")
}

func approvalMessage(slug, by, hash, at string) string {
	return fmt.Sprintf("Kogen immutable approval package\n\nKogen-Approval: %s\nKogen-Approved-By: %s\nKogen-Approved-Hash: %s\nKogen-Approved-At: %s\n", slug, by, hash, at)
}
