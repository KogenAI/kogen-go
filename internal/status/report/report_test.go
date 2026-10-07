package report

import (
	"encoding/json"
	"math"
	"testing"

	"kogen-go/internal/status/derive"
)

func TestBuildReportAssemblesRungsAuditCacheAndBuildAccounting(t *testing.T) {
	row := derive.Row{
		Intent:  derive.Intent{Slug: "greet", Priority: 2, Dependencies: []string{"base"}},
		Status:  derive.Landed,
		Run:     &derive.Run{ID: "1234567890abcdef", Status: "landed", StartedAt: 1_000},
		Landing: &derive.Landing{Commit: "abcdef1234567890", CommitTime: 4_000},
	}
	status := Report{
		Board: derive.Board{Rows: []derive.Row{row}},
		Agents: []Agent{{ID: "0123456789abcdef0123456789abcdef", Role: "builder", Build: row.Run.ID,
			Status: "running", ElapsedMS: 9, Activity: "editing", Events: "/state/agents/events.jsonl"}},
		NowMS:     9_000,
		StateRoot: "/state",
		Approvals: map[string]any{"greet": map[string]any{"by": "A. User", "base_sha": "base-sha"}},
		Runs: map[string]RunData{
			row.Run.ID: {Events: []Event{
				{"event": "started", "ts": int64(1_000), "base_sha": "event-base", "budget_ms": int64(8_000),
					"credential_source": "file", "credential_label": "default", "sandbox": "confined"},
				{"event": "rung_started", "ts": int64(1_100), "rung": "R1", "model": "gpt-6-luna", "effort": "max",
					"entered_because": "initial", "wall_ms": int64(2_000)},
				{"event": "model_stage", "ts": int64(1_200), "rung": "R1", "stage": "builder", "wall_ms": int64(1_000),
					"tokens": map[string]any{"input": int64(60), "cached_input": int64(40), "cache_write": int64(1), "output": int64(8), "reasoning": int64(2)}},
				{"event": "audit", "ts": int64(2_100), "rung": "R1", "items": []any{
					map[string]any{"id": "A1", "verdict": "valid", "reason": "matches"},
				}},
				{"event": "verification", "ts": int64(2_200), "acceptance": []any{
					map[string]any{"id": "A1", "status": "pass", "demoted": false},
				}, "checks": []any{
					map[string]any{"name": "unit", "status": "green", "excused": false, "findings": []any{
						map[string]any{"type": "test", "path": "lib/a.go", "message": "ok"},
					}},
				}},
				{"event": "commit_result", "ts": int64(3_000), "candidate_commit": "candidate", "tree": "tree"},
				{"event": "rung_finished", "ts": int64(3_100), "rung": "R1", "reason": "green", "verdict": "green",
					"diff_lines": int64(4), "candidate_ref": "refs/kogen/candidate"},
				{"event": "selection", "ts": int64(3_200), "winner_rung": "R1"},
				{"event": "provider_wait", "ts": int64(3_300), "paused_ms": int64(500)},
				{"event": "finished", "ts": int64(4_000), "status": "landed", "verdict": "green", "advisory_items": []any{}, "failures": []any{}},
			}},
		},
	}
	got := Build(row, status)
	if got["status"] != "landed" || got["build_id"] != row.Run.ID || got["landed_sha"] != row.Landing.Commit {
		t.Fatalf("report identity fields are incorrect: %#v", got)
	}
	if got["approved_by"] != "A. User" || got["base"] != "base-sha" {
		t.Fatalf("approval fields are incorrect: %#v", got)
	}
	if got["land_policy"] != "green" || got["verdict"] != "green" {
		t.Fatalf("draft land policy or verdict missing: %#v", got)
	}
	if rate, ok := got["cache_hit_rate"].(float64); !ok || math.Abs(rate-0.4) > 1e-12 {
		t.Fatalf("cache_hit_rate = %#v, want 0.4", got["cache_hit_rate"])
	}
	rungs, ok := got["rungs"].([]map[string]any)
	if !ok || len(rungs) != 1 {
		t.Fatalf("rungs = %#v", got["rungs"])
	}
	if rungs[0]["reason"] != "green" || rungs[0]["candidate_ref"] != "refs/kogen/candidate" || rungs[0]["wall_ms"] != int64(2_000) {
		t.Fatalf("rung data = %#v", rungs[0])
	}
	tokens := rungs[0]["tokens"].(map[string]any)
	if tokens["input"] != int64(60) || tokens["cached_input"] != int64(40) {
		t.Fatalf("rung token totals = %#v", tokens)
	}
	best := got["best_candidate"].(map[string]any)
	if best["rung"] != "R1" || best["diff_path"] != "/state/runs/1234567890abcdef/candidate.diff" {
		t.Fatalf("best_candidate = %#v", best)
	}
	audit := got["audit"].([]map[string]any)
	if len(audit) != 1 || audit[0]["id"] != "A1" || audit[0]["rung"] != "R1" {
		t.Fatalf("audit = %#v", got["audit"])
	}
	if got["journal"] != "/state/runs/1234567890abcdef" || got["cache_hit_rate"] == nil {
		t.Fatalf("journal/cache fields missing: %#v", got)
	}
	budget := got["budget"].(map[string]any)
	if budget["budget_ms"] != int64(8_000) || budget["used_ms"] != int64(3_000) || budget["paused_ms"] != int64(500) {
		t.Fatalf("budget = %#v", budget)
	}
	agents, ok := got["agents"].([]map[string]any)
	if !ok || len(agents) != 1 || agents[0]["type"] != "agent" {
		t.Fatalf("agents = %#v", got["agents"])
	}
	encoded, err := JSON(row, status)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Build JSON is invalid: %v", err)
	}
	if decoded["slug"] != "greet" {
		t.Fatalf("serialized report slug = %#v", decoded["slug"])
	}
}

func TestBuildReportKeepsUnknownUsageNullAndOmitsEmptyAgents(t *testing.T) {
	row := derive.Row{Intent: derive.Intent{Slug: "draft-a"}, Status: derive.Draft}
	status := Report{
		NowMS: 4_000,
		Runs: map[string]RunData{
			"run": {Events: []Event{
				{"event": "started", "ts": int64(1_000)},
				{"event": "model_stage", "tokens": map[string]any{"input": int64(10), "cached_input": nil}},
			}},
		},
	}
	row.Run = &derive.Run{ID: "run", Status: "failed", StartedAt: 1_000}
	got := Build(row, status)
	if got["cache_hit_rate"] != nil {
		t.Fatalf("unknown usage was treated as a measured rate: %#v", got["cache_hit_rate"])
	}
	if _, exists := got["agents"]; exists {
		t.Fatal("empty agents field should be omitted")
	}
	stages := got["model_stages"].([]any)
	stage := stages[0].(map[string]any)
	if _, hasEnvelope := stage["event"]; hasEnvelope {
		t.Fatal("model stage report retained the event envelope")
	}
	if _, hasTimestamp := stage["ts"]; hasTimestamp {
		t.Fatal("model stage report retained the timestamp")
	}
}
