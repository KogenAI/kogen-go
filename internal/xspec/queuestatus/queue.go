package queuestatus

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"kogen-go/internal/queue/lock"
	"kogen-go/internal/queue/schedule"
	"kogen-go/internal/xspec/protocol"
)

// QueueFactory creates an isolated queue lock and the production serial
// scheduler used by queue/drain.
func QueueFactory(ctx context.Context) (protocol.Slice, error) {
	slice := &queueSlice{}
	if err := slice.Reset(ctx); err != nil {
		return nil, err
	}
	return slice, nil
}

type queueSlice struct {
	root      string
	owner     *lock.Owner
	scheduler *schedule.QueueScheduler
}

func (s *queueSlice) Reset(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.close(); err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "kogen-xspec-queue-")
	if err != nil {
		return fmt.Errorf("queuestatus: create queue owner fixture: %w", err)
	}
	s.root = root
	s.scheduler = schedule.New("")
	return nil
}

func (s *queueSlice) HasEventTag(tag string) bool {
	switch tag {
	case "Enqueue", "Start", "Die", "Halt", "Outcome", "Release":
		return true
	default:
		return false
	}
}

func (s *queueSlice) Apply(ctx context.Context, event protocol.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var transition schedule.QueueEvent
	switch event.Tag {
	case "Enqueue":
		fields, err := eventObject(event, []string{"slug", "time", "priority"}, []string{"approval_hash", "approval_commit"})
		if err != nil {
			return err
		}
		slug, err := eventString(fields, "slug")
		if err != nil {
			return err
		}
		at, err := eventInt(fields, "time")
		if err != nil {
			return err
		}
		priority, err := eventInt(fields, "priority")
		if err != nil {
			return err
		}
		hash, err := optionalEventString(fields, "approval_hash")
		if err != nil {
			return err
		}
		commit, err := optionalEventString(fields, "approval_commit")
		if err != nil {
			return err
		}
		transition = schedule.QueueEvent{Kind: schedule.EventEnqueue, Approval: schedule.QueueApproval{
			Slug: slug, ApprovalTime: at, Priority: priority, ApprovalKey: hash, ApprovalCommit: commit,
		}}
	case "Start":
		started, err := lock.Acquire(s.root)
		if err != nil {
			return fmt.Errorf("queuestatus: acquire production queue owner: %w", err)
		}
		if started.Owner != nil {
			s.owner = started.Owner
		}
		transition = schedule.QueueEvent{Kind: schedule.EventStart}
	case "Die":
		if err := s.markOwnerDead(); err != nil {
			return err
		}
		transition = schedule.QueueEvent{Kind: schedule.EventDie}
	case "Halt":
		stopped, err := lock.RequestStop(s.root)
		if err != nil {
			return fmt.Errorf("queuestatus: request production queue stop: %w", err)
		}
		if stopped.Requested {
			if s.owner == nil {
				return fmt.Errorf("queuestatus: stop was delivered without this fixture owning the queue")
			}
			requested, err := s.owner.StopRequested()
			if err != nil || !requested {
				return fmt.Errorf("queuestatus: queue owner did not observe the published stop marker: %w", err)
			}
		}
		transition = schedule.QueueEvent{Kind: schedule.EventHalt}
	case "Outcome":
		fields, err := eventObject(event, []string{"kind"}, nil)
		if err != nil {
			return err
		}
		kind, err := eventString(fields, "kind")
		if err != nil {
			return err
		}
		transition = schedule.QueueEvent{Kind: schedule.EventOutcome, Outcome: queueOutcome(kind)}
	case "Release":
		transition = schedule.QueueEvent{Kind: schedule.EventRelease}
	default:
		return fmt.Errorf("queuestatus: unknown queue event %q", event.Tag)
	}

	observation := s.scheduler.Apply(transition)
	if !observation.Held && s.owner != nil {
		if err := s.owner.Release(); err != nil {
			return fmt.Errorf("queuestatus: release production queue owner: %w", err)
		}
		s.owner = nil
	}
	return nil
}

func (s *queueSlice) Observe(context.Context) (json.RawMessage, error) {
	return marshalObservation(s.scheduler.Observe())
}

func (s *queueSlice) Close() error { return s.close() }

func (s *queueSlice) close() error {
	var closeErr error
	if s.owner != nil {
		closeErr = s.owner.Release()
		s.owner = nil
	}
	if s.root != "" {
		if err := os.RemoveAll(s.root); err != nil && closeErr == nil {
			closeErr = err
		}
		s.root = ""
	}
	s.scheduler = nil
	return closeErr
}

// markOwnerDead injects a dead owner PID into this private fixture, then lets
// the production Owner release its descriptor without removing the stale lock.
// The following Acquire probes that PID through the real OS ownership path.
func (s *queueSlice) markOwnerDead() error {
	if s.owner == nil {
		return nil
	}
	pidPath := filepath.Join(s.root, "queue.pid")
	if err := os.WriteFile(pidPath, []byte("2147483647\n"), 0o600); err != nil {
		return fmt.Errorf("queuestatus: inject dead owner snapshot: %w", err)
	}
	if err := s.owner.Release(); err != nil {
		return fmt.Errorf("queuestatus: release dead owner descriptor: %w", err)
	}
	s.owner = nil
	return nil
}

func queueOutcome(kind string) schedule.DrainOutcome {
	switch kind {
	case "landed":
		return schedule.OutcomeLanded
	case "failed":
		return schedule.OutcomeFailed
	case "failed_provider":
		return schedule.OutcomeFailedProvider
	case "parked":
		return schedule.OutcomeParked
	case "stopped_environment":
		return schedule.OutcomeStoppedEnvironment
	case "stopped_provider":
		return schedule.OutcomeStoppedProvider
	case "stopped_controller":
		return schedule.OutcomeStoppedController
	case "skipped":
		return schedule.OutcomeSkipped
	default:
		return schedule.DrainOutcome("invalid:" + kind)
	}
}
