package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"kogen-go/internal/approval/prepare"
	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/intent"
	"kogen-go/internal/process"
	"kogen-go/internal/project"
	"kogen-go/internal/protection"
	"kogen-go/internal/testkit"
)

const (
	testIntentPath     = ".kogen/intents/greeting/intent.md"
	testAcceptancePath = ".kogen/acceptance/greeting.t.sh"
	testApprovalAt     = "2026-10-07T12:00:00Z"
)

var (
	testIntentBytes = []byte("---\ntitle: Greeting\nsize: small\ndomains: [app]\n---\nUpdate the greeting.\n\n## Acceptance\n- A1: update the greeting\n\n## Verify\n- A1: test\n")
	testAcceptBytes = []byte("#!/bin/sh\nprintf 'hello\\n'\n")
)

func TestPublishWritesExactImmutablePackageAndReapprovalParent(t *testing.T) {
	state := newPublishState(t)
	first, err := Publish(context.Background(), state.request, state.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if first.Parent != "" || first.Retried {
		t.Fatalf("first publish result = %#v, want root approval commit without retry", first)
	}
	assertPublishedPackage(t, state, first.Commit, "")

	second, err := Publish(context.Background(), state.request, state.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if second.Parent != first.Commit || second.Retried {
		t.Fatalf("second publish result = %#v, want parent %s", second, first.Commit)
	}
	assertPublishedPackage(t, state, second.Commit, first.Commit)
	if got := strings.TrimSpace(string(state.fixture.RunIn(t, state.fixture.Origin, "rev-parse", "refs/kogen/intents/greeting"))); got != string(second.Commit) {
		t.Fatalf("approval ref = %s, want %s", got, second.Commit)
	}
}

func TestPublishOmitsLedgerBoundToAnotherApprovalHash(t *testing.T) {
	state := newPublishState(t)
	writeFixtureFile(t, state.fixture.Checkout, ".kogen/intents/greeting/ledger.json", []byte(fmt.Sprintf("{\"approval_sha256\":%q,\"rows\":[]}\n", strings.Repeat("0", 64))))
	result, err := Publish(context.Background(), state.request, state.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	paths := strings.Fields(string(state.fixture.RunIn(t, state.fixture.Origin, "ls-tree", "-r", "--name-only", string(result.Commit))))
	sort.Strings(paths)
	want := []string{testAcceptancePath, ".kogen/intents/greeting/approval.json", testIntentPath}
	sort.Strings(want)
	if fmt.Sprint(paths) != fmt.Sprint(want) {
		t.Fatalf("approval tree paths = %v, want stale ledger omitted: %v", paths, want)
	}
}

func TestPublishRetriesLostRaceWithLatestRefAsParent(t *testing.T) {
	state := newPublishState(t)
	first, err := Publish(context.Background(), state.request, state.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	competitor := createCompetitorCommit(t, state, first.Commit, "concurrent approval\n")
	tracing := &racingGit{GitPort: state.dependencies.Git, policy: state.policy, targets: []contract.ObjectID{competitor}}
	dependencies := state.dependencies
	dependencies.Git = tracing
	result, err := Publish(context.Background(), state.request, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Retried || result.Parent != competitor || tracing.attempts != 2 || tracing.updates != 1 {
		t.Fatalf("retry result=%#v attempts=%d races=%d, want retry parent %s and two CAS attempts", result, tracing.attempts, tracing.updates, competitor)
	}
	assertPublishedPackage(t, state, result.Commit, competitor)
}

func TestPublishRefusesSecondLostRaceWithoutReplacingConcurrentRef(t *testing.T) {
	state := newPublishState(t)
	first, err := Publish(context.Background(), state.request, state.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	competitor1 := createCompetitorCommit(t, state, first.Commit, "concurrent approval one\n")
	competitor2 := createCompetitorCommit(t, state, competitor1, "concurrent approval two\n")
	tracing := &racingGit{GitPort: state.dependencies.Git, policy: state.policy, targets: []contract.ObjectID{competitor1, competitor2}}
	dependencies := state.dependencies
	dependencies.Git = tracing
	_, err = Publish(context.Background(), state.request, dependencies)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Reason != "approval_ref_conflict" {
		t.Fatalf("Publish error = %v, want approval_ref_conflict", err)
	}
	if tracing.attempts != 2 || tracing.updates != 2 {
		t.Fatalf("CAS attempts=%d injected races=%d, want exactly two of each", tracing.attempts, tracing.updates)
	}
	got := strings.TrimSpace(string(state.fixture.RunIn(t, state.fixture.Origin, "rev-parse", "refs/kogen/intents/greeting")))
	if got != string(competitor2) {
		t.Fatalf("ref after refusal = %s, want concurrent value %s", got, competitor2)
	}
}

func TestPublishLateReadRefusesChangedSourcesWithoutTouchingRef(t *testing.T) {
	state := newPublishState(t)
	mutating := &mutatingGit{GitPort: state.dependencies.Git, checkout: state.fixture.Checkout, path: testAcceptancePath}
	dependencies := state.dependencies
	dependencies.Git = mutating
	_, err := Publish(context.Background(), state.request, dependencies)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Reason != "hash_mismatch" {
		t.Fatalf("Publish error = %v, want late hash_mismatch", err)
	}
	changedBytes := []byte("changed after commit creation\n")
	changedHash := intent.ApprovalSHA256(testIntentBytes, changedBytes)
	wantError := fmt.Sprintf("intent/hash_mismatch: greeting is now %s, not %s; review it again with kogen intent approve greeting", changedHash[:8], state.request.GivenHashPrefix)
	if err.Error() != wantError {
		t.Fatalf("late source mismatch = %q, want %q", err, wantError)
	}
	if got := strings.TrimSpace(string(state.fixture.RunIn(t, state.fixture.Origin, "for-each-ref", "--format=%(objectname)", "refs/kogen/intents/greeting"))); got != "" {
		t.Fatalf("approval ref changed after late-read refusal: %s", got)
	}
	changed, readErr := os.ReadFile(filepath.Join(state.fixture.Checkout, filepath.FromSlash(testAcceptancePath)))
	if readErr != nil || bytes.Equal(changed, testAcceptBytes) {
		t.Fatalf("race fixture did not change source: bytes=%q err=%v", changed, readErr)
	}
}

func TestPublishRefusesSymbolicApprovalRef(t *testing.T) {
	state := newPublishState(t)
	state.fixture.RunIn(t, state.fixture.Origin, "symbolic-ref", "refs/kogen/intents/greeting", "refs/heads/main")
	_, err := Publish(context.Background(), state.request, state.dependencies)
	var failure *Failure
	if !errors.As(err, &failure) || !strings.Contains(failure.Error(), "approval ref is symbolic") {
		t.Fatalf("Publish error = %v, want symbolic ref refusal", err)
	}
	if got := strings.TrimSpace(string(state.fixture.RunIn(t, state.fixture.Origin, "symbolic-ref", "refs/kogen/intents/greeting"))); got != "refs/heads/main" {
		t.Fatalf("symbolic approval ref changed after refusal: %s", got)
	}
}

type publishState struct {
	fixture      *testkit.GitFixture
	request      Request
	dependencies Dependencies
	policy       contract.GitPolicy
}

func newPublishState(t *testing.T) *publishState {
	t.Helper()
	fixture := testkit.NewGitFixture(t)
	writeFixtureFile(t, fixture.Checkout, testIntentPath, testIntentBytes)
	writeFixtureFile(t, fixture.Checkout, testAcceptancePath, testAcceptBytes)
	parsed, err := intent.Parse("greeting", testIntentBytes)
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSpace(string(fixture.RunIn(t, fixture.Origin, "rev-parse", "refs/heads/main")))
	baseID, err := gitio.ParseObjectID(base)
	if err != nil {
		t.Fatal(err)
	}
	hash := intent.ApprovalSHA256(testIntentBytes, testAcceptBytes)
	ledger := fmt.Sprintf("{\"approval_sha256\":%q,\"rows\":[]}\n", hash)
	writeFixtureFile(t, fixture.Checkout, ".kogen/intents/greeting/ledger.json", []byte(ledger))
	prepared := &prepare.Prepared{
		Intent: parsed, IntentBytes: bytes.Clone(testIntentBytes), AcceptanceBytes: bytes.Clone(testAcceptBytes),
		IntentSHA256: intent.IntentSHA256(testIntentBytes), ApprovalSHA256: hash,
		Approver: "Kogen Test <test@example.invalid>", BaseCommit: baseID,
		AcceptanceSourcePath: testAcceptancePath,
		ProtectedManifest:    protection.Manifest{"Makefile": {SHA256: strings.Repeat("a", 64), Present: true}},
		CheckBaseline:        []prepare.BaselineRow{{Name: "unit", Status: contract.CheckGreen, ExitStatus: intPointer(0), Findings: []prepare.BaselineFinding{}}},
	}
	baseEnvironment := process.Environment{}
	for _, item := range fixture.Environment() {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			baseEnvironment[key] = value
		}
	}
	git := gitio.NewOrigin(process.Supervisor{})
	policy := gitio.OriginPolicy(fixture.Origin, baseEnvironment)
	return &publishState{
		fixture: fixture,
		request: Request{
			Project:  &project.Resolution{Checkout: fixture.Checkout, Origin: fixture.Origin, Base: "main"},
			Prepared: prepared, GivenHashPrefix: hash[:8], At: testApprovalAt,
		},
		dependencies: Dependencies{Git: git, Policy: func(directory string) contract.GitPolicy { return gitio.OriginPolicy(directory, baseEnvironment) }},
		policy:       policy,
	}
}

func assertPublishedPackage(t *testing.T, state *publishState, commit, parent contract.ObjectID) {
	t.Helper()
	pathsText := string(state.fixture.RunIn(t, state.fixture.Origin, "ls-tree", "-r", "--name-only", string(commit)))
	paths := strings.Fields(pathsText)
	wantPaths := []string{testAcceptancePath, ".kogen/intents/greeting/approval.json", testIntentPath, ".kogen/intents/greeting/ledger.json"}
	sort.Strings(paths)
	sort.Strings(wantPaths)
	if fmt.Sprint(paths) != fmt.Sprint(wantPaths) {
		t.Fatalf("approval tree paths = %v, want %v", paths, wantPaths)
	}
	if got := state.fixture.RunIn(t, state.fixture.Origin, "show", string(commit)+":"+testIntentPath); !bytes.Equal(got, testIntentBytes) {
		t.Fatalf("Intent blob = %q, want exact source bytes %q", got, testIntentBytes)
	}
	if got := state.fixture.RunIn(t, state.fixture.Origin, "show", string(commit)+":"+testAcceptancePath); !bytes.Equal(got, testAcceptBytes) {
		t.Fatalf("acceptance blob = %q, want exact source bytes %q", got, testAcceptBytes)
	}
	approval := state.fixture.RunIn(t, state.fixture.Origin, "show", string(commit)+":.kogen/intents/greeting/approval.json")
	var document map[string]json.RawMessage
	if err := json.Unmarshal(approval, &document); err != nil {
		t.Fatalf("decode approval.json: %v", err)
	}
	if bytes.Contains(approval, []byte(`\u003c`)) || bytes.Contains(approval, []byte(`\u003e`)) {
		t.Fatalf("approval.json escaped the raw approver identity: %s", approval)
	}
	keys := make([]string, 0, len(document))
	for key := range document {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	wantKeys := []string{"acceptance_paths", "approval_sha256", "at", "base_sha", "by", "check_baseline", "domains", "intent_sha256", "protected_manifest", "schema", "slug", "target_branch", "witness"}
	sort.Strings(wantKeys)
	if fmt.Sprint(keys) != fmt.Sprint(wantKeys) {
		t.Fatalf("approval.json fields = %v, want schema-2 fields %v", keys, wantKeys)
	}
	if string(document["schema"]) != "2" || string(document["approval_sha256"]) != fmt.Sprintf("%q", state.request.Prepared.ApprovalSHA256) {
		t.Fatalf("approval.json schema/hash = %s/%s", document["schema"], document["approval_sha256"])
	}
	if got := state.fixture.RunIn(t, state.fixture.Origin, "show", string(commit)+":.kogen/intents/greeting/ledger.json"); !bytes.Equal(got, []byte(fmt.Sprintf("{\"approval_sha256\":%q,\"rows\":[]}\n", state.request.Prepared.ApprovalSHA256))) {
		t.Fatalf("ledger blob changed: %q", got)
	}
	commitBytes := state.fixture.RunIn(t, state.fixture.Origin, "cat-file", "commit", string(commit))
	parts := bytes.SplitN(commitBytes, []byte("\n\n"), 2)
	if len(parts) != 2 {
		t.Fatalf("malformed commit object: %q", commitBytes)
	}
	headers := string(parts[0])
	parentLines := 0
	for _, line := range strings.Split(headers, "\n") {
		if strings.HasPrefix(line, "parent ") {
			parentLines++
		}
	}
	if parent == "" {
		if parentLines != 0 {
			t.Fatalf("root approval commit has %d parents: %s", parentLines, headers)
		}
	} else if parentLines != 1 || !strings.Contains(headers, "\nparent "+string(parent)+"\n") {
		t.Fatalf("approval commit parent chain = %s, want sole parent %s", headers, parent)
	}
	wantMessage := approvalMessage("greeting", state.request.Prepared.Approver, state.request.Prepared.ApprovalSHA256, testApprovalAt)
	if string(parts[1]) != wantMessage {
		t.Fatalf("commit message = %q, want exact trailers %q", parts[1], wantMessage)
	}
}

func createCompetitorCommit(t *testing.T, state *publishState, parent contract.ObjectID, message string) contract.ObjectID {
	t.Helper()
	refs := gitio.NewRefPort(state.dependencies.Git, state.policy)
	tree, err := refs.ResolveTree(context.Background(), string(parent))
	if err != nil {
		t.Fatal(err)
	}
	commit, err := refs.CommitTree(context.Background(), contract.CommitTreeRequest{
		Tree: tree, Parents: []contract.ObjectID{parent}, Message: []byte(message),
	})
	if err != nil {
		t.Fatal(err)
	}
	return commit
}

type racingGit struct {
	contract.GitPort
	policy   contract.GitPolicy
	targets  []contract.ObjectID
	updates  int
	attempts int
}

func (git *racingGit) Exec(ctx context.Context, args []string, stdin []byte, policy contract.GitPolicy) (contract.GitResult, error) {
	if len(args) == 5 && args[0] == "update-ref" && args[1] == "--no-deref" && strings.HasPrefix(args[2], "refs/kogen/intents/") {
		git.attempts++
	}
	if len(args) == 5 && args[0] == "update-ref" && args[1] == "--no-deref" && strings.HasPrefix(args[2], "refs/kogen/intents/") && git.updates < len(git.targets) {
		git.updates++
		target := git.targets[git.updates-1]
		if _, err := git.GitPort.Exec(ctx, []string{"update-ref", args[2], string(target)}, nil, git.policy); err != nil {
			return contract.GitResult{}, err
		}
	}
	return git.GitPort.Exec(ctx, args, stdin, policy)
}

type mutatingGit struct {
	contract.GitPort
	checkout string
	path     string
	mutated  bool
}

func (git *mutatingGit) Exec(ctx context.Context, args []string, stdin []byte, policy contract.GitPolicy) (contract.GitResult, error) {
	if !git.mutated && len(args) > 0 && args[0] == "commit-tree" {
		result, err := git.GitPort.Exec(ctx, args, stdin, policy)
		if err != nil {
			return result, err
		}
		git.mutated = true
		name := filepath.Join(git.checkout, filepath.FromSlash(git.path))
		if err := os.WriteFile(name, []byte("changed after commit creation\n"), 0o644); err != nil {
			return contract.GitResult{}, err
		}
		return result, nil
	}
	return git.GitPort.Exec(ctx, args, stdin, policy)
}

func writeFixtureFile(t *testing.T, root, name string, data []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func intPointer(value int) *int { return &value }
