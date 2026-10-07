// Package acceptance implements runner adapters and acceptance-ledger
// interpretation. It reports observations; gate policy belongs to callers.
package acceptance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"kogen-go/internal/contract"
)

// LedgerStatus is the runner-observed outcome of one executed test.
type LedgerStatus string

const (
	LedgerPassed   LedgerStatus = "passed"
	LedgerFailed   LedgerStatus = "failed"
	LedgerSkipped  LedgerStatus = "skipped"
	LedgerExcluded LedgerStatus = "excluded"
	LedgerInvalid  LedgerStatus = "invalid"
)

// LedgerRow has the exact three-field shape written by acceptance runners.
type LedgerRow struct {
	Tag    string       `json:"tag"`
	Test   string       `json:"test"`
	Status LedgerStatus `json:"status"`
}

// ParseLedgerReport decodes strict UTF-8 JSON Lines. Each line must be one
// object with exactly tag, test, and status string fields.
func ParseLedgerReport(data []byte) ([]LedgerRow, error) {
	if !utf8.Valid(data) {
		return nil, &LedgerReadError{Kind: LedgerInvalidUTF8}
	}
	if len(data) == 0 {
		return []LedgerRow{}, nil
	}
	lines := bytes.Split(data, []byte{'\n'})
	if len(lines) != 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	rows := make([]LedgerRow, 0, len(lines))
	for i, raw := range lines {
		line := bytes.TrimSuffix(raw, []byte{'\r'})
		row, err := parseLedgerRow(line)
		if err != nil {
			return nil, &LedgerReadError{Kind: LedgerMalformedLine, Line: i + 1, Detail: err.Error()}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func parseLedgerRow(line []byte) (LedgerRow, error) {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return LedgerRow{}, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return LedgerRow{}, fmt.Errorf("row must be a JSON object")
	}
	values := make(map[string]string, 3)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return LedgerRow{}, err
		}
		key, ok := token.(string)
		if !ok {
			return LedgerRow{}, fmt.Errorf("object key must be a string")
		}
		if key != "tag" && key != "test" && key != "status" {
			return LedgerRow{}, fmt.Errorf("unknown field %q", key)
		}
		if _, exists := values[key]; exists {
			return LedgerRow{}, fmt.Errorf("duplicate field %q", key)
		}
		valueToken, err := decoder.Token()
		if err != nil {
			return LedgerRow{}, err
		}
		value, ok := valueToken.(string)
		if !ok {
			return LedgerRow{}, fmt.Errorf("field %q must be a string", key)
		}
		values[key] = value
	}
	token, err = decoder.Token()
	if err != nil {
		return LedgerRow{}, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '}' {
		return LedgerRow{}, fmt.Errorf("row object is not closed")
	}
	if token, err = decoder.Token(); err != io.EOF {
		if err == nil {
			return LedgerRow{}, fmt.Errorf("unexpected trailing JSON token %v", token)
		}
		return LedgerRow{}, err
	}
	for _, key := range []string{"tag", "test", "status"} {
		if _, exists := values[key]; !exists {
			return LedgerRow{}, fmt.Errorf("missing field %q", key)
		}
	}
	row := LedgerRow{Tag: values["tag"], Test: values["test"], Status: LedgerStatus(values["status"])}
	switch row.Status {
	case LedgerPassed, LedgerFailed, LedgerSkipped, LedgerExcluded, LedgerInvalid:
		return row, nil
	default:
		return LedgerRow{}, fmt.Errorf("unknown status %q", row.Status)
	}
}

// LedgerReadError describes a report decoding failure. Line is one-based for
// malformed rows and zero for report-wide failures.
type LedgerReadErrorKind string

const (
	LedgerInvalidUTF8   LedgerReadErrorKind = "invalid_utf8"
	LedgerMalformedLine LedgerReadErrorKind = "malformed_line"
	LedgerUnsafeReport  LedgerReadErrorKind = "unsafe_report"
	LedgerMissingReport LedgerReadErrorKind = "missing_report"
)

type LedgerReadError struct {
	Kind   LedgerReadErrorKind
	Line   int
	Detail string
}

func (e *LedgerReadError) Error() string {
	if e == nil {
		return "ledger report is invalid"
	}
	switch e.Kind {
	case LedgerInvalidUTF8:
		return "ledger is not UTF-8"
	case LedgerUnsafeReport:
		return "ledger report is not a regular file"
	case LedgerMissingReport:
		return "ledger report is missing"
	case LedgerMalformedLine:
		return fmt.Sprintf("malformed ledger row on line %d: %s", e.Line, e.Detail)
	default:
		return "ledger report is invalid"
	}
}

// FailureKind names acceptance-run failures derived from process, report, and
// candidate-tree observations.
type FailureKind string

const (
	FailureToolMissing             FailureKind = "tool_missing"
	FailureAcceptanceCompileFailed FailureKind = "acceptance_compile_failed"
	FailureNoTaggedTests           FailureKind = "no_tagged_tests"
	FailureLedgerInvalid           FailureKind = "ledger_invalid"
	FailureAcceptanceTimeout       FailureKind = "acceptance_timeout"
	FailureTreeMutated             FailureKind = "tree_mutated"
	FailureSuite                   FailureKind = "suite"
)

// Failure retains a malformed report line where one is available.
type Failure struct {
	Kind   FailureKind
	Line   int
	Detail string
}

// Result contains all acceptance rows and the pass bit for each expected A<n>
// item. An item passes only when at least one matching row exists and every
// matching row is passed.
type Result struct {
	Process    contract.ProcessResult
	Rows       []LedgerRow
	ItemPass   map[string]bool
	Failures   []Failure
	TreeBefore string
	TreeAfter  string
}

// Assess combines the runner report with process and tree observations.
// Missing/empty reports are interpreted according to the runner outcome;
// unknown tags for this Intent and a nonzero runner exit with all items green
// add a suite failure.
func Assess(slug string, expectedItems []string, process contract.ProcessResult, report []byte, reportErr error, adapterUnavailable, treeMutated bool, treeBefore, treeAfter string) Result {
	expected := make(map[string]struct{}, len(expectedItems))
	for _, item := range expectedItems {
		expected[item] = struct{}{}
	}
	r := Result{Process: process, ItemPass: make(map[string]bool, len(expected)), TreeBefore: treeBefore, TreeAfter: treeAfter}
	if process.TimedOut {
		r.Failures = append(r.Failures, Failure{Kind: FailureAcceptanceTimeout})
	}
	if treeMutated {
		r.Failures = append(r.Failures, Failure{Kind: FailureTreeMutated})
	}

	runnerUnavailable := adapterUnavailable || process.Unavailable || process.ExitStatus == nil || isUnavailableExit(process.ExitStatus)
	var rows []LedgerRow
	var parseErr error
	if reportErr != nil {
		parseErr = reportErr
	} else {
		rows, parseErr = ParseLedgerReport(report)
	}
	switch {
	case parseErr == nil && len(rows) > 0:
		r.Rows = rows
	case runnerUnavailable:
		r.Failures = append(r.Failures, Failure{Kind: FailureToolMissing})
	case parseErr == nil || isMissingReport(parseErr):
		if process.ExitStatus != nil && *process.ExitStatus != 0 {
			r.Failures = append(r.Failures, Failure{Kind: FailureAcceptanceCompileFailed})
		} else {
			r.Failures = append(r.Failures, Failure{Kind: FailureNoTaggedTests})
		}
	default:
		failure := Failure{Kind: FailureLedgerInvalid, Detail: parseErr.Error()}
		var ledgerErr *LedgerReadError
		if asLedgerError(parseErr, &ledgerErr) && ledgerErr.Kind == LedgerMalformedLine {
			failure.Line = ledgerErr.Line
			failure.Detail = ledgerErr.Detail
		}
		r.Failures = append(r.Failures, failure)
	}

	if len(r.Rows) > 0 {
		prefix := slug + "/"
		unknownTag := false
		for _, row := range r.Rows {
			if strings.HasPrefix(row.Tag, prefix) {
				if _, exists := expected[strings.TrimPrefix(row.Tag, prefix)]; !exists {
					unknownTag = true
				}
			}
		}
		for _, item := range expectedItems {
			tag := prefix + item
			seen := false
			passed := true
			for _, row := range r.Rows {
				if row.Tag != tag {
					continue
				}
				seen = true
				if row.Status != LedgerPassed {
					passed = false
				}
			}
			r.ItemPass[item] = seen && passed
		}
		if unknownTag {
			r.Failures = append(r.Failures, Failure{Kind: FailureSuite})
		}
	}
	if len(r.Rows) == 0 {
		for _, item := range expectedItems {
			r.ItemPass[item] = false
		}
	}
	if process.ExitStatus != nil && *process.ExitStatus != 0 && len(expected) > 0 && allItemsPass(expectedItems, r.ItemPass) {
		r.Failures = appendFailureOnce(r.Failures, Failure{Kind: FailureSuite})
	}
	return r
}

func isUnavailableExit(status *int) bool { return status != nil && (*status == 126 || *status == 127) }

func allItemsPass(items []string, statuses map[string]bool) bool {
	if len(items) == 0 {
		return false
	}
	for _, item := range items {
		if !statuses[item] {
			return false
		}
	}
	return true
}

func appendFailureOnce(failures []Failure, value Failure) []Failure {
	for _, existing := range failures {
		if existing.Kind == value.Kind {
			return failures
		}
	}
	return append(failures, value)
}

func isMissingReport(err error) bool {
	var ledgerErr *LedgerReadError
	return errors.As(err, &ledgerErr) && ledgerErr.Kind == LedgerMissingReport
}

func asLedgerError(err error, target **LedgerReadError) bool {
	return errors.As(err, target)
}
