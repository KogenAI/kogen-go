// Package data embeds the frozen help and normative data corpus used by the
// command implementation. The compatibility overlay remains a separate path.
package data

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
)

// The files come from the frozen v1.2 input snapshot. Keep the manifest hash in
// sync only through a deliberate corpus migration recorded in docs/work/INPUTS.md.
const FrozenCorpusSHA256 = "869c526d0f8e26ae482ddad1ecf47cd04ad038adbda590a5ae29481635e97d40"

//go:embed *.json help v1.2/help
var corpus embed.FS

// FS returns the immutable embedded corpus rooted at its checked-in paths.
func FS() fs.FS { return corpus }

// ReadFile reads a corpus entry using a slash-separated embedded path.
func ReadFile(name string) ([]byte, error) { return corpus.ReadFile(name) }

// Digest hashes all embedded files in lexical path order. Each record is
// encoded as path NUL byte-count colon content NUL, so both names and raw bytes
// (including line endings) are covered without archive metadata.
func Digest() (string, error) {
	var names []string
	if err := fs.WalkDir(corpus, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			names = append(names, name)
		}
		return nil
	}); err != nil {
		return "", err
	}
	sort.Strings(names)

	hash := sha256.New()
	for _, name := range names {
		contents, err := corpus.ReadFile(name)
		if err != nil {
			return "", err
		}
		if _, err := fmt.Fprintf(hash, "%s\x00%d:", name, len(contents)); err != nil {
			return "", err
		}
		if _, err := hash.Write(contents); err != nil {
			return "", err
		}
		if _, err := hash.Write([]byte{0}); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Verify checks that the embedded paths and exact bytes still match the frozen
// manifest. It is called by the component acceptance test.
func Verify() error {
	actual, err := Digest()
	if err != nil {
		return fmt.Errorf("hash embedded corpus: %w", err)
	}
	if actual != FrozenCorpusSHA256 {
		return fmt.Errorf("embedded corpus SHA-256 mismatch: got %s, want %s", actual, FrozenCorpusSHA256)
	}
	return nil
}
