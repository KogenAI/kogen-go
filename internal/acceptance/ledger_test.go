package acceptance

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"kogen-go/internal/contract"
)

func TestParseLedgerReportRequiresExactJSONLShape(t *testing.T) {
	valid := []byte("{\"tag\":\"greet/A1\",\"test\":\"test says hi\",\"status\":\"passed\"}\r\n")
	rows, err := ParseLedgerReport(valid)
	if err != nil || !reflect.DeepEqual(rows, []LedgerRow{{Tag: "greet/A1", Test: "test says hi", Status: LedgerPassed}}) {
		t.Fatalf("ParseLedgerReport = %#v, %v", rows, err)
	}

	for _, test := range []struct {
		name string
		line string
	}{
		{name: "trailing field", line: `{"tag":"greet/A1","test":"t","status":"passed","extra":true}`},
		{name: "duplicate field", line: `{"tag":"greet/A1","tag":"greet/A2","test":"t","status":"passed"}`},
		{name: "missing field", line: `{"tag":"greet/A1","status":"passed"}`},
		{name: "null field", line: `{"tag":null,"test":"t","status":"passed"}`},
		{name: "unknown status", line: `{"tag":"greet/A1","test":"t","status":"running"}`},
		{name: "array", line: `[]`},
		{name: "trailing value", line: `{"tag":"greet/A1","test":"t","status":"passed"} false`},
		{name: "blank line", line: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseLedgerReport([]byte(test.line + "\n"))
			var ledgerErr *LedgerReadError
			if !errors.As(err, &ledgerErr) || ledgerErr.Kind != LedgerMalformedLine || ledgerErr.Line != 1 {
				t.Fatalf("error = %#v, want malformed line 1", err)
			}
		})
	}
	if _, err := ParseLedgerReport([]byte{'{', 0xff, '}'}); err == nil {
		t.Fatal("invalid UTF-8 report was accepted")
	} else {
		var ledgerErr *LedgerReadError
		if !errors.As(err, &ledgerErr) || ledgerErr.Kind != LedgerInvalidUTF8 {
			t.Fatalf("invalid UTF-8 error = %#v", err)
		}
	}
	if rows, err := ParseLedgerReport(nil); err != nil || len(rows) != 0 {
		t.Fatalf("empty report = %#v, %v", rows, err)
	}
}

func TestAssessLedgerMatrix(t *testing.T) {
	status := func(value int) *int { return &value }
	passedA1 := `{"tag":"greet/A1","test":"one","status":"passed"}`
	passedA2 := `{"tag":"greet/A2","test":"two","status":"passed"}`
	withRows := func(lines ...string) []byte { return []byte(strings.Join(lines, "\n") + "\n") }
	contains := func(result Result, kind FailureKind) bool {
		for _, failure := range result.Failures {
			if failure.Kind == kind {
				return true
			}
		}
		return false
	}

	t.Run("each item needs at least one all-passed row", func(t *testing.T) {
		rows := withRows(passedA1, `{"tag":"greet/A1","test":"skipped","status":"skipped"}`, passedA2)
		result := Assess("greet", []string{"A1", "A2"}, contract.ProcessResult{ExitStatus: status(0)}, rows, nil, false, false, "tree", "tree")
		if !result.ItemPass["A2"] || result.ItemPass["A1"] {
			t.Fatalf("item passes = %#v", result.ItemPass)
		}
	})

	t.Run("unknown current-slug tag is a suite failure", func(t *testing.T) {
		rows := withRows(passedA1, passedA2, `{"tag":"greet/A9","test":"extra","status":"passed"}`)
		result := Assess("greet", []string{"A1", "A2"}, contract.ProcessResult{ExitStatus: status(0)}, rows, nil, false, false, "tree", "tree")
		if !contains(result, FailureSuite) {
			t.Fatalf("failures = %#v", result.Failures)
		}
	})

	t.Run("nonzero exit with every A item green adds suite failure", func(t *testing.T) {
		rows := withRows(passedA1, passedA2)
		result := Assess("greet", []string{"A1", "A2"}, contract.ProcessResult{ExitStatus: status(1)}, rows, nil, false, false, "tree", "tree")
		if !result.ItemPass["A1"] || !result.ItemPass["A2"] || !contains(result, FailureSuite) {
			t.Fatalf("nonzero/all-green result = %#v", result)
		}
	})

	t.Run("missing empty and malformed unavailable reports are tool missing", func(t *testing.T) {
		for _, report := range [][]byte{nil, {}, []byte("{bad}\n")} {
			result := Assess("greet", []string{"A1"}, contract.ProcessResult{ExitStatus: status(127), Unavailable: true}, report, nil, false, false, "tree", "tree")
			if !contains(result, FailureToolMissing) || contains(result, FailureNoTaggedTests) || contains(result, FailureLedgerInvalid) {
				t.Fatalf("report %q failures = %#v", report, result.Failures)
			}
		}
		missing := &LedgerReadError{Kind: LedgerMissingReport}
		result := Assess("greet", []string{"A1"}, contract.ProcessResult{ExitStatus: status(127)}, nil, missing, false, false, "tree", "tree")
		if !contains(result, FailureToolMissing) {
			t.Fatalf("missing report failures = %#v", result.Failures)
		}
	})

	t.Run("empty reports distinguish compile failure and no tagged tests", func(t *testing.T) {
		failed := Assess("greet", []string{"A1"}, contract.ProcessResult{ExitStatus: status(2)}, nil, nil, false, false, "tree", "tree")
		if !contains(failed, FailureAcceptanceCompileFailed) {
			t.Fatalf("failed runner failures = %#v", failed.Failures)
		}
		green := Assess("greet", []string{"A1"}, contract.ProcessResult{ExitStatus: status(0)}, nil, nil, false, false, "tree", "tree")
		if !contains(green, FailureNoTaggedTests) {
			t.Fatalf("empty successful runner failures = %#v", green.Failures)
		}
	})

	t.Run("malformed report and tree mutation are retained", func(t *testing.T) {
		result := Assess("greet", []string{"A1"}, contract.ProcessResult{ExitStatus: status(0)}, []byte("not-json\n"), nil, false, true, "before", "after")
		if !contains(result, FailureLedgerInvalid) || !contains(result, FailureTreeMutated) {
			t.Fatalf("failures = %#v", result.Failures)
		}
		var malformed *Failure
		for i := range result.Failures {
			if result.Failures[i].Kind == FailureLedgerInvalid {
				malformed = &result.Failures[i]
			}
		}
		if malformed == nil || malformed.Line != 1 {
			t.Fatalf("malformed row detail = %#v", malformed)
		}
	})

	t.Run("timeout remains a failure with a valid report", func(t *testing.T) {
		result := Assess("greet", []string{"A1"}, contract.ProcessResult{ExitStatus: status(124), TimedOut: true}, withRows(passedA1), nil, false, false, "tree", "tree")
		if !result.ItemPass["A1"] || !contains(result, FailureAcceptanceTimeout) || !contains(result, FailureSuite) {
			t.Fatalf("timeout result = %#v", result)
		}
	})
}

func TestSortedEnvironmentIsDeterministicAndRejectsNUL(t *testing.T) {
	got, err := SortedEnvironment(map[string]string{"Z": "last", "A": "first"})
	if err != nil || !reflect.DeepEqual(got, []string{"A=first", "Z=last"}) {
		t.Fatalf("SortedEnvironment = %#v, %v", got, err)
	}
	if _, err := SortedEnvironment(map[string]string{"BAD": "a\x00b"}); err == nil {
		t.Fatal("NUL environment value accepted")
	}
}

func TestNormalizeTimeoutUsesAcceptanceDefault(t *testing.T) {
	if got := NormalizeTimeout(0); got != 10*time.Minute {
		t.Fatalf("default acceptance timeout = %s", got)
	}
	if got := NormalizeTimeout(5); got != 5 {
		t.Fatalf("explicit timeout = %s", got)
	}
}
