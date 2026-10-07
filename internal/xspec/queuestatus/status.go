package queuestatus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"time"

	"golang.org/x/sys/unix"

	"kogen-go/internal/contract"
	"kogen-go/internal/queue/lock"
	"kogen-go/internal/status/derive"
	"kogen-go/internal/status/render"
	"kogen-go/internal/status/report"
	"kogen-go/internal/xspec/protocol"
)

// StatusFactory creates an isolated state root for real queue-owner effects.
func StatusFactory(ctx context.Context) (protocol.Slice, error) {
	slice := &statusSlice{}
	if err := slice.Reset(ctx); err != nil {
		return nil, err
	}
	return slice, nil
}

type statusSlice struct {
	root         string
	queueOwner   *lock.Owner
	queuePID     *int
	intents      map[string]contract.IntentObservation
	refs         map[string]contract.RefObservation
	approvalTime map[string]int64
	landingTime  map[string]int64
	now          int64
	older        int64
	agentsBusy   int64
	watchSlug    string
	watchExit    int64
	last         string
}

func (s *statusSlice) Reset(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.close(); err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "kogen-xspec-status-")
	if err != nil {
		return fmt.Errorf("queuestatus: create status owner fixture: %w", err)
	}
	s.root = root
	s.intents = make(map[string]contract.IntentObservation)
	s.refs = make(map[string]contract.RefObservation)
	s.approvalTime = make(map[string]int64)
	s.landingTime = make(map[string]int64)
	s.watchExit = 0
	s.now = 0
	s.older = 0
	s.agentsBusy = 0
	s.watchSlug = ""
	s.last = "ok"
	return nil
}

func (s *statusSlice) HasEventTag(tag string) bool {
	switch tag {
	case "Row", "Raw", "Derive", "Now", "Older", "Queue", "Agents", "Watch", "Json":
		return true
	default:
		return false
	}
}

func (s *statusSlice) Apply(ctx context.Context, event protocol.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch event.Tag {
	case "Row":
		fields, err := eventObject(event, []string{"slug", "status", "priority", "at", "blocks", "sched", "started", "index"}, nil)
		if err != nil {
			return err
		}
		row, err := decodeStatusRow(fields)
		if err != nil {
			return err
		}
		s.applyRow(row)
	case "Raw":
		fields, err := eventObject(event, []string{"slug", "trailer", "claimed", "runStatus", "event", "alive", "approved", "reason", "same", "blocks", "priority", "at"}, nil)
		if err != nil {
			return err
		}
		raw, err := decodeStatusRaw(fields)
		if err != nil {
			return err
		}
		s.applyRaw(raw)
	case "Derive":
		s.last = "ok"
	case "Now":
		fields, err := eventObject(event, []string{"t"}, nil)
		if err != nil {
			return err
		}
		s.now, err = eventInt(fields, "t")
		if err != nil {
			return err
		}
		s.last = "ok"
	case "Older":
		fields, err := eventObject(event, []string{"n"}, nil)
		if err != nil {
			return err
		}
		count, err := eventInt(fields, "n")
		if err != nil {
			return err
		}
		if count < 0 {
			s.last = "bad_older"
		} else {
			s.older = count
			s.last = "ok"
		}
	case "Queue":
		fields, err := eventObject(event, []string{"running"}, nil)
		if err != nil {
			return err
		}
		running, err := eventBool(fields, "running")
		if err != nil {
			return err
		}
		if err := s.setQueueOwner(running); err != nil {
			return err
		}
		s.last = "ok"
	case "Agents":
		fields, err := eventObject(event, []string{"busy"}, nil)
		if err != nil {
			return err
		}
		busy, err := eventInt(fields, "busy")
		if err != nil {
			return err
		}
		if busy < 0 {
			s.last = "bad_agents"
		} else {
			s.agentsBusy = busy
			s.last = "ok"
		}
	case "Watch":
		fields, err := eventObject(event, []string{"slug"}, nil)
		if err != nil {
			return err
		}
		s.watchSlug, err = eventString(fields, "slug")
		if err != nil {
			return err
		}
		board, err := s.deriveBoard()
		if err != nil {
			return err
		}
		s.watchExit = s.computeWatchExit(board)
		s.last = "ok"
	case "Json":
		s.last = "ok"
	default:
		return fmt.Errorf("queuestatus: unknown status event %q", event.Tag)
	}
	return nil
}

func (s *statusSlice) Observe(ctx context.Context) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	board, err := s.deriveBoard()
	if err != nil {
		return nil, err
	}
	return marshalObservation(s.observation(board))
}

func (s *statusSlice) Close() error { return s.close() }

func (s *statusSlice) close() error {
	var closeErr error
	if s.queueOwner != nil {
		closeErr = s.queueOwner.Release()
		s.queueOwner = nil
	}
	if s.root != "" {
		if err := os.RemoveAll(s.root); err != nil && closeErr == nil {
			closeErr = err
		}
		s.root = ""
	}
	s.queuePID = nil
	s.intents = nil
	s.refs = nil
	s.approvalTime = nil
	s.landingTime = nil
	return closeErr
}

type statusRowEvent struct {
	slug, status, blocks, schedulingError  string
	priority, approvalTime, started, index int64
}

type statusRawEvent struct {
	slug, runStatus, lastEvent, reason, blocks   string
	trailer, claimed, ownerAlive, approved, same bool
	priority, approvalTime                       int64
}

func decodeStatusRow(fields map[string]json.RawMessage) (statusRowEvent, error) {
	var row statusRowEvent
	var err error
	if row.slug, err = eventString(fields, "slug"); err != nil {
		return row, err
	}
	if row.status, err = eventString(fields, "status"); err != nil {
		return row, err
	}
	if row.priority, err = eventInt(fields, "priority"); err != nil {
		return row, err
	}
	if row.approvalTime, err = eventInt(fields, "at"); err != nil {
		return row, err
	}
	if row.blocks, err = eventString(fields, "blocks"); err != nil {
		return row, err
	}
	if row.schedulingError, err = eventString(fields, "sched"); err != nil {
		return row, err
	}
	if row.started, err = eventInt(fields, "started"); err != nil {
		return row, err
	}
	if row.index, err = eventInt(fields, "index"); err != nil {
		return row, err
	}
	return row, nil
}

func decodeStatusRaw(fields map[string]json.RawMessage) (statusRawEvent, error) {
	var raw statusRawEvent
	var err error
	if raw.slug, err = eventString(fields, "slug"); err != nil {
		return raw, err
	}
	if raw.trailer, err = eventBool(fields, "trailer"); err != nil {
		return raw, err
	}
	if raw.claimed, err = eventBool(fields, "claimed"); err != nil {
		return raw, err
	}
	if raw.runStatus, err = eventString(fields, "runStatus"); err != nil {
		return raw, err
	}
	if raw.lastEvent, err = eventString(fields, "event"); err != nil {
		return raw, err
	}
	if raw.ownerAlive, err = eventBool(fields, "alive"); err != nil {
		return raw, err
	}
	if raw.approved, err = eventBool(fields, "approved"); err != nil {
		return raw, err
	}
	if raw.reason, err = eventString(fields, "reason"); err != nil {
		return raw, err
	}
	if raw.same, err = eventBool(fields, "same"); err != nil {
		return raw, err
	}
	if raw.blocks, err = eventString(fields, "blocks"); err != nil {
		return raw, err
	}
	if raw.priority, err = eventInt(fields, "priority"); err != nil {
		return raw, err
	}
	if raw.approvalTime, err = eventInt(fields, "at"); err != nil {
		return raw, err
	}
	return raw, nil
}

func (s *statusSlice) applyRow(row statusRowEvent) {
	if !knownStatusSlug(row.slug) {
		s.last = "bad_slug"
		return
	}
	if !knownStatusValue(row.status) {
		s.last = "bad_status"
		return
	}
	if invalidStatusDependency(row.blocks) {
		s.last = "bad_blocks"
		return
	}
	s.clearIntent(row.slug)
	intent := contract.IntentObservation{
		Slug: row.slug, Priority: int(row.priority), Dependencies: blocksList(row.blocks),
	}
	s.approvalTime[row.slug] = row.approvalTime
	s.landingTime[row.slug] = row.index
	switch row.status {
	case "landed":
		intent.LandedCommit = contract.ObjectID("landed-" + row.slug)
	case "approved", "failed", "parked", "building", "interrupted":
		approval := "approval-" + row.slug
		s.refs[approvalRef(row.slug)] = contract.RefObservation{Name: approvalRef(row.slug), Target: contract.ObjectID(approval), Exists: true}
		if row.status == "failed" || row.status == "parked" || row.status == "building" || row.status == "interrupted" {
			run := &contract.RunObservation{
				RunID: "run-" + row.slug, ApprovalCommit: contract.ObjectID(approval),
				Status: row.status, OwnerPID: deadOwnerPID(), StartedAt: time.Unix(row.started, 0),
			}
			if row.status == "building" {
				run.Status = "running"
				run.OwnerPID = os.Getpid()
				s.refs[claimRef()] = contract.RefObservation{Name: claimRef(), Target: contract.ObjectID(run.RunID), Exists: true}
			} else if row.status == "interrupted" {
				run.Status = "failed"
				run.Reason = "interrupted"
			}
			intent.LatestRun = run
		}
	}
	s.intents[row.slug] = intent
	s.last = "ok"
}

func (s *statusSlice) applyRaw(raw statusRawEvent) {
	if !knownStatusSlug(raw.slug) {
		s.last = "bad_slug"
		return
	}
	if invalidStatusDependency(raw.blocks) {
		s.last = "bad_blocks"
		return
	}
	s.clearIntent(raw.slug)
	intent := contract.IntentObservation{
		Slug: raw.slug, Priority: int(raw.priority), Dependencies: blocksList(raw.blocks),
	}
	s.approvalTime[raw.slug] = raw.approvalTime
	s.landingTime[raw.slug] = 0
	if raw.approved {
		current := contract.ObjectID("approval-" + raw.slug + "-current")
		s.refs[approvalRef(raw.slug)] = contract.RefObservation{Name: approvalRef(raw.slug), Target: current, Exists: true}
	}
	if raw.trailer {
		intent.LandedCommit = contract.ObjectID("landed-" + raw.slug)
	}
	if raw.runStatus != "" {
		runApproval := contract.ObjectID("approval-" + raw.slug + "-previous")
		if raw.approved && raw.same {
			runApproval = s.refs[approvalRef(raw.slug)].Target
		}
		pid := deadOwnerPID()
		if raw.ownerAlive {
			pid = os.Getpid()
		}
		run := &contract.RunObservation{
			RunID: "run-" + raw.slug, ApprovalCommit: runApproval,
			Status: raw.runStatus, Reason: raw.reason, LastEvent: raw.lastEvent,
			OwnerPID: pid,
		}
		intent.LatestRun = run
		if raw.claimed {
			s.refs[claimRef()] = contract.RefObservation{Name: claimRef(), Target: contract.ObjectID(run.RunID), Exists: true}
		}
	}
	s.intents[raw.slug] = intent
	s.last = "ok"
}

func (s *statusSlice) clearIntent(slug string) {
	delete(s.intents, slug)
	delete(s.refs, approvalRef(slug))
	if claim, ok := s.refs[claimRef()]; ok && string(claim.Target) == "run-"+slug {
		delete(s.refs, claimRef())
	}
	delete(s.approvalTime, slug)
	delete(s.landingTime, slug)
}

func (s *statusSlice) setQueueOwner(running bool) error {
	if running {
		started, err := lock.Acquire(s.root)
		if err != nil {
			return fmt.Errorf("queuestatus: acquire status queue owner: %w", err)
		}
		if started.Owner != nil {
			s.queueOwner = started.Owner
			pid := started.Owner.PID()
			s.queuePID = &pid
		} else {
			pid := started.PID
			s.queuePID = &pid
		}
		return nil
	}
	if s.queueOwner != nil {
		if err := s.queueOwner.Release(); err != nil {
			return fmt.Errorf("queuestatus: release status queue owner: %w", err)
		}
		s.queueOwner = nil
	}
	s.queuePID = nil
	return nil
}

func (s *statusSlice) deriveBoard() (derive.Board, error) {
	observed := s.snapshot()
	for index := range observed.Intents {
		if observed.Intents[index].LatestRun != nil {
			run := *observed.Intents[index].LatestRun
			alive, err := probeProcessOwner(run.OwnerPID)
			if err != nil {
				return derive.Board{}, fmt.Errorf("queuestatus: probe run owner: %w", err)
			}
			run.OwnerAlive = alive
			observed.Intents[index].LatestRun = &run
		}
	}

	refs := make(map[string]contract.RefObservation, len(observed.Refs))
	for _, ref := range observed.Refs {
		refs[ref.Name] = ref
	}
	input := derive.Input{}
	if claim := refs[claimRef()]; claim.Exists {
		input.ClaimRunID = string(claim.Target)
	}
	for _, observedIntent := range observed.Intents {
		intent := derive.Intent{
			Slug: observedIntent.Slug, Priority: observedIntent.Priority,
			Dependencies: append([]string(nil), observedIntent.Dependencies...),
			ApprovalTime: s.approvalTime[observedIntent.Slug],
		}
		if approval := refs[approvalRef(observedIntent.Slug)]; approval.Exists {
			intent.ApprovalCommit = string(approval.Target)
		}
		if observedIntent.LatestRun != nil {
			run := observedIntent.LatestRun
			startedAt := int64(0)
			if !run.StartedAt.IsZero() {
				startedAt = run.StartedAt.Unix()
			}
			input.Runs = append(input.Runs, derive.Run{
				ID: run.RunID, Slug: observedIntent.Slug,
				ApprovalCommit: string(run.ApprovalCommit), Status: run.Status,
				Reason: run.Reason, LastEvent: run.LastEvent, OwnerAlive: run.OwnerAlive,
				StartedAt: startedAt,
			})
		}
		if observedIntent.LandedCommit != "" {
			input.Landings = append(input.Landings, derive.Landing{
				Slug: observedIntent.Slug, Commit: string(observedIntent.LandedCommit), CommitTime: s.landingTime[observedIntent.Slug],
			})
		}
		input.Intents = append(input.Intents, intent)
	}
	return derive.Derive(input), nil
}

func (s *statusSlice) observation(board derive.Board) statusObservation {
	bySlug := make(map[string]derive.Row, len(board.Rows))
	for _, row := range board.Rows {
		bySlug[row.Intent.Slug] = row
	}
	statusOf := func(slug string) string {
		if row, ok := bySlug[slug]; ok {
			return string(row.Status)
		}
		return ""
	}
	reasonOf := func(slug string) string {
		if row, ok := bySlug[slug]; ok {
			return row.WaitReason
		}
		return ""
	}
	count := func(kind derive.Kind) int64 {
		var total int64
		for _, row := range board.Rows {
			if row.Status == kind {
				total++
			}
		}
		return total
	}
	sections := make([]string, 0, 8)
	if count(derive.Building) > 0 {
		sections = append(sections, "Building")
	}
	if len(board.Queue) > 0 {
		sections = append(sections, "Queued")
	}
	if count(derive.Blocked) > 0 {
		sections = append(sections, "Blocked")
	}
	if count(derive.Failed) > 0 {
		sections = append(sections, "Failed")
	}
	if count(derive.Parked) > 0 {
		sections = append(sections, "Parked")
	}
	if count(derive.Interrupted) > 0 {
		sections = append(sections, "Interrupted")
	}
	if count(derive.Draft) > 0 {
		sections = append(sections, "Drafts")
	}
	landed := count(derive.Landed) + s.older
	if landed > 0 {
		sections = append(sections, "Landed")
	}

	elapsed := ""
	for _, slug := range []string{"alpha", "bravo", "charlie"} {
		row, ok := bySlug[slug]
		if !ok || row.Status != derive.Building || row.Run == nil || row.Run.StartedAt == 0 {
			continue
		}
		age := s.now - row.Run.StartedAt
		switch {
		case age < 60:
			elapsed = "s"
		case age < 3600:
			elapsed = "m"
		default:
			elapsed = "h"
		}
		break
	}

	queueLine := "stopped"
	if s.queuePID != nil {
		queueLine = "running"
	} else if len(board.Queue) > 0 {
		queueLine = "waiting"
	}
	var next string
	var nextPriority int64
	var nextDependencies string
	if len(board.Queue) > 0 {
		next = board.Queue[0]
		if row, ok := bySlug[next]; ok {
			nextPriority = int64(row.Intent.Priority)
			if len(row.Intent.Dependencies) == 0 {
				nextDependencies = "no_dependencies"
			} else {
				nextDependencies = "dependencies_delivered"
			}
		}
	}
	watchStatus := ""
	if s.watchSlug != "" {
		if s.watchExit == 2 {
			watchStatus = "not_found"
		} else if statusOf(s.watchSlug) == string(derive.Approved) {
			watchStatus = "queued"
		} else {
			watchStatus = statusOf(s.watchSlug)
		}
	}
	watchPosition := int64(-1)
	for index, slug := range board.Queue {
		if slug == s.watchSlug {
			watchPosition = int64(index)
			break
		}
	}
	return statusObservation{
		Last: s.last, Alpha: statusOf("alpha"), Bravo: statusOf("bravo"), Charlie: statusOf("charlie"),
		Queue: nonNilStrings(board.Queue), WhyA: reasonOf("alpha"), WhyB: reasonOf("bravo"), WhyC: reasonOf("charlie"),
		Sections: sections, Earlier: max(int64(0), landed-5), Elapsed: elapsed, QueueLine: queueLine,
		Next: next, NextPriority: nextPriority, NextDependencies: nextDependencies,
		LandedShown: min(int64(5), landed), WatchSlug: s.watchSlug, WatchStatus: watchStatus,
		WatchPosition: watchPosition, WatchQueueSize: int64(len(board.Queue)), Exit: s.watchExit, JSONDetail: false,
	}
}

func (s *statusSlice) computeWatchExit(board derive.Board) int64 {
	if s.watchSlug != "" {
		found := false
		for _, row := range board.Rows {
			if row.Intent.Slug == s.watchSlug {
				found = true
				break
			}
		}
		if !found {
			return 2
		}
		return int64(render.WatchExitCode(report.Report{Board: board}, s.watchSlug))
	}
	agents := make([]report.Agent, 0, min(s.agentsBusy, 64))
	for index := int64(0); index < s.agentsBusy && index < 64; index++ {
		agents = append(agents, report.Agent{ID: fmt.Sprintf("%032x", index+1), Status: "running"})
	}
	status := report.Report{Board: board, QueuePID: s.queuePID, Agents: agents}
	if render.Idle(status) {
		return 0
	}
	return -1
}

type statusObservation struct {
	Last             string   `json:"last"`
	Alpha            string   `json:"alpha"`
	Bravo            string   `json:"bravo"`
	Charlie          string   `json:"charlie"`
	Queue            []string `json:"queue"`
	WhyA             string   `json:"whyA"`
	WhyB             string   `json:"whyB"`
	WhyC             string   `json:"whyC"`
	Sections         []string `json:"sections"`
	Earlier          int64    `json:"earlier"`
	Elapsed          string   `json:"elapsed"`
	QueueLine        string   `json:"queueLine"`
	Next             string   `json:"next"`
	NextPriority     int64    `json:"nextPriority"`
	NextDependencies string   `json:"nextDependencies"`
	LandedShown      int64    `json:"landedShown"`
	WatchSlug        string   `json:"watchSlug"`
	WatchStatus      string   `json:"watchStatus"`
	WatchPosition    int64    `json:"watchPosition"`
	WatchQueueSize   int64    `json:"watchQueueSize"`
	Exit             int64    `json:"exit"`
	JSONDetail       bool     `json:"jsonDetail"`
}

func (s *statusSlice) String() string { return "production-backed queue/status xspec slice" }

func (s *statusSlice) setError(code string) { s.last = code }

func (s *statusSlice) snapshot() contract.StateObservations {
	observed := contract.StateObservations{}
	for _, intent := range s.intents {
		copy := intent
		copy.Dependencies = append([]string(nil), intent.Dependencies...)
		if intent.LatestRun != nil {
			run := *intent.LatestRun
			copy.LatestRun = &run
		}
		observed.Intents = append(observed.Intents, copy)
	}
	for _, ref := range s.refs {
		observed.Refs = append(observed.Refs, ref)
	}
	sort.Slice(observed.Intents, func(i, j int) bool { return observed.Intents[i].Slug < observed.Intents[j].Slug })
	sort.Slice(observed.Refs, func(i, j int) bool { return observed.Refs[i].Name < observed.Refs[j].Name })
	return observed
}

func knownStatusSlug(slug string) bool {
	return slug == "alpha" || slug == "bravo" || slug == "charlie"
}

func knownStatusValue(status string) bool {
	switch status {
	case "approved", "building", "failed", "parked", "draft", "landed", "interrupted":
		return true
	default:
		return false
	}
}

func invalidStatusDependency(slug string) bool {
	return slug != "" && slug != "BAD" && slug != "ghost" && !knownStatusSlug(slug)
}

func blocksList(slug string) []string {
	if slug == "" {
		return nil
	}
	return []string{slug}
}

func approvalRef(slug string) string { return "refs/kogen/intents/" + slug }
func claimRef() string               { return "refs/kogen/claim" }
func deadOwnerPID() int              { return math.MaxInt32 }

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func probeProcessOwner(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	err := unix.Kill(pid, 0)
	if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.EINVAL) {
		return false, nil
	}
	if errors.Is(err, unix.EPERM) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
