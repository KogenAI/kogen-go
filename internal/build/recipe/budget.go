package recipe

import "time"

const (
	DefaultBuildWall = 60 * time.Minute
	MaxStageWall     = 30 * time.Minute
	LandingAllowance = 10 * time.Minute
)

type BudgetReport struct {
	BudgetMS int64
	UsedMS   int64
	PausedMS int64
}

// BuildBudget measures one Build wall. Provider waits pause its clock. The
// landing repair allowance starts independently and pauses for waits too.
type BuildBudget struct {
	budget            time.Duration
	started           time.Time
	pausedSince       time.Time
	paused            time.Duration
	isPaused          bool
	landingStarted    time.Time
	landingPauseBase  time.Duration
	landingHasStarted bool
}

func NewBuildBudget(start time.Time, budget time.Duration) BuildBudget {
	if budget <= 0 {
		budget = DefaultBuildWall
	}
	return BuildBudget{budget: budget, started: start}
}

func (b *BuildBudget) Pause(now time.Time) bool {
	if b.isPaused {
		return false
	}
	if now.Before(b.started) {
		now = b.started
	}
	b.pausedSince, b.isPaused = now, true
	return true
}

func (b *BuildBudget) Resume(now time.Time) bool {
	if !b.isPaused {
		return false
	}
	if now.After(b.pausedSince) {
		b.paused += now.Sub(b.pausedSince)
	}
	b.isPaused = false
	b.pausedSince = time.Time{}
	return true
}

func (b *BuildBudget) BeginLanding(now time.Time) bool {
	if b.landingHasStarted {
		return false
	}
	b.landingHasStarted = true
	b.landingStarted = now
	b.landingPauseBase = b.totalPaused(now)
	return true
}

func (b BuildBudget) Used(now time.Time) time.Duration {
	end := now
	if b.isPaused && b.pausedSince.Before(end) {
		end = b.pausedSince
	}
	elapsed := elapsed(b.started, end)
	return positive(elapsed - b.paused)
}

func (b BuildBudget) Paused(now time.Time) time.Duration {
	return b.totalPaused(now)
}

func (b BuildBudget) Remaining(now time.Time) time.Duration {
	return positive(b.budget - b.Used(now))
}

func (b BuildBudget) StageWall(now time.Time) time.Duration {
	return minDuration(b.Remaining(now), MaxStageWall)
}

func (b BuildBudget) Exhausted(now time.Time) bool {
	return b.Remaining(now) == 0
}

func (b BuildBudget) LandingRemaining(now time.Time) (time.Duration, bool) {
	if !b.landingHasStarted {
		return 0, false
	}
	pausedAfterStart := positive(b.totalPaused(now) - b.landingPauseBase)
	used := positive(elapsed(b.landingStarted, now) - pausedAfterStart)
	return positive(LandingAllowance - used), true
}

func (b BuildBudget) Report(now time.Time) BudgetReport {
	return BudgetReport{
		BudgetMS: b.budget.Milliseconds(),
		UsedMS:   b.Used(now).Milliseconds(),
		PausedMS: b.Paused(now).Milliseconds(),
	}
}

func (b BuildBudget) totalPaused(now time.Time) time.Duration {
	paused := b.paused
	if b.isPaused && now.After(b.pausedSince) {
		paused += now.Sub(b.pausedSince)
	}
	return paused
}

func elapsed(start, end time.Time) time.Duration {
	if end.Before(start) {
		return 0
	}
	return end.Sub(start)
}

func positive(duration time.Duration) time.Duration {
	if duration < 0 {
		return 0
	}
	return duration
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
