package schedule

import (
	"reflect"
	"testing"
)

func enqueue(s *QueueScheduler, approval QueueApproval) QueueObservation {
	return s.Apply(QueueEvent{Kind: EventEnqueue, Approval: approval})
}

func item(slug, key string, time, priority int64) QueueApproval {
	return QueueApproval{Slug: slug, ApprovalKey: key, ApprovalTime: time, Priority: priority}
}

func TestQueueOrderAndBlockedCandidates(t *testing.T) {
	s := New("main")
	enqueue(s, item("charlie", "c", 2, 0))
	enqueue(s, item("bravo", "b", 1, 0))
	enqueue(s, item("alpha", "a", 1, 0))
	enqueue(s, item("high", "h", 9, 2))
	blocked := item("blocked", "x", 1, 100)
	blocked.Blocked = true
	enqueue(s, blocked)

	if got, want := s.Observe().Queue, []string{"high", "alpha", "bravo", "charlie"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("queue = %v, want %v", got, want)
	}

	started := s.Apply(QueueEvent{Kind: EventStart})
	if started.Current != "high" || started.Phase != "building" {
		t.Fatalf("start = %#v, want high building", started)
	}
}

func TestBlockedOnlyQueueDoesNotStartBuild(t *testing.T) {
	s := New("main")
	blocked := item("blocked", "x", 1, 10)
	blocked.Blocked = true
	enqueue(s, blocked)

	got := s.Apply(QueueEvent{Kind: EventStart})
	if got.Line != "nothing_to_build" || got.Built != 0 || got.Exit != 0 || got.Held {
		t.Fatalf("blocked-only drain = %#v", got)
	}
}

func TestBranchMismatchIsSkippedWithoutBuildCount(t *testing.T) {
	s := New("main")
	approval := item("alpha", "a", 1, 0)
	approval.TargetBranch = "other"
	enqueue(s, approval)

	skipping := s.Apply(QueueEvent{Kind: EventStart})
	if skipping.Phase != "skipping" || skipping.Line != "skipped" || skipping.Current != "alpha" || skipping.Built != 0 {
		t.Fatalf("branch mismatch selection = %#v", skipping)
	}
	if got := s.Apply(QueueEvent{Kind: EventOutcome, Outcome: OutcomeSkipped}); got.Line != "nothing_to_build" || got.Exit != 0 || got.Built != 0 {
		t.Fatalf("branch mismatch completion = %#v", got)
	}
}

func TestSameApprovalKeyIsAttemptedOnceAcrossRepublishing(t *testing.T) {
	s := New("main")
	alpha := item("alpha", "same-content", 1, 0)
	alpha.ApprovalCommit = "commit-one"
	enqueue(s, alpha)
	enqueue(s, item("bravo", "bravo-content", 2, 0))
	s.Apply(QueueEvent{Kind: EventStart})

	// A republished approval commit with the same content key remains the same
	// attempt, even if its queue position changes.
	alpha.ApprovalTime = 3
	alpha.Priority = 99
	alpha.ApprovalCommit = "commit-two"
	enqueue(s, alpha)

	got := s.Apply(QueueEvent{Kind: EventOutcome, Outcome: OutcomeFailed})
	if got.Current != "bravo" || got.Built != 1 || !reflect.DeepEqual(got.Queue, []string{}) {
		t.Fatalf("same-key republish scheduled again: %#v", got)
	}
}

func TestChangedApprovalKeyMayBeAttemptedAgainInSameDrain(t *testing.T) {
	s := New("main")
	enqueue(s, item("alpha", "old", 1, 0))
	s.Apply(QueueEvent{Kind: EventStart})
	enqueue(s, item("alpha", "new", 2, 0))

	got := s.Apply(QueueEvent{Kind: EventOutcome, Outcome: OutcomeFailed})
	if got.Current != "alpha" || got.Phase != "building" || got.Built != 1 {
		t.Fatalf("new approval key was not eligible: %#v", got)
	}
	finished := s.Apply(QueueEvent{Kind: EventOutcome, Outcome: OutcomeLanded})
	if finished.Line != "done" || finished.Built != 2 || finished.Landed != 1 || finished.Exit != 1 {
		t.Fatalf("changed-key drain counts = %#v", finished)
	}
}

func TestRefreshCanUnblockCandidateBeforeNextSelection(t *testing.T) {
	s := New("main")
	enqueue(s, item("dependency", "dep", 1, 1))
	dependent := item("dependent", "child", 2, 0)
	dependent.Blocked = true
	enqueue(s, dependent)
	s.Apply(QueueEvent{Kind: EventStart})

	dependent.Blocked = false
	s.Apply(QueueEvent{Kind: EventRefresh, Approvals: []QueueApproval{dependent}})

	got := s.Apply(QueueEvent{Kind: EventOutcome, Outcome: OutcomeLanded})
	if got.Current != "dependent" || got.Built != 1 {
		t.Fatalf("refreshed dependent was not scheduled: %#v", got)
	}
}

func TestFailedProviderContinuesButStoppedProviderStaysQueued(t *testing.T) {
	s := New("main")
	enqueue(s, item("alpha", "a", 1, 0))
	enqueue(s, item("bravo", "b", 2, 0))
	s.Apply(QueueEvent{Kind: EventStart})

	failed := s.Apply(QueueEvent{Kind: EventOutcome, Outcome: OutcomeFailedProvider})
	if failed.Current != "bravo" || failed.Built != 1 || !failed.Held {
		t.Fatalf("recoverable provider failure did not continue: %#v", failed)
	}
	stopped := s.Apply(QueueEvent{Kind: EventOutcome, Outcome: OutcomeStoppedProvider})
	if stopped.Line != "stopped_because" || stopped.Exit != 4 || stopped.Built != 2 || stopped.Landed != 0 {
		t.Fatalf("provider stop = %#v", stopped)
	}
	if got, want := stopped.Queue, []string{"bravo"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stopped approval queue = %v, want %v", got, want)
	}
}

func TestStopAfterFailedBuildUsesNormalDrainExit(t *testing.T) {
	s := New("main")
	enqueue(s, item("alpha", "a", 1, 0))
	enqueue(s, item("bravo", "b", 2, 0))
	s.Apply(QueueEvent{Kind: EventStart})
	s.Apply(QueueEvent{Kind: EventHalt})

	got := s.Apply(QueueEvent{Kind: EventOutcome, Outcome: OutcomeFailed})
	if got.Line != "stopped_on_request" || got.Exit != 1 || got.Built != 1 || got.Landed != 0 {
		t.Fatalf("stop-after-failure result = %#v", got)
	}
	if got.Current != "" || !reflect.DeepEqual(got.Queue, []string{"bravo"}) {
		t.Fatalf("stop-after-failure queue = %#v", got)
	}
}

func TestStoppedControllerBuildIsCountedAndRemainsQueued(t *testing.T) {
	s := New("main")
	enqueue(s, item("alpha", "a", 1, 0))
	s.Apply(QueueEvent{Kind: EventStart})

	got := s.Apply(QueueEvent{Kind: EventOutcome, Outcome: OutcomeStoppedController})
	if got.Line != "stopped_because" || got.Exit != 70 || got.Built != 1 || got.Landed != 0 {
		t.Fatalf("controller stop result = %#v", got)
	}
	if !reflect.DeepEqual(got.Queue, []string{"alpha"}) {
		t.Fatalf("stopped approval was removed: %#v", got)
	}
}

func TestDeadOwnerCanBeTakenOverAndRetryCurrentApproval(t *testing.T) {
	s := New("main")
	enqueue(s, item("alpha", "a", 1, 0))
	s.Apply(QueueEvent{Kind: EventStart})
	s.Apply(QueueEvent{Kind: EventDie})

	got := s.Apply(QueueEvent{Kind: EventStart})
	if got.Current != "alpha" || got.Phase != "building" || !got.Alive || !got.Held {
		t.Fatalf("takeover did not retry approval: %#v", got)
	}
}
