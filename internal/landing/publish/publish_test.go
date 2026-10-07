package publish

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/journal"
	"kogen-go/internal/landing/commit"
	"kogen-go/internal/process"
	"kogen-go/internal/safefs"
	"kogen-go/internal/testkit"
)

type testClock struct {
	now     time.Time
	sleeps  []time.Duration
	onSleep func(time.Duration)
}

func (c *testClock) Now() time.Time { return c.now }

func (c *testClock) Sleep(_ context.Context, delay time.Duration) error {
	c.sleeps = append(c.sleeps, delay)
	c.now = c.now.Add(delay)
	if c.onSleep != nil {
		c.onSleep(delay)
	}
	return nil
}

func TestPublishPersistsIncomingCASAndUpdatesCleanCheckedOutBases(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	base := strings.TrimSpace(string(fixture.Run(t, "rev-parse", "refs/heads/main")))
	candidate := makeCandidate(t, fixture, base, "landed content\n")
	store, snapshot, closeRoot := newRun(t, fixture, base)
	defer closeRoot()
	clock := &testClock{now: time.Unix(1_800_000_000, 0)}
	var durableChecked, incomingChecked bool
	observer := ObserverFunc(func(point Point, candidate commit.Result) error {
		switch point {
		case RecordDurable:
			current, err := store.ReadSnapshot()
			if err != nil {
				return err
			}
			if current.Landing == nil || current.Landing.CandidateCommit != string(candidate.Commit) {
				return errors.New("landing record was not durable")
			}
			if got := strings.TrimSpace(string(fixture.Run(t, "rev-parse", "refs/heads/main"))); got != base {
				return errors.New("base moved before incoming publication")
			}
			durableChecked = true
		case IncomingPublished:
			got := strings.TrimSpace(string(fixture.Run(t, "rev-parse", "refs/kogen/incoming/0123456789abcdef0123456789abcdef")))
			if got != string(candidate.Commit) {
				return errors.New("incoming ref did not target candidate")
			}
			incomingChecked = true
		}
		return nil
	})
	result, err := Publish(context.Background(), requestFor(fixture, store, snapshot, candidate, clock, observer))
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if result.Kind != Landed || result.Commit != candidate.Commit || result.Tree != candidate.Tree {
		t.Fatalf("Publish() = %#v, want landed candidate", result)
	}
	if !durableChecked || !incomingChecked {
		t.Fatalf("publication observers: durable=%t incoming=%t", durableChecked, incomingChecked)
	}
	if got := strings.TrimSpace(string(fixture.Run(t, "rev-parse", "refs/heads/main"))); got != string(candidate.Commit) {
		t.Fatalf("base ref = %s, want %s", got, candidate.Commit)
	}
	if refExists(t, fixture, "refs/kogen/incoming/0123456789abcdef0123456789abcdef") {
		t.Fatal("incoming ref remains after successful cleanup")
	}
	for _, checkout := range []string{fixture.Checkout} {
		if got := strings.TrimSpace(string(fixture.RunIn(t, checkout, "rev-parse", "HEAD"))); got != string(candidate.Commit) {
			t.Errorf("%s HEAD = %s, want %s", checkout, got, candidate.Commit)
		}
		if got := string(fixture.RunIn(t, checkout, "status", "--porcelain", "--untracked-files=no")); got != "" {
			t.Errorf("%s tracked status = %q, want clean", checkout, got)
		}
		if got := string(fixture.RunIn(t, checkout, "show", "HEAD:README.md")); got != "landed content\n" {
			t.Errorf("%s landed bytes = %q", checkout, got)
		}
	}
	current, err := store.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "landed" || current.Landing == nil || current.Landing.FinalTree != string(candidate.Tree) {
		t.Fatalf("durable landing snapshot = %#v", current)
	}
	events, err := store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	if eventIndex(events, "landing_prepared") < 0 || eventIndex(events, "landing_prepared") >= eventIndex(events, "base_cas") || eventIndex(events, "base_cas") >= eventIndex(events, "finished") {
		t.Fatalf("publication event order = %#v", events)
	}
}

func TestPublishRetriesHeldBaseLockAfterDurableEvent(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	base := strings.TrimSpace(string(fixture.Run(t, "rev-parse", "refs/heads/main")))
	candidate := makeCandidate(t, fixture, base, "lock release\n")
	store, snapshot, closeRoot := newRun(t, fixture, base)
	defer closeRoot()
	lockName := strings.TrimSpace(string(fixture.Run(t, "rev-parse", "--git-path", "refs/heads/main.lock")))
	if !filepath.IsAbs(lockName) {
		lockName = filepath.Join(fixture.Checkout, lockName)
	}
	if err := os.WriteFile(lockName, []byte("held\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: time.Unix(1_800_000_000, 0)}
	clock.onSleep = func(delay time.Duration) {
		if delay != time.Second {
			t.Errorf("retry delay = %s, want 1s", delay)
		}
		if err := os.Remove(lockName); err != nil {
			t.Errorf("remove test lock: %v", err)
		}
	}
	result, err := Publish(context.Background(), requestFor(fixture, store, snapshot, candidate, clock, nil))
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if result.Kind != Landed || len(clock.sleeps) != 1 || clock.sleeps[0] != time.Second {
		t.Fatalf("Publish() = %#v, sleeps = %v", result, clock.sleeps)
	}
	events, err := store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 || events[0].Event != "landing_prepared" || events[1].Event != "landing_retry" {
		t.Fatalf("events = %#v, want landing_prepared then landing_retry", events)
	}
	if string(events[1].Fields["delay_ms"]) != "1000" {
		t.Fatalf("retry delay_ms = %s", events[1].Fields["delay_ms"])
	}
}

func TestPublishDoesNotReplaceAnExistingIncomingRef(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	base := strings.TrimSpace(string(fixture.Run(t, "rev-parse", "refs/heads/main")))
	candidate := makeCandidate(t, fixture, base, "must not replace incoming\n")
	store, snapshot, closeRoot := newRun(t, fixture, base)
	defer closeRoot()
	const incoming = "refs/kogen/incoming/0123456789abcdef0123456789abcdef"
	fixture.Run(t, "update-ref", incoming, base)
	_, err := Publish(context.Background(), requestFor(fixture, store, snapshot, candidate, nil, nil))
	if !errors.Is(err, ErrIncomingExists) {
		t.Fatalf("Publish() error = %v, want ErrIncomingExists", err)
	}
	if got := strings.TrimSpace(string(fixture.Run(t, "rev-parse", incoming))); got != base {
		t.Fatalf("incoming ref = %s, want existing target %s", got, base)
	}
	if got := strings.TrimSpace(string(fixture.Run(t, "rev-parse", "refs/heads/main"))); got != base {
		t.Fatalf("base ref = %s, want unchanged %s", got, base)
	}
}

func TestPublishPreservesDirtyCheckoutAndReturnsWarning(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	base := strings.TrimSpace(string(fixture.Run(t, "rev-parse", "refs/heads/main")))
	candidate := makeCandidate(t, fixture, base, "new candidate bytes\n")
	store, snapshot, closeRoot := newRun(t, fixture, base)
	defer closeRoot()
	local := filepath.Join(fixture.Checkout, "local edit.txt")
	if err := os.WriteFile(local, []byte("keep this local file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Publish(context.Background(), requestFor(fixture, store, snapshot, candidate, nil, nil))
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if result.Kind != Landed || len(result.Warnings) != 1 {
		t.Fatalf("Publish() = %#v, want one dirty checkout warning", result)
	}
	if !strings.Contains(result.Warnings[0], "has local changes and was not updated") || !strings.Contains(result.Warnings[0], fixture.Checkout) {
		t.Fatalf("warning = %q", result.Warnings[0])
	}
	if got, err := os.ReadFile(local); err != nil || string(got) != "keep this local file\n" {
		t.Fatalf("local edit = %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(fixture.Checkout, "README.md")); err != nil || string(got) != "fixture seed\n" {
		t.Fatalf("dirty checkout content = %q, %v; want original bytes preserved", got, err)
	}
	events, err := store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	if eventIndex(events, "landing_warning") < 0 {
		t.Fatalf("events = %#v, missing landing_warning", events)
	}
}

func TestPublishMakesIncomingCleanupFailureNonfatalAfterBaseCAS(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	base := strings.TrimSpace(string(fixture.Run(t, "rev-parse", "refs/heads/main")))
	candidate := makeCandidate(t, fixture, base, "landed despite cleanup\n")
	store, snapshot, closeRoot := newRun(t, fixture, base)
	defer closeRoot()
	observer := ObserverFunc(func(point Point, _ commit.Result) error {
		if point == BaseCASSucceeded {
			fixture.Run(t, "update-ref", "-d", "refs/kogen/incoming/0123456789abcdef0123456789abcdef")
		}
		return nil
	})
	result, err := Publish(context.Background(), requestFor(fixture, store, snapshot, candidate, nil, observer))
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if result.Kind != Landed || len(result.CleanupFailures) != 1 {
		t.Fatalf("Publish() = %#v, want landed with one cleanup failure", result)
	}
	if got := strings.TrimSpace(string(fixture.Run(t, "rev-parse", "refs/heads/main"))); got != string(candidate.Commit) {
		t.Fatalf("base ref = %s, want landed candidate %s", got, candidate.Commit)
	}
	events, err := store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	if eventIndex(events, "cleanup_failure") < 0 || eventIndex(events, "finished") < 0 {
		t.Fatalf("events = %#v, want cleanup_failure and landed finished event", events)
	}
	current, err := store.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "landed" {
		t.Fatalf("run status = %q, want landed", current.Status)
	}
}

func TestPublishCrashBoundaryAfterBaseCASLeavesIncomingForRecovery(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	base := strings.TrimSpace(string(fixture.Run(t, "rev-parse", "refs/heads/main")))
	candidate := makeCandidate(t, fixture, base, "recover this landing\n")
	store, snapshot, closeRoot := newRun(t, fixture, base)
	defer closeRoot()
	crash := errors.New("injected crash after base CAS")
	observer := ObserverFunc(func(point Point, _ commit.Result) error {
		if point == BaseCASSucceeded {
			return crash
		}
		return nil
	})
	if _, err := Publish(context.Background(), requestFor(fixture, store, snapshot, candidate, nil, observer)); !errors.Is(err, crash) {
		t.Fatalf("Publish() error = %v, want injected crash", err)
	}
	if got := strings.TrimSpace(string(fixture.Run(t, "rev-parse", "refs/heads/main"))); got != string(candidate.Commit) {
		t.Fatalf("base ref = %s, want %s", got, candidate.Commit)
	}
	if got := strings.TrimSpace(string(fixture.Run(t, "rev-parse", "refs/kogen/incoming/0123456789abcdef0123456789abcdef"))); got != string(candidate.Commit) {
		t.Fatalf("incoming ref = %s, want retained candidate %s", got, candidate.Commit)
	}
	current, err := store.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "running" || current.Landing == nil || current.Landing.CandidateCommit != string(candidate.Commit) {
		t.Fatalf("recovery record = %#v", current)
	}
}

func TestParseWorktreesSupportsDetachedFlagsAndPathsWithSpaces(t *testing.T) {
	rows, err := parseWorktrees([]byte("worktree /tmp/checked out\x00HEAD abc\x00branch refs/heads/main\x00\x00worktree /tmp/detached\x00HEAD def\x00detached\x00\x00"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0] != (worktreeRow{path: "/tmp/checked out", branch: "refs/heads/main"}) || rows[1] != (worktreeRow{path: "/tmp/detached"}) {
		t.Fatalf("worktrees = %#v", rows)
	}
}

func requestFor(fixture *testkit.GitFixture, store *journal.RunStore, snapshot *journal.RunSnapshot, candidate commit.Result, clock contract.Clock, observer Observer) Request {
	environment := make(process.Environment)
	for _, entry := range fixture.Environment() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			environment[key] = value
		}
	}
	return Request{
		Environment: environment,
		Repository:  fixture.Checkout,
		Store:       store,
		Snapshot:    snapshot,
		Candidate:   candidate,
		Clock:       clock,
		Observer:    observer,
	}
}

func newRun(t *testing.T, fixture *testkit.GitFixture, approvalCommit string) (*journal.RunStore, *journal.RunSnapshot, func()) {
	t.Helper()
	root, err := safefs.OpenRoot(fixture.Root)
	if err != nil {
		t.Fatalf("open journal root: %v", err)
	}
	snapshot := &journal.RunSnapshot{
		Schema:         2,
		RunID:          "0123456789abcdef0123456789abcdef",
		Slug:           "publish-fixture",
		ApprovalSHA256: strings.Repeat("a", 64),
		ApprovalCommit: approvalCommit,
		TargetBranch:   "main",
		Status:         "running",
		OwnerPID:       int64(os.Getpid()),
		OwnerStartedMS: 1,
		StartedMS:      1,
	}
	store, err := journal.NewRunStore(root, "state/runs/"+snapshot.RunID)
	if err != nil {
		root.Close()
		t.Fatalf("create run store: %v", err)
	}
	if err := store.Create(*snapshot); err != nil {
		root.Close()
		t.Fatalf("initialize run record: %v", err)
	}
	return store, snapshot, func() { _ = root.Close() }
}

func makeCandidate(t *testing.T, fixture *testkit.GitFixture, parent, bytes string) commit.Result {
	t.Helper()
	worktree := filepath.Join(fixture.Root, "candidate worktree")
	fixture.Run(t, "worktree", "add", "--detach", worktree, parent)
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte(bytes), 0o644); err != nil {
		t.Fatalf("write candidate: %v", err)
	}
	fixture.RunIn(t, worktree, "add", "README.md")
	tree := strings.TrimSpace(string(fixture.RunIn(t, worktree, "write-tree")))
	message := "Publish fixture\n\nKogen-Intent: publish-fixture\n"
	commitID := strings.TrimSpace(string(fixture.RunIn(t, worktree, "commit-tree", tree, "-p", parent, "-m", message)))
	fixture.Run(t, "worktree", "remove", "--force", worktree)
	return commit.Result{
		Commit:       contract.ObjectID(commitID),
		Parent:       contract.ObjectID(parent),
		VerifiedTree: contract.ObjectID(tree),
		Tree:         contract.ObjectID(tree),
		Message:      []byte(message),
	}
}

func eventIndex(events []journal.RunEvent, event string) int {
	for index := range events {
		if events[index].Event == event {
			return index
		}
	}
	return -1
}

func refExists(t *testing.T, fixture *testkit.GitFixture, ref string) bool {
	t.Helper()
	command := exec.Command("git", "show-ref", "--quiet", "--verify", ref)
	command.Dir = fixture.Checkout
	command.Env = fixture.Environment()
	err := command.Run()
	if err == nil {
		return true
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false
	}
	t.Fatalf("check ref %s: %v", ref, err)
	return false
}
