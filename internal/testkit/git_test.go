package testkit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitFixtureUsesLocalIdentityAndHasCleanSeed(t *testing.T) {
	fixture := NewGitFixture(t)
	if got := string(fixture.Run(t, "config", "--local", "--get", "user.name")); got != "Kogen Test Fixture\n" {
		t.Fatalf("fixture identity = %q", got)
	}
	if got := string(fixture.Run(t, "status", "--porcelain=v1")); got != "" {
		t.Fatalf("seed checkout is dirty: %q", got)
	}
	if got := string(fixture.RunIn(t, fixture.Origin, "rev-parse", "refs/heads/main")); got == "" {
		t.Fatal("seed commit was not pushed to the bare origin")
	}
	if got := string(fixture.Run(t, "config", "--local", "--get", "commit.gpgsign")); got != "false\n" {
		t.Fatalf("fixture signing policy = %q", got)
	}
}

func TestGitFixtureIgnoresInheritedGitConfiguration(t *testing.T) {
	global := filepath.Join(t.TempDir(), "global.gitconfig")
	if err := os.WriteFile(global, []byte("[user]\n\tname = Inherited Identity\n\temail = inherited@example.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "user.name")
	t.Setenv("GIT_CONFIG_VALUE_0", "Injected Identity")

	fixture := NewGitFixture(t)
	if got := string(fixture.Run(t, "config", "--local", "--get", "user.name")); got != "Kogen Test Fixture\n" {
		t.Fatalf("ambient Git config affected fixture identity: %q", got)
	}
	if got := string(fixture.Run(t, "config", "--global", "--list")); got != "" {
		t.Fatalf("fixture exposed global Git config: %q", got)
	}
}
