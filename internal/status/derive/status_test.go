package derive

import (
	"reflect"
	"testing"
)

func TestStatusPrecedenceAndCurrentApproval(t *testing.T) {
	input := Input{
		Intents: []Intent{
			{Slug: "draft-one"},
			{Slug: "landed-one", ApprovalCommit: "approval-new"},
			{Slug: "building-one", ApprovalCommit: "approval-build"},
			{Slug: "failed-one", ApprovalCommit: "approval-fail"},
			{Slug: "parked-one", ApprovalCommit: "approval-park"},
			{Slug: "stopped-one", ApprovalCommit: "approval-stop"},
			{Slug: "reapproved-one", ApprovalCommit: "approval-new"},
		},
		Runs: []Run{
			{ID: "a", Slug: "building-one", ApprovalCommit: "approval-build", Status: "running", StartedAt: 10},
			{ID: "b", Slug: "building-one", ApprovalCommit: "approval-build", Status: "running", StartedAt: 11},
			{ID: "c", Slug: "failed-one", ApprovalCommit: "approval-fail", Status: "failed", Reason: "check_failed", StartedAt: 12},
			{ID: "d", Slug: "parked-one", ApprovalCommit: "approval-park", Status: "parked", Reason: "not_landable", StartedAt: 13},
			{ID: "e", Slug: "stopped-one", ApprovalCommit: "approval-stop", Status: "stopped", Reason: "login_required", StartedAt: 14},
			{ID: "f", Slug: "reapproved-one", ApprovalCommit: "approval-old", Status: "failed", Reason: "check_failed", StartedAt: 15},
		},
		Landings:   []Landing{{Slug: "landed-one", Commit: "landed-commit", CommitTime: 1}},
		ClaimRunID: "b",
	}
	board := Derive(input)
	want := map[string]Kind{
		"draft-one":      Draft,
		"landed-one":     Landed,
		"building-one":   Building,
		"failed-one":     Failed,
		"parked-one":     Parked,
		"stopped-one":    Approved,
		"reapproved-one": Approved,
	}
	for _, row := range board.Rows {
		if row.Status != want[row.Intent.Slug] {
			t.Errorf("%s status = %q, want %q", row.Intent.Slug, row.Status, want[row.Intent.Slug])
		}
	}
	if len(board.Queue) != 2 {
		t.Fatalf("queue = %#v, want stopped-one and reapproved-one", board.Queue)
	}
}

func TestClaimMustNameLatestRun(t *testing.T) {
	intent := Intent{Slug: "greet-one", ApprovalCommit: "approval"}
	board := Derive(Input{
		Intents: []Intent{intent},
		Runs: []Run{
			{ID: "latest", Slug: intent.Slug, ApprovalCommit: "approval", Status: "running", StartedAt: 2},
			{ID: "older", Slug: intent.Slug, ApprovalCommit: "approval", Status: "running", StartedAt: 1},
		},
		ClaimRunID: "older",
	})
	if got := board.Rows[0].Status; got != Approved {
		t.Fatalf("status with a claim for a non-latest run = %q, want approved", got)
	}
}

func TestInterruptedOnlyOverridesForCurrentApproval(t *testing.T) {
	input := Input{
		Intents: []Intent{
			{Slug: "dead-one", ApprovalCommit: "current"},
			{Slug: "live-one", ApprovalCommit: "current"},
			{Slug: "stale-one", ApprovalCommit: "current"},
			{Slug: "failed-one", ApprovalCommit: "current"},
			{Slug: "old-fail-one", ApprovalCommit: "current"},
		},
		Runs: []Run{
			{ID: "dead", Slug: "dead-one", ApprovalCommit: "current", Status: "running", LastEvent: "interrupted", StartedAt: 1},
			{ID: "live", Slug: "live-one", ApprovalCommit: "current", Status: "running", LastEvent: "interrupted", OwnerAlive: true, StartedAt: 2},
			{ID: "stale", Slug: "stale-one", ApprovalCommit: "old", Status: "running", LastEvent: "interrupted", StartedAt: 3},
			{ID: "failed", Slug: "failed-one", ApprovalCommit: "current", Status: "failed", Reason: "interrupted", StartedAt: 4},
			{ID: "old-fail", Slug: "old-fail-one", ApprovalCommit: "old", Status: "failed", Reason: "interrupted", StartedAt: 5},
		},
		ClaimRunID: "live",
	}
	board := Derive(input)
	want := map[string]Kind{
		"dead-one":     Interrupted,
		"live-one":     Building,
		"stale-one":    Approved,
		"failed-one":   Interrupted,
		"old-fail-one": Approved,
	}
	for _, row := range board.Rows {
		if row.Status != want[row.Intent.Slug] {
			t.Errorf("%s status = %q, want %q", row.Intent.Slug, row.Status, want[row.Intent.Slug])
		}
	}
}

func TestDependenciesBlockInvalidUnknownCyclesAndPending(t *testing.T) {
	input := Input{
		Intents: []Intent{
			{Slug: "invalid-one", ApprovalCommit: "a", Dependencies: []string{"BAD"}},
			{Slug: "unknown-one", ApprovalCommit: "a", Dependencies: []string{"missing-one"}},
			{Slug: "cycle-one", ApprovalCommit: "a", Dependencies: []string{"cycle-two"}},
			{Slug: "cycle-two", ApprovalCommit: "b", Dependencies: []string{"cycle-one"}},
			{Slug: "pending-one", ApprovalCommit: "a", Dependencies: []string{"wait-for-one"}},
			{Slug: "wait-for-one", ApprovalCommit: "b"},
			{Slug: "delivered-one", ApprovalCommit: "a", Dependencies: []string{"landed-one"}},
			{Slug: "landed-one"},
			{Slug: "schedule-one", ApprovalCommit: "a", SchedulingError: "invalid dependencies: parse error"},
		},
		Landings: []Landing{{Slug: "landed-one", Commit: "c", CommitTime: 1}},
	}
	board := Derive(input)
	want := map[string]struct {
		status Kind
		reason string
	}{
		"invalid-one":   {Blocked, "invalid dependencies: BAD"},
		"unknown-one":   {Blocked, "unknown dependencies: missing-one"},
		"cycle-one":     {Blocked, "dependency cycle: cycle-one, cycle-two, cycle-one"},
		"cycle-two":     {Blocked, "dependency cycle: cycle-two, cycle-one, cycle-two"},
		"pending-one":   {Blocked, "waiting for delivered dependencies: wait-for-one"},
		"wait-for-one":  {Approved, ""},
		"delivered-one": {Approved, ""},
		"landed-one":    {Landed, ""},
		"schedule-one":  {Blocked, "invalid dependencies: parse error"},
	}
	for _, row := range board.Rows {
		wantRow := want[row.Intent.Slug]
		if row.Status != wantRow.status || row.WaitReason != wantRow.reason {
			t.Errorf("%s = (%q, %q), want (%q, %q)", row.Intent.Slug, row.Status, row.WaitReason, wantRow.status, wantRow.reason)
		}
	}
	if !reflect.DeepEqual(board.Queue, []string{"delivered-one", "wait-for-one"}) {
		t.Fatalf("queue = %#v, want only unblocked approved rows", board.Queue)
	}
}

func TestQueueOrderIsPriorityApprovalTimeThenSlug(t *testing.T) {
	board := Derive(Input{Intents: []Intent{
		{Slug: "later-one", ApprovalCommit: "a", Priority: 3, ApprovalTime: 20},
		{Slug: "first-one", ApprovalCommit: "a", Priority: 4, ApprovalTime: 30},
		{Slug: "same-z", ApprovalCommit: "a", Priority: 4, ApprovalTime: 10},
		{Slug: "same-a", ApprovalCommit: "a", Priority: 4, ApprovalTime: 10},
		{Slug: "blocked-one", ApprovalCommit: "a", Priority: 100, Dependencies: []string{"not-landed"}},
	}})
	want := []string{"same-a", "same-z", "first-one", "later-one"}
	if !reflect.DeepEqual(board.Queue, want) {
		t.Fatalf("queue = %#v, want %#v", board.Queue, want)
	}
	for index := 1; index < len(board.Rows); index++ {
		if board.Rows[index-1].Intent.Slug > board.Rows[index].Intent.Slug {
			t.Fatalf("rows are not slug sorted: %#v", board.Rows)
		}
	}
}

func TestReachableTrailerKeepsReusedSlugLanded(t *testing.T) {
	board := Derive(Input{
		Intents:  []Intent{{Slug: "greet-one", ApprovalCommit: "new-approval"}},
		Landings: []Landing{{Slug: "greet-one", Commit: "old-landed-commit", CommitTime: 10}},
	})
	row := board.Rows[0]
	if row.Status != Landed || row.Landing == nil || row.Landing.Commit != "old-landed-commit" {
		t.Fatalf("reused slug row = %#v, want landed from the reachable old trailer", row)
	}
}

func TestLatestLandingUsesNewestReachableCommit(t *testing.T) {
	board := Derive(Input{
		Intents: []Intent{{Slug: "landed-one"}},
		Landings: []Landing{
			{Slug: "landed-one", Commit: "older", CommitTime: 10},
			{Slug: "landed-one", Commit: "newer", CommitTime: 20},
		},
	})
	if got := board.Rows[0].Landing.Commit; got != "newer" {
		t.Fatalf("selected landing commit = %q, want newer", got)
	}
}

func TestValidSlug(t *testing.T) {
	for _, slug := range []string{"abc", "a-1", "one-two-three"} {
		if !validSlug(slug) {
			t.Errorf("validSlug(%q) = false", slug)
		}
	}
	for _, slug := range []string{"ab", "-abc", "abc-", "a--b", "UPPER", "has space"} {
		if validSlug(slug) {
			t.Errorf("validSlug(%q) = true", slug)
		}
	}
}
