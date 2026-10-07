package schedule

import "sort"

// QueueApproval is one approval candidate from a status snapshot. ApprovalKey
// is the stable content key (the digest of the exact approved Intent and
// acceptance bytes), not the commit that happened to publish it. Republishing
// identical approved bytes therefore does not create a second attempt in the
// same drain. A changed key does.
type QueueApproval struct {
	Slug           string
	ApprovalTime   int64
	Priority       int64
	ApprovalKey    string
	ApprovalCommit string
	TargetBranch   string
	Blocked        bool
}

// DrainOutcome is the observed result of attempting the current approval.
// FailedProvider is a recoverable per-Intent provider error; stopped provider
// outcomes represent conditions that prevent the drain from doing useful work.
type DrainOutcome string

const (
	OutcomeLanded             DrainOutcome = "landed"
	OutcomeFailed             DrainOutcome = "failed"
	OutcomeFailedProvider     DrainOutcome = "failed_provider"
	OutcomeParked             DrainOutcome = "parked"
	OutcomeStoppedEnvironment DrainOutcome = "stopped_environment"
	OutcomeStoppedProvider    DrainOutcome = "stopped_provider"
	OutcomeStoppedController  DrainOutcome = "stopped_controller"
	OutcomeSkipped            DrainOutcome = "skipped"
)

// QueueEvent is an input to the pure queue transition. Refresh replaces the
// pending candidates with a fresh status-derived snapshot; it can be applied
// after a Build has returned and before its Outcome event so dependencies and
// newly approved content are reflected before the next candidate is chosen.
type QueueEvent struct {
	Kind      QueueEventKind
	Approval  QueueApproval
	Approvals []QueueApproval
	Outcome   DrainOutcome
}

type QueueEventKind string

const (
	EventEnqueue QueueEventKind = "Enqueue"
	EventRefresh QueueEventKind = "Refresh"
	EventStart   QueueEventKind = "Start"
	EventDie     QueueEventKind = "Die"
	EventHalt    QueueEventKind = "Halt"
	EventOutcome QueueEventKind = "Outcome"
	EventRelease QueueEventKind = "Release"
)

// QueueObservation is the externally visible scheduler state. Phase is
// "building" for a Build, "skipping" for an approval-branch mismatch, and
// "idle" otherwise.
type QueueObservation struct {
	Last    string   `json:"last"`
	Line    string   `json:"line"`
	Exit    int      `json:"exit"`
	Held    bool     `json:"held"`
	Alive   bool     `json:"alive"`
	Stop    bool     `json:"stop"`
	Phase   string   `json:"phase"`
	Current string   `json:"current"`
	Queue   []string `json:"queue"`
	Built   uint64   `json:"built"`
	Landed  uint64   `json:"landed"`
}

type approvalKey struct {
	slug string
	key  string
}

// QueueScheduler owns the in-memory serial drain policy. It performs no I/O:
// callers supply fresh status snapshots, run one selected Build, then feed the
// observed outcome back into Apply.
type QueueScheduler struct {
	baseBranch string
	held       bool
	alive      bool
	stop       bool
	current    *QueueApproval
	phase      string
	waiting    map[string]QueueApproval
	attempted  map[approvalKey]struct{}
	built      uint64
	landed     uint64
	exit       int
	last       string
	line       string
}

// New creates a scheduler for the project's configured base branch. Branch
// mismatch candidates are skipped without starting a Build or changing counts.
func New(baseBranch string) *QueueScheduler {
	return &QueueScheduler{
		baseBranch: baseBranch,
		waiting:    make(map[string]QueueApproval),
		attempted:  make(map[approvalKey]struct{}),
		phase:      "idle",
		last:       "ok",
	}
}

// Apply applies one queue event and returns a complete immutable observation.
func (s *QueueScheduler) Apply(event QueueEvent) QueueObservation {
	switch event.Kind {
	case EventEnqueue:
		s.enqueue(event.Approval)
	case EventRefresh:
		s.refresh(event.Approvals)
	case EventStart:
		s.start()
	case EventDie:
		s.die()
	case EventHalt:
		s.halt()
	case EventOutcome:
		s.outcome(event.Outcome)
	case EventRelease:
		s.release()
	default:
		s.error("unknown_event")
	}
	return s.Observe()
}

// Observe returns a snapshot without exposing mutable scheduler storage.
func (s *QueueScheduler) Observe() QueueObservation {
	current := ""
	if s.current != nil {
		current = s.current.Slug
	}
	return QueueObservation{
		Last:    s.last,
		Line:    s.line,
		Exit:    s.exit,
		Held:    s.held,
		Alive:   s.alive,
		Stop:    s.stop,
		Phase:   s.phase,
		Current: current,
		Queue:   s.orderedSlugs(),
		Built:   s.built,
		Landed:  s.landed,
	}
}

func (s *QueueScheduler) enqueue(approval QueueApproval) {
	if approval.Slug == "" {
		s.error("invalid_approval")
		return
	}
	s.waiting[approval.Slug] = approval
	s.ok("enqueued")
}

func (s *QueueScheduler) refresh(approvals []QueueApproval) {
	next := make(map[string]QueueApproval, len(approvals))
	for _, approval := range approvals {
		if approval.Slug == "" {
			s.error("invalid_approval")
			return
		}
		if _, exists := next[approval.Slug]; exists {
			s.error("duplicate_approval")
			return
		}
		next[approval.Slug] = approval
	}
	s.waiting = next
	s.ok("refreshed")
}

func (s *QueueScheduler) start() {
	if s.held && s.alive {
		s.last = "ok"
		s.line = "already_running"
		s.exit = 0
		return
	}
	s.held = true
	s.alive = true
	s.stop = false
	s.attempted = make(map[approvalKey]struct{})
	s.built = 0
	s.landed = 0
	s.current = nil
	s.phase = "idle"
	s.launch()
}

func (s *QueueScheduler) die() {
	if !s.held {
		s.error("no_process")
		return
	}
	s.alive = false
	s.last = "ok"
	s.line = "owner_dead"
}

func (s *QueueScheduler) halt() {
	if !s.held || !s.alive {
		s.last = "ok"
		s.line = "not_running"
		s.exit = 0
		return
	}
	s.stop = true
	s.last = "ok"
	s.line = "stopping"
}

func (s *QueueScheduler) release() {
	if !s.held || !s.alive {
		s.error("not_owner")
		return
	}
	if s.current != nil {
		s.error("build_in_flight")
		return
	}
	s.finishDrain("released", 0)
}

func (s *QueueScheduler) outcome(outcome DrainOutcome) {
	if s.current == nil || !s.alive {
		s.error("not_building")
		return
	}
	if outcome == OutcomeSkipped {
		if s.phase != "skipping" {
			s.error("unexpected_skip")
			return
		}
		s.dropCurrent()
		s.launch()
		return
	}
	if s.phase != "building" {
		s.error("not_building")
		return
	}
	switch outcome {
	case OutcomeLanded, OutcomeFailed, OutcomeFailedProvider, OutcomeParked:
		s.built++
		if outcome == OutcomeLanded {
			s.landed++
		}
		s.dropCurrent()
		s.launch()
	case OutcomeStoppedEnvironment, OutcomeStoppedProvider, OutcomeStoppedController:
		s.built++
		exit := 3
		if outcome == OutcomeStoppedProvider {
			exit = 4
		} else if outcome == OutcomeStoppedController {
			exit = 70
		}
		s.finishDrain("stopped_because", exit)
	default:
		s.error("unknown_outcome")
	}
}

func (s *QueueScheduler) launch() {
	if s.stop {
		// A queue stop ends after the current Build. Its exit still reflects
		// earlier failed Builds, as required by §1.7.4.
		s.finishDrain("stopped_on_request", s.normalExit())
		return
	}
	next, ok := s.nextApproval()
	if !ok {
		line := "nothing_to_build"
		if s.built > 0 {
			line = "done"
		}
		s.finishDrain(line, s.normalExit())
		return
	}
	identity := keyFor(next)
	s.attempted[identity] = struct{}{}
	copy := next
	s.current = &copy
	s.last = "ok"
	s.exit = 0
	if s.baseBranch != "" && next.TargetBranch != "" && next.TargetBranch != s.baseBranch {
		s.phase = "skipping"
		s.line = "skipped"
		return
	}
	s.phase = "building"
	s.line = "building"
}

func (s *QueueScheduler) nextApproval() (QueueApproval, bool) {
	ordered := make([]QueueApproval, 0, len(s.waiting))
	for _, approval := range s.waiting {
		if approval.Blocked {
			continue
		}
		if _, tried := s.attempted[keyFor(approval)]; tried {
			continue
		}
		ordered = append(ordered, approval)
	}
	sortApprovals(ordered)
	if len(ordered) == 0 {
		return QueueApproval{}, false
	}
	return ordered[0], true
}

func (s *QueueScheduler) orderedSlugs() []string {
	ordered := make([]QueueApproval, 0, len(s.waiting))
	for _, approval := range s.waiting {
		if approval.Blocked {
			continue
		}
		if _, tried := s.attempted[keyFor(approval)]; tried {
			continue
		}
		ordered = append(ordered, approval)
	}
	sortApprovals(ordered)
	slugs := make([]string, len(ordered))
	for index, approval := range ordered {
		slugs[index] = approval.Slug
	}
	return slugs
}

func sortApprovals(approvals []QueueApproval) {
	sort.Slice(approvals, func(i, j int) bool {
		left, right := approvals[i], approvals[j]
		if left.Priority != right.Priority {
			return left.Priority > right.Priority
		}
		if left.ApprovalTime != right.ApprovalTime {
			return left.ApprovalTime < right.ApprovalTime
		}
		return left.Slug < right.Slug
	})
}

func (s *QueueScheduler) dropCurrent() {
	if s.current != nil {
		if latest, ok := s.waiting[s.current.Slug]; ok && keyFor(*s.current) == keyFor(latest) {
			delete(s.waiting, s.current.Slug)
		}
	}
	s.current = nil
	s.phase = "idle"
}

func keyFor(approval QueueApproval) approvalKey {
	return approvalKey{slug: approval.Slug, key: approval.ApprovalKey}
}

func (s *QueueScheduler) normalExit() int {
	if s.built == 0 || s.landed == s.built {
		return 0
	}
	return 1
}

func (s *QueueScheduler) finishDrain(line string, exit int) {
	s.held = false
	s.alive = false
	s.stop = false
	s.current = nil
	s.phase = "idle"
	s.attempted = make(map[approvalKey]struct{})
	s.last = "ok"
	s.line = line
	s.exit = exit
}

func (s *QueueScheduler) ok(line string) {
	s.last = "ok"
	s.line = line
}

func (s *QueueScheduler) error(code string) {
	s.last = code
	s.line = code
}
