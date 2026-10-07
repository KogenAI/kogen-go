package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kogen-go/internal/contract"
)

func TestSchemaGroupsIndependentProjectIssues(t *testing.T) {
	source := []byte(`name: demo
checks:
  - name: lint
    argv: [sh, lint.sh]
    timeout_ms: 1000
  - name: lint
    argv: [sh, lint.sh]
    timeout_ms: 1000
env: {KOGEN_SANDBOXED: true}
domains: {app: lib}
`)
	_, err := ParseConfig(".kogen/project.yaml", source)
	if err == nil {
		t.Fatal("invalid project config unexpectedly loaded")
	}
	message := err.Error()
	for _, want := range []string{
		`env key "KOGEN_SANDBOXED" is not allowed`,
		"domains.app must be a list of strings",
		`checks has duplicate name "lint"`,
	} {
		if !strings.Contains(message, want) {
			t.Errorf("grouped diagnostics omit %q:\n%s", want, message)
		}
	}
}

func TestProjectSchemaRejectsEscapingInputsAndZeroRungs(t *testing.T) {
	source := []byte("name: demo\nchecks: []\nsetup_inputs: [../secrets]\nbuild:\n  ladder: {max_rungs: 0}\n")
	_, err := ParseConfig("project.yaml", source)
	if err == nil || !strings.Contains(err.Error(), "setup_inputs entries must be safe relative paths") || !strings.Contains(err.Error(), "build.ladder.max_rungs must be an integer from 1 to 4") {
		t.Fatalf("unsafe setup input or zero rungs were accepted: %v", err)
	}
}

func TestDraftAuditorDemotionFixture(t *testing.T) {
	base := "name: demo\nchecks: []\nbuild: {}\n"
	config, err := ParseConfig("project.yaml", []byte(base))
	if err != nil {
		t.Fatalf("default draft config: %v", err)
	}
	if got := LandPolicy(config, nil); got != "green" {
		t.Fatalf("default land policy = %q, want green", got)
	}
	for _, setting := range []string{"auditor_demotion: false", "land: green-or-advisory"} {
		if _, err := ParseConfig("project.yaml", []byte("name: demo\nchecks: []\nbuild: {"+setting+"}\n")); err != nil {
			t.Errorf("compatible draft config %q: %v", setting, err)
		}
	}
	_, err = ParseConfig("project.yaml", []byte("name: demo\nchecks: []\nbuild: {auditor_demotion: true}\n"))
	if err == nil || !strings.Contains(err.Error(), "build.auditor_demotion has no admitted calibration") {
		t.Fatalf("D-AUD-01 did not refuse uncalibrated demotion: %v", err)
	}
}

func TestDraftShapeRoleFixtures(t *testing.T) {
	_, err := ParseConfig("project.yaml", []byte("name: demo\nchecks: []\nbuild:\n  roles:\n    fallback_shaper: {model: gpt-6.1-sol, effort: high}\n"))
	if err == nil || !strings.Contains(err.Error(), `build.roles has unknown role "fallback_shaper"`) {
		t.Fatalf("D-SHAPE-04 accepted explicit alias: %v", err)
	}

	config, err := ParseConfig("project.yaml", []byte("name: demo\nchecks: []\nbuild:\n  roles:\n    shaper: {model: grok-4.6, effort: high}\n"))
	if err != nil {
		t.Fatalf("parse cross-provider fixture: %v", err)
	}
	_, err = ResolveRoles("chatgpt", config, nil)
	if err == nil || !strings.Contains(err.Error(), "selects grok under the chatgpt provider") {
		t.Fatalf("D-SHAPE-05 accepted cross-provider shaper: %v", err)
	}

	grok, err := ResolveRoles("grok", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := grok.FallbackShaper; got != (contract.RoleSettings{Provider: "grok", Model: "grok-4.6", Effort: "high"}) {
		t.Fatalf("Grok fallback alias = %#v", got)
	}
}

func TestRoleMergeIsPerFieldAndFallbackAliasesEffectiveShaper(t *testing.T) {
	project, err := ParseConfig("project.yaml", []byte("name: demo\nchecks: []\nbuild:\n  roles:\n    builder: {model: gpt-6-luna}\n    shaper: {model: gpt-6-luna, effort: medium}\n"))
	if err != nil {
		t.Fatal(err)
	}
	machine, err := ParseMachineConfig("config.yaml", []byte("build:\n  roles:\n    builder: {model: gpt-6.1-sol, effort: low}\n    shaper: {model: gpt-6.1-sol, effort: high}\n"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ResolveRoles("chatgpt", project, machine)
	if err != nil {
		t.Fatal(err)
	}
	if got := manifest.Effective["builder"]; got.Model != "gpt-6-luna" || got.Effort != "low" {
		t.Fatalf("project/machine builder merge = %#v", got)
	}
	if got := manifest.FallbackShaper; got.Model != "gpt-6-luna" || got.Effort != "medium" || got.Provider != "chatgpt" {
		t.Fatalf("fallback alias did not inherit effective shaper: %#v", got)
	}
	defaults, err := ResolveRoles("chatgpt", nil, nil)
	if err != nil || defaults.FallbackShaper.Model != "gpt-6.1-sol" || defaults.FallbackShaper.Effort != "high" {
		t.Fatalf("ChatGPT default shaper alias = %#v, err=%v", defaults.FallbackShaper, err)
	}
}

func TestProjectResolutionUsesCanonicalCheckoutAndPrecedence(t *testing.T) {
	root := t.TempDir()
	checkout := filepath.Join(root, "checkout")
	if err := os.MkdirAll(filepath.Join(checkout, ".kogen"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, ".kogen", "project.yaml"), []byte("name: demo\nchecks: []\nbase: project-base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(checkout, filepath.Join(root, "checkout-link")); err != nil {
		t.Fatal(err)
	}
	git := &resolutionGit{top: checkout}
	resolved, err := Resolve(context.Background(), Options{
		CWD: root, Project: "checkout-link", Base: "cli-base", Home: filepath.Join(root, "home"),
		Git: git, Policy: func(directory string) contract.GitPolicy { return contract.GitPolicy{WorkingDirectory: directory} },
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Checkout != canonical || resolved.Origin != canonical || resolved.Base != "cli-base" {
		t.Fatalf("resolution = %#v", resolved)
	}
	if !strings.HasSuffix(resolved.StateRoot, StateKey(canonical)) {
		t.Fatalf("state root not based on canonical path: %q", resolved.StateRoot)
	}
	if git.calls["rev-parse --verify cli-base^{commit}"] != canonical {
		t.Fatalf("CLI base not verified against origin: %#v", git.calls)
	}

	withoutCLIBase, err := Resolve(context.Background(), Options{
		CWD: root, Project: "checkout-link", Home: filepath.Join(root, "home"),
		Git: git, Policy: func(directory string) contract.GitPolicy { return contract.GitPolicy{WorkingDirectory: directory} },
	})
	if err != nil {
		t.Fatal(err)
	}
	if withoutCLIBase.Base != "project-base" {
		t.Fatalf("project base precedence = %q, want project-base", withoutCLIBase.Base)
	}

	origin := filepath.Join(root, "origin.git")
	if err := os.MkdirAll(origin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(origin, "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	separateOrigin, err := Resolve(context.Background(), Options{
		CWD: root, Project: "checkout-link", Origin: "origin.git", Home: filepath.Join(root, "home"),
		Git: git, Policy: func(directory string) contract.GitPolicy { return contract.GitPolicy{WorkingDirectory: directory} },
	})
	if err != nil {
		t.Fatal(err)
	}
	canonicalOrigin, err := filepath.EvalSymlinks(origin)
	if err != nil {
		t.Fatal(err)
	}
	if separateOrigin.Origin != canonicalOrigin || separateOrigin.Base != "project-base" {
		t.Fatalf("separate origin resolution = %#v", separateOrigin)
	}
	if git.calls["rev-parse --verify project-base^{commit}"] != canonicalOrigin {
		t.Fatalf("project base was not resolved against the origin: %#v", git.calls)
	}
}

type resolutionGit struct {
	top   string
	calls map[string]string
}

func (g *resolutionGit) Exec(_ context.Context, args []string, _ []byte, policy contract.GitPolicy) (contract.GitResult, error) {
	if g.calls == nil {
		g.calls = make(map[string]string)
	}
	key := strings.Join(args, " ")
	g.calls[key] = policy.WorkingDirectory
	var output string
	switch key {
	case "rev-parse --show-toplevel":
		output = g.top
	case "rev-parse --verify cli-base^{commit}", "rev-parse --verify project-base^{commit}":
		output = strings.Repeat("a", 40) + "\n"
	default:
		return contract.GitResult{}, fmt.Errorf("unexpected Git invocation %s", key)
	}
	status := 0
	return contract.GitResult{Process: contract.ProcessResult{ExitStatus: &status}, Stdout: []byte(output)}, nil
}
