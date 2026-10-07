//go:build darwin || linux

package drain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"kogen-go/internal/build/single"
	"kogen-go/internal/contract"
	"kogen-go/internal/journal"
	"kogen-go/internal/project"
	"kogen-go/internal/queue/lock"
	"kogen-go/internal/queue/schedule"
	"kogen-go/internal/safefs"
)

type recoveryFunc func(context.Context, *project.Resolution) error

func (f recoveryFunc) Recover(ctx context.Context, resolved *project.Resolution) error {
	return f(ctx, resolved)
}

type snapshotFunc func(context.Context, *project.Resolution) ([]schedule.QueueApproval, error)

func (f snapshotFunc) Load(ctx context.Context, resolved *project.Resolution) ([]schedule.QueueApproval, error) {
	return f(ctx, resolved)
}

type buildFunc func(context.Context, *project.Resolution, string) (single.Outcome, error)

func (f buildFunc) Run(ctx context.Context, resolved *project.Resolution, slug string) (single.Outcome, error) {
	return f(ctx, resolved, slug)
}

func TestStartStreamsSubsequentBuildsAndCountsFailures(t *testing.T) {
	resolved := resolvedProject(t)
	var output bytes.Buffer
	var snapshotCalls, buildCalls int
	controller := controllerForTest(t, Dependencies{
		Recovery: recoveryFunc(func(context.Context, *project.Resolution) error { return nil }),
		Snapshot: snapshotFunc(func(context.Context, *project.Resolution) ([]schedule.QueueApproval, error) {
			snapshotCalls++
			switch snapshotCalls {
			case 1:
				return []schedule.QueueApproval{approval("alpha", "a", 1, 0), approval("bravo", "b", 2, 0)}, nil
			case 2:
				return []schedule.QueueApproval{approval("bravo", "b", 2, 0)}, nil
			default:
				return nil, nil
			}
		}),
		Build: buildFunc(func(_ context.Context, _ *project.Resolution, slug string) (single.Outcome, error) {
			buildCalls++
			if slug == "alpha" {
				return single.Outcome{Status: "failed", RunID: strings.Repeat("a", 32), Reason: "checks_red", Verdict: "red"}, nil
			}
			return single.Outcome{Status: "landed", RunID: strings.Repeat("b", 32), Commit: contract.ObjectID("1234567890abcdef")}, nil
		}),
		Output: &output,
	})

	result, err := controller.Start(context.Background(), resolved, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 1 || result.Built != 2 || result.Landed != 1 || result.Final != "queue: done; 2 Build(s), 1 landed, 1 not\n" {
		t.Fatalf("drain result = %#v", result)
	}
	if buildCalls != 2 || snapshotCalls != 3 {
		t.Fatalf("Build calls=%d snapshot calls=%d, want 2 and 3", buildCalls, snapshotCalls)
	}
	want := "building alpha\n" +
		"failed alpha: checks_red; best candidate red at refs/kogen/parked/" + strings.Repeat("a", 32) + " (Build aaaaaaaa)\n" +
		"building bravo\n" +
		"landed bravo 12345678 (Build bbbbbbbb)\n" +
		"queue: done; 2 Build(s), 1 landed, 1 not\n"
	if output.String() != want {
		t.Fatalf("streamed output:\n%s\nwant:\n%s", output.String(), want)
	}
	if _, err := os.Lstat(filepath.Join(resolved.StateRoot, "queue.pid")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("queue.pid remains after drain: %v", err)
	}
}

func TestStoppedBuildRemainsQueuedAndStopsDrain(t *testing.T) {
	resolved := resolvedProject(t)
	var output bytes.Buffer
	calls := 0
	controller := controllerForTest(t, Dependencies{
		Recovery: recoveryFunc(func(context.Context, *project.Resolution) error { return nil }),
		Snapshot: snapshotFunc(func(context.Context, *project.Resolution) ([]schedule.QueueApproval, error) {
			calls++
			if calls == 1 {
				return []schedule.QueueApproval{approval("alpha", "a", 1, 0), approval("bravo", "b", 2, 0)}, nil
			}
			return []schedule.QueueApproval{approval("bravo", "b", 2, 0)}, nil
		}),
		Build: buildFunc(func(context.Context, *project.Resolution, string) (single.Outcome, error) {
			return single.Outcome{
				Status: "stopped", RunID: strings.Repeat("a", 32), Reason: "login_required",
				Failure: &contract.Failure{Class: "provider", Reason: "login_required"},
			}, nil
		}),
		Output: &output,
	})

	result, err := controller.Start(context.Background(), resolved, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 4 || result.Built != 1 || result.Landed != 0 || result.Final != "queue: stopped because alpha hit a provider error; 1 Build(s), 0 landed, 1 not\n" {
		t.Fatalf("stopped drain result = %#v", result)
	}
	if got, want := result.Queue, []string{"alpha", "bravo"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("remaining queue = %v, want stopped approval retained: %v", got, want)
	}
	if !strings.Contains(output.String(), "stopped alpha: provider/login_required; it stays queued (Build aaaaaaaa)\n") {
		t.Fatalf("missing stopped outcome line: %s", output.String())
	}
}

func TestStopRequestsDrainAfterCurrentBuild(t *testing.T) {
	resolved := resolvedProject(t)
	var output bytes.Buffer
	snapshotCalls := 0
	controller := controllerForTest(t, Dependencies{
		Recovery: recoveryFunc(func(context.Context, *project.Resolution) error { return nil }),
		Snapshot: snapshotFunc(func(context.Context, *project.Resolution) ([]schedule.QueueApproval, error) {
			snapshotCalls++
			if snapshotCalls == 1 {
				return []schedule.QueueApproval{approval("alpha", "a", 1, 0), approval("bravo", "b", 2, 0)}, nil
			}
			return []schedule.QueueApproval{approval("bravo", "b", 2, 0)}, nil
		}),
		Build: buildFunc(func(_ context.Context, resolved *project.Resolution, _ string) (single.Outcome, error) {
			stop, err := lock.RequestStop(resolved.StateRoot)
			if err != nil || !stop.Requested {
				return single.Outcome{}, errors.Join(errors.New("queue stop was not published"), err)
			}
			return single.Outcome{Status: "landed", RunID: strings.Repeat("a", 32), Commit: "abcdef0123"}, nil
		}),
		Output: &output,
	})

	result, err := controller.Start(context.Background(), resolved, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.Built != 1 || result.Landed != 1 || result.Final != "queue: stopped on request; 1 Build(s), 1 landed, 0 not\n" {
		t.Fatalf("stop-after-current result = %#v", result)
	}
	if got, want := result.Queue, []string{"bravo"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("queue after stop = %v, want %v", got, want)
	}
	if strings.Contains(output.String(), "building bravo") {
		t.Fatalf("drain started another Build after stop: %s", output.String())
	}
}

func TestStopBeforeFirstBuildKeepsSelectedApprovalQueued(t *testing.T) {
	resolved := resolvedProject(t)
	var output bytes.Buffer
	buildCalls := 0
	controller := controllerForTest(t, Dependencies{
		Recovery: recoveryFunc(func(context.Context, *project.Resolution) error { return nil }),
		Snapshot: snapshotFunc(func(_ context.Context, resolved *project.Resolution) ([]schedule.QueueApproval, error) {
			approvals := []schedule.QueueApproval{approval("alpha", "a", 1, 0), approval("bravo", "b", 2, 0)}
			requested, err := lock.RequestStop(resolved.StateRoot)
			if err != nil || !requested.Requested {
				return nil, fmt.Errorf("queue stop request = %#v, %w", requested, err)
			}
			return approvals, nil
		}),
		Build: buildFunc(func(context.Context, *project.Resolution, string) (single.Outcome, error) {
			buildCalls++
			return single.Outcome{}, nil
		}),
		Output: &output,
	})

	result, err := controller.Start(context.Background(), resolved, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.Built != 0 || buildCalls != 0 || result.Final != "queue: stopped on request; 0 Build(s), 0 landed, 0 not\n" {
		t.Fatalf("pre-Build stop result=%#v Build calls=%d output=%q", result, buildCalls, output.String())
	}
	if got, want := result.Queue, []string{"alpha", "bravo"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pre-Build stop queue = %v, want %v", got, want)
	}
}

func TestSignalCancelsCurrentBuildAndReturnsSignalExit(t *testing.T) {
	resolved := resolvedProject(t)
	var output bytes.Buffer
	signals := make(chan os.Signal, 1)
	started := make(chan struct{})
	controller := controllerForTest(t, Dependencies{
		Recovery: recoveryFunc(func(context.Context, *project.Resolution) error { return nil }),
		Snapshot: snapshotFunc(func(context.Context, *project.Resolution) ([]schedule.QueueApproval, error) {
			return []schedule.QueueApproval{approval("alpha", "a", 1, 0)}, nil
		}),
		Build: buildFunc(func(ctx context.Context, resolved *project.Resolution, _ string) (single.Outcome, error) {
			close(started)
			const runID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			root, err := safefs.OpenRoot(resolved.StateRoot)
			if err != nil {
				return single.Outcome{}, err
			}
			store, err := journal.NewRunStore(root, "runs/"+runID)
			if err != nil {
				root.Close()
				return single.Outcome{}, err
			}
			if err := store.Create(journal.RunSnapshot{
				Schema: 2, RunID: runID, Slug: "alpha", ApprovalSHA256: strings.Repeat("a", 64),
				ApprovalCommit: strings.Repeat("b", 40), TargetBranch: "main", Status: "running",
				OwnerPID: int64(os.Getpid()), OwnerStartedMS: 1, StartedMS: 1, Recovery: []journal.RecoveryRecord{},
			}); err != nil {
				root.Close()
				return single.Outcome{}, err
			}
			<-ctx.Done()
			root.Close()
			return single.Outcome{Status: "stopped", RunID: runID, Reason: "interrupted"}, ctx.Err()
		}),
		Output:  &output,
		Signals: signals,
	})

	done := make(chan struct{})
	var result Result
	var resultErr error
	go func() {
		defer close(done)
		result, resultErr = controller.Start(context.Background(), resolved, StartOptions{})
	}()
	<-started
	signals <- syscall.SIGTERM
	<-done
	if resultErr != nil {
		t.Fatal(resultErr)
	}
	if result.ExitCode != 143 || result.Built != 0 || result.Final != "" || result.InterruptionError != nil {
		t.Fatalf("signal result = %#v", result)
	}
	if strings.Contains(output.String(), "queue: ") || !strings.Contains(output.String(), "building alpha\n") {
		t.Fatalf("signal should interrupt streaming without a final drain line: %q", output.String())
	}
	if _, err := os.Lstat(filepath.Join(resolved.StateRoot, "queue.pid")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("queue.pid remains after signal: %v", err)
	}
	root, err := safefs.OpenRoot(resolved.StateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	store, err := journal.NewRunStore(root, "runs/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != "running" || len(events) != 1 || events[0].Event != "interrupted" || string(events[0].Fields["reason"]) != `"sigterm"` {
		t.Fatalf("signal journal status=%q events=%#v", snapshot.Status, events)
	}
}

func TestStopWritesMarkerForLiveOwner(t *testing.T) {
	resolved := resolvedProject(t)
	if err := ensureStateRoot(resolved.StateRoot); err != nil {
		t.Fatal(err)
	}
	started, err := lock.Acquire(resolved.StateRoot)
	if err != nil || started.Owner == nil {
		t.Fatalf("Acquire() = %#v, %v", started, err)
	}
	defer started.Owner.Release()
	var output bytes.Buffer
	controller := controllerForTest(t, Dependencies{
		Recovery: recoveryFunc(func(context.Context, *project.Resolution) error { return nil }),
		Snapshot: snapshotFunc(func(context.Context, *project.Resolution) ([]schedule.QueueApproval, error) { return nil, nil }),
		Build: buildFunc(func(context.Context, *project.Resolution, string) (single.Outcome, error) {
			return single.Outcome{}, nil
		}),
		Output: &output,
	})

	result, err := controller.Stop(context.Background(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	if result.PID != os.Getpid() || output.String() != "queue: stopping after the current Build (pid "+fmt.Sprint(os.Getpid())+")\n" {
		t.Fatalf("stop result=%#v output=%q", result, output.String())
	}
	if requested, err := started.Owner.StopRequested(); err != nil || !requested {
		t.Fatalf("owner did not observe stop request: requested=%t err=%v", requested, err)
	}
}

func controllerForTest(t *testing.T, deps Dependencies) *Controller {
	t.Helper()
	controller, err := NewController(deps)
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

func resolvedProject(t *testing.T) *project.Resolution {
	t.Helper()
	return &project.Resolution{Checkout: t.TempDir(), Origin: t.TempDir(), Base: "main", StateRoot: filepath.Join(t.TempDir(), "state", "queue")}
}

func approval(slug, key string, approvedAt, priority int64) schedule.QueueApproval {
	return schedule.QueueApproval{Slug: slug, ApprovalKey: key, ApprovalTime: approvedAt, Priority: priority, TargetBranch: "main"}
}
