package remove

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/process"
	"kogen-go/internal/project"
	"kogen-go/internal/testkit"
	"kogen-go/internal/yamlmini"
)

const (
	testSlug         = "greet"
	testIntentPath   = ".kogen/intents/greet/intent.md"
	testAcceptPath   = ".kogen/acceptance/greet.t.sh"
	testRunID        = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testIntentSource = "---\ntitle: Greeting\nsize: small\ndomains: [app]\n---\nUpdate the greeting.\n"
)

func TestRemoveDraftCommitsOnlyIntentPathsAndPreservesOtherStagedChanges(t *testing.T) {
	state := newRemoveState(t)
	if err := os.MkdirAll(filepath.Join(state.fixture.Checkout, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRemoveFile(t, state.fixture.Checkout, "lib/other.txt", []byte("staged elsewhere\n"))
	state.fixture.Run(t, "add", "--", "lib/other.txt")

	result, err := Remove(context.Background(), Request{Project: state.project, Slug: testSlug}, state.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Commit) != 40 {
		t.Fatalf("removal commit = %q, want a full SHA-1 id", result.Commit)
	}
	if got := strings.TrimSpace(string(state.fixture.Run(t, "rev-parse", "HEAD"))); got != string(result.Commit) {
		t.Fatalf("checkout HEAD = %s, want removal commit %s", got, result.Commit)
	}
	if got := strings.TrimSpace(string(state.fixture.Run(t, "log", "-1", "--format=%s"))); got != "Remove Intent greet" {
		t.Fatalf("commit subject = %q", got)
	}
	changed := strings.TrimSpace(string(state.fixture.Run(t, "show", "--name-only", "--format=", "HEAD")))
	wantChanged := testAcceptPath + "\n" + testIntentPath
	if changed != wantChanged {
		t.Fatalf("removal commit paths = %q, want %q", changed, wantChanged)
	}
	if got := strings.TrimSpace(string(state.fixture.Run(t, "diff", "--cached", "--name-only"))); got != "lib/other.txt" {
		t.Fatalf("staged paths after removal = %q, want only the unrelated staged path", got)
	}
	for _, name := range []string{testIntentPath, testAcceptPath, ".kogen/intents/greet"} {
		if _, err := os.Lstat(filepath.Join(state.fixture.Checkout, filepath.FromSlash(name))); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("removed path %s still exists or could not be checked: %v", name, err)
		}
	}
}

func TestRemoveLandedIntentNeedsNoForce(t *testing.T) {
	state := newRemoveState(t)
	state.fixture.Run(t, "push", "--quiet", "origin", "HEAD:refs/heads/main")
	state.fixture.Run(t, "push", "--quiet", "origin", "HEAD:refs/kogen/intents/"+testSlug)

	result, err := Remove(context.Background(), Request{Project: state.project, Slug: testSlug}, state.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Commit) != 40 {
		t.Fatalf("removal commit = %q, want a full SHA-1 id", result.Commit)
	}
	if got := strings.TrimSpace(string(state.fixture.RunIn(t, state.fixture.Origin, "for-each-ref", "--format=%(objectname)", "refs/kogen/intents/"+testSlug))); got != "" {
		t.Fatalf("approval ref after landed removal = %q, want absent", got)
	}
}

func TestRemoveRequiresForceForDanglingApprovalRefAndDeletesItWithForce(t *testing.T) {
	state := newRemoveState(t)
	refPath := filepath.Join(state.fixture.Origin, "refs", "kogen", "intents", testSlug)
	if err := os.MkdirAll(filepath.Dir(refPath), 0o700); err != nil {
		t.Fatal(err)
	}
	dangling := strings.Repeat("f", 40)
	if err := os.WriteFile(refPath, []byte(dangling+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Remove(context.Background(), Request{Project: state.project, Slug: testSlug}, state.dependencies)
	var refusal *Failure
	if !errors.As(err, &refusal) || refusal.Reason != "remove_requires_force" {
		t.Fatalf("Remove error = %v, want remove_requires_force", err)
	}
	result, err := Remove(context.Background(), Request{Project: state.project, Slug: testSlug, Force: true}, state.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if result.Commit == "" {
		t.Fatal("force removal returned an empty commit")
	}
	if got := strings.TrimSpace(string(state.fixture.RunIn(t, state.fixture.Origin, "for-each-ref", "--format=%(refname)", "refs/kogen/intents/"+testSlug))); got != "" {
		t.Fatalf("dangling approval ref after force removal = %q, want absent", got)
	}
}

func TestRemoveRequiresForceForApprovalAndCurrentFailedOrParkedRuns(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   string
		reason   string
		wantText string
	}{
		{name: "approved", wantText: "Intent approved or queued; pass --force"},
		{name: "failed", status: "failed", reason: "repair_cap", wantText: "Intent still has a failed Build approval; pass --force"},
		{name: "parked", status: "parked", reason: "landing_retries", wantText: "Intent still has a parked Build approval; pass --force"},
		{name: "interrupted", status: "failed", reason: "interrupted", wantText: "Intent still has an approval ref; pass --force"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newRemoveState(t)
			approval := state.fixture.Run(t, "rev-parse", "HEAD")
			state.fixture.RunIn(t, state.fixture.Checkout, "push", "--quiet", "origin", "HEAD:refs/kogen/intents/"+testSlug)
			approval = []byte(strings.TrimSpace(string(approval)))
			if test.status != "" {
				writeRunState(t, state.project.StateRoot, approval, test.status, test.reason)
			}
			_, err := Remove(context.Background(), Request{Project: state.project, Slug: testSlug}, state.dependencies)
			var refusal *Failure
			if !errors.As(err, &refusal) || refusal.Reason != "remove_requires_force" || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("Remove error = %v, want remove_requires_force containing %q", err, test.wantText)
			}
			if _, statErr := os.Stat(filepath.Join(state.fixture.Checkout, testIntentPath)); statErr != nil {
				t.Fatalf("force refusal removed Intent source: %v", statErr)
			}
			if strings.TrimSpace(string(state.fixture.RunIn(t, state.fixture.Origin, "rev-parse", "refs/kogen/intents/"+testSlug))) == "" {
				t.Fatal("force refusal deleted approval ref")
			}
		})
	}
}

func TestRemoveForceCASDeletesOnlyTheObservedApprovalRef(t *testing.T) {
	state := newRemoveState(t)
	state.fixture.RunIn(t, state.fixture.Checkout, "push", "--quiet", "origin", "HEAD:refs/kogen/intents/"+testSlug)
	approvalTarget := strings.TrimSpace(string(state.fixture.RunIn(t, state.fixture.Origin, "rev-parse", "refs/kogen/intents/"+testSlug)))
	competitor := strings.TrimSpace(string(state.fixture.RunIn(t, state.fixture.Origin, "rev-parse", "refs/heads/main")))
	tracing := &raceApprovalDeleteGit{GitPort: state.dependencies.Git, ref: "refs/kogen/intents/" + testSlug, replacement: competitor}
	dependencies := state.dependencies
	dependencies.Git = tracing
	_, err := Remove(context.Background(), Request{Project: state.project, Slug: testSlug, Force: true}, dependencies)
	var refusal *Failure
	if !errors.As(err, &refusal) || refusal.Reason != "approval_ref_changed" {
		t.Fatalf("Remove error = %v, want approval_ref_changed", err)
	}
	if tracing.injected != 1 {
		t.Fatalf("injected approval races = %d, want 1", tracing.injected)
	}
	got := strings.TrimSpace(string(state.fixture.RunIn(t, state.fixture.Origin, "rev-parse", "refs/kogen/intents/"+testSlug)))
	if got != competitor || got == approvalTarget {
		t.Fatalf("approval ref after CAS conflict = %s, want concurrent owner %s", got, competitor)
	}
}

func TestRemoveForceRefusesAnActiveBuildEvenWithForce(t *testing.T) {
	state := newRemoveState(t)
	writeRemoveFile(t, state.fixture.Checkout, ".kogen/claim", []byte(testRunID+"\n"))
	state.fixture.Run(t, "add", "--", ".kogen/claim")
	state.fixture.Run(t, "commit", "--quiet", "-m", "claim fixture")
	state.fixture.Run(t, "push", "--quiet", "origin", "HEAD:refs/kogen/claim")
	state.fixture.Run(t, "reset", "--quiet", "--hard", "HEAD^")
	writeRunState(t, state.project.StateRoot, []byte(strings.TrimSpace(string(state.fixture.Run(t, "rev-parse", "HEAD")))), "running", "")

	_, err := Remove(context.Background(), Request{Project: state.project, Slug: testSlug, Force: true}, state.dependencies)
	var refusal *Failure
	if !errors.As(err, &refusal) || refusal.Reason != "remove_blocked" {
		t.Fatalf("Remove error = %v, want remove_blocked", err)
	}
	if _, statErr := os.Stat(filepath.Join(state.fixture.Checkout, testIntentPath)); statErr != nil {
		t.Fatalf("active Build refusal removed Intent source: %v", statErr)
	}
}

func TestRemoveRequiresCommitWhenIntentFilesAreUntracked(t *testing.T) {
	state := newRemoveState(t)
	state.fixture.Run(t, "reset", "--quiet", "--hard", "refs/remotes/origin/main")
	writeRemoveFile(t, state.fixture.Checkout, testIntentPath, []byte(testIntentSource))
	writeRemoveFile(t, state.fixture.Checkout, testAcceptPath, []byte("#!/bin/sh\nexit 0\n"))
	_, err := Remove(context.Background(), Request{Project: state.project, Slug: testSlug}, state.dependencies)
	var refusal *Failure
	if !errors.As(err, &refusal) || refusal.Reason != "remove_requires_commit" {
		t.Fatalf("Remove error = %v, want remove_requires_commit", err)
	}
	if _, statErr := os.Stat(filepath.Join(state.fixture.Checkout, testIntentPath)); statErr != nil {
		t.Fatalf("untracked Intent refusal removed source: %v", statErr)
	}
}

type removeState struct {
	fixture      *testkit.GitFixture
	project      *project.Resolution
	dependencies Dependencies
}

func newRemoveState(t *testing.T) *removeState {
	t.Helper()
	fixture := testkit.NewGitFixture(t)
	writeRemoveFile(t, fixture.Checkout, testIntentPath, []byte(testIntentSource))
	writeRemoveFile(t, fixture.Checkout, testAcceptPath, []byte("#!/bin/sh\nexit 0\n"))
	fixture.Run(t, "add", "--", testIntentPath, testAcceptPath)
	fixture.Run(t, "commit", "--quiet", "-m", "add draft Intent")
	baseEnvironment := process.Environment{}
	for _, item := range fixture.Environment() {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			baseEnvironment[key] = value
		}
	}
	policy := func(directory string) contract.GitPolicy { return gitio.OriginPolicy(directory, baseEnvironment) }
	return &removeState{
		fixture: fixture,
		project: &project.Resolution{
			Checkout:  fixture.Checkout,
			Origin:    fixture.Origin,
			Base:      "refs/heads/main",
			StateRoot: filepath.Join(fixture.Root, "state"),
			Config: &project.Config{Raw: yamlmini.Mapping{
				"acceptance": yamlmini.Mapping{"ext": ".t.sh"},
			}},
		},
		dependencies: Dependencies{Git: gitio.NewOrigin(process.Supervisor{}), Policy: policy},
	}
}

func writeRunState(t *testing.T, stateRoot string, approval []byte, status, reason string) {
	t.Helper()
	approvalID := strings.TrimSpace(string(approval))
	runID := testRunID
	runDirectory := filepath.Join(stateRoot, "runs", runID)
	if err := os.MkdirAll(runDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	run := map[string]any{
		"schema": 2, "run_id": runID, "slug": testSlug,
		"approval_sha256": strings.Repeat("b", 64), "approval_commit": approvalID,
		"target_branch": "main", "status": status, "landing": nil,
		"owner_pid": 123, "owner_started_ms": 1, "started_ms": 100,
	}
	contents, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDirectory, "run.json"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
	event := ""
	if status == "failed" {
		event = fmt.Sprintf("{\"event\":\"finished\",\"reason\":%q}\n", reason)
	}
	if err := os.WriteFile(filepath.Join(runDirectory, "events.jsonl"), []byte(event), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeRemoveFile(t *testing.T, root, name string, contents []byte) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, contents, 0o644); err != nil {
		t.Fatal(err)
	}
}

type raceApprovalDeleteGit struct {
	contract.GitPort
	ref         string
	replacement string
	injected    int
}

func (g *raceApprovalDeleteGit) Exec(ctx context.Context, args []string, stdin []byte, policy contract.GitPolicy) (contract.GitResult, error) {
	if len(args) == 5 && args[0] == "update-ref" && args[1] == "--no-deref" && args[2] == "-d" && args[3] == g.ref && g.injected == 0 {
		g.injected++
		if _, err := g.GitPort.Exec(ctx, []string{"update-ref", g.ref, g.replacement, args[4]}, nil, policy); err != nil {
			return contract.GitResult{}, err
		}
	}
	return g.GitPort.Exec(ctx, args, stdin, policy)
}
