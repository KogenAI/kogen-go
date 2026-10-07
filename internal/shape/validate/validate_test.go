package validate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kogen-go/internal/contract"
)

const validGenerated = "---\ntitle: Preserve shaped behavior\nsize: small\ndomains: [app]\n---\nPreserve the requested behavior.\n\n## Acceptance\n- A1: The generated behavior is checked.\n\n## Verify\n- A1: test\n\n## Notes\napproach: Validate generated files and restore the adapter candidate path after checks run.\n"

type testAdapter struct {
	paths        Paths
	base         map[string]bool
	baseCalls    int
	baseSource   string
	baseItems    []string
	mutateSource bool
}

func (a *testAdapter) Paths(string) (Paths, error) { return a.paths, nil }
func (a *testAdapter) BaseResults(_ context.Context, checkout, source string, itemIDs []string) (map[string]bool, error) {
	a.baseCalls++
	a.baseSource = source
	a.baseItems = append([]string(nil), itemIDs...)
	if _, err := os.Lstat(filepath.Join(checkout, filepath.FromSlash(a.paths.Candidate))); !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("candidate path was not restored before base run: %v", err)
	}
	if a.mutateSource {
		if err := os.WriteFile(filepath.Join(checkout, filepath.FromSlash(source)), []byte("mutated source"), 0o640); err != nil {
			return nil, err
		}
	}
	return cloneResults(a.base), nil
}

type testSetup struct{ called func(string) error }

func (s testSetup) Run(_ context.Context, checkout string) error {
	if s.called != nil {
		return s.called(checkout)
	}
	return nil
}

type testFormatter struct {
	unavailable bool
	paths       []string
	err         error
}

func (f *testFormatter) Format(_ context.Context, _ string, paths []string) (bool, error) {
	f.paths = append([]string(nil), paths...)
	return f.unavailable, f.err
}

type testChecks struct {
	run func(string, string) ([]contract.CheckResult, error)
}

func (c *testChecks) Run(_ context.Context, checkout, candidate string) ([]contract.CheckResult, error) {
	return c.run(checkout, candidate)
}

type filesystemTrees struct{}

func (filesystemTrees) Snapshot(_ context.Context, checkout string) (string, error) {
	hash := sha256.New()
	err := filepath.WalkDir(checkout, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == checkout {
			return nil
		}
		relative, err := filepath.Rel(checkout, current)
		if err != nil {
			return err
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%o\x00", filepath.ToSlash(relative), info.Mode())
		if info.Mode().IsRegular() {
			contents, err := os.ReadFile(current)
			if err != nil {
				return err
			}
			_, _ = hash.Write(contents)
		} else if info.Mode()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(current)
			if err != nil {
				return err
			}
			_, _ = hash.Write([]byte(target))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type constantTrees struct{}

func (constantTrees) Snapshot(context.Context, string) (string, error) { return "same", nil }

func TestNormalizePreservesRawRequestAndDropsModelRequest(t *testing.T) {
	request := append([]byte("raw request\r\nlegacy bytes: "), 0xff, 0x00, '\n', 0xc3)
	generated := []byte(validGenerated + "\n## Request\nmodel request must disappear\n")
	got, failure := Normalize(generated, request)
	if failure != nil {
		t.Fatal(failure)
	}
	wantSuffix := append([]byte("## Request\n"), request...)
	if !bytes.HasSuffix(got, wantSuffix) {
		t.Fatalf("raw Request changed: suffix=%q", got[len(got)-len(wantSuffix):])
	}
	if bytes.Contains(got, []byte("model request must disappear")) {
		t.Fatal("model Request section was retained")
	}
	if !bytes.Contains(got, []byte("## Notes\nApproach: Validate generated files")) {
		t.Fatalf("Approach was not normalized: %q", got)
	}
}

func TestReclassifyPreservesLineEndingsAndOpaqueRequest(t *testing.T) {
	prefix := []byte("---\r\ntitle: Preserve shaped behavior\r\nsize: small\r\ndomains: [app]\r\n---\r\nPreserve requested behavior.\r\n\r\n## Acceptance\r\n- A1: The behavior is checked.\r\n- A2: Existing behavior remains.\r\n\r\n## Verify\r\n- A1: test domain=app\r\n- A2: test keep after=A1\r\n\r\n## Notes\r\nApproach: Validate generated behavior and preserve the existing adapter contract.\r\n\r\n## Request\r\n")
	source := append(bytes.Clone(prefix), 0xff, '\r', '\n', 0x00)
	got, warnings, red, failure := Reclassify("greet", source, map[string]bool{"A1": true, "A2": false})
	if failure != nil {
		t.Fatal(failure)
	}
	if !red {
		t.Fatal("A2 should remain a red change item")
	}
	if !bytes.Contains(got, []byte("- A1: test keep domain=app\r\n")) || !bytes.Contains(got, []byte("- A2: test after=A1\r\n")) {
		t.Fatalf("Verify entries were not reclassified: %q", got)
	}
	if !bytes.HasSuffix(got, source[len(prefix):]) {
		t.Fatal("reclassification changed opaque Request bytes")
	}
	if !bytes.Contains(got, []byte("Approach: Validate generated behavior and preserve the existing adapter contract.\r\n\r\n## Request\n")) {
		t.Fatalf("reclassification changed the existing Request separator: %q", got)
	}
	if len(warnings) != 2 || warnings[0].Message != "A2 was reclassified as test" || warnings[1].Message != "A1 was reclassified as test keep" {
		t.Fatalf("warnings = %#v", warnings)
	}
}

func TestPrepareCleanupPrecedesSetupAndValidateRestoresAdapterPath(t *testing.T) {
	f := makeFixture(t, map[string]bool{"A1": false})
	ledger := filepath.Join(f.checkout, ".kogen", "intents", "greet", ledgerArtifact)
	warnings := filepath.Join(f.checkout, ".kogen", "intents", "greet", warningsArtifact)
	if err := os.WriteFile(ledger, []byte("old ledger"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(warnings, []byte("old warnings"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.validator.setup = testSetup{called: func(checkout string) error {
		for _, stale := range []string{ledger, warnings} {
			if _, err := os.Lstat(stale); !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("stale artifact remains: %v", err)
			}
		}
		if checkout != f.checkout {
			return fmt.Errorf("setup checkout=%q", checkout)
		}
		return nil
	}}
	if err := f.validator.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.writeGenerated()
	f.formatter.unavailable = true
	f.checks.run = func(checkout, candidate string) ([]contract.CheckResult, error) {
		staged, err := os.ReadFile(filepath.Join(checkout, filepath.FromSlash(candidate)))
		if err != nil {
			return nil, fmt.Errorf("candidate not staged: %w", err)
		}
		if !bytes.Equal(staged, f.sourceBytes) {
			return nil, errors.New("staged test bytes changed")
		}
		return greenCheck(checkout, candidate)
	}
	request := append([]byte("Keep raw bytes\r\n"), 0xff, 0x00, '\n')
	f.validator.request = request
	outcome, err := f.validator.Validate(context.Background(), 1, "shaper", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Validated || outcome.Failure != nil {
		t.Fatalf("outcome=%#v", outcome)
	}
	if !bytes.HasSuffix(outcome.IntentBytes, append([]byte("## Request\n"), request...)) {
		t.Fatal("validation did not retain raw Request bytes")
	}
	if !bytes.Contains(outcome.IntentBytes, []byte("## Notes\nApproach: Validate")) {
		t.Fatal("Notes Approach was not normalized")
	}
	candidate := filepath.Join(f.checkout, filepath.FromSlash(f.adapter.paths.Candidate))
	if _, err := os.Lstat(candidate); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("staged path not restored: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(f.checkout, filepath.FromSlash(f.adapter.paths.Source))); err != nil || !bytes.Equal(got, f.sourceBytes) {
		t.Fatalf("adapter source changed: %q %v", got, err)
	}
	if f.adapter.baseCalls != 1 || f.adapter.baseSource != f.adapter.paths.Source || strings.Join(f.adapter.baseItems, ",") != "A1" {
		t.Fatalf("base call=%d %q %v", f.adapter.baseCalls, f.adapter.baseSource, f.adapter.baseItems)
	}
	if len(outcome.Warnings) != 1 || outcome.Warnings[0].Code != "formatter_unavailable" {
		t.Fatalf("warnings=%#v", outcome.Warnings)
	}
	if len(outcome.Progress) != 1 || outcome.Progress[0] != "shaper pass=1 role=shaper warning formatter_unavailable" {
		t.Fatalf("progress=%#v", outcome.Progress)
	}
	if strings.Join(f.formatter.paths, ",") != f.validator.intentPath+","+f.adapter.paths.Source {
		t.Fatalf("formatter paths=%#v", f.formatter.paths)
	}
}

func TestAllItemsKeepWritesReclassificationAndRestoresStagedPath(t *testing.T) {
	f := makeFixture(t, map[string]bool{"A1": true})
	if err := f.validator.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.writeGenerated()
	outcome, err := f.validator.Validate(context.Background(), 1, "shaper", 2)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Failure == nil || outcome.Failure.Reason != "all_items_keep" {
		t.Fatalf("failure=%#v", outcome.Failure)
	}
	if !bytes.Contains(outcome.IntentBytes, []byte("- A1: test keep\n")) {
		t.Fatalf("Intent=%q", outcome.IntentBytes)
	}
	if len(outcome.Warnings) != 1 || outcome.Warnings[0].Message != "A1 was reclassified as test keep" {
		t.Fatalf("warnings=%#v", outcome.Warnings)
	}
	if _, err := os.Lstat(filepath.Join(f.checkout, filepath.FromSlash(f.adapter.paths.Candidate))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("staged path remains: %v", err)
	}
	stored, err := os.ReadFile(filepath.Join(f.checkout, filepath.FromSlash(f.validator.intentPath)))
	if err != nil || !bytes.Contains(stored, []byte("- A1: test keep\n")) {
		t.Fatalf("reclassified Intent not written: %q %v", stored, err)
	}
}

func TestStyleFindingsRepairBeforeAcceptanceAndWarnAfterAllowance(t *testing.T) {
	f := makeFixture(t, map[string]bool{"A1": false})
	if err := f.validator.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.writeGeneratedBytes([]byte(strings.Replace(validGenerated, "Preserve the requested behavior.", "Preserve the requested behavior in order to keep it clear.", 1)))
	first, err := f.validator.Validate(context.Background(), 1, "shaper", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.StyleRepair) == 0 || first.Failure != nil || f.adapter.baseCalls != 0 {
		t.Fatalf("style findings did not request a repair before acceptance: %#v", first)
	}
	last, err := f.validator.Validate(context.Background(), 1, "shaper", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !last.Validated || len(last.StyleRepair) != 0 {
		t.Fatalf("final style pass did not continue: %#v", last)
	}
	found := false
	for _, warning := range last.Warnings {
		if warning.Code == "lint_banned_phrase" && strings.Contains(warning.Message, "in order to") {
			found = true
		}
	}
	if !found {
		t.Fatalf("remaining style finding was not retained as a warning: %#v", last.Warnings)
	}
}

func TestUnavailableAcceptanceCheckIsReportedAndStagedPathIsRemoved(t *testing.T) {
	f := makeFixture(t, map[string]bool{"A1": false})
	if err := f.validator.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.writeGenerated()
	f.checks.run = func(string, string) ([]contract.CheckResult, error) {
		status := 127
		return []contract.CheckResult{{Name: "compile", Status: contract.CheckUnavailable, ExitStatus: &status, Unavailable: true}}, nil
	}
	outcome, err := f.validator.Validate(context.Background(), 1, "shaper", 2)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Failure == nil || outcome.Failure.Reason != "acceptance_check_unavailable" || outcome.Failure.Detail != "acceptance check compile is unavailable" {
		t.Fatalf("failure=%#v", outcome.Failure)
	}
	if _, err := os.Lstat(filepath.Join(f.checkout, filepath.FromSlash(f.adapter.paths.Candidate))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("staged candidate remains after unavailable check: %v", err)
	}
}

func TestStagedPathConflictPreservesExistingPath(t *testing.T) {
	f := makeFixture(t, map[string]bool{"A1": false})
	if err := f.validator.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.writeGenerated()
	candidate := filepath.Join(f.checkout, filepath.FromSlash(f.adapter.paths.Candidate))
	if err := os.MkdirAll(filepath.Dir(candidate), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte("user-owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.checks.run = func(string, string) ([]contract.CheckResult, error) {
		t.Fatal("checks ran after path conflict")
		return nil, nil
	}
	outcome, err := f.validator.Validate(context.Background(), 1, "shaper", 2)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Failure == nil || outcome.Failure.Reason != "acceptance_check_path_conflict" {
		t.Fatalf("failure=%#v", outcome.Failure)
	}
	got, err := os.ReadFile(candidate)
	if err != nil || string(got) != "user-owned" {
		t.Fatalf("existing candidate changed: %q %v", got, err)
	}
}

func TestStagedCheckMutationIsRejectedAndCandidateIsRemoved(t *testing.T) {
	f := makeFixture(t, map[string]bool{"A1": false})
	f.validator.trees = constantTrees{}
	if err := f.validator.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.writeGenerated()
	f.checks.run = func(checkout, candidate string) ([]contract.CheckResult, error) {
		if err := os.WriteFile(filepath.Join(checkout, filepath.FromSlash(candidate)), []byte("mutated staged test"), 0o640); err != nil {
			return nil, err
		}
		return greenCheck(checkout, "")
	}
	outcome, err := f.validator.Validate(context.Background(), 1, "shaper", 2)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Failure == nil || outcome.Failure.Reason != "tree_mutated" {
		t.Fatalf("failure=%#v outcome=%#v", outcome.Failure, outcome)
	}
	if _, err := os.Lstat(filepath.Join(f.checkout, filepath.FromSlash(f.adapter.paths.Candidate))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("candidate remains: %v", err)
	}
}

func TestBaseAdapterCannotMutateIgnoredAcceptanceSource(t *testing.T) {
	f := makeFixture(t, map[string]bool{"A1": false})
	f.validator.trees = constantTrees{}
	if err := f.validator.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.writeGenerated()
	f.adapter.mutateSource = true
	outcome, err := f.validator.Validate(context.Background(), 1, "shaper", 2)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Failure == nil || outcome.Failure.Reason != "tree_mutated" {
		t.Fatalf("failure=%#v", outcome.Failure)
	}
}

func TestGatePathIsRejectedBeforeFormatter(t *testing.T) {
	f := makeFixture(t, map[string]bool{"A1": false})
	f.validator.gatePaths = []string{"Makefile"}
	if err := f.validator.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	generated := strings.Replace(validGenerated, "approach: Validate", "approach: Update Makefile and validate", 1)
	f.writeGeneratedBytes([]byte(generated))
	outcome, err := f.validator.Validate(context.Background(), 1, "shaper", 2)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Failure == nil || outcome.Failure.Reason != "undeclared_gate_path" || !strings.Contains(outcome.Failure.Detail, "Makefile") {
		t.Fatalf("failure=%#v", outcome.Failure)
	}
	if len(f.formatter.paths) != 0 {
		t.Fatal("formatter ran before gate validation")
	}
}

func TestSynRejectedSourcesProduceExactParserRepairFeedback(t *testing.T) {
	cases := []struct{ file, slug, key string }{
		{"syn-06-rejected-intent.md", "syn-06-migration-ticket-numbers", "title"},
		{"syn-20-rejected-intent.md", "syn-20-email-invite-flow", "size"},
	}
	for _, test := range cases {
		t.Run(test.slug, func(t *testing.T) {
			generated, err := os.ReadFile(filepath.Join("..", "prompts", "testdata", test.file))
			if err != nil {
				t.Fatal(err)
			}
			normalized, failure := Normalize(generated, []byte("benchmark request"))
			if failure != nil {
				t.Fatal(failure)
			}
			_, failure = parseAndLint(test.slug, normalized)
			want := "line 2: frontmatter is missing required key `" + test.key + "`"
			if failure == nil || failure.Reason != "intent_parse_failed" || failure.Detail != want {
				t.Fatalf("failure=%#v want=%q", failure, want)
			}
			if got, expected := failure.Error(), "candidate/intent_parse_failed: "+want; got != expected {
				t.Fatalf("feedback=%q want=%q", got, expected)
			}
		})
	}
}

type fixture struct {
	t           *testing.T
	checkout    string
	validator   *Validator
	adapter     *testAdapter
	formatter   *testFormatter
	checks      *testChecks
	sourceBytes []byte
}

func makeFixture(t *testing.T, base map[string]bool) *fixture {
	t.Helper()
	checkout := t.TempDir()
	intentDir := filepath.Join(checkout, ".kogen", "intents", "greet")
	sourceDir := filepath.Join(checkout, ".kogen", "acceptance")
	for _, dir := range []string{intentDir, sourceDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sourceBytes := []byte("adapter bytes stay exact\r\n\xff\x00\n")
	source := ".kogen/acceptance/greet.case-source"
	if err := os.WriteFile(filepath.Join(checkout, filepath.FromSlash(source)), sourceBytes, 0o640); err != nil {
		t.Fatal(err)
	}
	adapter := &testAdapter{paths: Paths{Source: source, Candidate: "tmp/acceptance/greet.custom-case"}, base: cloneResults(base)}
	formatter := &testFormatter{}
	checks := &testChecks{run: greenCheck}
	validator, err := New(Options{Checkout: checkout, Slug: "greet", Request: []byte("request"), Adapter: adapter, Setup: testSetup{}, Formatter: formatter, Checks: checks, Trees: filesystemTrees{}})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, checkout: checkout, validator: validator, adapter: adapter, formatter: formatter, checks: checks, sourceBytes: sourceBytes}
}

func (f *fixture) writeGenerated() { f.writeGeneratedBytes([]byte(validGenerated)) }
func (f *fixture) writeGeneratedBytes(contents []byte) {
	f.validator.request = []byte("request")
	output := filepath.Join(f.checkout, filepath.FromSlash(f.validator.intentPath))
	if err := os.WriteFile(output, contents, 0o644); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			f.t.Fatal(err)
			return
		}
		if mkdirErr := os.MkdirAll(filepath.Dir(output), 0o755); mkdirErr != nil {
			f.t.Fatal(mkdirErr)
			return
		}
		if writeErr := os.WriteFile(output, contents, 0o644); writeErr != nil {
			f.t.Fatal(writeErr)
		}
	}
}

func greenCheck(string, string) ([]contract.CheckResult, error) {
	return []contract.CheckResult{{Name: "syntax", Status: contract.CheckGreen, ExitStatus: intPointer(0)}}, nil
}
func cloneResults(source map[string]bool) map[string]bool {
	result := make(map[string]bool, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
func intPointer(value int) *int { return &value }
