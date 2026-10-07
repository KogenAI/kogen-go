package render

import (
	"fmt"
	"strings"
	"testing"

	"kogen-go/internal/status/derive"
	"kogen-go/internal/status/report"
)

func TestOverviewSectionsLandedWindowAndAgents(t *testing.T) {
	pid := 91
	rows := []derive.Row{
		{Intent: derive.Intent{Slug: "build-long"}, Status: derive.Building, Run: &derive.Run{ID: "12345678abcdef", Status: "running", StartedAt: 1_000}},
		{Intent: derive.Intent{Slug: "blocked-a"}, Status: derive.Blocked, WaitReason: "waiting for delivered dependencies: base-a"},
		{Intent: derive.Intent{Slug: "draft-a"}, Status: derive.Draft},
		{Intent: derive.Intent{Slug: "failed-a"}, Status: derive.Failed, Run: &derive.Run{ID: "aaaaaaaaaaaa", Status: "failed", Reason: "repair_cap"}},
		{Intent: derive.Intent{Slug: "parked-a"}, Status: derive.Parked, Run: &derive.Run{ID: "bbbbbbbbbbbb", Status: "parked", Reason: "landing_retries"}},
		{Intent: derive.Intent{Slug: "stopped-a"}, Status: derive.Interrupted, Run: &derive.Run{ID: "cccccccccccc", Status: "failed", Reason: "interrupted"}},
	}
	for i := 1; i <= 7; i++ {
		rows = append(rows, derive.Row{
			Intent:  derive.Intent{Slug: "land-" + string(rune('a'+i-1))},
			Status:  derive.Landed,
			Landing: &derive.Landing{Commit: "abcdef12" + string(rune('a'+i-1)), CommitTime: int64(i)},
		})
	}
	status := report.Report{
		Board: derive.Board{Rows: rows, Queue: []string{"queue-a", "queue-long"}},
		Agents: []report.Agent{{ID: strings.Repeat("a", 32), Role: "builder", Build: "build-id", Status: "running",
			ElapsedMS: 30, Activity: "editing", Events: "/state/agents/a/events.jsonl"}},
		QueuePID: &pid,
		NowMS:    4_000,
		Runs:     map[string]report.RunData{"12345678abcdef": {Events: []report.Event{{"event": "rung_started", "rung": "R2"}}}},
	}
	want := "Queue: running (pid 91)\n" +
		"Building:\n  build-long  R2, 3s (Build 12345678)\n" +
		"Queued:\n  queue-a\n  queue-long\n" +
		"Blocked:\n  blocked-a  waiting for delivered dependencies: base-a\n" +
		"Failed:\n  failed-a  repair_cap (Build aaaaaaaa)\n" +
		"Parked:\n  parked-a  landing_retries (Build bbbbbbbb)\n" +
		"Interrupted:\n  stopped-a  interrupted (Build cccccccc)\n" +
		"Drafts:\n  draft-a\n" +
		"Landed (7):\n  land-g  abcdef12\n  land-f  abcdef12\n  land-e  abcdef12\n  land-d  abcdef12\n  land-c  abcdef12\n  and 2 earlier\n" +
		"Agents:\n  " + strings.Repeat("a", 32) + " builder Build=build-id running elapsed_ms=30 editing\n    events: /state/agents/a/events.jsonl\n"
	if got := Overview(status); got != want {
		t.Fatalf("Overview() mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

func TestOverviewStoppedQueueIncludesNext(t *testing.T) {
	status := report.Report{
		Board: derive.Board{
			Rows:  []derive.Row{{Intent: derive.Intent{Slug: "needs-base", Priority: 4, Dependencies: []string{"base"}}, Status: derive.Approved}},
			Queue: []string{"needs-base"},
		},
	}
	want := "Queue: stopped, 1 waiting; start it with kogen queue start\n" +
		"Next: needs-base (priority 4; dependencies delivered; ties by approval time and slug)\n" +
		"Queued:\n  needs-base\n"
	if got := Overview(status); got != want {
		t.Fatalf("Overview() = %q, want %q", got, want)
	}
}

func TestJSONLinesUsesSlugOrderAndAppendsAgents(t *testing.T) {
	status := report.Report{
		Board: derive.Board{Rows: []derive.Row{
			{Intent: derive.Intent{Slug: "z-last", Priority: 3}, Status: derive.Draft},
			{Intent: derive.Intent{Slug: "a-first"}, Status: derive.Landed, Landing: &derive.Landing{Commit: "12345678abcdef"}},
		}},
		Agents: []report.Agent{{ID: strings.Repeat("f", 32), Role: "context", Build: "b2", Status: "waiting",
			ElapsedMS: 8, Activity: "wait", Events: "/events"}},
	}
	got, err := JSONLines(status)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"blocks_on\":[],\"build_id\":null,\"landed_sha\":\"12345678abcdef\",\"priority\":0,\"slug\":\"a-first\",\"status\":\"landed\"}\n" +
		"{\"blocks_on\":[],\"build_id\":null,\"landed_sha\":null,\"priority\":3,\"slug\":\"z-last\",\"status\":\"draft\"}\n" +
		"{\"activity\":\"wait\",\"build\":\"b2\",\"elapsed_ms\":8,\"events\":\"/events\",\"id\":\"" + strings.Repeat("f", 32) + "\",\"role\":\"context\",\"status\":\"waiting\",\"type\":\"agent\"}\n"
	if got != want {
		t.Fatalf("JSONLines() mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestSlugIncludesBuildFieldsWithoutVerdict(t *testing.T) {
	row := derive.Row{
		Intent: derive.Intent{Slug: "build-a"},
		Status: derive.Failed,
		Run:    &derive.Run{ID: "12345678abcdef", Status: "failed", Reason: "repair_cap", StartedAt: 1_000},
	}
	status := report.Report{
		StateRoot: "/state",
		NowMS:     5_000,
		Runs: map[string]report.RunData{
			row.Run.ID: {
				CandidateDiffPath: "/state/runs/12345678abcdef/candidate.diff",
				Events: []report.Event{
					{"event": "model_stage", "stage": "builder", "wall_ms": float64(2_500)},
					{"event": "setup_reused", "saved_wall_ms": 300},
					{"event": "context_continuation"},
					{"event": "phase_timing", "phase": "gate", "wall_ms": float64(1_500)},
					{"event": "check_proposal", "paths": []any{"go test ./...", "make check"}},
					{"event": "verification", "acceptance": []any{
						map[string]any{"id": "A1", "status": "pass"},
						map[string]any{"id": "A2", "status": "failed"},
					}},
				},
			},
		},
	}
	got := Slug(row, status)
	want := "build-a: failed, repair_cap\n" +
		"Build 12345678: failed, repair_cap\n" +
		"  model time: builder 2s\n" +
		"  setup: reused (saved preparation 300 ms)\n" +
		"  context continuations: 1 (same approved Build; checkpoints in journal)\n" +
		"  gate: 1s\n" +
		"  candidate checks (caller approval required): go test ./..., make check\n" +
		"  acceptance verified: A1\n" +
		"  acceptance remaining: A2\n" +
		"  candidate diff: /state/runs/12345678abcdef/candidate.diff\n" +
		"  journal: /state/runs/12345678abcdef\n"
	if got != want {
		t.Fatalf("Slug() mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
	}
	if strings.Contains(got, "verdict:") {
		t.Fatalf("Slug() included forbidden verdict line: %s", got)
	}
}

func TestWatcherOnlyEmitsChangedFramesAndIdleExit(t *testing.T) {
	var watcher Watcher
	if frame, changed := watcher.Next("one\n"); !changed || frame != "one\n" {
		t.Fatalf("first frame = %q, changed %v", frame, changed)
	}
	if frame, changed := watcher.Next("one\n"); changed || frame != "" {
		t.Fatalf("same frame = %q, changed %v", frame, changed)
	}
	if frame, changed := watcher.Next("two\n"); !changed || frame != "\ntwo\n" {
		t.Fatalf("changed frame = %q, changed %v", frame, changed)
	}
	row := derive.Row{Intent: derive.Intent{Slug: "done"}, Status: derive.Landed}
	idle := report.Report{Board: derive.Board{Rows: []derive.Row{row}}}
	if !Idle(idle) || WatchExitCode(idle, "done") != 0 || WatchExitCode(idle, "pending") != 1 {
		t.Fatal("idle watch completion or slug exit code is wrong")
	}
	active := idle
	active.Agents = []report.Agent{{Status: "waiting"}}
	if Idle(active) {
		t.Fatal("watch terminated while an agent was waiting")
	}
	pid := 12
	active.Agents = nil
	active.QueuePID = &pid
	if Idle(active) {
		t.Fatal("watch terminated while the queue was running")
	}
}

func BenchmarkStatusRender50Intents200Runs(b *testing.B) {
	rows := make([]derive.Row, 50)
	runs := make(map[string]report.RunData, 200)
	for i := 0; i < 200; i++ {
		id := fmt.Sprintf("run-%03d", i)
		runs[id] = report.RunData{Events: []report.Event{
			{"event": "rung_started", "rung": "R1"},
			{"event": "model_stage", "stage": "builder", "wall_ms": float64(15_000)},
		}}
	}
	queue := make([]string, 0, 13)
	for i := range rows {
		slug := fmt.Sprintf("intent-%02d", i)
		switch i % 4 {
		case 0:
			id := fmt.Sprintf("run-%03d", i)
			rows[i] = derive.Row{
				Intent: derive.Intent{Slug: slug},
				Status: derive.Building,
				Run:    &derive.Run{ID: id, Status: "running", StartedAt: 1_000},
			}
		case 1:
			queue = append(queue, slug)
			rows[i] = derive.Row{Intent: derive.Intent{Slug: slug}, Status: derive.Approved}
		case 2:
			rows[i] = derive.Row{Intent: derive.Intent{Slug: slug}, Status: derive.Draft}
		default:
			rows[i] = derive.Row{Intent: derive.Intent{Slug: slug}, Status: derive.Blocked, WaitReason: "waiting for delivered dependencies: base"}
		}
	}
	status := report.Report{
		Board:     derive.Board{Rows: rows, Queue: queue},
		NowMS:     60_000,
		StateRoot: "/state",
		Runs:      runs,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Overview(status)
		if _, err := JSONLines(status); err != nil {
			b.Fatal(err)
		}
	}
}
