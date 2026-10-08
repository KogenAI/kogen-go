package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"kogen-go/internal/build/single"
	"kogen-go/internal/testkit"
)

func TestPublicApprovalQueueGateLandingAndStatus(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	seedIntent(t, fixture, "greet", validFoundationIntent("Greeting", 7))
	writeBuildRouteFixture(t, fixture)
	fixture.Run(t, "add", "-A")
	fixture.Run(t, "commit", "--message", "add approved Build fixture")
	fixture.Run(t, "push", "origin", "main")

	agent := &routeFakeAgent{}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cli := &CLI{
		CWD: fixture.Checkout, Env: fixtureEnvironment(fixture.Environment()),
		In: strings.NewReader(""), Out: stdout, Err: stderr, buildAgent: agent,
	}
	if code := cli.Run([]string{"intent", "approve", "greet"}); code != 5 {
		t.Fatalf("approval card exit = %d, output:\n%s", code, stdout.String())
	}
	hash := regexp.MustCompile(`(?m)^SHA-256: ([0-9a-f]{64})$`).FindStringSubmatch(stdout.String())
	if hash == nil {
		t.Fatalf("approval card has no digest:\n%s", stdout.String())
	}
	stdout.Reset()
	if code := cli.Run([]string{"intent", "approve", "greet", hash[1]}); code != 0 {
		t.Fatalf("approval publish exit = %d, output:\n%s", code, stdout.String())
	}
	stdout.Reset()
	if code := cli.Run([]string{"queue", "start"}); code != 0 {
		t.Fatalf("queue start exit = %d, output:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "building greet\n") || !strings.Contains(got, "landed greet ") || !strings.Contains(got, "queue: done; 1 Build(s), 1 landed, 0 not\n") {
		t.Fatalf("queue did not complete the real one-rung path:\n%s", got)
	}
	if agent.plans != 1 || agent.developments != 1 {
		t.Fatalf("fake model calls: plans=%d developments=%d, want one of each", agent.plans, agent.developments)
	}
	parentRow := strings.Fields(string(fixture.RunIn(t, fixture.Origin, "rev-list", "--parents", "-n", "1", "refs/heads/main")))
	if len(parentRow) != 2 {
		t.Fatalf("landed Build commit does not have exactly one base parent: %q", parentRow)
	}
	stdout.Reset()
	if code := cli.Run([]string{"status"}); code != 0 {
		t.Fatalf("status after landing exit = %d, output:\n%s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "Landed (1):\n  greet") {
		t.Fatalf("status did not observe the landed approval:\n%s", stdout.String())
	}
}

func TestQueueInvalidApprovalDoesNotCallTheAgent(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	seedIntent(t, fixture, "greet", validFoundationIntent("Greeting", 0))
	writeBuildRouteFixture(t, fixture)
	fixture.Run(t, "add", "-A")
	fixture.Run(t, "commit", "--message", "add approval fixture")
	fixture.Run(t, "push", "origin", "main")

	agent := &routeFakeAgent{}
	stdout := &bytes.Buffer{}
	cli := &CLI{
		CWD: fixture.Checkout, Env: fixtureEnvironment(fixture.Environment()),
		In: strings.NewReader(""), Out: stdout, Err: &bytes.Buffer{}, buildAgent: agent,
	}
	if code := cli.Run([]string{"intent", "approve", "greet"}); code != 5 {
		t.Fatalf("approval card exit = %d, output:\n%s", code, stdout.String())
	}
	hash := regexp.MustCompile(`(?m)^SHA-256: ([0-9a-f]{64})$`).FindStringSubmatch(stdout.String())
	if hash == nil {
		t.Fatalf("approval card has no digest:\n%s", stdout.String())
	}
	stdout.Reset()
	if code := cli.Run([]string{"intent", "approve", "greet", hash[1]}); code != 0 {
		t.Fatalf("approval publish exit = %d, output:\n%s", code, stdout.String())
	}

	approvalPath := filepath.Join(fixture.Checkout, ".kogen", "intents", "greet", "approval.json")
	if err := os.MkdirAll(filepath.Dir(approvalPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(approvalPath, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.Run(t, "add", ".kogen/intents/greet/approval.json")
	fixture.Run(t, "commit", "--message", "replace approval with malformed package")
	fixture.Run(t, "push", "origin", "main")
	current := strings.TrimSpace(string(fixture.Run(t, "rev-parse", "HEAD")))
	fixture.RunIn(t, fixture.Origin, "update-ref", "refs/kogen/intents/greet", current)

	stdout.Reset()
	if code := cli.Run([]string{"queue", "start"}); code != 70 {
		t.Fatalf("invalid approval drain exit = %d, want controller stop 70; output:\n%s", code, stdout.String())
	}
	if agent.plans != 0 || agent.developments != 0 {
		t.Fatalf("invalid approval reached provider: plans=%d developments=%d", agent.plans, agent.developments)
	}
	if !strings.Contains(stdout.String(), "stopped greet: controller/approval_invalid; it stays queued") {
		t.Fatalf("invalid approval was not refused at B0:\n%s", stdout.String())
	}
}

func writeBuildRouteFixture(t *testing.T, fixture *testkit.GitFixture) {
	t.Helper()
	projectConfig := "name: fixture\nchecks: []\nacceptance:\n  adapter: command\n  ext: .t.sh\n  candidate_dir: test/acceptance\n  run: [sh, \"{path}\"]\n"
	projectPath := filepath.Join(fixture.Checkout, ".kogen", "project.yaml")
	if err := os.MkdirAll(filepath.Dir(projectPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectPath, []byte(projectConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	candidateDirectory := filepath.Join(fixture.Checkout, "test", "acceptance")
	if err := os.MkdirAll(candidateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidateDirectory, ".keep"), []byte("fixture candidate directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	acceptancePath := filepath.Join(fixture.Checkout, ".kogen", "acceptance", "greet.t.sh")
	if err := os.MkdirAll(filepath.Dir(acceptancePath), 0o700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '{\"tag\":\"greet/A1\",\"test\":\"fixture\",\"status\":\"passed\"}\\n' >> \"$KOGEN_LEDGER_REPORT\"\n"
	if err := os.WriteFile(acceptancePath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
}

type routeFakeAgent struct {
	plans        int
	developments int
}

func (agent *routeFakeAgent) Plan(context.Context, single.PlanRequest) (single.PlanResult, error) {
	agent.plans++
	return single.PlanResult{Text: "Run the approved acceptance test and preserve its behavior.", Difficulty: "easy"}, nil
}

func (agent *routeFakeAgent) Develop(_ context.Context, request single.DevelopRequest) (single.Development, error) {
	agent.developments++
	if request.WorkspaceRoot == nil || request.Session == nil || request.Approval == nil {
		return single.Development{}, fmt.Errorf("fake builder did not receive the real workspace, session, and approval")
	}
	return single.Development{Completion: "finish", Turns: 1}, nil
}

var _ single.Agent = (*routeFakeAgent)(nil)
