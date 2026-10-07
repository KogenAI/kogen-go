// Package derive computes Intent status and queue order from immutable project
// observations. It performs no I/O and does not recover runs or read Git state.
package derive

import (
	"sort"
	"strings"
)

// Kind is the derived state shown for an Intent.
type Kind string

const (
	Building    Kind = "building"
	Approved    Kind = "approved"
	Blocked     Kind = "blocked"
	Failed      Kind = "failed"
	Parked      Kind = "parked"
	Interrupted Kind = "interrupted"
	Draft       Kind = "draft"
	Landed      Kind = "landed"
)

// Intent contains the current checkout's scheduling fields and approval
// identity. An empty ApprovalCommit means that no approval ref exists.
type Intent struct {
	Slug            string
	Priority        int
	ApprovalCommit  string
	ApprovalTime    int64
	Dependencies    []string
	SchedulingError string
}

// Run is one persisted Build observation. Derive selects the latest run per
// slug by StartedAt, breaking ties by the lexicographically greater ID.
type Run struct {
	ID             string
	Slug           string
	ApprovalCommit string
	Status         string
	Reason         string
	LastEvent      string
	Stage          string
	OwnerAlive     bool
	StartedAt      int64
}

// Landing is a Kogen-Intent trailer found on a commit reachable from the
// configured base. It is independent of the current checkout's Intent bytes.
type Landing struct {
	Slug       string
	Commit     string
	CommitTime int64
}

// Input is the complete read-only observation used for status derivation.
// Runs should already reflect recovery. Intent slugs are expected to be unique
// and already validated by the reader.
type Input struct {
	Intents    []Intent
	Runs       []Run
	Landings   []Landing
	ClaimRunID string
}

// Row is the derived status for one Intent. Run and Landing are nil when no
// matching observation exists. WaitReason is set only for Blocked rows.
type Row struct {
	Intent     Intent
	Status     Kind
	WaitReason string
	Run        *Run
	Landing    *Landing
}

// Board contains slug-sorted rows and the separately sorted drain queue.
type Board struct {
	Rows  []Row
	Queue []string
}

// Derive applies the spec's status precedence, dependency blocking, and queue
// order. Input slices are not modified.
func Derive(input Input) Board {
	latestRuns := latestRuns(input.Runs)
	landings := latestLandings(input.Landings)
	rows := make([]Row, 0, len(input.Intents))
	baseKinds := make(map[string]Kind, len(input.Intents))
	intentBySlug := make(map[string]Intent, len(input.Intents))

	for _, intent := range input.Intents {
		intent.Dependencies = cloneStrings(intent.Dependencies)
		intentBySlug[intent.Slug] = intent
		var run *Run
		if found, ok := latestRuns[intent.Slug]; ok {
			runCopy := found
			run = &runCopy
		}
		landing, hasLanding := landings[intent.Slug]
		var landingCopy *Landing
		if hasLanding {
			copy := landing
			landingCopy = &copy
		}
		kind := classify(intent, run, input.ClaimRunID, hasLanding)
		baseKinds[intent.Slug] = kind
		rows = append(rows, Row{Intent: intent, Status: kind, Run: run, Landing: landingCopy})
	}

	for index := range rows {
		row := &rows[index]
		if row.Status != Approved {
			continue
		}
		if reason := dependencyReason(row.Intent, intentBySlug, baseKinds); reason != "" {
			row.Status = Blocked
			row.WaitReason = reason
		}
	}

	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Intent.Slug < rows[j].Intent.Slug
	})
	queue := make([]Intent, 0, len(rows))
	for _, row := range rows {
		if row.Status == Approved {
			queue = append(queue, row.Intent)
		}
	}
	sort.Slice(queue, func(i, j int) bool {
		if queue[i].Priority != queue[j].Priority {
			return queue[i].Priority > queue[j].Priority
		}
		if queue[i].ApprovalTime != queue[j].ApprovalTime {
			return queue[i].ApprovalTime < queue[j].ApprovalTime
		}
		return queue[i].Slug < queue[j].Slug
	})
	ordered := make([]string, len(queue))
	for index, intent := range queue {
		ordered[index] = intent.Slug
	}
	return Board{Rows: rows, Queue: ordered}
}

func classify(intent Intent, run *Run, claimRunID string, hasLanding bool) Kind {
	if hasLanding {
		return Landed
	}
	if run != nil && isInterrupted(intent, *run) {
		return Interrupted
	}
	if run != nil && run.ID == claimRunID && run.Status == "running" {
		return Building
	}
	if intent.ApprovalCommit == "" {
		return Draft
	}
	if run != nil && run.ApprovalCommit == intent.ApprovalCommit {
		switch run.Status {
		case "failed":
			return Failed
		case "parked":
			return Parked
		}
	}
	return Approved
}

func isInterrupted(intent Intent, run Run) bool {
	if intent.ApprovalCommit == "" || run.ApprovalCommit != intent.ApprovalCommit {
		return false
	}
	if run.Status == "failed" && run.Reason == "interrupted" {
		return true
	}
	return run.Status == "running" && run.LastEvent == "interrupted" && !run.OwnerAlive
}

func latestRuns(runs []Run) map[string]Run {
	latest := make(map[string]Run)
	for _, run := range runs {
		prior, ok := latest[run.Slug]
		if !ok || run.StartedAt > prior.StartedAt || (run.StartedAt == prior.StartedAt && run.ID > prior.ID) {
			latest[run.Slug] = run
		}
	}
	return latest
}

func latestLandings(landings []Landing) map[string]Landing {
	latest := make(map[string]Landing)
	for _, landing := range landings {
		prior, ok := latest[landing.Slug]
		if !ok || landing.CommitTime > prior.CommitTime || (landing.CommitTime == prior.CommitTime && landing.Commit > prior.Commit) {
			latest[landing.Slug] = landing
		}
	}
	return latest
}

func dependencyReason(intent Intent, intents map[string]Intent, kinds map[string]Kind) string {
	if intent.SchedulingError != "" {
		return intent.SchedulingError
	}
	if len(intent.Dependencies) == 0 {
		return ""
	}

	invalid := make([]string, 0)
	unknown := make([]string, 0)
	for _, dependency := range intent.Dependencies {
		if !validSlug(dependency) {
			invalid = append(invalid, dependency)
			continue
		}
		if _, exists := intents[dependency]; !exists {
			unknown = append(unknown, dependency)
		}
	}
	if len(invalid) != 0 {
		return "invalid dependencies: " + joinSlugs(invalid)
	}
	if len(unknown) != 0 {
		return "unknown dependencies: " + joinSlugs(unknown)
	}
	if cycle := dependencyCycle(intent.Slug, intents, kinds); len(cycle) != 0 {
		return "dependency cycle: " + joinSlugs(cycle)
	}

	pending := make([]string, 0)
	for _, dependency := range intent.Dependencies {
		if kinds[dependency] != Landed {
			pending = append(pending, dependency)
		}
	}
	if len(pending) != 0 {
		return "waiting for delivered dependencies: " + joinSlugs(pending)
	}
	return ""
}

// dependencyCycle returns a deterministic cycle that contains start. A path
// leading into another cycle is a pending dependency, not itself a cycle.
func dependencyCycle(start string, intents map[string]Intent, kinds map[string]Kind) []string {
	path := []string{start}
	visiting := map[string]bool{start: true}
	visited := make(map[string]bool)
	var visit func(string) bool
	visit = func(slug string) bool {
		dependencies := cloneStrings(intents[slug].Dependencies)
		sort.Strings(dependencies)
		for _, dependency := range dependencies {
			if dependency == start {
				path = append(path, start)
				return true
			}
			kind := kinds[dependency]
			if kind != Approved && kind != Blocked {
				continue
			}
			if visiting[dependency] || visited[dependency] {
				continue
			}
			visiting[dependency] = true
			path = append(path, dependency)
			if visit(dependency) {
				return true
			}
			path = path[:len(path)-1]
			delete(visiting, dependency)
			visited[dependency] = true
		}
		return false
	}
	if visit(start) {
		return path
	}
	return nil
}

func validSlug(slug string) bool {
	if len(slug) < 3 || len(slug) > 48 {
		return false
	}
	previousDash := true
	for index := 0; index < len(slug); index++ {
		char := slug[index]
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9':
			previousDash = false
		case char == '-' && !previousDash && index != len(slug)-1:
			previousDash = true
		default:
			return false
		}
	}
	return !previousDash
}

func joinSlugs(slugs []string) string {
	return strings.Join(slugs, ", ")
}

func cloneStrings(values []string) []string {
	return append([]string(nil), values...)
}
