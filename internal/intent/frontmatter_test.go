package intent

import "testing"

func TestFrontmatterOptionalFieldsAndDefaults(t *testing.T) {
	source := []byte("---\ntitle: T\nsize: huge\ndomains: [app, docs]\nchanges_gate: true\nlimits: [one]\nblocks_on: [base]\npriority: -2\nassumptions: [{name: api, path: api.md, contains: stable}]\nshared_contracts:\n  - name: wire\n    path: wire.md\n    contains: encoding stays fixed\nsource: operator\n---\nBrief.\n")
	parsed, err := Parse("greet", source)
	if err != nil {
		t.Fatal(err)
	}
	fm := parsed.Frontmatter
	if fm.Title != "T" || fm.Size != "huge" || len(fm.Domains) != 2 || !fm.ChangesGate || fm.Priority != -2 {
		t.Fatalf("frontmatter fields = %#v", fm)
	}
	if len(fm.Limits) != 1 || fm.Limits[0] != "one" || len(fm.BlocksOn) != 1 || fm.BlocksOn[0] != "base" {
		t.Fatalf("list fields = %#v", fm)
	}
	if len(fm.Assumptions) != 1 || fm.Assumptions[0] != (Contract{Name: "api", Path: "api.md", Contains: "stable"}) {
		t.Fatalf("assumptions = %#v", fm.Assumptions)
	}
	if len(fm.SharedContracts) != 1 || fm.SharedContracts[0].Contains != "encoding stays fixed" {
		t.Fatalf("shared contracts = %#v", fm.SharedContracts)
	}
	if fm.Source == nil || *fm.Source != "operator" {
		t.Fatalf("source = %#v", fm.Source)
	}
}

func TestFrontmatterDefaults(t *testing.T) {
	parsed, err := Parse("greet", []byte("---\ntitle: T\nsize: small\ndomains: []\n---\nBrief.\n"))
	if err != nil {
		t.Fatal(err)
	}
	fm := parsed.Frontmatter
	if fm.ChangesGate || fm.Priority != 0 || len(fm.Limits) != 0 || len(fm.BlocksOn) != 0 || len(fm.Assumptions) != 0 || len(fm.SharedContracts) != 0 || fm.Source != nil {
		t.Fatalf("unexpected defaults: %#v", fm)
	}
}
