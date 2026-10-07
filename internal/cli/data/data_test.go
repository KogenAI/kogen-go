package data

import (
	"io/fs"
	"testing"
)

func TestFrozenCorpusHash(t *testing.T) {
	if err := Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestFrozenHelpAndOverlayAreEmbedded(t *testing.T) {
	for _, name := range []string{
		"constants.json",
		"lint.json",
		"moved.json",
		"yaml-errors.json",
		"help/kogen.txt",
		"v1.2/help/kogen.txt",
	} {
		contents, err := fs.ReadFile(FS(), name)
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		if len(contents) == 0 {
			t.Errorf("embedded corpus entry %s is empty", name)
		}
	}
}
