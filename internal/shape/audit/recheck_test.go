package audit

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"kogen-go/internal/intent"
	"kogen-go/internal/safefs"
)

type fakeDependencies struct {
	landed map[string]string
	seen   []string
}

func (f *fakeDependencies) LandedOnBranch(_ context.Context, branch, slug string) (string, bool, error) {
	f.seen = append(f.seen, branch+":"+slug)
	commit, ok := f.landed[slug]
	return commit, ok, nil
}

func TestRecheckBindsPredicatesToBaseAndRequiresLandedDependencies(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(baseDir, "api.md"), []byte("The wire contract remains stable."), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := (safefs.Opener{}).OpenRoot(baseDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.(*safefs.Root).Close()
	deps := &fakeDependencies{landed: map[string]string{"schema-base": "abc123"}}
	options := RecheckOptions{
		Base: "base-tree-sha", Branch: "main", Root: root,
		Predicates: []intent.Contract{{Name: "wire", Path: "api.md", Contains: "wire contract remains stable"}},
		BlocksOn:   []string{"schema-base"}, Deps: deps,
	}
	result, err := Recheck(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Stale) != 0 || len(result.Predicates) != 1 || !result.Predicates[0].Matched || len(result.Dependencies) != 1 || result.Dependencies[0].Commit != "abc123" {
		t.Fatalf("matching recheck = %#v", result)
	}
	if !result.CanBuild() {
		t.Fatalf("matching recheck blocks the Build: %#v", result.Stale)
	}
	if result.Base != "base-tree-sha" || len(deps.seen) != 1 || deps.seen[0] != "main:schema-base" {
		t.Fatalf("base or dependency branch was not bound: result=%#v seen=%#v", result, deps.seen)
	}

	options.Predicates[0].Contains = "new contract text"
	options.Predicates = append(options.Predicates, intent.Contract{Name: "missing", Path: "missing.md", Contains: "present"})
	options.BlocksOn = []string{"schema-base", "not-landed"}
	result, err = Recheck(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Stale) != 3 || result.Stale[0].Name != "wire" || result.Stale[1].Name != "missing" || result.Stale[2].Name != "not-landed" {
		t.Fatalf("stale predicates/dependencies were not reported: %#v", result.Stale)
	}
	if result.CanBuild() {
		t.Fatal("stale recheck permitted the Build")
	}
}

func TestRecheckRejectsUnsafePredicatePathsAndSkipsEmptyContractSet(t *testing.T) {
	deps := &fakeDependencies{landed: map[string]string{}}
	baseDir := t.TempDir()
	root, err := (safefs.Opener{}).OpenRoot(baseDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.(*safefs.Root).Close()
	result, err := Recheck(context.Background(), RecheckOptions{
		Base: "base", Branch: "main", Root: root, Predicates: []intent.Contract{{Name: "escape", Path: "../outside", Contains: "x"}},
		BlocksOn: []string{"not-landed"}, Deps: deps,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Stale) != 2 || result.Stale[0].Detail != "predicate is incomplete or has an unsafe path" || result.Stale[1].Path != "blocks_on" {
		t.Fatalf("unsafe path/dependency were not stale: %#v", result.Stale)
	}
	if _, err := Recheck(context.Background(), RecheckOptions{}); err != nil {
		t.Fatalf("empty predicate set should not require a root or dependency resolver: %v", err)
	}
}
