package app

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"kogen-go/internal/process"
	"kogen-go/internal/testkit"
)

func TestHelpVersionAndParserRoutes(t *testing.T) {
	root := t.TempDir()
	cli := &CLI{CWD: root, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	if code := cli.Run(nil); code != 0 {
		t.Fatalf("help exit = %d", code)
	}
	if got := cli.Out.(*bytes.Buffer).String(); got != "Commands:\n  status     Show the queue, Builds and Intents\n  intent     Shape, approve or remove an Intent\n  queue      Build approved Intents one at a time\n  provider   Manage Kogen's provider logins\n  version    Show the Kogen version\n  help       List commands\n\nIntent shaping defaults to gpt-6.1-sol at high effort; an explicit build.roles.shaper\nin project or machine config overrides this default.\n\nRun kogen <command> to see its subcommands and options.\n" {
		t.Fatalf("top-level help changed:\n%s", got)
	}

	stdout := &bytes.Buffer{}
	cli = &CLI{CWD: root, Out: stdout, Err: &bytes.Buffer{}}
	if code := cli.Run([]string{"version"}); code != 0 {
		t.Fatalf("version exit = %d", code)
	}
	if !regexp.MustCompile(`^kogen ([0-9a-f]{8}|unknown) \([0-9]{4}-[0-9]{2}-[0-9]{2}(, uncommitted changes)?\)\n$`).MatchString(stdout.String()) {
		t.Fatalf("version format = %q", stdout.String())
	}

	stdout.Reset()
	cli = &CLI{CWD: root, Out: stdout, Err: &bytes.Buffer{}}
	if code := cli.Run([]string{"status", "--watch", "--json"}); code != 2 {
		t.Fatalf("parser usage exit = %d", code)
	}
	if !strings.HasPrefix(stdout.String(), "kogen status: --watch and --json can't be combined\n\nUsage: kogen status") {
		t.Fatalf("parser usage output = %q", stdout.String())
	}
}

func TestRealApprovalCardPublishStatusAndDraftRemoval(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	seedIntent(t, fixture, "greet", validFoundationIntent("Greeting", 7))
	seedIntent(t, fixture, "draft-item", validFoundationIntent("Draft", 0))
	fixture.Run(t, "add", "-A")
	fixture.Run(t, "commit", "--message", "add review intents")

	env := fixtureEnvironment(fixture.Environment())
	stdout := &bytes.Buffer{}
	cli := &CLI{CWD: fixture.Checkout, Env: env, In: strings.NewReader(""), Out: stdout, Err: &bytes.Buffer{}}
	if code := cli.Run([]string{"intent", "approve", "greet"}); code != 5 {
		t.Fatalf("card exit = %d, output:\n%s", code, stdout.String())
	}
	card := stdout.String()
	if !strings.Contains(card, "Intent: greet — Greeting\n") || !strings.Contains(card, "Acceptance\n  - [A1] The greeting names the user. (test)\n") {
		t.Fatalf("approval card omitted parsed Intent content:\n%s", card)
	}
	hash := regexp.MustCompile(`(?m)^SHA-256: ([0-9a-f]{64})$`).FindStringSubmatch(card)
	if hash == nil {
		t.Fatalf("card has no exact approval digest:\n%s", card)
	}

	stdout.Reset()
	if code := cli.Run([]string{"intent", "approve", "greet", hash[1]}); code != 0 {
		t.Fatalf("approval publish exit = %d, output:\n%s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "approved greet "+hash[1][:8]+" (approval ") || !strings.Contains(stdout.String(), "Next: kogen queue start") {
		t.Fatalf("approval publish output = %q", stdout.String())
	}
	if got := strings.TrimSpace(string(fixture.RunIn(t, fixture.Origin, "for-each-ref", "--format=%(refname)", "refs/kogen/intents/greet"))); got != "refs/kogen/intents/greet" {
		t.Fatalf("approval ref = %q", got)
	}

	stdout.Reset()
	if code := cli.Run([]string{"status"}); code != 0 {
		t.Fatalf("synthetic status exit = %d, output:\n%s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "Next: greet (priority 7; no dependencies; ties by approval time and slug)\n") || !strings.Contains(stdout.String(), "Drafts:\n  draft-item\n") {
		t.Fatalf("status did not combine queued and draft Intents:\n%s", stdout.String())
	}

	stdout.Reset()
	if code := cli.Run([]string{"intent", "remove", "draft-item"}); code != 0 {
		t.Fatalf("draft remove exit = %d, output:\n%s", code, stdout.String())
	}
	if !strings.HasPrefix(stdout.String(), "removed: draft-item\ncommit: ") {
		t.Fatalf("draft removal output = %q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(fixture.Checkout, ".kogen", "intents", "draft-item", "intent.md")); !os.IsNotExist(err) {
		t.Fatalf("draft Intent still exists after removal: %v", err)
	}
}

func TestApprovalRejectsHashMismatchBeforeSetupAndResolvesProjectOption(t *testing.T) {
	fixture := testkit.NewGitFixture(t)
	seedIntent(t, fixture, "greet", validFoundationIntent("Greeting", 0))
	fixture.Run(t, "add", "-A")
	fixture.Run(t, "commit", "--message", "add intent")
	root := filepath.Dir(fixture.Checkout)
	link := filepath.Join(root, "project-link")
	if err := os.Symlink(fixture.Checkout, link); err != nil {
		t.Fatal(err)
	}
	stdout := &bytes.Buffer{}
	cli := &CLI{
		CWD: root, Env: fixtureEnvironment(fixture.Environment()), Out: stdout, Err: &bytes.Buffer{},
	}
	if code := cli.Run([]string{"intent", "approve", "greet", "000000", "--project", link}); code != 1 {
		t.Fatalf("hash mismatch exit = %d, output:\n%s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "intent/hash_mismatch: greet is now ") {
		t.Fatalf("hash mismatch output = %q", stdout.String())
	}
}

func seedIntent(t *testing.T, fixture *testkit.GitFixture, slug string, contents string) {
	t.Helper()
	intentPath := filepath.Join(fixture.Checkout, ".kogen", "intents", slug, "intent.md")
	acceptancePath := filepath.Join(fixture.Checkout, ".kogen", "acceptance", slug+".t.sh")
	if err := os.MkdirAll(filepath.Dir(intentPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(acceptancePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(intentPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(acceptancePath, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func validFoundationIntent(title string, priority int) string {
	return "---\n" +
		"title: " + title + "\n" +
		"size: small\n" +
		"domains: [app]\n" +
		"priority: " + strconv.Itoa(priority) + "\n" +
		"---\n" +
		"The greeting names the user.\n\n" +
		"## Acceptance\n" +
		"- A1: The greeting names the user.\n\n" +
		"## Verify\n" +
		"- A1: test\n"
}

func fixtureEnvironment(entries []string) process.Environment {
	environment := make(process.Environment, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			environment[key] = value
		}
	}
	return environment
}
