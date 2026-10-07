// Package commit creates and verifies the single-parent squash commit prepared
// for landing. Candidate content comes from the verified base-relative tree;
// approved Intent and candidate-test bytes are restored exactly at commit
// construction, independently of ignore rules.
package commit
