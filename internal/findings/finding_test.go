package findings

import (
	"strings"
	"testing"

	"kogen-go/internal/contract"
)

func TestParseGNUAndStableIdentity(t *testing.T) {
	findings := ParseGNU([]byte(strings.Join([]string{
		"lib/greet.txt:2:1: error: [lint/todo] greet.txt: TODO found",
		"test/unit/greet.t.sh:1:1: error: [kt/test] alpha: assertion failed",
		"C:/work:repo/test.ex:7:8: warning: [exunit/failure] named test: mismatch",
		"test/unit/greet.t.sh:2: note: [kt/test] informational",
		"not a GNU line",
	}, "\n")))
	if len(findings) != 4 {
		t.Fatalf("got %d findings, want 4", len(findings))
	}
	if findings[0].Symbol != "" || findings[0].Message != "greet.txt: TODO found" {
		t.Fatalf("non-test message was split: %#v", findings[0])
	}
	if findings[1].Symbol != "alpha" || findings[1].Message != "assertion failed" {
		t.Fatalf("test symbol was not parsed: %#v", findings[1])
	}
	if findings[2].Path != "C:/work:repo/test.ex" || findings[2].Symbol != "named test" {
		t.Fatalf("colon path or test symbol parsed incorrectly: %#v", findings[2])
	}
	if findings[3].Symbol != "" || findings[3].Message != "informational" {
		t.Fatalf("test rule without symbol separator was split: %#v", findings[3])
	}

	first := findings[1]
	changedPositionAndText := first
	line, column := uint32(99), uint32(13)
	changedPositionAndText.Line = &line
	changedPositionAndText.Column = &column
	changedPositionAndText.Message = "different diagnostic"
	if first.Identity() != changedPositionAndText.Identity() {
		t.Fatal("line, column, and message must not affect identity")
	}
	if first.Identity() == findings[2].Identity() {
		t.Fatal("different test symbols must have different identities")
	}
}

func TestAcceptanceIdentityKeepsItemSymbol(t *testing.T) {
	first := AcceptanceIdentity("test/acceptance/greet.t.sh", "greet/A1")
	second := AcceptanceIdentity("test/acceptance/greet.t.sh", "greet/A2")
	if first.Path != second.Path || first.Rule != "acceptance" || first.Symbol != "greet/A1" {
		t.Fatalf("unexpected acceptance identity: %#v", first)
	}
	if first == second {
		t.Fatal("acceptance item symbol must distinguish same-file items")
	}
}

func TestIsExcusedUsesIdentitySubsetThenExitFallback(t *testing.T) {
	base := Finding{Path: "lib/a.txt", Rule: "lint/todo", Message: "old", Line: ptr(uint32(2))}.Identity()
	same := Finding{Path: "lib/a.txt", Rule: "lint/todo", Message: "new", Line: ptr(uint32(9))}.Identity()
	newSymbol := contract.FindingIdentity{Path: "lib/a.txt", Rule: "kt/test", Symbol: "new test"}
	baseExit, sameExit, otherExit := 1, 1, 2
	if !IsExcused(contract.CheckRed, contract.CheckRed, &baseExit, &sameExit, []contract.FindingIdentity{base}, []contract.FindingIdentity{same}) {
		t.Fatal("same identity with changed location/message should be excused")
	}
	if IsExcused(contract.CheckRed, contract.CheckRed, &baseExit, &sameExit, []contract.FindingIdentity{base}, []contract.FindingIdentity{base, newSymbol}) {
		t.Fatal("new identity should block base-red excusal")
	}
	if !IsExcused(contract.CheckRed, contract.CheckRed, &baseExit, &sameExit, nil, []contract.FindingIdentity{same}) {
		t.Fatal("missing baseline identities should use equal exit-status fallback")
	}
	if IsExcused(contract.CheckGreen, contract.CheckGreen, &baseExit, &sameExit, []contract.FindingIdentity{base}, []contract.FindingIdentity{same}) {
		t.Fatal("green baseline must never be excused")
	}
	if IsExcused(contract.CheckRed, contract.CheckUnavailable, &baseExit, &sameExit, nil, nil) {
		t.Fatal("different statuses must not be excused")
	}
	if IsExcused(contract.CheckRed, contract.CheckRed, &baseExit, &otherExit, nil, nil) {
		t.Fatal("empty identity sets require matching exit status")
	}
}

func TestApprovalBaselineWarningFormatting(t *testing.T) {
	rows := []BaselineRow{
		{
			Name:   "lint",
			Status: contract.CheckRed,
			Findings: []Finding{{
				Path: "lib/greet.txt", Rule: "lint/todo", Message: "greet.txt: TODO found", Line: ptr(uint32(2)),
			}},
		},
		{Name: "ghost", Status: contract.CheckUnavailable},
	}
	if !HasRedBaseline(rows) {
		t.Fatal("red baseline was not detected")
	}
	got := BaseRedWarningBlock(rows)
	want := "Warning: configured checks are already red on the base:\n" +
		"  - lint: [lint/todo] lib/greet.txt:2: greet.txt: TODO found\n" +
		"Hint: fix the base first, or scope the check, e.g. a changed-files format argv."
	if got != want {
		t.Fatalf("warning block mismatch:\n got: %q\nwant: %q", got, want)
	}
	if lines := BaselineStatusWarningLines(rows); len(lines) != 1 || lines[0] != "  - baseline_unavailable: ghost — configured check did not pass on the base" {
		t.Fatalf("unexpected status warning lines: %#v", lines)
	}
	if BaseRedWarningBlock([]BaselineRow{{Name: "lint", Status: contract.CheckGreen}}) != "" {
		t.Fatal("green baseline should not show a red warning block")
	}
}

func ptr(value uint32) *uint32 { return &value }
