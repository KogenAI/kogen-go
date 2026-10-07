package findings

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"
	"time"

	"kogen-go/internal/contract"
)

func TestRenderGateFeedbackBoundsMessageAndKeepsFullArtifact(t *testing.T) {
	findings := make([]Finding, 22)
	for index := range findings {
		findings[index] = Finding{
			Path:    "src/a.go",
			Rule:    "lint/long",
			Message: strings.Repeat("é", 220) + " full diagnostic",
			Line:    ptr(uint32(index + 1)),
			Column:  ptr(1),
		}
	}
	rawLog := strings.Join([]string{
		"line zero",
		"line one",
		"/home/user/log path",
		"/tmp/run/log path",
		"line four",
		"line five",
		"line six",
		"line seven",
		"line eight",
		"line nine",
	}, "\n") + "\n"
	baseExit := 1
	feedback, err := RenderGateFeedback(FeedbackInput{
		RunID: "build-123",
		Checks: []FeedbackCheck{
			{
				Name: "lint", Program: "lint", Status: contract.CheckRed,
				Timeout: 3 * time.Second, Findings: findings,
				LogPath: "/tmp/run/logs/lint.log", RawLog: []byte(rawLog),
			},
			{Name: "ghost", Program: "ghost-tool", Status: contract.CheckUnavailable, LogPath: "/tmp/run/logs/ghost.log"},
			{
				Name: "baseline", Status: contract.CheckRed, Excused: true,
				Findings: []Finding{{Path: "src/base.go", Rule: "kt/test", Symbol: "base/A1", Message: "base failure"}},
			},
		},
		Baseline: []BaselineRow{
			{Name: "ghost", Status: contract.CheckGreen},
			{Name: "baseline", Status: contract.CheckRed, ExitCode: &baseExit, Findings: []Finding{{Path: "src/base.go", Rule: "kt/test", Symbol: "base/A1", Message: "base failure"}}},
		},
		Acceptance: []FeedbackAcceptance{{ID: "greet/A1", Passed: true}, {ID: "greet/A2", Status: "failed"}},
		RunDir:     "/tmp/run",
		Home:       "/home/user",
	})
	if err != nil {
		t.Fatal(err)
	}
	if feedback.ArtifactPath != "gate-findings-build-123.json" {
		t.Fatalf("unexpected artifact path %q", feedback.ArtifactPath)
	}
	for _, want := range []string{
		"check lint: Red",
		"src/a.go:1:1: error: [lint/long] " + strings.Repeat("é", 200),
		"… 12 more lint findings",
		"ghost-tool is not available, but it ran on the base",
		"raw log: /tmp/run/logs/lint.log",
		"raw tail (first failed step lint):\n$HOME/log path\n$TMPDIR/log path",
		"acceptance greet/A2: failed",
		"Base-red warning: check \"baseline\" still has only findings recorded at approval.",
		"gate: 2 errors, 1 warnings (lint 10); checks lint=red, ghost=unavailable, baseline=red; acceptance 1/2",
	} {
		if !strings.Contains(feedback.Text, want) {
			t.Errorf("feedback missing %q:\n%s", want, feedback.Text)
		}
	}
	if strings.HasSuffix(feedback.Text, "\n") || !strings.HasSuffix(feedback.Text, "acceptance 1/2") {
		t.Fatalf("final gate line must be last: %q", feedback.Text)
	}
	if strings.Contains(feedback.Text, "full diagnostic") {
		t.Fatal("bounded message unexpectedly included the untruncated suffix")
	}

	var artifact []struct {
		Check string `json:"check"`
		Finding
	}
	if err := json.Unmarshal(feedback.Artifact, &artifact); err != nil {
		t.Fatal(err)
	}
	if len(artifact) != 23 {
		t.Fatalf("artifact has %d findings, want all 23", len(artifact))
	}
	if artifact[0].Message != findings[0].Message || artifact[0].Line == nil || *artifact[0].Line != 1 {
		t.Fatalf("artifact lost full finding data: %#v", artifact[0])
	}
	if artifact[22].Check != "baseline" || artifact[22].Symbol != "base/A1" {
		t.Fatalf("artifact lost check or symbol identity: %#v", artifact[22])
	}

	root := &captureRoot{}
	if err := feedback.Publish(root); err != nil {
		t.Fatal(err)
	}
	if root.path != feedback.ArtifactPath || string(root.data) != string(feedback.Artifact) || root.mode != 0o600 || root.publication != contract.PublicationCreateOnly {
		t.Fatalf("unsafe or unexpected artifact publication: %#v", root)
	}
}

func TestFeedbackRawTailKeepsLastEightLinesAndCapsCharacters(t *testing.T) {
	lines := []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight"}
	var got []string
	appendRawTail(&got, "lint", []byte(strings.Join(lines, "\n")+"\n"), "", "")
	if len(got) != 1 || got[0] != "raw tail (first failed step lint):\none\ntwo\nthree\nfour\nfive\nsix\nseven\neight" {
		t.Fatalf("unexpected 8-line tail: %#v", got)
	}
	got = nil
	appendRawTail(&got, "lint", []byte(strings.Repeat("x", 650)), "", "")
	if len(got) != 1 || len([]rune(strings.TrimPrefix(got[0], "raw tail (first failed step lint):\n"))) != maxRawTailRunes {
		t.Fatalf("tail must be capped at %d characters", maxRawTailRunes)
	}
}

func TestRenderGateFeedbackRejectsUnsafeArtifactID(t *testing.T) {
	if _, err := RenderGateFeedback(FeedbackInput{RunID: "../escape"}); err == nil {
		t.Fatal("expected path-shaped artifact ID to be rejected")
	}
}

type captureRoot struct {
	path        string
	data        []byte
	mode        fs.FileMode
	publication contract.PublicationMode
}

func (*captureRoot) OpenRead(string) (io.ReadCloser, error) { return nil, errors.New("unused") }
func (*captureRoot) ReadFile(string) ([]byte, error)        { return nil, errors.New("unused") }
func (*captureRoot) ReadDir(string) ([]fs.DirEntry, error)  { return nil, errors.New("unused") }
func (*captureRoot) Lstat(string) (fs.FileInfo, error)      { return nil, errors.New("unused") }
func (*captureRoot) Readlink(string) (string, error)        { return "", errors.New("unused") }
func (*captureRoot) MkdirAll(string, fs.FileMode) error     { return errors.New("unused") }
func (root *captureRoot) Publish(path string, data []byte, mode fs.FileMode, publication contract.PublicationMode) error {
	root.path = path
	root.data = append([]byte(nil), data...)
	root.mode = mode
	root.publication = publication
	return nil
}
func (*captureRoot) Append(string, []byte, fs.FileMode) error { return errors.New("unused") }
func (*captureRoot) Remove(string) error                      { return errors.New("unused") }
func (*captureRoot) Rename(string, string) error              { return errors.New("unused") }
func (*captureRoot) Symlink(string, string) error             { return errors.New("unused") }
func (*captureRoot) SyncDir(string) error                     { return errors.New("unused") }
