// Package testkit provides hermetic test fixtures. It is never imported by
// production command wiring.
package testkit

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fixtureGitTimeout = 20 * time.Second

// GitFixture is an isolated bare origin and clean checkout with an explicit
// local identity. All global and system Git configuration is disabled.
type GitFixture struct {
	Root     string
	Home     string
	Origin   string
	Checkout string
	env      []string
}

// NewGitFixture creates a temporary origin and pushes one deterministic seed
// commit. Test commits use a repository-local identity and disabled signing.
func NewGitFixture(t testing.TB) *GitFixture {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o700); err != nil {
		t.Fatalf("create isolated home: %v", err)
	}
	fixture := &GitFixture{
		Root:     root,
		Home:     home,
		Origin:   filepath.Join(root, "origin.git"),
		Checkout: filepath.Join(root, "checkout"),
		env:      hermeticGitEnvironment(home),
	}
	fixture.mustGit(t, root, "init", "--bare", "--initial-branch=main", fixture.Origin)
	fixture.mustGit(t, root, "clone", "--quiet", fixture.Origin, fixture.Checkout)
	for _, pair := range [][2]string{
		{"user.name", "Kogen Test Fixture"},
		{"user.email", "kogen-test-fixture@example.invalid"},
		{"commit.gpgsign", "false"},
		{"core.hooksPath", "/dev/null"},
		{"init.defaultBranch", "main"},
	} {
		fixture.mustGit(t, fixture.Checkout, "config", "--local", pair[0], pair[1])
	}
	if err := os.WriteFile(filepath.Join(fixture.Checkout, "README.md"), []byte("fixture seed\n"), 0o644); err != nil {
		t.Fatalf("write fixture seed: %v", err)
	}
	fixture.mustGit(t, fixture.Checkout, "add", "README.md")
	fixture.mustGit(t, fixture.Checkout, "commit", "--message", "fixture seed")
	fixture.mustGit(t, fixture.Checkout, "push", "--set-upstream", "origin", "main")
	return fixture
}

// Environment returns a copy of the fixture's sanitized subprocess environment.
func (f *GitFixture) Environment() []string { return append([]string(nil), f.env...) }

// Run invokes Git in the fixture checkout with a bounded timeout.
func (f *GitFixture) Run(t testing.TB, args ...string) []byte {
	t.Helper()
	return f.RunIn(t, f.Checkout, args...)
}

// RunIn invokes Git in dir using the same isolated environment.
func (f *GitFixture) RunIn(t testing.TB, dir string, args ...string) []byte {
	t.Helper()
	output, err := f.runGit(dir, args...)
	if err != nil {
		t.Fatalf("git %s in fixture: %v", strings.Join(args, " "), err)
	}
	return output
}

func (f *GitFixture) mustGit(t testing.TB, dir string, args ...string) {
	t.Helper()
	if _, err := f.runGit(dir, args...); err != nil {
		t.Fatalf("initialize Git fixture with %s: %v", strings.Join(args, " "), err)
	}
}

func (f *GitFixture) runGit(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fixtureGitTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	command.Env = f.Environment()
	command.WaitDelay = 250 * time.Millisecond
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("timed out after %s", fixtureGitTimeout)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func hermeticGitEnvironment(home string) []string {
	env := make([]string, 0, len(os.Environ())+8)
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || strings.HasPrefix(key, "GIT_") || key == "HOME" || key == "XDG_CONFIG_HOME" {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Kogen Test Fixture",
		"GIT_AUTHOR_EMAIL=kogen-test-fixture@example.invalid",
		"GIT_COMMITTER_NAME=Kogen Test Fixture",
		"GIT_COMMITTER_EMAIL=kogen-test-fixture@example.invalid",
	)
}
